// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! TLS for the swarm peer-by-IP content plane.
//!
//! The three content-plane HTTP clients (lockbox pusher, peer_client,
//! gitback fan-out) connect to *other envoys* by IP on port 1531. Those
//! peers present an mTLS server cert issued by the fleet CA, but the cert's
//! CN/SAN is the agent UUID — never the dialed IP — so rustls' standard
//! hostname verification always fails. The previous code reacted by turning
//! verification off wholesale (`disable_verification(true)`), which also
//! dropped the *chain* check: a LAN attacker answering on a peer's IP could
//! terminate the connection with any self-signed cert.
//!
//! The correct trust model for these connections is **"any cert that chains
//! to the fleet CA"** — peer identity is the CA, not the name. This module
//! provides a custom rustls [`ServerCertVerifier`] that verifies the chain
//! to the fleet CA but skips the hostname/SAN check, and builds a
//! `reqwest::blocking::Client` around it. mTLS is preserved: the client
//! still presents the envoy's client cert, which the peer's server verifies
//! against the same CA.
//!
//! ureq 3.3 exposes no custom-verifier hook (only `disable_verification`),
//! which is why this path uses reqwest + a preconfigured rustls
//! `ClientConfig` instead. The server-directed clients (curator resolver,
//! poolq CLI, puddle name/pubkey bootstraps, bootstrap enroll) talk to the
//! configured server by hostname and correctly keep standard verification —
//! they stay on ureq and are untouched.

use std::io::BufReader;
use std::sync::Arc;
use std::time::Duration;

use anyhow::{bail, Context, Result};
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::client::WebPkiServerVerifier;
use rustls::crypto::CryptoProvider;
use rustls::pki_types::{CertificateDer, PrivateKeyDer, ServerName, UnixTime};
use rustls::{ClientConfig, DigitallySignedStruct, RootCertStore, SignatureScheme};

use crate::update::TlsCerts;

/// The HTTP client type used across the swarm content plane. A thin alias
/// over `reqwest::blocking::Client` so the (many) consumer signatures that
/// thread this client around read as one concept and so the underlying type
/// can be swapped in exactly one place if needed. Cheap to clone (Arc
/// internally), matching the ureq agent it replaced.
pub type SwarmAgent = reqwest::blocking::Client;

/// Custom rustls server-cert verifier: accepts any server cert that chains
/// to the fleet CA, regardless of the name on the cert. Hostname/SAN
/// checking is deliberately skipped because envoy certs are CN'd by IP and
/// we dial peers by IP. Everything else (chain to a trusted root, validity
/// window, handshake-signature verification) is delegated to webpki, exactly
/// as a stock verifier would.
#[derive(Debug)]
struct FleetCaVerifier {
    roots: Arc<RootCertStore>,
    /// Stock webpki verifier — used only to delegate the TLS 1.2/1.3
    /// handshake-signature checks and the supported-scheme list. Its
    /// `verify_server_cert` (which would enforce the name) is never called.
    inner: Arc<WebPkiServerVerifier>,
    /// Signature-verification algorithms from the process crypto provider,
    /// passed to the chain-verification call.
    supported_algs: Vec<&'static dyn rustls::pki_types::SignatureVerificationAlgorithm>,
}

impl FleetCaVerifier {
    fn new(roots: Arc<RootCertStore>) -> Result<Self> {
        let provider = CryptoProvider::get_default()
            .context("no process-default rustls CryptoProvider installed")?;
        let inner = WebPkiServerVerifier::builder_with_provider(roots.clone(), provider.clone())
            .build()
            .context("building inner WebPkiServerVerifier")?;
        let supported_algs = provider
            .signature_verification_algorithms
            .all
            .to_vec();
        Ok(Self {
            roots,
            inner,
            supported_algs,
        })
    }
}

