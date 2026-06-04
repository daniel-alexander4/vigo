// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Resource reversal — the `revert:` / `on_revert:` lever for non-state types.
//!
//! The non-state resource types have no `state: absent` retraction. Reversal
//! gives a subset of them an undo lever, applied as a decorator over the real
//! executor so the convergence dispatch (`execute_resource`) and `converge`
//! stay untouched: [`wrap_reversible`] swaps each reversible type's executor
//! for a [`ReversibleExecutor`] at agent start, once the persistent state store
//! is open.
//!
//! Two flavors, both triggered by `revert: true` and made idempotent by a
//! persisted `reverted` marker ([`crate::state::reversal`]):
//!
//! - **Group B — value-setters** (`hostname`, `timezone`): capture the live
//!   value at first forward apply (write-once-guarded), restore it on revert.
//!   A missing snapshot at revert fails loud — Vigo never guesses a default.
//! - **Group A — local-command** (`exec`, `backup`, `db_backup`, `replace`):
//!   no snapshot; revert runs the operator's `on_revert:` command locally via
//!   `sh -c`. Publish-time validation guarantees `on_revert:` is present.
//! - **Group A — native-context command** (`ssh_exec`, `powershell`): like the
//!   local-command flavor, but the `on_revert:` inverse runs in the executor's
//!   own context — re-dispatched through the inner executor (remote SSH /
//!   local PowerShell) with idempotency guards stripped — not local `sh -c`.
//!
//! The stanza/policy setters (`cisco_interface`, `junos_interface`,
//! `local_security_policy`, `service_recovery_windows`) are a tracked follow-on
//! — they revert in a structured (multi-field) or remote-device context this
//! build doesn't cover.

use crate::executors::{run_command, Action, Executor, ResourceResult};
use crate::state::reversal::{ReversalCache, ReversalEntry};
use crate::state::StateStore;
use std::collections::HashMap;
use std::sync::Arc;
use std::time::Instant;

/// Group B value-setters → the param the executor reads and the reversal layer
/// restores. `Some` ⇒ this type reverts by snapshot/restore.
pub fn snapshot_target_param(resource_type: &str) -> Option<&'static str> {
    match resource_type {
        "hostname" => Some("hostname"),
        "timezone" => Some("timezone"),
        _ => None,
    }
}

/// Group A local-command types → revert by running `on_revert:` locally via
/// `sh -c`.
pub fn is_command_reversible(resource_type: &str) -> bool {
    matches!(resource_type, "exec" | "backup" | "db_backup" | "replace")
}

/// Group A native-context command types → revert by re-dispatching the inner
/// executor with `command` ← `on_revert:` (idempotency guards stripped), so the
/// inverse runs over the executor's own transport — remote SSH (`ssh_exec`) or
/// local PowerShell (`powershell`) — not `sh -c`.
pub fn is_inner_command_reversible(resource_type: &str) -> bool {
    matches!(resource_type, "ssh_exec" | "powershell")
}

/// Every type that supports `revert:` in this build.
pub fn is_reversible(resource_type: &str) -> bool {
    snapshot_target_param(resource_type).is_some()
        || is_command_reversible(resource_type)
        || is_inner_command_reversible(resource_type)
}

/// Wrap each reversible executor in the map with a [`ReversibleExecutor`].
/// No-op when the state store is unavailable (reversal needs persistence) —
/// the bare executors keep working, only `revert:` is inert.
pub fn wrap_reversible(
    executors: &mut HashMap<String, Box<dyn Executor>>,
    store: Option<Arc<StateStore>>,
) {
    let Some(store) = store else { return };
    let types: Vec<String> = executors
        .keys()
        .filter(|t| is_reversible(t))
        .cloned()
        .collect();
    for t in types {
        if let Some(inner) = executors.remove(&t) {
            let kind = match snapshot_target_param(&t) {
                Some(param) => ReversalKind::Snapshot { param },
                None if is_inner_command_reversible(&t) => ReversalKind::CommandViaInner,
                None => ReversalKind::Command,
            };
            executors.insert(
                t.clone(),
                Box::new(ReversibleExecutor {
                    resource_type: t,
                    inner,
                    store: store.clone(),
                    kind,
                }),
            );
        }
    }
}

