// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Executor-facing `artifact:` param resolution (ADR-024, slice 6).
//!
//! Module authors write things like:
//!
//! ```yaml
//! - type: nonrepo_package
//!   artifact: alexander4/vigo-agent:latest    # <puddle-name>/<name>:<tag>
//!   state: present
//!
//! - type: source_package
//!   artifact: e3810a256.../my-tarball@v3      # <hex_aid>/<name>@<version>
//!   target_path: /opt/my-tool
//!
//! - type: nonrepo_package
//!   artifact: vigo-agent:latest               # implicit namespace (the
//!                                             # unique fleet-name claim)
//!   state: present
//! ```
//!
//! This module is the bridge between that string and a concrete
//! materialized path on disk. It:
//!
//! 1. Parses the param into `(namespace, name, tag_or_version)` —
//!    accepting the three forms above. When no namespace is present,
//!    resolves the implicit namespace from the cached fleet name map
//!    ([`crate::swarm::puddle::names_cache::FleetNameMap`]) iff exactly
//!    one puddle has claimed a name; zero or 2+ claims is a hard error
//!    with guidance toward the explicit forms.
//! 2. Translates the namespace to a 32-byte puddle pubkey (when name)
//!    or parses it as 64-hex (when artifact_id). Computes
//!    `artifact_id = sha256(founder_pubkey ‖ name)`.
//! 3. Auto-detects the envoy's current `os` / `arch` against the catalog
//!    convention (GNU-style: `linux`/`darwin`/`windows` × `amd64`/`arm64`).
//! 4. Hands off to [`super::Resolver::pull`] which fetches + verifies
//!    + materializes under `/var/lib/vigo/artifacts/`.
//!
//! Used by the `nonrepo_package` and `source_package` executors as a
//! drop-in alternative to their `url:` param. The catalog entry's
//! signed `blob_sha` is the integrity guarantee — declaring `sha256:`
//! alongside `artifact:` is redundant and rejected at parse time.

use std::path::PathBuf;

use anyhow::{bail, Context, Result};
use sha2::{Digest, Sha256};

use super::catalog::ID_LEN;
use super::resolver::{Resolver, Resolution};
use crate::auth;
use crate::swarm::puddle::names_cache::FleetNameMap;
use crate::update::TlsCerts;

/// Build a [`Resolver`] from this envoy's agent config + bootstrap TLS
/// material. Used by every CLI / executor that needs to resolve curator
/// artifacts. Empty api_addr / missing certs are tolerated — the
/// resolver falls back to the cached catalog and local blob cache, so
/// already-held artifacts still resolve offline.
pub fn build_resolver_from_agent_config() -> Result<Resolver> {
    let data_dir = crate::platform::paths::default_data_dir();
    let cfg = crate::config::load(&data_dir);
    let api_addr = cfg
        .get("api_addr")
        .cloned()
        .unwrap_or_else(|| "https://localhost:8443".to_string());
    let tls = if auth::keypair::has_bootstrap_certs(&data_dir) {
        Some(TlsCerts {
            ca_cert: auth::keypair::ca_cert_path(&data_dir).to_string_lossy().to_string(),
            client_cert: auth::keypair::tls_cert_path(&data_dir).to_string_lossy().to_string(),
            client_key: auth::keypair::tls_key_path(&data_dir).to_string_lossy().to_string(),
        })
    } else {
        None
    };
    Ok(Resolver::new(api_addr, tls))
}

/// What [`resolve_artifact_param`] returns: the materialized path plus
/// the resolved metadata the caller needs for downstream idempotency
/// checks (post-install version comparison, etc.).
#[derive(Debug, Clone)]
pub struct ResolvedArtifact {
    /// Absolute path under `/var/lib/vigo/artifacts/<aid>/<name>/<ver>/<os>-<arch>`
    /// where the verified bytes now live. Caller treats this as the
    /// install source — same role `url:`'s download path plays today.
    pub path: PathBuf,
    /// The artifact's name as the catalog entry declares it (post-
    /// namespace-strip).
    pub name: String,
    /// The version string the tag-or-version arg resolved to. Useful
    /// for the executor's version-marker idempotency.
    pub version: String,
}

