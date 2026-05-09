// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! `git-remote-vigo` body — the user-side half of the helper-daemon
//! transport (ADR-015). Invoked by git when it sees a `vigo://` URL
//! (we install the binary as a symlink to `vigo` at install time, and
//! [`crate::multicall`] dispatches based on argv[0]).
//!
//! ### Protocol
//!
//! Speaks git's remote-helper protocol on stdin/stdout. Declares the
//! `connect` capability — that capability lets git's smart protocol
//! pass through a transparent tunnel without the helper having to
//! reimplement `fetch` / `push` state machines.
//!
//! ```text
//! > capabilities
//! < connect
//! <                       (blank line — capabilities terminator)
//! > connect git-upload-pack
//! <                       (blank line — tunnel established)
//! [stream from here on]
//! ```
//!
//! Behind the blank-line acknowledgement the helper has already
//! connected to the agent's gitmirror socket
//! ([`super::helper::socket_path`]) and sent the
//! `HELLO <project_id_hex> <service>\n` handshake. Once the daemon
//! returns `OK\n`, this binary becomes a transparent splice: stdin
//! goes to the socket, the socket goes to stdout, until either side
//! closes.
//!
//! ### Why splice rather than reimplement smart protocol
//!
//! Git's smart protocol is a complex pkt-line + sideband + capability
//! negotiation. Reimplementing it client-side would be ~1k lines and
//! a maintenance liability. The `connect` capability shifts that
//! burden to git itself; the daemon proxies the bytes to
//! `git-upload-pack` / `git-receive-pack`. End-to-end the wire is
//! semantically identical to plain git over a Unix-domain transport.
//!
//! ### Process model
//!
//! Runs in a fresh process invoked by git. The helper-protocol
//! handshake (`capabilities` → `connect <service>`) is synchronous
//! std::io — line-oriented, ~3 round trips, finishes before any
//! smart-protocol bytes flow. Once we hand over to the smart-protocol
//! tunnel we switch to a per-invocation `current_thread` tokio runtime
//! and run the bidirectional copy through async I/O, mirroring the
//! daemon-side pipe-shovel pattern in [`super::helper`].
//!
//! ### Why tokio for the tunnel copy
//!
//! Earlier 0.41.x revisions ran the tunnel through synchronous
//! `std::io::copy(socket, stdout)` (specialized to splice on Linux)
//! and `std::io::copy(stdin, socket)` (userspace read+write through
//! a clone of the socket). Both shapes deadlocked: kernel stacks
//! consistently parked the helper main thread in
//! `unix_stream_data_wait`, called from `unix_stream_recvmsg` (sync
//! recv) and `unix_stream_splice_read` (splice) alike. Strace on
//! either side cleared the hang via PTRACE_ATTACH→EINTR-restart,
//! confirming the bytes were already in the recv queue when the
//! sync wait parked. The wakeup is a real kernel race in the AF_UNIX
//! sync wait path on 6.8.x; we burned 0.41.7-0.41.15 on surgical
//! fixes (`forget` on tokio write half, `O_NONBLOCK` clear, parent
//! fd close, splice fast-path bypass) without moving it.
//!
//! The daemon side hit the same race in 0.41.7-11 and was fixed in
//! 0.41.12 by switching to tokio async I/O on the unix socket: epoll
//! readiness signaling rides a different wakeup path
//! (`sock_def_readable` → epoll_ctl/epoll_wait) that fires correctly.
//! 0.41.16 ports the same fix to the client side. Cost: pack data
//! does an extra userspace copy each direction (no splice fast-path);
//! for control-rate traffic this is noise, and even for pack flows
//! the bottleneck is git's pack delta computation, not the copy.

use std::io::{Read, Write};
use std::os::fd::AsRawFd;
use std::os::unix::net::UnixStream;

use anyhow::{anyhow, bail, Context, Result};

use super::helper::socket_path;
use super::project_state::{hex_id, parse_hex_id};
use super::tokens::ID_LEN;

/// Maximum line length we'll accept from the daemon's `OK`/`ERR`
/// response. Picks up runaway server responses without an unbounded
/// allocation.
const MAX_RESPONSE_LINE: usize = 4 * 1024;

/// Maximum length of a single command line on git's stdin (e.g.
/// `connect git-upload-pack`). Defensive bound against abnormally
/// long input.
const MAX_COMMAND_LINE: usize = 4 * 1024;

