// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Persistent host power-loss record.
//!
//! Answers one question the server cannot answer for itself: did this host's
//! last session end in a graceful shutdown, or was power removed? A hard cut
//! leaves nothing in any log the agent can trust — `last`/`wtmp` is absent on
//! Ubuntu 26.04 and Debian 13 and stale on the latter — so the agent records
//! the answer itself, on the way down.
//!
//! The record is keyed on the kernel's `boot_id`, never on a derived boot
//! *time*: `uptime.boot_time` is `now - /proc/uptime`, which moves by a second
//! or more across NTP steps and would manufacture phantom events. Keying on
//! `boot_id` also makes the predicate immune to the three in-tree ways the
//! process dies without a signal — `execv` self-update, a release panic
//! (`panic = "abort"`), and systemd's SIGKILL escalation — because none of
//! them change the boot.
//!
//! Durability is LMDB's: the environment is opened with no `NOSYNC`/`WRITEMAP`
//! (see [`crate::state::StateStore::open`]), so a commit fsyncs and the
//! copy-on-write meta page cannot tear. That matters more here than anywhere
//! else in the store — this is the one record whose whole purpose is to
//! survive the power cut it is counting.
//!
//! Three keys rather than one composite: a truncated or missing key degrades
//! to "no record" instead of poisoning the others.

use anyhow::Result;
use heed::types::{Bytes, Str};
use heed::{Database, Env, RoTxn, RwTxn};
use std::mem::MaybeUninit;

/// Rolling window over which events are reported. Older events age out.
pub const WINDOW_SECS: i64 = 30 * 24 * 3600;

/// Hard cap on retained events, independent of the age window. A host that
/// crash-loops inside the window must not grow the record without bound —
/// the production check-in path does not enforce `maxTraitValueBytes`.
pub const MAX_EVENTS: usize = 64;

/// Path the kernel exposes the current boot's identity at.
const BOOT_ID_PATH: &str = "/proc/sys/kernel/random/boot_id";

const KEY_BOOT_ID: &str = "boot_id";
const KEY_CLEAN: &str = "clean";
const KEY_EVENTS: &str = "events";

/// One observed boot-to-boot transition.
///
/// `clean` distinguishes the two, and both are retained: the count of *all*
/// transitions is the denominator that makes a zero numerator meaningful. A
/// host reporting "0 unclean of 9 transitions" is healthy; one reporting
/// "0 of 0" has told you nothing.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct BootEvent {
    /// Unix seconds at which the *new* boot observed the transition. This is
    /// when the event was noticed, not when power was lost — the host was by
    /// definition not running in between.
    pub at: i64,
    /// Whether the previous session marked itself clean before going down.
    pub clean: bool,
}

/// The persisted record for the current boot.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BootRecord {
    /// Kernel `boot_id` of the boot that wrote this record.
    pub boot_id: String,
    /// Set by the shutdown hook once teardown has been requested.
    pub clean: bool,
    /// Retained transitions, oldest first.
    pub events: Vec<BootEvent>,
}

/// What comparing the stored record against the current boot tells us.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Transition {
    /// No record at all — a fresh install. Explicitly *not* an unclean event.
    FirstBoot,
    /// Same boot as the stored record: an agent restart, `execv` self-update,
    /// panic-abort respawn, or `kill -9`. The host did not reboot.
    SameBoot,
    /// The host rebooted and the previous session marked itself clean.
    CleanReboot,
    /// The host rebooted and the previous session never marked itself clean.
    UncleanShutdown,
}

/// Whether the detector can speak at all, and if not, why.
///
/// Reported alongside the counts so a zero is classifiable from the row
/// itself. Without this, "0 unclean shutdowns" and "this detector has never
/// worked" are the same bytes on the wire.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DetectorState {
    /// A record exists; the next transition will be observed.
    Armed,
    /// First run since install — nothing observed yet, counts omitted.
    FirstBoot,
    /// `boot_id` is unreadable; counts omitted entirely.
    Unsupported,
}

impl DetectorState {
    /// Wire form. Stable — the server matches on these strings.
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Armed => "armed",
            Self::FirstBoot => "first_boot",
            Self::Unsupported => "unsupported",
        }
    }
}

