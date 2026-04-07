// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! gRPC client wrapper with mTLS and ED25519 signing.
//!
//! Mirrors the Go `checkin.Client` — wraps `VigoAgentClient` with automatic
//! request signing and TLS configuration.

use crate::auth::signer;
use crate::proto::{
    vigo_agent_client::VigoAgentClient, CheckInRequest, CheckInResponse,
    DownloadCustomTraitRequest, ModuleResult, RegisterRequest, RegisterResponse,
    ReportResultRequest, ReportResultResponse, ReportTraitsRequest, WakeRequest,
    WakeResponse,
};
use anyhow::{Context, Result};
use ed25519_dalek::SigningKey;
use std::collections::HashMap;
use std::time::Duration;
use tonic::transport::{Certificate, Channel, ClientTlsConfig, Identity};

/// Number of consecutive failures before switching to the next server.
const FAILOVER_THRESHOLD: u32 = 3;

/// Number of check-in cycles between primary probe attempts when failed over.
/// At 15s intervals, 20 cycles = ~5 minutes.
const PRIMARY_PROBE_CYCLES: u32 = 20;

pub struct Client {
    agent: VigoAgentClient<Channel>,
    envoy_id: String,
    signing_key: SigningKey,

    // Failover state.
    /// All server addresses: index 0 is the primary, rest are fallbacks.
    servers: Vec<String>,
    /// Index of the currently active server in `servers`.
    active_server_idx: usize,
    /// Consecutive failures on the currently active server.
    consecutive_failures: u32,
    /// Cycles since the last primary probe (only tracked when failed over).
    cycles_since_probe: u32,

    // TLS config state needed for reconnection.
    tls_ca_pem: Option<Vec<u8>>,
    tls_identity: Option<(Vec<u8>, Vec<u8>)>,

    /// Agent binary version string (sent on every check-in).
    agent_version: String,
}

impl Client {
    /// Connect to the Vigo server with mTLS.
    /// If `client_cert` and `client_key` are None, connects with CA-only verification.
    pub async fn connect(
        server_addr: &str,
        ca_file: Option<&str>,
        client_cert_file: Option<&str>,
        client_key_file: Option<&str>,
        envoy_id: &str,
        signing_key: SigningKey,
    ) -> Result<Self> {
        // Read TLS material once, reuse for reconnections.
        let ca_pem = if let Some(ca_path) = ca_file {
            Some(
                tokio::fs::read(ca_path)
                    .await
                    .with_context(|| format!("read CA cert: {}", ca_path))?,
            )
        } else {
            None
        };

        let identity_pem = if let (Some(cert_path), Some(key_path)) = (client_cert_file, client_key_file) {
            let cert_pem = tokio::fs::read(cert_path)
                .await
                .with_context(|| format!("read client cert: {}", cert_path))?;
            let key_pem = tokio::fs::read(key_path)
                .await
                .with_context(|| format!("read client key: {}", key_path))?;
            Some((cert_pem, key_pem))
        } else {
            None
        };

        let channel = Self::build_channel(server_addr, ca_pem.as_deref(), identity_pem.as_ref()).await?;

        Ok(Self {
            agent: VigoAgentClient::new(channel),
            envoy_id: envoy_id.to_string(),
            signing_key,
            servers: vec![server_addr.to_string()],
            active_server_idx: 0,
            consecutive_failures: 0,
            cycles_since_probe: 0,
            tls_ca_pem: ca_pem,
            tls_identity: identity_pem,
            agent_version: String::new(),
        })
    }

    /// Set the agent version string reported on every check-in.
    pub fn set_agent_version(&mut self, version: &str) {
        self.agent_version = version.to_string();
    }

    /// Build a gRPC channel to the given server address.
    async fn build_channel(
        server_addr: &str,
        ca_pem: Option<&[u8]>,
        identity_pem: Option<&(Vec<u8>, Vec<u8>)>,
    ) -> Result<Channel> {
        let mut tls_config = ClientTlsConfig::new();

        if let Some(ca) = ca_pem {
            tls_config = tls_config.ca_certificate(Certificate::from_pem(ca));
        }

        if let Some((cert, key)) = identity_pem {
            tls_config = tls_config.identity(Identity::from_pem(cert.clone(), key.clone()));
        }

        let endpoint_uri = if server_addr.starts_with("https://") {
            server_addr.to_string()
        } else {
            format!("https://{}", server_addr)
        };

        let domain = server_addr
            .trim_start_matches("https://")
            .split(':')
            .next()
            .unwrap_or("localhost");
        tls_config = tls_config.domain_name(domain);

        Channel::from_shared(endpoint_uri)
            .context("parse server address")?
            .tls_config(tls_config)
            .context("configure TLS")?
            .connect_timeout(Duration::from_secs(10))
            .timeout(Duration::from_secs(30))
            .tcp_keepalive(Some(Duration::from_secs(30)))
            .http2_keep_alive_interval(Duration::from_secs(30))
            .keep_alive_timeout(Duration::from_secs(10))
            .connect()
            .await
            .context("connect to gRPC server")
    }

