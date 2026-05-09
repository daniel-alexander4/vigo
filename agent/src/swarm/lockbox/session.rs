// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Lockbox session helper daemon — holds the unwrapped age
//! identity in RAM between CLI invocations in one user login
//! session. The CLI's `unlock` verb spawns one of these; subsequent
//! `decrypt` calls talk to it over a Unix socket.
//!
//! Lifecycle:
//!
//! 1. User runs `vigo swarm lockbox unlock` — CLI reads password, unwraps
//!    `identity.age` via Argon2id, captures the login shell's PID.
//! 2. CLI spawns the binary as `vigo swarm lockbox-session-daemon
//!    --parent-pid <shell-pid> --idle-mins <N>` with `setsid()` so
//!    the daemon outlives the shell's `vigo swarm lockbox unlock` child.
//! 3. CLI writes the unwrapped age identity (bech32 "AGE-SECRET-KEY-
//!    1..." string) to the daemon's stdin and closes.
//! 4. Daemon reads stdin once, zeroizes the buffer, holds the parsed
//!    `Identity` in memory.
//! 5. Daemon binds `/run/user/<UID>/lockbox.sock` (Linux) or
//!    `$TMPDIR/lockbox-<UID>.sock` (macOS), 0600, SO_PEERCRED-checked.
//! 6. Three tasks race on `tokio::select!`:
//!    - Accept loop: serve DECRYPT/STATUS/LOCK requests.
//!    - Parent-PID watchdog: poll the shell's PID every 10 s; exit
//!      when gone.
//!    - Idle timer: reset on every DECRYPT/STATUS; exit on expiry.
//! 7. On exit (any reason): zeroize the held identity, unlink socket,
//!    unlink PID file.
//!
//! **Security posture:** only the socket owner can connect
//! (`SO_PEERCRED` / `LOCAL_PEERCRED` with UID match). The socket is
//! 0600 in a 0700 runtime dir. Root still beats all of this — the
//! daemon's explicit non-goal is resisting local root (see threat
//! model in `plan_lockbox.md`).

use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::PathBuf;
use std::process;
use std::time::{Duration, Instant};

use age::secrecy::ExposeSecret;
use age::x25519::Identity;
use anyhow::{bail, Context, Result};
use base64::engine::general_purpose::STANDARD as B64;
use base64::Engine;
use tokio::sync::mpsc;
use zeroize::Zeroizing;

// ---------- socket paths ----------

