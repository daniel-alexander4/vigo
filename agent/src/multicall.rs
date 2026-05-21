// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Multi-call binary dispatch (ADR-015).
//!
//! The `vigo` agent binary ships as a multi-call binary: when invoked
//! under a different argv[0] (typically via a symlink), it switches
//! into a different mode without having to maintain a second
//! executable. ADR-015 introduces this for `git-remote-gitback` — git's
//! remote-helper protocol needs an executable named exactly that to
//! handle `gitback://` URLs.
//!
//! Dispatch is the **very first thing** main() runs, before any clap
//! parsing, before any logger setup. Two failure modes the ADR
//! flagged that this layout addresses:
//!
//! 1. The check has to live ahead of any code that would reject
//!    "unknown" arguments — git invokes us as
//!    `git-remote-gitback <remote-name> <url>`, which would not survive
//!    the regular CLI parser.
//! 2. The argv[0] inspection has to canonicalize: strip directory
//!    prefix, strip `.exe` suffix on Windows-built binaries, then
//!    match against an explicit table. A blanket `contains("git-remote-gitback")`
//!    would be both too permissive and too brittle.
//!
//! ADR-015 step 4 wired the real body in
//! [`crate::swarm::gitback::remote_helper::run`]: speak the git
//! remote-helper protocol on stdin/stdout, declare the `connect`
//! capability, and on `connect <service>` open
//! `/var/run/vigo/gitback.sock`, send the
//! `HELLO <project_id_hex> <service>` handshake, and splice the
//! socket transparently to git's stdio. The agent on the other end
//! verifies the user's token chain and proxies to
//! `git-upload-pack` / `git-receive-pack`.

use std::ffi::OsStr;
use std::path::Path;

/// Modes selectable via argv[0]. Returns `None` for normal `vigo` /
/// `vigo-*` invocations — the caller falls through to clap parsing.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MulticallMode {
    /// `git-remote-gitback` — git remote helper for `gitback://` URLs.
    /// Bound to ADR-015's git transport layer.
    GitRemoteGitback,
}

/// Inspect argv[0] and decide whether we're being invoked under a
/// non-default name. Returns `Some(mode)` for known multicall names
/// or `None` for normal CLI dispatch.
///
/// Canonicalization:
/// - Strip directory components (`/usr/local/bin/git-remote-gitback` →
///   `git-remote-gitback`).
/// - Strip a `.exe` suffix (Windows builds — Phase 1 of ADR-015 is
///   Linux + macOS, but the dispatch shape stays consistent).
/// - Lowercase the result for case-insensitive matching on macOS HFS+
///   case-insensitive filesystems.
pub fn classify_argv0(argv0: &OsStr) -> Option<MulticallMode> {
    let s = argv0.to_str()?;
    // Strip BOTH `/` and `\` separators so a Windows-style path
    // canonicalizes correctly on a Linux host (matters for tests and
    // for the eventual cross-platform port). `Path::file_name`
    // alone is platform-dependent — it splits on `\` only when
    // running on Windows.
    let basename = s
        .rsplit(['/', '\\'])
        .next()
        .unwrap_or(s);
    // Drop empty + bare-dot inputs that survived the rsplit (e.g.
    // `""`, `.`, `path/`). `Path::file_name` returns None for these
    // and we want the same behavior.
    if basename.is_empty() || basename == "." || basename == ".." {
        // Defensive: if the path ended in a separator, the trailing
        // empty component tricks rsplit. Re-check via Path::file_name
        // to catch the platform-native case correctly.
        let alt = Path::new(s).file_name().and_then(|n| n.to_str())?;
        return classify_basename(alt);
    }
    classify_basename(basename)
}

fn classify_basename(basename: &str) -> Option<MulticallMode> {
    let stripped = basename.strip_suffix(".exe").unwrap_or(basename);
    match stripped.to_ascii_lowercase().as_str() {
        "git-remote-gitback" => Some(MulticallMode::GitRemoteGitback),
        _ => None,
    }
}

/// Dispatch entry point. Runs the git remote-helper protocol loop
/// and exits when the tunnel closes (or the daemon refuses). Always
/// terminates the process — never returns.
pub fn run_git_remote_gitback(args: Vec<String>) -> ! {
    crate::swarm::gitback::remote_helper::run(args);
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::ffi::OsString;

    fn os(s: &str) -> OsString {
        OsString::from(s)
    }

    #[test]
    fn plain_basename_matches() {
        assert_eq!(
            classify_argv0(&os("git-remote-gitback")),
            Some(MulticallMode::GitRemoteGitback)
        );
    }

    #[test]
    fn absolute_path_strips_dirs() {
        assert_eq!(
            classify_argv0(&os("/usr/local/bin/git-remote-gitback")),
            Some(MulticallMode::GitRemoteGitback)
        );
    }

    #[test]
    fn relative_path_strips_dirs() {
        assert_eq!(
            classify_argv0(&os("./git-remote-gitback")),
            Some(MulticallMode::GitRemoteGitback)
        );
        assert_eq!(
            classify_argv0(&os("../bin/git-remote-gitback")),
            Some(MulticallMode::GitRemoteGitback)
        );
    }

    #[test]
    fn exe_suffix_stripped() {
        // Windows multi-call dispatch is deferred to a later ticket
        // (Linux-first doctrine), but the basename canonicalizer
        // already handles it so when the Windows port lands we
        // don't have to revisit this.
        assert_eq!(
            classify_argv0(&os("git-remote-gitback.exe")),
            Some(MulticallMode::GitRemoteGitback)
        );
        assert_eq!(
            classify_argv0(&os("C:\\Program Files\\Vigo\\git-remote-gitback.exe")),
            Some(MulticallMode::GitRemoteGitback)
        );
    }

    #[test]
    fn case_insensitive_match() {
        // macOS HFS+ default is case-insensitive; the kernel hands us
        // whatever case the caller used in their PATH lookup.
        assert_eq!(
            classify_argv0(&os("Git-Remote-Gitback")),
            Some(MulticallMode::GitRemoteGitback)
        );
        assert_eq!(
            classify_argv0(&os("GIT-REMOTE-GITBACK")),
            Some(MulticallMode::GitRemoteGitback)
        );
    }

    #[test]
    fn vigo_default_returns_none() {
        // The agent binary itself — fall through to CLI parsing.
        assert_eq!(classify_argv0(&os("vigo")), None);
        assert_eq!(classify_argv0(&os("/usr/local/sbin/vigo")), None);
    }

    #[test]
    fn rejects_partial_matches() {
        // No `contains()` heuristic — must match exactly.
        assert_eq!(classify_argv0(&os("git-remote-gitback-something")), None);
        assert_eq!(classify_argv0(&os("xgit-remote-gitback")), None);
        assert_eq!(classify_argv0(&os("git-remote")), None);
        assert_eq!(classify_argv0(&os("git-remote-foo")), None);
    }

    #[test]
    fn empty_or_dot_returns_none() {
        assert_eq!(classify_argv0(&os("")), None);
        // `.` has no file_name component — Path::file_name returns None.
        assert_eq!(classify_argv0(&os(".")), None);
    }
}
