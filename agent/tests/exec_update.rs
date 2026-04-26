// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Integration tests for the agent self-update execv() behavior.
//!
//! Bug context: after `force_self_update` exec()s the new binary, the agent
//! was observed to stop logging to journald and traits went missing (pending
//! item: "Agent exec() leaves process in degraded state"). These tests
//! verify the underlying OS/runtime behaviors that the self-update mechanism
//! depends on, to identify what breaks after execv().
//!
//! Test approach: since execv() replaces the process image, we use fork()
//! to create a child that calls execv(), while the parent captures output
//! and verifies behavior. This matches the kernel behavior of direct execv()
//! for FD inheritance, signal masks, and resource limits.
//!
//! NOTE: These tests use fork() from within the multi-threaded Rust test
//! runner, which is the same scenario as the agent calling execv() from
//! within its multi-threaded tokio runtime.

#[cfg(unix)]
#[allow(unreachable_code)]
mod exec_update {
    use std::ffi::CString;
    use std::os::unix::io::RawFd;

    // -- helpers --------------------------------------------------------

    /// Create a pipe. Returns (read_fd, write_fd).
    fn pipe() -> (RawFd, RawFd) {
        let mut fds = [0i32; 2];
        assert_eq!(unsafe { libc::pipe(fds.as_mut_ptr()) }, 0, "pipe() failed");
        (fds[0], fds[1])
    }

    /// Create a pipe with O_CLOEXEC on both ends.
    fn pipe_cloexec() -> (RawFd, RawFd) {
        let mut fds = [0i32; 2];
        assert_eq!(
            unsafe { libc::pipe2(fds.as_mut_ptr(), libc::O_CLOEXEC) },
            0,
            "pipe2(O_CLOEXEC) failed"
        );
        (fds[0], fds[1])
    }

    /// Read all available bytes from a raw FD (non-blocking after EOF).
    fn read_all(fd: RawFd) -> String {
        let mut out = Vec::new();
        let mut buf = [0u8; 4096];
        loop {
            let n = unsafe { libc::read(fd, buf.as_mut_ptr() as *mut libc::c_void, buf.len()) };
            if n <= 0 {
                break;
            }
            out.extend_from_slice(&buf[..n as usize]);
        }
        String::from_utf8_lossy(&out).to_string()
    }

    /// Wait for a child process to exit and return its exit status.
    fn wait_child(pid: nix::unistd::Pid) -> i32 {
        use nix::sys::wait::{waitpid, WaitStatus};
        match waitpid(pid, None).expect("waitpid") {
            WaitStatus::Exited(_, code) => code,
            WaitStatus::Signaled(_, sig, _) => -(sig as i32),
            other => panic!("unexpected wait status: {:?}", other),
        }
    }

    /// Find the compiled agent binary (built by `cargo test`).
    fn agent_binary_path() -> Option<String> {
        let test_exe = std::env::current_exe().ok()?;
        // Integration test binary: .build/debug/deps/exec_update-<hash>
        // Agent binary:            .build/debug/vigo
        let debug_dir = test_exe.parent()?.parent()?;
        let agent = debug_dir.join("vigo");
        if agent.exists() {
            Some(agent.to_string_lossy().to_string())
        } else {
            None
        }
    }

    // -- test 1: stderr pipe survives execv ----------------------------

