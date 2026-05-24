// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Curator helper daemon — the privileged half of the artifact publish path
//! (ADR-024, slice 2).
//!
//! Listens on `/var/run/vigo/curator.sock` (0666 + SO_PEERCRED, exactly like
//! `gitback.sock`). The user-side `vigo swarm curator push` (slice 4) builds and
//! re-signs the [`CatalogEntry`] under its own puddle key, then streams it here
//! with the blob bytes for the `{version, os, arch}` build it carries. The
//! daemon — running as root — does the parts a non-root user can't:
//!
//! 1. Identify the caller via SO_PEERCRED and require their usercrate
//!    `curator: true` flag. The entry's Ed25519 signature is the real "who may
//!    write this artifact" gate (only a holder of that puddle's key can produce
//!    it); this is the orthogonal "which Unix users on this envoy may drive the
//!    publish machinery at all" gate.
//! 2. Cryptographically validate the entry end-to-end (`verify` walks the
//!    ADR-019 delegation chain) and check the named `{version, os, arch}` is in
//!    it with a `blob_sha`/`size` matching the bytes being pushed, under the
//!    per-blob [`crate::swarm::runtime_limits::curator_max_bytes`] cap.
//! 3. Stage the blob into the swarm [`BlobCache`] (content-addressed) and record
//!    a [`Manifest`] CRDT entry with the artifact's `target`, so the existing
//!    filecast `Manager` distributes it to matching swarm-enabled envoys exactly
//!    like a filecast blob.
//! 4. Drop the signed entry as `~<user>/.vigo-curator/<artifact_id>/entry.json.pending`;
//!    the root sentinel reactor ([`super::run_sentinel_reactor`]) re-verifies it
//!    and promotes it to `entry.json`, where the `swarm_curator` trait collector
//!    picks it up → `curatormesh` → the fleet catalog.
//!
//! Wire protocol — one of two verbs per connection:
//!
//! ### `PUSH` — publish a new blob (the version + platform must be in the entry):
//!
//! ```text
//! → PUSH\n
//! → <json header>\n           {"entry": <CatalogEntry>, "version": "1.2.3",
//!                              "os": "linux", "arch": "amd64", "blob_size": N}
//! ← READY\n                   (only after the header validates; otherwise
//!                              ← ERR <msg>\n and the connection closes)
//! → <blob_size bytes>
//! ← OK\n                      (or ← ERR <msg>\n if hashing/staging fails)
//! ```
//!
//! ### `ENTRY` — re-sign an existing artifact without shipping blob bytes:
//!
//! ```text
//! → ENTRY\n
//! → <CatalogEntry JSON>\n
//! ← OK\n                      (or ← ERR <msg>\n)
//! ```
//!
//! The ENTRY verb is the publish path for `tag`/`untag`/`set-recipients`/
//! `set-target`/`rm` (slice 4c): the client re-signs the entry under the
//! same anchor key, the daemon validates + drops it as a `*.pending` for
//! the reactor to promote (LWW). For `set-target`, the daemon also
//! re-records the manifest seed for every referenced blob so the filecast
//! `Manager` re-distributes to the new target on the next pass — without
//! that, target changes wouldn't take effect until the bytes happened to
//! be re-pushed.
//!
//! What slice 2 does **not** do: fan the entry out peer-to-peer (the propagation
//! path through slices 1-3 is the `swarm_curator` trait → check-in →
//! `curatormesh` aggregator → the slice-3 agent catalog cache sourced from the
//! server, like the ADR-022 name map — see `pending.md`), and push the manifest
//! entry into the *server's* authoritative `ManifestStore` for cross-subnet
//! distribution (the server side of slice 3, when `curatormesh` feeds the
//! catalog's blob entries into the store). The local `record_seed` here handles
//! same-LAN gossip; the rest follows.
//!
//! There is no `vigocli` front-end onto this socket yet — the operator + founder
//! CLI surfaces land in slice 4. Slice 2's coverage is the in-process tests at
//! the bottom of this file, which drive [`serve`] directly (the SO_PEERCRED step
//! in [`handle_client`] can't be faked over an in-memory pipe).

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Duration;

use anyhow::{anyhow, bail, Context, Result};
use serde::Deserialize;
use sha2::{Digest, Sha256};
use tokio::io::{AsyncBufReadExt, AsyncReadExt, AsyncWriteExt, BufReader};
use tokio::net::{UnixListener, UnixStream};
use tokio::sync::Semaphore;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use crate::swarm::cache::BlobCache;
use crate::swarm::curator::catalog::{validate_artifact_name, CatalogEntry};
use crate::swarm::curator::state;
use crate::swarm::HELPER_MAX_CONNECTIONS;
use crate::swarm::anchor::envelopes::DELEGATION_MAX_LEN;
use crate::swarm::anchor::peer_uid::peer_uid;
use crate::swarm::manifest::Manifest;
use crate::swarm::policy::UserPolicy;

/// System-wide socket path the daemon binds and `vigo swarm curator` connects
/// to. `/var/run/vigo/` is the shared per-product subdir for system-level helper
/// sockets (the per-user puddle session helper lives under `$XDG_RUNTIME_DIR`
/// instead); we sit alongside `gitback.sock`.
pub const SOCKET_DIR: &str = "/var/run/vigo";
pub const SOCKET_NAME: &str = "curator.sock";