/// Parsed form of an `artifact:` param value.
#[derive(Debug, PartialEq, Eq)]
struct ParsedRef<'a> {
    /// `Some("alexander4")` for `alexander4/foo:latest`,
    /// `Some("e3810a2562bdde75…")` for `<hex64>/foo@v3`,
    /// `None` for bare `foo:latest`.
    namespace: Option<&'a str>,
    name: &'a str,
    /// The tag or version string (the part after `:` or `@`).
    tag_or_version: &'a str,
    /// `true` if the separator was `@` (literal version); `false` if `:`
    /// (tag-first, falls through to version).
    is_version: bool,
}

/// Parse `artifact:` strings:
///
/// - `<ns>/<name>:<tag>`      → namespace + tag form
/// - `<ns>/<name>@<version>`  → namespace + version form
/// - `<name>:<tag>`            → bare tag form (implicit namespace)
/// - `<name>@<version>`        → bare version form (implicit namespace)
///
/// `ns` is either a puddle friendly name or a 64-hex artifact-id
/// prefix — [`resolve_artifact_param`] does the actual lookup.
fn parse_ref(input: &str) -> Result<ParsedRef<'_>> {
    let s = input.trim();
    if s.is_empty() {
        bail!("`artifact:` value is empty");
    }
    // Split namespace at the first `/` (puddle names + 64-hex ids are
    // both `/`-free).
    let (namespace, rest) = match s.split_once('/') {
        Some((ns, rest)) => (Some(ns), rest),
        None => (None, s),
    };
    if let Some(ns) = namespace {
        if ns.is_empty() {
            bail!("`artifact:` namespace is empty (`{input}`) — drop the leading `/`");
        }
    }

    // The `name:tag` / `name@version` split. Prefer `@` so `foo@1.0:beta`
    // (an unusual tag containing `:`) is unambiguous; this matches
    // OCI / Docker tag conventions.
    let (name, tag_or_version, is_version) = if let Some((n, v)) = rest.split_once('@') {
        (n, v, true)
    } else if let Some((n, t)) = rest.split_once(':') {
        (n, t, false)
    } else {
        bail!(
            "`artifact:` must include a tag or version (`<name>:<tag>` or `<name>@<version>`), got `{input}`"
        );
    };
    if name.is_empty() {
        bail!("`artifact:` artifact name is empty in `{input}`");
    }
    if tag_or_version.is_empty() {
        bail!("`artifact:` tag or version is empty in `{input}`");
    }
    Ok(ParsedRef { namespace, name, tag_or_version, is_version: _is_version_kept_for_future(is_version) })
}

// The `is_version` flag is kept on the parsed struct in case a future
// version of `Resolver::resolve` distinguishes "always a tag" from
// "always a version"; today both paths go through the same call so the
// flag is informational only. Wrapping it through a helper keeps the
// dead-code linter quiet without an `#[allow]` attribute that'd cover
// real dead code too.
fn _is_version_kept_for_future(b: bool) -> bool { b }

/// The current envoy's `(os, arch)` in the catalog's GNU-style
/// convention (matches what `vigo swarm curator push --os <X> --arch <Y>`
/// expects). Translation table:
///
/// - Rust `std::env::consts::OS` `"linux"` → `"linux"`
/// - Rust `std::env::consts::OS` `"macos"` → `"darwin"`
/// - Rust `std::env::consts::OS` everything else → unchanged
/// - Rust `std::env::consts::ARCH` `"x86_64"` → `"amd64"`
/// - Rust `std::env::consts::ARCH` `"aarch64"` → `"arm64"`
/// - Rust `std::env::consts::ARCH` everything else → unchanged
///
/// The convention isn't enforced anywhere in the catalog itself — an
/// operator who publishes with `--arch x86_64` will not match an
/// auto-detected `amd64`. Documented as a publish-side convention; the
/// resolver gives a clear "platform linux/amd64 not in version 1.0 of
/// the entry" error if there's a mismatch (the catalog entry's
/// platforms list is in the message).
pub fn current_platform() -> (String, String) {
    let os = match std::env::consts::OS {
        "macos" => "darwin".to_string(),
        other => other.to_string(),
    };
    let arch = match std::env::consts::ARCH {
        "x86_64" => "amd64".to_string(),
        "aarch64" => "arm64".to_string(),
        other => other.to_string(),
    };
    (os, arch)
}