    /// Verify that stderr output works after execv() when stderr is a pipe
    /// (simulates systemd's journal pipe setup).
    ///
    /// This is the most basic requirement: if the stderr pipe doesn't
    /// survive exec, no tracing output can reach journald.
    #[test]
    fn stderr_pipe_survives_execv() {
        let (read_fd, write_fd) = pipe();

        match unsafe { nix::unistd::fork() }.expect("fork") {
            nix::unistd::ForkResult::Child => {
                // Close read end, redirect stderr to pipe write end.
                unsafe { libc::close(read_fd) };
                unsafe { libc::dup2(write_fd, libc::STDERR_FILENO) };
                unsafe { libc::close(write_fd) };

                let sh = CString::new("/bin/sh").unwrap();
                let arg0 = CString::new("sh").unwrap();
                let arg1 = CString::new("-c").unwrap();
                let arg2 = CString::new("echo STDERR_SURVIVED >&2").unwrap();
                nix::unistd::execv(&sh, &[&arg0, &arg1, &arg2]).expect("execv");
                unreachable!();
            }
            nix::unistd::ForkResult::Parent { child } => {
                unsafe { libc::close(write_fd) };
                let output = read_all(read_fd);
                unsafe { libc::close(read_fd) };
                let status = wait_child(child);

                assert_eq!(status, 0, "child should exit cleanly");
                assert!(
                    output.contains("STDERR_SURVIVED"),
                    "stderr pipe must survive execv — got: {:?}",
                    output
                );
            }
        }
    }

    // -- test 2: stdout pipe survives execv ----------------------------

    /// Same as above but for stdout. The agent uses tracing (stderr) but
    /// some paths (--version) write to stdout.
    #[test]
    fn stdout_pipe_survives_execv() {
        let (read_fd, write_fd) = pipe();

        match unsafe { nix::unistd::fork() }.expect("fork") {
            nix::unistd::ForkResult::Child => {
                unsafe { libc::close(read_fd) };
                unsafe { libc::dup2(write_fd, libc::STDOUT_FILENO) };
                unsafe { libc::close(write_fd) };

                let sh = CString::new("/bin/sh").unwrap();
                let arg0 = CString::new("sh").unwrap();
                let arg1 = CString::new("-c").unwrap();
                let arg2 = CString::new("echo STDOUT_SURVIVED").unwrap();
                nix::unistd::execv(&sh, &[&arg0, &arg1, &arg2]).expect("execv");
                unreachable!();
            }
            nix::unistd::ForkResult::Parent { child } => {
                unsafe { libc::close(write_fd) };
                let output = read_all(read_fd);
                unsafe { libc::close(read_fd) };
                let status = wait_child(child);

                assert_eq!(status, 0);
                assert!(
                    output.contains("STDOUT_SURVIVED"),
                    "stdout pipe must survive execv — got: {:?}",
                    output
                );
            }
        }
    }

    // -- test 3: CLOEXEC FDs are closed after exec ---------------------

    /// Verify that FDs opened with O_CLOEXEC are closed after execv().
    /// This is critical because std::fs::File (used by ConvergenceLock)
    /// sets O_CLOEXEC — the lock must be released when exec happens.
    #[test]
    fn cloexec_fds_closed_after_execv() {
        let (capture_r, capture_w) = pipe();
        let (cloexec_r, _cloexec_w) = pipe_cloexec();

        match unsafe { nix::unistd::fork() }.expect("fork") {
            nix::unistd::ForkResult::Child => {
                unsafe { libc::close(capture_r) };
                unsafe { libc::dup2(capture_w, libc::STDOUT_FILENO) };
                unsafe { libc::close(capture_w) };

                // After exec, cloexec_r should be closed. The shell
                // script tries to read from it — if closed, read fails.
                let fd_num = cloexec_r.to_string();
                let script = format!(
                    "if ! cat /proc/self/fd/{fd} >/dev/null 2>&1; then \
                         echo CLOEXEC_CLOSED; \
                     else \
                         echo CLOEXEC_LEAKED; \
                     fi",
                    fd = fd_num
                );

                let sh = CString::new("/bin/sh").unwrap();
                let arg0 = CString::new("sh").unwrap();
                let arg1 = CString::new("-c").unwrap();
                let arg2 = CString::new(script).unwrap();
                nix::unistd::execv(&sh, &[&arg0, &arg1, &arg2]).expect("execv");
                unreachable!();
            }
            nix::unistd::ForkResult::Parent { child } => {
                unsafe { libc::close(capture_w) };
                let output = read_all(capture_r);
                unsafe { libc::close(capture_r) };
                unsafe { libc::close(cloexec_r) };
                // _cloexec_w dropped automatically
                let status = wait_child(child);

                assert_eq!(status, 0);
                assert!(
                    output.contains("CLOEXEC_CLOSED"),
                    "O_CLOEXEC FD must be closed after execv — got: {:?}",
                    output
                );
            }
        }
    }