    /// Sign arbitrary bytes with the agent's signing key. Used for tunnel handshake.
    /// Returns (signature, timestamp) so the caller can include the timestamp in the message.
    pub fn sign(&self, payload: &[u8]) -> (Vec<u8>, String) {
        use crate::auth::signer;
        use std::time::{SystemTime, UNIX_EPOCH};
        let ts = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs()
            .to_string();
        let sig = signer::sign_payload(payload, &ts, &self.signing_key);
        (sig, ts)
    }

    /// Build a new VigoAgentClient on a fresh gRPC channel using the same mTLS
    /// config. Used by Scrier tunnel relay to open a separate connection.
    pub async fn new_tunnel_client(&self) -> Result<VigoAgentClient<Channel>> {
        let server_addr = &self.servers[self.active_server_idx];
        let channel = Self::build_channel(
            server_addr,
            self.tls_ca_pem.as_deref(),
            self.tls_identity.as_ref(),
        )
        .await?;
        Ok(VigoAgentClient::new(channel))
    }

    /// Set fallback server addresses. The primary is already at index 0.
    pub fn set_fallback_servers(&mut self, fallbacks: Vec<String>) {
        // Keep the primary (index 0), replace everything after it.
        self.servers.truncate(1);
        for addr in fallbacks {
            if !addr.is_empty() && !self.servers.contains(&addr) {
                self.servers.push(addr);
            }
        }
    }

    /// Get the address of the currently active server.
    pub fn active_server(&self) -> &str {
        &self.servers[self.active_server_idx]
    }

    /// Whether the client is currently connected to a fallback (not the primary).
    pub fn is_failed_over(&self) -> bool {
        self.active_server_idx != 0
    }

    /// Record a successful RPC. Resets consecutive failure counter.
    pub fn record_success(&mut self) {
        self.consecutive_failures = 0;
    }

    /// Record a failed RPC and attempt failover if the threshold is exceeded.
    /// Returns true if a failover occurred (caller should retry).
    pub async fn record_failure(&mut self) -> bool {
        self.consecutive_failures += 1;

        if self.servers.len() <= 1 {
            return false; // No fallbacks configured.
        }

        if self.consecutive_failures < FAILOVER_THRESHOLD {
            return false; // Haven't hit the threshold yet.
        }

        // Switch to the next server in the list.
        let old_idx = self.active_server_idx;
        self.active_server_idx = (self.active_server_idx + 1) % self.servers.len();
        self.consecutive_failures = 0;
        self.cycles_since_probe = 0;

        let old_addr = &self.servers[old_idx];
        let new_addr = &self.servers[self.active_server_idx];
        tracing::warn!(
            from = %old_addr,
            to = %new_addr,
            "primary unreachable after {} attempts, failing over to {}",
            FAILOVER_THRESHOLD,
            new_addr,
        );

        // Reconnect to the new server.
        match Self::build_channel(
            new_addr,
            self.tls_ca_pem.as_deref(),
            self.tls_identity.as_ref(),
        )
        .await
        {
            Ok(channel) => {
                self.agent = VigoAgentClient::new(channel);
                true
            }
            Err(e) => {
                tracing::warn!(error = %e, server = %new_addr, "failover reconnect failed");
                false
            }
        }
    }

    /// If currently failed over, probe the primary server periodically.
    /// Returns true if we successfully failed back to the primary.
    pub async fn maybe_probe_primary(&mut self) -> bool {
        if !self.is_failed_over() {
            return false;
        }

        self.cycles_since_probe += 1;
        if self.cycles_since_probe < PRIMARY_PROBE_CYCLES {
            return false;
        }
        self.cycles_since_probe = 0;

        let primary_addr = &self.servers[0];
        tracing::info!(primary = %primary_addr, "probing primary server");

        match Self::build_channel(
            primary_addr,
            self.tls_ca_pem.as_deref(),
            self.tls_identity.as_ref(),
        )
        .await
        {
            Ok(channel) => {
                // Probe with a lightweight check-in.
                let mut probe_client = VigoAgentClient::new(channel.clone());
                let ts = Self::timestamp_str();
                let sig = crate::auth::signer::sign_payload(b"", &ts, &self.signing_key);
                let req = CheckInRequest {
                    envoy_id: self.envoy_id.clone(),
                    state_fingerprint: String::new(),
                    signature: sig,
                    timestamp: Some(Self::now_timestamp()),
                    traits_json: Vec::new(),
                    policy_version: 0,
                    policy_mode: true,
                    cached_module_hashes: HashMap::new(),
                    capabilities: None,
                    agent_version: self.agent_version.clone(),
                    ip_address: crate::platform::net::get_outbound_ip(),
                };

                match probe_client.check_in(tonic::Request::new(req)).await {
                    Ok(_) => {
                        tracing::info!(
                            primary = %self.servers[0],
                            "primary recovered, reconnecting"
                        );
                        self.agent = VigoAgentClient::new(channel);
                        self.active_server_idx = 0;
                        self.consecutive_failures = 0;
                        true
                    }
                    Err(_) => false,
                }
            }
            Err(_) => false,
        }
    }

