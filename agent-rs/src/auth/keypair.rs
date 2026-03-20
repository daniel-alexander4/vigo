// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! ED25519 keypair management compatible with the Go agent's PKCS8 PEM format.
//!
//! Go uses `x509.MarshalPKCS8PrivateKey` → PEM "PRIVATE KEY" and
//! `x509.MarshalPKIXPublicKey` → PEM "PUBLIC KEY". We match that exactly
//! using `ed25519-dalek` + `pkcs8` + `spki` crates.

use anyhow::{bail, Context, Result};
use ed25519_dalek::{SigningKey, VerifyingKey};
use pkcs8::{DecodePrivateKey, EncodePrivateKey, LineEnding};
use spki::EncodePublicKey;
use std::path::Path;

const KEY_FILENAME: &str = "agent.key";
const CERTS_SUBDIR: &str = "certs";
const TLS_KEY_FILENAME: &str = "tls.key";
const TLS_CERT_FILENAME: &str = "tls.crt";
const CA_CERT_FILENAME: &str = "ca.crt";

/// Generate a new ED25519 keypair.
pub fn generate_keypair() -> Result<(VerifyingKey, SigningKey)> {
    let mut csprng = rand::rngs::OsRng;
    let signing = SigningKey::generate(&mut csprng);
    let verifying = signing.verifying_key();
    Ok((verifying, signing))
}

/// Save an ED25519 private key in PKCS8 PEM format to {dir}/agent.key with 0600 permissions.
pub fn save_keypair(dir: &Path, signing: &SigningKey) -> Result<()> {
    let pem = signing
        .to_pkcs8_pem(LineEnding::LF)
        .context("marshal private key to PKCS8 PEM")?;

    let path = dir.join(KEY_FILENAME);
    std::fs::write(&path, pem.as_bytes()).context("write private key file")?;

    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))?;
    }

    Ok(())
}

/// Load an ED25519 keypair from a PKCS8 PEM file in the given directory.
pub fn load_keypair(dir: &Path) -> Result<(VerifyingKey, SigningKey)> {
    let path = dir.join(KEY_FILENAME);
    let data = std::fs::read_to_string(&path)
        .with_context(|| format!("read key file {}", path.display()))?;

    let signing = SigningKey::from_pkcs8_pem(&data)
        .context("parse PKCS8 private key")?;
    let verifying = signing.verifying_key();
    Ok((verifying, signing))
}

/// Export an ED25519 public key in PKIX/SPKI PEM format (Go-compatible).
pub fn public_key_pem(verifying: &VerifyingKey) -> Result<String> {
    let pem = verifying
        .to_public_key_pem(LineEnding::LF)
        .context("marshal public key to PKIX PEM")?;
    Ok(pem)
}

/// Load or generate: try to load existing keypair, generate if file doesn't exist.
pub fn load_or_generate(dir: &Path) -> Result<(VerifyingKey, SigningKey)> {
    match load_keypair(dir) {
        Ok(keys) => Ok(keys),
        Err(e) => {
            // Check if it's a file-not-found error
            if let Some(io_err) = e.downcast_ref::<std::io::Error>() {
                if io_err.kind() == std::io::ErrorKind::NotFound {
                    let (verifying, signing) = generate_keypair()?;
                    save_keypair(dir, &signing)?;
                    return Ok((verifying, signing));
                }
            }
            // Also check the anyhow chain for io::Error
            for cause in e.chain() {
                if let Some(io_err) = cause.downcast_ref::<std::io::Error>() {
                    if io_err.kind() == std::io::ErrorKind::NotFound {
                        let (verifying, signing) = generate_keypair()?;
                        save_keypair(dir, &signing)?;
                        return Ok((verifying, signing));
                    }
                }
            }
            bail!("load keypair: {}", e);
        }
    }
}

