// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Helper daemon — root-side Unix socket that proxies
//! `git-receive-pack` / `git-upload-pack` for `gitback://` URLs.
//!
//! ### Why a daemon
//!
//! The unified-substrate gitback layout (ADR-015) keeps every
//! project's bare repo at `/var/lib/vigo/swarm/gitback/projects/
//! <project_id_hex>.git`, mode `0700 root:root`. That permission
//! posture exists to gate **non-root local users** from cloning bare
//! repos that aren't theirs. The user-side `git-remote-gitback` binary
//! (multicall mode of `vigo`) runs as the calling user; without a
//! root-side mediator, it cannot read the bare repo. The helper
//! daemon is that mediator.
//!
//! ### Lifecycle
//!
//! Daemon binds [`socket_path`] on agent startup (under
//! [`spawn`](super::spawn)) and accepts connections forever. Each
//! connection is a fresh `git-remote-gitback` invocation:
//!
//! 1. Client sends a one-line `HELLO <project_id_hex> <service>\n`
//!    handshake. `service` is `git-upload-pack` (fetch) or
//!    `git-receive-pack` (push).
//! 2. Daemon resolves the calling user's UID via SO_PEERCRED, looks
//!    up that user's home in `/etc/passwd`, then loads
//!    `<home>/.vigo-gitback/<project_id_hex>/tokens.json`. Reading
//!    from disk under root authority means the helper never needs to
//!    trust wire-presented bytes for authorization.
//! 3. Daemon verifies the token chain against the URL's project_id
//!    via [`crate::swarm::anchor::tokens::verify_chain`]. It also
//!    rejects any chain that names a token id present in the local
//!    revocation set (see [`super::envelopes::is_token_revoked`]).
//! 4. Daemon writes `OK\n` and spawns `git <service> <repo_path>`
//!    with **piped** stdin/stdout/stderr. Two tokio tasks shovel
//!    bytes between the unix socket and the child's pipes. The
//!    indirection avoids a kernel-level wakeup race in AF_UNIX
//!    where the previous "dup the socket fd as the child's stdio"
//!    pattern lost git's refs advertisement (see commit history
//!    around 0.41.7-0.41.12 for the long road to this layout).
//! 5. When the child exits the daemon aborts the socket→stdin
//!    forwarder, drains the stdout→socket forwarder, and goes back
//!    to accept().
//!
//! ### Cross-platform path
//!
//! The socket lives at [`socket_path`] — `/var/run/vigo/gitback.sock`
//! on every Unix the agent runs on (Linux, macOS, FreeBSD, OpenBSD,
//! NetBSD, illumos). `/var/run` is portable and conventional for
//! root-owned runtime sockets; the parent dir is created mode 0755
//! at daemon startup if absent. Windows is deferred per the
//! Linux-first doctrine — the multicall stub already mirrors that
//! cadence.
//!
//! ### Authorization gate
//!
//! For both fetch and push, holding any non-revoked token in the
//! project's chain is sufficient — admin role is enforced only on
//! mint / revoke (CLI verbs), not on git operations. The chain's
//! root must verify against the URL's project_id; the helper does
//! not consult external state to "decide" which puddle a caller
//! belongs to. Whoever has read access to
//! `~/.vigo-gitback/<pid>/tokens.json` and SO_PEERCRED-matches
//! that user's UID can fetch and push under that puddle's identity.

use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use anyhow::{anyhow, bail, Context, Result};
use tokio::io::{AsyncReadExt, AsyncWriteExt, BufReader};
use tokio::net::{UnixListener, UnixStream};
use tokio::sync::Semaphore;
use tokio_util::sync::CancellationToken;

use super::envelopes::is_token_revoked;
use super::fan_out;
use super::project_state::{
    parse_hex_id, project_repo_path, DelegationChain, ProjectConfig, TokenChain,
};
use crate::swarm::anchor::peer_uid::peer_uid;
use crate::swarm::anchor::tokens::{verify_chain, ID_LEN};
use crate::swarm::HELPER_MAX_CONNECTIONS;
use crate::update::TlsCerts;

/// System-wide socket path the daemon binds and `git-remote-gitback`
/// connects to. `/var/run` is portable across Linux (where it's a
/// symlink to `/run`), macOS (tmpfs, recreated at boot), and the
/// BSDs / illumos. `vigo/` is the per-product subdir; we share it
/// only with future system-level helper sockets — the per-user
/// session helpers live under `$XDG_RUNTIME_DIR/vigo/` instead.
pub const SOCKET_DIR: &str = "/var/run/vigo";
pub const SOCKET_NAME: &str = "gitback.sock";

/// Maximum bytes accepted in the HELLO header line. Far above any
/// plausible legitimate use; tight enough that a misbehaving client
/// can't drag the daemon's per-connection memory off-bound.
const MAX_HEADER_BYTES: usize = 4 * 1024;

/// Per-connection negotiation timeout. After OK the read/write
/// timeouts are removed because git's pkt-line protocol can sit
/// idle on huge fetches.
const NEGOTIATE_TIMEOUT: Duration = Duration::from_secs(30);

/// Recognized service names. Matches the verb git's smart-HTTP
/// transport uses, and what `git-receive-pack` / `git-upload-pack`
/// register themselves as.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Service {
    UploadPack,
    ReceivePack,
}

impl Service {
    fn as_str(self) -> &'static str {
        match self {
            Service::UploadPack => "git-upload-pack",
            Service::ReceivePack => "git-receive-pack",
        }
    }

    fn parse(s: &str) -> Option<Self> {
        match s {
            "git-upload-pack" => Some(Self::UploadPack),
            "git-receive-pack" => Some(Self::ReceivePack),
            _ => None,
        }
    }
}

pub fn socket_path() -> PathBuf {
    if let Some(p) = std::env::var_os("VIGO_GITBACK_SOCKET_PATH") {
        return PathBuf::from(p);
    }
    Path::new(SOCKET_DIR).join(SOCKET_NAME)
}

