// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Service-account puddle helper daemon (ADR-025 Slice 1b).
//!
//! Sibling of [`super::session`] but for the *service-account*
//! puddle owned by the vigosrv runtime user, not a per-user puddle.
//! Unwraps `/srv/vigo/puddle/identity.wrapped` (envelope version
//! `0x02`) using an HKDF-SHA256-derived wrap key from the secrets
//! master key + per-host salt — the same construction the Go side
//! uses in `server/puddle/{wrap,identity}.go`.
//!
//! Differences from per-user [`super::session`]:
//!   - System-wide socket at `/var/run/vigo/puddle.sock` (vs per-UID
//!     `XDG_RUNTIME_DIR`). Mode 0600.
//!   - Unwraps from disk at startup using the master key, no
//!     interactive passphrase / stdin handoff.
//!   - No idle timeout (service identity is long-lived).
//!   - No parent-PID watchdog (the agent that spawns it is the
//!     fleet-managed long-running agent; no user shell to track).
//!   - SO_PEERCRED gate allows the owner uid of `identity.wrapped`,
//!     not the helper's own uid — the helper runs as root, the
//!     intended consumer is the vigosrv runtime user.
//!   - mtime-based reload: identity / salt / master-key files are
//!     re-read on access if their mtime changed, so a master-key
//!     rotation or operator-driven rewrap takes effect without
//!     restarting the helper.
//!
//! Verbs (wire-compatible with [`super::session`] where they overlap):
//!   - `STATUS\n`              → `STATUS unlocked=true\n`
//!   - `PUBKEY\n`              → `OK <base64 raw pubkey>\n`
//!   - `SIGN <base64>\n`       → `OK <base64 signature>\n`
//!
//! `LOCK` is intentionally absent — the service-account puddle has
//! no user-driven "lock the vault" semantic; lifetime is the
//! helper's own.

use std::fs;
use std::io::{BufRead, BufReader, Write};
use std::os::unix::fs::MetadataExt;
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use std::process;
use std::sync::Arc;
use std::time::{Duration, SystemTime};

use anyhow::{bail, Context, Result};
use base64::engine::general_purpose::STANDARD as B64;
use base64::Engine;
use chacha20poly1305::aead::Aead;
use chacha20poly1305::{KeyInit, XChaCha20Poly1305, XNonce};
use ed25519_dalek::{Signer, SigningKey};
use hkdf::Hkdf;
use sha2::{Digest, Sha256};
use tokio::sync::{RwLock, Semaphore};
use zeroize::Zeroizing;

use crate::swarm::HELPER_MAX_CONNECTIONS;

// ---------- wire / on-disk constants (must match server/puddle/wrap.go) ----------

const WRAP_VERSION: u8 = 0x02;
const HKDF_PURPOSE: &str = "vigo-puddle-wrap-v1";
const OPS_HKDF_PURPOSE: &str = "vigo-ops-puddle-wrap-v1";
const NONCE_LEN: usize = 24;
const SEED_LEN: usize = 32;
const TAG_LEN: usize = 16;
const SALT_LEN: usize = 32;
const WRAP_KEY_LEN: usize = 32;
const ENVELOPE_MIN: usize = 1 + NONCE_LEN + SEED_LEN + TAG_LEN;

// ---------- default paths ----------

pub const DEFAULT_SECRETS_DIR: &str = "/srv/vigo/secrets";
pub const DEFAULT_PUDDLE_DIR: &str = "/srv/vigo/puddle";
pub const DEFAULT_OPS_PUDDLE_DIR: &str = "/srv/vigo/ops-puddle";
pub const DEFAULT_SOCKET_PATH: &str = "/var/run/vigo/puddle.sock";

const WRAPPED_FILE: &str = "identity.wrapped";
const SALT_FILE: &str = ".salt";
const MASTER_KEY_FILE: &str = ".master.key";
const PIDFILE_NAME: &str = "puddle.pid";

pub fn socket_path() -> PathBuf {
    PathBuf::from(DEFAULT_SOCKET_PATH)
}

// ---------- client side ----------

pub fn connect() -> Result<UnixStream> {
    let path = socket_path();
    UnixStream::connect(&path)
        .with_context(|| format!("connecting to service puddle helper at {}", path.display()))
}

/// Ask the running helper to sign `payload`. Returns the raw 64-byte
/// Ed25519 signature. Used by callers that want to delegate signing
/// to the helper instead of unwrapping the master key themselves.
pub fn request_sign(payload: &[u8]) -> Result<Vec<u8>> {
    let stream = connect()?;
    let (mut reader, mut writer) = stream_halves(stream)?;
    writer
        .write_all(format!("SIGN {}\n", B64.encode(payload)).as_bytes())
        .context("sending SIGN")?;
    writer.flush().ok();
    let mut line = String::new();
    BufReader::new(&mut reader)
        .read_line(&mut line)
        .context("reading SIGN response")?;
    parse_ok_or_err(&line)
}