impl ServerCertVerifier for FleetCaVerifier {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        intermediates: &[CertificateDer<'_>],
        _server_name: &ServerName<'_>,
        _ocsp_response: &[u8],
        now: UnixTime,
    ) -> std::result::Result<ServerCertVerified, rustls::Error> {
        // Chain-to-CA WITHOUT the server-name check. This is the entire
        // point of the verifier: peer identity is "signed by the fleet CA",
        // not "named X". The name argument is intentionally ignored.
        let parsed = rustls::client::verify_server_cert_signed_by_trust_anchor(
            &rustls::server::ParsedCertificate::try_from(end_entity)?,
            &self.roots,
            intermediates,
            now,
            &self.supported_algs,
        );
        parsed.map(|()| ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> std::result::Result<HandshakeSignatureValid, rustls::Error> {
        self.inner.verify_tls12_signature(message, cert, dss)
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> std::result::Result<HandshakeSignatureValid, rustls::Error> {
        self.inner.verify_tls13_signature(message, cert, dss)
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.inner.supported_verify_schemes()
    }
}

/// Load PEM bytes and split into certs / private key, then build a rustls
/// `ClientConfig` that:
///   - verifies the peer's server cert chains to the fleet CA (skip name),
///   - presents the envoy's client cert (mTLS preserved).
pub fn build_swarm_client_config(certs: &TlsCerts) -> Result<Arc<ClientConfig>> {
    // CA → RootCertStore (mirrors server.rs::build_tls_acceptor).
    let ca_pem = std::fs::read(&certs.ca_cert).context("reading CA cert")?;
    let mut ca_reader = BufReader::new(ca_pem.as_slice());
    let mut root_store = RootCertStore::empty();
    for cert in rustls_pemfile::certs(&mut ca_reader) {
        let cert = cert.context("parsing CA cert")?;
        root_store.add(cert).context("adding CA to root store")?;
    }
    if root_store.is_empty() {
        bail!("no certificates found in {}", certs.ca_cert);
    }
    let roots = Arc::new(root_store);

    // Client cert chain.
    let cert_pem = std::fs::read(&certs.client_cert).context("reading client cert")?;
    let mut cert_reader = BufReader::new(cert_pem.as_slice());
    let chain: Vec<CertificateDer<'static>> = rustls_pemfile::certs(&mut cert_reader)
        .filter_map(|r| r.ok())
        .collect();
    if chain.is_empty() {
        bail!("no certificates found in {}", certs.client_cert);
    }

    // Client private key.
    let key_pem = std::fs::read(&certs.client_key).context("reading client key")?;
    let mut key_reader = BufReader::new(key_pem.as_slice());
    let key: PrivateKeyDer<'static> = rustls_pemfile::private_key(&mut key_reader)
        .context("parsing client key")?
        .context("no private key found in client key PEM")?;

    let verifier = Arc::new(FleetCaVerifier::new(roots)?);

    let config = ClientConfig::builder()
        .dangerous()
        .with_custom_certificate_verifier(verifier)
        .with_client_auth_cert(chain, key)
        .context("building swarm client TLS config (with client auth)")?;

    Ok(Arc::new(config))
}

/// Build a blocking HTTP client for the swarm content plane from the fleet
/// mTLS certs, with the given per-site timeouts.
///
/// `connect` bounds the TCP+TLS handshake; `total` is the overall per-request
/// timeout (covers send + receive of the body). Both mirror the ureq agents
/// these clients replaced.
pub fn build_client(certs: &TlsCerts, connect: Duration, total: Duration) -> Result<SwarmAgent> {
    let config = build_swarm_client_config(certs)?;
    reqwest::blocking::Client::builder()
        .use_preconfigured_tls((*config).clone())
        .connect_timeout(connect)
        .timeout(total)
        .build()
        .context("building swarm blocking HTTP client")
}

#[cfg(test)]
mod tests {
    use super::*;
    use rcgen::{CertificateParams, DistinguishedName, DnType, KeyPair};

    /// Mint a self-signed CA and a leaf signed by it. `leaf_cn` is put in the
    /// leaf's CN + as a SAN so we can prove name-mismatch independent of the
    /// dialed name. Returns (ca_der, leaf_der, leaf_cn).
    fn mint_ca_and_leaf(leaf_cn: &str) -> (CertificateDer<'static>, CertificateDer<'static>) {
        // CA.
        let mut ca_params = CertificateParams::new(vec![]).unwrap();
        ca_params.is_ca = rcgen::IsCa::Ca(rcgen::BasicConstraints::Unconstrained);
        let mut ca_dn = DistinguishedName::new();
        ca_dn.push(DnType::CommonName, "Test Fleet CA");
        ca_params.distinguished_name = ca_dn;
        let ca_key = KeyPair::generate().unwrap();
        let ca_cert = ca_params.self_signed(&ca_key).unwrap();

        // Leaf signed by the CA, named with a value that will NOT match any
        // dialed hostname/IP (proving the skip-hostname behavior).
        let mut leaf_params = CertificateParams::new(vec![leaf_cn.to_string()]).unwrap();
        let mut leaf_dn = DistinguishedName::new();
        leaf_dn.push(DnType::CommonName, leaf_cn);
        leaf_params.distinguished_name = leaf_dn;
        let leaf_key = KeyPair::generate().unwrap();
        let leaf_cert = leaf_params.signed_by(&leaf_key, &ca_cert, &ca_key).unwrap();

        (
            CertificateDer::from(ca_cert.der().to_vec()),
            CertificateDer::from(leaf_cert.der().to_vec()),
        )
    }

    fn ensure_provider() {
        // The binary installs ring process-wide at startup; tests don't run
        // that path, so install it here (idempotent — ignore "already set").
        let _ = rustls::crypto::ring::default_provider().install_default();
    }

    fn verifier_with_roots(ca: &CertificateDer<'static>) -> FleetCaVerifier {
        let mut roots = RootCertStore::empty();
        roots.add(ca.clone()).unwrap();
        FleetCaVerifier::new(Arc::new(roots)).unwrap()
    }

    #[test]
    fn accepts_fleet_ca_leaf_regardless_of_name() {
        ensure_provider();
        let (ca, leaf) = mint_ca_and_leaf("192.0.2.7"); // IP-shaped CN
        let verifier = verifier_with_roots(&ca);

        // Dial under a name that does NOT match the leaf's CN/SAN.
        let dialed = ServerName::try_from("10.9.9.9").unwrap();
        let res = verifier.verify_server_cert(
            &leaf,
            &[],
            &dialed,
            &[],
            UnixTime::now(),
        );
        assert!(
            res.is_ok(),
            "fleet-CA-signed leaf must be accepted despite the name mismatch: {res:?}"
        );
    }

    #[test]
    fn rejects_leaf_from_a_different_ca() {
        ensure_provider();
        // Trust fleet CA A, but present a leaf signed by an unrelated CA B.
        let (ca_a, _leaf_a) = mint_ca_and_leaf("192.0.2.7");
        let (_ca_b, leaf_b) = mint_ca_and_leaf("192.0.2.7");
        let verifier = verifier_with_roots(&ca_a);

        let dialed = ServerName::try_from("192.0.2.7").unwrap();
        let res = verifier.verify_server_cert(
            &leaf_b,
            &[],
            &dialed,
            &[],
            UnixTime::now(),
        );
        assert!(
            res.is_err(),
            "leaf signed by a non-fleet CA must be rejected"
        );
    }

    #[test]
    fn stock_verifier_rejects_fleet_leaf_on_name() {
        // Proves the skip-hostname behavior is what differs: a stock webpki
        // verifier rejects the very same fleet-CA leaf on a name mismatch,
        // while our FleetCaVerifier (above) accepts it.
        ensure_provider();
        let (ca, leaf) = mint_ca_and_leaf("192.0.2.7");
        let mut roots = RootCertStore::empty();
        roots.add(ca).unwrap();
        let stock = WebPkiServerVerifier::builder(Arc::new(roots)).build().unwrap();

        let dialed = ServerName::try_from("10.9.9.9").unwrap();
        let res = stock.verify_server_cert(&leaf, &[], &dialed, &[], UnixTime::now());
        assert!(
            res.is_err(),
            "stock verifier must reject the fleet leaf on name mismatch (chain is fine)"
        );
    }
}
