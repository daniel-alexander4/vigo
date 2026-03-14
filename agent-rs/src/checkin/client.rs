// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! gRPC client wrapper with mTLS and ED25519 signing.
//!
//! Mirrors the Go `checkin.Client` — wraps `HobnobAgentClient` with automatic
//! request signing and TLS configuration.

use crate::auth::signer;
use crate::proto::{
    hobnob_agent_client::HobnobAgentClient, CheckInRequest, CheckInResponse,
    DownloadCustomTraitRequest, ModuleResult, RegisterRequest, RegisterResponse,
    ReportResultRequest, ReportResultResponse, ReportTraitsRequest,
};
use anyhow::{Context, Result};
use ed25519_dalek::SigningKey;
use std::collections::HashMap;
use tonic::transport::{Certificate, Channel, ClientTlsConfig, Identity};

pub struct Client {
    agent: HobnobAgentClient<Channel>,
    thrall_id: String,
    signing_key: SigningKey,
}

impl Client {
    /// Connect to the Hobnob server with mTLS.
    /// If `client_cert` and `client_key` are None, connects with CA-only verification.
    pub async fn connect(
        server_addr: &str,
        ca_file: Option<&str>,
        client_cert_file: Option<&str>,
        client_key_file: Option<&str>,
        thrall_id: &str,
        signing_key: SigningKey,
    ) -> Result<Self> {
        let mut tls_config = ClientTlsConfig::new();

        if let Some(ca_path) = ca_file {
            let ca_pem = tokio::fs::read(ca_path)
                .await
                .with_context(|| format!("read CA cert: {}", ca_path))?;
            tls_config = tls_config.ca_certificate(Certificate::from_pem(ca_pem));
        }

        if let (Some(cert_path), Some(key_path)) = (client_cert_file, client_key_file) {
            let cert_pem = tokio::fs::read(cert_path)
                .await
                .with_context(|| format!("read client cert: {}", cert_path))?;
            let key_pem = tokio::fs::read(key_path)
                .await
                .with_context(|| format!("read client key: {}", key_path))?;
            tls_config = tls_config.identity(Identity::from_pem(cert_pem, key_pem));
        }

        // tonic requires https:// scheme for TLS connections
        let endpoint_uri = if server_addr.starts_with("https://") {
            server_addr.to_string()
        } else {
            format!("https://{}", server_addr)
        };

        // Extract the hostname for TLS domain name verification
        let domain = server_addr
            .trim_start_matches("https://")
            .split(':')
            .next()
            .unwrap_or("localhost");
        tls_config = tls_config.domain_name(domain);

        let channel = Channel::from_shared(endpoint_uri)
            .context("parse server address")?
            .tls_config(tls_config)
            .context("configure TLS")?
            .connect()
            .await
            .context("connect to gRPC server")?;

        Ok(Self {
            agent: HobnobAgentClient::new(channel),
            thrall_id: thrall_id.to_string(),
            signing_key,
        })
    }

    /// Get the thrall ID this client authenticates as.
    pub fn thrall_id(&self) -> &str {
        &self.thrall_id
    }

    /// Get current Unix timestamp as decimal string (matches Go's strconv.FormatInt).
    fn timestamp_str() -> String {
        use std::time::{SystemTime, UNIX_EPOCH};
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs()
            .to_string()
    }

    /// Build a prost_types::Timestamp for the current time.
    fn now_timestamp() -> prost_types::Timestamp {
        use std::time::{SystemTime, UNIX_EPOCH};
        let d = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
        prost_types::Timestamp {
            seconds: d.as_secs() as i64,
            nanos: d.subsec_nanos() as i32,
        }
    }

    /// CheckIn calls the server's CheckIn RPC with ED25519-signed request.
    pub async fn check_in(
        &mut self,
        state_fingerprint: &str,
        traits_json: Option<Vec<u8>>,
        policy_version: u64,
        cached_module_hashes: Option<HashMap<String, String>>,
    ) -> Result<CheckInResponse> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(state_fingerprint.as_bytes(), &ts, &self.signing_key);

        let req = CheckInRequest {
            thrall_id: self.thrall_id.clone(),
            state_fingerprint: state_fingerprint.to_string(),
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
            traits_json: traits_json.unwrap_or_default(),
            policy_version,
            policy_mode: true,
            cached_module_hashes: cached_module_hashes.unwrap_or_default(),
            capabilities: None,
        };

        let resp = self
            .agent
            .check_in(tonic::Request::new(req))
            .await
            .context("check-in RPC")?;