/// Max bytes accepted in the verb line (`PUSH\n`) — tiny.
const MAX_VERB_BYTES: usize = 64;

/// Max bytes accepted in the JSON header line carrying the signed
/// [`CatalogEntry`]. Generous (a catalog entry is versions × platforms + a tag
/// map + ≤5 delegation envelopes — kilobytes, not megabytes, in any sane case)
/// but bounded so a misbehaving client can't drag the daemon's per-connection
/// memory off-bound.
const MAX_PUSH_HEADER_BYTES: usize = 4 * 1024 * 1024;

/// Per-connection negotiation timeout for the verb + header reads. The blob
/// stream is bounded by `blob_size`, not a timeout (a 2 GiB image over a slow
/// link is legitimate).
const NEGOTIATE_TIMEOUT: Duration = Duration::from_secs(60);

/// `record_seed`'s retention slot for an artifact blob: empty ⇒ never
/// auto-expired by the retention pruner (which only expires entries with a
/// positive retention). Curator artifacts are static and long-lived; a
/// publisher revokes them explicitly (`rm`, slice 4) rather than on a timer.
const ARTIFACT_RETENTION: &str = "";

/// Read-buffer size for streaming the blob from the socket to the staging file.
const STREAM_CHUNK: usize = 64 * 1024;

/// Sequence so concurrent PUSHes in this process don't collide on a staging path.
static STAGE_SEQ: AtomicU64 = AtomicU64::new(0);

pub fn socket_path() -> PathBuf {
    if let Some(p) = std::env::var_os("VIGO_CURATOR_SOCKET_PATH") {
        return PathBuf::from(p);
    }
    Path::new(SOCKET_DIR).join(SOCKET_NAME)
}

/// The resolved Unix caller, built by [`handle_client`] from SO_PEERCRED →
/// passwd → usercrate-policy. Passed to [`serve`] so the request logic stays
/// testable without faking SO_PEERCRED or `/var/lib/vigo` paths.
struct Caller {
    username: String,
    home: PathBuf,
    policy: UserPolicy,
}

/// The JSON header line the client sends after `PUSH\n`: the full re-signed
/// catalog entry, which `{version, os, arch}` build the following bytes are, and
/// how many bytes follow.
#[derive(Debug, Deserialize)]
struct PushHeader {
    entry: CatalogEntry,
    version: String,
    os: String,
    arch: String,
    blob_size: u64,
}

/// Run the curator helper daemon until `cancel` fires. Binds the socket and
/// serves each connection in a detached task. `envoy_id` is this envoy's
/// enrollment id (the manifest seeder id); `manifest` is the shared swarm
/// manifest CRDT.
pub async fn run(envoy_id: String, cancel: CancellationToken, manifest: Arc<Manifest>) -> Result<()> {
    let cache = Arc::new(
        BlobCache::new(crate::platform::paths::default_blobs_dir())
            .context("opening swarm blob cache for curator")?,
    );
    clean_stale_stages(cache.dir());
    let listener = bind_listener()?;
    info!(path = %socket_path().display(), envoy_id = %envoy_id, "curator helper daemon listening");
    let envoy_id = Arc::new(envoy_id);
    let semaphore = Arc::new(Semaphore::new(HELPER_MAX_CONNECTIONS));
    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                info!("curator helper daemon: cancellation requested, shutting down");
                let _ = std::fs::remove_file(socket_path());
                return Ok(());
            }
            accepted = listener.accept() => {
                let (stream, _addr) = match accepted {
                    Ok(v) => v,
                    Err(e) => {
                        debug!(error = %e, "curator helper accept error, continuing");
                        continue;
                    }
                };
                // Bound concurrent client handlers. `acquire_owned().await` queues
                // the accept loop when at saturation (operator-facing local socket;
                // queue-on-full is friendlier than reject-on-full at this volume).
                let permit = match semaphore.clone().acquire_owned().await {
                    Ok(p) => p,
                    Err(_) => {
                        // Semaphore closed — only happens at shutdown.
                        continue;
                    }
                };
                let cache = cache.clone();
                let manifest = manifest.clone();
                let envoy_id = envoy_id.clone();
                tokio::spawn(async move {
                    let _permit = permit; // held for connection lifetime
                    if let Err(e) = handle_client(stream, &cache, &manifest, &envoy_id).await {
                        warn!(error = %e, "curator helper client error");
                    }
                });
            }
        }
    }
}

fn bind_listener() -> Result<UnixListener> {
    let path = socket_path();
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).with_context(|| format!("creating {}", parent.display()))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(parent, std::fs::Permissions::from_mode(0o755));
        }
    }
    let _ = std::fs::remove_file(&path); // stale socket from a crashed daemon
    let std_listener = std::os::unix::net::UnixListener::bind(&path)
        .with_context(|| format!("binding {}", path.display()))?;
    std_listener.set_nonblocking(true).context("set listener non-blocking")?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        // 0666 + SO_PEERCRED: any local user can connect; the daemon identifies
        // them by UID and gates on their usercrate `curator:` flag — same as
        // gitback.sock.
        let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o666));
    }
    UnixListener::from_std(std_listener).context("tokio UnixListener")
}

