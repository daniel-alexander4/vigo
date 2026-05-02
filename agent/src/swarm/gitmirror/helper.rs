// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Helper daemon — root-side Unix socket that proxies
//! `git-receive-pack` / `git-upload-pack` for `vigo://` URLs.
//!
//! ### Why a daemon
//!
//! The unified-substrate gitmirror layout (ADR-015) keeps every
//! project's bare repo at `/var/lib/vigo/swarm/gitmirror/projects/
//! <project_id_hex>.git`, mode `0700 root:root`. That permission
//! posture exists to gate **non-root local users** from cloning bare
//! repos that aren't theirs. The user-side `git-remote-vigo` binary
//! (multicall mode of `vigo`) runs as the calling user; without a
//! root-side mediator, it cannot read the bare repo. The helper
//! daemon is that mediator.
//!
//! ### Lifecycle
//!
//! Daemon binds [`socket_path`] on agent startup (under
//! [`spawn`](super::spawn)) and accepts connections forever. Each
//! connection is a fresh `git-remote-vigo` invocation:
//!
//! 1. Client sends a one-line `HELLO <project_id_hex> <service>\n`
//!    handshake. `service` is `git-upload-pack` (fetch) or
//!    `git-receive-pack` (push).
//! 2. Daemon resolves the calling user's UID via SO_PEERCRED, looks
//!    up that user's home in `/etc/passwd`, then loads
//!    `<home>/.vigo-gitmirror/<project_id_hex>/tokens.json`. Reading
//!    from disk under root authority means the helper never needs to
//!    trust wire-presented bytes for authorization.
//! 3. Daemon verifies the token chain against the URL's project_id
//!    via [`crate::swarm::gitmirror::tokens::verify_chain`]. It also
//!    rejects any chain that names a token id present in the local
//!    revocation set (see [`super::envelopes::is_token_revoked`]).
//! 4. Daemon writes `OK\n` and spawns `git <service> <repo_path>`
//!    with the socket attached as both stdin and stdout. From this
//!    point on the connection is a transparent tunnel speaking the
//!    pkt-line git smart protocol.
//! 5. When the child exits the daemon closes its end and goes back
//!    to accept().
//!
//! ### Cross-platform path
//!
//! The socket lives at [`socket_path`] — `/var/run/vigo/gitmirror.sock`
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
//! not consult external state to "decide" which fleet a caller
//! belongs to. Whoever has read access to
//! `~/.vigo-gitmirror/<pid>/tokens.json` and SO_PEERCRED-matches
//! that user's UID can fetch and push under that fleet's identity.

use std::os::fd::{AsRawFd, FromRawFd};
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

