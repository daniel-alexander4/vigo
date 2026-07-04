use anyhow::Result;
use std::os::unix::net::UnixDatagram;

use crate::capability::CapabilityResult;
use crate::config::SyslogTarget;

/// Send an alert to the local syslog socket.
pub fn send(target: &SyslogTarget, result: &CapabilityResult, severity: &str) -> Result<()> {
    let priority = match (target.facility.as_str(), severity) {
        (_, "fail" | "error") => 11,  // daemon.err
        (_, "warn") => 12,            // daemon.warning
        _ => 14,                       // daemon.info
    };

    let msg = format!(
        "<{}>connwaer: {} {} — {}",
        priority,
        result.capability,
        severity,
        result.summary,
    );

    let sock = UnixDatagram::unbound()?;
    sock.send_to(msg.as_bytes(), "/dev/log")?;

    tracing::debug!("syslog alert sent: {}", result.capability);
    Ok(())
}