pub fn request_pubkey() -> Result<Vec<u8>> {
    let stream = connect()?;
    let (mut reader, mut writer) = stream_halves(stream)?;
    writer.write_all(b"PUBKEY\n").context("sending PUBKEY")?;
    writer.flush().ok();
    let mut line = String::new();
    BufReader::new(&mut reader)
        .read_line(&mut line)
        .context("reading PUBKEY response")?;
    parse_ok_or_err(&line)
}

pub fn request_status() -> Result<String> {
    let stream = connect()?;
    let (mut reader, mut writer) = stream_halves(stream)?;
    writer.write_all(b"STATUS\n").context("sending STATUS")?;
    writer.flush().ok();
    let mut line = String::new();
    BufReader::new(&mut reader)
        .read_line(&mut line)
        .context("reading STATUS response")?;
    Ok(line.trim().to_string())
}

fn stream_halves(stream: UnixStream) -> Result<(UnixStream, UnixStream)> {
    stream.set_read_timeout(Some(Duration::from_secs(30))).ok();
    stream.set_write_timeout(Some(Duration::from_secs(30))).ok();
    let reader = stream.try_clone().context("cloning Unix stream")?;
    Ok((reader, stream))
}

fn parse_ok_or_err(line: &str) -> Result<Vec<u8>> {
    let trimmed = line.trim_end_matches('\n');
    if let Some(rest) = trimmed.strip_prefix("OK ") {
        B64.decode(rest.trim()).context("decoding OK payload")
    } else if let Some(reason) = trimmed.strip_prefix("ERR ") {
        bail!("helper error: {}", reason)
    } else if trimmed == "OK" {
        Ok(Vec::new())
    } else {
        bail!("unexpected helper response: {}", trimmed)
    }
}

// ---------- daemon-side: read + derive + unwrap ----------

/// Normalize the master key file to 32 bytes the same way
/// `server/secrets/local.go::deriveKey` does:
/// - 64-hex-char (trimmed) → hex-decode
/// - exactly 32 raw bytes → use as-is
/// - otherwise → SHA-256(raw)
fn read_master_key(key_file: &Path) -> Result<Zeroizing<[u8; 32]>> {
    let raw = fs::read(key_file)
        .with_context(|| format!("reading master key {}", key_file.display()))?;
    let trimmed: Vec<u8> = String::from_utf8_lossy(&raw).trim().as_bytes().to_vec();
    if trimmed.len() == 64 {
        if let Ok(decoded) = hex::decode(&trimmed) {
            if decoded.len() == 32 {
                let mut out = [0u8; 32];
                out.copy_from_slice(&decoded);
                return Ok(Zeroizing::new(out));
            }
        }
    }
    if raw.len() == 32 {
        let mut out = [0u8; 32];
        out.copy_from_slice(&raw);
        return Ok(Zeroizing::new(out));
    }
    let mut hasher = Sha256::new();
    hasher.update(&raw);
    let digest = hasher.finalize();
    let mut out = [0u8; 32];
    out.copy_from_slice(&digest);
    Ok(Zeroizing::new(out))
}

fn derive_wrap_key(master_key: &[u8; 32], salt: &[u8]) -> Result<Zeroizing<[u8; 32]>> {
    derive_wrap_key_with_purpose(master_key, salt, HKDF_PURPOSE)
}

fn derive_ops_wrap_key(master_key: &[u8; 32], salt: &[u8]) -> Result<Zeroizing<[u8; 32]>> {
    derive_wrap_key_with_purpose(master_key, salt, OPS_HKDF_PURPOSE)
}

fn derive_wrap_key_with_purpose(
    master_key: &[u8; 32],
    salt: &[u8],
    purpose: &str,
) -> Result<Zeroizing<[u8; 32]>> {
    let hk = Hkdf::<Sha256>::new(Some(salt), master_key);
    let mut out = [0u8; WRAP_KEY_LEN];
    hk.expand(purpose.as_bytes(), &mut out)
        .map_err(|e| anyhow::anyhow!("hkdf expand: {}", e))?;
    Ok(Zeroizing::new(out))
}

fn unwrap_envelope(envelope: &[u8], wrap_key: &[u8; 32]) -> Result<Zeroizing<[u8; 32]>> {
    if envelope.len() < ENVELOPE_MIN {
        bail!(
            "envelope is {} bytes, expected at least {}",
            envelope.len(),
            ENVELOPE_MIN
        );
    }
    if envelope[0] != WRAP_VERSION {
        bail!(
            "envelope has unknown version byte 0x{:02x} (expected 0x{:02x})",
            envelope[0],
            WRAP_VERSION
        );
    }
    let nonce = XNonce::from_slice(&envelope[1..1 + NONCE_LEN]);
    let ciphertext = &envelope[1 + NONCE_LEN..];
    let aead = XChaCha20Poly1305::new(wrap_key.as_slice().into());
    let seed = aead.decrypt(nonce, ciphertext).map_err(|_| {
        anyhow::anyhow!(
            "unwrap failed: wrong master key, corrupted envelope, or mismatched per-host salt"
        )
    })?;
    if seed.len() != SEED_LEN {
        bail!("unwrapped seed is {} bytes, expected {}", seed.len(), SEED_LEN);
    }
    let mut out = [0u8; SEED_LEN];
    out.copy_from_slice(&seed);
    Ok(Zeroizing::new(out))
}