/// Per-UID runtime directory. Returns `/run/user/<UID>/vigo` on
/// Linux (systemd-managed tmpfs), `$TMPDIR/lockbox-<UID>` on
/// macOS. Creates with mode 0700 if missing.
pub fn runtime_dir() -> Result<PathBuf> {
    let uid = unsafe { libc::getuid() };
    let dir = if let Some(xdg) = std::env::var_os("XDG_RUNTIME_DIR") {
        PathBuf::from(xdg).join("vigo")
    } else if let Some(tmp) = std::env::var_os("TMPDIR") {
        PathBuf::from(tmp).join(format!("lockbox-{}", uid))
    } else {
        PathBuf::from(format!("/tmp/lockbox-{}", uid))
    };
    std::fs::create_dir_all(&dir).with_context(|| format!("creating {}", dir.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let perms = std::fs::Permissions::from_mode(0o700);
        let _ = std::fs::set_permissions(&dir, perms);
    }
    Ok(dir)
}

pub fn socket_path() -> Result<PathBuf> {
    Ok(runtime_dir()?.join("lockbox.sock"))
}

pub fn pidfile_path() -> Result<PathBuf> {
    Ok(runtime_dir()?.join("lockbox.pid"))
}

// ---------- client side: talk to a running helper ----------

/// Try to connect to an existing session helper. Returns `Ok(stream)`
/// if a helper is running and accepts our connection, `Err` otherwise.
pub fn connect() -> Result<UnixStream> {
    let path = socket_path()?;
    UnixStream::connect(&path)
        .with_context(|| format!("connecting to session helper at {}", path.display()))
}

/// Ask an existing helper to decrypt `ciphertext`. Returns plaintext
/// bytes on success. Performs one round-trip per call.
pub fn request_decrypt(ciphertext: &[u8]) -> Result<Vec<u8>> {
    let stream = connect()?;
    let (mut reader, mut writer) = stream_halves(stream)?;
    writer
        .write_all(format!("DECRYPT {}\n", B64.encode(ciphertext)).as_bytes())
        .context("sending DECRYPT")?;
    writer.flush().ok();
    let mut line = String::new();
    BufReader::new(&mut reader)
        .read_line(&mut line)
        .context("reading DECRYPT response")?;
    parse_ok_or_err(&line)
}

/// Ask an existing helper for its status. Returns the status line.
pub fn request_status() -> Result<String> {
    let stream = connect()?;
    let (mut reader, mut writer) = stream_halves(stream)?;
    writer.write_all(b"STATUS\n").context("sending STATUS")?;
    writer.flush().ok();
    let mut line = String::new();
    BufReader::new(&mut reader)
        .read_line(&mut line)
        .context("reading STATUS response")?;
    Ok(line.trim().to_string())
}

/// Ask an existing helper to lock (scrub key + exit). Returns Ok(())
/// even if the helper wasn't running (idempotent from the user's
/// perspective — the goal is "no held key after this returns").
pub fn request_lock() -> Result<()> {
    let stream = match connect() {
        Ok(s) => s,
        Err(_) => {
            // Already locked (no helper). Clean up stale socket +
            // pidfile if they exist — common after a crash.
            let _ = std::fs::remove_file(socket_path()?);
            let _ = std::fs::remove_file(pidfile_path()?);
            return Ok(());
        }
    };
    let (_reader, mut writer) = stream_halves(stream)?;
    let _ = writer.write_all(b"LOCK\n");
    let _ = writer.flush();
    Ok(())
}

fn stream_halves(stream: UnixStream) -> Result<(UnixStream, UnixStream)> {
    stream
        .set_read_timeout(Some(Duration::from_secs(30)))
        .ok();
    stream
        .set_write_timeout(Some(Duration::from_secs(30)))
        .ok();
    let reader = stream.try_clone().context("cloning Unix stream")?;
    Ok((reader, stream))
}

fn parse_ok_or_err(line: &str) -> Result<Vec<u8>> {
    let trimmed = line.trim_end_matches('\n');
    if let Some(rest) = trimmed.strip_prefix("OK ") {
        let bytes = B64
            .decode(rest.trim())
            .context("decoding OK payload")?;
        Ok(bytes)
    } else if let Some(reason) = trimmed.strip_prefix("ERR ") {
        bail!("helper error: {}", reason)
    } else if trimmed == "OK" {
        Ok(Vec::new())
    } else {
        bail!("unexpected helper response: {}", trimmed)
    }
}

/// Spawn the session helper daemon from the `unlock` CLI path.
///
/// The helper runs as `vigo swarm lockbox-session-daemon --parent-pid X
/// --idle-mins M --home <path>` with `setsid()` so it outlives the
/// shell's `vigo swarm lockbox unlock` child. The unwrapped identity is
/// piped in via stdin as the bech32 `AGE-SECRET-KEY-1...` string
/// followed by a newline, then stdin is closed. The daemon reads
/// once and zeroizes its buffer.
///
/// `home` is the user's home directory — passed in so the helper can
/// drop a session-unlocked sentinel under `<home>/.lockbox/` for
/// the root-context watcher to consult (ADR-012). Resolving home from
/// the helper's own getuid would also work but introduces an
/// unnecessary passwd-lookup step in the daemon path.
pub fn spawn_helper(
    identity: &Identity,
    parent_pid: u32,
    idle_mins: u32,
    home: &std::path::Path,
) -> Result<()> {
    use std::os::unix::process::CommandExt;
    use std::process::{Command, Stdio};

    // If one is already running, ask it to lock first so we don't
    // leave two helpers fighting for the same socket.
    let _ = request_lock();
    // Give the old helper a beat to unlink the socket.
    std::thread::sleep(Duration::from_millis(100));

    // Route stderr to a per-user log file in the runtime dir so
    // daemon errors are recoverable via `vigo swarm lockbox status` / tail
    // without keeping the parent shell's stderr pipe open (which
    // blocks `sed`, `tee`, etc. from seeing EOF on the pipeline).
    let log_path = runtime_dir()?.join("lockbox.log");
    let log_file = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(&log_path)
        .with_context(|| format!("opening {}", log_path.display()))?;
    let log_stderr = log_file
        .try_clone()
        .context("cloning log fd for stderr")?;

    let current_exe = std::env::current_exe().context("resolving current exe")?;
    let mut cmd = Command::new(&current_exe);
    cmd.arg("lockbox-session-daemon")
        .arg("--parent-pid")
        .arg(parent_pid.to_string())
        .arg("--idle-mins")
        .arg(idle_mins.to_string())
        .arg("--home")
        .arg(home.as_os_str())
        .stdin(Stdio::piped())
        .stdout(Stdio::from(log_file))
        .stderr(Stdio::from(log_stderr));

    // Detach from the controlling terminal so the daemon survives
    // the shell's SIGHUP when the login session ends normally. The
    // parent-PID watchdog handles the "login ends cleanly" case.
    unsafe {
        cmd.pre_exec(|| {
            // setsid returns -1 on error (we're already a session
            // leader); that's fine — the child's still detached.
            libc::setsid();
            Ok(())
        });
    }

    let mut child = cmd.spawn().context("spawning session helper")?;
    let mut stdin = child
        .stdin
        .take()
        .context("session helper stdin not piped")?;
    // Write identity + newline; the bech32 already does its own trimming
    // but the helper reads one line so the terminator is explicit.
    let secret = identity.to_string();
    let bytes = Zeroizing::new(format!("{}\n", secret.expose_secret()).into_bytes());
    stdin
        .write_all(&bytes)
        .context("writing identity to helper stdin")?;
    // Dropping `stdin` here closes the pipe — helper's read returns EOF.
    drop(stdin);

    // Don't wait on the child — it's detached via setsid. Just drop
    // the handle (reaping happens via the parent-PID watchdog once
    // the helper itself exits; until then it runs independently).
    std::mem::forget(child);
    Ok(())
}

// ---------- server side: run as the daemon ----------

/// Daemon entry point — invoked via `vigo swarm lockbox-session-daemon
/// --parent-pid X --idle-mins M`. Reads the age identity from stdin,
/// runs the socket server until:
/// - the parent PID disappears, OR
/// - idle timer expires, OR
/// - a LOCK request arrives.
///
/// On any exit path, scrubs the held identity + removes socket +
/// pidfile.
///
/// Async so it inherits `main()`'s `#[tokio::main]` runtime — we
/// can't build a new runtime inside an active one.
pub async fn daemon_main(args: Vec<String>) -> Result<()> {
    let parent_pid = arg_value(&args, "--parent-pid")?.parse::<u32>()?;
    let idle_mins = arg_value(&args, "--idle-mins")
        .unwrap_or_else(|_| "60".into())
        .parse::<u32>()?;
    // --home is optional only for forward-compat with older spawn paths
    // (none in current codebase). When present, the session-unlocked
    // sentinel is written/cleaned at <home>/.lockbox/session.unlocked
    // so the watcher can detect lock state.
    let home_arg = arg_value(&args, "--home").ok().map(PathBuf::from);

    // Read the identity from stdin. Expect one line, bech32-format
    // "AGE-SECRET-KEY-1..." followed by newline then EOF.
    let mut stdin = std::io::stdin();
    let mut buf = Zeroizing::new(Vec::with_capacity(512));
    stdin
        .read_to_end(&mut buf)
        .context("reading identity from stdin")?;
    let secret_str = std::str::from_utf8(&buf)
        .context("identity on stdin is not UTF-8")?
        .trim();
    let identity: Identity = secret_str
        .parse()
        .map_err(|e| anyhow::anyhow!("parsing identity: {}", e))?;

    // Set up the runtime environment.
    let sock_path = socket_path()?;
    let pid_path = pidfile_path()?;

    // If a socket is stuck at the path, unlink it (stale from a
    // previous crash).
    let _ = std::fs::remove_file(&sock_path);
    let listener =
        UnixListener::bind(&sock_path).with_context(|| format!("binding {}", sock_path.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let perms = std::fs::Permissions::from_mode(0o600);
        let _ = std::fs::set_permissions(&sock_path, perms);
    }
    listener
        .set_nonblocking(true)
        .context("setting listener nonblocking")?;

    // Write PID file.
    std::fs::write(&pid_path, format!("{}\n", process::id())).ok();

    // Drop the session-unlocked sentinel under the user's home so the
    // root-context watcher can detect lock state. Best-effort: a
    // failure here just means auto-encrypt-on-write stays off until
    // the next unlock, which is the same posture as a session never
    // having been unlocked. Caller's lifecycle invariant is "if the
    // sentinel exists with a live PID, the helper is up."
    let unlocked_sentinel = home_arg
        .as_ref()
        .map(|h| super::config::session_unlocked_path(h));
    if let Some(path) = unlocked_sentinel.as_ref() {
        if let Some(parent) = path.parent() {
            let _ = std::fs::create_dir_all(parent);
        }
        let _ = std::fs::write(path, format!("{}\n", process::id()));
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(
                path,
                std::fs::Permissions::from_mode(0o600),
            );
        }
    }

    let listener = tokio::net::UnixListener::from_std(listener)
        .context("tokio UnixListener")?;
    let result = run_server(listener, identity, parent_pid, idle_mins).await;

    // Cleanup regardless of exit reason.
    let _ = std::fs::remove_file(&sock_path);
    let _ = std::fs::remove_file(&pid_path);
    if let Some(path) = unlocked_sentinel.as_ref() {
        let _ = std::fs::remove_file(path);
    }
    result
}

async fn run_server(
    listener: tokio::net::UnixListener,
    identity: Identity,
    parent_pid: u32,
    idle_mins: u32,
) -> Result<()> {
    let my_uid = unsafe { libc::getuid() };
    let idle_total = Duration::from_secs(idle_mins as u64 * 60);
    let (activity_tx, mut activity_rx) = mpsc::unbounded_channel::<()>();
    let (shutdown_tx, mut shutdown_rx) = mpsc::channel::<()>(1);

    tracing::info!(
        parent_pid,
        idle_mins,
        "lockbox session helper started"
    );

    // Parent-PID watchdog — polls every 10 s, signals shutdown when
    // the tracked PID is gone.
    let watchdog_shutdown = shutdown_tx.clone();
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(Duration::from_secs(10));
        loop {
            interval.tick().await;
            if !pid_alive(parent_pid) {
                tracing::info!(parent_pid, "parent PID gone, shutting down");
                let _ = watchdog_shutdown.send(()).await;
                return;
            }
        }
    });

    // Idle timer — signals shutdown if no activity for idle_total.
    let idle_shutdown = shutdown_tx.clone();
    tokio::spawn(async move {
        let mut last_activity = Instant::now();
        loop {
            let elapsed = last_activity.elapsed();
            if elapsed >= idle_total {
                tracing::info!("idle timeout reached, shutting down");
                let _ = idle_shutdown.send(()).await;
                return;
            }
            let sleep_for = idle_total - elapsed;
            tokio::select! {
                _ = tokio::time::sleep(sleep_for) => {}
                got = activity_rx.recv() => {
                    if got.is_some() {
                        last_activity = Instant::now();
                    } else {
                        return;
                    }
                }
            }
        }
    });

    // Accept loop.
    loop {
        tokio::select! {
            _ = shutdown_rx.recv() => return Ok(()),
            accept = listener.accept() => {
                let (stream, _addr) = match accept {
                    Ok(v) => v,
                    Err(e) => {
                        tracing::debug!(error = %e, "accept error, continuing");
                        continue;
                    }
                };
                if !peer_uid_matches(&stream, my_uid) {
                    tracing::warn!("rejecting session client with mismatched UID");
                    drop(stream);
                    continue;
                }
                let id = identity.clone();
                let act = activity_tx.clone();
                let sd = shutdown_tx.clone();
                tokio::spawn(async move {
                    if let Err(e) = handle_client(stream, id, act, sd).await {
                        tracing::debug!(error = %e, "client session error");
                    }
                });
            }
        }
    }
}