/// Read the kernel's boot identity. `None` when unreadable, which is the
/// signal to report [`DetectorState::Unsupported`] rather than a zero.
///
/// Inside a container this returns the *host's* boot_id, so a container
/// restart correctly reads as `SameBoot` — a documented blind spot, not a
/// silent one: container envoys simply never report a transition.
pub fn read_boot_id() -> Option<String> {
    let raw = std::fs::read_to_string(BOOT_ID_PATH).ok()?;
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return None;
    }
    Some(trimmed.to_string())
}

/// Classify the current boot against whatever was stored.
///
/// Conjunctive on `boot_id` by design: a differing clean flag alone must
/// never count, or every `execv` self-update in the fleet reports a power
/// loss.
pub fn classify(stored: Option<&BootRecord>, current_boot_id: &str) -> Transition {
    match stored {
        None => Transition::FirstBoot,
        Some(rec) if rec.boot_id == current_boot_id => Transition::SameBoot,
        Some(rec) if rec.clean => Transition::CleanReboot,
        Some(_) => Transition::UncleanShutdown,
    }
}

/// Bring the retained event list into the window: clamp stamps from the
/// future, drop stamps older than the window, keep the newest [`MAX_EVENTS`].
///
/// Future stamps are clamped rather than dropped because the machines most
/// likely to produce them are the ones this feature is for — an SBC with no
/// RTC, or a host that booted before NTP settled. Dropping would silently
/// discard a real event; clamping keeps it and ages it honestly from now.
pub fn prune_events(events: &mut Vec<BootEvent>, now: i64) {
    for e in events.iter_mut() {
        if e.at > now {
            e.at = now;
        }
    }
    let cutoff = now.saturating_sub(WINDOW_SECS);
    events.retain(|e| e.at >= cutoff);
    events.sort_by_key(|e| e.at);
    if events.len() > MAX_EVENTS {
        let drop = events.len() - MAX_EVENTS;
        events.drain(..drop);
    }
}

/// Unclean events in the retained window.
pub fn unclean_count(events: &[BootEvent]) -> usize {
    events.iter().filter(|e| !e.clean).count()
}

/// Most recent unclean event, as `YYYY-MM-DD`.
///
/// Day granularity is deliberate. On a personal machine a wall-clock stamp
/// records when a named human was sitting at it, and every `viewer` sees the
/// trait; the day is enough to correlate against a change and carries far
/// less about the person.
pub fn last_unclean_date(events: &[BootEvent]) -> Option<String> {
    events
        .iter()
        .filter(|e| !e.clean)
        .map(|e| e.at)
        .max()
        .map(format_date_utc)
}

#[allow(deprecated)]
fn format_date_utc(timestamp: i64) -> String {
    unsafe {
        let t = timestamp as libc::time_t;
        let mut tm = MaybeUninit::<libc::tm>::uninit();
        libc::gmtime_r(&t, tm.as_mut_ptr());
        let tm = tm.assume_init();
        format!("{:04}-{:02}-{:02}", tm.tm_year + 1900, tm.tm_mon + 1, tm.tm_mday)
    }
}

/// Pack events as `[i64 LE][u8 clean]` repeating.
fn encode_events(events: &[BootEvent]) -> Vec<u8> {
    let mut buf = Vec::with_capacity(events.len() * 9);
    for e in events {
        buf.extend_from_slice(&e.at.to_le_bytes());
        buf.push(e.clean as u8);
    }
    buf
}

/// Unpack [`encode_events`]. A trailing partial record is ignored rather than
/// failing the whole read — a half-written tail must not cost the history.
fn decode_events(bytes: &[u8]) -> Vec<BootEvent> {
    bytes
        .chunks_exact(9)
        .filter_map(|c| {
            let at = i64::from_le_bytes(c[..8].try_into().ok()?);
            Some(BootEvent {
                at,
                clean: c[8] != 0,
            })
        })
        .collect()
}