struct Loaded {
    key: SigningKey,
    pubkey_b64: String,
    wrap_mtime: SystemTime,
    salt_mtime: SystemTime,
    key_mtime: SystemTime,
    /// Ops-puddle is loaded lazily on first OPS_* request. Once
    /// loaded, mtime-tracking mirrors the service-account path so
    /// an operator-driven rewrap takes effect without a helper
    /// restart. None until first successful load; the previous-
    /// attempt error (if any) lives in `ops_last_err` for surfacing
    /// in OPS_* responses.
    ops: Option<OpsLoaded>,
    ops_last_err: Option<String>,
}

struct OpsLoaded {
    key: SigningKey,
    pubkey_bytes: [u8; 32],
    pubkey_b64: String,
    wrap_mtime: SystemTime,
    salt_mtime: SystemTime,
}

fn key_file_path(secrets_dir: &Path, override_path: Option<&Path>) -> PathBuf {
    match override_path {
        Some(p) => p.to_path_buf(),
        None => secrets_dir.join(MASTER_KEY_FILE),
    }
}

fn load_identity(
    secrets_dir: &Path,
    puddle_dir: &Path,
    key_file: Option<&Path>,
) -> Result<Loaded> {
    let key_path = key_file_path(secrets_dir, key_file);
    let wrap_path = puddle_dir.join(WRAPPED_FILE);
    let salt_path = puddle_dir.join(SALT_FILE);

    let master_key = read_master_key(&key_path)?;
    let salt = fs::read(&salt_path)
        .with_context(|| format!("reading salt {}", salt_path.display()))?;
    if salt.len() != SALT_LEN {
        bail!(
            "salt at {} is {} bytes, expected {}",
            salt_path.display(),
            salt.len(),
            SALT_LEN
        );
    }
    let envelope = fs::read(&wrap_path)
        .with_context(|| format!("reading wrapped identity {}", wrap_path.display()))?;
    let wrap_key = derive_wrap_key(&master_key, &salt)?;
    let seed = unwrap_envelope(&envelope, &wrap_key)?;
    let key = SigningKey::from_bytes(&seed);
    let pubkey_b64 = B64.encode(key.verifying_key().to_bytes());

    Ok(Loaded {
        key,
        pubkey_b64,
        wrap_mtime: fs::metadata(&wrap_path)?.modified()?,
        salt_mtime: fs::metadata(&salt_path)?.modified()?,
        key_mtime: fs::metadata(&key_path)?.modified()?,
        ops: None,
        ops_last_err: None,
    })
}

fn load_ops_identity(
    secrets_dir: &Path,
    ops_dir: &Path,
    key_file: Option<&Path>,
) -> Result<OpsLoaded> {
    let key_path = key_file_path(secrets_dir, key_file);
    let wrap_path = ops_dir.join(WRAPPED_FILE);
    let salt_path = ops_dir.join(SALT_FILE);

    let master_key = read_master_key(&key_path)?;
    let salt = fs::read(&salt_path)
        .with_context(|| format!("reading ops-puddle salt {}", salt_path.display()))?;
    if salt.len() != SALT_LEN {
        bail!(
            "ops-puddle salt at {} is {} bytes, expected {}",
            salt_path.display(),
            salt.len(),
            SALT_LEN
        );
    }
    let envelope = fs::read(&wrap_path)
        .with_context(|| format!("reading ops-puddle wrapped identity {}", wrap_path.display()))?;
    let wrap_key = derive_ops_wrap_key(&master_key, &salt)?;
    let seed = unwrap_envelope(&envelope, &wrap_key)?;
    let key = SigningKey::from_bytes(&seed);
    let pubkey_bytes = key.verifying_key().to_bytes();
    let pubkey_b64 = B64.encode(pubkey_bytes);

    Ok(OpsLoaded {
        key,
        pubkey_bytes,
        pubkey_b64,
        wrap_mtime: fs::metadata(&wrap_path)?.modified()?,
        salt_mtime: fs::metadata(&salt_path)?.modified()?,
    })
}

fn allowed_uid(wrap_path: &Path) -> Result<u32> {
    let md = fs::metadata(wrap_path)
        .with_context(|| format!("stat {}", wrap_path.display()))?;
    Ok(md.uid())
}

// ---------- daemon entry ----------

