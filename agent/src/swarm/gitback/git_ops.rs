// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Hardened wrapper around `git` subprocess invocations.
//!
//! This is the only place in the gitback subsystem that spawns the
//! `git` binary. Every invocation goes through [`build_command`]
//! which applies a fixed set of hardening args + env vars and
//! requires the caller to specify privilege explicitly via [`RunAs`].
//!
//! The minimum supported git version is 2.35. Earlier versions have
//! known symlink and `safe.directory` CVEs that these mitigations
//! cannot fully paper over.

use std::process::Command;
use thiserror::Error;

/// Minimum supported git major version.
pub const MIN_GIT_MAJOR: u32 = 2;
/// Minimum supported git minor version.
pub const MIN_GIT_MINOR: u32 = 35;

/// Privilege mode for a git invocation.
#[derive(Debug, Clone, Copy)]
pub enum RunAs {
    /// Run as root (the agent's own identity). Use only for
    /// receiver-side operations that write into the root-owned bare
    /// repo (bundle verify, bundle unbundle, init bare).
    Root,
    /// Run as whoever invoked the CLI — no privilege transition. Use
    /// for CLI verbs operating on the caller's own working repo
    /// (`project init` / `project push` running `git remote add` +
    /// `git push` in the operator's checkout). Same env hardening as
    /// [`RunAs::Root`] — neutralizes the user's hooks during the
    /// automated push and never blocks on an interactive prompt — but
    /// no `uid`/`gid` change and no `env_clear`, since we're already
    /// the right user.
    Caller,
    /// Drop to a target user before `exec()` via
    /// `std::os::unix::process::CommandExt::uid()` / `gid()`.
    /// Use for source-side `git bundle create` and receiver-side
    /// `git checkout -f` against a user-owned working tree.
    User { uid: u32, gid: u32 },
}

#[derive(Debug, Error)]
pub enum GitOpsError {
    #[error("failed to run `git --version`: {0}")]
    VersionRun(#[source] std::io::Error),
    #[error("`git --version` exited with status {0}")]
    VersionStatus(String),
    #[error("could not parse git version from output: {0:?}")]
    VersionParse(String),
    #[error(
        "git version {major}.{minor} is below the required minimum {MIN_GIT_MAJOR}.{MIN_GIT_MINOR}"
    )]
    VersionTooOld { major: u32, minor: u32 },
}

/// Build a hardened `git` Command ready for the caller to append
/// subcommand + arguments.
///
/// Applies to every invocation:
///
/// - `-c core.hooksPath=/dev/null` — neutralize hooks.
/// - `-c filter.*.smudge=cat` / `-c filter.*.clean=cat` — neutralize
///   gitattributes filter exec via wildcard clobber.
/// - `-c core.symlinks=true -c core.protectHFS=true -c core.protectNTFS=true`
///   — symlink/path-confusion hardening at checkout time.
/// - `-c protocol.*.allow=never` — refuse all submodule URL schemes.
/// - `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_SYSTEM=/dev/null` —
///   prevent user/system git config injection.
/// - `GIT_TERMINAL_PROMPT=0` — never block on interactive prompts.
///
/// Privilege drop via [`RunAs::User`] is delegated to
/// [`crate::platform::users::run_as_user`], which also calls
/// `env_clear()` and re-injects the hardening env vars plus a
/// minimal `HOME`/`USER`/`PATH` set. When [`RunAs::Root`] is used,
/// the hardening env vars are applied here directly.
pub fn build_command(run_as: RunAs) -> Command {
    let mut cmd = Command::new("git");

    // Hardening args must come before any subcommand.
    cmd.args([
        "-c",
        "core.hooksPath=/dev/null",
        "-c",
        "filter.*.smudge=cat",
        "-c",
        "filter.*.clean=cat",
        "-c",
        "core.symlinks=true",
        "-c",
        "core.protectHFS=true",
        "-c",
        "core.protectNTFS=true",
        "-c",
        "protocol.*.allow=never",
    ]);

    match run_as {
        RunAs::Root | RunAs::Caller => {
            cmd.env("GIT_CONFIG_GLOBAL", "/dev/null");
            cmd.env("GIT_CONFIG_SYSTEM", "/dev/null");
            cmd.env("GIT_TERMINAL_PROMPT", "0");
        }
        RunAs::User { uid, gid } => {
            crate::platform::users::run_as_user(&mut cmd, uid, gid);
        }
    }

    cmd
}