        Ok(resp.into_inner())
    }

    /// Register calls the server's Register RPC.
    pub async fn register(
        &mut self,
        hostname: &str,
        ip_addr: &str,
        pubkey_pem: &str,
        token: &str,
    ) -> Result<RegisterResponse> {
        let req = RegisterRequest {
            hostname: hostname.to_string(),
            ip_address: ip_addr.to_string(),
            pubkey_pem: pubkey_pem.to_string(),
            token: token.to_string(),
        };

        let resp = self
            .agent
            .register(tonic::Request::new(req))
            .await
            .context("register RPC")?;

        Ok(resp.into_inner())
    }

    /// ReportResult calls the server's ReportResult RPC with signed results.
    pub async fn report_result(
        &mut self,
        run_id: &str,
        results: Vec<ModuleResult>,
    ) -> Result<ReportResultResponse> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(run_id.as_bytes(), &ts, &self.signing_key);

        let req = ReportResultRequest {
            run_id: run_id.to_string(),
            thrall_id: self.thrall_id.clone(),
            results,
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
        };

        let resp = self
            .agent
            .report_result(tonic::Request::new(req))
            .await
            .context("report-result RPC")?;

        Ok(resp.into_inner())
    }

    /// ReportResult for a different thrall (used by gateway proxy for device results).
    pub async fn report_result_for(
        &mut self,
        run_id: &str,
        thrall_id: &str,
        results: Vec<ModuleResult>,
    ) -> Result<ReportResultResponse> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(run_id.as_bytes(), &ts, &self.signing_key);

        let req = ReportResultRequest {
            run_id: run_id.to_string(),
            thrall_id: thrall_id.to_string(),
            results,
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
        };

        let resp = self
            .agent
            .report_result(tonic::Request::new(req))
            .await
            .context("report-result-for RPC")?;

        Ok(resp.into_inner())
    }

    /// ReportTraits calls the server's ReportTraits RPC with signed traits JSON.
    pub async fn report_traits(&mut self, traits_json: &[u8]) -> Result<()> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(traits_json, &ts, &self.signing_key);

        let req = ReportTraitsRequest {
            thrall_id: self.thrall_id.clone(),
            traits_json: traits_json.to_vec(),
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
        };

        self.agent
            .report_traits(tonic::Request::new(req))
            .await
            .context("report-traits RPC")?;

        Ok(())
    }

    /// ReportTraits for a different thrall (used by gateway proxy for device traits).
    pub async fn report_traits_for(&mut self, thrall_id: &str, traits_json: &[u8]) -> Result<()> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(traits_json, &ts, &self.signing_key);

        let req = ReportTraitsRequest {
            thrall_id: thrall_id.to_string(),
            traits_json: traits_json.to_vec(),
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
        };

        self.agent
            .report_traits(tonic::Request::new(req))
            .await
            .context("report-traits-for RPC")?;

        Ok(())
    }

    /// DownloadCustomTrait retrieves a custom trait executable by name.
    pub async fn download_custom_trait(&mut self, name: &str) -> Result<Vec<u8>> {
        let req = DownloadCustomTraitRequest {
            name: name.to_string(),
        };

        let resp = self
            .agent
            .download_custom_trait(tonic::Request::new(req))
            .await
            .with_context(|| format!("download custom trait {:?}", name))?;

        Ok(resp.into_inner().data)
    }

    /// Build a signed CheckInRequest without sending it (for stream mode).
    pub fn build_check_in_request(
        &self,
        state_fingerprint: &str,
        traits_json: Option<Vec<u8>>,
        policy_version: u64,
        cached_module_hashes: Option<HashMap<String, String>>,
    ) -> CheckInRequest {
        self.build_check_in_request_with_caps(
            state_fingerprint,
            traits_json,
            policy_version,
            cached_module_hashes,
            None,
        )
    }

    /// Build a signed CheckInRequest with optional StreamCapabilities.
    pub fn build_check_in_request_with_caps(
        &self,
        state_fingerprint: &str,
        traits_json: Option<Vec<u8>>,
        policy_version: u64,
        cached_module_hashes: Option<HashMap<String, String>>,
        capabilities: Option<crate::proto::StreamCapabilities>,
    ) -> CheckInRequest {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(state_fingerprint.as_bytes(), &ts, &self.signing_key);

        CheckInRequest {
            thrall_id: self.thrall_id.clone(),
            state_fingerprint: state_fingerprint.to_string(),
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
            traits_json: traits_json.unwrap_or_default(),
            policy_version,
            policy_mode: true,
            cached_module_hashes: cached_module_hashes.unwrap_or_default(),
            capabilities,
        }
    }

    /// Build a signed ReportResultRequest without sending it (for stream mode).
    pub fn build_report_result_request(
        &self,
        run_id: &str,
        results: Vec<ModuleResult>,
    ) -> ReportResultRequest {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(run_id.as_bytes(), &ts, &self.signing_key);

        ReportResultRequest {
            run_id: run_id.to_string(),
            thrall_id: self.thrall_id.clone(),
            results,
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
        }
    }

    /// Get a clone of the underlying gRPC agent client (for stream mode).
    pub fn agent_client(&self) -> HobnobAgentClient<Channel> {
        self.agent.clone()
    }

}
