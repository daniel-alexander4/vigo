// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Download engine: fetches blob chunks from the server using HTTP Range
//! requests. Runs at low I/O priority to avoid starving the host.
//! In later phases, this becomes the fallback path when no peers are available.

use anyhow::{bail, Context, Result};
use std::collections::HashMap;
use tracing::{debug, info, warn};

use super::cache::BlobCache;
use super::meta::{BlobMeta, CHUNK_SIZE};
use crate::update::TlsCerts;

/// Download a blob from the server, chunk by chunk, with resume support.
/// Runs in a blocking thread (ureq is synchronous).
pub async fn download_from_server(
    meta: &BlobMeta,
    server_url: &str,
    tls: Option<&TlsCerts>,
    cache: &BlobCache,
) -> Result<std::path::PathBuf> {
    // Check disk space before starting.
    cache
        .check_disk_space(meta.size)
        .context("pre-download disk space check")?;

    let meta = meta.clone();
    let server_url = server_url.to_string();
    let tls_owned = tls.map(|t| TlsCerts {
        ca_cert: t.ca_cert.clone(),
        client_cert: t.client_cert.clone(),
        client_key: t.client_key.clone(),
    });
    let cache_dir = cache.dir().to_path_buf();

    let path = tokio::task::spawn_blocking(move || -> Result<std::path::PathBuf> {
        // Apply low I/O priority on Linux.
        #[cfg(target_os = "linux")]
        {
            unsafe {
                libc::nice(15);
                let ioprio = (2 << 13) | 7; // best-effort, lowest priority
                libc::syscall(libc::SYS_ioprio_set, 1i32, 0i32, ioprio);
            }
        }
        #[cfg(all(unix, not(target_os = "linux")))]
        unsafe {
            libc::nice(15);
        }

        let agent = build_download_agent(tls_owned.as_ref())?;
        let cache = BlobCache::new(cache_dir)?;

        // If already complete, return immediately.
        if cache.has_blob(&meta.sha256) {
            debug!(sha256 = &meta.sha256[..12], "blob already in cache");
            return Ok(cache.dir().join(&meta.sha256));
        }

        let download_url = format!("{}/api/v1/swarm/blob/{}", server_url, meta.sha256);

        info!(
            sha256 = &meta.sha256[..12],
            label = %meta.label,
            size = meta.size,
            chunks = meta.chunk_count,
            "downloading blob from server"
        );

        let download_start = std::time::Instant::now();
        let mut chunk_details: Vec<(u32, String, u64)> = Vec::new();

        for i in 0..meta.chunk_count {
            // Skip chunks we already have (resume support).
            if cache.has_chunk(&meta.sha256, i) {
                debug!(chunk = i, "chunk already cached, skipping");
                continue;
            }

            // Re-check disk space every 10 chunks.
            if i > 0 && i % 10 == 0 {
                if let Err(e) = cache.check_disk_space(meta.size) {
                    warn!(error = %e, chunk = i, "disk space low during download, pausing");
                    bail!("disk space check failed at chunk {}: {}", i, e);
                }
            }

            let start_byte = i as u64 * CHUNK_SIZE;
            let end_byte = std::cmp::min(start_byte + CHUNK_SIZE - 1, meta.size - 1);
            let range = format!("bytes={}-{}", start_byte, end_byte);

            debug!(chunk = i, range = %range, "downloading chunk");

            let chunk_start = std::time::Instant::now();
            let resp = agent
                .get(&download_url)
                .header("Range", &range)
                .call()
                .map_err(|e| anyhow::anyhow!("chunk {} download failed: {}", i, e))?;

            if resp.status() != 206 && resp.status() != 200 {
                bail!("chunk {} returned status {}", i, resp.status());
            }

            let body = resp
                .into_body()
                .read_to_vec()
                .with_context(|| format!("reading chunk {} body", i))?;

            let expected_len = (end_byte - start_byte + 1) as usize;
            if body.len() != expected_len {
                bail!(
                    "chunk {} size mismatch: expected {}, got {}",
                    i,
                    expected_len,
                    body.len()
                );
            }

            let chunk_ms = chunk_start.elapsed().as_millis() as u64;
            chunk_details.push((i, "server".to_string(), chunk_ms));

            cache.write_chunk(&meta.sha256, i, &body)?;
        }

        // Write source data so the trait collector can report it.
        let total_ms = download_start.elapsed().as_millis() as u64;
        let mut source_counts: HashMap<String, u32> = HashMap::new();
        source_counts.insert("server".to_string(), meta.chunk_count as u32);
        cache.write_chunk_sources(&meta.sha256, &source_counts, &chunk_details, total_ms);

        // Assemble and verify.
        let path = cache.assemble(&meta.sha256, meta.size, meta.chunk_count)?;
        Ok(path)
    })
    .await
    .context("download task panicked")??;

    Ok(path)
}

/// Build a ureq agent for blob downloads, with optional mTLS.
pub fn build_download_agent(tls: Option<&TlsCerts>) -> Result<ureq::Agent> {
    if let Some(certs) = tls {
        return build_mtls_download_agent(certs);
    }
    Ok(ureq::Agent::new_with_config(
        ureq::config::Config::builder()
            .timeout_global(Some(std::time::Duration::from_secs(120)))
            .build(),
    ))
}

fn build_mtls_download_agent(certs: &TlsCerts) -> Result<ureq::Agent> {
    use ureq::tls::{parse_pem, PemItem};

    let ca_pem = std::fs::read(&certs.ca_cert).context("read CA cert")?;
    let cert_pem = std::fs::read(&certs.client_cert).context("read client cert")?;
    let key_pem = std::fs::read(&certs.client_key).context("read client key")?;

    let mut ca_certs = Vec::new();
    for item in parse_pem(&ca_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            ca_certs.push(c);
        }
    }
    let root_certs = ureq::tls::RootCerts::new_with_certs(&ca_certs);

    let mut client_certs = Vec::new();
    for item in parse_pem(&cert_pem) {
        if let Ok(PemItem::Certificate(c)) = item {
            client_certs.push(c);
        }
    }

    let mut client_key = None;
    for item in parse_pem(&key_pem) {
        if let Ok(PemItem::PrivateKey(k)) = item {
            client_key = Some(k);
            break;
        }
    }
    let key = client_key.context("no private key found in client key PEM")?;

    let client_id = ureq::tls::ClientCert::new_with_certs(&client_certs, key);

    let tls_cfg = ureq::tls::TlsConfig::builder()
        .root_certs(root_certs)
        .client_cert(Some(client_id))
        .build();

    Ok(ureq::Agent::new_with_config(
        ureq::config::Config::builder()
            .tls_config(tls_cfg)
            .timeout_global(Some(std::time::Duration::from_secs(120)))
            .build(),
    ))
}