async fn handle_client(
    stream: tokio::net::UnixStream,
    identity: Identity,
    activity: mpsc::UnboundedSender<()>,
    shutdown: mpsc::Sender<()>,
) -> Result<()> {
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
    let (reader, mut writer) = stream.into_split();
    let mut br = BufReader::new(reader);
    let mut line = String::new();
    let n = br.read_line(&mut line).await.context("reading request")?;
    if n == 0 {
        return Ok(());
    }
    let line = line.trim_end_matches('\n');

    if line == "LOCK" {
        let _ = writer.write_all(b"OK\n").await;
        let _ = writer.flush().await;
        let _ = shutdown.send(()).await;
        return Ok(());
    }

    if line == "STATUS" {
        let _ = activity.send(());
        let msg = format!("STATUS unlocked=true\n");
        let _ = writer.write_all(msg.as_bytes()).await;
        return Ok(());
    }

    if let Some(rest) = line.strip_prefix("DECRYPT ") {
        let _ = activity.send(());
        let ct = match B64.decode(rest.trim()) {
            Ok(b) => b,
            Err(_) => {
                let _ = writer.write_all(b"ERR bad base64 ciphertext\n").await;
                return Ok(());
            }
        };
        match decrypt_bytes(&ct, &identity) {
            Ok(pt) => {
                let resp = format!("OK {}\n", B64.encode(&pt));
                let _ = writer.write_all(resp.as_bytes()).await;
                let _ = writer.flush().await;
            }
            Err(e) => {
                let msg = format!("ERR {}\n", escape_err(&e.to_string()));
                let _ = writer.write_all(msg.as_bytes()).await;
            }
        }
        return Ok(());
    }

    let _ = writer.write_all(b"ERR unknown verb\n").await;
    Ok(())
}