    /// Get the envoy ID this client authenticates as.
    pub fn envoy_id(&self) -> &str {
        &self.envoy_id
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
            envoy_id: self.envoy_id.clone(),
            state_fingerprint: state_fingerprint.to_string(),
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
            traits_json: traits_json.unwrap_or_default(),
            policy_version,
            policy_mode: true,
            cached_module_hashes: cached_module_hashes.unwrap_or_default(),
            capabilities: None,
            agent_version: self.agent_version.clone(),
            ip_address: crate::platform::net::get_outbound_ip(),
        };

        let resp = self
            .agent
            .check_in(tonic::Request::new(req))
            .await
            .context("check-in RPC")?;

        Ok(resp.into_inner())
    }

    /// Wake blocks until the server has urgent work or the interval expires.
    /// Replaces tokio::sleep between check-in cycles — gives the server a way
    /// to wake this agent instantly for Scrier tunnels, task dispatch, etc.
    pub async fn wake(&mut self, timeout: Duration) -> Result<WakeResponse> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(self.envoy_id.as_bytes(), &ts, &self.signing_key);

        let req = WakeRequest {
            envoy_id: self.envoy_id.clone(),
            signature: sig,
            timestamp: ts,
        };

        let mut grpc_req = tonic::Request::new(req);
        grpc_req.set_timeout(timeout + Duration::from_secs(5)); // safety margin

        let resp = self
            .agent
            .wake(grpc_req)
            .await
            .context("wake RPC")?;

        Ok(resp.into_inner())
    }

    /// Register calls the server's Register RPC.
    pub async fn register(
        &mut self,
        hostname: &str,
        ip_addr: &str,
        pubkey_pem: &str,
        token: &str,
        force: bool,
    ) -> Result<RegisterResponse> {
        let req = RegisterRequest {
            hostname: hostname.to_string(),
            ip_address: ip_addr.to_string(),
            pubkey_pem: pubkey_pem.to_string(),
            token: token.to_string(),
            force,
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
            envoy_id: self.envoy_id.clone(),
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

    /// ReportResult for a different envoy (used by gateway proxy for device results).
    pub async fn report_result_for(
        &mut self,
        run_id: &str,
        envoy_id: &str,
        results: Vec<ModuleResult>,
    ) -> Result<ReportResultResponse> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(run_id.as_bytes(), &ts, &self.signing_key);

        let req = ReportResultRequest {
            run_id: run_id.to_string(),
            envoy_id: envoy_id.to_string(),
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
            envoy_id: self.envoy_id.clone(),
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

    /// ReportTraits for a different envoy (used by gateway proxy for device traits).
    pub async fn report_traits_for(&mut self, envoy_id: &str, traits_json: &[u8]) -> Result<()> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(traits_json, &ts, &self.signing_key);

        let req = ReportTraitsRequest {
            envoy_id: envoy_id.to_string(),
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
            envoy_id: self.envoy_id.clone(),
            state_fingerprint: state_fingerprint.to_string(),
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
            traits_json: traits_json.unwrap_or_default(),
            policy_version,
            policy_mode: true,
            cached_module_hashes: cached_module_hashes.unwrap_or_default(),
            capabilities,
            agent_version: self.agent_version.clone(),
            ip_address: crate::platform::net::get_outbound_ip(),
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
            envoy_id: self.envoy_id.clone(),
            results,
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
        }
    }

    /// Get a clone of the underlying gRPC agent client (for stream mode).
    pub fn agent_client(&self) -> VigoAgentClient<Channel> {
        self.agent.clone()
    }

    #[cfg(test)]
    /// Create a client for testing failover state machine logic.
    /// The inner gRPC channel points at a dummy address and cannot be used for RPCs.
    pub(crate) fn new_for_test(primary: &str, fallbacks: Vec<String>) -> Self {
        use ed25519_dalek::SigningKey;
        use rand::rngs::OsRng;

        // Build a lazy channel that doesn't actually connect.
        let channel = Channel::from_static("https://[::1]:1")
            .connect_lazy();

        let mut servers = vec![primary.to_string()];
        for fb in &fallbacks {
            if !fb.is_empty() && !servers.contains(fb) {
                servers.push(fb.clone());
            }
        }

        Self {
            agent: VigoAgentClient::new(channel),
            envoy_id: "test-envoy".into(),
            signing_key: SigningKey::generate(&mut OsRng),
            servers,
            active_server_idx: 0,
            consecutive_failures: 0,
            cycles_since_probe: 0,
            tls_ca_pem: None,
            tls_identity: None,
            agent_version: String::new(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_set_fallback_servers() {
        let mut client = Client::new_for_test("primary:1530", vec![]);
        assert_eq!(client.servers, vec!["primary:1530"]);
        assert!(!client.is_failed_over());

        client.set_fallback_servers(vec![
            "peer1:1530".into(),
            "peer2:1530".into(),
        ]);
        assert_eq!(
            client.servers,
            vec!["primary:1530", "peer1:1530", "peer2:1530"]
        );
    }

    #[tokio::test]
    async fn test_set_fallback_servers_dedup() {
        let mut client = Client::new_for_test("primary:1530", vec![]);
        client.set_fallback_servers(vec![
            "peer1:1530".into(),
            "primary:1530".into(), // duplicate of primary
            "peer1:1530".into(),   // duplicate
        ]);
        assert_eq!(
            client.servers,
            vec!["primary:1530", "peer1:1530"]
        );
    }

    #[tokio::test]
    async fn test_record_success_resets_failures() {
        let mut client = Client::new_for_test("primary:1530", vec!["peer1:1530".into()]);
        client.consecutive_failures = 2;
        client.record_success();
        assert_eq!(client.consecutive_failures, 0);
    }

    #[tokio::test]
    async fn test_record_failure_no_fallbacks() {
        let mut client = Client::new_for_test("primary:1530", vec![]);
        // Without fallbacks, record_failure should never trigger failover.
        for _ in 0..10 {
            let switched = client.record_failure().await;
            assert!(!switched);
        }
        assert_eq!(client.active_server_idx, 0);
    }

    #[tokio::test]
    async fn test_failover_after_threshold() {
        let mut client = Client::new_for_test(
            "primary:1530",
            vec!["peer1:1530".into()],
        );

        // First 2 failures don't trigger failover.
        assert!(!client.record_failure().await);
        assert_eq!(client.active_server_idx, 0);
        assert!(!client.record_failure().await);
        assert_eq!(client.active_server_idx, 0);

        // Third failure triggers failover (but build_channel will fail since
        // there's no real server — record_failure returns false because
        // reconnect failed, but the index still advances).
        let _switched = client.record_failure().await;
        // Regardless of whether the channel connected, the server index advanced.
        assert_eq!(client.active_server_idx, 1);
        assert_eq!(client.active_server(), "peer1:1530");
        assert!(client.is_failed_over());
        assert_eq!(client.consecutive_failures, 0);
    }

    #[tokio::test]
    async fn test_failover_wraps_around() {
        let mut client = Client::new_for_test(
            "primary:1530",
            vec!["peer1:1530".into(), "peer2:1530".into()],
        );
        // Force advance past threshold 3 times to wrap through all servers.
        // Advance to peer1 (idx 1).
        client.consecutive_failures = FAILOVER_THRESHOLD - 1;
        client.record_failure().await;
        assert_eq!(client.active_server_idx, 1);

        // Advance to peer2 (idx 2).
        client.consecutive_failures = FAILOVER_THRESHOLD - 1;
        client.record_failure().await;
        assert_eq!(client.active_server_idx, 2);

        // Wrap back to primary (idx 0).
        client.consecutive_failures = FAILOVER_THRESHOLD - 1;
        client.record_failure().await;
        assert_eq!(client.active_server_idx, 0);
        assert!(!client.is_failed_over());
    }

    #[tokio::test]
    async fn test_probe_primary_not_triggered_when_on_primary() {
        let mut client = Client::new_for_test(
            "primary:1530",
            vec!["peer1:1530".into()],
        );
        // On primary — probe should not fire.
        client.cycles_since_probe = PRIMARY_PROBE_CYCLES + 1;
        assert!(!client.maybe_probe_primary().await);
    }

    #[tokio::test]
    async fn test_probe_primary_cycle_counting() {
        let mut client = Client::new_for_test(
            "primary:1530",
            vec!["peer1:1530".into()],
        );
        // Simulate being on fallback.
        client.active_server_idx = 1;

        // Probe doesn't fire until enough cycles.
        for _ in 0..(PRIMARY_PROBE_CYCLES - 1) {
            assert!(!client.maybe_probe_primary().await);
        }
        // On the PRIMARY_PROBE_CYCLES-th call, it tries to probe.
        // It will fail (no real server), but the cycle counter resets.
        let result = client.maybe_probe_primary().await;
        // Probe failed since no server — stays on fallback.
        assert!(!result);
        assert_eq!(client.active_server_idx, 1);
        // Counter was reset.
        assert_eq!(client.cycles_since_probe, 0);
    }
}