/// Check if TLS cert, key, and CA cert exist in the certs subdirectory.
pub fn has_bootstrap_certs(dir: &Path) -> bool {
    let cd = dir.join(CERTS_SUBDIR);
    [TLS_KEY_FILENAME, TLS_CERT_FILENAME, CA_CERT_FILENAME]
        .iter()
        .all(|f| cd.join(f).exists())
}

/// Path helpers matching the Go agent.
pub fn tls_key_path(dir: &Path) -> std::path::PathBuf {
    dir.join(CERTS_SUBDIR).join(TLS_KEY_FILENAME)
}
pub fn tls_cert_path(dir: &Path) -> std::path::PathBuf {
    dir.join(CERTS_SUBDIR).join(TLS_CERT_FILENAME)
}
pub fn ca_cert_path(dir: &Path) -> std::path::PathBuf {
    dir.join(CERTS_SUBDIR).join(CA_CERT_FILENAME)
}

/// Generate an ECDSA P-256 private key for TLS client certificates.
pub fn generate_tls_key() -> Result<p256::ecdsa::SigningKey> {
    let key = p256::ecdsa::SigningKey::random(&mut rand::rngs::OsRng);
    Ok(key)
}

/// Create a PEM-encoded X.509 CSR for the given hostname using an ECDSA key.
pub fn generate_csr(key: &p256::ecdsa::SigningKey, hostname: &str) -> Result<String> {
    use rcgen::{CertificateParams, DistinguishedName, KeyPair, PKCS_ECDSA_P256_SHA256};

    let mut params = CertificateParams::default();
    let mut dn = DistinguishedName::new();
    dn.push(rcgen::DnType::CommonName, hostname);
    params.distinguished_name = dn;
    params.subject_alt_names = vec![rcgen::SanType::DnsName(hostname.try_into()?)];

    // Convert p256 key to rcgen KeyPair via PKCS8 DER.
    use p256::pkcs8::EncodePrivateKey;
    let pkcs8_der = key
        .to_pkcs8_der()
        .context("encode ECDSA key to PKCS8 DER")?;
    let private_key_der = rustls_pki_types::PrivateKeyDer::Pkcs8(
        rustls_pki_types::PrivatePkcs8KeyDer::from(pkcs8_der.as_bytes().to_vec()),
    );
    let key_pair = KeyPair::from_der_and_sign_algo(&private_key_der, &PKCS_ECDSA_P256_SHA256)?;

    let csr = params.serialize_request(&key_pair)?;
    Ok(csr.pem()?)
}

/// Save an ECDSA private key in PKCS8 PEM format to {dir}/certs/tls.key with 0600 permissions.
pub fn save_tls_key(dir: &Path, key: &p256::ecdsa::SigningKey) -> Result<()> {
    use p256::pkcs8::EncodePrivateKey;
    let pem = key
        .to_pkcs8_pem(LineEnding::LF)
        .context("encode TLS key to PKCS8 PEM")?;

    let certs_dir = dir.join(CERTS_SUBDIR);
    std::fs::create_dir_all(&certs_dir).context("create certs directory")?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&certs_dir, std::fs::Permissions::from_mode(0o700))?;
    }

    let path = certs_dir.join(TLS_KEY_FILENAME);
    std::fs::write(&path, pem.as_bytes()).context("write TLS key file")?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600))?;
    }
    Ok(())
}

/// Save a PEM-encoded certificate to {dir}/certs/tls.crt with 0644 permissions.
pub fn save_tls_cert(dir: &Path, cert_pem: &str) -> Result<()> {
    let certs_dir = dir.join(CERTS_SUBDIR);
    std::fs::create_dir_all(&certs_dir).context("create certs directory")?;

    let path = certs_dir.join(TLS_CERT_FILENAME);
    std::fs::write(&path, cert_pem.as_bytes()).context("write TLS cert file")?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644))?;
    }
    Ok(())
}

