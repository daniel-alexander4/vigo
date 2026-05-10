// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary
//
// Integration test for ADR-015 multi-call binary dispatch.
//
// Cargo's integration-test runner builds the `vigo` binary and lets us
// invoke it like any other process. The dispatch we want to verify
// inspects argv[0] *before* any clap parsing — so the test invokes
// the binary with the regular `vigo` argv[0] and asserts the process
// does NOT match the git-remote-swarmgit dispatch path. End-to-end
// dispatch (argv[0] = "git-remote-swarmgit" via symlink → real handler
// runs the protocol loop with git → push/clone succeed) is covered
// by `agent/tests/swarmgit_helper.rs`.

#![cfg(unix)]

use std::os::unix::process::CommandExt;
use std::process::Command;

/// Locate the test binary the same way Cargo's `CARGO_BIN_EXE_<name>`
/// env var does. Set by the test harness before running.
fn vigo_bin() -> String {
    env!("CARGO_BIN_EXE_vigo").to_string()
}

#[test]
fn argv0_default_falls_through_to_cli() {
    // Sanity check: invoking with the normal `vigo` argv[0] and no
    // subcommand should give the regular CLI's no-command behavior,
    // NOT enter the git-remote-swarmgit dispatch (which expects a
    // `swarmgit://` URL and would error differently). Catches a
    // regression where multicall canonicalization matches too
    // eagerly on the default name.
    let bin = vigo_bin();
    let mut cmd = Command::new(&bin);
    cmd.arg0("vigo");
    cmd.stdin(std::process::Stdio::null());

    let output = cmd.output().expect("failed to run vigo");
    let stderr = String::from_utf8_lossy(&output.stderr);

    // The git-remote-swarmgit handler exits with this exact prefix when
    // invoked without a swarmgit:// URL. If default invocation hit that
    // path, stderr would carry that string.
    assert!(
        !stderr.contains("expected swarmgit:// URL"),
        "default invocation should not enter git-remote-swarmgit dispatch; \
         stderr suggests it did: {}",
        stderr
    );
}
