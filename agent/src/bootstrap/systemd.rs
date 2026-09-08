// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Systemd service installation for the Vigo agent.

use anyhow::{bail, Context, Result};
use std::path::Path;
use tracing::info;

const INSTALL_PATH: &str = "/usr/local/sbin/vigo";
const SERVICE_NAME: &str = "vigo-envoy";
const UNIT_PATH: &str = "/etc/systemd/system/vigo-envoy.service";

/// Derive the gRPC address from a bootstrap server URL and gRPC port.
pub fn derive_grpc_addr(bootstrap_server: &str, grpc_port: &str) -> String {
    super::derive_grpc_addr(bootstrap_server, grpc_port)
}

/// Path to the installed unit file. Exported so the upgrade path
/// (`update::apply`) can re-write it on every binary upgrade — see
/// [`refresh_unit_file_if_changed`].
pub const UNIT_FILE_PATH: &str = UNIT_PATH;

/// Generate the systemd unit file content.
/// The agent reads server address from the config file written during bootstrap.
fn unit_file() -> String {
    format!(
        "[Unit]\n\
         Description=Vigo Configuration Management Agent\n\
         After=network.target\n\
         Wants=network-online.target\n\
         \n\
         [Service]\n\
         Type=simple\n\
         ExecStart={}\n\
         Restart=always\n\
         RestartSec=10\n\
         TimeoutStopSec=15\n\
         LimitNOFILE=65536\n\
         \n\
         [Install]\n\
         WantedBy=multi-user.target\n",
        INSTALL_PATH
    )
}

/// Install and start the systemd service. Returns the gRPC address used.
pub async fn install_service(
    bootstrap_server: &str,
    grpc_port: &str,
) -> Result<String> {
    // Check systemctl exists.
    if !Path::new("/usr/bin/systemctl").exists() && !Path::new("/bin/systemctl").exists() {
        bail!("systemctl not found");
    }

    let grpc_addr = derive_grpc_addr(bootstrap_server, grpc_port);

    // Copy running binary to /usr/local/sbin/vigo.
    let src_path = std::env::current_exe().context("resolve executable path")?;
    let src_path = std::fs::canonicalize(&src_path).context("resolve symlinks")?;
    if src_path.to_str() != Some(INSTALL_PATH) {
        std::fs::copy(&src_path, INSTALL_PATH)
            .with_context(|| format!("install binary to {}", INSTALL_PATH))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(INSTALL_PATH, std::fs::Permissions::from_mode(0o755))?;
        }
        info!(path = INSTALL_PATH, "installed vigo binary");
    }

    // ADR-015: install the git-remote-gitback symlink so the agent's
    // multi-call dispatch is reachable from git's `git-remote-<scheme>`
    // PATH lookup. Best-effort — a failure here doesn't block agent
    // install; the user just won't be able to `git push gitback://...`
    // until the link is in place.
    if let Err(e) = super::ensure_git_remote_gitback_symlink(INSTALL_PATH) {
        tracing::warn!(
            error = %e,
            link = super::GIT_REMOTE_GITBACK_SYMLINK,
            target = INSTALL_PATH,
            "could not install git-remote-gitback symlink; gitback:// git URLs unavailable until fixed"
        );
    }

    // Write unit file.
    let unit = unit_file();
    std::fs::write(UNIT_PATH, &unit).context("write systemd unit file")?;

    // daemon-reload, enable, start.
    for args in [
        vec!["systemctl", "daemon-reload"],
        vec!["systemctl", "enable", SERVICE_NAME],
        vec!["systemctl", "start", SERVICE_NAME],
    ] {
        let status = tokio::process::Command::new(args[0])
            .args(&args[1..])
            .status()
            .await
            .with_context(|| format!("run {}", args.join(" ")))?;
        if !status.success() {
            bail!("{} exited with {}", args.join(" "), status);
        }
    }

    println!("Vigo agent service installed and started.");
    println!("  systemctl status {}    # check status", SERVICE_NAME);
    println!("  journalctl -u {} -f    # follow logs", SERVICE_NAME);

    Ok(grpc_addr)
}

/// Re-write the systemd unit file at `UNIT_PATH` if its contents
/// differ from the current template. Called from two paths:
///
/// 1. **`update::apply`** during an in-band binary upgrade — picks
///    up new unit fields (e.g. `LimitNOFILE=65536`, `Wants=
///    network-online.target`) without manual reinstall.
/// 2. **`run_daemon` startup** — every daemon launch self-heals the
///    unit so envoys whose binary was deployed by a path that
///    bypasses `update::apply` (manual `scp` + `systemctl restart`,
///    package install, image build) still end up with a current
///    unit file. The next service restart picks up the new limits.
///
/// Returns `Ok(true)` if the file changed and was rewritten,
/// `Ok(false)` if it already matched. Best-effort: any I/O error is
/// returned to the caller but does not abort the caller's flow.
pub async fn refresh_unit_file_if_changed() -> Result<bool> {
    refresh_unit_file_at(Path::new(UNIT_PATH), /* daemon_reload */ true).await
}