enum ReversalKind {
    /// Group B: restore a captured prior value via the executor's target param.
    Snapshot { param: &'static str },
    /// Group A: run the operator's `on_revert:` command locally via `sh -c`.
    Command,
    /// Group A, native context: re-dispatch the inner executor with its
    /// `command` param swapped to `on_revert:` (idempotency guards stripped),
    /// so the inverse runs over the executor's own transport — remote SSH
    /// (`ssh_exec`) or local PowerShell (`powershell`) — not `sh -c`.
    CommandViaInner,
}

/// Decorator that adds reversal behavior around a real executor.
pub struct ReversibleExecutor {
    resource_type: String,
    inner: Box<dyn Executor>,
    store: Arc<StateStore>,
    kind: ReversalKind,
}

impl ReversibleExecutor {
    fn store_key(&self, params: &HashMap<String, String>) -> String {
        let name = params.get("name").map(|s| s.as_str()).unwrap_or("");
        ReversalCache::key(&self.resource_type, name)
    }

    fn read_entry(&self, key: &str) -> Option<ReversalEntry> {
        let rtxn = self.store.read_txn().ok()?;
        self.store.reversal.get(&rtxn, key).ok().flatten()
    }

    fn write_entry(&self, key: &str, entry: &ReversalEntry) {
        if let Ok(mut wtxn) = self.store.write_txn() {
            if self.store.reversal.put(&mut wtxn, key, entry).is_ok() {
                let _ = wtxn.commit();
            }
        }
    }

    fn delete_entry(&self, key: &str) {
        if let Ok(mut wtxn) = self.store.write_txn() {
            if self.store.reversal.delete(&mut wtxn, key).is_ok() {
                let _ = wtxn.commit();
            }
        }
    }

    fn result(&self, params: &HashMap<String, String>) -> ResourceResult {
        let name = params.get("name").map(|s| s.as_str()).unwrap_or("");
        ResourceResult::new(&self.resource_type, name)
    }