/// Run the helper daemon until `cancel` fires. Binds the socket,
/// accepts connections forever, and serves each in a detached task.
/// Returns when the cancellation token is triggered.
///
/// `tls` is threaded through so the post-receive-pack fan-out hook
/// can build an mTLS HTTP client to push bundles to peers. When
/// `None`, fan-out is silently skipped — local push still works,
/// just no replication. (Pre-enrollment fixtures hit this path.)
pub async fn run(cancel: CancellationToken, tls: Option<TlsCerts>) -> Result<()> {
    let listener = bind_listener()?;
    tracing::info!(path = %socket_path().display(), "gitback helper daemon listening");
    let tls = std::sync::Arc::new(tls);
    let semaphore = std::sync::Arc::new(Semaphore::new(HELPER_MAX_CONNECTIONS));
    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                tracing::info!("gitback helper daemon: cancellation requested, shutting down");
                let _ = std::fs::remove_file(socket_path());
                return Ok(());
            }
            accepted = listener.accept() => {
                let (stream, _addr) = match accepted {
                    Ok(v) => v,
                    Err(e) => {
                        tracing::debug!(error = %e, "helper accept error, continuing");
                        continue;
                    }
                };
                // Bound concurrent client handlers. `acquire_owned().await` queues
                // the accept loop when at saturation; the operator-facing local
                // socket profile makes queue-on-full friendlier than reject.
                let permit = match semaphore.clone().acquire_owned().await {
                    Ok(p) => p,
                    Err(_) => continue, // semaphore closed — shutdown
                };
                let tls = tls.clone();
                tokio::spawn(async move {
                    let _permit = permit; // held for connection lifetime
                    if let Err(e) = handle_client(stream, tls).await {
                        tracing::warn!(error = %e, "gitback helper client error");
                    }
                });
            }
        }
    }
}

fn bind_listener() -> Result<UnixListener> {
    let path = socket_path();
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)
            .with_context(|| format!("creating {}", parent.display()))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(parent, std::fs::Permissions::from_mode(0o755));
        }
    }
    // Stale-socket cleanup — if the daemon crashed the previous bind
    // is still squatting at the path.
    let _ = std::fs::remove_file(&path);

    let std_listener = std::os::unix::net::UnixListener::bind(&path)
        .with_context(|| format!("binding {}", path.display()))?;
    std_listener
        .set_nonblocking(true)
        .context("set listener non-blocking")?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        // 0666 + SO_PEERCRED: any local user can connect; the daemon
        // identifies them by UID and serves only their own per-user
        // tokens. Stricter perms would lock out non-root users entirely
        // (the very people the helper exists to serve).
        let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o666));
    }
    UnixListener::from_std(std_listener).context("tokio UnixListener")
}