/// Path-parameterized + daemon-reload-gated core of
/// `refresh_unit_file_if_changed`. Exposed `pub(crate)` so the unit
/// tests can drive it against a tempdir with `daemon_reload=false`
/// — the actual `systemctl daemon-reload` call would fail in CI
/// where systemd may not be running.
pub(crate) async fn refresh_unit_file_at(
    path: &Path,
    daemon_reload: bool,
) -> Result<bool> {
    let target = unit_file();
    let current = std::fs::read_to_string(path).unwrap_or_default();
    if current == target {
        return Ok(false);
    }

    std::fs::write(path, &target)
        .with_context(|| format!("rewrite systemd unit at {}", path.display()))?;
    info!(path = %path.display(), "refreshed systemd unit file");

    if daemon_reload {
        // daemon-reload picks up the new contents. We do NOT restart
        // the service here — restart-vs-reload is the caller's
        // policy. `update::apply` exec()s into the new binary right
        // after, which re-reads the unit on the next start cycle.
        // `run_daemon`'s startup-time refresh leaves the running
        // process in place; the new limits apply at the next
        // restart (manual or systemd-driven).
        let status = tokio::process::Command::new("systemctl")
            .args(["daemon-reload"])
            .status()
            .await
            .context("run systemctl daemon-reload after unit refresh")?;
        if !status.success() {
            bail!("systemctl daemon-reload after unit refresh exited with {}", status);
        }
    }
    Ok(true)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_derive_grpc_addr() {
        assert_eq!(
            derive_grpc_addr("http://vigosrv.example.com:8443", "1530"),
            "vigosrv.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("https://vigosrv.example.com:8443", "1530"),
            "vigosrv.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("http://vigosrv.example.com", "1530"),
            "vigosrv.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("vigosrv.example.com:8443", "1530"),
            "vigosrv.example.com:1530"
        );
        assert_eq!(
            derive_grpc_addr("http://10.0.0.1:8443", "1530"),
            "10.0.0.1:1530"
        );
    }

    #[test]
    fn test_unit_file_content() {
        let unit = unit_file();
        assert!(unit.contains("ExecStart=/usr/local/sbin/vigo"));
        assert!(!unit.contains("--server"), "service should read config from file, not CLI flags");
        assert!(unit.contains("Restart=always"));
        assert!(unit.contains("WantedBy=multi-user.target"));
        // Safety knobs that 0.30.17 added — every refresh of an
        // older bare unit must inject these. Regression-guards the
        // EMFILE-loop fix from coming undone if someone edits the
        // template and forgets one.
        assert!(unit.contains("LimitNOFILE=65536"));
        assert!(unit.contains("Wants=network-online.target"));
        // Bounds the shutdown hook that records a clean stop. Without an
        // explicit value systemd waits its 90s default before SIGKILL, so a
        // wedged teardown would stall every reboot on every envoy.
        assert!(unit.contains("TimeoutStopSec=15"));
    }

    /// Bare unit that lacks every safety knob — exact shape that
    /// shipped on girlslaptop and triggered the 836G syslog flood.
    /// `refresh_unit_file_at` must rewrite it to the current
    /// template on first run, then no-op on subsequent runs.
    #[tokio::test]
    async fn refresh_rewrites_bare_unit() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("vigo-envoy.service");
        std::fs::write(
            &path,
            "[Unit]\nDescription=stale\n[Service]\nExecStart=/usr/local/sbin/vigo\n",
        )
        .unwrap();

        let changed = refresh_unit_file_at(&path, /* daemon_reload */ false)
            .await
            .unwrap();
        assert!(changed, "bare unit should be rewritten");

        let after = std::fs::read_to_string(&path).unwrap();
        assert!(after.contains("LimitNOFILE=65536"));
        assert!(after.contains("Wants=network-online.target"));
        assert!(after.contains("Restart=always"));
        assert!(after.contains("TimeoutStopSec=15"));
        assert_eq!(after, unit_file(), "rewritten content should equal template exactly");

        // Second call: contents now match → no rewrite, no error.
        let changed_again = refresh_unit_file_at(&path, false).await.unwrap();
        assert!(!changed_again, "second call should report no change");
    }

    /// If the unit file is missing (e.g., agent installed before
    /// any systemd unit was ever written, or the unit was manually
    /// deleted), refresh creates it.
    #[tokio::test]
    async fn refresh_creates_missing_unit() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("vigo-envoy.service");
        // Confirm pre-state: file does not exist.
        assert!(!path.exists());

        let changed = refresh_unit_file_at(&path, false).await.unwrap();
        assert!(changed, "missing unit should be treated as needing refresh");
        let after = std::fs::read_to_string(&path).unwrap();
        assert_eq!(after, unit_file());
    }

    /// If the unit already matches the template (post-refresh
    /// or freshly bootstrapped), refresh is a pure read+compare —
    /// no write, no daemon-reload attempt.
    #[tokio::test]
    async fn refresh_noop_on_matching_unit() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("vigo-envoy.service");
        std::fs::write(&path, unit_file()).unwrap();

        let mtime_before = std::fs::metadata(&path)
            .unwrap()
            .modified()
            .unwrap();

        // Sleep briefly so a write would produce a distinct mtime.
        std::thread::sleep(std::time::Duration::from_millis(10));

        let changed = refresh_unit_file_at(&path, false).await.unwrap();
        assert!(!changed, "matching unit should not be rewritten");

        let mtime_after = std::fs::metadata(&path)
            .unwrap()
            .modified()
            .unwrap();
        assert_eq!(
            mtime_before, mtime_after,
            "matching unit must not be rewritten (mtime preserved)"
        );
    }
}