/// What the collector reports, computed once at startup.
///
/// The counts are omitted from the trait entirely when `detector` is not
/// [`DetectorState::Armed`] — a zero that means "never observed" must not be
/// reportable as a zero that means "observed and clean".
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PowerStatus {
    /// Whether the counts below mean anything.
    pub detector: DetectorState,
    /// Unclean shutdowns in the window.
    pub unclean_30d: usize,
    /// All boot transitions in the window — the denominator for the above.
    pub transitions_30d: usize,
    /// Newest unclean event as `YYYY-MM-DD`, if any.
    pub last_unclean_date: Option<String>,
}

impl PowerStatus {
    /// The detector could not run at all.
    fn unsupported() -> Self {
        Self {
            detector: DetectorState::Unsupported,
            unclean_30d: 0,
            transitions_30d: 0,
            last_unclean_date: None,
        }
    }
}

/// Evaluate the previous session, record the current boot, and return what
/// the collector should report.
///
/// Runs exactly once, at startup, before the first check-in. It both reads
/// and re-arms in a single write transaction: re-arming after a `SameBoot`
/// restart is what stops an early `systemctl stop` from masking a power cut
/// later in the same boot.
///
/// `boot_id` and `now` are injected so the whole decision is testable without
/// a real boot.
pub fn evaluate(
    store: &crate::state::StateStore,
    boot_id: Option<String>,
    now: i64,
) -> Result<PowerStatus> {
    let Some(boot_id) = boot_id else {
        return Ok(PowerStatus::unsupported());
    };

    let mut wtxn = store.write_txn()?;
    let stored = store.power.load(&wtxn)?;
    let transition = classify(stored.as_ref(), &boot_id);

    let mut events = stored.map(|r| r.events).unwrap_or_default();
    match transition {
        Transition::FirstBoot | Transition::SameBoot => {}
        Transition::CleanReboot => events.push(BootEvent { at: now, clean: true }),
        Transition::UncleanShutdown => events.push(BootEvent { at: now, clean: false }),
    }
    prune_events(&mut events, now);

    store.power.store(
        &mut wtxn,
        &BootRecord {
            boot_id,
            clean: false,
            events: events.clone(),
        },
    )?;
    wtxn.commit()?;

    Ok(PowerStatus {
        detector: match transition {
            Transition::FirstBoot => DetectorState::FirstBoot,
            _ => DetectorState::Armed,
        },
        unclean_30d: unclean_count(&events),
        transitions_30d: events.len(),
        last_unclean_date: last_unclean_date(&events),
    })
}

/// Mark the current session clean, on the way down.
///
/// Blocking, and deliberately so — it must complete before the runtime is
/// dropped. The caller bounds it with a timeout and exits regardless: leaving
/// the record un-marked costs one false unclean event, while holding the
/// process open past systemd's stop timeout costs a SIGKILL mid-write.
pub fn mark_session_clean(store: &crate::state::StateStore) -> Result<()> {
    let mut wtxn = store.write_txn()?;
    store.power.mark_clean(&mut wtxn)?;
    wtxn.commit()?;
    Ok(())
}

/// LMDB-backed power record.
pub struct PowerCache {
    db: Database<Str, Bytes>,
}

impl PowerCache {
    /// Open (or create) the power database.
    pub fn open(env: &Env, wtxn: &mut RwTxn<'_>) -> Result<Self> {
        let db = env
            .create_database(wtxn, Some("power"))
            .map_err(|e| anyhow::anyhow!("creating power db: {}", e))?;
        Ok(Self { db })
    }

    /// Load the stored record. `None` when no boot identity has been written
    /// — a fresh install, or a wiped state directory.
    pub fn load(&self, rtxn: &RoTxn<'_>) -> Result<Option<BootRecord>> {
        let boot_id = match self.db.get(rtxn, KEY_BOOT_ID)? {
            Some(b) => String::from_utf8_lossy(b).into_owned(),
            None => return Ok(None),
        };
        if boot_id.is_empty() {
            return Ok(None);
        }
        let clean = self.db.get(rtxn, KEY_CLEAN)?.is_some_and(|b| b.first() == Some(&1));
        let events = self.db.get(rtxn, KEY_EVENTS)?.map(decode_events).unwrap_or_default();
        Ok(Some(BootRecord {
            boot_id,
            clean,
            events,
        }))
    }

