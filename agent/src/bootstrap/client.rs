// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! HTTP client for the bootstrap registration endpoint (POST /api/v1/bootstrap/register).

use anyhow::{bail, Context, Result};
use serde::{Deserialize, Serialize};

#[derive(Debug, Serialize)]
pub struct RegisterRequest {
    pub hostname: String,
    pub ip_address: String,
    pub pubkey_pem: String,
    pub csr_pem: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub token: Option<String>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub force: bool,
}

#[derive(Debug, Deserialize)]
pub struct RegisterResponse {
    pub envoy_id: String,
    pub signed_cert_pem: String,
    pub ca_cert_pem: String,
    // Spanner enrollment routing: when non-empty, the contacted bolt enrolled
    // this envoy on the bolt whose patterns own its hostname; this is that
    // bolt's gRPC address and the agent must check in there (load_bolt_addr
    // overrides the server addr at runtime). Empty ⇒ the contacted bolt owns it.
    #[serde(default)]
    pub bolt_addr: String,
    #[serde(default)]
    pub fallback_servers: Vec<String>,
}

/// POST the bootstrap registration request and return the signed certs.
///
/// `ca_pem` is the fleet CA in PEM form, when the operator supplied one via
/// `--ca-file` (the bootstrap script writes the server-embedded copy out for
/// exactly this). With it, enrollment verifies the server against that CA like
/// any other connection. Without it we fall back to an unverified handshake —
/// the historical behaviour, and trust-on-first-use: whoever answers gets to
/// hand us the CA we will trust from then on. That is tolerable on a trusted
/// LAN and is not tolerable across the public internet, so the caller warns.
pub async fn register(
    server: &str,
    req: &RegisterRequest,
    ca_pem: Option<&str>,
) -> Result<RegisterResponse> {
    let url = format!(
        "{}/api/v1/bootstrap/register",
        server.trim_end_matches('/')
    );

    let body = serde_json::to_vec(req).context("serialize register request")?;
    let url_clone = url.clone();

    // Parse the CA up front so a malformed --ca-file fails loudly here rather
    // than silently degrading to an unverified handshake inside the blocking
    // task. A CA that does not parse is an operator error, not a reason to
    // trust the network.
    let root_cert = match ca_pem {
        Some(pem) => Some(
            ureq::tls::Certificate::from_pem(pem.as_bytes())
                .map_err(|e| anyhow::anyhow!("parse CA certificate for bootstrap: {}", e))?,
        ),
        None => None,
    };

    let resp_bytes = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let tls_builder = ureq::tls::TlsConfig::builder();
        let tls = match root_cert {
            // Verify against the supplied fleet CA — normal hostname/SAN
            // checking applies, which is what we want: the server cert carries
            // the address the operator typed.
            Some(cert) => tls_builder
                .root_certs(ureq::tls::RootCerts::new_with_certs(&[cert]))
                .build(),
            // No CA available: bootstrap predates having any trust anchor.
            None => tls_builder.disable_verification(true).build(),
        };

        let agent = ureq::Agent::new_with_config(
            ureq::config::Config::builder()
                .tls_config(tls)
                .http_status_as_error(false)
                .build(),
        );

        let http_resp = agent
            .post(&url_clone)
            .header("Content-Type", "application/json")
            .send(&body[..])
            .map_err(|e| anyhow::anyhow!("bootstrap request to {}: {}", url_clone, e))?;

        let status = http_resp.status();
        let resp_body = http_resp
            .into_body()
            .read_to_vec()
            .context("read response body")?;

        if status != 200 {
            let text = String::from_utf8_lossy(&resp_body);
            bail!("bootstrap rejected (HTTP {}): {}", status, text.trim());
        }

        Ok(resp_body)
    })
    .await
    .context("spawn_blocking join")?
    .with_context(|| format!("bootstrap request to {}", url))?;

    let resp: RegisterResponse =
        serde_json::from_slice(&resp_bytes).context("parse bootstrap JSON response")?;

    if resp.envoy_id.is_empty() {
        bail!("server returned empty envoy_id");
    }

    Ok(resp)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_register_request_serialization() {
        let req = RegisterRequest {
            hostname: "web01".into(),
            ip_address: "10.0.0.1".into(),
            pubkey_pem: "-----BEGIN PUBLIC KEY-----\nfoo\n-----END PUBLIC KEY-----\n".into(),
            csr_pem: "-----BEGIN CERTIFICATE REQUEST-----\nbar\n-----END CERTIFICATE REQUEST-----\n".into(),
            token: Some("tok123".into()),
            force: false,
        };
        let json = serde_json::to_value(&req).unwrap();
        assert_eq!(json["hostname"], "web01");
        assert_eq!(json["token"], "tok123");
        assert!(json.get("force").is_none(), "force=false should be omitted");
    }

    #[test]
    fn test_register_request_no_token() {
        let req = RegisterRequest {
            hostname: "web01".into(),
            ip_address: "10.0.0.1".into(),
            pubkey_pem: "pub".into(),
            csr_pem: "csr".into(),
            token: None,
            force: false,
        };
        let json = serde_json::to_value(&req).unwrap();
        assert!(json.get("token").is_none());
        assert!(json.get("force").is_none());
    }

    #[test]
    fn test_register_request_force() {
        let req = RegisterRequest {
            hostname: "web01".into(),
            ip_address: "10.0.0.1".into(),
            pubkey_pem: "pub".into(),
            csr_pem: "csr".into(),
            token: Some("tok".into()),
            force: true,
        };
        let json = serde_json::to_value(&req).unwrap();
        assert_eq!(json["force"], true);
    }

    #[test]
    fn test_register_response_deserialization() {
        let json = r#"{
            "envoy_id": "550e8400-e29b-41d4-a716-446655440000",
            "signed_cert_pem": "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----\n",
            "ca_cert_pem": "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n"
        }"#;
        let resp: RegisterResponse = serde_json::from_str(json).unwrap();
        assert_eq!(resp.envoy_id, "550e8400-e29b-41d4-a716-446655440000");
        assert!(resp.signed_cert_pem.contains("BEGIN CERTIFICATE"));
        assert!(resp.ca_cert_pem.contains("BEGIN CERTIFICATE"));
    }
}
