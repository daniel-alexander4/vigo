// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

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
    pub thrall_id: String,
    pub signed_cert_pem: String,
    pub ca_cert_pem: String,
}

/// POST the bootstrap registration request and return the signed certs.
pub async fn register(server: &str, req: &RegisterRequest) -> Result<RegisterResponse> {
    let url = format!(
        "{}/api/v1/bootstrap/register",
        server.trim_end_matches('/')
    );

    let body = serde_json::to_vec(req).context("serialize register request")?;
    let url_clone = url.clone();

    let resp_bytes = tokio::task::spawn_blocking(move || -> Result<Vec<u8>> {
        let tls = ureq::tls::TlsConfig::builder()
            .disable_verification(true) // Bootstrap happens before we have TLS certs
            .build();

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

    if resp.thrall_id.is_empty() {
        bail!("server returned empty thrall_id");
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
            "thrall_id": "550e8400-e29b-41d4-a716-446655440000",
            "signed_cert_pem": "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----\n",
            "ca_cert_pem": "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n"
        }"#;
        let resp: RegisterResponse = serde_json::from_str(json).unwrap();
        assert_eq!(resp.thrall_id, "550e8400-e29b-41d4-a716-446655440000");
        assert!(resp.signed_cert_pem.contains("BEGIN CERTIFICATE"));
        assert!(resp.ca_cert_pem.contains("BEGIN CERTIFICATE"));
    }
}