/// Check that `git --version` reports a version ≥
/// [`MIN_GIT_MAJOR`].[`MIN_GIT_MINOR`]. Called once on agent startup;
/// the subsystem caches the result.
pub fn check_version() -> Result<(u32, u32), GitOpsError> {
    let output = Command::new("git")
        .arg("--version")
        .output()
        .map_err(GitOpsError::VersionRun)?;

    if !output.status.success() {
        return Err(GitOpsError::VersionStatus(output.status.to_string()));
    }

    let text = String::from_utf8_lossy(&output.stdout);
    let (major, minor) = parse_version(&text)
        .ok_or_else(|| GitOpsError::VersionParse(text.trim().to_string()))?;

    if major < MIN_GIT_MAJOR || (major == MIN_GIT_MAJOR && minor < MIN_GIT_MINOR) {
        return Err(GitOpsError::VersionTooOld { major, minor });
    }

    Ok((major, minor))
}

/// Parse `"git version X.Y.Z..."` into `(major, minor)`.
/// Handles distro suffixes like `2.35.1-ubuntu1`.
fn parse_version(text: &str) -> Option<(u32, u32)> {
    let rest = text.trim().strip_prefix("git version ")?;
    let mut parts = rest.split('.');
    let major: u32 = parts.next()?.parse().ok()?;
    let minor_str = parts.next()?;
    let minor_digits: String = minor_str
        .chars()
        .take_while(|c| c.is_ascii_digit())
        .collect();
    if minor_digits.is_empty() {
        return None;
    }
    let minor: u32 = minor_digits.parse().ok()?;
    Some((major, minor))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_standard_version() {
        assert_eq!(parse_version("git version 2.35.1"), Some((2, 35)));
        assert_eq!(parse_version("git version 2.44.0"), Some((2, 44)));
        assert_eq!(parse_version("git version 2.35.1-ubuntu1"), Some((2, 35)));
    }

    #[test]
    fn parses_two_component_version() {
        assert_eq!(parse_version("git version 2.35"), Some((2, 35)));
    }

    #[test]
    fn parses_with_trailing_newline() {
        assert_eq!(parse_version("git version 2.40.2\n"), Some((2, 40)));
    }

    #[test]
    fn rejects_malformed() {
        assert_eq!(parse_version("garbage"), None);
        assert_eq!(parse_version(""), None);
        assert_eq!(parse_version("git version"), None);
        assert_eq!(parse_version("git version foo.bar"), None);
        assert_eq!(parse_version("git version 2"), None);
    }

    #[test]
    fn build_command_applies_hardening_args() {
        let cmd = build_command(RunAs::Root);
        let args: Vec<String> = cmd
            .get_args()
            .map(|s| s.to_string_lossy().into_owned())
            .collect();

        assert!(args.iter().any(|a| a == "core.hooksPath=/dev/null"));
        assert!(args.iter().any(|a| a == "filter.*.smudge=cat"));
        assert!(args.iter().any(|a| a == "filter.*.clean=cat"));
        assert!(args.iter().any(|a| a == "core.symlinks=true"));
        assert!(args.iter().any(|a| a == "core.protectHFS=true"));
        assert!(args.iter().any(|a| a == "core.protectNTFS=true"));
        assert!(args.iter().any(|a| a == "protocol.*.allow=never"));
    }

    #[test]
    fn build_command_root_applies_env_hardening() {
        let cmd = build_command(RunAs::Root);
        let envs: Vec<(String, String)> = cmd
            .get_envs()
            .filter_map(|(k, v)| {
                v.map(|v| {
                    (
                        k.to_string_lossy().into_owned(),
                        v.to_string_lossy().into_owned(),
                    )
                })
            })
            .collect();
        assert!(envs
            .iter()
            .any(|(k, v)| k == "GIT_CONFIG_GLOBAL" && v == "/dev/null"));
        assert!(envs
            .iter()
            .any(|(k, v)| k == "GIT_CONFIG_SYSTEM" && v == "/dev/null"));
        assert!(envs
            .iter()
            .any(|(k, v)| k == "GIT_TERMINAL_PROMPT" && v == "0"));
    }

    #[test]
    fn build_command_caller_matches_root_env_hardening() {
        let cmd = build_command(RunAs::Caller);
        let envs: Vec<(String, String)> = cmd
            .get_envs()
            .filter_map(|(k, v)| {
                v.map(|v| {
                    (
                        k.to_string_lossy().into_owned(),
                        v.to_string_lossy().into_owned(),
                    )
                })
            })
            .collect();
        assert!(envs
            .iter()
            .any(|(k, v)| k == "GIT_CONFIG_GLOBAL" && v == "/dev/null"));
        assert!(envs
            .iter()
            .any(|(k, v)| k == "GIT_CONFIG_SYSTEM" && v == "/dev/null"));
        assert!(envs
            .iter()
            .any(|(k, v)| k == "GIT_TERMINAL_PROMPT" && v == "0"));
        // No uid/gid override — Command exposes that via get_uid()/get_gid(),
        // which are nightly-only; instead just confirm the hardening args land.
        let args: Vec<String> = cmd
            .get_args()
            .map(|s| s.to_string_lossy().into_owned())
            .collect();
        assert!(args.iter().any(|a| a == "core.hooksPath=/dev/null"));
    }

    #[test]
    #[cfg(unix)]
    fn build_command_user_applies_env_hardening() {
        let cmd = build_command(RunAs::User { uid: 1000, gid: 1000 });
        let envs: Vec<(String, String)> = cmd
            .get_envs()
            .filter_map(|(k, v)| {
                v.map(|v| {
                    (
                        k.to_string_lossy().into_owned(),
                        v.to_string_lossy().into_owned(),
                    )
                })
            })
            .collect();
        // run_as_user should have injected these.
        assert!(envs
            .iter()
            .any(|(k, v)| k == "GIT_CONFIG_GLOBAL" && v == "/dev/null"));
        assert!(envs
            .iter()
            .any(|(k, v)| k == "GIT_CONFIG_SYSTEM" && v == "/dev/null"));
    }

    #[test]
    fn version_check_runs_real_git_if_available() {
        // Informational: exercises check_version() against whatever
        // git the host has. We don't fail the test on VersionTooOld
        // because dev machines routinely lag behind the MVP floor
        // (the floor is a runtime requirement for gitback operation,
        // not a build-time requirement for running the test suite).
        // The parse_version unit tests cover the logic; this test
        // only confirms the subprocess plumbing works end-to-end.
        match check_version() {
            Ok((major, minor)) => {
                assert!(major > 0);
                println!("detected git {}.{} (meets MVP floor)", major, minor);
            }
            Err(GitOpsError::VersionRun(_)) => {
                println!("git not available in test environment, skipping");
            }
            Err(GitOpsError::VersionTooOld { major, minor }) => {
                println!(
                    "host git {}.{} is below MVP floor {}.{} — that's fine for test, \
                     the runtime check correctly rejected it",
                    major, minor, MIN_GIT_MAJOR, MIN_GIT_MINOR
                );
            }
            Err(e) => panic!("unexpected version check error: {:?}", e),
        }
    }
}
