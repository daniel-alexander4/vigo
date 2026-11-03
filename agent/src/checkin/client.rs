// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! gRPC client wrapper with mTLS and ED25519 signing.
//!
//! Mirrors the Go `checkin.Client` — wraps `VigoAgentClient` with automatic
//! request signing and TLS configuration.

use crate::auth::signer;
use crate::proto::{
    vigo_agent_client::VigoAgentClient, CheckInRequest, CheckInResponse, ResourceRunResult,
    DownloadCustomTraitRequest, RegisterRequest, RegisterResponse, ReportResultRequest,
    ReportResultResponse, ReportTraitsRequest, WakeRequest, WakeResponse,
};
use anyhow::{Context, Result};
use ed25519_dalek::SigningKey;
use sha2::{Digest, Sha256};
use std::collections::{BTreeMap, HashSet};
use std::time::Duration;
use tonic::transport::{Certificate, Channel, ClientTlsConfig, Identity};
use tracing::Instrument;

/// Number of consecutive failures before switching to the next server.
const FAILOVER_THRESHOLD: u32 = 3;

/// Number of check-in cycles between primary probe attempts when failed over.
/// At 15s intervals, 20 cycles = ~5 minutes.
const PRIMARY_PROBE_CYCLES: u32 = 20;

/// ADR-033: partition full traits JSON into (vitals_json, inventory_json) along
/// the agent's existing collector classification — volatile collectors
/// (loadavg/memory/swarm/...) are the vitals shipped every check-in; everything
/// else is inventory, shipped only when its content hash changes. Also computes
/// the `vitals_state_hash` over the operational vitals collectors' meaningful
/// content (ADR-033 Option C Phase 1). Returns None when the payload isn't a
/// JSON object (the caller then sends nothing).
fn split_traits(traits_json: &[u8]) -> Option<(Vec<u8>, Vec<u8>, String)> {
    let val: serde_json::Value = serde_json::from_slice(traits_json).ok()?;
    let obj = val.as_object()?;
    let volatile: HashSet<&'static str> = crate::traits::volatile_collectors()
        .iter()
        .map(|c| c.name())
        .collect();
    let mut vitals = serde_json::Map::new();
    let mut inventory = serde_json::Map::new();
    for (k, v) in obj {
        if volatile.contains(k.as_str()) {
            vitals.insert(k.clone(), v.clone());
        } else {
            inventory.insert(k.clone(), v.clone());
        }
    }
    let vitals_state_hash = compute_vitals_state_hash(&vitals);
    Some((
        serde_json::to_vec(&serde_json::Value::Object(vitals)).ok()?,
        serde_json::to_vec(&serde_json::Value::Object(inventory)).ok()?,
        vitals_state_hash,
    ))
}

/// Content hash gating inventory transmission. Server-opaque — it is only ever
/// compared against the value the agent itself last sent, so it need not match
/// any server-side computation.
fn sha256_hex(data: &[u8]) -> String {
    hex::encode(Sha256::digest(data))
}

/// Operational vitals collectors whose MEANINGFUL content the server uses to
/// decide whether a full re-persist/re-flatten is warranted (ADR-033 Option C
/// Phase 1). Their churny telemetry (timestamps, in-flight download progress,
/// login sessions) is stripped before hashing — see `CHURNY_VITALS_KEYS`.
/// Everything else in the vitals blob (loadavg/memory/agent_self/host_self/…)
/// is pure per-tick telemetry routed to the server's live-vitals path, not this
/// hash, so it is deliberately excluded here. `swarm` is the `SwarmCache`
/// collector's JSON key.
const OPERATIONAL_VITALS: &[&str] = &[
    "swarm",
    "swarm_seeds",
    "puddle",
    "gitback",
    "curator",
    "poolq",
    "lockbox",
    "longdrawer",
    "users",
    "ports",
];