    // -- test 4: non-CLOEXEC FDs leak across exec ----------------------

    /// Verify that raw FDs without O_CLOEXEC survive execv(). These are
    /// leaked FDs that the new process inherits but doesn't know about.
    ///
    /// This matters because LMDB (heed), or any C library that uses raw
    /// open() without O_CLOEXEC, would leak FDs across exec.
    #[test]
    fn non_cloexec_fds_leak_across_execv() {
        let (capture_r, capture_w) = pipe();
        let (leak_r, _leak_w) = pipe(); // no CLOEXEC

        match unsafe { nix::unistd::fork() }.expect("fork") {
            nix::unistd::ForkResult::Child => {
                unsafe { libc::close(capture_r) };
                unsafe { libc::dup2(capture_w, libc::STDOUT_FILENO) };
                unsafe { libc::close(capture_w) };

                let fd_num = leak_r.to_string();
                let script = format!(
                    "if [ -e /proc/self/fd/{fd} ]; then \
                         echo FD_LEAKED; \
                     else \
                         echo FD_CLOSED; \
                     fi",
                    fd = fd_num
                );

                let sh = CString::new("/bin/sh").unwrap();
                let arg0 = CString::new("sh").unwrap();
                let arg1 = CString::new("-c").unwrap();
                let arg2 = CString::new(script).unwrap();
                nix::unistd::execv(&sh, &[&arg0, &arg1, &arg2]).expect("execv");
                unreachable!();
            }
            nix::unistd::ForkResult::Parent { child } => {
                unsafe { libc::close(capture_w) };
                let output = read_all(capture_r);
                unsafe { libc::close(capture_r) };
                unsafe { libc::close(leak_r) };
                let status = wait_child(child);

                assert_eq!(status, 0);
                assert!(
                    output.contains("FD_LEAKED"),
                    "non-CLOEXEC FD should survive execv (this is a leak) — got: {:?}",
                    output
                );
            }
        }
    }

    // -- test 5: many leaked FDs don't prevent new process startup -----

    /// Open 100 raw pipe pairs (200 FDs) without CLOEXEC, then fork+exec.
    /// The child should start successfully despite inheriting 200+ leaked FDs.
    ///
    /// This simulates the agent's accumulated runtime FDs (gRPC, swarm,
    /// multicast, LMDB, etc.) surviving exec.
    #[test]
    fn many_leaked_fds_dont_prevent_startup() {
        let mut leak_fds: Vec<RawFd> = Vec::new();
        for _ in 0..100 {
            let (r, w) = pipe(); // no CLOEXEC
            leak_fds.push(r);
            leak_fds.push(w);
        }

        let (capture_r, capture_w) = pipe_cloexec();

        match unsafe { nix::unistd::fork() }.expect("fork") {
            nix::unistd::ForkResult::Child => {
                unsafe { libc::close(capture_r) };
                unsafe { libc::dup2(capture_w, libc::STDOUT_FILENO) };
                unsafe { libc::close(capture_w) };

                // Count open FDs in the child after exec.
                let sh = CString::new("/bin/sh").unwrap();
                let arg0 = CString::new("sh").unwrap();
                let arg1 = CString::new("-c").unwrap();
                let arg2 = CString::new(
                    "count=$(ls /proc/self/fd 2>/dev/null | wc -l); \
                     echo FD_COUNT=$count; \
                     echo STARTUP_OK"
                ).unwrap();
                nix::unistd::execv(&sh, &[&arg0, &arg1, &arg2]).expect("execv");
                unreachable!();
            }
            nix::unistd::ForkResult::Parent { child } => {
                unsafe { libc::close(capture_w) };
                let output = read_all(capture_r);
                unsafe { libc::close(capture_r) };
                for fd in leak_fds {
                    unsafe { libc::close(fd) };
                }
                let status = wait_child(child);

                assert_eq!(status, 0);
                assert!(
                    output.contains("STARTUP_OK"),
                    "child should start despite 200 leaked FDs — got: {:?}",
                    output
                );

                // The child should have many more FDs than the standard 3.
                if let Some(count_line) = output.lines().find(|l| l.starts_with("FD_COUNT=")) {
                    let count: usize = count_line
                        .trim_start_matches("FD_COUNT=")
                        .trim()
                        .parse()
                        .unwrap_or(0);
                    assert!(
                        count > 100,
                        "child should inherit leaked FDs, found only {} open FDs",
                        count
                    );
                }
            }
        }
    }