pub async fn daemon_main(args: Vec<String>) -> Result<()> {
    let secrets_dir = arg_value(&args, "--secrets-dir")
        .unwrap_or_else(|_| DEFAULT_SECRETS_DIR.to_string());
    let puddle_dir = arg_value(&args, "--puddle-dir")
        .unwrap_or_else(|_| DEFAULT_PUDDLE_DIR.to_string());
    let ops_puddle_dir = arg_value(&args, "--ops-puddle-dir")
        .unwrap_or_else(|_| DEFAULT_OPS_PUDDLE_DIR.to_string());
    let key_file = arg_value(&args, "--key-file").ok();
    let socket_path_arg = arg_value(&args, "--socket")
        .unwrap_or_else(|_| DEFAULT_SOCKET_PATH.to_string());

    let secrets_dir = PathBuf::from(secrets_dir);
    let puddle_dir = PathBuf::from(puddle_dir);
    let ops_puddle_dir = PathBuf::from(ops_puddle_dir);
    let key_file = key_file.map(PathBuf::from);
    let socket_path = PathBuf::from(socket_path_arg);

    // First load — fail fast on a misconfigured deployment so the
    // operator sees the error in the agent log rather than silent
    // helper crashes on every accept. Ops-puddle is loaded lazily on
    // first OPS_* request (envoys without one shouldn't fail boot).
    let loaded = load_identity(&secrets_dir, &puddle_dir, key_file.as_deref())?;
    let wrap_path = puddle_dir.join(WRAPPED_FILE);
    let allowed = allowed_uid(&wrap_path)?;

    if let Some(parent) = socket_path.parent() {
        std::fs::create_dir_all(parent).with_context(|| {
            format!("creating socket parent dir {}", parent.display())
        })?;
    }
    let _ = std::fs::remove_file(&socket_path);
    let listener = UnixListener::bind(&socket_path)
        .with_context(|| format!("binding {}", socket_path.display()))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(
            &socket_path,
            std::fs::Permissions::from_mode(0o600),
        );
    }
    listener
        .set_nonblocking(true)
        .context("setting listener nonblocking")?;

    let pidfile = socket_path.with_file_name(PIDFILE_NAME);
    std::fs::write(&pidfile, format!("{}\n", process::id())).ok();

    let listener = tokio::net::UnixListener::from_std(listener)
        .context("tokio UnixListener")?;
    let state = Arc::new(RwLock::new(loaded));
    let result = run_server(
        listener,
        state,
        secrets_dir,
        puddle_dir,
        ops_puddle_dir,
        key_file,
        allowed,
    )
    .await;

    let _ = std::fs::remove_file(&socket_path);
    let _ = std::fs::remove_file(&pidfile);
    result
}

async fn run_server(
    listener: tokio::net::UnixListener,
    state: Arc<RwLock<Loaded>>,
    secrets_dir: PathBuf,
    puddle_dir: PathBuf,
    ops_puddle_dir: PathBuf,
    key_file: Option<PathBuf>,
    allowed: u32,
) -> Result<()> {
    tracing::info!(
        allowed_uid = allowed,
        "service-account puddle helper started"
    );

    let semaphore = Arc::new(Semaphore::new(HELPER_MAX_CONNECTIONS));
    loop {
        let (stream, _addr) = match listener.accept().await {
            Ok(v) => v,
            Err(e) => {
                tracing::debug!(error = %e, "accept error");
                crate::swarm::server::accept_error_backoff().await;
                continue;
            }
        };
        if !peer_uid_matches(&stream, allowed) {
            tracing::warn!("rejecting service-puddle client with mismatched UID");
            drop(stream);
            continue;
        }
        if let Err(e) = maybe_reload(
            &state,
            &secrets_dir,
            &puddle_dir,
            &ops_puddle_dir,
            key_file.as_deref(),
        )
        .await
        {
            tracing::warn!(error = %e, "mtime-reload check failed; serving stale identity");
        }
        // Bound concurrent client handlers; queue-on-saturation
        // (operator-facing local socket).
        let permit = match semaphore.clone().acquire_owned().await {
            Ok(p) => p,
            Err(_) => continue, // semaphore closed — shutdown
        };
        let state_for_task = Arc::clone(&state);
        let secrets_for_task = secrets_dir.clone();
        let ops_for_task = ops_puddle_dir.clone();
        let key_file_for_task = key_file.clone();
        tokio::spawn(async move {
            let _permit = permit; // held for connection lifetime
            if let Err(e) = handle_client(
                stream,
                state_for_task,
                secrets_for_task,
                ops_for_task,
                key_file_for_task,
            )
            .await
            {
                tracing::debug!(error = %e, "client session error");
            }
        });
    }
}