/// Remove stale `.curator-stage.*.tmp` files left by a daemon that crashed
/// mid-stream (possibly large).
fn clean_stale_stages(blobs_dir: &Path) {
    let Ok(rd) = std::fs::read_dir(blobs_dir) else { return };
    for ent in rd.flatten() {
        let name = ent.file_name();
        let name = name.to_string_lossy();
        if name.starts_with(".curator-stage.") && name.ends_with(".tmp") {
            let _ = std::fs::remove_file(ent.path());
        }
    }
}

async fn handle_client(
    stream: UnixStream,
    cache: &BlobCache,
    manifest: &Manifest,
    envoy_id: &str,
) -> Result<()> {
    let uid = peer_uid(&stream).ok_or_else(|| anyhow!("could not determine peer UID via SO_PEERCRED"))?;
    let caller = match resolve_caller(uid) {
        Ok(c) => c,
        Err(e) => {
            let (_r, mut w) = stream.into_split();
            let _ = w.write_all(format!("ERR {}\n", sanitize_err(&format!("{e:#}"))).as_bytes()).await;
            let _ = w.flush().await;
            return Err(e);
        }
    };

    let (read_half, mut write_half) = stream.into_split();
    let mut br = BufReader::new(read_half);
    let result = serve(&mut br, &mut write_half, &caller, cache, manifest, envoy_id).await;
    match &result {
        Ok(()) => {
            let _ = write_half.write_all(b"OK\n").await;
        }
        Err(e) => {
            let _ = write_half
                .write_all(format!("ERR {}\n", sanitize_err(&format!("{e:#}"))).as_bytes())
                .await;
        }
    }
    let _ = write_half.flush().await;
    result
}

fn resolve_caller(uid: u32) -> Result<Caller> {
    let (username, home) = state::passwd_entry_for_uid(uid)
        .with_context(|| format!("resolving uid {uid} to a passwd entry"))?;
    let policy = crate::swarm::policy::read_user_policy(&username)
        .with_context(|| format!("reading usercrate policy for {username}"))?;
    Ok(Caller { username, home, policy })
}

/// Serve one connection — dispatches on the verb line. Returns `Ok(())` once
/// the requested operation succeeded; the caller writes the final `OK\n`. On
/// the error path the caller writes `ERR <msg>\n`.
async fn serve<R, W>(
    br: &mut BufReader<R>,
    write: &mut W,
    caller: &Caller,
    cache: &BlobCache,
    manifest: &Manifest,
    envoy_id: &str,
) -> Result<()>
where
    R: tokio::io::AsyncRead + Unpin,
    W: tokio::io::AsyncWrite + Unpin,
{
    // --- authorization (the user-layer gate; the entry signature is the rest).
    if !caller.policy.curator {
        bail!(
            "user {} is not authorized to use curator on this envoy \
             (set `curator: true` on the user resource in the usercrate)",
            caller.username
        );
    }

    // --- verb.
    let verb = read_line(br, MAX_VERB_BYTES).await.context("reading verb line")?;
    let verb = verb.trim();
    match verb {
        "PUSH" => serve_push(br, write, caller, cache, manifest, envoy_id).await,
        "ENTRY" => serve_entry(br, caller, manifest, envoy_id).await,
        other => bail!("unknown verb {other:?} (expected PUSH or ENTRY)"),
    }
}