    // -- test 6: flock released when all OFD references are closed ------

    /// The convergence lock uses std::fs::File (O_CLOEXEC) + flock().
    /// In a direct execv (no fork), the FD is closed by CLOEXEC and
    /// the lock is released — the new binary can re-acquire it.
    ///
    /// We can't test direct exec (it would replace the test process),
    /// so we simulate it: fork, then drop the lock in the parent before
    /// the child checks. After fork, both parent and child share the
    /// same open file description (OFD). Once both close their FDs
    /// (parent drops, child's CLOEXEC closes on exec), the lock is freed.
    #[test]
    fn flock_released_when_all_ofd_refs_closed() {
        let tmp = tempfile::NamedTempFile::new().expect("tempfile");
        let path = tmp.path().to_str().unwrap().to_string();

        // Acquire flock via a std::fs::File (which sets O_CLOEXEC).
        let lock_file = std::fs::File::options()
            .write(true)
            .open(&path)
            .expect("open lock file");
        let ret = unsafe { libc::flock(std::os::unix::io::AsRawFd::as_raw_fd(&lock_file), libc::LOCK_EX | libc::LOCK_NB) };
        assert_eq!(ret, 0, "parent should acquire lock");

        let (capture_r, capture_w) = pipe_cloexec();

        match unsafe { nix::unistd::fork() }.expect("fork") {
            nix::unistd::ForkResult::Child => {
                // Child's inherited FD has O_CLOEXEC — closed on exec.
                unsafe { libc::close(capture_r) };
                unsafe { libc::dup2(capture_w, libc::STDOUT_FILENO) };
                unsafe { libc::close(capture_w) };

                // Small delay so parent can drop its reference first.
                let script = format!(
                    "sleep 0.05; \
                     flock -n -x '{}' -c 'echo LOCK_ACQUIRED' || echo LOCK_BLOCKED",
                    path
                );
                let sh = CString::new("/bin/sh").unwrap();
                let arg0 = CString::new("sh").unwrap();
                let arg1 = CString::new("-c").unwrap();
                let arg2 = CString::new(script).unwrap();
                nix::unistd::execv(&sh, &[&arg0, &arg1, &arg2]).expect("execv");
                unreachable!();
            }
            nix::unistd::ForkResult::Parent { child } => {
                // Drop the lock BEFORE the child checks — simulates
                // direct exec where the calling process's FD is the only
                // reference and CLOEXEC closes it.
                drop(lock_file);

                unsafe { libc::close(capture_w) };
                let output = read_all(capture_r);
                unsafe { libc::close(capture_r) };
                let status = wait_child(child);

                assert_eq!(status, 0);
                assert!(
                    output.contains("LOCK_ACQUIRED"),
                    "lock should be released when all OFD refs are closed — got: {:?}",
                    output
                );
            }
        }
    }

    // -- test 7: flock STUCK when FD lacks CLOEXEC ---------------------

