// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Helper daemon — root-side Unix socket that proxies
//! `git-receive-pack` / `git-upload-pack` for `swarmgit://` URLs.
//!
//! ### Why a daemon
//!
//! The unified-substrate swarmgit layout (ADR-015) keeps every
//! project's bare repo at `/var/lib/vigo/swarm/swarmgit/projects/
//! <project_id_hex>.git`, mode `0700 root:root`. That permission
//! posture exists to gate **non-root local users** from cloning bare
//! repos that aren't theirs. The user-side `git-remote-swarmgit` binary
//! (multicall mode of `vigo`) runs as the calling user; without a
//! root-side mediator, it cannot read the bare repo. The helper
//! daemon is that mediator.
//!
//! ### Lifecycle
//!
//! Daemon binds [`socket_path`] on agent startup (under
//! [`spawn`](super::spawn)) and accepts connections forever. Each
//! connection is a fresh `git-remote-swarmgit` invocation:
//!
//! 1. Client sends a one-line `HELLO <project_id_hex> <service>\n`
//!    handshake. `service` is `git-upload-pack` (fetch) or
//!    `git-receive-pack` (push).
//! 2. Daemon resolves the calling user's UID via SO_PEERCRED, looks
//!    up that user's home in `/etc/passwd`, then loads
//!    `<home>/.vigo-swarmgit/<project_id_hex>/tokens.json`. Reading
//!    from disk under root authority means the helper never needs to
//!    trust wire-presented bytes for authorization.
//! 3. Daemon verifies the token chain against the URL's project_id
//!    via [`crate::swarm::swarmgit::tokens::verify_chain`]. It also
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
//! The socket lives at [`socket_path`] — `/var/run/vigo/swarmgit.sock`
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
//! `~/.vigo-swarmgit/<pid>/tokens.json` and SO_PEERCRED-matches
//! that user's UID can fetch and push under that puddle's identity.

use std::os::fd::AsRawFd;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use anyhow::{anyhow, bail, Context, Result};
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::{UnixListener, UnixStream};
use tokio_util::sync::CancellationToken;

use super::envelopes::is_token_revoked;
use super::fan_out;
use super::project_state::{
    parse_hex_id, project_repo_path, DelegationChain, ProjectConfig, TokenChain,
};
use super::tokens::{verify_chain, ID_LEN};
use crate::update::TlsCerts;