/// Resolve an `artifact:` param to a materialized local path via
/// curator's resolver. Convenience over [`Resolver::pull`] that handles
/// namespace lookup + platform auto-detect.
pub fn resolve_artifact_param(param: &str, resolver: &Resolver) -> Result<ResolvedArtifact> {
    let (os, arch) = current_platform();
    resolve_artifact_param_for(param, resolver, &os, &arch, &FleetNameMap::load().unwrap_or_default())
}

/// Test-seam form of [`resolve_artifact_param`]: takes the platform
/// and the fleet name map explicitly so unit tests don't depend on the
/// host's `std::env::consts` values or the on-disk `names.json` cache.
pub fn resolve_artifact_param_for(
    param: &str,
    resolver: &Resolver,
    os: &str,
    arch: &str,
    names: &FleetNameMap,
) -> Result<ResolvedArtifact> {
    let parsed = parse_ref(param)?;
    let founder_pubkey = resolve_namespace(parsed.namespace, names)?;
    let aid_bytes = artifact_id(&founder_pubkey, parsed.name);
    let aid_hex = hex::encode(aid_bytes);

    let path = resolver
        .pull(&aid_hex, parsed.tag_or_version, os, arch)
        .with_context(|| {
            format!(
                "resolving curator artifact `{param}` (artifact_id {aid_hex}, {os}/{arch})"
            )
        })?;

    // The resolved version is what the resolver landed on after
    // tag-to-version lookup. We re-call resolve() to recover it for the
    // ResolvedArtifact return — pull() doesn't expose the resolved
    // Resolution. Cheap: server-side has a 30s cache and pull() warmed
    // the local catalog cache, so this is sub-ms.
    let resolution: Resolution = resolver
        .resolve(&aid_hex, parsed.tag_or_version, os, arch)
        .with_context(|| format!("re-resolving curator artifact `{param}` for version info"))?;

    Ok(ResolvedArtifact {
        path,
        name: resolution.name,
        version: resolution.version,
    })
}

/// Compute `artifact_id = sha256(founder_pubkey ‖ name)`. Mirror of
/// `catalog::artifact_id_for` (kept local to avoid pulling that
/// module's deps into the executor seam).
fn artifact_id(founder_pubkey: &[u8; ID_LEN], name: &str) -> [u8; ID_LEN] {
    let mut h = Sha256::new();
    h.update(founder_pubkey);
    h.update(name.as_bytes());
    let d = h.finalize();
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&d);
    out
}

/// Turn a namespace token into the founder puddle pubkey it represents:
///
/// - `None` (bare `<name>:<tag>`) — consult the fleet name map. If
///   exactly one puddle has claimed a name, that's the implicit
///   namespace. Zero or 2+ → error pointing the operator at the explicit
///   forms.
/// - 64-hex string — parse as a literal puddle pubkey hex.
/// - Anything else — treat as a puddle friendly name; look it up in the
///   name map.
fn resolve_namespace(ns: Option<&str>, names: &FleetNameMap) -> Result<[u8; ID_LEN]> {
    match ns {
        None => resolve_implicit_namespace(names),
        Some(token) if is_hex_pubkey(token) => parse_hex_pubkey(token),
        Some(token) => resolve_friendly_name(token, names),
    }
}