    /// If a lock is held via a raw FD (no CLOEXEC), the lock survives
    /// execv() and the new process CANNOT acquire it. This would cause
    /// the new agent binary to fail acquiring the convergence lock,
    /// potentially explaining the "degraded state" bug.
    ///
    /// Currently the agent uses std::fs::File (CLOEXEC) for locks, so
    /// this shouldn't happen. But if any code path opens a lock file
    /// via raw libc::open() without O_CLOEXEC, this is the result.
    #[test]
    fn flock_survives_exec_without_cloexec() {
        let tmp = tempfile::NamedTempFile::new().expect("tempfile");
        let path = tmp.path().to_str().unwrap().to_string();

        // Open the file WITHOUT O_CLOEXEC.
        let cpath = CString::new(path.clone()).unwrap();
        let lock_fd = unsafe { libc::open(cpath.as_ptr(), libc::O_RDWR) };
        assert!(lock_fd >= 0, "open lock file");

        let ret = unsafe { libc::flock(lock_fd, libc::LOCK_EX | libc::LOCK_NB) };
        assert_eq!(ret, 0, "parent should acquire lock");

        let (capture_r, capture_w) = pipe_cloexec();

        match unsafe { nix::unistd::fork() }.expect("fork") {
            nix::unistd::ForkResult::Child => {
                unsafe { libc::close(capture_r) };
                unsafe { libc::dup2(capture_w, libc::STDOUT_FILENO) };
                unsafe { libc::close(capture_w) };

                // The child inherits lock_fd (no CLOEXEC), so the flock
                // is still held after exec. A new open+flock on the same
                // path will block (or fail with LOCK_NB).
                let script = format!(
                    "flock -n -x '{}' -c 'echo LOCK_FREE' || echo LOCK_HELD",
                    path
                );
                let sh = CString::new("/bin/sh").unwrap();
                let arg0 = CString::new("sh").unwrap();
                let arg1 = CString::new("-c").unwrap();
                let arg2 = CString::new(script).unwrap();
                nix::unistd::execv(&sh, &[&arg0, &arg1, &arg2]).expect("execv");
                unreachable!();
            }
            nix::unistd::ForkResult::Parent { child } => {
                unsafe { libc::close(capture_w) };
                let output = read_all(capture_r);
                unsafe { libc::close(capture_r) };
                let status = wait_child(child);
                unsafe { libc::close(lock_fd) };

                assert_eq!(status, 0);
                // The lock SHOULD still be held in the child because the
                // FD survived exec. This demonstrates the deadlock risk.
                assert!(
                    output.contains("LOCK_HELD"),
                    "non-CLOEXEC flock must survive execv (deadlock risk!) — got: {:?}",
                    output
                );
            }
        }
    }

    // -- test 8: agent binary starts correctly after fork+exec ---------

    /// The most realistic test: fork, then exec into the actual agent
    /// binary with `version` subcommand. If the binary starts and
    /// produces output, its main() → setup_logger() path works after exec.
    ///
    /// Skipped if the agent binary isn't built.
    #[test]
    fn agent_binary_starts_after_execv() {
        let binary = match agent_binary_path() {
            Some(p) => p,
            None => {
                eprintln!("SKIPPED: agent binary not found (run `cargo build` first)");
                return;
            }
        };

        let (capture_r, capture_w) = pipe();

        match unsafe { nix::unistd::fork() }.expect("fork") {
            nix::unistd::ForkResult::Child => {
                unsafe { libc::close(capture_r) };
                unsafe { libc::dup2(capture_w, libc::STDOUT_FILENO) };
                unsafe { libc::close(capture_w) };

                let bin = CString::new(binary.as_str()).unwrap();
                let arg0 = CString::new(binary.as_str()).unwrap();
                let arg1 = CString::new("version").unwrap();
                nix::unistd::execv(&bin, &[&arg0, &arg1]).expect("execv agent");
                unreachable!();
            }
            nix::unistd::ForkResult::Parent { child } => {
                unsafe { libc::close(capture_w) };
                let output = read_all(capture_r);
                unsafe { libc::close(capture_r) };
                let status = wait_child(child);

                assert_eq!(status, 0, "agent version should exit 0, got {}", status);
                assert!(
                    !output.trim().is_empty(),
                    "agent version should print a version string"
                );
                let version = output.trim();
                assert!(
                    version.contains('.'),
                    "agent version should contain '.', got: {:?}",
                    version
                );
            }
        }
    }