    /// Honor `revert: true`. Idempotent via the `reverted` marker.
    fn do_revert(
        &self,
        params: &HashMap<String, String>,
        dry_run: bool,
        key: &str,
        entry: Option<ReversalEntry>,
    ) -> ResourceResult {
        let start = Instant::now();

        // Already reverted → settled no-op (this is what keeps `revert: true`
        // from flapping red every cycle once honored).
        if entry.as_ref().map(|e| e.reverted).unwrap_or(false) {
            let mut r = self.result(params);
            r.duration_ms = start.elapsed().as_millis() as i32;
            return r;
        }

        match &self.kind {
            ReversalKind::Snapshot { param } => {
                let Some(snapshot) = entry.and_then(|e| e.snapshot) else {
                    // Fail loud — Vigo never guesses a prior value.
                    let mut r = self.result(params);
                    r.error = Some(format!(
                        "cannot revert {}: no prior value was captured by this agent \
                         (snapshot absent — resource was never applied here, or agent \
                         state was rebuilt). Set the desired value explicitly instead.",
                        self.resource_type
                    ));
                    r.duration_ms = start.elapsed().as_millis() as i32;
                    return r;
                };

                // Restore by re-applying the executor with the target param
                // swapped to the snapshot value.
                let mut restore_params = params.clone();
                restore_params.insert((*param).to_string(), snapshot);
                let mut r = self.inner.execute(&restore_params, dry_run);
                if r.error.is_none() && !dry_run {
                    self.write_entry(key, &ReversalEntry { snapshot: restore_params.get(*param).cloned(), reverted: true });
                }
                r.duration_ms = start.elapsed().as_millis() as i32;
                r
            }
            ReversalKind::Command | ReversalKind::CommandViaInner => {
                let cmd = params.get("on_revert").map(|s| s.as_str()).unwrap_or("");
                let mut r = self.result(params);
                if cmd.is_empty() {
                    r.error = Some(format!(
                        "cannot revert {}: on_revert command is required",
                        self.resource_type
                    ));
                    r.duration_ms = start.elapsed().as_millis() as i32;
                    return r;
                }
                if dry_run {
                    r.action = Action::Updated;
                    r.changed = true;
                    r.duration_ms = start.elapsed().as_millis() as i32;
                    return r;
                }
                if let ReversalKind::CommandViaInner = &self.kind {
                    // Re-run the inner executor with `command` ← on_revert and
                    // the idempotency guards stripped, so the inverse runs over
                    // the executor's own transport (remote SSH / local
                    // PowerShell) and runs unconditionally.
                    let mut revert_params = params.clone();
                    revert_params.insert("command".to_string(), cmd.to_string());
                    revert_params.remove("onlyif");
                    revert_params.remove("unless");
                    let mut ir = self.inner.execute(&revert_params, false);
                    if ir.error.is_none() {
                        self.write_entry(key, &ReversalEntry { snapshot: None, reverted: true });
                    }
                    ir.duration_ms = start.elapsed().as_millis() as i32;
                    return ir;
                }
                match run_command("sh", &["-c", cmd]) {
                    Ok(o) if o.status.success() => {
                        r.action = Action::Updated;
                        r.changed = true;
                        self.write_entry(key, &ReversalEntry { snapshot: None, reverted: true });
                    }
                    Ok(o) => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        r.error = Some(format!("on_revert command failed: {}", stderr.trim()));
                    }
                    Err(e) => {
                        r.error = Some(format!("on_revert command failed to launch: {}", e));
                    }
                }
                r.duration_ms = start.elapsed().as_millis() as i32;
                r
            }
        }
    }

    /// Forward (non-revert) apply: re-arm a prior revert and capture the
    /// write-once snapshot before delegating to the real executor.
    fn do_forward(
        &self,
        params: &HashMap<String, String>,
        dry_run: bool,
        key: &str,
        entry: Option<ReversalEntry>,
    ) -> ResourceResult {
        if !dry_run {
            match &self.kind {
                ReversalKind::Snapshot { .. } => {
                    let need_capture = match &entry {
                        // Re-arm: a prior revert is being overridden by a fresh
                        // forward apply — re-capture against the current value.
                        Some(e) if e.reverted => true,
                        // Write-once: capture only if we've never captured.
                        Some(e) => e.snapshot.is_none(),
                        None => true,
                    };
                    if need_capture {
                        match self.inner.current_value(params) {
                            Some(v) => self.write_entry(
                                key,
                                &ReversalEntry { snapshot: Some(v), reverted: false },
                            ),
                            // Couldn't read current value — drop any stale entry
                            // so a later cycle retries the capture.
                            None => {
                                if entry.is_some() {
                                    self.delete_entry(key);
                                }
                            }
                        }
                    }
                }
                ReversalKind::Command | ReversalKind::CommandViaInner => {
                    // No snapshot to keep — just clear a spent revert marker.
                    if entry.map(|e| e.reverted).unwrap_or(false) {
                        self.delete_entry(key);
                    }
                }
            }
        }
        self.inner.execute(params, dry_run)
    }
}