    /// Write the record for the current boot. Always stores `clean = false`:
    /// the session is only clean once the shutdown hook says so.
    pub fn store(&self, wtxn: &mut RwTxn<'_>, rec: &BootRecord) -> Result<()> {
        self.db.put(wtxn, KEY_BOOT_ID, rec.boot_id.as_bytes())?;
        self.db.put(wtxn, KEY_CLEAN, &[rec.clean as u8])?;
        self.db.put(wtxn, KEY_EVENTS, &encode_events(&rec.events))?;
        Ok(())
    }

    /// Mark the current session clean. Called from the shutdown path, and the
    /// only write that happens after startup.
    pub fn mark_clean(&self, wtxn: &mut RwTxn<'_>) -> Result<()> {
        self.db.put(wtxn, KEY_CLEAN, &[1u8])?;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rec(boot_id: &str, clean: bool) -> BootRecord {
        BootRecord {
            boot_id: boot_id.into(),
            clean,
            events: Vec::new(),
        }
    }

    #[test]
    fn classify_first_boot_is_not_an_event() {
        assert_eq!(classify(None, "abc"), Transition::FirstBoot);
    }

    #[test]
    fn classify_same_boot_id_never_counts() {
        // execv self-update, panic-abort respawn, kill -9, systemctl restart:
        // all leave the record dirty at an unchanged boot_id.
        assert_eq!(classify(Some(&rec("abc", false)), "abc"), Transition::SameBoot);
        assert_eq!(classify(Some(&rec("abc", true)), "abc"), Transition::SameBoot);
    }

    #[test]
    fn classify_clean_reboot() {
        assert_eq!(classify(Some(&rec("abc", true)), "def"), Transition::CleanReboot);
    }

    #[test]
    fn classify_unclean_shutdown() {
        assert_eq!(
            classify(Some(&rec("abc", false)), "def"),
            Transition::UncleanShutdown
        );
    }

    #[test]
    fn prune_drops_only_events_past_the_window() {
        let now = 1_800_000_000;
        let mut ev = vec![
            BootEvent { at: now - WINDOW_SECS - 1, clean: false }, // out
            BootEvent { at: now - WINDOW_SECS, clean: false },     // exactly at the edge: in
            BootEvent { at: now - WINDOW_SECS + 1, clean: false }, // in
        ];
        prune_events(&mut ev, now);
        assert_eq!(ev.len(), 2, "the boundary stamp itself is retained (>= cutoff)");
        assert_eq!(ev[0].at, now - WINDOW_SECS);
    }

    #[test]
    fn prune_clamps_future_stamps_instead_of_dropping_them() {
        // A host with no RTC boots at a wrong clock; the event is real.
        let now = 1_800_000_000;
        let mut ev = vec![BootEvent { at: now + 400 * 24 * 3600, clean: false }];
        prune_events(&mut ev, now);
        assert_eq!(ev.len(), 1);
        assert_eq!(ev[0].at, now);
    }

    #[test]
    fn prune_caps_retained_events_and_keeps_the_newest() {
        let now = 1_800_000_000;
        let mut ev: Vec<BootEvent> = (0..MAX_EVENTS as i64 + 10)
            .map(|i| BootEvent { at: now - i, clean: false })
            .collect();
        prune_events(&mut ev, now);
        assert_eq!(ev.len(), MAX_EVENTS);
        assert_eq!(ev[ev.len() - 1].at, now, "newest survives");
        assert_eq!(ev[0].at, now - (MAX_EVENTS as i64 - 1), "oldest dropped");
    }

    #[test]
    fn unclean_count_ignores_clean_transitions() {
        let ev = vec![
            BootEvent { at: 10, clean: true },
            BootEvent { at: 20, clean: false },
            BootEvent { at: 30, clean: true },
            BootEvent { at: 40, clean: false },
        ];
        assert_eq!(unclean_count(&ev), 2);
        assert_eq!(ev.len(), 4, "the denominator counts every transition");
    }

    #[test]
    fn last_unclean_date_picks_the_newest_unclean_only() {
        // 1757116800 = 2025-09-06, 1757203200 = 2025-09-07
        let ev = vec![
            BootEvent { at: 1_757_116_800, clean: false },
            BootEvent { at: 1_757_203_200, clean: true },
        ];
        assert_eq!(last_unclean_date(&ev).as_deref(), Some("2025-09-06"));
    }

    #[test]
    fn last_unclean_date_none_when_all_clean() {
        let ev = vec![BootEvent { at: 1_757_116_800, clean: true }];
        assert!(last_unclean_date(&ev).is_none());
    }

    #[test]
    fn events_roundtrip_through_the_byte_layout() {
        let ev = vec![
            BootEvent { at: -1, clean: false },
            BootEvent { at: 1_757_116_800, clean: true },
        ];
        assert_eq!(decode_events(&encode_events(&ev)), ev);
    }

    #[test]
    fn decode_ignores_a_torn_trailing_record() {
        // A power cut mid-write is the scenario this feature exists for.
        let mut bytes = encode_events(&[BootEvent { at: 5, clean: true }]);
        bytes.extend_from_slice(&[9, 9, 9]);
        let out = decode_events(&bytes);
        assert_eq!(out.len(), 1);
        assert_eq!(out[0].at, 5);
    }

    #[test]
    fn decode_of_garbage_is_empty_not_a_panic() {
        assert!(decode_events(&[1, 2, 3]).is_empty());
        assert!(decode_events(&[]).is_empty());
    }

    #[test]
    fn store_load_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();
        let record = BootRecord {
            boot_id: "8eb6bfdd-7f55-449b-8522-bdc63a20371b".into(),
            clean: false,
            events: vec![BootEvent { at: 1_757_116_800, clean: false }],
        };

        let mut wtxn = store.write_txn().unwrap();
        store.power.store(&mut wtxn, &record).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        assert_eq!(store.power.load(&rtxn).unwrap().unwrap(), record);
    }

