// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary
//
// Integration test for ADR-015 multi-call binary dispatch.
//
// Cargo's integration-test runner builds the `vigo` binary and lets us
// invoke it like any other process. The dispatch we want to verify
// inspects argv[0] *before* any clap parsing — so the test invokes
// the binary with `Command::arg0("git-remote-vigo")` and checks that
// the process exits with the stub's distinctive exit code (78 / EX_CONFIG)
// and emits the marker error line on stderr. Both signals together
// rule out "we accidentally fell through to normal CLI parsing,
// which then exited with status 2 because positionals didn't parse."

#![cfg(unix)]

use std::os::unix::process::CommandExt;
use std::process::Command;

/// Locate the test binary the same way Cargo's `CARGO_BIN_EXE_<name>`
/// env var does. Set by the test harness before running.
fn vigo_bin() -> String {
    env!("CARGO_BIN_EXE_vigo").to_string()
}

#[test]
fn argv0_git_remote_vigo_enters_stub() {
    let bin = vigo_bin();
    let mut cmd = Command::new(&bin);
    cmd.arg0("git-remote-vigo");
    // Suppress stdin so the helper-protocol stub can't block waiting
    // for input; the stub doesn't read stdin today, but pin the
    // expectation that the test never hangs.
    cmd.stdin(std::process::Stdio::null());

    let output = cmd.output().expect("failed to run vigo");
    let stderr = String::from_utf8_lossy(&output.stderr);
    let code = output.status.code();

    assert_eq!(
        code,
        Some(vigo::multicall::STUB_EXIT_CODE),
        "expected exit code {:?}, got {:?}; stderr was: {}",
        vigo::multicall::STUB_EXIT_CODE,
        code,
        stderr
    );
    assert!(
        stderr.contains("ADR-015"),
        "stderr should mention ADR-015 so the user knows what's missing; got: {}",
        stderr
    );
    assert!(
        stderr.contains("git-remote-vigo"),
        "stderr should name the dispatch mode for clarity; got: {}",
        stderr
    );
}

#[test]
fn argv0_default_falls_through_to_cli() {
    // Sanity check: invoking with the normal `vigo` argv[0] but no
    // command should give the regular CLI's no-command behavior, NOT
    // the multi-call stub's exit code. The exact non-zero code is
    // less important than "it isn't 78."
    let bin = vigo_bin();
    let mut cmd = Command::new(&bin);
    cmd.arg0("vigo");
    cmd.stdin(std::process::Stdio::null());

    let output = cmd.output().expect("failed to run vigo");
    let code = output.status.code();
    assert_ne!(
        code,
        Some(vigo::multicall::STUB_EXIT_CODE),
        "default invocation should not enter the multi-call stub; \
         exit code {:?} suggests the dispatch matched too eagerly",
        code
    );
}

#[test]
fn argv0_via_symlink_path_enters_stub() {
    // Match the package-recipe shape: a symlink at
    // `/usr/local/bin/git-remote-vigo` → the real `vigo` binary.
    // We can't necessarily write into /usr/local in a test env, so
    // create the symlink in a tempdir and invoke through it. This
    // is the closest the integration test can come to the
    // production install layout.
    let bin = vigo_bin();
    let tmp = tempfile::tempdir().expect("tempdir");
    let link = tmp.path().join("git-remote-vigo");
    std::os::unix::fs::symlink(&bin, &link).expect("symlink");

    let mut cmd = Command::new(&link);
    cmd.stdin(std::process::Stdio::null());

    let output = cmd.output().expect("failed to run vigo via symlink");
    let stderr = String::from_utf8_lossy(&output.stderr);
    let code = output.status.code();

    assert_eq!(
        code,
        Some(vigo::multicall::STUB_EXIT_CODE),
        "symlink invocation should hit the stub; got code {:?}, stderr: {}",
        code,
        stderr
    );
}