    // -- test 9: agent binary starts with leaked FDs -------------------

    /// Fork+exec the agent binary with 100 leaked raw FDs (no CLOEXEC).
    /// Simulates the worst case: the old process had many open sockets,
    /// LMDB FDs, etc., and they all survive into the new binary.
    ///
    /// Skipped if the agent binary isn't built.
    #[test]
    fn agent_binary_starts_with_leaked_fds() {
        let binary = match agent_binary_path() {
            Some(p) => p,
            None => {
                eprintln!("SKIPPED: agent binary not found");
                return;
            }
        };

        // Create 100 leaked pipe pairs (200 FDs, no CLOEXEC).
        let mut leak_fds: Vec<RawFd> = Vec::new();
        for _ in 0..100 {
            let (r, w) = pipe();
            leak_fds.push(r);
            leak_fds.push(w);
        }

        let (capture_r, capture_w) = pipe_cloexec();

        match unsafe { nix::unistd::fork() }.expect("fork") {
            nix::unistd::ForkResult::Child => {
                // Don't close the leaked FDs — they should survive exec.
                unsafe { libc::close(capture_r) };
                unsafe { libc::dup2(capture_w, libc::STDOUT_FILENO) };
                unsafe { libc::close(capture_w) };

                let bin = CString::new(binary.as_str()).unwrap();
                let arg0 = CString::new(binary.as_str()).unwrap();
                let arg1 = CString::new("version").unwrap();
                nix::unistd::execv(&bin, &[&arg0, &arg1]).expect("execv agent");
                unreachable!();
            }
            nix::unistd::ForkResult::Parent { child } => {
                unsafe { libc::close(capture_w) };
                let output = read_all(capture_r);
                unsafe { libc::close(capture_r) };
                for fd in leak_fds {
                    unsafe { libc::close(fd) };
                }
                let status = wait_child(child);

                assert_eq!(
                    status, 0,
                    "agent should start despite 200 leaked FDs, exit code: {}",
                    status
                );
                assert!(
                    output.trim().contains('.'),
                    "agent should print version even with leaked FDs — got: {:?}",
                    output
                );
            }
        }
    }

    // -- test 10: exec from within tokio multi-thread runtime ----------