/// System-wide socket path the daemon binds and `git-remote-vigo`
/// connects to. `/var/run` is portable across Linux (where it's a
/// symlink to `/run`), macOS (tmpfs, recreated at boot), and the
/// BSDs / illumos. `vigo/` is the per-product subdir; we share it
/// only with future system-level helper sockets — the per-user
/// session helpers live under `$XDG_RUNTIME_DIR/vigo/` instead.
pub const SOCKET_DIR: &str = "/var/run/vigo";
pub const SOCKET_NAME: &str = "gitmirror.sock";

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
    if let Some(p) = std::env::var_os("VIGO_GITMIRROR_SOCKET_PATH") {
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
    tracing::info!(path = %socket_path().display(), "gitmirror helper daemon listening");
    let tls = std::sync::Arc::new(tls);
    loop {
        tokio::select! {
            _ = cancel.cancelled() => {
                tracing::info!("gitmirror helper daemon: cancellation requested, shutting down");
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
                        tracing::warn!(error = %e, "gitmirror helper client error");
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
    let uid = peer_uid(&stream)
        .ok_or_else(|| anyhow!("could not determine peer UID via SO_PEERCRED"))?;

    // Tokio's split moves the halves; we need the raw FD of the
    // underlying socket later to attach as the child's stdio, so
    // remember it now before splitting.
    let raw_fd = stream.as_raw_fd();

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

    let gitmirror_base = crate::platform::paths::default_gitmirror_dir();
    let repo = project_repo_path(&gitmirror_base, &project_id);

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

    // Hand off: write OK, then re-assemble the read + write halves
    // into the original stream (we still own `raw_fd`, but tokio's
    // halves don't expose a `reunite` for owned stdio reasons).
    // Strategy: dup the socket FD twice (one for stdin, one for
    // stdout), drop our halves, let the child's process inherit the
    // duplicates as its stdio.
    write_half.write_all(b"OK\n").await.context("writing OK")?;
    write_half.flush().await.ok();

    drop(br);
    drop(write_half);
    // At this point our halves are dropped; the underlying FD is
    // still alive because the kernel ref-counts it. Dup it to feed
    // the child.
    let stdin_fd = unsafe { libc::dup(raw_fd) };
    let stdout_fd = unsafe { libc::dup(raw_fd) };
    if stdin_fd < 0 || stdout_fd < 0 {
        bail!(
            "dup() of client socket fd failed: {}",
            std::io::Error::last_os_error()
        );
    }
    // Close the original — we've duped twice; the child holds the
    // only references that matter from here on.
    unsafe {
        libc::close(raw_fd);
    }
    let stdin = unsafe { Stdio::from_raw_fd(stdin_fd) };
    let stdout = unsafe { Stdio::from_raw_fd(stdout_fd) };

    let mut cmd = tokio::process::Command::new("git");
    cmd.arg(short_service(service))
        .arg(&repo)
        .stdin(stdin)
        .stdout(stdout)
        .stderr(Stdio::null())
        .kill_on_drop(true);
    let mut child = cmd.spawn().with_context(|| {
        format!(
            "spawning git {} against {}",
            service.as_str(),
            repo.display()
        )
    })?;

    // Snapshot refs before so we can compute a delta after a push.
    // Capturing here lets us avoid keeping a copy of the bare repo's
    // ref state across the (potentially long) receive-pack run.
    let pre_refs = if matches!(service, Service::ReceivePack) {
        fan_out::snapshot_refs(&repo).ok()
    } else {
        None
    };

    let status = child.wait().await.context("awaiting git child")?;
    if !status.success() {
        tracing::warn!(
            uid,
            service = service.as_str(),
            exit_code = status.code(),
            "gitmirror helper: git child exited non-zero"
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
                        "gitmirror helper: post-receive ref snapshot failed; skipping fan-out"
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
                        "gitmirror helper: no TLS certs available, fan-out skipped"
                    );
                    return Ok(());
                }
            };
            // Build the bundle + push outside the tokio worker thread —
            // git is synchronous and we don't want to starve the runtime.
            let project_id_owned = project_id;
            let home_owned = home.clone();
            let chain_owned = chain;
            let gitmirror_base = crate::platform::paths::default_gitmirror_dir();
            tokio::task::spawn_blocking(move || {
                let bundle_path = gitmirror_base
                    .join("incoming")
                    .join(format!(
                        "{}-fanout-{:032x}.bundle",
                        super::project_state::hex_id(&project_id_owned),
                        rand::random::<u128>(),
                    ));
                let repo_path = project_repo_path(&gitmirror_base, &project_id_owned);
                if let Err(e) = fan_out::build_bundle(&repo_path, &bundle_path) {
                    tracing::warn!(
                        project_id = %super::project_state::hex_id(&project_id_owned),
                        error = %e,
                        "gitmirror helper: build_bundle for fan-out failed"
                    );
                    return;
                }
                let (attempted, succeeded) = fan_out::fan_out_bundle(
                    &home_owned,
                    &project_id_owned,
                    &chain_owned,
                    &bundle_path,
                    &tls_for_fanout,
                );
                let _ = std::fs::remove_file(&bundle_path);
                tracing::info!(
                    project_id = %super::project_state::hex_id(&project_id_owned),
                    attempted,
                    succeeded,
                    "gitmirror helper: fan-out swept cross-subnet members"
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
    let founder = parse_hex_id(&cfg.founder_fleet_pubkey)
        .context("project.json founder_fleet_pubkey is not valid hex")?;

    let chain = TokenChain::load(home, project_id)
        .with_context(|| format!("loading tokens.json for {}", super::project_state::hex_id(project_id)))?
        .ok_or_else(|| anyhow!("no tokens.json — re-run `vigo swarm gitmirror project join`"))?;

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
    let gitmirror_base = crate::platform::paths::default_gitmirror_dir();
    for (i, token) in chain.chain.iter().enumerate() {
        let tid = token.id();
        if is_token_revoked(&gitmirror_base, project_id, &tid) {
            bail!(
                "token at chain depth {} ({}) is revoked",
                i,
                hex::encode(&tid[..8])
            );
        }
    }
    // Also reject if this fleet has self-leaved (the leaf's subject
    // pubkey is in the project's leave envelopes).
    if let Some(leaf) = chain.chain.last() {
        if has_left(&gitmirror_base, project_id, &leaf.body.subject_fleet_pubkey)? {
            bail!("this fleet has issued a LEAVE envelope for the project");
        }
    }
    Ok(chain)
}

fn has_left(
    gitmirror_base: &Path,
    project_id: &[u8; ID_LEN],
    fleet_pubkey: &[u8; ID_LEN],
) -> Result<bool> {
    let p = super::envelopes::leave_path(gitmirror_base, project_id, fleet_pubkey);
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
        // cryptsync's session helper uses.
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
    let path = std::env::var_os("VIGO_GITMIRROR_PASSWD_FILE")
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
        let sock_path = temp.path().join("test-gitmirror.sock");
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
        std::env::set_var("VIGO_GITMIRROR_SOCKET_PATH", &sock_path);
        std::env::set_var("VIGO_GITMIRROR_PASSWD_FILE", &passwd_path);
        struct CleanupEnv;
        impl Drop for CleanupEnv {
            fn drop(&mut self) {
                std::env::remove_var("VIGO_GITMIRROR_SOCKET_PATH");
                std::env::remove_var("VIGO_GITMIRROR_PASSWD_FILE");
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
        // No project.json under fake_home/.vigo-gitmirror — the
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