fn decrypt_bytes(ciphertext: &[u8], identity: &Identity) -> Result<Vec<u8>> {
    let decryptor = age::Decryptor::new(ciphertext).context("reading age header")?;
    let ids: Vec<Box<dyn age::Identity>> = vec![Box::new(identity.clone())];
    let mut r = decryptor
        .decrypt(ids.iter().map(|i| i.as_ref()))
        .context("age decrypt failed — identity does not match")?;
    let mut out = Vec::new();
    std::io::copy(&mut r, &mut out).context("reading plaintext")?;
    Ok(out)
}

fn escape_err(s: &str) -> String {
    s.replace('\n', " ").replace('\r', " ")
}

/// Check `/proc/<pid>` on Linux, or use `kill(pid, 0)` elsewhere,
/// to test whether a PID is alive.
fn pid_alive(pid: u32) -> bool {
    #[cfg(target_os = "linux")]
    {
        return std::path::Path::new(&format!("/proc/{}", pid)).exists();
    }
    #[cfg(not(target_os = "linux"))]
    {
        // kill(pid, 0) returns 0 if the process exists (and we have
        // permission to signal it). ESRCH means gone; EPERM means
        // alive (we'd just not be able to signal it).
        let rc = unsafe { libc::kill(pid as libc::pid_t, 0) };
        if rc == 0 {
            return true;
        }
        let err = std::io::Error::last_os_error();
        err.raw_os_error() != Some(libc::ESRCH)
    }
}

