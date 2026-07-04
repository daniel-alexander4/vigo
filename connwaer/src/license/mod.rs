//! License verification for Connwaer.
//!
//! Reads the Vigo license file (.lic), verifies the Ed25519 signature, and
//! checks that the "connwaer" feature is present in the Features field.
//!
//! The license format matches the Go server: JSON with payload + signature.
//! Signature is base64-encoded Ed25519 over canonical JSON (sorted keys, no whitespace).

use anyhow::{bail, Context, Result};
use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use serde::Deserialize;
use std::path::Path;

/// Hex-encoded Ed25519 public key — must match server/license/pubkey.go.
const PUBLIC_KEY_HEX: &str = "616d6b3f669392ffd165100387d799a429cec74c09f4da153a7543bce578d8d3";

#[derive(Debug, Deserialize)]
pub struct License {
    pub payload: Payload,
    pub signature: String,
}

#[derive(Debug, Deserialize, serde::Serialize)]
pub struct Payload {
    pub customer_name: String,
    pub customer_id: String,
    pub license_id: String,
    pub tier: String,
    pub max_envoys: i64,
    #[serde(default)]
    pub fingerprint: String,
    pub issued_at: String,
    pub expires_at: String,
    pub version: i64,
    #[serde(default)]
    pub features: Vec<String>,
}

impl License {
    /// Load and verify a license from a directory. Scans for .lic files,
    /// tries each in lexicographic order, returns the first valid one.
    pub fn load_dir(dir: &Path) -> Result<Self> {
        let mut entries: Vec<_> = std::fs::read_dir(dir)
            .with_context(|| format!("reading license dir: {}", dir.display()))?
            .filter_map(|e| e.ok())
            .filter(|e| {
                e.path().extension()
                    .and_then(|ext| ext.to_str())
                    .map(|ext| ext == "lic")
                    .unwrap_or(false)
            })
            .collect();

        entries.sort_by_key(|e| e.file_name());

        for entry in entries {
            let path = entry.path();
            match Self::load_file(&path) {
                Ok(lic) => {
                    if let Err(e) = lic.verify() {
                        tracing::warn!("license {} failed verification: {e}", path.display());
                        continue;
                    }
                    tracing::info!("loaded license: {} ({})", lic.payload.license_id, path.display());
                    return Ok(lic);
                }
                Err(e) => {
                    tracing::warn!("skipping {}: {e}", path.display());
                    continue;
                }
            }
        }

        bail!("no valid .lic file found in {}", dir.display())
    }

    fn load_file(path: &Path) -> Result<Self> {
        let data = std::fs::read_to_string(path)
            .with_context(|| format!("reading {}", path.display()))?;
        let lic: License = serde_json::from_str(&data)
            .with_context(|| format!("parsing {}", path.display()))?;
        Ok(lic)
    }

    /// Verify the Ed25519 signature against the embedded public key.
    pub fn verify(&self) -> Result<()> {
        let pub_bytes = hex::decode(PUBLIC_KEY_HEX)
            .context("decoding public key hex")?;

        let verifying_key = VerifyingKey::from_bytes(
            pub_bytes.as_slice().try_into()
                .map_err(|_| anyhow::anyhow!("invalid public key length"))?
        ).context("invalid public key")?;

        let canonical = canonical_json(&self.payload)?;

        let sig_bytes = base64_decode(&self.signature)
            .context("decoding signature base64")?;
        let signature = Signature::from_bytes(
            sig_bytes.as_slice().try_into()
                .map_err(|_| anyhow::anyhow!("invalid signature length: got {}", sig_bytes.len()))?
        );

        verifying_key.verify(&canonical, &signature)
            .map_err(|_| anyhow::anyhow!("license signature verification failed"))?;

        Ok(())
    }

    /// Check if the license has expired.
    pub fn is_expired(&self) -> bool {
        let today = chrono::Utc::now().format("%Y-%m-%d").to_string();
        self.payload.expires_at < today
    }

    /// Check if a feature is enabled.
    pub fn has_feature(&self, name: &str) -> bool {
        self.payload.features.iter().any(|f| f == name)
    }
}

/// Produce canonical JSON matching the Go implementation: sorted keys, no whitespace.
fn canonical_json(payload: &Payload) -> Result<Vec<u8>> {
    // Serialize to a generic Value, then re-serialize with sorted keys.
    let value = serde_json::to_value(payload)?;
    let map = value.as_object()
        .ok_or_else(|| anyhow::anyhow!("payload is not a JSON object"))?;

    let mut keys: Vec<&String> = map.keys().collect();
    keys.sort();

    let mut buf = String::from("{");
    for (i, key) in keys.iter().enumerate() {
        if i > 0 {
            buf.push(',');
        }
        let key_json = serde_json::to_string(key)?;
        let val_json = serde_json::to_string(&map[*key])?;
        buf.push_str(&key_json);
        buf.push(':');
        buf.push_str(&val_json);
    }
    buf.push('}');

    Ok(buf.into_bytes())
}

/// Simple base64 standard decoding (no padding issues).
fn base64_decode(input: &str) -> Result<Vec<u8>> {
    use base64::Engine;
    base64::engine::general_purpose::STANDARD
        .decode(input)
        .context("base64 decode")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn canonical_json_sorts_keys() {
        let payload = Payload {
            customer_name: "Test".to_string(),
            customer_id: "cust-1".to_string(),
            license_id: "lic-1".to_string(),
            tier: "free".to_string(),
            max_envoys: 25,
            fingerprint: String::new(),
            issued_at: "2026-01-01".to_string(),
            expires_at: "2099-12-31".to_string(),
            version: 1,
            features: vec![],
        };
        let json = canonical_json(&payload).unwrap();
        let s = String::from_utf8(json).unwrap();
        // Keys should be sorted alphabetically.
        assert!(s.starts_with("{\"customer_id\":"));
        // "customer_id" < "customer_name" < "expires_at" < ...
        let first_key_pos = s.find("\"customer_id\"").unwrap();
        let second_key_pos = s.find("\"customer_name\"").unwrap();
        assert!(first_key_pos < second_key_pos);
    }

    #[test]
    fn has_feature_works() {
        let payload = Payload {
            customer_name: "Test".to_string(),
            customer_id: "cust-1".to_string(),
            license_id: "lic-1".to_string(),
            tier: "growth".to_string(),
            max_envoys: 100,
            fingerprint: String::new(),
            issued_at: "2026-01-01".to_string(),
            expires_at: "2099-12-31".to_string(),
            version: 1,
            features: vec!["connwaer".to_string()],
        };
        let lic = License {
            payload,
            signature: String::new(),
        };
        assert!(lic.has_feature("connwaer"));
        assert!(!lic.has_feature("nonexistent"));
    }
}