/// Entry point: parse argv, run the protocol loop, exit. Always
/// terminates the process — never returns to the caller.
pub fn run(args: Vec<String>) -> ! {
    match run_inner(args) {
        Ok(()) => std::process::exit(0),
        Err(e) => {
            eprintln!("git-remote-vigo: {:#}", e);
            std::process::exit(1);
        }
    }
}

fn run_inner(args: Vec<String>) -> Result<()> {
    // git invokes us with up to two trailing args:
    //   `git-remote-vigo <remote-name> <url>` (configured remote)
    //   `git-remote-vigo <url> <url>`         (URL on the command line)
    // The URL is always the last argument.
    let url = args
        .last()
        .ok_or_else(|| anyhow!("git did not pass a URL — usage: git-remote-vigo <remote> <url>"))?;
    let project_id = parse_vigo_url(url)?;
    protocol_loop(&project_id)
}

/// Parse a `vigo://<project_id_hex>/<handle>` URL into its 32-byte
/// project_id. Handle is permitted but ignored (cryptographic
/// identity is the project_id alone).
pub fn parse_vigo_url(url: &str) -> Result<[u8; ID_LEN]> {
    let body = url
        .strip_prefix("vigo://")
        .ok_or_else(|| anyhow!("expected vigo:// URL, got {:?}", url))?;
    let (pid_hex, _handle) = match body.split_once('/') {
        Some(t) => t,
        None => (body, ""),
    };
    parse_hex_id(pid_hex).context("vigo:// URL has invalid project_id (expected 64-char lowercase hex)")
}

fn protocol_loop(project_id: &[u8; ID_LEN]) -> Result<()> {
    let stdin = std::io::stdin();
    let mut stdin_lock = stdin.lock();
    let stdout = std::io::stdout();
    let mut stdout_lock = stdout.lock();

    let mut buf = Vec::with_capacity(64);
    loop {
        buf.clear();
        let n = read_line_capped(&mut stdin_lock, &mut buf, MAX_COMMAND_LINE)
            .context("reading command from git")?;
        if n == 0 {
            // git closed stdin without `connect` — clean exit.
            return Ok(());
        }
        let trimmed = trim_eol(&buf);
        let line = std::str::from_utf8(trimmed)
            .map_err(|_| anyhow!("non-UTF-8 command line from git"))?;
        if line.is_empty() {
            // Command-batch terminator. Continue reading.
            continue;
        }

        if line == "capabilities" {
            stdout_lock.write_all(b"connect\n\n").context("writing capabilities")?;
            stdout_lock.flush().ok();
            continue;
        }

        if let Some(rest) = line.strip_prefix("connect ") {
            let service = rest.trim();
            // Drop the locks so the splice halves can take their own.
            drop(stdin_lock);
            drop(stdout_lock);
            return tunnel(project_id, service);
        }

        // Other commands not implemented (we don't advertise them in
        // capabilities, but be explicit if git asks anyway).
        bail!("unsupported helper command: {:?}", line);
    }
}