fn peer_uid_matches(stream: &tokio::net::UnixStream, our_uid: u32) -> bool {
    // Use raw fd + SO_PEERCRED on Linux.
    #[cfg(target_os = "linux")]
    {
        use std::mem;
        use std::os::fd::AsRawFd;
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
            return false;
        }
        return cred.uid == our_uid;
    }
    // macOS: LOCAL_PEEREID returns (uid, gid) pair via SOL_LOCAL.
    #[cfg(target_os = "macos")]
    {
        use std::os::fd::AsRawFd;
        let fd = stream.as_raw_fd();
        let mut cred: [u32; 2] = [0; 2]; // [uid, gid]
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
            return false;
        }
        return cred[0] == our_uid;
    }
    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    {
        // Other Unixes — err closed (no feature today).
        let _ = stream;
        let _ = our_uid;
        false
    }
}

fn arg_value(args: &[String], key: &str) -> Result<String> {
    let mut iter = args.iter();
    while let Some(a) = iter.next() {
        if a == key {
            return iter
                .next()
                .cloned()
                .with_context(|| format!("{} expects a value", key));
        }
    }
    bail!("missing arg {}", key)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_ok_decodes_base64() {
        let line = format!("OK {}\n", B64.encode(b"plain"));
        let out = parse_ok_or_err(&line).unwrap();
        assert_eq!(out, b"plain");
    }

    #[test]
    fn parse_err_returns_error_with_reason() {
        let err = parse_ok_or_err("ERR bad password\n").unwrap_err();
        assert!(err.to_string().contains("bad password"));
    }

    #[test]
    fn parse_unexpected_response_errors() {
        assert!(parse_ok_or_err("HELLO\n").is_err());
    }

    #[test]
    fn arg_value_finds_key() {
        let args = vec![
            "--parent-pid".into(),
            "42".into(),
            "--idle-mins".into(),
            "60".into(),
        ];
        assert_eq!(arg_value(&args, "--parent-pid").unwrap(), "42");
        assert_eq!(arg_value(&args, "--idle-mins").unwrap(), "60");
        assert!(arg_value(&args, "--missing").is_err());
    }
}