/// Save a PEM-encoded CA certificate to {dir}/certs/ca.crt with 0644 permissions.
pub fn save_ca_cert(dir: &Path, ca_pem: &str) -> Result<()> {
    let certs_dir = dir.join(CERTS_SUBDIR);
    std::fs::create_dir_all(&certs_dir).context("create certs directory")?;

    let path = certs_dir.join(CA_CERT_FILENAME);
    std::fs::write(&path, ca_pem.as_bytes()).context("write CA cert file")?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644))?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn test_generate_keypair() {
        let (verifying, signing) = generate_keypair().unwrap();
        // Verify the keys produce valid signatures
        use ed25519_dalek::Signer;
        let msg = b"test message";
        let sig = signing.sign(msg);
        use ed25519_dalek::Verifier;
        assert!(verifying.verify(msg, &sig).is_ok());
    }

    #[test]
    fn test_save_and_load_keypair() {
        let dir = TempDir::new().unwrap();
        let (orig_verifying, orig_signing) = generate_keypair().unwrap();

        save_keypair(dir.path(), &orig_signing).unwrap();

        // Verify file permissions
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let meta = std::fs::metadata(dir.path().join(KEY_FILENAME)).unwrap();
            assert_eq!(meta.permissions().mode() & 0o777, 0o600);
        }

        let (loaded_verifying, loaded_signing) = load_keypair(dir.path()).unwrap();
        assert_eq!(orig_verifying.as_bytes(), loaded_verifying.as_bytes());
        assert_eq!(orig_signing.to_bytes(), loaded_signing.to_bytes());
    }

    #[test]
    fn test_load_keypair_not_found() {
        let dir = TempDir::new().unwrap();
        let err = load_keypair(dir.path()).unwrap_err();
        // Should be a file not found error somewhere in the chain
        let has_not_found = err.chain().any(|e| {
            e.downcast_ref::<std::io::Error>()
                .map(|io_e| io_e.kind() == std::io::ErrorKind::NotFound)
                .unwrap_or(false)
        });
        assert!(has_not_found, "expected NotFound error, got: {}", err);
    }

    #[test]
    fn test_public_key_pem() {
        use spki::DecodePublicKey;
        let (verifying, _) = generate_keypair().unwrap();
        let pem = public_key_pem(&verifying).unwrap();
        assert!(pem.starts_with("-----BEGIN PUBLIC KEY-----"));
        assert!(pem.contains("-----END PUBLIC KEY-----"));

        // Round-trip: parse the PEM back
        let parsed = VerifyingKey::from_public_key_pem(&pem).unwrap();
        assert_eq!(verifying.as_bytes(), parsed.as_bytes());
    }

    #[test]
    fn test_private_key_pem_format() {
        let (_, signing) = generate_keypair().unwrap();
        let dir = TempDir::new().unwrap();
        save_keypair(dir.path(), &signing).unwrap();

        let data = std::fs::read_to_string(dir.path().join(KEY_FILENAME)).unwrap();
        assert!(data.starts_with("-----BEGIN PRIVATE KEY-----"));
        assert!(data.contains("-----END PRIVATE KEY-----"));

        // Verify it's parseable
        let parsed = SigningKey::from_pkcs8_pem(&data).unwrap();
        assert_eq!(signing.to_bytes(), parsed.to_bytes());
    }

    #[test]
    fn test_load_or_generate_new() {
        let dir = TempDir::new().unwrap();
        let (verifying, signing) = load_or_generate(dir.path()).unwrap();

        // Key file should exist now
        assert!(dir.path().join(KEY_FILENAME).exists());

        // Should produce valid signatures
        use ed25519_dalek::{Signer, Verifier};
        let sig = signing.sign(b"test");
        assert!(verifying.verify(b"test", &sig).is_ok());
    }

    #[test]
    fn test_load_or_generate_existing() {
        let dir = TempDir::new().unwrap();
        let (_, orig_signing) = generate_keypair().unwrap();
        save_keypair(dir.path(), &orig_signing).unwrap();

        let (_, loaded_signing) = load_or_generate(dir.path()).unwrap();
        assert_eq!(orig_signing.to_bytes(), loaded_signing.to_bytes());
    }

    #[test]
    fn test_has_bootstrap_certs() {
        let dir = TempDir::new().unwrap();
        assert!(!has_bootstrap_certs(dir.path()));

        let certs_dir = dir.path().join(CERTS_SUBDIR);
        std::fs::create_dir_all(&certs_dir).unwrap();
        std::fs::write(certs_dir.join(TLS_KEY_FILENAME), "key").unwrap();
        std::fs::write(certs_dir.join(TLS_CERT_FILENAME), "cert").unwrap();
        assert!(!has_bootstrap_certs(dir.path()));

        std::fs::write(certs_dir.join(CA_CERT_FILENAME), "ca").unwrap();
        assert!(has_bootstrap_certs(dir.path()));
    }

    #[test]
    fn test_generate_tls_key() {
        let key = generate_tls_key().unwrap();
        // Verify the key can produce a valid signature.
        use p256::ecdsa::{signature::Signer, Signature};
        let _sig: Signature = key.sign(b"test data");
    }

    #[test]
    fn test_generate_csr() {
        let key = generate_tls_key().unwrap();
        let csr_pem = generate_csr(&key, "web01.example.com").unwrap();
        assert!(csr_pem.contains("-----BEGIN CERTIFICATE REQUEST-----"));
        assert!(csr_pem.contains("-----END CERTIFICATE REQUEST-----"));
    }

    #[test]
    fn test_save_tls_key() {
        let dir = TempDir::new().unwrap();
        let key = generate_tls_key().unwrap();
        save_tls_key(dir.path(), &key).unwrap();

        let key_path = dir.path().join(CERTS_SUBDIR).join(TLS_KEY_FILENAME);
        assert!(key_path.exists());

        let data = std::fs::read_to_string(&key_path).unwrap();
        assert!(data.starts_with("-----BEGIN PRIVATE KEY-----"));

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&key_path).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o600);
        }
    }

    #[test]
    fn test_save_tls_cert_and_ca_cert() {
        let dir = TempDir::new().unwrap();
        let fake_cert = "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----\n";
        let fake_ca = "-----BEGIN CERTIFICATE-----\nfake-ca\n-----END CERTIFICATE-----\n";

        save_tls_cert(dir.path(), fake_cert).unwrap();
        save_ca_cert(dir.path(), fake_ca).unwrap();

        let cert_data = std::fs::read_to_string(dir.path().join(CERTS_SUBDIR).join(TLS_CERT_FILENAME)).unwrap();
        assert_eq!(cert_data, fake_cert);

        let ca_data = std::fs::read_to_string(dir.path().join(CERTS_SUBDIR).join(CA_CERT_FILENAME)).unwrap();
        assert_eq!(ca_data, fake_ca);

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let cert_mode = std::fs::metadata(dir.path().join(CERTS_SUBDIR).join(TLS_CERT_FILENAME))
                .unwrap().permissions().mode() & 0o777;
            assert_eq!(cert_mode, 0o644);
            let ca_mode = std::fs::metadata(dir.path().join(CERTS_SUBDIR).join(CA_CERT_FILENAME))
                .unwrap().permissions().mode() & 0o777;
            assert_eq!(ca_mode, 0o644);
        }
    }

    #[test]
    fn test_save_tls_roundtrip() {
        let dir = TempDir::new().unwrap();
        let key = generate_tls_key().unwrap();
        let csr_pem = generate_csr(&key, "test-host").unwrap();

        save_tls_key(dir.path(), &key).unwrap();
        save_tls_cert(dir.path(), &csr_pem).unwrap(); // CSR as placeholder cert
        save_ca_cert(dir.path(), "ca-placeholder").unwrap();

        assert!(has_bootstrap_certs(dir.path()));
    }
}