    #[test]
    fn load_is_none_before_anything_is_stored() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();
        let rtxn = store.read_txn().unwrap();
        assert!(store.power.load(&rtxn).unwrap().is_none());
    }

    #[test]
    fn mark_clean_flips_only_the_clean_flag() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();
        let record = BootRecord {
            boot_id: "boot-a".into(),
            clean: false,
            events: vec![BootEvent { at: 7, clean: false }],
        };

        let mut wtxn = store.write_txn().unwrap();
        store.power.store(&mut wtxn, &record).unwrap();
        wtxn.commit().unwrap();

        let mut wtxn = store.write_txn().unwrap();
        store.power.mark_clean(&mut wtxn).unwrap();
        wtxn.commit().unwrap();

        let rtxn = store.read_txn().unwrap();
        let loaded = store.power.load(&rtxn).unwrap().unwrap();
        assert!(loaded.clean);
        assert_eq!(loaded.boot_id, "boot-a");
        assert_eq!(loaded.events, record.events, "history is not disturbed");
    }

    /// Drive `evaluate` through a sequence of (boot_id, now) startups against
    /// one store, optionally marking clean between them, and return the final
    /// status. `steps` is `(boot_id, now, mark_clean_after)`.
    fn run_boots(steps: &[(&str, i64, bool)]) -> PowerStatus {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();
        let mut last = PowerStatus::unsupported();
        for (boot_id, now, clean_after) in steps {
            last = evaluate(&store, Some((*boot_id).into()), *now).unwrap();
            if *clean_after {
                mark_session_clean(&store).unwrap();
            }
        }
        last
    }

    #[test]
    fn evaluate_first_boot_reports_first_boot_and_no_events() {
        let s = run_boots(&[("boot-a", 1_800_000_000, false)]);
        assert_eq!(s.detector, DetectorState::FirstBoot);
        assert_eq!(s.transitions_30d, 0);
        assert_eq!(s.unclean_30d, 0);
        assert!(s.last_unclean_date.is_none());
    }

    #[test]
    fn evaluate_counts_a_hard_cut() {
        // boot-a comes up and is never marked clean; boot-b then starts.
        let s = run_boots(&[
            ("boot-a", 1_800_000_000, false),
            ("boot-b", 1_800_001_000, false),
        ]);
        assert_eq!(s.detector, DetectorState::Armed);
        assert_eq!(s.unclean_30d, 1);
        assert_eq!(s.transitions_30d, 1);
        assert!(s.last_unclean_date.is_some());
    }

    #[test]
    fn evaluate_does_not_count_a_clean_reboot() {
        // This is the assertion that fails if the SIGTERM handler is missing.
        let s = run_boots(&[
            ("boot-a", 1_800_000_000, true),
            ("boot-b", 1_800_001_000, false),
        ]);
        assert_eq!(s.unclean_30d, 0, "a marked-clean session is not a power loss");
        assert_eq!(s.transitions_30d, 1, "but it is still an observed transition");
        assert!(s.last_unclean_date.is_none());
    }

    #[test]
    fn evaluate_ignores_agent_restart_within_one_boot() {
        // execv self-update, panic-abort respawn, kill -9, systemctl restart.
        let s = run_boots(&[
            ("boot-a", 1_800_000_000, false),
            ("boot-a", 1_800_000_500, false),
            ("boot-a", 1_800_000_900, false),
        ]);
        assert_eq!(s.unclean_30d, 0);
        assert_eq!(s.transitions_30d, 0, "the host never rebooted");
    }

    #[test]
    fn evaluate_rearms_after_a_clean_mark_in_the_same_boot() {
        // Marked clean by `systemctl stop`, restarted by Restart=always, then
        // power is cut. The cut must still count.
        let s = run_boots(&[
            ("boot-a", 1_800_000_000, true),
            ("boot-a", 1_800_000_100, false), // restart re-arms
            ("boot-b", 1_800_002_000, false), // power cut between the two
        ]);
        assert_eq!(s.unclean_30d, 1);
    }

    #[test]
    fn evaluate_accumulates_across_the_window_and_keeps_the_denominator() {
        let s = run_boots(&[
            ("boot-a", 1_800_000_000, false),
            ("boot-b", 1_800_001_000, false), // unclean
            ("boot-c", 1_800_002_000, true),  // unclean, then marked clean
            ("boot-d", 1_800_003_000, false), // clean
        ]);
        assert_eq!(s.unclean_30d, 2);
        assert_eq!(s.transitions_30d, 3);
    }

    #[test]
    fn evaluate_ages_events_out_of_the_window() {
        let s = run_boots(&[
            ("boot-a", 1_800_000_000, false),
            ("boot-b", 1_800_001_000, true), // unclean event recorded, then stops cleanly
            ("boot-c", 1_800_001_000 + WINDOW_SECS + 1, false), // clean reboot, past the window
        ]);
        assert_eq!(s.unclean_30d, 0, "the old event aged out");
        assert_eq!(s.transitions_30d, 1, "only the fresh transition remains");
    }

    #[test]
    fn evaluate_unsupported_when_boot_id_is_unreadable() {
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();
        let s = evaluate(&store, None, 1_800_000_000).unwrap();
        assert_eq!(s.detector, DetectorState::Unsupported);
        assert_eq!(s.transitions_30d, 0);
    }

    #[test]
    fn evaluate_always_leaves_the_record_dirty() {
        // Startup re-arms unconditionally; only the shutdown hook marks clean.
        let dir = tempfile::tempdir().unwrap();
        let store = crate::state::StateStore::open(dir.path()).unwrap();
        evaluate(&store, Some("boot-a".into()), 1_800_000_000).unwrap();
        mark_session_clean(&store).unwrap();
        evaluate(&store, Some("boot-a".into()), 1_800_000_100).unwrap();
        let rtxn = store.read_txn().unwrap();
        assert!(!store.power.load(&rtxn).unwrap().unwrap().clean);
    }

    #[test]
    fn detector_state_wire_strings_are_stable() {
        assert_eq!(DetectorState::Armed.as_str(), "armed");
        assert_eq!(DetectorState::FirstBoot.as_str(), "first_boot");
        assert_eq!(DetectorState::Unsupported.as_str(), "unsupported");
    }
}