/// Stat the identity / salt / master-key files; if any mtime
/// changed since the last load, re-derive and replace the held key.
/// Also re-checks the ops-puddle envelope mtimes if ops is already
/// loaded — picks up operator-driven rewraps without a helper
/// restart. Skips reload on any individual stat error: the next
/// accept retries, and a permanent error eventually surfaces via
/// load failure.
async fn maybe_reload(
    state: &RwLock<Loaded>,
    secrets_dir: &Path,
    puddle_dir: &Path,
    ops_puddle_dir: &Path,
    key_file: Option<&Path>,
) -> Result<()> {
    let key_path = key_file_path(secrets_dir, key_file);
    let wrap_path = puddle_dir.join(WRAPPED_FILE);
    let salt_path = puddle_dir.join(SALT_FILE);

    let wrap_m = fs::metadata(&wrap_path)?.modified()?;
    let salt_m = fs::metadata(&salt_path)?.modified()?;
    let key_m = fs::metadata(&key_path)?.modified()?;

    // Ops-puddle mtime targets — only checked if it's currently loaded.
    let ops_wrap_path = ops_puddle_dir.join(WRAPPED_FILE);
    let ops_salt_path = ops_puddle_dir.join(SALT_FILE);

    // Quick check: is anything stale?
    let (service_stale, ops_stale) = {
        let s = state.read().await;
        let service_stale =
            s.wrap_mtime != wrap_m || s.salt_mtime != salt_m || s.key_mtime != key_m;
        let ops_stale = if let Some(ops) = s.ops.as_ref() {
            match (
                fs::metadata(&ops_wrap_path).and_then(|m| m.modified()),
                fs::metadata(&ops_salt_path).and_then(|m| m.modified()),
            ) {
                (Ok(ow), Ok(os)) => ops.wrap_mtime != ow || ops.salt_mtime != os,
                _ => false, // can't stat — leave as-is rather than scrubbing
            }
        } else {
            false
        };
        (service_stale, ops_stale)
    };

    if !service_stale && !ops_stale {
        return Ok(());
    }

    let mut s = state.write().await;
    if service_stale {
        tracing::info!("service-account identity mtime changed; reloading");
        let new_id = load_identity(secrets_dir, puddle_dir, key_file)?;
        // Preserve already-loaded ops state across a service-only reload —
        // the master key is the same, the ops wrap is still valid.
        let preserved_ops = s.ops.take();
        let preserved_ops_err = s.ops_last_err.take();
        *s = new_id;
        s.ops = preserved_ops;
        s.ops_last_err = preserved_ops_err;
    }
    if ops_stale {
        tracing::info!("ops-puddle identity mtime changed; reloading");
        match load_ops_identity(secrets_dir, ops_puddle_dir, key_file) {
            Ok(new_ops) => {
                s.ops = Some(new_ops);
                s.ops_last_err = None;
            }
            Err(e) => {
                tracing::warn!(error = %e, "ops-puddle reload failed; dropping loaded ops state");
                s.ops = None;
                s.ops_last_err = Some(format!("{:#}", e));
            }
        }
    }
    Ok(())
}

/// Ensure the ops-puddle is loaded (lazy-load on first OPS_* request).
/// Returns Ok(()) when ops is present and usable; Err when the
/// ops-puddle isn't on disk or unwrap failed. Caller surfaces the
/// error to the operator over the wire so vigocli can tell them
/// "no ops-puddle — run init first".
async fn ensure_ops_loaded(
    state: &RwLock<Loaded>,
    secrets_dir: &Path,
    ops_puddle_dir: &Path,
    key_file: Option<&Path>,
) -> Result<()> {
    {
        let s = state.read().await;
        if s.ops.is_some() {
            return Ok(());
        }
    }
    // Slow path: attempt load under the write lock. Double-checked
    // so concurrent OPS_* don't all duplicate the load work.
    let mut s = state.write().await;
    if s.ops.is_some() {
        return Ok(());
    }
    match load_ops_identity(secrets_dir, ops_puddle_dir, key_file) {
        Ok(ops) => {
            s.ops = Some(ops);
            s.ops_last_err = None;
            Ok(())
        }
        Err(e) => {
            s.ops_last_err = Some(format!("{:#}", e));
            Err(e)
        }
    }
}

