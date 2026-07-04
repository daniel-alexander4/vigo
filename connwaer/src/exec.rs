//! Bounded subprocess runner — the single subprocess-discipline home for the
//! crate. Every external command connwaer spawns routes through here: the
//! child runs in its own process group (Unix), stdout/stderr are drained
//! concurrently while it runs, and on deadline the whole group is killed
//! (SIGTERM, short grace, SIGKILL) so a wedged tool — or a grandchild it
//! spawned via `sh -c` — can never pin a scheduler thread past its budget.

use anyhow::{Context, Result};
use std::io::{Read, Write};
use std::process::{Child, Command, Output, Stdio};
use std::thread;
use std::time::{Duration, Instant};

/// Default budget for quick probes (`which`, single status/query commands).
pub const PROBE_TIMEOUT: Duration = Duration::from_secs(60);

const POLL_INTERVAL: Duration = Duration::from_millis(50);
#[cfg(unix)]
const TERM_GRACE: Duration = Duration::from_secs(2);

/// Run a command to completion within `timeout`.
pub fn run(program: &str, args: &[&str], timeout: Duration) -> Result<Output> {
    run_with_stdin(program, args, None, timeout)
}

/// Run the platform shell (`sh -c` / `cmd.exe /C`) on a full command line.
/// This is the entry point for operator-configured check/backup commands —
/// the process-group kill matters most here, since the shell's children
/// would survive a plain kill of the shell itself.
pub fn run_shell(command_line: &str, timeout: Duration) -> Result<Output> {
    #[cfg(unix)]
    return run("sh", &["-c", command_line], timeout);
    #[cfg(not(unix))]
    return run("cmd.exe", &["/C", command_line], timeout);
}

/// Probe convenience: lossy-trimmed stdout within `PROBE_TIMEOUT`, `None` on
/// spawn failure or timeout, exit status ignored.
pub fn stdout_of(program: &str, args: &[&str]) -> Option<String> {
    run(program, args, PROBE_TIMEOUT)
        .ok()
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
}

/// Like `stdout_of`, but `None` unless the command exited successfully.
pub fn stdout_if_ok(program: &str, args: &[&str]) -> Option<String> {
    run(program, args, PROBE_TIMEOUT)
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
}

/// Run a command with bytes piped to stdin. The stdin write is
/// EPIPE-tolerant: a child that exits (or closes stdin) before reading
/// everything is judged by its exit status, not by the write.
pub fn run_with_stdin(
    program: &str,
    args: &[&str],
    stdin_data: Option<&[u8]>,
    timeout: Duration,
) -> Result<Output> {
    let mut cmd = Command::new(program);
    cmd.args(args)
        .stdin(if stdin_data.is_some() { Stdio::piped() } else { Stdio::null() })
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());

    // Own process group, so the deadline kill reaches grandchildren.
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        unsafe {
            cmd.pre_exec(|| {
                libc::setpgid(0, 0);
                Ok(())
            });
        }
    }

    let mut child = cmd
        .spawn()
        .with_context(|| format!("spawning {program}"))?;

    // Start draining before writing stdin — a child that floods stdout while
    // we're still feeding it a large stdin must not deadlock on a full pipe.
    let stdout_pipe = child.stdout.take();
    let stderr_pipe = child.stderr.take();
    let out_handle = thread::spawn(move || drain(stdout_pipe));
    let err_handle = thread::spawn(move || drain(stderr_pipe));

    // The stdin write also gets its own thread: if the child neither reads
    // nor exits, a write past the pipe buffer would otherwise block this
    // thread before the deadline loop ever starts.
    let stdin_handle = match (stdin_data, child.stdin.take()) {
        (Some(data), Some(mut stdin)) => {
            let data = data.to_vec();
            Some(thread::spawn(move || {
                let _ = stdin.write_all(&data);
                // Dropped here: the child sees EOF.
            }))
        }
        _ => None,
    };

    let deadline = Instant::now() + timeout;
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) => {
                if Instant::now() >= deadline {
                    kill_tree(&mut child);
                    let _ = child.wait();
                    let _ = out_handle.join();
                    let _ = err_handle.join();
                    if let Some(h) = stdin_handle {
                        let _ = h.join();
                    }
                    anyhow::bail!(
                        "{program} timed out after {}s and was killed",
                        timeout.as_secs()
                    );
                }
                thread::sleep(POLL_INTERVAL);
            }
            Err(e) => {
                kill_tree(&mut child);
                let _ = child.wait();
                let _ = out_handle.join();
                let _ = err_handle.join();
                if let Some(h) = stdin_handle {
                    let _ = h.join();
                }
                return Err(e).with_context(|| format!("waiting for {program}"));
            }
        }
    };

    let stdout = out_handle.join().unwrap_or_default();
    let stderr = err_handle.join().unwrap_or_default();
    if let Some(h) = stdin_handle {
        let _ = h.join();
    }
    Ok(Output { status, stdout, stderr })
}