async fn handle_client(
    stream: UnixStream,
    tls: std::sync::Arc<Option<TlsCerts>>,
) -> Result<()> {
    let started = std::time::Instant::now();
    let uid = peer_uid(&stream)
        .ok_or_else(|| anyhow!("could not determine peer UID via SO_PEERCRED"))?;
    tracing::info!(uid, "gitback helper: client accepted");

    let (read_half, mut write_half) = stream.into_split();
    let mut br = BufReader::with_capacity(MAX_HEADER_BYTES, read_half);

    let header = match tokio::time::timeout(NEGOTIATE_TIMEOUT, read_header(&mut br)).await {
        Ok(Ok(s)) => s,
        Ok(Err(e)) => {
            let _ = write_half.write_all(format!("ERR {}\n", sanitize_err(&e.to_string())).as_bytes()).await;
            return Err(e.context("reading HELLO"));
        }
        Err(_) => {
            let _ = write_half.write_all(b"ERR negotiation timeout\n").await;
            bail!("negotiation timed out");
        }
    };

    // ADR-022: `RESOLVE-NAME <puddle-name> <repo-handle>` is a pure
    // lookup — resolve the friendly name → project_id against the
    // locally-cached fleet name map, reply `OK <pid_hex>
    // <founder_pubkey_hex>\n` (or `ERR ...\n`), and close. No git
    // process, no token chain. `git-remote-gitback` sends this when
    // the first URL component isn't 64-char lowercase hex.
    if header
        .split_ascii_whitespace()
        .next()
        .map(|v| v.eq_ignore_ascii_case("RESOLVE-NAME"))
        .unwrap_or(false)
    {
        return match handle_resolve_name(uid, &header) {
            Ok((pid_hex, founder_hex)) => {
                tracing::info!(uid, project_id = %pid_hex, "gitback helper: RESOLVE-NAME ok");
                let _ = write_half
                    .write_all(format!("OK {} {}\n", pid_hex, founder_hex).as_bytes())
                    .await;
                let _ = write_half.flush().await;
                Ok(())
            }
            Err(e) => {
                let _ = write_half
                    .write_all(format!("ERR {}\n", sanitize_err(&e.to_string())).as_bytes())
                    .await;
                let _ = write_half.flush().await;
                Err(e)
            }
        };
    }

    // `STATUS` — a pure read of the envoy-wide gitback project view
    // (the same data `collect_projects` builds for the `gitback`
    // trait on every check-in). The daemon runs as root, so it can
    // see the `0700` bare repos under `projects/`; an unprivileged
    // `vigo swarm gitback status` can't, so it asks us. Reply
    // `OK <compact-json-array>\n` (or `ERR ...\n`), then close. No
    // git process, no token chain — this metadata is the same
    // public tier as the `0644` `gitback-announced.json` cache and
    // the server-bound trait.
    if header
        .split_ascii_whitespace()
        .next()
        .map(|v| v.eq_ignore_ascii_case("STATUS"))
        .unwrap_or(false)
    {
        // `collect_projects` does synchronous filesystem walks (incl.
        // `git for-each-ref` per project) — keep it off the reactor.
        return match tokio::task::spawn_blocking(status_response).await {
            Ok(Ok(json)) => {
                tracing::info!(uid, bytes = json.len(), "gitback helper: STATUS ok");
                let _ = write_half.write_all(format!("OK {}\n", json).as_bytes()).await;
                let _ = write_half.flush().await;
                Ok(())
            }
            Ok(Err(e)) => {
                let _ = write_half
                    .write_all(format!("ERR {}\n", sanitize_err(&e.to_string())).as_bytes())
                    .await;
                let _ = write_half.flush().await;
                Err(e)
            }
            Err(e) => {
                let _ = write_half.write_all(b"ERR status task failed\n").await;
                let _ = write_half.flush().await;
                bail!("status task join error: {}", e);
            }
        };
    }

    let (project_id, service) = match parse_header(&header) {
        Ok(v) => v,
        Err(e) => {
            let _ = write_half.write_all(format!("ERR {}\n", sanitize_err(&e.to_string())).as_bytes()).await;
            return Err(e);
        }
    };
    tracing::info!(
        uid,
        elapsed_ms = started.elapsed().as_millis() as u64,
        project_id = %super::project_state::hex_id(&project_id),
        service = service.as_str(),
        "gitback helper: HELLO parsed"
    );

    let (username, home) = match passwd_entry_for_uid(uid) {
        Ok(v) => v,
        Err(e) => {
            let _ = write_half.write_all(format!("ERR {}\n", sanitize_err(&e.to_string())).as_bytes()).await;
            return Err(e);
        }
    };

    // Usercrate-policy gate (default-deny both layers, ADR-002 model).
    // The crypto token chain proves puddle membership but NOT that this
    // envoy's operator still authorizes this user for gitback — a
    // revoked `gitback: true` flag scrubs only the user-owned
    // ~/.vigo-gitback/, so a restored tokens.json would otherwise still
    // verify. Mirror curator's helper, which gates on the policy flag
    // before any git operation. Read verbs (RESOLVE-NAME/STATUS) are
    // pure lookups handled above and stay ungated.
    if let Err(e) = enforce_gitback_policy(&username) {
        let _ = write_half.write_all(format!("ERR {}\n", sanitize_err(&e.to_string())).as_bytes()).await;
        return Err(e);
    }

    let chain = match authorize(&home, &project_id) {
        Ok(c) => c,
        Err(e) => {
            let _ = write_half.write_all(format!("ERR {}\n", sanitize_err(&e.to_string())).as_bytes()).await;
            return Err(e);
        }
    };

    let gitback_base = crate::platform::paths::default_gitback_dir();
    let repo = project_repo_path(&gitback_base, &project_id);

    if matches!(service, Service::UploadPack) && !repo.exists() {
        let msg = "no bare repo yet — push from a member to seed it first";
        let _ = write_half.write_all(format!("ERR {}\n", msg).as_bytes()).await;
        bail!("{}", msg);
    }
    if matches!(service, Service::ReceivePack) && !repo.exists() {
        // First push: create the bare repo. The push proceeds against
        // an empty repo; ref-policy is enforced by git's own
        // receive-pack hooks (we don't override them for v1).
        if let Err(e) = init_bare(&repo) {
            let _ = write_half
                .write_all(format!("ERR could not init bare repo: {}\n", sanitize_err(&e.to_string())).as_bytes())
                .await;
            return Err(e);
        }
    }
    if matches!(service, Service::UploadPack) {
        // Before serving a clone/fetch, make sure the bare repo's
        // `HEAD` symref names a branch that exists — `git init --bare`
        // leaves it at `master`, so a repo whose only branch is `main`
        // would advertise `symref=HEAD:refs/heads/master` and the
        // client's `git clone` would warn and skip the checkout. No-op
        // when `HEAD` is already valid; this also self-heals repos
        // created before this shipped. (ReceivePack reconciles after
        // the push instead — see the post-receive block below.)
        if let Err(e) = fan_out::set_head_to_default_branch(&repo) {
            tracing::warn!(
                project_id = %super::project_state::hex_id(&project_id),
                error = %e,
                "gitback helper: could not reconcile bare repo HEAD before upload-pack; continuing"
            );
        }
    }

    // Hand off via piped stdio: spawn git with pipe stdin/stdout and
    // shovel bytes between the unix socket and the child's pipes
    // through tokio tasks. This deliberately replaces the prior
    // dup-and-spawn-with-socket-as-stdio pattern: that shape sat over
    // a kernel-level wakeup race in the AF_UNIX layer where
    // receive-pack's refs advertisement never landed in the peer's
    // recv queue. We burned five versions (0.41.7-0.41.11) on
    // surgical fixes to the dup chain (forget, O_NONBLOCK, parent fd
    // close) without moving the deadlock — strace consistently masked
    // the bug, confirming a wakeup race below sendmsg. Pipes have
    // well-understood wakeup semantics; this also lets tokio's
    // epoll-based reader own the socket side, which is more
    // race-resistant than blocking read in git-receive-pack itself.
    // Cost: pack data does an extra userspace copy each direction
    // (no splice fast-path); for control-rate traffic this is noise,
    // and on Linux tokio's copy can reclaim splice-into-pipe later if
    // it becomes a hot spot.
    write_half.write_all(b"OK\n").await.context("writing OK")?;
    write_half.flush().await.ok();

    // Reclaim the read half from the BufReader. The smart protocol
    // doesn't pipeline past HELLO before OK, so the BufReader's
    // internal buffer is empty here — but if a future client ever
    // does pipeline, those bytes belong at the head of git's input
    // stream, not in the socket recv queue (we already consumed them
    // into userspace). Capture and re-inject.
    let leftover = br.buffer().to_vec();
    if !leftover.is_empty() {
        tracing::warn!(
            uid,
            buffered = leftover.len(),
            "gitback helper: client pipelined bytes past HELLO; routing to child stdin"
        );
    }
    let mut read_half = br.into_inner();

    let mut cmd = tokio::process::Command::new("git");
    cmd.arg(short_service(service))
        .arg(&repo)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    let spawn_started = std::time::Instant::now();
    let mut child = cmd.spawn().with_context(|| {
        format!(
            "spawning git {} against {}",
            service.as_str(),
            repo.display()
        )
    })?;
    let child_pid = child.id();
    let mut child_stdin = child
        .stdin
        .take()
        .ok_or_else(|| anyhow!("piped child stdin missing after spawn"))?;
    let mut child_stdout = child
        .stdout
        .take()
        .ok_or_else(|| anyhow!("piped child stdout missing after spawn"))?;
    let mut child_stderr = child
        .stderr
        .take()
        .ok_or_else(|| anyhow!("piped child stderr missing after spawn"))?;
    tracing::info!(
        uid,
        child_pid,
        service = service.as_str(),
        elapsed_ms = started.elapsed().as_millis() as u64,
        spawn_ms = spawn_started.elapsed().as_millis() as u64,
        "gitback helper: git child spawned with piped stdio"
    );

    // Re-inject any pipelined bytes the BufReader absorbed past HELLO.
    if !leftover.is_empty() {
        if let Err(e) = child_stdin.write_all(&leftover).await {
            tracing::warn!(uid, child_pid, error = %e, "gitback helper: writing pipelined bytes to child stdin");
        }
    }

    // Forwarder: socket → child stdin (client commands → git).
    // Loops until the socket reads EOF (client closed) or the child
    // stdin pipe breaks. Dropping child_stdin closes the pipe and
    // propagates EOF to git's read side. Manual loop so we can trace
    // every read/write while diagnosing the 0.41.x deadlock — the
    // tokio::io::copy version was opaque to debugging.
    let to_child = tokio::spawn(async move {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let mut buf = vec![0u8; 16 * 1024];
        let mut total: u64 = 0;
        loop {
            let n = match read_half.read(&mut buf).await {
                Ok(0) => {
                    tracing::info!(uid, child_pid, total, "gitback shovel: socket→stdin EOF");
                    break;
                }
                Ok(n) => n,
                Err(e) => {
                    tracing::warn!(uid, child_pid, total, error = %e, "gitback shovel: socket→stdin read err");
                    break;
                }
            };
            tracing::debug!(uid, child_pid, n, total, "gitback shovel: socket→stdin read");
            if let Err(e) = child_stdin.write_all(&buf[..n]).await {
                tracing::warn!(uid, child_pid, total, error = %e, "gitback shovel: socket→stdin write err");
                break;
            }
            total += n as u64;
        }
        Ok::<u64, std::io::Error>(total)
    });

    // Forwarder: child stdout → socket (git output → client).
    // When git exits, the stdout pipe EOFs and copy returns Ok.
    // Dropping write_half issues SHUT_WR on the unix socket, so the
    // client's next read returns 0 — git's protocol "0000" flush is
    // already through, this just signals the connection is done.
    // Manual loop with tracing — same reason as to_child.
    let to_socket = tokio::spawn(async move {
        use tokio::io::{AsyncReadExt, AsyncWriteExt};
        let mut buf = vec![0u8; 16 * 1024];
        let mut total: u64 = 0;
        loop {
            let n = match child_stdout.read(&mut buf).await {
                Ok(0) => {
                    tracing::info!(uid, child_pid, total, "gitback shovel: stdout→socket EOF");
                    break;
                }
                Ok(n) => n,
                Err(e) => {
                    tracing::warn!(uid, child_pid, total, error = %e, "gitback shovel: stdout→socket read err");
                    break;
                }
            };
            tracing::trace!(uid, child_pid, n, total, "gitback shovel: stdout→socket read");
            if let Err(e) = write_half.write_all(&buf[..n]).await {
                tracing::warn!(uid, child_pid, total, error = %e, "gitback shovel: stdout→socket write err");
                break;
            }
            if let Err(e) = write_half.flush().await {
                tracing::warn!(uid, child_pid, total, error = %e, "gitback shovel: stdout→socket flush err");
                break;
            }
            total += n as u64;
            tracing::trace!(uid, child_pid, n, total, "gitback shovel: stdout→socket wrote+flushed");
        }
        Ok::<u64, std::io::Error>(total)
    });

    // stderr drainer: git's tracing output goes here; we surface it
    // on non-zero exit.
    let stderr_handle = Some(tokio::spawn(async move {
        use tokio::io::AsyncReadExt;
        let mut buf = Vec::new();
        let _ = child_stderr.read_to_end(&mut buf).await;
        buf
    }));

    // Snapshot refs before so we can compute a delta after a push.
    // Capturing here lets us avoid keeping a copy of the bare repo's
    // ref state across the (potentially long) receive-pack run.
    let pre_refs = if matches!(service, Service::ReceivePack) {
        fan_out::snapshot_refs(&repo).ok()
    } else {
        None
    };

    let status = child.wait().await.context("awaiting git child")?;

    // Post-exit cleanup of the shovel tasks. to_socket finishes
    // naturally once the child's stdout pipe EOFs (which it has,
    // because the child exited). to_child may still be parked in a
    // socket read if the client hasn't closed its end — abort
    // unblocks it. abort() on an already-finished task is a no-op.
    to_child.abort();
    let to_child_result = to_child.await;
    let to_socket_result = to_socket.await;
    tracing::info!(
        uid,
        child_pid,
        to_child_result = ?to_child_result.as_ref().map(|r| r.as_ref().map(|n| *n).map_err(|e| e.to_string())),
        to_socket_result = ?to_socket_result.as_ref().map(|r| r.as_ref().map(|n| *n).map_err(|e| e.to_string())),
        "gitback helper: shovel tasks finished"
    );

    let stderr_bytes = if let Some(h) = stderr_handle {
        h.await.unwrap_or_default()
    } else {
        Vec::new()
    };
    let stderr = String::from_utf8_lossy(&stderr_bytes);
    #[cfg(unix)]
    let signal = {
        use std::os::unix::process::ExitStatusExt;
        status.signal()
    };
    #[cfg(not(unix))]
    let signal: Option<i32> = None;
    if status.success() {
        tracing::info!(
            uid,
            child_pid,
            service = service.as_str(),
            exit_code = status.code(),
            elapsed_ms = started.elapsed().as_millis() as u64,
            stderr_bytes = stderr_bytes.len(),
            stderr = %stderr.trim(),
            "gitback helper: git child exited cleanly"
        );
    } else {
        tracing::warn!(
            uid,
            child_pid,
            service = service.as_str(),
            exit_code = status.code(),
            signal,
            elapsed_ms = started.elapsed().as_millis() as u64,
            stderr = %stderr.trim(),
            "gitback helper: git child exited non-zero"
        );
        return Ok(());
    }

    // After a successful receive-pack, fan out to cross-subnet
    // members listed in this user's project.json. LAN auto-discovery
    // is a follow-up slice — for now, only explicitly-named hosts
    // receive pushes. No-op on idempotent push (no ref delta).
    if matches!(service, Service::ReceivePack) {
        if let Some(before) = pre_refs {
            let after = match fan_out::snapshot_refs(&repo) {
                Ok(a) => a,
                Err(e) => {
                    tracing::warn!(
                        project_id = %super::project_state::hex_id(&project_id),
                        error = %e,
                        "gitback helper: post-receive ref snapshot failed; skipping fan-out"
                    );
                    return Ok(());
                }
            };
            if before == after {
                return Ok(());
            }
            // Refs advanced — repoint `HEAD` at a live branch. The
            // first push into a freshly-`git init --bare`d repo lands
            // the branches but leaves `HEAD` at `master`, so a clone
            // from this envoy would skip the checkout. No-op when
            // `HEAD` is already valid. Non-fatal: the push succeeded
            // regardless.
            if let Err(e) = fan_out::set_head_to_default_branch(&repo) {
                tracing::warn!(
                    project_id = %super::project_state::hex_id(&project_id),
                    error = %e,
                    "gitback helper: could not reconcile bare repo HEAD after push; continuing"
                );
            }
            let tls_for_fanout = match tls.as_ref() {
                Some(t) => t.clone(),
                None => {
                    tracing::debug!(
                        project_id = %super::project_state::hex_id(&project_id),
                        "gitback helper: no TLS certs available, fan-out skipped"
                    );
                    return Ok(());
                }
            };
            // Build the bundle + push outside the tokio worker thread —
            // git is synchronous and we don't want to starve the runtime.
            let project_id_owned = project_id;
            let home_owned = home.clone();
            let chain_owned = chain;
            // ADR-019: load the per-user delegation chain alongside
            // tokens.json so the X-Vigo-Chain header carries both
            // and receivers can verify a post-rekey leaf back to
            // genesis.
            let delegations_owned = DelegationChain::load(&home_owned, &project_id_owned)
                .ok()
                .flatten()
                .map(|c| c.delegations)
                .unwrap_or_default();
            let gitback_base = crate::platform::paths::default_gitback_dir();
            tokio::task::spawn_blocking(move || {
                let bundle_path = gitback_base
                    .join("incoming")
                    .join(format!(
                        "{}-fanout-{:032x}.bundle",
                        super::project_state::hex_id(&project_id_owned),
                        rand::random::<u128>(),
                    ));
                let repo_path = project_repo_path(&gitback_base, &project_id_owned);
                if let Err(e) = fan_out::build_bundle(&repo_path, &bundle_path) {
                    tracing::warn!(
                        project_id = %super::project_state::hex_id(&project_id_owned),
                        error = %e,
                        "gitback helper: build_bundle for fan-out failed"
                    );
                    return;
                }
                let (attempted, succeeded) = fan_out::fan_out_bundle(
                    &home_owned,
                    &project_id_owned,
                    &chain_owned,
                    &delegations_owned,
                    &bundle_path,
                    &tls_for_fanout,
                );
                let _ = std::fs::remove_file(&bundle_path);
                tracing::info!(
                    project_id = %super::project_state::hex_id(&project_id_owned),
                    attempted,
                    succeeded,
                    "gitback helper: fan-out swept cross-subnet members"
                );
            });
        }
    }
    Ok(())
}