/// Keys dropped at any nesting depth before hashing operational vitals: fields
/// that wiggle every check-in without reflecting a meaningful state change. The
/// rescrut crack on "just strip `updated_at`" is honored here — `last_seen`
/// (lockbox peers) and `active_download_ms`/`downloading` (swarm in-flight
/// transfers) churn independently of `updated_at`, so all of them are stripped.
const CHURNY_VITALS_KEYS: &[&str] = &[
    "updated_at",           // per-user swarm collectors stamp this every collect
    "mtime",                // lockbox/longdrawer per-file modification time
    "last_seen",            // lockbox peer liveness
    "active_download_ms",   // swarm in-flight transfer progress (per sha)
    "downloading",          // swarm in-flight chunk counts
    "chunk_sources",        // swarm transient per-chunk source map
    "chunk_details",        // swarm per-chunk progress (carries ms)
    "logged_in",            // users login/logout session churn
    "bare_repo_size_bytes", // gitback bare-repo dir size drifts with git GC
];

/// Recursively drop churny keys and sort array elements so the resulting bytes
/// depend only on meaningful content — independent of cosmetic reordering
/// (`read_dir` order, push order) or telemetry wiggle. Object keys are already
/// canonical because `serde_json::Map` is a `BTreeMap` (no `preserve_order`).
fn canonicalize_meaningful(v: &serde_json::Value) -> serde_json::Value {
    use serde_json::Value;
    match v {
        Value::Object(map) => {
            let mut out = serde_json::Map::new();
            for (k, val) in map {
                if CHURNY_VITALS_KEYS.contains(&k.as_str()) {
                    continue;
                }
                out.insert(k.clone(), canonicalize_meaningful(val));
            }
            Value::Object(out)
        }
        Value::Array(arr) => {
            let mut items: Vec<Value> = arr.iter().map(canonicalize_meaningful).collect();
            // Sort by serialized form so set-equal arrays in any order hash equal.
            items.sort_by_key(|a| a.to_string());
            Value::Array(items)
        }
        other => other.clone(),
    }
}

/// sha256-hex over the meaningful content of the operational vitals collectors.
/// Server-opaque, like `sha256_hex` above — only ever compared against the
/// agent's own prior value, so the exact serialization need not match any
/// server-side computation. Empty string when no operational vitals are present.
fn compute_vitals_state_hash(vitals: &serde_json::Map<String, serde_json::Value>) -> String {
    let mut canon = serde_json::Map::new();
    for name in OPERATIONAL_VITALS {
        if let Some(v) = vitals.get(*name) {
            canon.insert((*name).to_string(), canonicalize_meaningful(v));
        }
    }
    if canon.is_empty() {
        return String::new();
    }
    match serde_json::to_vec(&serde_json::Value::Object(canon)) {
        Ok(bytes) => sha256_hex(&bytes),
        Err(_) => String::new(),
    }
}