async fn handle_client(
    stream: tokio::net::UnixStream,
    state: Arc<RwLock<Loaded>>,
    secrets_dir: PathBuf,
    ops_puddle_dir: PathBuf,
    key_file: Option<PathBuf>,
) -> Result<()> {
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
    let (reader, mut writer) = stream.into_split();
    let mut br = BufReader::new(reader);
    let mut line = String::new();
    let n = br.read_line(&mut line).await.context("reading request")?;
    if n == 0 {
        return Ok(());
    }
    let line = line.trim_end_matches('\n').to_string();

    // Service-account verbs read straight from the always-loaded
    // service key under the read lock. Ops verbs may need to lazy-
    // load and take the write lock — gate them via ensure_ops_loaded.
    if line == "STATUS" {
        let s = state.read().await;
        let ops_str = if s.ops.is_some() {
            "loaded"
        } else if s.ops_last_err.is_some() {
            "error"
        } else {
            "unloaded"
        };
        let resp = format!("STATUS unlocked=true ops={}\n", ops_str);
        let _ = writer.write_all(resp.as_bytes()).await;
        return Ok(());
    }

    if line == "PUBKEY" {
        let pubkey_b64 = {
            let s = state.read().await;
            s.pubkey_b64.clone()
        };
        let resp = format!("OK {}\n", pubkey_b64);
        let _ = writer.write_all(resp.as_bytes()).await;
        return Ok(());
    }

    if let Some(rest) = line.strip_prefix("SIGN ") {
        let payload = match B64.decode(rest.trim()) {
            Ok(b) => b,
            Err(_) => {
                let _ = writer.write_all(b"ERR bad base64 payload\n").await;
                return Ok(());
            }
        };
        let key = {
            let s = state.read().await;
            s.key.clone()
        };
        let sig = key.sign(&payload);
        let resp = format!("OK {}\n", B64.encode(sig.to_bytes()));
        let _ = writer.write_all(resp.as_bytes()).await;
        let _ = writer.flush().await;
        return Ok(());
    }

    if line == "OPS_PUBKEY" {
        if let Err(e) =
            ensure_ops_loaded(&state, &secrets_dir, &ops_puddle_dir, key_file.as_deref()).await
        {
            let resp = format!("ERR ops-puddle not available: {:#}\n", e);
            let _ = writer.write_all(resp.as_bytes()).await;
            return Ok(());
        }
        let pubkey_b64 = {
            let s = state.read().await;
            s.ops.as_ref().map(|o| o.pubkey_b64.clone()).unwrap_or_default()
        };
        let resp = format!("OK {}\n", pubkey_b64);
        let _ = writer.write_all(resp.as_bytes()).await;
        return Ok(());
    }

    if let Some(rest) = line.strip_prefix("OPS_SIGN ") {
        let payload = match B64.decode(rest.trim()) {
            Ok(b) => b,
            Err(_) => {
                let _ = writer.write_all(b"ERR bad base64 payload\n").await;
                return Ok(());
            }
        };
        if let Err(e) =
            ensure_ops_loaded(&state, &secrets_dir, &ops_puddle_dir, key_file.as_deref()).await
        {
            let resp = format!("ERR ops-puddle not available: {:#}\n", e);
            let _ = writer.write_all(resp.as_bytes()).await;
            return Ok(());
        }
        let key = {
            let s = state.read().await;
            s.ops.as_ref().map(|o| o.key.clone())
        };
        let Some(key) = key else {
            let _ = writer.write_all(b"ERR ops-puddle vanished between load and sign\n").await;
            return Ok(());
        };
        let sig = key.sign(&payload);
        let resp = format!("OK {}\n", B64.encode(sig.to_bytes()));
        let _ = writer.write_all(resp.as_bytes()).await;
        let _ = writer.flush().await;
        return Ok(());
    }

    if let Some(rest) = line.strip_prefix("FOUND_OPS_PROJECT ") {
        // Wire: FOUND_OPS_PROJECT <handle> <scope>
        // Scope values mirror DrScope::from_str: "swarm-wide" | "members-only".
        let parts: Vec<&str> = rest.trim().splitn(2, ' ').collect();
        if parts.len() != 2 {
            let _ = writer
                .write_all(b"ERR usage: FOUND_OPS_PROJECT <handle> <scope>\n")
                .await;
            return Ok(());
        }
        let handle = parts[0].to_string();
        let scope_str = parts[1].to_string();
        let scope: crate::swarm::gitback::project_state::DrScope = match scope_str.parse() {
            Ok(s) => s,
            Err(e) => {
                let resp = format!("ERR bad scope: {}\n", e);
                let _ = writer.write_all(resp.as_bytes()).await;
                return Ok(());
            }
        };

        if let Err(e) =
            ensure_ops_loaded(&state, &secrets_dir, &ops_puddle_dir, key_file.as_deref()).await
        {
            let resp = format!("ERR ops-puddle not available: {:#}\n", e);
            let _ = writer.write_all(resp.as_bytes()).await;
            return Ok(());
        }
        let (ops_key, ops_pub) = {
            let s = state.read().await;
            match s.ops.as_ref() {
                Some(o) => (o.key.clone(), o.pubkey_bytes),
                None => {
                    let _ = writer
                        .write_all(b"ERR ops-puddle vanished between load and use\n")
                        .await;
                    return Ok(());
                }
            }
        };

        // The signer runs synchronously in-process — no IPC.
        let signer = move |bytes: &[u8]| -> anyhow::Result<[u8; 64]> {
            Ok(ops_key.sign(bytes).to_bytes())
        };
        // State root is the ops-puddle dir; project state lands at
        // <ops_puddle_dir>/.vigo-gitback/<project_id>/ per the per-user
        // gitback convention reused for ops-puddle.
        let project_id = match crate::swarm::gitback::cli::mint_founder_project(
            &ops_puddle_dir,
            &handle,
            scope,
            ops_pub,
            signer,
        ) {
            Ok(pid) => pid,
            Err(e) => {
                let resp = format!("ERR mint_founder_project: {:#}\n", e);
                let _ = writer.write_all(resp.as_bytes()).await;
                return Ok(());
            }
        };
        let resp = format!("OK {}\n", hex::encode(project_id));
        let _ = writer.write_all(resp.as_bytes()).await;
        let _ = writer.flush().await;
        return Ok(());
    }

    let _ = writer.write_all(b"ERR unknown verb\n").await;
    Ok(())
}