async fn read_header<R>(reader: &mut BufReader<R>) -> Result<String>
where
    R: tokio::io::AsyncRead + Unpin,
{
    // Read the HELLO line one byte at a time, capping allocation at
    // MAX_HEADER_BYTES. `read_line` would grow the String unbounded on a
    // newline-less stream (the length check only fires AFTER the whole
    // line is buffered) — a pre-auth allocation DoS on the 0666 socket.
    // We can't `.take()` the underlying reader: the same reader carries
    // the git smart-protocol tunnel after the handshake, and a cap there
    // would truncate the tunnel. Reads are buffered, so byte-at-a-time is
    // cheap; the header is a short one-shot line.
    let mut buf = Vec::new();
    loop {
        let mut byte = [0u8; 1];
        let n = reader.read(&mut byte).await.context("read_line")?;
        if n == 0 {
            if buf.is_empty() {
                bail!("client closed connection before HELLO");
            }
            break;
        }
        buf.push(byte[0]);
        if byte[0] == b'\n' {
            break;
        }
        if buf.len() > MAX_HEADER_BYTES {
            bail!("HELLO line too long (>{} bytes)", MAX_HEADER_BYTES);
        }
    }
    String::from_utf8(buf).context("HELLO line is not valid UTF-8")
}

fn parse_header(line: &str) -> Result<([u8; ID_LEN], Service)> {
    let trimmed = line.trim_end_matches(['\n', '\r']);
    let mut it = trimmed.split_ascii_whitespace();
    let verb = it
        .next()
        .ok_or_else(|| anyhow!("empty header line"))?;
    if !verb.eq_ignore_ascii_case("HELLO") {
        bail!("expected HELLO, got {:?}", verb);
    }
    let pid_hex = it
        .next()
        .ok_or_else(|| anyhow!("HELLO missing project_id"))?;
    let service_str = it
        .next()
        .ok_or_else(|| anyhow!("HELLO missing service"))?;
    if it.next().is_some() {
        bail!("HELLO has extra tokens");
    }
    let project_id = parse_hex_id(pid_hex).context("HELLO project_id")?;
    let service =
        Service::parse(service_str).ok_or_else(|| anyhow!("unknown service {:?}", service_str))?;
    Ok((project_id, service))
}