/// ADR-033 conditional-traits gate state (behind `Client.inventory_gate`).
#[derive(Default)]
struct InventoryGate {
    /// Hash of the inventory the server last accepted from us; None until the
    /// first inventory is sent. A vitals-only check-in is safe only when this
    /// matches the agent's current inventory hash.
    last_hash: Option<String>,
    /// Set when the server returned `resend_inventory` — forces the next
    /// check-in to ship the full inventory even if its hash is unchanged.
    force_resend: bool,
}

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

    /// Instant of the previous CheckIn request, used to compute the
    /// inter-CheckIn delta the server needs to size each envoy's
    /// staleness threshold (2.5 × max(configured_interval,
    /// observed_cycle)). None on first contact — server falls back to
    /// configured interval until the second CheckIn arrives.
    /// `Mutex` gives interior mutability so request builders can stay
    /// `&self` (some are reached through immutable borrows in the
    /// stream loop). Lock is held for nanoseconds and uncontended.
    last_checkin_instant: std::sync::Mutex<Option<std::time::Instant>>,

    /// ADR-033 conditional-traits gate. Behind a `Mutex` for interior mutability
    /// so the stream-path request builder (`&self`, reached through immutable
    /// borrows in the lock-step stream loop) can read it — mirroring
    /// `last_checkin_instant`. `prepare_split` reads it to decide whether to ship
    /// the inventory; `commit_inventory` records the accepted hash + resend flag
    /// after the response. Shared across the unary and stream paths (so a
    /// unary→stream promotion doesn't re-send the inventory the unary cycle just
    /// sent). Never accessed concurrently — the agent builds check-ins from one
    /// loop at a time — but the lock keeps it sound regardless.
    inventory_gate: std::sync::Mutex<InventoryGate>,
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

        let identity_pem =
            if let (Some(cert_path), Some(key_path)) = (client_cert_file, client_key_file) {
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

        let channel =
            Self::build_channel(server_addr, ca_pem.as_deref(), identity_pem.as_ref()).await?;

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
            last_checkin_instant: std::sync::Mutex::new(None),
            inventory_gate: std::sync::Mutex::new(InventoryGate::default()),
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

        // Not `.split(':').next()` — that takes everything before the first
        // colon, which for an IPv6 literal is "[2605", and tonic rejects it
        // with a bare "configure TLS". rustls wants the address unbracketed so
        // it parses as an IP and matches the cert's IP SAN.
        let domain = match crate::bootstrap::tls_server_name(server_addr) {
            "" => "localhost",
            d => d,
        };
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
                // Probe doesn't advance the inter-CheckIn cursor: it
                // reaches a different (primary) server than the active
                // server's recurring CheckIn loop, and stamping a delta
                // here would reset the cadence the server sees on the
                // failover channel. Send 0 (server treats as bootstrap).
                let (traceparent, _) = super::traceparent::generate();
                let req = CheckInRequest {
                    envoy_id: self.envoy_id.clone(),
                    state_fingerprint: String::new(),
                    signature: sig,
                    timestamp: Some(Self::now_timestamp()),
                    traits_json: Vec::new(),
                    policy_version: 0,
                    policy_mode: true,
                    cached_configcrate_hashes: BTreeMap::new(),
                    capabilities: None,
                    agent_version: self.agent_version.clone(),
                    ip_address: crate::platform::net::get_outbound_ip(),
                    last_checkin_delta_ms: 0,
                    traceparent,
                    ..Default::default() // ADR-033 vitals/inventory: empty for a probe (no traits)
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

    /// Compute the inter-CheckIn delta and advance the cursor. Returns
    /// 0 on first contact (server treats as bootstrap and falls back
    /// to configured interval). Saturates to u32::MAX ms; the server
    /// re-clamps to freshness.MaxReportedCycle. Uses interior
    /// mutability so the request builders that this drives can stay
    /// `&self` for the immutable-borrow paths in the stream loop.
    fn next_checkin_delta_ms(&self) -> u32 {
        let now = std::time::Instant::now();
        let mut guard = self.last_checkin_instant.lock().unwrap();
        let delta = match *guard {
            Some(prev) => now
                .saturating_duration_since(prev)
                .as_millis()
                .min(u32::MAX as u128) as u32,
            None => 0,
        };
        *guard = Some(now);
        delta
    }

    /// Get current Unix timestamp as decimal string (matches Go's strconv.FormatInt).
    fn timestamp_str() -> String {
        use std::time::{SystemTime, UNIX_EPOCH};
        // unwrap_or_default (epoch) rather than unwrap: a pre-1970 wall clock
        // (misconfigured RTC before NTP) would otherwise panic this hot path
        // on every check-in. The server's timestamp-skew check rejects the
        // bogus value, and the daemon loop handles that without crashing.
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs()
            .to_string()
    }

    /// Build a prost_types::Timestamp for the current time.
    fn now_timestamp() -> prost_types::Timestamp {
        use std::time::{SystemTime, UNIX_EPOCH};
        let d = SystemTime::now().duration_since(UNIX_EPOCH).unwrap_or_default();
        prost_types::Timestamp {
            seconds: d.as_secs() as i64,
            nanos: d.subsec_nanos() as i32,
        }
    }

    /// ADR-033: partition the full traits payload into (vitals, inventory,
    /// inventory_hash) and decide whether to ship the inventory this cycle.
    /// Vitals ride every check-in; the inventory rides only when its hash changed
    /// or the server requested a resend. A payload that can't be split (None /
    /// not a JSON object) yields all-empty — the server skips traits that cycle.
    /// Pure read of the gate; the caller records the outcome via
    /// `commit_inventory` once it holds the response.
    fn prepare_split(&self, traits_json: Option<Vec<u8>>) -> (Vec<u8>, Vec<u8>, String, String) {
        let (vitals, inventory, vitals_state_hash) =
            match traits_json.as_deref().and_then(split_traits) {
                Some(t) => t,
                None => return (Vec::new(), Vec::new(), String::new(), String::new()),
            };
        let hash = sha256_hex(&inventory);
        let send_inventory = {
            let gate = self.inventory_gate.lock().unwrap();
            gate.force_resend || gate.last_hash.as_deref() != Some(hash.as_str())
        };
        let inv = if send_inventory { inventory } else { Vec::new() };
        (vitals, inv, hash, vitals_state_hash)
    }

    /// ADR-033: after a check-in response, record the inventory the server now
    /// holds and honor a resend request. `sent_hash` is the inventory hash the
    /// request carried (empty when no traits were sent). Recording only after the
    /// response — never optimistically in the builder — means a failed send never
    /// advances the gate, so the inventory is re-shipped next cycle. Called by
    /// both the unary and stream paths.
    pub(crate) fn commit_inventory(&self, sent_hash: &str, resend: bool) {
        let mut gate = self.inventory_gate.lock().unwrap();
        if !sent_hash.is_empty() {
            gate.last_hash = Some(sent_hash.to_string());
        }
        gate.force_resend = resend;
    }

    /// CheckIn calls the server's CheckIn RPC with ED25519-signed request.
    pub async fn check_in(
        &mut self,
        state_fingerprint: &str,
        traits_json: Option<Vec<u8>>,
        policy_version: u64,
        cached_configcrate_hashes: Option<BTreeMap<String, String>>,
    ) -> Result<CheckInResponse> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(state_fingerprint.as_bytes(), &ts, &self.signing_key);
        let (traceparent, trace_id) = super::traceparent::generate();

        // ADR-033 conditional traits: vitals every check-in, inventory only when
        // changed or the server requested a resend (see prepare_split).
        let (vitals_json, inventory_json, inventory_hash, vitals_state_hash) =
            self.prepare_split(traits_json);

        let req = CheckInRequest {
            envoy_id: self.envoy_id.clone(),
            state_fingerprint: state_fingerprint.to_string(),
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
            traits_json: Vec::new(), // split mode: full payload rides vitals + inventory below
            policy_version,
            policy_mode: true,
            cached_configcrate_hashes: cached_configcrate_hashes.unwrap_or_default(),
            capabilities: None,
            agent_version: self.agent_version.clone(),
            ip_address: crate::platform::net::get_outbound_ip(),
            last_checkin_delta_ms: self.next_checkin_delta_ms(),
            traceparent,
            vitals_json,
            inventory_json,
            inventory_hash: inventory_hash.clone(),
            vitals_state_hash,
        };

        let span = tracing::info_span!("check_in", trace_id = %trace_id);
        let resp = self
            .agent
            .check_in(tonic::Request::new(req))
            .instrument(span)
            .await
            .context("check-in RPC")?
            .into_inner();

        // Inflate a gzip-compressed bundle (large config-change responses) back
        // into policy_bundle before anything downstream reads it.
        let mut resp = resp;
        super::inflate_policy_bundle(&mut resp);

        // Record what the server now holds, and honor a resend request next cycle.
        self.commit_inventory(&inventory_hash, resp.resend_inventory);

        Ok(resp)
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

        let resp = self.agent.wake(grpc_req).await.context("wake RPC")?;

        Ok(resp.into_inner())
    }

    /// ResolveSecrets fetches the plaintext values of placeholder-bearing
    /// secret references from the server. The server checks that every ref
    /// is entitled (i.e. appears as a placeholder in the envoy's resolved
    /// bundle) and rejects the whole call with PermissionDenied otherwise.
    ///
    /// The signed payload is the sorted refs joined with NUL bytes, plus
    /// the timestamp — matches verifyResolveSecretsCtx on the server.
    pub async fn resolve_secrets(
        &mut self,
        refs: Vec<String>,
    ) -> Result<crate::proto::ResolveSecretsResponse> {
        use crate::proto::ResolveSecretsRequest;
        let ts = Self::timestamp_str();
        let mut sorted = refs.clone();
        sorted.sort();
        let payload = sorted.join("\x00");
        let sig = signer::sign_payload(payload.as_bytes(), &ts, &self.signing_key);

        let req = ResolveSecretsRequest {
            envoy_id: self.envoy_id.clone(),
            signature: sig,
            timestamp: ts,
            refs,
        };

        let resp = self
            .agent
            .resolve_secrets(tonic::Request::new(req))
            .await
            .context("resolve_secrets RPC")?;

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
        results: Vec<ResourceRunResult>,
        total_count: i32,
    ) -> Result<ReportResultResponse> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(run_id.as_bytes(), &ts, &self.signing_key);
        let (traceparent, trace_id) = super::traceparent::generate();

        let req = ReportResultRequest {
            run_id: run_id.to_string(),
            envoy_id: self.envoy_id.clone(),
            results,
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
            traceparent,
            total_count,
        };

        let span = tracing::info_span!("report_result", trace_id = %trace_id, run_id = %run_id);
        let resp = self
            .agent
            .report_result(tonic::Request::new(req))
            .instrument(span)
            .await
            .context("report-result RPC")?;

        Ok(resp.into_inner())
    }

    /// ReportResult for a different envoy (used by gateway proxy for device results).
    pub async fn report_result_for(
        &mut self,
        run_id: &str,
        envoy_id: &str,
        results: Vec<ResourceRunResult>,
        total_count: i32,
    ) -> Result<ReportResultResponse> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(run_id.as_bytes(), &ts, &self.signing_key);
        let (traceparent, trace_id) = super::traceparent::generate();

        let req = ReportResultRequest {
            run_id: run_id.to_string(),
            envoy_id: envoy_id.to_string(),
            results,
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
            traceparent,
            total_count,
        };

        let span = tracing::info_span!("report_result_for", trace_id = %trace_id, run_id = %run_id);
        let resp = self
            .agent
            .report_result(tonic::Request::new(req))
            .instrument(span)
            .await
            .context("report-result-for RPC")?;

        Ok(resp.into_inner())
    }

    /// ReportTraits calls the server's ReportTraits RPC with signed traits JSON.
    pub async fn report_traits(&mut self, traits_json: &[u8]) -> Result<()> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(traits_json, &ts, &self.signing_key);
        let (traceparent, trace_id) = super::traceparent::generate();

        let req = ReportTraitsRequest {
            envoy_id: self.envoy_id.clone(),
            traits_json: traits_json.to_vec(),
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
            traceparent,
        };

        let span = tracing::info_span!("report_traits", trace_id = %trace_id);
        self.agent
            .report_traits(tonic::Request::new(req))
            .instrument(span)
            .await
            .context("report-traits RPC")?;

        Ok(())
    }

    /// ReportTraits for a different envoy (used by gateway proxy for device traits).
    pub async fn report_traits_for(&mut self, envoy_id: &str, traits_json: &[u8]) -> Result<()> {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(traits_json, &ts, &self.signing_key);
        let (traceparent, trace_id) = super::traceparent::generate();

        let req = ReportTraitsRequest {
            envoy_id: envoy_id.to_string(),
            traits_json: traits_json.to_vec(),
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
            traceparent,
        };

        let span = tracing::info_span!("report_traits_for", trace_id = %trace_id);
        self.agent
            .report_traits(tonic::Request::new(req))
            .instrument(span)
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
    /// Advances the inter-CheckIn cursor via interior mutability.
    pub fn build_check_in_request(
        &self,
        state_fingerprint: &str,
        traits_json: Option<Vec<u8>>,
        policy_version: u64,
        cached_configcrate_hashes: Option<BTreeMap<String, String>>,
    ) -> CheckInRequest {
        self.build_check_in_request_with_caps(
            state_fingerprint,
            traits_json,
            policy_version,
            cached_configcrate_hashes,
            None,
        )
    }

    /// Build a signed CheckInRequest with optional StreamCapabilities.
    /// Advances the inter-CheckIn cursor used for the
    /// `last_checkin_delta_ms` field — the server uses that delta to
    /// size the per-envoy staleness threshold.
    pub fn build_check_in_request_with_caps(
        &self,
        state_fingerprint: &str,
        traits_json: Option<Vec<u8>>,
        policy_version: u64,
        cached_configcrate_hashes: Option<BTreeMap<String, String>>,
        capabilities: Option<crate::proto::StreamCapabilities>,
    ) -> CheckInRequest {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(state_fingerprint.as_bytes(), &ts, &self.signing_key);
        let (traceparent, _) = super::traceparent::generate();

        // ADR-033: split like the unary path — vitals every check-in, inventory
        // only on change/resend. The caller records the outcome via
        // `commit_inventory` once it has the response (the builder is `&self`).
        let (vitals_json, inventory_json, inventory_hash, vitals_state_hash) =
            self.prepare_split(traits_json);

        CheckInRequest {
            envoy_id: self.envoy_id.clone(),
            state_fingerprint: state_fingerprint.to_string(),
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
            traits_json: Vec::new(), // split mode: payload rides vitals + inventory
            policy_version,
            policy_mode: true,
            cached_configcrate_hashes: cached_configcrate_hashes.unwrap_or_default(),
            capabilities,
            agent_version: self.agent_version.clone(),
            ip_address: crate::platform::net::get_outbound_ip(),
            last_checkin_delta_ms: self.next_checkin_delta_ms(),
            traceparent,
            vitals_json,
            inventory_json,
            inventory_hash,
            vitals_state_hash,
        }
    }

    /// Build a signed ReportResultRequest without sending it (for stream mode).
    pub fn build_report_result_request(
        &self,
        run_id: &str,
        results: Vec<ResourceRunResult>,
        total_count: i32,
    ) -> ReportResultRequest {
        let ts = Self::timestamp_str();
        let sig = signer::sign_payload(run_id.as_bytes(), &ts, &self.signing_key);
        let (traceparent, _) = super::traceparent::generate();

        ReportResultRequest {
            run_id: run_id.to_string(),
            envoy_id: self.envoy_id.clone(),
            results,
            signature: sig,
            timestamp: Some(Self::now_timestamp()),
            traceparent,
            total_count,
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

        // Ensure the rustls CryptoProvider is installed (needed for Channel TLS).
        let _ = rustls::crypto::ring::default_provider().install_default();

        // Build a lazy channel that doesn't actually connect.
        let channel = Channel::from_static("https://[::1]:1").connect_lazy();

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
            last_checkin_instant: std::sync::Mutex::new(None),
            inventory_gate: std::sync::Mutex::new(InventoryGate::default()),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_split_traits_partitions_by_volatility() {
        // loadavg/uptime are volatile (vitals); packages is stable (inventory).
        let traits = br#"{"loadavg":{"load1":1.0},"packages":["a","b"],"uptime":{"seconds":5}}"#;
        let (vitals, inventory, _hash) = split_traits(traits).expect("split a JSON object");
        let v: serde_json::Value = serde_json::from_slice(&vitals).unwrap();
        let i: serde_json::Value = serde_json::from_slice(&inventory).unwrap();
        assert!(v.get("loadavg").is_some() && v.get("uptime").is_some());
        assert!(v.get("packages").is_none());
        assert!(i.get("packages").is_some());
        assert!(i.get("loadavg").is_none() && i.get("uptime").is_none());
    }

    #[tokio::test]
    async fn test_stream_builder_gates_inventory_via_commit() {
        // ADR-033 #2: the stream builder splits like the unary path, gated by the
        // shared inventory_gate. prepare_split only reads; commit_inventory records
        // after the response — so the inventory rides only on change / resend.
        let client = Client::new_for_test("https://localhost:1", vec![]);
        let traits = br#"{"loadavg":{"load1":1.0},"packages":["a","b"]}"#.to_vec();

        // First build ships the full inventory (gate empty).
        let req1 = client.build_check_in_request("", Some(traits.clone()), 0, None);
        assert!(req1.traits_json.is_empty(), "split mode leaves traits_json empty");
        assert!(!req1.vitals_json.is_empty(), "vitals always sent");
        assert!(!req1.inventory_json.is_empty(), "first inventory sent");
        assert!(!req1.inventory_hash.is_empty());

        // Server accepted it; unchanged traits now ship vitals only.
        client.commit_inventory(&req1.inventory_hash, false);
        let req2 = client.build_check_in_request("", Some(traits.clone()), 0, None);
        assert!(!req2.vitals_json.is_empty());
        assert!(req2.inventory_json.is_empty(), "unchanged inventory not resent");
        assert_eq!(req2.inventory_hash, req1.inventory_hash);

        // A resend request forces the inventory back onto the next check-in.
        client.commit_inventory(&req2.inventory_hash, true);
        let req3 = client.build_check_in_request("", Some(traits), 0, None);
        assert!(!req3.inventory_json.is_empty(), "resend forces inventory");
    }

    #[test]
    fn test_split_traits_non_object_returns_none() {
        assert!(split_traits(b"not json").is_none());
        assert!(split_traits(b"[1,2,3]").is_none());
    }

    #[test]
    fn test_sha256_hex_is_stable_and_lowercase() {
        let h1 = sha256_hex(b"{\"a\":1}");
        let h2 = sha256_hex(b"{\"a\":1}");
        assert_eq!(h1, h2);
        assert_eq!(h1.len(), 64);
        assert_eq!(h1, h1.to_lowercase());
        assert_ne!(h1, sha256_hex(b"{\"a\":2}"));
    }

    /// Helper: compute vitals_state_hash from a JSON object literal of vitals.
    fn vsh(vitals_json: &str) -> String {
        let v: serde_json::Value = serde_json::from_str(vitals_json).unwrap();
        compute_vitals_state_hash(v.as_object().unwrap())
    }

    #[test]
    fn test_vitals_state_hash_ignores_churny_telemetry() {
        // Two ticks: identical meaningful content, only churny fields differ
        // (updated_at, peer last_seen, swarm in-flight progress). Hash must match.
        let tick_a = r#"{
            "puddle":{"updated_at":100,"puddle_count":1,"puddles":[{"user":"dan","initialized":true}]},
            "lockbox":{"updated_at":100,"users":[{"user":"dan","files_digest":"abc","peers":[{"hostname":"plex","last_seen":100}]}]},
            "swarm":{"blob_count":2,"blobs":["sha1","sha2"],"active_download_ms":4200,"downloading":3},
            "ports":{"listening":[22,443]}
        }"#;
        let tick_b = r#"{
            "puddle":{"updated_at":999,"puddle_count":1,"puddles":[{"user":"dan","initialized":true}]},
            "lockbox":{"updated_at":999,"users":[{"user":"dan","files_digest":"abc","peers":[{"hostname":"plex","last_seen":888}]}]},
            "swarm":{"blob_count":2,"blobs":["sha1","sha2"],"active_download_ms":17,"downloading":0},
            "ports":{"listening":[22,443]}
        }"#;
        assert_eq!(vsh(tick_a), vsh(tick_b), "churny telemetry must not perturb the hash");
        assert!(!vsh(tick_a).is_empty());
    }

    #[test]
    fn test_vitals_state_hash_changes_on_meaningful_change() {
        let base = r#"{"swarm":{"blob_count":2,"blobs":["sha1","sha2"]},"ports":{"listening":[22]}}"#;
        // A new blob (meaningful), a new listening port, and changed puddle membership
        // each flip the hash.
        let new_blob = r#"{"swarm":{"blob_count":3,"blobs":["sha1","sha2","sha3"]},"ports":{"listening":[22]}}"#;
        let new_port = r#"{"swarm":{"blob_count":2,"blobs":["sha1","sha2"]},"ports":{"listening":[22,8443]}}"#;
        assert_ne!(vsh(base), vsh(new_blob), "a new blob is meaningful");
        assert_ne!(vsh(base), vsh(new_port), "a new listening port is meaningful");
    }

    #[test]
    fn test_vitals_state_hash_order_independent() {
        // Same set of blobs in a different array order hashes equal (read_dir /
        // push order must not matter).
        let a = r#"{"swarm":{"blob_count":2,"blobs":["sha1","sha2"]}}"#;
        let b = r#"{"swarm":{"blob_count":2,"blobs":["sha2","sha1"]}}"#;
        assert_eq!(vsh(a), vsh(b), "array element order must not perturb the hash");
    }

    #[test]
    fn test_vitals_state_hash_excludes_non_operational_vitals() {
        // loadavg/memory/agent_self are vitals but NOT operational — they belong
        // to the live-vitals path, so they must never enter this hash.
        let with_health = r#"{"ports":{"listening":[22]},"loadavg":{"load1":9.9},"agent_self":{"rss_bytes":12345}}"#;
        let ports_only = r#"{"ports":{"listening":[22]}}"#;
        assert_eq!(vsh(with_health), vsh(ports_only), "non-operational vitals excluded");
    }

    #[test]
    fn test_vitals_state_hash_empty_when_no_operational_vitals() {
        let none = r#"{"loadavg":{"load1":1.0},"memory":{"total":8}}"#;
        assert!(vsh(none).is_empty(), "no operational collectors → empty hash");
    }

    #[tokio::test]
    async fn test_set_fallback_servers() {
        let mut client = Client::new_for_test("primary:1530", vec![]);
        assert_eq!(client.servers, vec!["primary:1530"]);
        assert!(!client.is_failed_over());

        client.set_fallback_servers(vec!["peer1:1530".into(), "peer2:1530".into()]);
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
        assert_eq!(client.servers, vec!["primary:1530", "peer1:1530"]);
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
        let mut client = Client::new_for_test("primary:1530", vec!["peer1:1530".into()]);

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
        let mut client = Client::new_for_test("primary:1530", vec!["peer1:1530".into()]);
        // On primary — probe should not fire.
        client.cycles_since_probe = PRIMARY_PROBE_CYCLES + 1;
        assert!(!client.maybe_probe_primary().await);
    }

    #[tokio::test]
    async fn test_probe_primary_cycle_counting() {
        let mut client = Client::new_for_test("primary:1530", vec!["peer1:1530".into()]);
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