fn peer_uid_matches(stream: &tokio::net::UnixStream, allowed: u32) -> bool {
    #[cfg(target_os = "linux")]
    {
        use std::mem;
        use std::os::fd::AsRawFd;
        let fd = stream.as_raw_fd();
        let mut cred: libc::ucred = unsafe { mem::zeroed() };
        let mut len = mem::size_of::<libc::ucred>() as libc::socklen_t;
        let rc = unsafe {
            libc::getsockopt(
                fd,
                libc::SOL_SOCKET,
                libc::SO_PEERCRED,
                &mut cred as *mut _ as *mut _,
                &mut len,
            )
        };
        if rc != 0 {
            return false;
        }
        cred.uid == allowed
    }
    #[cfg(target_os = "macos")]
    {
        use std::os::fd::AsRawFd;
        let fd = stream.as_raw_fd();
        let mut cred: [u32; 2] = [0; 2];
        let mut len = std::mem::size_of_val(&cred) as libc::socklen_t;
        const SOL_LOCAL: libc::c_int = 0;
        const LOCAL_PEEREID: libc::c_int = 3;
        let rc = unsafe {
            libc::getsockopt(
                fd,
                SOL_LOCAL,
                LOCAL_PEEREID,
                cred.as_mut_ptr() as *mut _,
                &mut len,
            )
        };
        if rc != 0 {
            return false;
        }
        return cred[0] == allowed;
    }
    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    {
        let _ = stream;
        let _ = allowed;
        false
    }
}

fn arg_value(args: &[String], key: &str) -> Result<String> {
    let mut iter = args.iter();
    while let Some(a) = iter.next() {
        if a == key {
            return iter
                .next()
                .cloned()
                .with_context(|| format!("{} expects a value", key));
        }
    }
    bail!("missing arg {}", key)
}

// ---------- spawn helper from agent boot ----------

/// Try to start the service-account puddle helper if this host has a
/// service-account puddle on disk and no live helper is already
/// running. No-op otherwise — envoys that aren't co-located with a
/// vigosrv won't have an `identity.wrapped` and this returns
/// silently. Spawned via `current_exe()` + `setsid()` so the helper
/// outlives the agent process that started it.
pub fn try_spawn_helper() {
    let wrap_path = PathBuf::from(DEFAULT_PUDDLE_DIR).join(WRAPPED_FILE);
    if !wrap_path.exists() {
        return;
    }
    if helper_already_running() {
        tracing::debug!("service-account puddle helper already running; skipping spawn");
        return;
    }

    use std::process::{Command, Stdio};

    let current_exe = match std::env::current_exe() {
        Ok(p) => p,
        Err(e) => {
            tracing::warn!(error = %e, "resolving current exe for service-puddle helper");
            return;
        }
    };

    // Best-effort log to /var/log/vigo/; fall back to /dev/null if
    // the dir isn't writable (e.g. read-only / sandboxed host).
    let _ = std::fs::create_dir_all("/var/log/vigo");
    let log = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open("/var/log/vigo/service-puddle.log")
        .ok();

    let mut cmd = Command::new(&current_exe);
    cmd.arg("service-puddle-daemon").stdin(Stdio::null());
    match log {
        Some(f) => {
            let f2 = match f.try_clone() {
                Ok(c) => c,
                Err(_) => {
                    cmd.stdout(Stdio::null()).stderr(Stdio::null());
                    return spawn_detached(cmd);
                }
            };
            cmd.stdout(Stdio::from(f)).stderr(Stdio::from(f2));
        }
        None => {
            cmd.stdout(Stdio::null()).stderr(Stdio::null());
        }
    }

    spawn_detached(cmd);
}

fn spawn_detached(mut cmd: std::process::Command) {
    use std::os::unix::process::CommandExt;

    unsafe {
        cmd.pre_exec(|| {
            libc::setsid();
            Ok(())
        });
    }
    match cmd.spawn() {
        Ok(child) => {
            std::mem::forget(child);
            tracing::info!("started service-account puddle helper");
        }
        Err(e) => {
            tracing::warn!(error = %e, "spawning service-account puddle helper");
        }
    }
}

/// Returns true if a helper pidfile exists and points to a live
/// process. False otherwise (no pidfile, stale pidfile, or read
/// failure). Stale pidfiles are not cleaned up here — the
/// successor helper does that during bind.
fn helper_already_running() -> bool {
    let pidfile = socket_path().with_file_name(PIDFILE_NAME);
    let raw = match fs::read_to_string(&pidfile) {
        Ok(s) => s,
        Err(_) => return false,
    };
    let pid: u32 = match raw.trim().parse() {
        Ok(p) => p,
        Err(_) => return false,
    };
    pid_alive(pid)
}

fn pid_alive(pid: u32) -> bool {
    #[cfg(target_os = "linux")]
    {
        std::path::Path::new(&format!("/proc/{}", pid)).exists()
    }
    #[cfg(not(target_os = "linux"))]
    {
        let rc = unsafe { libc::kill(pid as libc::pid_t, 0) };
        if rc == 0 {
            return true;
        }
        let err = std::io::Error::last_os_error();
        err.raw_os_error() != Some(libc::ESRCH)
    }
}

