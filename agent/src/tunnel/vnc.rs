// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! VNC shadow session support — find the console X display, prompt for consent,
//! spawn x11vnc on a dynamic port, and return the port for the tunnel relay.

use std::process::Stdio;
use tokio::process::Command;
use tracing::{info, warn};

/// Result of setting up a VNC shadow session.
pub struct VncSession {
    pub port: u32,
    pub child: tokio::process::Child, // x11vnc process — killed on drop
}

impl Drop for VncSession {
    fn drop(&mut self) {
        // Best-effort kill — x11vnc with -once should exit on disconnect,
        // but kill it if it's still running.
        let _ = self.child.start_kill();
    }
}

/// Ceiling for the short lookups (`who`, `id`) and the notify-send.
const VNC_CMD_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(5);

/// Ceiling for the consent dialog — zenity's own `--timeout=30` plus slack
/// for `su` and the X server, so a wedged session can't hold the tunnel open.
const CONSENT_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(45);

/// run_lookup runs a short console lookup through the bounded runner
/// (own process group, killed at [`VNC_CMD_TIMEOUT`]); a `who` or `id`
/// blocked on NSS/LDAP no longer hangs tunnel setup.
fn run_lookup(name: &str, args: &[&str]) -> Option<std::process::Output> {
    crate::executors::run_command_niced_timeout(name, args, 0, VNC_CMD_TIMEOUT).ok()
}

/// status_bounded spawns `cmd` in its own process group and waits at most
/// `timeout` for it; past that the whole group (su, the shell, zenity) is
/// killed and a TimedOut error returned.
async fn status_bounded(
    mut cmd: Command,
    timeout: std::time::Duration,
) -> std::io::Result<std::process::ExitStatus> {
    crate::platform::process::configure_process_group(&mut cmd);
    cmd.kill_on_drop(true);
    let mut child = cmd.spawn()?;
    match tokio::time::timeout(timeout, child.wait()).await {
        Ok(r) => r,
        Err(_) => {
            if let Some(pid) = child.id() {
                let _ = tokio::task::spawn_blocking(move || {
                    crate::platform::process::kill_process_tree(pid, std::time::Duration::from_millis(500))
                })
                .await;
            }
            let _ = child.wait().await;
            Err(std::io::Error::new(std::io::ErrorKind::TimedOut, "timed out"))
        }
    }
}

/// Information about the active X11 console session.
struct ConsoleInfo {
    display: String,     // e.g., ":0"
    user: String,        // e.g., "dan"
    xauthority: String,  // e.g., "/home/dan/.Xauthority"
}

/// Find the console X11 display, user, and Xauthority path.
fn find_console() -> Option<ConsoleInfo> {
    // Check common X11 display paths.
    let display = if std::path::Path::new("/tmp/.X11-unix/X0").exists() {
        ":0".to_string()
    } else if std::path::Path::new("/tmp/.X11-unix/X1").exists() {
        ":1".to_string()
    } else {
        return None;
    };

    // Find who owns the display by checking Xorg process.
    let output = run_lookup("who", &[])?;
    let who_output = String::from_utf8_lossy(&output.stdout);

    // Parse `who` output: look for a line with the display (e.g., "dan  :0")
    let display_num = display.trim_start_matches(':');
    let mut user = String::new();
    for line in who_output.lines() {
        if line.contains(&format!(":{}", display_num))
            || line.contains(&format!("(:{}", display_num))
        {
            if let Some(u) = line.split_whitespace().next() {
                user = u.to_string();
                break;
            }
        }
    }

    if user.is_empty() {
        // Fallback: check who owns the X socket.
        if let Ok(meta) = std::fs::metadata(format!("/tmp/.X11-unix/X{}", display_num)) {
            use std::os::unix::fs::MetadataExt;
            if let Some(output) = run_lookup("id", &["-nu", &meta.uid().to_string()]) {
                user = String::from_utf8_lossy(&output.stdout).trim().to_string();
            }
        }
    }

    if user.is_empty() {
        return None;
    }

    // Find Xauthority — check common locations.
    let home = if user == "root" {
        "/root".to_string()
    } else {
        format!("/home/{}", user)
    };

    let xauthority = if std::path::Path::new(&format!("{}/.Xauthority", home)).exists() {
        format!("{}/.Xauthority", home)
    } else {
        // Try run directory (GDM, SDDM).
        let uid_output = run_lookup("id", &["-u", &user])?;
        let uid = String::from_utf8_lossy(&uid_output.stdout).trim().to_string();
        let run_path = format!("/run/user/{}/gdm/Xauthority", uid);
        if std::path::Path::new(&run_path).exists() {
            run_path
        } else {
            // Last resort: tell x11vnc to guess.
            return Some(ConsoleInfo {
                display,
                user,
                xauthority: String::new(), // empty = use -auth guess
            });
        }
    };

    Some(ConsoleInfo {
        display,
        user,
        xauthority,
    })
}