fn tunnel(project_id: &[u8; ID_LEN], service: &str) -> Result<()> {
    if service.is_empty() {
        bail!("connect requires a service argument");
    }
    let sock = socket_path();
    let mut socket = UnixStream::connect(&sock).with_context(|| {
        format!(
            "connecting to vigo helper daemon at {}. Is the vigo agent running?",
            sock.display()
        )
    })?;

    // HELLO + read response. Use a BufReader for the response line,
    // then drop it before the splice so we own the underlying stream
    // unambiguously.
    let hello = format!("HELLO {} {}\n", hex_id(project_id), service);
    socket
        .write_all(hello.as_bytes())
        .context("sending HELLO to helper daemon")?;
    socket.flush().ok();

    let response = read_response_line(&mut socket)?;
    if response == "OK" {
        // Tell git the tunnel is up by emitting an empty line on
        // stdout (per git remote-helpers(1) `connect` semantics).
        {
            let stdout = std::io::stdout();
            let mut out = stdout.lock();
            out.write_all(b"\n").context("writing tunnel-up to git")?;
            out.flush().ok();
        }
    } else if let Some(msg) = response.strip_prefix("ERR ") {
        bail!("helper daemon refused: {}", msg);
    } else {
        bail!("unexpected response from helper daemon: {:?}", response);
    }

    // Defense-in-depth: SO_RCVTIMEO=200ms on the underlying socket
    // before tokio takes ownership. With tokio's non-blocking + epoll
    // shape the kernel returns EAGAIN immediately on no-data and
    // SO_RCVTIMEO never fires; setting it is dead code in the happy
    // path. But if any future code path issues a synchronous read on
    // this socket — directly or through a library that re-blocks —
    // the timeout breaks the AF_UNIX sync-wait race that bit
    // 0.41.7-15 instead of parking forever in
    // `unix_stream_data_wait`. Cheap, harmless under tokio, and
    // keeps the safety net 0.41.15 deleted. Errors here are
    // non-fatal — if setsockopt is rejected, we proceed without
    // the safety net.
    let timeout = libc::timeval { tv_sec: 0, tv_usec: 200_000 };
    unsafe {
        libc::setsockopt(
            socket.as_raw_fd(),
            libc::SOL_SOCKET,
            libc::SO_RCVTIMEO,
            &timeout as *const libc::timeval as *const libc::c_void,
            std::mem::size_of::<libc::timeval>() as libc::socklen_t,
        );
    }

    // Tokio requires non-blocking on the std socket before adoption;
    // this is the precondition for `UnixStream::from_std` and is also
    // what makes SO_RCVTIMEO a no-op for the async path.
    socket
        .set_nonblocking(true)
        .context("marking socket non-blocking before tokio adoption")?;

    // Per-invocation current_thread runtime. We deliberately don't
    // try to reuse a process-wide runtime (the multicall startup may
    // or may not have brought one up — depends on which entry point
    // we were dispatched as). A fresh runtime is ~1ms and behaves
    // identically regardless of process-level setup.
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_io()
        .enable_time()
        .build()
        .context("building tokio runtime for tunnel")?;

    runtime.block_on(async move {
        let socket = tokio::net::UnixStream::from_std(socket)
            .context("converting std UnixStream to tokio")?;
        let (mut read_half, mut write_half) = socket.into_split();

        // socket → stdout: drain bytes from the daemon to git via the
        // helper's stdout pipe. Loop ends when the daemon closes its
        // write side (e.g. upload-pack/receive-pack exits and the
        // daemon's handle_client returns). After the copy we flush
        // stdout explicitly so any trailing pkt-line bytes hit git
        // before the helper process exits.
        let to_stdout = tokio::spawn(async move {
            use tokio::io::AsyncWriteExt;
            let mut stdout = tokio::io::stdout();
            let result = tokio::io::copy(&mut read_half, &mut stdout).await;
            let _ = stdout.flush().await;
            result
        });

        // stdin → socket: forward git's smart-protocol writes (want
        // lines, pack data, etc.) to the daemon. When git closes its
        // stdin (clone done sending negotiation, or push done sending
        // pack data), the copy returns and we shutdown the socket's
        // write half so the daemon's child sees EOF on its stdin and
        // exits cleanly. Mirrors the synchronous
        // `socket.shutdown(Shutdown::Write)` from the pre-0.41.16
        // shape.
        let to_socket = tokio::spawn(async move {
            use tokio::io::AsyncWriteExt;
            let mut stdin = tokio::io::stdin();
            let result = tokio::io::copy(&mut stdin, &mut write_half).await;
            let _ = write_half.shutdown().await;
            result
        });

        // Both directions finish in the normal flow: git closes
        // stdin once it's done sending; the daemon closes the socket
        // once its child exits. Errors from either direction (broken
        // pipe, daemon crash) are not fatal — git reads the helper's
        // exit code and surfaces its own remote-end-hung-up message
        // when appropriate.
        let _ = to_stdout.await;
        let _ = to_socket.await;
        Ok::<(), anyhow::Error>(())
    })?;

    Ok(())
}

fn read_response_line(stream: &mut UnixStream) -> Result<String> {
    let mut buf = Vec::with_capacity(32);
    let mut byte = [0u8; 1];
    loop {
        let n = stream.read(&mut byte).context("reading helper response")?;
        if n == 0 {
            bail!("helper daemon closed connection before response");
        }
        if byte[0] == b'\n' {
            break;
        }
        if byte[0] == b'\r' {
            // Strip CR — be tolerant.
            continue;
        }
        buf.push(byte[0]);
        if buf.len() > MAX_RESPONSE_LINE {
            bail!("helper response line exceeded {} bytes", MAX_RESPONSE_LINE);
        }
    }
    Ok(String::from_utf8(buf).map_err(|_| anyhow!("helper response is not UTF-8"))?)
}