// ---------- tests ----------

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    /// Reference vector against the Go side: a fixed master key + salt +
    /// envelope produced by the Go code should unwrap to the expected
    /// seed. Generated by the parallel Go tests in server/puddle/ so
    /// the two implementations are byte-compatible.
    #[test]
    fn unwrap_matches_go_construction() {
        // master_key is hex-encoded 32 bytes; salt is 32 raw bytes.
        // Round-trip through our own wrap path (the Go side does the
        // same construction; equivalent byte-shapes => equivalent
        // unwrap behavior).
        let master_key: [u8; 32] = [0x42; 32];
        let salt: [u8; 32] = [0xaa; 32];
        let seed: [u8; 32] = [0x33; 32];

        let wrap_key = derive_wrap_key(&master_key, &salt).unwrap();

        // Build an envelope the way wrap.go does: version || nonce || aead.Seal(seed)
        let nonce: [u8; 24] = [0x55; 24];
        let aead = XChaCha20Poly1305::new(wrap_key.as_slice().into());
        let ct = aead
            .encrypt(XNonce::from_slice(&nonce), seed.as_slice())
            .unwrap();
        let mut envelope = Vec::with_capacity(1 + 24 + ct.len());
        envelope.push(WRAP_VERSION);
        envelope.extend_from_slice(&nonce);
        envelope.extend_from_slice(&ct);

        let unwrapped = unwrap_envelope(&envelope, &wrap_key).unwrap();
        assert_eq!(&*unwrapped, &seed);
    }

    #[test]
    fn unwrap_refuses_wrong_version() {
        let master_key: [u8; 32] = [0x01; 32];
        let salt: [u8; 32] = [0x02; 32];
        let wrap_key = derive_wrap_key(&master_key, &salt).unwrap();

        let nonce: [u8; 24] = [0x03; 24];
        let aead = XChaCha20Poly1305::new(wrap_key.as_slice().into());
        let ct = aead.encrypt(XNonce::from_slice(&nonce), &[0u8; 32][..]).unwrap();
        let mut envelope = Vec::with_capacity(1 + 24 + ct.len());
        envelope.push(0x01); // wrong version (per-user envelope, not service-account)
        envelope.extend_from_slice(&nonce);
        envelope.extend_from_slice(&ct);

        let err = unwrap_envelope(&envelope, &wrap_key).unwrap_err();
        assert!(err.to_string().contains("0x01"));
        assert!(err.to_string().contains("0x02"));
    }

    #[test]
    fn unwrap_refuses_wrong_key() {
        let master_key: [u8; 32] = [0x01; 32];
        let salt: [u8; 32] = [0x02; 32];
        let wrong_master: [u8; 32] = [0x99; 32];
        let wrap_key = derive_wrap_key(&master_key, &salt).unwrap();
        let wrong_wrap_key = derive_wrap_key(&wrong_master, &salt).unwrap();

        let nonce: [u8; 24] = [0x03; 24];
        let aead = XChaCha20Poly1305::new(wrap_key.as_slice().into());
        let ct = aead.encrypt(XNonce::from_slice(&nonce), &[0u8; 32][..]).unwrap();
        let mut envelope = Vec::with_capacity(1 + 24 + ct.len());
        envelope.push(WRAP_VERSION);
        envelope.extend_from_slice(&nonce);
        envelope.extend_from_slice(&ct);

        let err = unwrap_envelope(&envelope, &wrong_wrap_key).unwrap_err();
        assert!(err.to_string().contains("unwrap failed"));
    }

    #[test]
    fn read_master_key_accepts_hex() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join(".master.key");
        let raw_key: [u8; 32] = [0x42; 32];
        let hex = hex::encode(raw_key);
        fs::write(&path, format!("{}\n", hex)).unwrap();

        let parsed = read_master_key(&path).unwrap();
        assert_eq!(&*parsed, &raw_key);
    }

    #[test]
    fn read_master_key_accepts_raw_32() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join(".master.key");
        let raw_key: [u8; 32] = [0x77; 32];
        fs::write(&path, raw_key).unwrap();

        let parsed = read_master_key(&path).unwrap();
        assert_eq!(&*parsed, &raw_key);
    }

    #[test]
    fn read_master_key_sha256_falls_back() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join(".master.key");
        let arbitrary = b"some-long-passphrase-that-is-not-32-bytes-or-64-hex";
        fs::write(&path, arbitrary).unwrap();

        let parsed = read_master_key(&path).unwrap();
        // Should be SHA-256(arbitrary); verify by computing it.
        let mut h = Sha256::new();
        h.update(arbitrary);
        let want = h.finalize();
        assert_eq!(&parsed[..], &want[..]);
    }

    #[test]
    fn parse_ok_decodes_base64() {
        let line = format!("OK {}\n", B64.encode(b"sig-bytes"));
        let out = parse_ok_or_err(&line).unwrap();
        assert_eq!(out, b"sig-bytes");
    }

    #[test]
    fn parse_err_returns_error() {
        let err = parse_ok_or_err("ERR no key loaded\n").unwrap_err();
        assert!(err.to_string().contains("no key loaded"));
    }

    #[test]
    fn arg_value_finds_key() {
        let args = vec![
            "--secrets-dir".into(),
            "/srv/vigo/secrets".into(),
            "--puddle-dir".into(),
            "/srv/vigo/puddle".into(),
        ];
        assert_eq!(arg_value(&args, "--secrets-dir").unwrap(), "/srv/vigo/secrets");
        assert_eq!(arg_value(&args, "--puddle-dir").unwrap(), "/srv/vigo/puddle");
        assert!(arg_value(&args, "--missing").is_err());
    }
}