/// Prompt the console user for consent via zenity.
/// Returns true if allowed, false if denied or timed out.
async fn prompt_consent(console: &ConsoleInfo) -> bool {
    // Send a desktop notification first to get the user's attention,
    // then show the modal question dialog on top of all windows.
    // The notification ensures the user notices even if the dialog
    // opens behind their current window.
    let env_prefix = format!(
        "DISPLAY={} XAUTHORITY={}",
        console.display, console.xauthority
    );

    // Notification first; bounded so a wedged su/notify-send can't stall.
    let mut notify = Command::new("su");
    notify
        .args([
            "-", &console.user, "-c",
            &format!(
                "{} notify-send -u critical -t 30000 'Vigo Remote Access' 'An administrator is requesting to view your screen.' 2>/dev/null",
                env_prefix
            ),
        ])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    let _ = status_bounded(notify, VNC_CMD_TIMEOUT).await;

    // Small delay so the notification appears before the dialog.
    tokio::time::sleep(std::time::Duration::from_millis(300)).await;

    // Show the consent dialog. Use --class to set WM_CLASS so window
    // rules can target it if needed. xdotool search --sync blocks until
    // the zenity window exists (up to 5s via timeout), then raises it
    // above all other windows so the user sees it immediately.
    let mut dialog = Command::new("su");
    dialog
        .args([
            "-", &console.user, "-c",
            &format!(
                concat!(
                    "{} bash -c '",
                    "zenity --question ",
                    "--title=\"Vigo Remote Access\" ",
                    "--text=\"An administrator is requesting to view your screen.\\n\\nAllow remote access?\" ",
                    "--timeout=30 ",
                    "--width=350 ",
                    "--class=vigo-consent ",
                    "& ZPID=$!; ",
                    "if command -v xdotool >/dev/null 2>&1; then ",
                    "  WID=$(timeout 5 xdotool search --sync --pid $ZPID 2>/dev/null | head -1); ",
                    "  [ -n \"$WID\" ] && xdotool windowactivate --sync $WID windowfocus --sync $WID 2>/dev/null; ",
                    "fi; ",
                    "wait $ZPID",
                    "'",
                ),
                env_prefix
            ),
        ])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    let result = status_bounded(dialog, CONSENT_TIMEOUT).await;

    match result {
        Ok(status) => status.success(), // exit 0 = Allow, non-zero = Deny/timeout
        Err(e) => {
            warn!(error = %e, "consent prompt failed");
            false
        }
    }
}

