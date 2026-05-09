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
//! Runs in a fresh process invoked by git. Synchronous std::io —
//! we're not in the agent's tokio runtime here, this is the user-side
//! short-lived helper. Splice is a single std::thread::spawn for
//! stdin→socket, with the main thread carrying socket→stdout.

use std::io::{Read, Write};
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

    // Bidirectional copy: stdin → socket on a worker thread,
    // socket → stdout on the main thread. When either direction
    // hits EOF we half-close the socket; the other direction
    // unwinds naturally on the next read.
    //
    // We deliberately do NOT use `std::io::copy` here. The stdlib
    // specializes copy() into a `splice(2)` fast-path on Linux when
    // both endpoints expose file descriptors — and `splice()` on
    // AF_UNIX stream sockets has a kernel-side wakeup race: the
    // splice can park itself in `unix_stream_data_wait` with bytes
    // sitting in the recv queue and never wake up, deadlocking the
    // protocol exchange. Strace masks the race because PTRACE
    // attach interrupts the splice with EINTR and the restart
    // re-checks the queue. The work-around: use plain read+write
    // through a userspace buffer, which routes through the
    // unix_stream_read_generic path that does wake up reliably.
    let writer_socket = socket.try_clone().context("cloning socket for writer thread")?;
    let stdin_handle = std::thread::spawn(move || -> std::io::Result<()> {
        let mut socket = writer_socket;
        let stdin = std::io::stdin();
        let mut stdin_lock = stdin.lock();
        copy_via_buffer(&mut stdin_lock, &mut socket)?;
        // Half-close the write side so the daemon's child process
        // sees EOF on its stdin (otherwise upload-pack hangs forever).
        let _ = socket.shutdown(std::net::Shutdown::Write);
        Ok(())
    });

    {
        let stdout = std::io::stdout();
        let mut out = stdout.lock();
        copy_via_buffer(&mut socket, &mut out).context("socket → stdout")?;
        out.flush().ok();
    }

    // Reap the writer thread. If it errored (e.g. broken pipe when
    // the daemon shut us down early) we still want to exit cleanly —
    // git treats remote-helper exit code as the success/failure
    // signal, and the daemon's child already reported its own status
    // through the smart protocol.
    let _ = stdin_handle.join();
    Ok(())
}

/// Manual bytes copy from `r` to `w` using a fixed userspace buffer.
/// Equivalent in semantics to `std::io::copy` but explicitly avoids
/// the splice(2) fast-path that deadlocks on AF_UNIX stream sockets
/// (see protocol_loop's tunnel splice comment).
fn copy_via_buffer<R: Read, W: Write>(r: &mut R, w: &mut W) -> std::io::Result<u64> {
    let mut buf = [0u8; 16 * 1024];
    let mut total: u64 = 0;
    loop {
        let n = match r.read(&mut buf) {
            Ok(0) => return Ok(total),
            Ok(n) => n,
            Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
            Err(e) => return Err(e),
        };
        w.write_all(&buf[..n])?;
        total += n as u64;
    }
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

    #[test]
    fn copy_via_buffer_empty_input() {
        let mut src: &[u8] = b"";
        let mut dst = Vec::new();
        let n = copy_via_buffer(&mut src, &mut dst).unwrap();
        assert_eq!(n, 0);
        assert!(dst.is_empty());
    }

    #[test]
    fn copy_via_buffer_small_payload() {
        let mut src: &[u8] = b"hello, world";
        let mut dst = Vec::new();
        let n = copy_via_buffer(&mut src, &mut dst).unwrap();
        assert_eq!(n, 12);
        assert_eq!(&dst, b"hello, world");
    }

    #[test]
    fn copy_via_buffer_large_payload_crosses_buffer_boundary() {
        // 50KB payload — forces multiple read iterations through the
        // 16KB internal buffer.
        let payload: Vec<u8> = (0..50_000).map(|i| (i % 251) as u8).collect();
        let mut src = payload.as_slice();
        let mut dst = Vec::new();
        let n = copy_via_buffer(&mut src, &mut dst).unwrap();
        assert_eq!(n, 50_000);
        assert_eq!(dst, payload);
    }

    #[test]
    fn copy_via_buffer_retries_on_interrupted() {
        // Reader that returns Interrupted on the first call, then EOF.
        // copy_via_buffer must retry rather than propagate the error.
        struct InterruptOnce {
            yielded: bool,
        }
        impl std::io::Read for InterruptOnce {
            fn read(&mut self, _buf: &mut [u8]) -> std::io::Result<usize> {
                if !self.yielded {
                    self.yielded = true;
                    Err(std::io::Error::new(
                        std::io::ErrorKind::Interrupted,
                        "test",
                    ))
                } else {
                    Ok(0)
                }
            }
        }
        let mut src = InterruptOnce { yielded: false };
        let mut dst = Vec::new();
        let n = copy_via_buffer(&mut src, &mut dst).unwrap();
        assert_eq!(n, 0);
        assert!(dst.is_empty());
    }
}