impl Executor for ReversibleExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let revert = params.get("revert").map(|s| s == "true").unwrap_or(false);
        let key = self.store_key(params);
        let entry = self.read_entry(&key);
        if revert {
            self.do_revert(params, dry_run, &key, entry)
        } else {
            self.do_forward(params, dry_run, &key, entry)
        }
    }

    fn current_value(&self, params: &HashMap<String, String>) -> Option<String> {
        self.inner.current_value(params)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex;

    /// A stub Group B executor: records what it was asked to apply and reports
    /// a configurable current value.
    struct StubSetter {
        current: Mutex<Option<String>>,
        applied: Mutex<Vec<String>>,
    }
    impl StubSetter {
        fn new(current: Option<&str>) -> Self {
            Self {
                current: Mutex::new(current.map(|s| s.to_string())),
                applied: Mutex::new(Vec::new()),
            }
        }
    }
    impl Executor for StubSetter {
        fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
            let desired = params.get("value").cloned().unwrap_or_default();
            let mut r = ResourceResult::new("value_setter", &desired);
            if !dry_run {
                self.applied.lock().unwrap().push(desired.clone());
                *self.current.lock().unwrap() = Some(desired.clone());
            }
            r.action = Action::Updated;
            r.changed = true;
            r
        }
        fn current_value(&self, _params: &HashMap<String, String>) -> Option<String> {
            self.current.lock().unwrap().clone()
        }
    }

    fn store() -> Arc<StateStore> {
        use std::sync::atomic::{AtomicU64, Ordering};
        static N: AtomicU64 = AtomicU64::new(0);
        let n = N.fetch_add(1, Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!(
            "vigo-reversal-test-{}-{}",
            std::process::id(),
            n
        ));
        let _ = std::fs::remove_dir_all(&path);
        std::fs::create_dir_all(&path).unwrap();
        Arc::new(StateStore::open(&path).unwrap())
    }

    fn rev(inner: Box<dyn Executor>, store: Arc<StateStore>, param: &'static str) -> ReversibleExecutor {
        ReversibleExecutor {
            resource_type: "value_setter".into(),
            inner,
            store,
            kind: ReversalKind::Snapshot { param },
        }
    }

    fn params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    #[test]
    fn captures_prior_value_on_first_forward_apply() {
        let s = store();
        let w = rev(Box::new(StubSetter::new(Some("original"))), s.clone(), "value");
        let p = params(&[("name", "r1"), ("value", "new")]);
        w.execute(&p, false);
        let key = ReversalCache::key("value_setter", "r1");
        let entry = w.read_entry(&key).unwrap();
        assert_eq!(entry.snapshot.as_deref(), Some("original"));
        assert!(!entry.reverted);
    }

    #[test]
    fn capture_is_write_once() {
        let s = store();
        let stub = StubSetter::new(Some("original"));
        let w = rev(Box::new(stub), s.clone(), "value");
        let p = params(&[("name", "r1"), ("value", "new")]);
        w.execute(&p, false); // captures "original", applies "new"
        w.execute(&p, false); // second apply must NOT overwrite snapshot
        let key = ReversalCache::key("value_setter", "r1");
        assert_eq!(w.read_entry(&key).unwrap().snapshot.as_deref(), Some("original"));
    }

    #[test]
    fn revert_restores_prior_value_and_marks() {
        let s = store();
        let w = rev(Box::new(StubSetter::new(Some("original"))), s.clone(), "value");
        let fwd = params(&[("name", "r1"), ("value", "new")]);
        w.execute(&fwd, false);
        let rv = params(&[("name", "r1"), ("value", "new"), ("revert", "true")]);
        let r = w.execute(&rv, false);
        assert!(r.error.is_none());
        let key = ReversalCache::key("value_setter", "r1");
        let entry = w.read_entry(&key).unwrap();
        assert!(entry.reverted);
        assert_eq!(entry.snapshot.as_deref(), Some("original"));
    }

    #[test]
    fn revert_is_idempotent_noop_when_marked() {
        let s = store();
        let w = rev(Box::new(StubSetter::new(Some("original"))), s.clone(), "value");
        let fwd = params(&[("name", "r1"), ("value", "new")]);
        w.execute(&fwd, false);
        let rv = params(&[("name", "r1"), ("value", "new"), ("revert", "true")]);
        w.execute(&rv, false); // restores
        let r2 = w.execute(&rv, false); // already reverted → settled
        assert_eq!(r2.action, Action::None);
        assert!(!r2.changed);
        assert!(r2.error.is_none());
    }

    #[test]
    fn revert_without_snapshot_fails_loud() {
        let s = store();
        let w = rev(Box::new(StubSetter::new(Some("x"))), s.clone(), "value");
        // Revert with no prior forward apply → no snapshot captured.
        let rv = params(&[("name", "r1"), ("value", "new"), ("revert", "true")]);
        let r = w.execute(&rv, false);
        assert!(r.error.is_some());
        assert!(r.error.as_deref().unwrap().contains("no prior value was captured"));
    }

    #[test]
    fn forward_after_revert_rearms() {
        let s = store();
        let w = rev(Box::new(StubSetter::new(Some("original"))), s.clone(), "value");
        let key = ReversalCache::key("value_setter", "r1");
        let fwd = params(&[("name", "r1"), ("value", "new")]);
        w.execute(&fwd, false); // snapshot "original"
        let rv = params(&[("name", "r1"), ("value", "new"), ("revert", "true")]);
        w.execute(&rv, false); // reverts, current back to "original", marker set
        // Forward again with a new value → re-arm: marker cleared, fresh snapshot.
        let fwd2 = params(&[("name", "r1"), ("value", "newer")]);
        w.execute(&fwd2, false);
        let entry = w.read_entry(&key).unwrap();
        assert!(!entry.reverted, "marker must clear on re-arm");
        assert_eq!(entry.snapshot.as_deref(), Some("original"), "re-capture is the value present at re-arm");
    }

    #[test]
    fn command_revert_runs_on_revert() {
        let s = store();
        let marker = std::env::temp_dir().join(format!("vigo-revtest-{}", std::process::id()));
        let _ = std::fs::remove_file(&marker);
        let w = ReversibleExecutor {
            resource_type: "exec".into(),
            inner: Box::new(StubSetter::new(None)),
            store: s.clone(),
            kind: ReversalKind::Command,
        };
        let rv = params(&[
            ("name", "r1"),
            ("revert", "true"),
            ("on_revert", &format!("touch {}", marker.display())),
        ]);
        let r = w.execute(&rv, false);
        assert!(r.error.is_none(), "unexpected error: {:?}", r.error);
        assert!(marker.exists(), "on_revert command should have run");
        let _ = std::fs::remove_file(&marker);
    }

    #[test]
    fn command_revert_without_on_revert_fails_loud() {
        let s = store();
        let w = ReversibleExecutor {
            resource_type: "exec".into(),
            inner: Box::new(StubSetter::new(None)),
            store: s.clone(),
            kind: ReversalKind::Command,
        };
        let rv = params(&[("name", "r1"), ("revert", "true")]);
        let r = w.execute(&rv, false);
        assert!(r.error.is_some());
        assert!(r.error.as_deref().unwrap().contains("on_revert command is required"));
    }

    /// Records the params it was executed with, so CommandViaInner tests can
    /// assert the re-dispatched params (command swap + guard strip).
    struct RecordingInner {
        last: Mutex<Option<HashMap<String, String>>>,
    }
    impl RecordingInner {
        fn new() -> Self {
            Self { last: Mutex::new(None) }
        }
    }
    // Implemented on the Arc so a test can keep a handle on the recorder while
    // the wrapper owns it as a Box<dyn Executor>.
    impl Executor for Arc<RecordingInner> {
        fn execute(&self, params: &HashMap<String, String>, _dry_run: bool) -> ResourceResult {
            *self.last.lock().unwrap() = Some(params.clone());
            let mut r = ResourceResult::new("recording", "");
            r.action = Action::Updated;
            r.changed = true;
            r
        }
    }

    fn inner_rev(inner: Arc<RecordingInner>, store: Arc<StateStore>) -> ReversibleExecutor {
        ReversibleExecutor {
            resource_type: "ssh_exec".into(),
            inner: Box::new(inner),
            store,
            kind: ReversalKind::CommandViaInner,
        }
    }

    #[test]
    fn inner_command_revert_redispatches_with_command_swapped_and_guards_stripped() {
        let s = store();
        let inner = Arc::new(RecordingInner::new());
        let w = inner_rev(inner.clone(), s.clone());
        let rv = params(&[
            ("name", "r1"),
            ("host", "10.0.0.1"),
            ("username", "admin"),
            ("credential", "secret:net/admin"),
            ("command", "forward-cmd"),
            ("onlyif", "should-be-stripped"),
            ("unless", "should-be-stripped"),
            ("revert", "true"),
            ("on_revert", "undo-cmd"),
        ]);
        let r = w.execute(&rv, false);
        assert!(r.error.is_none(), "unexpected error: {:?}", r.error);
        let dispatched = inner.last.lock().unwrap().clone().expect("inner was executed");
        // command swapped to on_revert; transport params preserved; guards gone.
        assert_eq!(dispatched.get("command").map(String::as_str), Some("undo-cmd"));
        assert_eq!(dispatched.get("host").map(String::as_str), Some("10.0.0.1"));
        assert_eq!(dispatched.get("credential").map(String::as_str), Some("secret:net/admin"));
        assert!(!dispatched.contains_key("onlyif"), "onlyif must be stripped");
        assert!(!dispatched.contains_key("unless"), "unless must be stripped");
        // Marker persisted.
        let key = ReversalCache::key("ssh_exec", "r1");
        assert!(w.read_entry(&key).unwrap().reverted);
    }

    #[test]
    fn inner_command_revert_is_idempotent_and_rearms() {
        let s = store();
        let inner = Arc::new(RecordingInner::new());
        let w = inner_rev(inner.clone(), s.clone());
        let key = ReversalCache::key("ssh_exec", "r1");
        let rv = params(&[
            ("name", "r1"),
            ("command", "fwd"),
            ("revert", "true"),
            ("on_revert", "undo"),
        ]);
        w.execute(&rv, false); // reverts
        assert!(w.read_entry(&key).unwrap().reverted);
        *inner.last.lock().unwrap() = None;
        let r2 = w.execute(&rv, false); // already reverted → settled no-op
        assert_eq!(r2.action, Action::None);
        assert!(inner.last.lock().unwrap().is_none(), "inner must not re-run once reverted");
        // Forward apply re-arms: spent marker cleared.
        let fwd = params(&[("name", "r1"), ("command", "fwd")]);
        w.execute(&fwd, false);
        assert!(w.read_entry(&key).is_none(), "forward apply clears the spent marker");
    }

    #[test]
    fn inner_command_revert_dry_run_does_not_dispatch() {
        let s = store();
        let inner = Arc::new(RecordingInner::new());
        let w = inner_rev(inner.clone(), s.clone());
        let rv = params(&[
            ("name", "r1"),
            ("command", "fwd"),
            ("revert", "true"),
            ("on_revert", "undo"),
        ]);
        let r = w.execute(&rv, true);
        assert!(r.changed);
        assert!(inner.last.lock().unwrap().is_none(), "dry-run must not touch the transport");
    }

    #[test]
    fn inner_command_revert_without_on_revert_fails_loud() {
        let s = store();
        let inner = Arc::new(RecordingInner::new());
        let w = inner_rev(inner, s.clone());
        let rv = params(&[("name", "r1"), ("command", "fwd"), ("revert", "true")]);
        let r = w.execute(&rv, false);
        assert!(r.error.is_some());
        assert!(r.error.as_deref().unwrap().contains("on_revert command is required"));
    }
}