/// Resolve a `RESOLVE-NAME <puddle-name> <repo-handle>` request
/// (ADR-022). Returns `(project_id_hex, founder_pubkey_hex)`. Tries
/// the locally-cached fleet name map first; on a miss, falls back to
/// the *requesting* user's own `~/.vigo-puddle/name-claim.json` so a
/// freshly-`puddle name set` claim resolves from that envoy before it
/// has propagated through the trait → server → re-fetch round trip.
/// Pure lookup — no per-user token chain, no git process. `uid` is the
/// SO_PEERCRED'd caller, used only for that self-claim fallback.
fn handle_resolve_name(uid: u32, header: &str) -> Result<(String, String)> {
    let trimmed = header.trim_end_matches(['\n', '\r']);
    let mut it = trimmed.split_ascii_whitespace();
    let _verb = it.next(); // RESOLVE-NAME — already matched by the caller
    let name = it
        .next()
        .ok_or_else(|| anyhow!("RESOLVE-NAME missing <puddle-name>"))?;
    let handle = it
        .next()
        .ok_or_else(|| anyhow!("RESOLVE-NAME missing <repo-handle>"))?;
    if it.next().is_some() {
        bail!("RESOLVE-NAME has extra tokens");
    }
    crate::swarm::puddle::name::validate_name(name).context("puddle name in gitback:// URL")?;
    if handle.is_empty() {
        bail!("RESOLVE-NAME repo handle must not be empty");
    }

    // A missing/corrupt cache file isn't fatal — fall through to the
    // self-claim fallback below (covers a fresh envoy whose name-map
    // bootstrap hasn't run yet).
    let map = crate::swarm::puddle::names_cache::FleetNameMap::load().unwrap_or_default();
    let verified: Vec<_> = map
        .verified_claims(name)
        .into_iter()
        .map(|(claim, _)| claim)
        .collect();

    use crate::swarm::puddle::name::NameResolve;
    let founder_pk = match crate::swarm::puddle::name::resolve_claims(&verified) {
        NameResolve::Unique(pk) | NameResolve::Superseded(pk) => pk,
        NameResolve::NotFound => {
            if let Some(resolved) = resolve_name_via_local_claim(uid, name, handle) {
                return Ok(resolved);
            }
            bail!(
                "puddle name {:?} is not in this envoy's fleet name map and no local claim \
                 matches it — set it with `vigo swarm puddle name set {}`, or clone \
                 gitback://<project_id>/{} directly",
                name,
                name,
                handle
            )
        }
        NameResolve::Ambiguous(claimants) => {
            let prefixes: Vec<String> = claimants
                .iter()
                .map(|p| {
                    if p.len() > 12 {
                        format!("{}…", &p[..12])
                    } else {
                        p.clone()
                    }
                })
                .collect();
            bail!(
                "puddle name {:?} is claimed by {} puddles ({}); resolution is ambiguous — \
                 clone gitback://<project_id>/{} directly",
                name,
                claimants.len(),
                prefixes.join(", "),
                handle
            );
        }
    };
    let pid = crate::swarm::anchor::tokens::project_id_for(&founder_pk, handle);
    Ok((super::project_state::hex_id(&pid), hex::encode(founder_pk)))
}