fn drain<R: Read>(pipe: Option<R>) -> Vec<u8> {
    let mut buf = Vec::new();
    if let Some(mut p) = pipe {
        let _ = p.read_to_end(&mut buf);
    }
    buf
}

/// Kill the child's whole process group: SIGTERM, a short grace for clean
/// shutdown, then SIGKILL for stragglers. On non-Unix only the direct child
/// can be killed.
fn kill_tree(child: &mut Child) {
    #[cfg(unix)]
    {
        let pgid = child.id() as i32;
        unsafe { libc::killpg(pgid, libc::SIGTERM) };
        let grace_end = Instant::now() + TERM_GRACE;
        while Instant::now() < grace_end {
            if let Ok(Some(_)) = child.try_wait() {
                break;
            }
            thread::sleep(POLL_INTERVAL);
        }
        unsafe { libc::killpg(pgid, libc::SIGKILL) };
    }
    #[cfg(not(unix))]
    {
        let _ = child.kill();
    }
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;

    #[test]
    fn exit_status_and_output_pass_through() {
        let out = run_shell("echo hi; echo err >&2; exit 3", Duration::from_secs(10)).unwrap();
        assert_eq!(out.status.code(), Some(3));
        assert_eq!(String::from_utf8_lossy(&out.stdout).trim(), "hi");
        assert_eq!(String::from_utf8_lossy(&out.stderr).trim(), "err");
    }

    #[test]
    fn timeout_kills_the_process_group() {
        let dir = tempfile::tempdir().unwrap();
        let pid_file = dir.path().join("pid");
        // The sleep is a grandchild of connwaer (child of sh); its PID lands
        // in pid_file so we can verify the group kill reached it.
        let cmd = format!("sleep 60 & echo $! > {}; wait", pid_file.display());

        let start = Instant::now();
        let err = run_shell(&cmd, Duration::from_secs(1)).unwrap_err();
        assert!(err.to_string().contains("timed out"), "got: {err}");
        // 1s budget + 2s grace + slack — nowhere near the sleep's 60s.
        assert!(start.elapsed() < Duration::from_secs(10));

        let pid: i32 = std::fs::read_to_string(&pid_file)
            .unwrap()
            .trim()
            .parse()
            .unwrap();
        // SIGKILL delivery is asynchronous; allow a short settle.
        let mut alive = true;
        for _ in 0..40 {
            alive = unsafe { libc::kill(pid, 0) } == 0;
            if !alive {
                break;
            }
            thread::sleep(Duration::from_millis(50));
        }
        assert!(!alive, "grandchild sleep (pid {pid}) survived the group kill");
    }

    #[test]
    fn large_output_drains_without_deadlock() {
        // Far past the ~64 KiB pipe buffer; a drain-after-wait design hangs here.
        let out = run_shell("head -c 1000000 /dev/zero", Duration::from_secs(30)).unwrap();
        assert!(out.status.success());
        assert_eq!(out.stdout.len(), 1_000_000);
    }

    #[test]
    fn stdin_round_trip() {
        let out = run_with_stdin("cat", &[], Some(b"ping"), Duration::from_secs(10)).unwrap();
        assert!(out.status.success());
        assert_eq!(out.stdout, b"ping");
    }

    #[test]
    fn stdin_write_tolerates_early_exit() {
        // `true` exits without reading stdin; the large write hits EPIPE,
        // which must not turn a successful exit into an error.
        let big = vec![b'x'; 1_000_000];
        let out = run_with_stdin("true", &[], Some(&big), Duration::from_secs(10)).unwrap();
        assert!(out.status.success());
    }

    #[test]
    fn stdout_of_ignores_exit_status() {
        assert_eq!(
            stdout_of("sh", &["-c", "echo partial; exit 1"]),
            Some("partial".to_string())
        );
        assert_eq!(stdout_of("/nonexistent-cmd-xyz", &[]), None);
    }

    #[test]
    fn stdout_if_ok_requires_success() {
        assert_eq!(
            stdout_if_ok("sh", &["-c", "echo partial; exit 1"]),
            None
        );
        assert_eq!(
            stdout_if_ok("sh", &["-c", "echo good"]),
            Some("good".to_string())
        );
    }
}