fn resolve_implicit_namespace(names: &FleetNameMap) -> Result<[u8; ID_LEN]> {
    let mut candidates: Vec<(String, [u8; ID_LEN])> = Vec::new();
    for name in names.names.keys() {
        let verified = names.verified_claims(name);
        if verified.is_empty() {
            continue;
        }
        // If multiple claims under one name disagree on pubkey (collision
        // per ADR-022), that name resolves to nothing — skip it. This
        // matches `git-remote-gitback`'s "ambiguous on collision" stance.
        let first_pk = verified[0].1;
        let unanimous = verified.iter().all(|(_, pk)| *pk == first_pk);
        if unanimous {
            candidates.push((name.clone(), first_pk));
        }
    }
    match candidates.as_slice() {
        [] => bail!(
            "bare `<name>:<tag>` form needs a fleet name claim to resolve to. \
             No puddle has claimed a name in this fleet — use the explicit \
             `<puddle-name>/<name>:<tag>` or `<artifact_id>/<name>:<tag>` form, \
             or run `vigo swarm puddle name set <name>` on the publishing envoy."
        ),
        [(_, pk)] => Ok(*pk),
        many => {
            let names_csv: Vec<&str> = many.iter().map(|(n, _)| n.as_str()).collect();
            bail!(
                "bare `<name>:<tag>` form is ambiguous — {} puddle names claimed in this fleet ({}). \
                 Use the explicit `<puddle-name>/<name>:<tag>` form to pick one.",
                many.len(),
                names_csv.join(", ")
            )
        }
    }
}

fn resolve_friendly_name(name: &str, names: &FleetNameMap) -> Result<[u8; ID_LEN]> {
    let verified = names.verified_claims(name);
    if verified.is_empty() {
        bail!(
            "no puddle has claimed the name `{name}` in this fleet. \
             Check `vigocli swarm puddle names list`, or use the explicit \
             `<artifact_id>/<name>:<tag>` form."
        );
    }
    let first_pk = verified[0].1;
    if !verified.iter().all(|(_, pk)| *pk == first_pk) {
        bail!(
            "puddle name `{name}` is contested by {} different claimants — \
             refusing to resolve. Use `<artifact_id>/<name>:<tag>` to disambiguate.",
            verified.len()
        );
    }
    Ok(first_pk)
}

fn is_hex_pubkey(s: &str) -> bool {
    s.len() == ID_LEN * 2 && s.bytes().all(|b| b.is_ascii_hexdigit())
}