/// PUSH — publish a new blob. The catalog entry must already carry the
/// `{version, os, arch}` whose bytes are being streamed. See module docs.
async fn serve_push<R, W>(
    br: &mut BufReader<R>,
    write: &mut W,
    caller: &Caller,
    cache: &BlobCache,
    manifest: &Manifest,
    envoy_id: &str,
) -> Result<()>
where
    R: tokio::io::AsyncRead + Unpin,
    W: tokio::io::AsyncWrite + Unpin,
{
    // --- header: the signed catalog entry + which build the bytes are.
    let header_line = read_line(br, MAX_PUSH_HEADER_BYTES).await.context("reading PUSH header")?;
    let PushHeader { entry, version, os, arch, blob_size } =
        serde_json::from_str(header_line.trim()).context("parsing PUSH header JSON")?;

    // --- validate the entry end-to-end (signature + ADR-019 delegation chain).
    entry.verify(DELEGATION_MAX_LEN).context("pushed catalog entry failed verification")?;
    validate_artifact_name(&entry.body.name).context("pushed catalog entry artifact name")?;
    let artifact_id_hex = entry.artifact_id_hex().context("deriving artifact_id")?;

    // --- the named build must be in the entry, with a sha/size matching the bytes.
    let av = entry
        .body
        .versions
        .get(&version)
        .ok_or_else(|| anyhow!("version {version:?} is not in the catalog entry"))?;
    let platform = av
        .platforms
        .iter()
        .find(|p| p.os == os && p.arch == arch)
        .ok_or_else(|| anyhow!("platform {os}/{arch} is not in version {version:?} of the entry"))?;
    if platform.size != blob_size {
        bail!(
            "blob_size {blob_size} disagrees with the entry's recorded size {} for {os}/{arch} @ {version}",
            platform.size
        );
    }
    let expected_sha = platform.blob_sha.to_ascii_lowercase();
    if expected_sha.len() != 64 || !expected_sha.bytes().all(|b| b.is_ascii_hexdigit()) {
        bail!("entry's blob_sha for {os}/{arch} @ {version} is not a sha256 hex string");
    }
    let cap = crate::swarm::runtime_limits::curator_max_bytes();
    if blob_size > cap {
        bail!("artifact blob is {blob_size} bytes, over the curator max_bytes cap of {cap}");
    }
    cache.check_disk_space(blob_size).context("staging artifact blob")?;

    // --- everything that can be checked from the header checked out: ask for the bytes.
    write.write_all(b"READY\n").await.context("sending READY")?;
    write.flush().await.context("flushing READY")?;

    // --- stream the bytes to a staging file, hashing as we go.
    let stage = cache.dir().join(format!(
        ".curator-stage.{}.{}.tmp",
        std::process::id(),
        STAGE_SEQ.fetch_add(1, Ordering::Relaxed)
    ));
    let got_sha = match stream_to_file(br, &stage, blob_size).await {
        Ok(s) => s,
        Err(e) => {
            let _ = tokio::fs::remove_file(&stage).await;
            return Err(e).context("streaming artifact blob");
        }
    };
    if got_sha != expected_sha {
        let _ = tokio::fs::remove_file(&stage).await;
        bail!("artifact blob hashes to {got_sha}, but the entry says {expected_sha}");
    }

    // --- promote into the content-addressed cache (or drop if already held).
    if cache.has_blob(&expected_sha) {
        let _ = tokio::fs::remove_file(&stage).await;
    } else {
        let dest = cache.blob_path(&expected_sha);
        tokio::fs::rename(&stage, &dest)
            .await
            .with_context(|| format!("moving staged blob into {}", dest.display()))?;
    }

    // --- record the manifest CRDT entry so the filecast Manager distributes it.
    let label = format!("curator:{artifact_id_hex}:{version}:{os}-{arch}");
    let address = format!(
        "{}:{}",
        crate::swarm::discovery::local_ipv4().unwrap_or(std::net::Ipv4Addr::LOCALHOST),
        crate::swarm::server::DEFAULT_PORT
    );
    manifest.record_seed(
        &expected_sha,
        &label,
        &entry.body.target,
        ARTIFACT_RETENTION,
        envoy_id,
        blob_size,
        &address,
    );

    // --- drop the signed entry for the sentinel reactor to validate + promote.
    write_pending_entry(&caller.home, &artifact_id_hex, &entry)
        .with_context(|| format!("writing pending catalog entry for {}", caller.username))?;

    info!(
        user = %caller.username,
        artifact_id = %artifact_id_hex,
        name = %entry.body.name,
        version = %version,
        platform = %format!("{os}/{arch}"),
        sha = %&expected_sha[..12],
        bytes = blob_size,
        "curator: published artifact build"
    );
    Ok(())
}

/// ENTRY — re-sign without shipping new bytes. Used by tag / untag /
/// set-recipients / set-target / rm. Validates the signed entry end-to-end,
/// drops it as `*.pending` for the reactor's LWW promotion, and (for blobs
/// the entry references that the manifest already knows about) re-records
/// the seed so the new `target` glob takes effect for filecast
/// redistribution on the next Manager pass. Doesn't touch the blob cache —
/// the bytes were staged by a prior PUSH (or aren't ours).
async fn serve_entry<R>(
    br: &mut BufReader<R>,
    caller: &Caller,
    manifest: &Manifest,
    envoy_id: &str,
) -> Result<()>
where
    R: tokio::io::AsyncRead + Unpin,
{
    let header_line = read_line(br, MAX_PUSH_HEADER_BYTES)
        .await
        .context("reading ENTRY body")?;
    let entry: CatalogEntry =
        serde_json::from_str(header_line.trim()).context("parsing ENTRY JSON")?;

    entry
        .verify(DELEGATION_MAX_LEN)
        .context("submitted catalog entry failed verification")?;
    validate_artifact_name(&entry.body.name).context("submitted catalog entry artifact name")?;
    let artifact_id_hex = entry.artifact_id_hex().context("deriving artifact_id")?;

    // Re-record any blob this entry still references that's already in the
    // manifest. For `tag`/`untag`/`set-recipients` this is a no-op-shaped
    // overwrite (target/label unchanged); for `set-target` it's the whole
    // point — refresh the manifest's `target` so the filecast Manager
    // redistributes to the new envoy set on its next pass. Blobs the
    // manifest doesn't know about are silently skipped — they belong to a
    // peer publisher and aren't ours to re-record.
    let address = format!(
        "{}:{}",
        crate::swarm::discovery::local_ipv4().unwrap_or(std::net::Ipv4Addr::LOCALHOST),
        crate::swarm::server::DEFAULT_PORT
    );
    let mut refreshed_blobs = 0usize;
    for (version, av) in &entry.body.versions {
        for p in &av.platforms {
            let sha = p.blob_sha.to_ascii_lowercase();
            let existing = match manifest.get_entry(&sha) {
                Some(e) => e,
                None => continue,
            };
            // Only refresh entries this envoy seeded (`seeder_id == envoy_id`)
            // — refreshing a peer's seed entry would silently transfer
            // ownership of a blob we don't actually hold.
            if existing.seeder_id != envoy_id {
                continue;
            }
            let label = format!(
                "curator:{artifact_id_hex}:{}:{}-{}",
                version, p.os, p.arch
            );
            manifest.record_seed(
                &sha,
                &label,
                &entry.body.target,
                ARTIFACT_RETENTION,
                envoy_id,
                p.size,
                &address,
            );
            refreshed_blobs += 1;
        }
    }

    write_pending_entry(&caller.home, &artifact_id_hex, &entry)
        .with_context(|| format!("writing pending catalog entry for {}", caller.username))?;

    info!(
        user = %caller.username,
        artifact_id = %artifact_id_hex,
        name = %entry.body.name,
        target = %entry.body.target,
        recipients = entry.body.recipients.len(),
        versions = entry.body.versions.len(),
        tags = entry.body.tags.len(),
        refreshed_blobs,
        "curator: submitted re-signed catalog entry"
    );
    Ok(())
}