/// System-wide socket path the daemon binds and `git-remote-swarmgit`
/// connects to. `/var/run` is portable across Linux (where it's a
/// symlink to `/run`), macOS (tmpfs, recreated at boot), and the
/// BSDs / illumos. `vigo/` is the per-product subdir; we share it
/// only with future system-level helper sockets — the per-user
/// session helpers live under `$XDG_RUNTIME_DIR/vigo/` instead.
pub const SOCKET_DIR: &str = "/var/run/vigo";
pub const SOCKET_NAME: &str = "swarmgit.sock";

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
    if let Some(p) = std::env::var_os("VIGO_SWARMGIT_SOCKET_PATH") {
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
    tracing::info!(path = %socket_path().display(), "swarmgit helper daemon listening");
    let tls = std::sync::Arc::new(tls);
    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                tracing::info!("swarmgit helper daemon: cancellation requested, shutting down");
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
                let tls = tls.clone();
                tokio::spawn(async move {
                    if let Err(e) = handle_client(stream, tls).await {
                        tracing::warn!(error = %e, "swarmgit helper client error");
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
    tracing::info!(uid, "swarmgit helper: client accepted");

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
        "swarmgit helper: HELLO parsed"
    );

    let home = match home_for_uid(uid) {
        Ok(p) => p,
        Err(e) => {
            let _ = write_half.write_all(format!("ERR {}\n", sanitize_err(&e.to_string())).as_bytes()).await;
            return Err(e);
        }
    };

    let chain = match authorize(&home, &project_id) {
        Ok(c) => c,
        Err(e) => {
            let _ = write_half.write_all(format!("ERR {}\n", sanitize_err(&e.to_string())).as_bytes()).await;
            return Err(e);
        }
    };

    let swarmgit_base = crate::platform::paths::default_swarmgit_dir();
    let repo = project_repo_path(&swarmgit_base, &project_id);

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
            "swarmgit helper: client pipelined bytes past HELLO; routing to child stdin"
        );
    }
    let mut read_half = br.into_inner();

    let mut cmd = tokio::process::Command::new("git");
    cmd.arg(short_service(service))
        .arg(&repo)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        // Keep GIT_TRACE_PACKET on for one cycle so any residual
        // protocol-level oddity surfaces in the WARN log on non-zero
        // exit. Drop in a follow-up once pipe-shovel has soaked.
        .env("GIT_TRACE_PACKET", "1")
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
        "swarmgit helper: git child spawned with piped stdio"
    );

    // Re-inject any pipelined bytes the BufReader absorbed past HELLO.
    if !leftover.is_empty() {
        if let Err(e) = child_stdin.write_all(&leftover).await {
            tracing::warn!(uid, child_pid, error = %e, "swarmgit helper: writing pipelined bytes to child stdin");
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
                    tracing::info!(uid, child_pid, total, "swarmgit shovel: socket→stdin EOF");
                    break;
                }
                Ok(n) => n,
                Err(e) => {
                    tracing::warn!(uid, child_pid, total, error = %e, "swarmgit shovel: socket→stdin read err");
                    break;
                }
            };
            tracing::debug!(uid, child_pid, n, total, "swarmgit shovel: socket→stdin read");
            if let Err(e) = child_stdin.write_all(&buf[..n]).await {
                tracing::warn!(uid, child_pid, total, error = %e, "swarmgit shovel: socket→stdin write err");
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
                    tracing::info!(uid, child_pid, total, "swarmgit shovel: stdout→socket EOF");
                    break;
                }
                Ok(n) => n,
                Err(e) => {
                    tracing::warn!(uid, child_pid, total, error = %e, "swarmgit shovel: stdout→socket read err");
                    break;
                }
            };
            tracing::info!(uid, child_pid, n, total, "swarmgit shovel: stdout→socket read");
            if let Err(e) = write_half.write_all(&buf[..n]).await {
                tracing::warn!(uid, child_pid, total, error = %e, "swarmgit shovel: stdout→socket write err");
                break;
            }
            if let Err(e) = write_half.flush().await {
                tracing::warn!(uid, child_pid, total, error = %e, "swarmgit shovel: stdout→socket flush err");
                break;
            }
            total += n as u64;
            tracing::info!(uid, child_pid, n, total, "swarmgit shovel: stdout→socket wrote+flushed");
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
        "swarmgit helper: shovel tasks finished"
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
            "swarmgit helper: git child exited cleanly"
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
            "swarmgit helper: git child exited non-zero"
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
                        "swarmgit helper: post-receive ref snapshot failed; skipping fan-out"
                    );
                    return Ok(());
                }
            };
            if before == after {
                return Ok(());
            }
            let tls_for_fanout = match tls.as_ref() {
                Some(t) => t.clone(),
                None => {
                    tracing::debug!(
                        project_id = %super::project_state::hex_id(&project_id),
                        "swarmgit helper: no TLS certs available, fan-out skipped"
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
            let swarmgit_base = crate::platform::paths::default_swarmgit_dir();
            tokio::task::spawn_blocking(move || {
                let bundle_path = swarmgit_base
                    .join("incoming")
                    .join(format!(
                        "{}-fanout-{:032x}.bundle",
                        super::project_state::hex_id(&project_id_owned),
                        rand::random::<u128>(),
                    ));
                let repo_path = project_repo_path(&swarmgit_base, &project_id_owned);
                if let Err(e) = fan_out::build_bundle(&repo_path, &bundle_path) {
                    tracing::warn!(
                        project_id = %super::project_state::hex_id(&project_id_owned),
                        error = %e,
                        "swarmgit helper: build_bundle for fan-out failed"
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
                    "swarmgit helper: fan-out swept cross-subnet members"
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
    let mut line = String::new();
    let n = reader.read_line(&mut line).await.context("read_line")?;
    if n == 0 {
        bail!("client closed connection before HELLO");
    }
    if line.len() > MAX_HEADER_BYTES {
        bail!("HELLO line too long ({} bytes)", line.len());
    }
    Ok(line)
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

fn authorize(home: &Path, project_id: &[u8; ID_LEN]) -> Result<TokenChain> {
    // Project must exist locally (founder issued / joined).
    let cfg = ProjectConfig::load(home, project_id)
        .with_context(|| format!("loading project.json for {}", super::project_state::hex_id(project_id)))?
        .ok_or_else(|| anyhow!("no project.json — has this user joined the project?"))?;
    // ADR-019 slice 5: tombstoned projects are archive-only. Refuse
    // both fetch and push at the helper-daemon authorization gate.
    // Operator's path forward is `swarmgit project re-found <handle>`
    // (mints a fresh project_id and byte-migrates) or `project leave`.
    if let Some(when) = cfg.tombstoned_at {
        bail!(
            "project '{}' is tombstoned (since unix-ts {}); helper daemon refuses git \
             operations. Run `vigo swarm swarmgit project re-found {}` to mint a fresh \
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
        .ok_or_else(|| anyhow!("no tokens.json — re-run `vigo swarm swarmgit project join`"))?;

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
    let swarmgit_base = crate::platform::paths::default_swarmgit_dir();
    for (i, token) in chain.chain.iter().enumerate() {
        let tid = token.id();
        if is_token_revoked(&swarmgit_base, project_id, &tid) {
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
        if has_left(&swarmgit_base, project_id, &leaf.body.subject_puddle_pubkey)? {
            bail!("this puddle has issued a LEAVE envelope for the project");
        }
    }
    Ok(chain)
}

fn has_left(
    swarmgit_base: &Path,
    project_id: &[u8; ID_LEN],
    puddle_pubkey: &[u8; ID_LEN],
) -> Result<bool> {
    let p = super::envelopes::leave_path(swarmgit_base, project_id, puddle_pubkey);
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

// ---------- peer credentials ----------

fn peer_uid(stream: &UnixStream) -> Option<u32> {
    #[cfg(target_os = "linux")]
    {
        use std::mem;
        let fd = stream.as_raw_fd();
        let mut cred: libc::ucred = unsafe { mem::zeroed() };
        let mut len = mem::size_of::<libc::ucred>() as libc::socklen_t;
        let rc = unsafe {
            libc::getsockopt(
                fd,
                libc::SOL_SOCKET,
                libc::SO_PEERCRED,
                &mut cred as *mut _ as *mut _,
                &mut len,
            )
        };
        if rc != 0 {
            return None;
        }
        return Some(cred.uid);
    }
    #[cfg(target_os = "macos")]
    {
        let fd = stream.as_raw_fd();
        // SOL_LOCAL/LOCAL_PEEREID returns (uid, gid) — same shape
        // lockbox's session helper uses.
        let mut cred: [u32; 2] = [0; 2];
        let mut len = std::mem::size_of_val(&cred) as libc::socklen_t;
        const SOL_LOCAL: libc::c_int = 0;
        const LOCAL_PEEREID: libc::c_int = 3;
        let rc = unsafe {
            libc::getsockopt(
                fd,
                SOL_LOCAL,
                LOCAL_PEEREID,
                cred.as_mut_ptr() as *mut _,
                &mut len,
            )
        };
        if rc != 0 {
            return None;
        }
        return Some(cred[0]);
    }
    #[cfg(any(
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd",
        target_os = "dragonfly"
    ))]
    {
        // BSD getpeereid(3) — populates uid + gid by FD.
        let fd = stream.as_raw_fd();
        let mut uid: libc::uid_t = 0;
        let mut gid: libc::gid_t = 0;
        let rc = unsafe { libc::getpeereid(fd, &mut uid, &mut gid) };
        if rc != 0 {
            return None;
        }
        return Some(uid);
    }
    #[cfg(target_os = "illumos")]
    {
        // illumos exposes ucred via getpeerucred(3C); fall back to
        // SO_PEERCRED-style getsockopt where supported. For Phase 1
        // we leave this stubbed; agent on illumos still loads.
        let _ = stream;
        return None;
    }
    #[cfg(not(any(
        target_os = "linux",
        target_os = "macos",
        target_os = "freebsd",
        target_os = "openbsd",
        target_os = "netbsd",
        target_os = "dragonfly",
        target_os = "illumos"
    )))]
    {
        let _ = stream;
        None
    }
}

// ---------- /etc/passwd lookup ----------

/// Resolve a UID to its home directory by reading `/etc/passwd`.
/// We deliberately do not call `getpwuid_r(3)` — direct read keeps
/// the daemon's external surface tiny and avoids any NSS plugin
/// (LDAP, SSSD, etc.) hitting a slow remote on the request path.
/// For Vigo's target (a per-host agent serving local users) the
/// passwd file is authoritative.
fn home_for_uid(uid: u32) -> Result<PathBuf> {
    let path = std::env::var_os("VIGO_SWARMGIT_PASSWD_FILE")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("/etc/passwd"));
    let body = std::fs::read_to_string(&path)
        .with_context(|| format!("read {}", path.display()))?;
    home_for_uid_in(&body, uid)
        .ok_or_else(|| anyhow!("no passwd entry for uid {}", uid))
}

fn home_for_uid_in(passwd_body: &str, uid: u32) -> Option<PathBuf> {
    for line in passwd_body.lines() {
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        // passwd line: name:passwd:uid:gid:gecos:home:shell
        let mut parts = line.splitn(7, ':');
        let _name = parts.next()?;
        let _pw = parts.next()?;
        let line_uid: u32 = parts.next()?.parse().ok()?;
        let _gid = parts.next()?;
        let _gecos = parts.next()?;
        let home = parts.next()?;
        if line_uid == uid {
            return Some(PathBuf::from(home));
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
    // runtime. See `lockbox_roundtrip` and `swarmgit_helper`
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
        assert_eq!(home_for_uid_in(body, 0), Some(PathBuf::from("/root")));
        assert_eq!(home_for_uid_in(body, 1000), Some(PathBuf::from("/home/dan")));
        assert_eq!(home_for_uid_in(body, 9999), None);
    }

    #[test]
    fn passwd_lookup_skips_comments_and_blanks() {
        let body = "\
# this is a comment

dan:x:1000:1000:Dan:/home/dan:/bin/zsh
";
        assert_eq!(home_for_uid_in(body, 1000), Some(PathBuf::from("/home/dan")));
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
        let sock_path = temp.path().join("test-swarmgit.sock");
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

        // Test-isolation env vars; remove on test exit.
        std::env::set_var("VIGO_SWARMGIT_SOCKET_PATH", &sock_path);
        std::env::set_var("VIGO_SWARMGIT_PASSWD_FILE", &passwd_path);
        struct CleanupEnv;
        impl Drop for CleanupEnv {
            fn drop(&mut self) {
                std::env::remove_var("VIGO_SWARMGIT_SOCKET_PATH");
                std::env::remove_var("VIGO_SWARMGIT_PASSWD_FILE");
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
        // No project.json under fake_home/.vigo-swarmgit — the
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