/// Build the `STATUS` socket-verb payload: the authoritative
/// envoy-wide gitback project view, as `crate::traits::gitback::
/// collect_projects` computes it for the `gitback` trait on every
/// check-in (per-user `~/.vigo-gitback/` state joined with the
/// root-owned bare repos + envelope store). Compact single-line JSON
/// array of `ProjectEntry` so it fits the socket's one-line response
/// convention. Runs as root inside the daemon, so the bare-repo
/// `exists()` / size / ref checks actually succeed.
fn status_response() -> Result<String> {
    let base = crate::platform::paths::default_gitback_dir();
    let passwd =
        std::fs::read_to_string(crate::traits::gitback::passwd_path()).unwrap_or_default();
    let projects = crate::traits::gitback::collect_projects(&passwd, &base);
    serde_json::to_string(&projects).context("serialize STATUS payload")
}

/// Resolve `<name>` against the requesting user's own
/// `~/.vigo-puddle/name-claim.json` — the helper runs as root and can
/// read it (it's world-readable by construction). Used only when the
/// fleet name map misses, so `gitback://<my-puddle-name>/<repo>`
/// works from the envoy that just ran `puddle name set`. Returns
/// `(project_id_hex, founder_pubkey_hex)` on a verified self-claim,
/// `None` otherwise.
fn resolve_name_via_local_claim(uid: u32, name: &str, handle: &str) -> Option<(String, String)> {
    let home = home_for_uid(uid).ok()?;
    let claim = crate::swarm::puddle::local_name_claim(&home).ok().flatten()?;
    if claim.body.name != name {
        return None;
    }
    claim.verify_signature().ok()?;
    let pk = claim.puddle_pubkey_bytes().ok()?;
    let pid = crate::swarm::anchor::tokens::project_id_for(&pk, handle);
    Some((super::project_state::hex_id(&pid), hex::encode(pk)))
}

/// Enforce the caller's usercrate `gitback` flag before a git operation
/// — the user layer of the two-layer swarm authorization model (ADR-002).
/// Mirrors curator's helper, which gates on `caller.policy.curator`
/// before serving. Reads `/var/lib/vigo/usercrate-policy/<user>.json`; a
/// missing file (user never enrolled in a gitback usercrate) or
/// `gitback: false` denies. This is what makes a flag revocation actually
/// bite: without it, a user whose flag was removed could restore a
/// backed-up ~/.vigo-gitback/tokens.json and resume push/fetch, since the
/// crypto chain alone still verifies.
fn enforce_gitback_policy(username: &str) -> Result<()> {
    // VIGO_USERCRATE_POLICY_DIR overrides the policy dir for test
    // isolation, mirroring VIGO_GITBACK_PASSWD_FILE above; production
    // reads the daemon-canonical POLICY_DIR. The daemon runs as root in a
    // controlled (systemd) environment where these overrides are unset.
    let dir = std::env::var_os("VIGO_USERCRATE_POLICY_DIR")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from(crate::swarm::policy::POLICY_DIR));
    enforce_gitback_policy_at(&dir, username)
}

/// Directory-overridable form of [`enforce_gitback_policy`]. Production
/// uses [`crate::swarm::policy::POLICY_DIR`]; tests pass a tempdir.
fn enforce_gitback_policy_at(dir: &Path, username: &str) -> Result<()> {
    let policy = crate::swarm::policy::read_user_policy_at(dir, username)
        .with_context(|| format!("reading usercrate policy for {username}"))?;
    if !policy.gitback {
        bail!(
            "user {username} is not authorized to use gitback on this envoy \
             (set `gitback: true` on the user resource in the usercrate)"
        );
    }
    Ok(())
}

fn authorize(home: &Path, project_id: &[u8; ID_LEN]) -> Result<TokenChain> {
    // Project must exist locally (founder issued / joined).
    let cfg = ProjectConfig::load(home, project_id)
        .with_context(|| format!("loading project.json for {}", super::project_state::hex_id(project_id)))?
        .ok_or_else(|| anyhow!("no project.json — has this user joined the project?"))?;
    // ADR-019 slice 5: tombstoned projects are archive-only. Refuse
    // both fetch and push at the helper-daemon authorization gate.
    // Operator's path forward is `gitback project re-found <handle>`
    // (mints a fresh project_id and byte-migrates) or `project leave`.
    if let Some(when) = cfg.tombstoned_at {
        bail!(
            "project '{}' is tombstoned (since unix-ts {}); helper daemon refuses git \
             operations. Run `vigo swarm gitback project re-found {}` to mint a fresh \
             project_id and migrate.",
            cfg.handle,
            when,
            cfg.handle
        );
    }
    let founder = parse_hex_id(&cfg.founder_puddle_pubkey)
        .context("project.json founder_puddle_pubkey is not valid hex")?;

    let chain = TokenChain::load(home, project_id)
        .with_context(|| format!("loading tokens.json for {}", super::project_state::hex_id(project_id)))?
        .ok_or_else(|| anyhow!("no tokens.json — re-run `vigo swarm gitback project join`"))?;

    // ADR-019: load the per-user delegation chain. Missing file =
    // empty list = pre-rekey steady state, verifier behaves identically
    // to pre-ADR-019. Non-empty = founder has rekeyed; the chain
    // bridges genesis founder pubkey to the current signing identity.
    let delegations = DelegationChain::load(home, project_id)
        .with_context(|| format!("loading delegations.json for {}", super::project_state::hex_id(project_id)))?
        .map(|c| c.delegations)
        .unwrap_or_default();

    let now = chrono_now_secs();
    verify_chain(project_id, &founder, &delegations, &chain.chain, now)
        .map_err(|e| anyhow!("token chain rejected: {}", e))?;

    // Reject any chain whose tokens have an active revocation. Match
    // by content-addressed token id; revocations can target any link.
    let gitback_base = crate::platform::paths::default_gitback_dir();
    for (i, token) in chain.chain.iter().enumerate() {
        let tid = token.id();
        if is_token_revoked(&gitback_base, project_id, &tid) {
            bail!(
                "token at chain depth {} ({}) is revoked",
                i,
                hex::encode(&tid[..8])
            );
        }
    }
    // Also reject if this puddle has self-leaved (the leaf's subject
    // pubkey is in the project's leave envelopes).
    if let Some(leaf) = chain.chain.last() {
        if has_left(&gitback_base, project_id, &leaf.body.subject_puddle_pubkey)? {
            bail!("this puddle has issued a LEAVE envelope for the project");
        }
    }
    Ok(chain)
}

fn has_left(
    gitback_base: &Path,
    project_id: &[u8; ID_LEN],
    puddle_pubkey: &[u8; ID_LEN],
) -> Result<bool> {
    let p = super::envelopes::leave_path(gitback_base, project_id, puddle_pubkey);
    Ok(p.exists())
}