/// Stream exactly `expected` bytes from `br` into a fresh file at `path`,
/// returning the lowercase sha256 hex of what was written. Errors if the stream
/// ends early.
async fn stream_to_file<R>(br: &mut R, path: &Path, expected: u64) -> Result<String>
where
    R: tokio::io::AsyncRead + Unpin,
{
    let mut f = tokio::fs::File::create(path)
        .await
        .with_context(|| format!("creating {}", path.display()))?;
    let mut hasher = Sha256::new();
    let mut buf = vec![0u8; STREAM_CHUNK];
    let mut remaining = expected;
    while remaining > 0 {
        let want = remaining.min(buf.len() as u64) as usize;
        let n = br.read(&mut buf[..want]).await.context("reading blob bytes")?;
        if n == 0 {
            bail!("stream ended after {} of {expected} blob bytes", expected - remaining);
        }
        hasher.update(&buf[..n]);
        f.write_all(&buf[..n]).await.context("writing staged blob")?;
        remaining -= n as u64;
    }
    f.flush().await.context("flushing staged blob")?;
    Ok(hex::encode(hasher.finalize()))
}

/// Write `entry` as `~<home>/.vigo-curator/<artifact_id_hex>/entry.json.pending`
/// (creating the dirs), atomically (temp + rename within the artifact dir), mode
/// 0644 — the root sentinel reactor promotes it to `entry.json` and the
/// `swarm_curator` trait collector reads that.
fn write_pending_entry(home: &Path, artifact_id_hex: &str, entry: &CatalogEntry) -> Result<()> {
    let dir = state::artifact_dir(home, artifact_id_hex);
    std::fs::create_dir_all(&dir).with_context(|| format!("creating {}", dir.display()))?;
    let dest = state::entry_pending_path(home, artifact_id_hex);
    let tmp = dir.join(format!(
        "{}.{}.{}.tmp",
        state::ENTRY_PENDING_FILE,
        std::process::id(),
        STAGE_SEQ.fetch_add(1, Ordering::Relaxed)
    ));
    let bytes = serde_json::to_vec_pretty(entry).context("serializing catalog entry")?;
    std::fs::write(&tmp, &bytes).with_context(|| format!("writing {}", tmp.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o644));
    }
    std::fs::rename(&tmp, &dest)
        .with_context(|| format!("renaming {} -> {}", tmp.display(), dest.display()))?;
    Ok(())
}

async fn read_line<R>(br: &mut BufReader<R>, max: usize) -> Result<String>
where
    R: tokio::io::AsyncRead + Unpin,
{
    let mut line = String::new();
    let n = tokio::time::timeout(NEGOTIATE_TIMEOUT, br.read_line(&mut line))
        .await
        .map_err(|_| anyhow!("negotiation timed out"))?
        .context("read_line")?;
    if n == 0 {
        bail!("client closed the connection before sending a line");
    }
    if line.len() > max {
        bail!("line too long ({} bytes, cap {max})", line.len());
    }
    Ok(line)
}

