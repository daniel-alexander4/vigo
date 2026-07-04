use anyhow::Result;
use std::time::Duration;

use crate::capability::CapabilityResult;
use crate::config::WebhookTarget;

/// Overall per-request budget. A hung endpoint must not stall the dispatch
/// path (which runs the retry loop inline).
const REQUEST_TIMEOUT: Duration = Duration::from_secs(10);

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

        let agent: ureq::Agent = ureq::Agent::config_builder()
            .timeout_global(Some(REQUEST_TIMEOUT))
            .build()
            .into();
        let mut req = agent
            .post(&target.url)
            .header("Content-Type", "application/json")
            .header("User-Agent", "connwaer");

        for (key, value) in &target.headers {
            req = req.header(key, value);
        }

        match req.send(&body) {
            Ok(resp) => {
                let status = resp.status().as_u16();
                if (200..300).contains(&status) {
                    tracing::debug!("webhook sent to {} ({})", redacted(&target.url), status);
                    return Ok(());
                }
                tracing::warn!("webhook {} returned status {}, attempt {}", redacted(&target.url), status, attempt + 1);
            }
            Err(e) => {
                tracing::warn!("webhook {} failed: {e}, attempt {}", redacted(&target.url), attempt + 1);
            }
        }
    }

    anyhow::bail!("webhook {} failed after 3 attempts", redacted(&target.url))
}

/// Webhook URLs routinely embed secrets in the path or query (Slack/Teams
/// tokens). Log only scheme and host.
fn redacted(url: &str) -> String {
    let Some((scheme, rest)) = url.split_once("://") else {
        return "<webhook>".to_string();
    };
    let host = rest.split(['/', '?']).next().unwrap_or("");
    format!("{scheme}://{host}/…")
}

#[cfg(test)]
mod tests {
    use super::redacted;

    #[test]
    fn redaction_drops_path_and_query() {
        assert_eq!(
            redacted("https://hooks.slack.com/services/T000/B000/secrettoken"),
            "https://hooks.slack.com/…"
        );
        assert_eq!(redacted("https://h.example.com?key=s3cret"), "https://h.example.com/…");
        assert_eq!(redacted("not a url"), "<webhook>");
    }
}