fn init_bare(repo: &Path) -> Result<()> {
    if let Some(parent) = repo.parent() {
        std::fs::create_dir_all(parent)
            .with_context(|| format!("create {}", parent.display()))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(parent, std::fs::Permissions::from_mode(0o700));
        }
    }
    let status = std::process::Command::new("git")
        .arg("init")
        .arg("--bare")
        .arg(repo)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .status()
        .with_context(|| format!("spawn git init --bare {}", repo.display()))?;
    if !status.success() {
        bail!("git init --bare {} failed", repo.display());
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(repo, std::fs::Permissions::from_mode(0o700));
    }
    Ok(())
}

fn short_service(s: Service) -> &'static str {
    // git accepts both `git upload-pack` and `git-upload-pack` as
    // the verb form on the command line; the dashless "subcommand"
    // form is the canonical one.
    match s {
        Service::UploadPack => "upload-pack",
        Service::ReceivePack => "receive-pack",
    }
}

fn chrono_now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

fn sanitize_err(s: &str) -> String {
    s.replace(['\n', '\r'], " ")
}

// `peer_uid` moved to `crate::swarm::anchor::peer_uid` — the
// SO_PEERCRED helper is shared by every root-side helper daemon.

// ---------- /etc/passwd lookup ----------

/// Resolve a UID to its `(username, home)` by reading `/etc/passwd`.
/// We deliberately do not call `getpwuid_r(3)` — direct read keeps
/// the daemon's external surface tiny and avoids any NSS plugin
/// (LDAP, SSSD, etc.) hitting a slow remote on the request path.
/// For Vigo's target (a per-host agent serving local users) the
/// passwd file is authoritative. The username is needed to look up the
/// caller's usercrate policy flag.
fn passwd_entry_for_uid(uid: u32) -> Result<(String, PathBuf)> {
    let path = std::env::var_os("VIGO_GITBACK_PASSWD_FILE")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("/etc/passwd"));
    let body = std::fs::read_to_string(&path)
        .with_context(|| format!("read {}", path.display()))?;
    passwd_entry_for_uid_in(&body, uid)
        .ok_or_else(|| anyhow!("no passwd entry for uid {}", uid))
}

/// Resolve a UID to its home directory only. Thin wrapper for callers
/// (the RESOLVE-NAME self-claim path) that don't need the username.
fn home_for_uid(uid: u32) -> Result<PathBuf> {
    passwd_entry_for_uid(uid).map(|(_, home)| home)
}