    /// Create a tokio multi-thread runtime with active background tasks,
    /// then fork+exec from within it. This simulates the agent's actual
    /// execution context when force_self_update triggers.
    ///
    /// The fork() happens from within a tokio worker thread (via
    /// spawn_blocking). After fork, the child has a single thread in a
    /// potentially corrupted runtime state — but it immediately exec's,
    /// replacing the entire process image.
    #[test]
    fn exec_from_tokio_multithreaded_runtime() {
        let rt = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(4)
            .enable_all()
            .build()
            .expect("build tokio runtime");

        // Spawn background tasks to simulate the agent's workload
        // (check-in loop, swarm, multicast, gitmirror reactor, etc.).
        for _ in 0..10 {
            rt.spawn(async {
                loop {
                    tokio::time::sleep(std::time::Duration::from_millis(10)).await;
                }
            });
        }

        // Also open some tokio TCP listeners and UDP sockets to simulate
        // the agent's network state.
        rt.block_on(async {
            let _listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.ok();
            let _udp = tokio::net::UdpSocket::bind("127.0.0.1:0").await.ok();
        });

        // Now fork+exec from within spawn_blocking — this matches the
        // code path in update::apply() where execv() is called.
        let result = rt.block_on(async {
            tokio::task::spawn_blocking(|| {
                let (capture_r, capture_w) = pipe();

                match unsafe { nix::unistd::fork() }.expect("fork from tokio") {
                    nix::unistd::ForkResult::Child => {
                        unsafe { libc::close(capture_r) };
                        unsafe { libc::dup2(capture_w, libc::STDOUT_FILENO) };
                        unsafe { libc::dup2(capture_w, libc::STDERR_FILENO) };
                        unsafe { libc::close(capture_w) };

                        let sh = CString::new("/bin/sh").unwrap();
                        let arg0 = CString::new("sh").unwrap();
                        let arg1 = CString::new("-c").unwrap();
                        let arg2 = CString::new(
                            "echo TOKIO_EXEC_OK; \
                             echo FD_COUNT=$(ls /proc/self/fd 2>/dev/null | wc -l)"
                        ).unwrap();
                        nix::unistd::execv(&sh, &[&arg0, &arg1, &arg2]).expect("execv");
                        unreachable!();
                    }
                    nix::unistd::ForkResult::Parent { child } => {
                        unsafe { libc::close(capture_w) };
                        let output = read_all(capture_r);
                        unsafe { libc::close(capture_r) };
                        let status = wait_child(child);
                        (output, status)
                    }
                }
            })
            .await
            .expect("spawn_blocking join")
        });

        let (output, status) = result;
        assert_eq!(status, 0, "child should exit cleanly after exec from tokio context");
        assert!(
            output.contains("TOKIO_EXEC_OK"),
            "exec from tokio multi-thread runtime should work — got: {:?}",
            output
        );
    }

    // -- test 11: exec from tokio with the actual agent binary ---------

    /// The closest simulation of the real bug scenario: a tokio runtime
    /// with background tasks fork+exec's into the actual agent binary.
    ///
    /// Skipped if the agent binary isn't built.
    #[test]
    fn agent_starts_after_exec_from_tokio_runtime() {
        let binary = match agent_binary_path() {
            Some(p) => p,
            None => {
                eprintln!("SKIPPED: agent binary not found");
                return;
            }
        };

        let rt = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(4)
            .enable_all()
            .build()
            .expect("build tokio runtime");

        // Background tasks.
        for _ in 0..10 {
            rt.spawn(async {
                loop {
                    tokio::time::sleep(std::time::Duration::from_millis(10)).await;
                }
            });
        }

        let result = rt.block_on(async {
            let bin = binary.clone();
            tokio::task::spawn_blocking(move || {
                let (capture_r, capture_w) = pipe();

                match unsafe { nix::unistd::fork() }.expect("fork") {
                    nix::unistd::ForkResult::Child => {
                        unsafe { libc::close(capture_r) };
                        unsafe { libc::dup2(capture_w, libc::STDOUT_FILENO) };
                        unsafe { libc::close(capture_w) };

                        let cbin = CString::new(bin.as_str()).unwrap();
                        let arg0 = CString::new(bin.as_str()).unwrap();
                        let arg1 = CString::new("version").unwrap();
                        nix::unistd::execv(&cbin, &[&arg0, &arg1]).expect("execv agent");
                        unreachable!();
                    }
                    nix::unistd::ForkResult::Parent { child } => {
                        unsafe { libc::close(capture_w) };
                        let output = read_all(capture_r);
                        unsafe { libc::close(capture_r) };
                        let status = wait_child(child);
                        (output, status)
                    }
                }
            })
            .await
            .expect("spawn_blocking join")
        });

        let (output, status) = result;
        assert_eq!(status, 0, "agent should start after exec from tokio runtime");
        let version = output.trim();
        assert!(
            !version.is_empty() && version.contains('.'),
            "agent should print version after exec from tokio runtime — got: {:?}",
            version
        );
    }

    // -- test 12: signal mask preserved across exec --------------------

