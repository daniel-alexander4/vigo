// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Integration test pinning the gitback helper-daemon transport.
//!
//! Spawns the daemon on a per-test temp Unix socket (override via
//! `VIGO_GITBACK_SOCKET_PATH`), generates a self-contained puddle
//! keypair and bootstrap token chain, and runs `git push` + `git
//! clone` against a temp bare repo via `gitback://` URLs. The whole
//! round-trip finishes in well under a second on a working build.
//!
//! The 0.41.7-18 AF_UNIX `unix_stream_data_wait` wakeup-race regression
//! would surface here as a `git` invocation that doesn't return within
//! the per-call 5-second timeout. Any future change that re-introduces
//! synchronous-blocking I/O on the unix socket — on either side of the
//! transport — fails this test loudly instead of shipping.
//!
//! Skips with a clear message if the `git` binary isn't on `PATH`.

use std::path::Path;
use std::process::Command;
use std::time::Duration;

use tempfile::TempDir;
use tokio_util::sync::CancellationToken;

use vigo::swarm::anchor::tokens::{issue_bootstrap, project_id_for};
use vigo::swarm::gitback::project_state::{
    hex_id, DrScope, ProjectConfig, TokenChain, PROJECT_STATE_VERSION,
};

fn git_available() -> bool {
    Command::new("git")
        .arg("--version")
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn helper_daemon_push_and_clone_roundtrip() {
    if !git_available() {
        eprintln!("skipping gitback_helper integration test: `git` not on PATH");
        return;
    }

    let tmp = TempDir::new().expect("tempdir");
    let home = tmp.path().join("home");
    let base = tmp.path().join("gitback-base");
    let sock = tmp.path().join("gitback.sock");
    let passwd = tmp.path().join("passwd");
    let bin_dir = tmp.path().join("bin");
    let src_repo = tmp.path().join("src");
    let clone_dir = tmp.path().join("clone");

    std::fs::create_dir_all(&home).unwrap();
    std::fs::create_dir_all(&base).unwrap();
    std::fs::create_dir_all(&bin_dir).unwrap();
    std::fs::create_dir_all(&src_repo).unwrap();

    // Spoof passwd so `home_for_uid` resolves the test process's UID
    // to our tempdir home rather than the operator's real $HOME.
    let uid = unsafe { libc::geteuid() };
    std::fs::write(
        &passwd,
        format!("test:x:{}:0::{}:/bin/sh\n", uid, home.display()),
    )
    .unwrap();

    // Process-global env so both the in-process daemon and the
    // child `git-remote-gitback` (spawned by `git`) pick up the
    // override paths.
    std::env::set_var("VIGO_GITBACK_SOCKET_PATH", &sock);
    std::env::set_var("VIGO_GITBACK_DIR", &base);
    std::env::set_var("VIGO_GITBACK_PASSWD_FILE", &passwd);

    // Founder identity.
    let mut rng = rand::rngs::OsRng;
    let signing_key = ed25519_dalek::SigningKey::generate(&mut rng);
    let founder_pub = signing_key.verifying_key().to_bytes();
    let project_id = project_id_for(&founder_pub, "proj");

    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs() as i64;
    let bootstrap = issue_bootstrap(&signing_key, "proj", now, None).expect("bootstrap token");

    // Per-user project state under the spoofed home.
    let cfg = ProjectConfig {
        version: PROJECT_STATE_VERSION,
        project_id: hex_id(&project_id),
        handle: "test-project".to_string(),
        founder_puddle_pubkey: hex::encode(founder_pub),
        members: vec![hex::encode(founder_pub)],
        dr_scope: DrScope::MembersOnly,
        cross_subnet_members: Vec::new(),
        updated_at: now,
        tombstoned_at: None,
    };
    cfg.save(&home).expect("save project.json");
    let chain = TokenChain {
        version: PROJECT_STATE_VERSION,
        chain: vec![bootstrap],
    };
    chain.save(&home, &project_id).expect("save tokens.json");

    // Spawn the helper daemon. tls=None disables fan-out; this test
    // is exclusively about the local push/clone transport.
    let cancel = CancellationToken::new();
    let daemon_cancel = cancel.clone();
    let daemon =
        tokio::spawn(async move { vigo::swarm::gitback::helper::run(daemon_cancel, None).await });

    // Bound the bind wait so a stuck listener fails fast.
    let bind_deadline = std::time::Instant::now() + Duration::from_secs(2);
    while !sock.exists() {
        if std::time::Instant::now() >= bind_deadline {
            panic!("helper daemon did not bind {} within 2s", sock.display());
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }

    // Symlink the test binary as `git-remote-gitback` so git's transport
    // dispatch finds the multicall handler. CARGO_BIN_EXE_vigo is set
    // by Cargo for integration tests in a crate that builds a binary.
    let target = env!("CARGO_BIN_EXE_vigo");
    let link = bin_dir.join("git-remote-gitback");
    std::os::unix::fs::symlink(target, &link).expect("symlink git-remote-gitback");
    let path = format!(
        "{}:{}",
        bin_dir.display(),
        std::env::var("PATH").unwrap_or_default()
    );

    // Build a tiny source repo with one commit.
    git_must(&src_repo, &["init", "-q", "-b", "master"], &path);
    std::fs::write(src_repo.join("c1.txt"), "hello\n").unwrap();
    git_must(&src_repo, &["config", "user.email", "test@test"], &path);
    git_must(&src_repo, &["config", "user.name", "test"], &path);
    git_must(&src_repo, &["add", "c1.txt"], &path);
    git_must(&src_repo, &["commit", "-qm", "c1"], &path);

    let url = format!("gitback://{}/test-project", hex_id(&project_id));

    // Push. With the AF_UNIX wakeup-race active, this hangs > 30s.
    let push = run_with_timeout(
        Duration::from_secs(5),
        Command::new("git")
            .arg("-C")
            .arg(&src_repo)
            .args(["push", &url, "master"])
            .env("PATH", &path),
        "git push",
    )
    .await;
    assert!(
        push.status.success(),
        "git push failed: stdout={:?} stderr={:?}",
        String::from_utf8_lossy(&push.stdout),
        String::from_utf8_lossy(&push.stderr),
    );

    // Clone. Same deadlock risk class as push.
    let clone = run_with_timeout(
        Duration::from_secs(5),
        Command::new("git")
            .args(["clone", &url])
            .arg(&clone_dir)
            .env("PATH", &path),
        "git clone",
    )
    .await;
    assert!(
        clone.status.success(),
        "git clone failed: stdout={:?} stderr={:?}",
        String::from_utf8_lossy(&clone.stdout),
        String::from_utf8_lossy(&clone.stderr),
    );

    // The cloned repo may or may not have a working tree depending on
    // the bare's default-branch config (it's lazily inited without
    // --initial-branch, so HEAD may not point at master). Verify the
    // round-trip via the object DB directly — that's what we actually
    // care about: the bytes survived the transport.
    let blob = Command::new("git")
        .arg("-C")
        .arg(&clone_dir)
        .args(["cat-file", "-p", "refs/remotes/origin/master:c1.txt"])
        .env("PATH", &path)
        .output()
        .expect("cat-file");
    assert!(
        blob.status.success(),
        "cat-file origin/master:c1.txt failed: stderr={:?}",
        String::from_utf8_lossy(&blob.stderr),
    );
    assert_eq!(blob.stdout, b"hello\n");

    // `STATUS` verb — the path `vigo swarm gitback status` uses (an
    // unprivileged caller can't stat the `0700` bare repo, so it asks
    // the daemon). Reply is `OK <compact-json-array>\n`.
    {
        use std::io::{BufRead, BufReader, Write};
        use std::os::unix::net::UnixStream;

        let mut s = UnixStream::connect(&sock).expect("connect for STATUS");
        s.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
        s.write_all(b"STATUS\n").expect("send STATUS");
        s.flush().ok();
        let mut line = String::new();
        BufReader::new(s)
            .read_line(&mut line)
            .expect("read STATUS reply");
        let line = line.trim_end_matches(['\n', '\r']);
        let json = line
            .strip_prefix("OK ")
            .unwrap_or_else(|| panic!("STATUS reply not OK: {line:?}"));
        let v: serde_json::Value = serde_json::from_str(json).expect("parse STATUS JSON");
        let arr = v.as_array().expect("STATUS reply is a JSON array");
        let p = arr
            .iter()
            .find(|p| p["handle"] == "test-project")
            .unwrap_or_else(|| panic!("STATUS did not list test-project: {v}"));
        assert_eq!(
            p["has_bare_repo"],
            serde_json::json!(true),
            "STATUS should report the pushed bare repo present"
        );
        assert!(p["bare_repo_size_bytes"].as_u64().unwrap_or(0) > 0);
        assert!(
            p["head_refs"].get("refs/heads/master").is_some(),
            "head_refs: {}",
            p["head_refs"]
        );
    }

    cancel.cancel();
    let _ = daemon.await;
}

fn git_must(repo: &Path, args: &[&str], path: &str) {
    let out = Command::new("git")
        .arg("-C")
        .arg(repo)
        .args(args)
        .env("PATH", path)
        .output()
        .unwrap_or_else(|e| panic!("git {:?}: {}", args, e));
    assert!(
        out.status.success(),
        "git {:?} failed: stdout={:?} stderr={:?}",
        args,
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr),
    );
}

/// Run a `Command` to completion under a wall-clock timeout. Panics
/// with a descriptive message if the timeout fires — the panic message
/// is the regression signal we want to surface ("git push timed out
/// — AF_UNIX wakeup race regression?").
async fn run_with_timeout(
    timeout: Duration,
    cmd: &mut Command,
    label: &str,
) -> std::process::Output {
    // Snapshot the command into something owned so we can move it
    // into the spawn_blocking closure. Easiest: capture program +
    // args + env explicitly.
    let program = cmd.get_program().to_os_string();
    let args: Vec<_> = cmd.get_args().map(|a| a.to_os_string()).collect();
    let cwd = cmd.get_current_dir().map(|p| p.to_path_buf());
    let envs: Vec<_> = cmd
        .get_envs()
        .filter_map(|(k, v)| v.map(|vv| (k.to_os_string(), vv.to_os_string())))
        .collect();

    let label_owned = label.to_string();
    let join = tokio::time::timeout(
        timeout,
        tokio::task::spawn_blocking(move || {
            let mut c = Command::new(&program);
            c.args(&args);
            if let Some(d) = cwd {
                c.current_dir(d);
            }
            for (k, v) in envs {
                c.env(k, v);
            }
            c.output().expect("spawn")
        }),
    )
    .await
    .unwrap_or_else(|_| {
        panic!(
            "{} timed out — AF_UNIX wakeup race regression?",
            label_owned
        )
    });
    join.expect("join")
}