fn sanitize_err(s: &str) -> String {
    s.replace(['\n', '\r'], " ")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::curator::catalog::{ArtifactVersion, CatalogEntryBody, Platform, KIND_GENERIC};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;
    use std::collections::BTreeMap;
    use tempfile::TempDir;

    // --- scaffolding ------------------------------------------------------

    struct Bed {
        _tmp: TempDir,
        blobs: PathBuf,
        home: PathBuf,
        manifest: Arc<Manifest>,
    }

    impl Bed {
        fn new() -> Self {
            let tmp = TempDir::new().unwrap();
            let root = tmp.path();
            let blobs = root.join("blobs");
            std::fs::create_dir_all(&blobs).unwrap();
            let home = root.join("home");
            std::fs::create_dir_all(&home).unwrap();
            let manifest = Manifest::load(&root.join("manifest-state"));
            Bed { _tmp: tmp, blobs, home, manifest }
        }
        fn cache(&self) -> BlobCache {
            BlobCache::new(self.blobs.clone()).unwrap()
        }
        fn caller(&self, curator: bool) -> Caller {
            Caller {
                username: "dan".into(),
                home: self.home.clone(),
                policy: UserPolicy { puddle: true, gitback: false, curator, poolq: false },
            }
        }
    }

    fn fresh_key() -> SigningKey {
        SigningKey::generate(&mut OsRng)
    }

    fn entry_for(k: &SigningKey, name: &str, blob: &[u8]) -> CatalogEntry {
        let mut versions = BTreeMap::new();
        versions.insert(
            "1.0.0".to_string(),
            ArtifactVersion {
                platforms: vec![Platform {
                    os: "linux".into(),
                    arch: "amd64".into(),
                    blob_sha: hex::encode(Sha256::digest(blob)),
                    size: blob.len() as u64,
                }],
            },
        );
        let mut tags = BTreeMap::new();
        tags.insert("latest".to_string(), "1.0.0".to_string());
        let body = CatalogEntryBody {
            schema_version: 1,
            name: name.into(),
            kind: KIND_GENERIC.into(),
            founder_puddle_pubkey: hex::encode(k.verifying_key().to_bytes()),
            signing_puddle_pubkey: hex::encode(k.verifying_key().to_bytes()),
            versions,
            tags,
            recipients: vec!["*".into()],
            target: "*".into(),
            issued_at: 100,
            delegations: vec![],
        };
        CatalogEntry::issue(k, body).unwrap()
    }

    fn push_request(entry: &CatalogEntry, version: &str, os: &str, arch: &str, blob: &[u8]) -> Vec<u8> {
        let header = serde_json::json!({
            "entry": entry, "version": version, "os": os, "arch": arch, "blob_size": blob.len(),
        });
        let mut out = b"PUSH\n".to_vec();
        out.extend_from_slice(serde_json::to_string(&header).unwrap().as_bytes());
        out.push(b'\n');
        out.extend_from_slice(blob);
        out
    }

    /// Drive `serve` against an in-memory request; returns `(result, response_bytes)`.
    async fn run_serve(bed: &Bed, caller: &Caller, request: &[u8]) -> (Result<()>, Vec<u8>) {
        let cache = bed.cache();
        let mut br = BufReader::new(std::io::Cursor::new(request.to_vec()));
        let mut sink: Vec<u8> = Vec::new();
        let r = serve(&mut br, &mut sink, caller, &cache, &bed.manifest, "envoy-test").await;
        (r, sink)
    }

    // --- tests ------------------------------------------------------------

    #[tokio::test]
    #[serial_test::serial(runtime_limits)]
    async fn happy_path_stages_blob_records_manifest_writes_pending() {
        let bed = Bed::new();
        let caller = bed.caller(true);
        let k = fresh_key();
        let blob = b"the artifact bytes".to_vec();
        let entry = entry_for(&k, "vigo-agent", &blob);
        let aid = entry.artifact_id_hex().unwrap();
        let sha = hex::encode(Sha256::digest(&blob));

        let (r, resp) = run_serve(&bed, &caller, &push_request(&entry, "1.0.0", "linux", "amd64", &blob)).await;
        r.unwrap();
        assert_eq!(resp, b"READY\n", "header validated ⇒ READY before the blob");

        assert!(bed.cache().has_blob(&sha), "blob must be staged into the content-addressed cache");
        let me = bed.manifest.get_entry(&sha).expect("manifest entry recorded");
        assert_eq!(me.target, "*");
        assert!(me.label.starts_with("curator:"), "label was {:?}", me.label);
        assert_eq!(me.seeder_id, "envoy-test");

        let pend = state::entry_pending_path(&bed.home, &aid);
        assert!(pend.exists(), "entry.json.pending must be written for the reactor");
        let written: CatalogEntry = serde_json::from_slice(&std::fs::read(&pend).unwrap()).unwrap();
        assert_eq!(written, entry);
        assert!(!state::entry_path(&bed.home, &aid).exists(), "entry.json is the reactor's job, not the daemon's");
    }

    #[tokio::test]
    async fn refuses_when_user_lacks_curator_flag() {
        let bed = Bed::new();
        let caller = bed.caller(false);
        let k = fresh_key();
        let blob = b"x".to_vec();
        let entry = entry_for(&k, "x", &blob);
        let (r, _) = run_serve(&bed, &caller, &push_request(&entry, "1.0.0", "linux", "amd64", &blob)).await;
        let err = r.unwrap_err();
        assert!(format!("{err:#}").contains("not authorized"), "got: {err:#}");
        assert!(!bed.cache().has_blob(&hex::encode(Sha256::digest(&blob))));
    }

    #[tokio::test]
    async fn rejects_tampered_entry() {
        let bed = Bed::new();
        let caller = bed.caller(true);
        let k = fresh_key();
        let blob = b"y".to_vec();
        let mut entry = entry_for(&k, "y", &blob);
        entry.body.name = "yy".into(); // invalidates the signature
        let (r, resp) = run_serve(&bed, &caller, &push_request(&entry, "1.0.0", "linux", "amd64", &blob)).await;
        assert!(format!("{:#}", r.unwrap_err()).contains("verification"));
        assert!(resp.is_empty(), "rejected before READY");
    }

    #[tokio::test]
    #[serial_test::serial(runtime_limits)]
    async fn rejects_blob_sha_mismatch() {
        let bed = Bed::new();
        let caller = bed.caller(true);
        let k = fresh_key();
        let claimed = b"the bytes the entry was signed over".to_vec();
        let entry = entry_for(&k, "z", &claimed);
        // Send *different* bytes of the same length under the same (valid) header.
        let actual = vec![b'!'; claimed.len()];
        let header = serde_json::json!({
            "entry": entry, "version": "1.0.0", "os": "linux", "arch": "amd64", "blob_size": actual.len(),
        });
        let mut req = b"PUSH\n".to_vec();
        req.extend_from_slice(serde_json::to_string(&header).unwrap().as_bytes());
        req.push(b'\n');
        req.extend_from_slice(&actual);
        let (r, resp) = run_serve(&bed, &caller, &req).await;
        assert!(format!("{:#}", r.unwrap_err()).contains("hashes to"));
        assert_eq!(resp, b"READY\n", "the header was valid ⇒ READY was sent; the bytes then failed");
        assert!(!bed.cache().has_blob(&hex::encode(Sha256::digest(&claimed))));
        assert!(!bed.cache().has_blob(&hex::encode(Sha256::digest(&actual))));
    }

    #[tokio::test]
    async fn rejects_unknown_build() {
        let bed = Bed::new();
        let caller = bed.caller(true);
        let k = fresh_key();
        let blob = b"q".to_vec();
        let entry = entry_for(&k, "q", &blob); // only carries linux/amd64 @ 1.0.0
        let (r, _) = run_serve(&bed, &caller, &push_request(&entry, "1.0.0", "darwin", "arm64", &blob)).await;
        assert!(format!("{:#}", r.unwrap_err()).contains("not in version"));
    }

    #[tokio::test]
    #[serial_test::serial(runtime_limits)]
    async fn rejects_oversize_blob() {
        let bed = Bed::new();
        let caller = bed.caller(true);
        let _g = crate::swarm::runtime_limits::install_for_test(&crate::proto::SwarmRuntimeLimits {
            filecast_max_size_bytes: 0,
            filecast_default_retention: String::new(),
            lockbox_max_file_bytes: 0,
            lockbox_envelope_retention_secs: 0,
            gitback_max_bundle_bytes: 0,
            gitback_envelope_retention_secs: 0,
            longdrawer_max_file_bytes: 0,
            curator_max_bytes: 4,
            curator_local_port: 0,
            poolq_max_msg_bytes: 0,
            poolq_retention_secs: 0,
        });
        let k = fresh_key();
        let blob = b"too big for the cap".to_vec();
        let entry = entry_for(&k, "big", &blob);
        let (r, resp) = run_serve(&bed, &caller, &push_request(&entry, "1.0.0", "linux", "amd64", &blob)).await;
        assert!(format!("{:#}", r.unwrap_err()).contains("over the curator max_bytes"));
        assert!(resp.is_empty(), "rejected before READY");
    }

    #[tokio::test]
    async fn rejects_unknown_verb() {
        let bed = Bed::new();
        let caller = bed.caller(true);
        let (r, _) = run_serve(&bed, &caller, b"HOWDY\n").await;
        assert!(format!("{:#}", r.unwrap_err()).contains("unknown verb"));
    }

    // ----- ENTRY verb (slice 4c) -----

    fn entry_request(entry: &CatalogEntry) -> Vec<u8> {
        let mut out = b"ENTRY\n".to_vec();
        out.extend_from_slice(serde_json::to_string(entry).unwrap().as_bytes());
        out.push(b'\n');
        out
    }

    /// Re-issue a signed entry under the same key with a higher `issued_at`
    /// and a tweaked field — what `tag`/`set-target`/etc. produce.
    fn reissue_with<F: FnOnce(&mut CatalogEntryBody)>(k: &SigningKey, base: &CatalogEntry, mutate: F) -> CatalogEntry {
        let mut body = base.body.clone();
        body.issued_at += 1;
        mutate(&mut body);
        CatalogEntry::issue(k, body).unwrap()
    }

    /// Seed the manifest the way a prior PUSH would have, without driving
    /// `serve_push` — sidesteps the shared `runtime_limits` static the
    /// oversize-blob test mutates. Same shape `serve_push` records.
    fn seed_manifest_for(bed: &Bed, seeder_id: &str, entry: &CatalogEntry) {
        for (version, av) in &entry.body.versions {
            for p in &av.platforms {
                let sha = p.blob_sha.to_ascii_lowercase();
                let aid = entry.artifact_id_hex().unwrap();
                let label = format!("curator:{aid}:{version}:{}-{}", p.os, p.arch);
                bed.manifest.record_seed(
                    &sha,
                    &label,
                    &entry.body.target,
                    "",
                    seeder_id,
                    p.size,
                    "127.0.0.1:1531",
                );
            }
        }
    }

    #[tokio::test]
    async fn entry_writes_pending_without_blob() {
        let bed = Bed::new();
        let caller = bed.caller(true);
        let k = fresh_key();
        let blob = b"the artifact bytes".to_vec();
        let initial = entry_for(&k, "vigo-agent", &blob);
        let aid = initial.artifact_id_hex().unwrap();
        let sha = hex::encode(Sha256::digest(&blob));
        seed_manifest_for(&bed, "envoy-test", &initial);
        // Stage the blob (would have been done by a real PUSH).
        std::fs::write(bed.cache().blob_path(&sha), &blob).unwrap();
        // Lay down the canonical entry the reactor would have promoted.
        let dir = state::artifact_dir(&bed.home, &aid);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(state::entry_path(&bed.home, &aid), serde_json::to_vec(&initial).unwrap()).unwrap();

        // ENTRY re-sign (add a tag) — no blob bytes flow.
        let revised = reissue_with(&k, &initial, |b| {
            b.tags.insert("stable".into(), "1.0.0".into());
        });
        let (r, resp) = run_serve(&bed, &caller, &entry_request(&revised)).await;
        r.unwrap();
        assert_eq!(resp, b"", "ENTRY sends no intermediate response — caller writes OK after Ok(())");

        // Pending was written.
        assert!(state::entry_pending_path(&bed.home, &aid).exists());
        let written: CatalogEntry =
            serde_json::from_slice(&std::fs::read(state::entry_pending_path(&bed.home, &aid)).unwrap()).unwrap();
        assert_eq!(written.body.tags.get("stable").map(String::as_str), Some("1.0.0"));
        // Blob stayed put.
        assert!(bed.cache().has_blob(&sha));
    }

    #[tokio::test]
    async fn entry_set_target_refreshes_manifest_for_our_seeded_blobs() {
        let bed = Bed::new();
        let caller = bed.caller(true);
        let k = fresh_key();
        let blob = b"the artifact bytes".to_vec();
        let initial = entry_for(&k, "vigo-agent", &blob);
        let sha = hex::encode(Sha256::digest(&blob));
        seed_manifest_for(&bed, "envoy-test", &initial);
        assert_eq!(bed.manifest.get_entry(&sha).unwrap().target, "*");

        // Re-sign with a narrowed target.
        let revised = reissue_with(&k, &initial, |b| b.target = "us-west1-*".into());
        run_serve(&bed, &caller, &entry_request(&revised)).await.0.unwrap();
        assert_eq!(
            bed.manifest.get_entry(&sha).unwrap().target,
            "us-west1-*",
            "ENTRY with a changed target must refresh the manifest seed so filecast redistributes"
        );
    }

    #[tokio::test]
    async fn entry_does_not_refresh_peer_seeded_blobs() {
        // A peer originally seeded the blob (different envoy_id). Our daemon
        // must NOT silently take ownership when we ENTRY-resubmit the entry
        // pointing at it.
        let bed = Bed::new();
        let caller = bed.caller(true);
        let k = fresh_key();
        let blob = b"peer-seeded".to_vec();
        let initial = entry_for(&k, "vigo-agent", &blob);
        let sha = hex::encode(Sha256::digest(&blob));
        bed.manifest.record_seed(
            &sha,
            "curator:peer-seeded",
            "*",
            "",
            "other-envoy",
            blob.len() as u64,
            "10.0.0.99:1531",
        );
        // ENTRY with our key under a different target.
        let revised = reissue_with(&k, &initial, |b| b.target = "elsewhere-*".into());
        run_serve(&bed, &caller, &entry_request(&revised)).await.0.unwrap();
        let me = bed.manifest.get_entry(&sha).unwrap();
        assert_eq!(me.seeder_id, "other-envoy", "peer-seeded blob must keep its original seeder_id");
        assert_eq!(me.target, "*", "peer-seeded blob target must not be overwritten by our ENTRY");
    }

    #[tokio::test]
    async fn entry_rejects_tampered_resign() {
        let bed = Bed::new();
        let caller = bed.caller(true);
        let k = fresh_key();
        let blob = b"x".to_vec();
        let base = entry_for(&k, "x", &blob);
        let mut tampered = base.clone();
        tampered.body.tags.insert("evil".into(), "1.0.0".into()); // mutates body without re-signing
        let (r, _) = run_serve(&bed, &caller, &entry_request(&tampered)).await;
        assert!(format!("{:#}", r.unwrap_err()).contains("verification"));
    }

    #[tokio::test]
    async fn entry_refuses_when_user_lacks_curator_flag() {
        let bed = Bed::new();
        let caller = bed.caller(false);
        let k = fresh_key();
        let blob = b"x".to_vec();
        let base = entry_for(&k, "x", &blob);
        let revised = reissue_with(&k, &base, |b| b.target = "narrowed".into());
        let (r, _) = run_serve(&bed, &caller, &entry_request(&revised)).await;
        assert!(format!("{:#}", r.unwrap_err()).contains("not authorized"));
    }
}