    /// Verify that a blocked signal mask survives execv(). If the old
    /// agent process blocked signals (e.g. during critical sections),
    /// the new process inherits that mask, which could prevent signal
    /// handlers from working.
    #[test]
    fn signal_mask_survives_execv() {
        use nix::sys::signal::{SigSet, SigmaskHow, sigprocmask};

        let (capture_r, capture_w) = pipe();

        match unsafe { nix::unistd::fork() }.expect("fork") {
            nix::unistd::ForkResult::Child => {
                // Block SIGUSR1 in the child before exec.
                let mut set = SigSet::empty();
                set.add(nix::sys::signal::Signal::SIGUSR1);
                sigprocmask(SigmaskHow::SIG_BLOCK, Some(&set), None)
                    .expect("block SIGUSR1");

                unsafe { libc::close(capture_r) };
                unsafe { libc::dup2(capture_w, libc::STDOUT_FILENO) };
                unsafe { libc::close(capture_w) };

                // After exec, check if SIGUSR1 is still blocked.
                let sh = CString::new("/bin/sh").unwrap();
                let arg0 = CString::new("sh").unwrap();
                let arg1 = CString::new("-c").unwrap();
                // Read the signal mask from /proc/self/status.
                // SigBlk line shows blocked signals as a hex bitmask.
                // SIGUSR1 is signal 10 on Linux, bit 9 (0-indexed) → 0x200.
                let arg2 = CString::new(
                    "mask=$(grep SigBlk /proc/self/status | awk '{print $2}'); \
                     echo SIGMASK=$mask; \
                     if [ $((0x$mask & 0x200)) -ne 0 ]; then \
                         echo SIGUSR1_BLOCKED; \
                     else \
                         echo SIGUSR1_CLEAR; \
                     fi"
                ).unwrap();
                nix::unistd::execv(&sh, &[&arg0, &arg1, &arg2]).expect("execv");
                unreachable!();
            }
            nix::unistd::ForkResult::Parent { child } => {
                unsafe { libc::close(capture_w) };
                let output = read_all(capture_r);
                unsafe { libc::close(capture_r) };
                let status = wait_child(child);

                assert_eq!(status, 0);
                assert!(
                    output.contains("SIGUSR1_BLOCKED"),
                    "blocked signal mask should survive execv — got: {:?}",
                    output
                );
            }
        }
    }

    // -- test 13: environment variables survive exec -------------------

    /// Verify that environment variables set before exec are available
    /// in the new process. The agent relies on env vars for config
    /// (VIGO_DATA_DIR, etc.) and the update path preserves them via exec.
    #[test]
    fn environment_survives_execv() {
        let (capture_r, capture_w) = pipe();

        match unsafe { nix::unistd::fork() }.expect("fork") {
            nix::unistd::ForkResult::Child => {
                std::env::set_var("VIGO_EXEC_TEST_MARKER", "PRESENT");

                unsafe { libc::close(capture_r) };
                unsafe { libc::dup2(capture_w, libc::STDOUT_FILENO) };
                unsafe { libc::close(capture_w) };

                let sh = CString::new("/bin/sh").unwrap();
                let arg0 = CString::new("sh").unwrap();
                let arg1 = CString::new("-c").unwrap();
                let arg2 = CString::new(
                    "echo ENV_MARKER=$VIGO_EXEC_TEST_MARKER"
                ).unwrap();
                nix::unistd::execv(&sh, &[&arg0, &arg1, &arg2]).expect("execv");
                unreachable!();
            }
            nix::unistd::ForkResult::Parent { child } => {
                unsafe { libc::close(capture_w) };
                let output = read_all(capture_r);
                unsafe { libc::close(capture_r) };
                let status = wait_child(child);

                assert_eq!(status, 0);
                assert!(
                    output.contains("ENV_MARKER=PRESENT"),
                    "environment should survive execv — got: {:?}",
                    output
                );
            }
        }
    }
}
