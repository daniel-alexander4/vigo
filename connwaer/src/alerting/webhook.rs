use anyhow::{Context, Result};
use std::time::Duration;

use crate::capability::CapabilityResult;
use crate::config::WebhookTarget;

/// Send a webhook alert with retry (3 attempts, exponential backoff).
pub fn send(target: &WebhookTarget, result: &CapabilityResult, severity: &str) -> Result<()> {
    let payload = serde_json::json!({
        "severity": severity,
        "capability": result.capability,
        "status": result.status,
        "summary": result.summary,
        "hostname": result.hostname,
        "platform": result.platform,
        "completed_at": result.completed_at,
        "controls": result.controls,
    });

    let body = serde_json::to_string(&payload)?;

    for attempt in 0..3u32 {
        if attempt > 0 {
            let delay = Duration::from_millis(500 * 2u64.pow(attempt - 1));
            std::thread::sleep(delay);
        }

        let mut req = ureq::post(&target.url)
            .header("Content-Type", "application/json")
            .header("User-Agent", "connwaer");

        for (key, value) in &target.headers {
            req = req.header(key, value);
        }

        match req.send(&body) {
            Ok(resp) => {
                let status = resp.status().as_u16();
                if (200..300).contains(&status) {
                    tracing::debug!("webhook sent to {} ({})", target.url, status);
                    return Ok(());
                }
                tracing::warn!("webhook {} returned status {}, attempt {}", target.url, status, attempt + 1);
            }
            Err(e) => {
                tracing::warn!("webhook {} failed: {e}, attempt {}", target.url, attempt + 1);
            }
        }
    }

    anyhow::bail!("webhook {} failed after 3 attempts", target.url)
}