/// Start a VNC shadow session: find display, optionally prompt consent,
/// spawn x11vnc on a dynamic port, wait for it to be ready.
pub async fn start_session(
    tunnel_id: &str,
    consent_mode: &str,
) -> Result<VncSession, String> {
    // Find the console X11 display.
    // find_console runs blocking lookups (`who`, `id`): off the async workers.
    let console = tokio::task::spawn_blocking(find_console)
        .await
        .ok()
        .flatten()
        .ok_or_else(|| "no X11 display found (is a graphical session running?)".to_string())?;

    info!(
        tunnel_id = %tunnel_id,
        display = %console.display,
        user = %console.user,
        "found console X11 session"
    );

    // Consent prompt (unless mode is "never").
    if consent_mode != "never" {
        info!(tunnel_id = %tunnel_id, "prompting console user for consent");
        if !prompt_consent(&console).await {
            return Err("user denied remote access".to_string());
        }
        info!(tunnel_id = %tunnel_id, "consent granted");
    }

    // Pick a random high port and run x11vnc in the foreground.
    // Older x11vnc versions (0.9.16) don't reliably print PORT= with -bg,
    // so we assign the port ourselves and wait for "screen setup finished"
    // on stderr to know it's ready.
    let port = pick_ephemeral_port()
        .ok_or_else(|| "no available ephemeral port for x11vnc".to_string())?;

    let mut cmd = Command::new("x11vnc");
    cmd.args([
        "-display", &console.display,
        "-localhost",
        "-nopw",
        "-once",
        "-rfbport", &port.to_string(),
    ]);

    if !console.xauthority.is_empty() {
        cmd.args(["-auth", &console.xauthority]);
    } else {
        cmd.arg("-auth");
        cmd.arg("guess");
    }

    cmd.stdout(Stdio::null());
    cmd.stderr(Stdio::piped());

    let mut child = cmd.spawn()
        .map_err(|e| format!("spawn x11vnc: {} (is x11vnc installed?)", e))?;

    // Wait for x11vnc to finish setup by watching stderr for
    // "screen setup finished." which it prints after binding the port.
    let stderr = child.stderr.take()
        .ok_or_else(|| "failed to capture x11vnc stderr".to_string())?;

    tokio::time::timeout(
        std::time::Duration::from_secs(10),
        wait_for_ready(stderr),
    )
    .await
    .map_err(|_| "x11vnc did not become ready within 10s".to_string())?
    .map_err(|e| format!("x11vnc startup: {}", e))?;

    info!(tunnel_id = %tunnel_id, port = port, "x11vnc started");

    Ok(VncSession {
        port,
        child,
    })
}

/// Wait for x11vnc to become ready by scanning stderr for "screen setup finished".
async fn wait_for_ready(
    stderr: tokio::process::ChildStderr,
) -> Result<(), String> {
    use tokio::io::{AsyncBufReadExt, BufReader};
    let mut reader = BufReader::new(stderr);
    let mut line = String::new();
    loop {
        line.clear();
        let n = reader.read_line(&mut line).await
            .map_err(|e| format!("read x11vnc stderr: {}", e))?;
        if n == 0 {
            return Err("x11vnc exited before becoming ready".to_string());
        }
        if line.contains("screen setup finished") {
            return Ok(());
        }
    }
}

/// Bind a TCP listener on port 0 to get an OS-assigned ephemeral port,
/// then immediately close it. There's a small TOCTOU window, but x11vnc
/// binds immediately after we return.
fn pick_ephemeral_port() -> Option<u32> {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").ok()?;
    let port = listener.local_addr().ok()?.port();
    drop(listener);
    Some(port as u32)
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;

    /// A console command that never returns (a wedged `su`, a dialog on a
    /// hung X server) is killed at its ceiling; the old `.status().await`
    /// waited forever.
    #[tokio::test]
    async fn status_bounded_kills_a_hung_command() {
        let mut cmd = Command::new("sh");
        cmd.args(["-c", "sleep 30"]);
        let started = std::time::Instant::now();
        let res = status_bounded(cmd, std::time::Duration::from_millis(300)).await;
        assert_eq!(res.unwrap_err().kind(), std::io::ErrorKind::TimedOut);
        assert!(started.elapsed() < std::time::Duration::from_secs(5));
    }

    #[tokio::test]
    async fn status_bounded_passes_exit_status_through() {
        let mut cmd = Command::new("sh");
        cmd.args(["-c", "exit 3"]);
        let st = status_bounded(cmd, std::time::Duration::from_secs(5)).await.unwrap();
        assert_eq!(st.code(), Some(3));
    }
}