fn parse_hex_pubkey(s: &str) -> Result<[u8; ID_LEN]> {
    let bytes = hex::decode(s).context("namespace hex decode")?;
    if bytes.len() != ID_LEN {
        bail!("namespace hex must be {} bytes (got {})", ID_LEN, bytes.len());
    }
    let mut out = [0u8; ID_LEN];
    out.copy_from_slice(&bytes);
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::swarm::puddle::name::{NameClaim, NameClaimBody};
    use ed25519_dalek::SigningKey;
    use rand::rngs::OsRng;

    fn pubkey_hex(k: &SigningKey) -> String {
        hex::encode(k.verifying_key().to_bytes())
    }

    fn pubkey_bytes(k: &SigningKey) -> [u8; 32] {
        k.verifying_key().to_bytes()
    }

    /// Build a name map with N puddles, each claiming its own name.
    /// `entries` is `[(puddle_name, signing_key)]`.
    fn name_map(entries: &[(&str, &SigningKey)]) -> FleetNameMap {
        let mut m = FleetNameMap::default();
        for (name, k) in entries {
            let body = NameClaimBody {
                puddle_pubkey: pubkey_hex(k),
                name: name.to_string(),
                claimed_at: 1,
                supersedes: vec![],
            };
            let claim = NameClaim::issue(k, body).expect("issue claim");
            m.names.insert(name.to_string(), vec![claim]);
        }
        m
    }

    #[test]
    fn parse_ref_accepts_all_four_shapes() {
        assert_eq!(
            parse_ref("foo:latest").unwrap(),
            ParsedRef { namespace: None, name: "foo", tag_or_version: "latest", is_version: false }
        );
        assert_eq!(
            parse_ref("foo@1.2.3").unwrap(),
            ParsedRef { namespace: None, name: "foo", tag_or_version: "1.2.3", is_version: true }
        );
        assert_eq!(
            parse_ref("alexander4/foo:latest").unwrap(),
            ParsedRef { namespace: Some("alexander4"), name: "foo", tag_or_version: "latest", is_version: false }
        );
        let hex = "a".repeat(64);
        let input = format!("{hex}/foo@1.0");
        let parsed = parse_ref(&input).unwrap();
        assert_eq!(parsed.namespace, Some(hex.as_str()));
        assert!(parsed.is_version);
    }

    #[test]
    fn parse_ref_rejects_malformed() {
        assert!(parse_ref("").is_err());
        assert!(parse_ref("   ").is_err());
        assert!(parse_ref("foo").is_err(), "missing tag-or-version separator");
        assert!(parse_ref(":bar").is_err(), "empty name");
        assert!(parse_ref("foo:").is_err(), "empty tag");
        assert!(parse_ref("/foo:bar").is_err(), "empty namespace");
        assert!(parse_ref("foo@").is_err(), "empty version");
    }

    #[test]
    fn implicit_namespace_uses_unique_claim() {
        let k = SigningKey::generate(&mut OsRng);
        let m = name_map(&[("alexander4", &k)]);
        let got = resolve_namespace(None, &m).unwrap();
        assert_eq!(got, pubkey_bytes(&k));
    }

    #[test]
    fn implicit_namespace_errors_when_zero_claimed() {
        let m = FleetNameMap::default();
        let err = resolve_namespace(None, &m).unwrap_err();
        let msg = format!("{err:#}");
        assert!(msg.contains("No puddle has claimed a name"), "got: {msg}");
        assert!(msg.contains("<puddle-name>"), "guidance points at explicit form: {msg}");
    }

    #[test]
    fn implicit_namespace_errors_when_multiple_claimed() {
        let k1 = SigningKey::generate(&mut OsRng);
        let k2 = SigningKey::generate(&mut OsRng);
        let m = name_map(&[("alexander4", &k1), ("operator-b", &k2)]);
        let err = resolve_namespace(None, &m).unwrap_err();
        let msg = format!("{err:#}");
        assert!(msg.contains("ambiguous"), "got: {msg}");
        assert!(msg.contains("alexander4") && msg.contains("operator-b"),
            "guidance lists both options: {msg}");
    }

    #[test]
    fn friendly_name_resolves() {
        let k = SigningKey::generate(&mut OsRng);
        let m = name_map(&[("alexander4", &k)]);
        let got = resolve_namespace(Some("alexander4"), &m).unwrap();
        assert_eq!(got, pubkey_bytes(&k));
    }

    #[test]
    fn friendly_name_unknown_errors() {
        let m = FleetNameMap::default();
        let err = resolve_namespace(Some("nobody"), &m).unwrap_err();
        assert!(format!("{err:#}").contains("no puddle has claimed the name `nobody`"));
    }

    #[test]
    fn hex_namespace_round_trips() {
        let k = SigningKey::generate(&mut OsRng);
        let hex = pubkey_hex(&k);
        let m = FleetNameMap::default();
        let got = resolve_namespace(Some(&hex), &m).unwrap();
        assert_eq!(got, pubkey_bytes(&k));
    }

    #[test]
    fn hex_namespace_wrong_length_errors() {
        let m = FleetNameMap::default();
        // 63 hex chars — falls into the friendly-name path (not hex-shaped),
        // and the empty name map yields the friendly-name-unknown error.
        assert!(resolve_namespace(Some(&"a".repeat(63)), &m).is_err());
        // 64 chars but non-hex — also friendly-name path.
        assert!(resolve_namespace(Some(&format!("g{}", "a".repeat(63))), &m).is_err());
    }

    #[test]
    fn current_platform_translates_rust_constants() {
        let (os, arch) = current_platform();
        // We can't assert exact values without knowing the test host, but
        // the *form* must never bleed through Rust's constants.
        assert_ne!(os, "macos", "must translate macos → darwin");
        assert_ne!(arch, "x86_64", "must translate x86_64 → amd64");
        assert_ne!(arch, "aarch64", "must translate aarch64 → arm64");
        // And whatever was returned must be non-empty.
        assert!(!os.is_empty());
        assert!(!arch.is_empty());
    }

    #[test]
    fn artifact_id_matches_catalog_helper() {
        // Sanity: this helper must produce the same hash the catalog
        // module's artifact_id_for produces.
        let k = SigningKey::generate(&mut OsRng);
        let pk = pubkey_bytes(&k);
        let ours = artifact_id(&pk, "vigo-agent");
        let theirs = super::super::catalog::artifact_id_for(&pk, "vigo-agent");
        assert_eq!(ours, theirs);
    }
}