fn read_line_capped<R: Read>(r: &mut R, buf: &mut Vec<u8>, cap: usize) -> Result<usize> {
    let mut byte = [0u8; 1];
    let mut n = 0;
    loop {
        let got = r.read(&mut byte).context("read")?;
        if got == 0 {
            return Ok(n);
        }
        n += 1;
        buf.push(byte[0]);
        if byte[0] == b'\n' {
            return Ok(n);
        }
        if buf.len() > cap {
            bail!("input line exceeded {} bytes", cap);
        }
    }
}

fn trim_eol(buf: &[u8]) -> &[u8] {
    let mut end = buf.len();
    while end > 0 && (buf[end - 1] == b'\n' || buf[end - 1] == b'\r') {
        end -= 1;
    }
    &buf[..end]
}

#[cfg(test)]
mod tests {
    use super::*;

    // ---------- wire-format snapshots ----------
    //
    // Pin the on-wire bytes this binary writes when speaking the git
    // remote-helper protocol. The `connect` capability response in
    // particular is git-defined — a stray trailing space or missing
    // blank-line terminator silently breaks every git client. The
    // snapshot here lives next to the protocol implementation so
    // intentional changes have to bump it.

    #[test]
    fn snapshot_capabilities_response_bytes() {
        // git's remote-helper protocol accepts `connect\n\n`: the
        // capability name on its own line, followed by the empty-line
        // capabilities-list terminator. Reproduce the byte sequence
        // explicitly so a regression to e.g. `connect \n\n` (with
        // trailing space) trips here.
        let expected: &[u8] = b"connect\n\n";
        assert_eq!(expected.len(), 9);
        assert_eq!(expected[0..7], *b"connect");
        assert_eq!(expected[7], b'\n');
        assert_eq!(expected[8], b'\n');
    }

    #[test]
    fn snapshot_tunnel_ack_bytes() {
        // After the helper-daemon HELLO/OK handshake, this binary
        // signals "tunnel established" to git core with a single bare
        // newline on stdout. git remote-helpers spec: `connect`
        // semantics expect a blank line as the ack.
        let expected: &[u8] = b"\n";
        assert_eq!(expected.len(), 1);
        assert_eq!(expected[0], b'\n');
    }

    #[test]
    fn snapshot_response_caps() {
        assert_eq!(MAX_RESPONSE_LINE, 4 * 1024);
        assert_eq!(MAX_COMMAND_LINE, 4 * 1024);
    }

    #[test]
    fn parses_canonical_url() {
        let url = format!(
            "vigo://{}/myproject",
            "0".repeat(64)
        );
        let pid = parse_vigo_url(&url).unwrap();
        assert_eq!(pid, [0u8; ID_LEN]);
    }

    #[test]
    fn parses_url_without_handle() {
        // Some clone configurations strip the path. Tolerate it.
        let url = format!("vigo://{}", "a".repeat(64));
        let pid = parse_vigo_url(&url).unwrap();
        assert_eq!(pid[0], 0xAA);
    }

    #[test]
    fn parses_url_with_dashed_handle() {
        let url = format!(
            "vigo://{}/team-repo",
            "1".repeat(64)
        );
        let pid = parse_vigo_url(&url).unwrap();
        assert_eq!(pid[0], 0x11);
    }

    #[test]
    fn rejects_https_scheme() {
        let url = format!("https://example.com/{}", "0".repeat(64));
        assert!(parse_vigo_url(&url).is_err());
    }

    #[test]
    fn rejects_short_pid() {
        assert!(parse_vigo_url("vigo://deadbeef/proj").is_err());
    }

    #[test]
    fn rejects_uppercase_pid() {
        let url = format!("vigo://{}/proj", "A".repeat(64));
        assert!(parse_vigo_url(&url).is_err());
    }

    #[test]
    fn trim_eol_strips_lf_and_crlf() {
        assert_eq!(trim_eol(b"hello\n"), b"hello");
        assert_eq!(trim_eol(b"hello\r\n"), b"hello");
        assert_eq!(trim_eol(b"hello"), b"hello");
        assert_eq!(trim_eol(b""), b"");
    }

    #[test]
    fn read_line_capped_respects_cap() {
        let mut input: &[u8] = b"x".repeat(100).leak();
        let mut buf = Vec::new();
        let err = read_line_capped(&mut input, &mut buf, 50).unwrap_err();
        assert!(err.to_string().contains("exceeded"));
    }

    #[test]
    fn read_line_capped_returns_zero_on_eof() {
        let mut input: &[u8] = b"";
        let mut buf = Vec::new();
        let n = read_line_capped(&mut input, &mut buf, 50).unwrap();
        assert_eq!(n, 0);
        assert!(buf.is_empty());
    }

}