fn passwd_entry_for_uid_in(passwd_body: &str, uid: u32) -> Option<(String, PathBuf)> {
    for line in passwd_body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        // passwd line: name:passwd:uid:gid:gecos:home:shell
        let mut parts = line.splitn(7, ':');
        let name = parts.next()?;
        let _pw = parts.next()?;
        let line_uid: u32 = parts.next()?.parse().ok()?;
        let _gid = parts.next()?;
        let _gecos = parts.next()?;
        let home = parts.next()?;
        if line_uid == uid {
            return Some((name.to_string(), PathBuf::from(home)));
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_valid_hello() {
        let line = "HELLO 0000000000000000000000000000000000000000000000000000000000000001 git-upload-pack\n";
        let (pid, service) = parse_header(line).unwrap();
        assert_eq!(service, Service::UploadPack);
        assert_eq!(pid[31], 1);
    }

    #[test]
    fn rejects_unknown_verb() {
        let line = "HOWDY abcd... git-upload-pack\n";
        assert!(parse_header(line).is_err());
    }

    #[test]
    fn rejects_unknown_service() {
        let line = format!(
            "HELLO {} git-banana\n",
            "0".repeat(64)
        );
        assert!(parse_header(&line).is_err());
    }

    #[test]
    fn rejects_short_pid() {
        let line = "HELLO deadbeef git-upload-pack\n";
        assert!(parse_header(line).is_err());
    }

    #[test]
    fn rejects_extra_tokens() {
        let line = format!(
            "HELLO {} git-upload-pack extra\n",
            "0".repeat(64)
        );
        assert!(parse_header(&line).is_err());
    }

    #[test]
    fn service_round_trips() {
        for s in [Service::UploadPack, Service::ReceivePack] {
            assert_eq!(Service::parse(s.as_str()), Some(s));
        }
    }

    // ---------- wire-format snapshots ----------
    //
    // Pin the on-wire byte sequences this transport advertises. Any
    // intentional change here is fine — bump the snapshot. Accidental
    // changes (rename of a service token, stray trailing space in
    // HELLO) fail loudly here instead of confusing a remote peer at
    // runtime. See `lockbox_roundtrip` and `gitback_helper`
    // integration tests for the higher-level round-trip assertions.

    #[test]
    fn snapshot_service_names() {
        // Service tokens flow through git's smart-protocol service
        // negotiation; a rename here would silently break every
        // existing peer.
        assert_eq!(Service::UploadPack.as_str(), "git-upload-pack");
        assert_eq!(Service::ReceivePack.as_str(), "git-receive-pack");
    }

    #[test]
    fn snapshot_hello_line_format() {
        // The HELLO line shape `HELLO <pid_hex> <service>\n` is the
        // first thing the client writes after connecting. The daemon's
        // `parse_header` is forgiving of trailing CR but not of any
        // other shape change; pinning the exact bytes here keeps the
        // wire-level contract explicit alongside the parser tests.
        let pid_hex = "0".repeat(64);
        let hello = format!("HELLO {} git-upload-pack\n", pid_hex);
        assert_eq!(hello.as_bytes().last(), Some(&b'\n'));
        assert!(hello.starts_with("HELLO "));
        assert!(hello.contains(" git-upload-pack"));
        // Round-trip: the parser must accept what we emit.
        let (parsed_pid, parsed_service) = parse_header(&hello).expect("parse_header");
        assert_eq!(parsed_pid, [0u8; 32]);
        assert_eq!(parsed_service, Service::UploadPack);
    }

    #[test]
    fn snapshot_max_header_bytes_unchanged() {
        // Pinned to 4 KiB. Any change here may interact with peer
        // BufReader sizing on the client side.
        assert_eq!(MAX_HEADER_BYTES, 4 * 1024);
    }

    #[test]
    fn passwd_lookup_finds_user() {
        let body = "\
root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
dan:x:1000:1000:Dan,,,:/home/dan:/bin/zsh
";
        assert_eq!(
            passwd_entry_for_uid_in(body, 0),
            Some(("root".to_string(), PathBuf::from("/root")))
        );
        assert_eq!(
            passwd_entry_for_uid_in(body, 1000),
            Some(("dan".to_string(), PathBuf::from("/home/dan")))
        );
        assert_eq!(passwd_entry_for_uid_in(body, 9999), None);
    }

    #[test]
    fn passwd_lookup_skips_comments_and_blanks() {
        let body = "\
# this is a comment

dan:x:1000:1000:Dan:/home/dan:/bin/zsh
";
        assert_eq!(
            passwd_entry_for_uid_in(body, 1000),
            Some(("dan".to_string(), PathBuf::from("/home/dan")))
        );
    }

    #[test]
    fn enforce_gitback_policy_gates_on_flag() {
        use crate::swarm::policy::{write_user_policy_at, UserPolicy};
        let dir = tempfile::TempDir::new().unwrap();

        // No policy file → deny (user never enrolled in a gitback usercrate).
        assert!(enforce_gitback_policy_at(dir.path(), "dan").is_err());

        // gitback: false → deny (flag revoked; a restored tokens.json must
        // NOT re-arm push/fetch).
        write_user_policy_at(
            dir.path(),
            "dan",
            &UserPolicy { gitback: false, ..Default::default() },
        )
        .unwrap();
        assert!(enforce_gitback_policy_at(dir.path(), "dan").is_err());

        // gitback: true → allow.
        write_user_policy_at(
            dir.path(),
            "dan",
            &UserPolicy { gitback: true, ..Default::default() },
        )
        .unwrap();
        assert!(enforce_gitback_policy_at(dir.path(), "dan").is_ok());
    }

    #[tokio::test]
    async fn read_header_caps_oversized_line() {
        // A newline-less stream longer than MAX_HEADER_BYTES must error
        // rather than grow the buffer unbounded (pre-auth allocation DoS
        // on the 0666 socket).
        let big = vec![b'A'; MAX_HEADER_BYTES + 100];
        let mut br = BufReader::new(&big[..]);
        let err = read_header(&mut br).await.unwrap_err();
        assert!(err.to_string().contains("too long"), "err={err}");
    }

    #[tokio::test]
    async fn read_header_reads_line_and_leaves_tunnel_intact() {
        // The cap must bound only the header line — the bytes after the
        // newline belong to the git smart-protocol tunnel and must stay
        // readable from the same reader.
        let input = b"HELLO abc gitback\nTUNNEL-BYTES";
        let mut br = BufReader::new(&input[..]);
        let line = read_header(&mut br).await.unwrap();
        assert_eq!(line, "HELLO abc gitback\n");
        let mut rest = Vec::new();
        br.read_to_end(&mut rest).await.unwrap();
        assert_eq!(rest, b"TUNNEL-BYTES");
    }

    #[tokio::test]
    async fn read_header_errors_on_empty_stream() {
        let mut br = BufReader::new(&b""[..]);
        let err = read_header(&mut br).await.unwrap_err();
        assert!(err.to_string().contains("before HELLO"), "err={err}");
    }

    /// End-to-end: bind a daemon on a tempfile socket, connect a
    /// client, send a HELLO for a project that doesn't exist on
    /// disk for the connecting UID, and verify the daemon returns
    /// `ERR <message>` rather than crashing or hanging.
    ///
    /// Covers: bind → accept → SO_PEERCRED UID resolution → passwd
    /// lookup → project_state load (missing) → ERR response →
    /// cancellation cleanup. The OK path (which spawns a real
    /// `git upload-pack`) is left for a later integration test
    /// alongside the fan-out work.
    #[tokio::test]
    async fn daemon_rejects_project_missing_locally() {
        let temp = tempfile::tempdir().unwrap();
        let sock_path = temp.path().join("test-gitback.sock");
        let passwd_path = temp.path().join("passwd");
        let fake_home = temp.path().join("fake-home");
        std::fs::create_dir_all(&fake_home).unwrap();

        // Map the running test process's UID to fake_home so the
        // helper's project_state load looks under our empty fixture
        // dir instead of the developer's real home.
        let my_uid = unsafe { libc::getuid() };
        let passwd_body = format!(
            "root:x:0:0:root:/root:/bin/bash\ntest-user:x:{}:{}:test:{}:/bin/sh\n",
            my_uid,
            my_uid,
            fake_home.display()
        );
        std::fs::write(&passwd_path, passwd_body).unwrap();

        // Seed a gitback:true policy for test-user so the request clears
        // the usercrate-policy gate and reaches the project.json check
        // this test targets.
        let policy_dir = temp.path().join("usercrate-policy");
        crate::swarm::policy::write_user_policy_at(
            &policy_dir,
            "test-user",
            &crate::swarm::policy::UserPolicy { gitback: true, ..Default::default() },
        )
        .unwrap();

        // Test-isolation env vars; remove on test exit.
        std::env::set_var("VIGO_GITBACK_SOCKET_PATH", &sock_path);
        std::env::set_var("VIGO_GITBACK_PASSWD_FILE", &passwd_path);
        std::env::set_var("VIGO_USERCRATE_POLICY_DIR", &policy_dir);
        struct CleanupEnv;
        impl Drop for CleanupEnv {
            fn drop(&mut self) {
                std::env::remove_var("VIGO_GITBACK_SOCKET_PATH");
                std::env::remove_var("VIGO_GITBACK_PASSWD_FILE");
                std::env::remove_var("VIGO_USERCRATE_POLICY_DIR");
            }
        }
        let _cleanup = CleanupEnv;

        let cancel = CancellationToken::new();
        let cancel_for_daemon = cancel.clone();
        let daemon = tokio::spawn(async move { run(cancel_for_daemon, None).await });

        // Wait for the daemon to bind. We poll by attempting to
        // connect; the socket appears as soon as bind() returns.
        for _ in 0..50 {
            if sock_path.exists() {
                break;
            }
            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
        }
        assert!(sock_path.exists(), "daemon never bound socket");

        let mut stream = tokio::net::UnixStream::connect(&sock_path).await.unwrap();
        let pid_hex = "0".repeat(64);
        let hello = format!("HELLO {} git-upload-pack\n", pid_hex);
        tokio::io::AsyncWriteExt::write_all(&mut stream, hello.as_bytes())
            .await
            .unwrap();
        tokio::io::AsyncWriteExt::flush(&mut stream).await.ok();

        // Read response line — should be "ERR <message>\n".
        use tokio::io::AsyncBufReadExt;
        let mut br = tokio::io::BufReader::new(&mut stream);
        let mut line = String::new();
        br.read_line(&mut line).await.unwrap();
        assert!(
            line.starts_with("ERR "),
            "expected ERR line, got: {:?}",
            line
        );
        // No project.json under fake_home/.vigo-gitback — the
        // expected error comes from `authorize()`.
        assert!(
            line.contains("project.json") || line.contains("not initialized"),
            "ERR message should mention missing project.json: {:?}",
            line
        );

        cancel.cancel();
        // Reaping the daemon takes one accept cycle; tolerate either
        // outcome — the test's assertion is the ERR response above.
        let _ = tokio::time::timeout(std::time::Duration::from_secs(2), daemon).await;
        assert!(
            !sock_path.exists(),
            "daemon should remove socket on cancellation"
        );
    }
}
