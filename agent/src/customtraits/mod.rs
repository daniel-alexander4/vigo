// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Custom trait executable sync, caching, execution, and JSON parsing.
//!
//! Mirrors the Go `customtraits` package: compares a server-provided manifest
//! against locally cached executables, downloads new/changed ones, cleans up
//! stale files, executes each with a timeout, and parses JSON output.

use anyhow::{Context, Result};
use sha2::{Digest, Sha256};
use serde_json::Value;
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::time::Duration;
use tracing::warn;

/// Abstraction over the gRPC download call, for testability.
#[async_trait::async_trait]
pub trait Downloader: Send + Sync {
    async fn download_custom_trait(&mut self, name: &str) -> Result<Vec<u8>>;
}

/// Entry from the server manifest (name + expected SHA256).
#[derive(Debug, Clone)]
pub struct ManifestEntry {
    pub name: String,
    pub sha256: String,
}

/// Manages custom trait sync, caching, execution, and JSON parsing.
pub struct Executor {
    cache_dir: PathBuf,
    timeout: Duration,
}

impl Executor {
    pub fn new(cache_dir: &Path, timeout: Duration) -> Self {
        Self {
            cache_dir: cache_dir.to_path_buf(),
            timeout,
        }
    }

    /// Compare the manifest against locally cached executables, download
    /// new/changed ones, clean up stale files, execute each with a timeout,
    /// and return the merged results.
    ///
    /// Results are nested under a `"custom"` key in the returned map.
    pub async fn sync_and_run(
        &self,
        downloader: &mut dyn Downloader,
        manifest: &[ManifestEntry],
    ) -> Result<Value> {
        if manifest.is_empty() {
            return Ok(Value::Object(Default::default()));
        }

        // Ensure cache directory exists.
        std::fs::create_dir_all(&self.cache_dir)
            .with_context(|| format!("create cache dir {:?}", self.cache_dir))?;

        // The name is server-supplied and becomes a path under the cache dir
        // that root executes: anything but a single plain file-name component
        // (`../x`, `/etc/cron.d/x`, `a/b`) would write and run outside it.
        let manifest: Vec<&ManifestEntry> = manifest
            .iter()
            .filter(|e| {
                let ok = is_safe_trait_name(&e.name);
                if !ok {
                    warn!(name = %e.name, "custom trait name is not a single file name — refusing it (not downloaded, not run)");
                }
                ok
            })
            .collect();

        // Build set of manifest names for stale cleanup.
        let manifest_names: std::collections::HashSet<&str> =
            manifest.iter().map(|e| e.name.as_str()).collect();

        // Compute local checksums.
        let local = local_checksums(&self.cache_dir)?;

        // Only an executable whose bytes match the manifest hash this cycle is
        // run — a cached file from an older manifest must not run in place of
        // a download that failed or did not verify.
        let mut verified: std::collections::HashSet<&str> = std::collections::HashSet::new();

        // Download new or changed executables.
        for entry in &manifest {
            if let Some(local_hash) = local.get(&entry.name) {
                if *local_hash == entry.sha256 {
                    verified.insert(entry.name.as_str());
                    continue; // up to date
                }
            }
            let path = self.cache_dir.join(&entry.name);

            match downloader.download_custom_trait(&entry.name).await {
                Ok(data) => {
                    // Verify the downloaded bytes against the manifest hash
                    // BEFORE writing/executing — a corrupt or tampered
                    // download must never land executable on disk or run
                    // (mirrors the self-update verify-then-write order in
                    // update::apply). The hash was previously only checked on
                    // the next sync, so a bad binary ran at least once.
                    let got = compute_sha256(&data);
                    if got != entry.sha256 {
                        warn!(
                            name = %entry.name,
                            expected = %entry.sha256,
                            got = %got,
                            "custom trait hash mismatch — skipping (not written, not run; stale cached copy removed)",
                        );
                        remove_stale(&path);
                        continue;
                    }
                    match write_executable(&path, &data) {
                        Ok(()) => {
                            verified.insert(entry.name.as_str());
                        }
                        Err(e) => {
                            warn!(name = %entry.name, error = %e, "failed to write custom trait");
                            remove_stale(&path);
                        }
                    }
                }
                Err(e) => {
                    warn!(name = %entry.name, error = %e, "failed to download custom trait (stale cached copy removed, not run)");
                    remove_stale(&path);
                }
            }
        }

        // Clean up stale files not in manifest.
        if let Ok(entries) = std::fs::read_dir(&self.cache_dir) {
            for ent in entries.flatten() {
                if ent.file_type().map_or(true, |ft| ft.is_dir()) {
                    continue;
                }
                let name = ent.file_name().to_string_lossy().to_string();
                if !manifest_names.contains(name.as_str()) {
                    let _ = std::fs::remove_file(ent.path());
                }
            }
        }

        // Execute each trait and collect results.
        let mut custom_map = serde_json::Map::new();
        for entry in &manifest {
            if !verified.contains(entry.name.as_str()) {
                continue; // download failed or did not verify
            }
            let path = self.cache_dir.join(&entry.name);

            match self.run_one(&entry.name, &path).await {
                Ok(parsed) => {
                    custom_map.insert(entry.name.clone(), parsed);
                }
                Err(e) => {
                    warn!(name = %entry.name, error = %e, "custom trait execution failed");
                }
            }
        }

        let mut result = serde_json::Map::new();
        if !custom_map.is_empty() {
            result.insert("custom".to_string(), Value::Object(custom_map));
        }

        Ok(Value::Object(result))
    }

    /// Execute a single trait executable and parse its stdout as JSON.
    /// Runs through the shared bounded runner off the async runtime: niced,
    /// stdin from /dev/null, its own process group killed (children too) at
    /// the timeout, and stdout/stderr capped at [`MAX_TRAIT_OUTPUT`].
    async fn run_one(&self, name: &str, path: &Path) -> Result<Value> {
        let cmd = std::process::Command::new(path);
        let label = name.to_string();
        let timeout = self.timeout;
        let output = tokio::task::spawn_blocking(move || {
            crate::executors::run_built_command_niced_timeout_capped(
                cmd,
                &label,
                crate::traits::COLLECTOR_NICE,
                timeout,
                MAX_TRAIT_OUTPUT,
            )
        })
        .await
        .with_context(|| format!("join {}", name))?
        .with_context(|| format!("execute {}", name))?;

        if output.stdout.len() as u64 >= MAX_TRAIT_OUTPUT {
            anyhow::bail!("{} wrote more than {} bytes of output", name, MAX_TRAIT_OUTPUT);
        }

        if !output.status.success() {
            anyhow::bail!(
                "{} exited with status {}",
                name,
                output.status.code().unwrap_or(-1)
            );
        }

        let parsed: Value = serde_json::from_slice(&output.stdout)
            .with_context(|| format!("parse JSON from {}", name))?;

        // Ensure the output is an object.
        if !parsed.is_object() {
            anyhow::bail!("{} returned non-object JSON", name);
        }

        Ok(parsed)
    }
}

/// Largest stdout (and stderr) a custom trait may produce. Trait output is
/// a small JSON object; anything this size is a broken or hostile script.
const MAX_TRAIT_OUTPUT: u64 = 1024 * 1024;

/// is_safe_trait_name: the name is exactly one plain file-name component.
fn is_safe_trait_name(name: &str) -> bool {
    use std::path::Component;
    if name.is_empty() || name.contains('/') || name.contains('\\') || name.contains('\0') {
        return false;
    }
    let mut comps = Path::new(name).components();
    matches!((comps.next(), comps.next()), (Some(Component::Normal(_)), None))
}

/// Remove a cached executable that no longer matches the manifest.
fn remove_stale(path: &Path) {
    if let Err(e) = std::fs::remove_file(path) {
        if e.kind() != std::io::ErrorKind::NotFound {
            warn!(path = %path.display(), error = %e, "failed to remove stale custom trait");
        }
    }
}

/// Write data to a file and mark it executable (0755): written to a temp
/// file in the same directory and renamed over the target, so a reader
/// never sees a partial executable and an existing symlink at the target is
/// replaced, not followed.
fn write_executable(path: &Path, data: &[u8]) -> Result<()> {
    use std::io::Write;
    let dir = path.parent().unwrap_or(Path::new("."));
    let mut f = tempfile::Builder::new().prefix(".trait-").tempfile_in(dir)?;
    f.write_all(data)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        f.as_file().set_permissions(std::fs::Permissions::from_mode(0o755))?;
    }
    f.as_file().sync_all()?;
    // Close the writer before anything can exec the file (ETXTBSY).
    f.into_temp_path().persist(path).map_err(|e| e.error)?;
    Ok(())
}

/// Read all files in dir and return a map of filename → SHA256 hex digest.
fn local_checksums(dir: &Path) -> Result<HashMap<String, String>> {
    let mut checksums = HashMap::new();

    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(checksums),
        Err(e) => return Err(e.into()),
    };

    for ent in entries.flatten() {
        if ent.file_type().map_or(true, |ft| ft.is_dir()) {
            continue;
        }
        if let Ok(data) = std::fs::read(ent.path()) {
            let name = ent.file_name().to_string_lossy().to_string();
            checksums.insert(name, compute_sha256(&data));
        }
    }

    Ok(checksums)
}

/// Compute SHA256 hex digest of data.
pub fn compute_sha256(data: &[u8]) -> String {
    format!("{:x}", Sha256::digest(data))
}

/// Convert proto CustomTraitEntry list to ManifestEntry list.
pub fn manifest_from_proto(entries: &[crate::proto::CustomTraitEntry]) -> Vec<ManifestEntry> {
    entries
        .iter()
        .map(|e| ManifestEntry {
            name: e.name.clone(),
            sha256: e.sha256.clone(),
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Mock downloader for tests.
    struct MockDownloader {
        data: HashMap<String, Vec<u8>>,
        called: HashMap<String, usize>,
        fail_name: Option<String>,
    }

    impl MockDownloader {
        fn new() -> Self {
            Self {
                data: HashMap::new(),
                called: HashMap::new(),
                fail_name: None,
            }
        }
    }

    #[async_trait::async_trait]
    impl Downloader for MockDownloader {
        async fn download_custom_trait(&mut self, name: &str) -> Result<Vec<u8>> {
            *self.called.entry(name.to_string()).or_default() += 1;
            if self.fail_name.as_deref() == Some(name) {
                anyhow::bail!("download failed for {}", name);
            }
            self.data
                .get(name)
                .cloned()
                .ok_or_else(|| anyhow::anyhow!("trait {:?} not found", name))
        }
    }

    fn make_script(output: &str) -> Vec<u8> {
        format!("#!/bin/sh\necho '{}'\n", output).into_bytes()
    }

    fn make_fail_script() -> Vec<u8> {
        b"#!/bin/sh\nexit 1\n".to_vec()
    }

    fn make_sleep_script(seconds: u32) -> Vec<u8> {
        format!("#!/bin/sh\nsleep {}\n", seconds).into_bytes()
    }

    #[tokio::test]
    async fn sync_and_run_new_traits_downloads_both() {
        let dir = tempfile::tempdir().unwrap();
        let mut dl = MockDownloader::new();

        let script1 = make_script(r#"{"version":"1.2.3"}"#);
        let script2 = make_script(r#"{"status":"ok"}"#);
        dl.data.insert("app-version".into(), script1.clone());
        dl.data.insert("health".into(), script2.clone());

        let exec = Executor::new(dir.path(), Duration::from_secs(10));
        let manifest = vec![
            ManifestEntry { name: "app-version".into(), sha256: compute_sha256(&script1) },
            ManifestEntry { name: "health".into(), sha256: compute_sha256(&script2) },
        ];

        let result = exec.sync_and_run(&mut dl, &manifest).await.unwrap();

        assert_eq!(dl.called.get("app-version"), Some(&1));
        assert_eq!(dl.called.get("health"), Some(&1));

        let custom = result.get("custom").unwrap().as_object().unwrap();
        assert_eq!(
            custom.get("app-version").unwrap().get("version").unwrap(),
            "1.2.3"
        );
        assert_eq!(
            custom.get("health").unwrap().get("status").unwrap(),
            "ok"
        );
    }

    #[tokio::test]
    async fn sync_and_run_cached_no_redownload() {
        let dir = tempfile::tempdir().unwrap();
        let mut dl = MockDownloader::new();

        let script = make_script(r#"{"cached":"yes"}"#);
        dl.data.insert("mytrait".into(), script.clone());

        // Pre-populate cache.
        let cache_path = dir.path().join("mytrait");
        write_executable(&cache_path, &script).unwrap();

        let exec = Executor::new(dir.path(), Duration::from_secs(10));
        let manifest = vec![ManifestEntry {
            name: "mytrait".into(),
            sha256: compute_sha256(&script),
        }];

        let result = exec.sync_and_run(&mut dl, &manifest).await.unwrap();

        // Should NOT have downloaded (checksum matches).
        assert_eq!(dl.called.get("mytrait"), None);

        // But should still have executed.
        let custom = result.get("custom").unwrap().as_object().unwrap();
        assert!(custom.contains_key("mytrait"));
    }

    #[tokio::test]
    async fn sync_and_run_changed_downloads_only_changed() {
        let dir = tempfile::tempdir().unwrap();
        let mut dl = MockDownloader::new();

        let old_script = make_script(r#"{"v":"old"}"#);
        let new_script = make_script(r#"{"v":"new"}"#);
        let unchanged_script = make_script(r#"{"v":"same"}"#);

        dl.data.insert("changed".into(), new_script.clone());
        dl.data.insert("unchanged".into(), unchanged_script.clone());

        // Pre-populate cache.
        write_executable(&dir.path().join("changed"), &old_script).unwrap();
        write_executable(&dir.path().join("unchanged"), &unchanged_script).unwrap();

        let exec = Executor::new(dir.path(), Duration::from_secs(10));
        let manifest = vec![
            ManifestEntry { name: "changed".into(), sha256: compute_sha256(&new_script) },
            ManifestEntry { name: "unchanged".into(), sha256: compute_sha256(&unchanged_script) },
        ];

        exec.sync_and_run(&mut dl, &manifest).await.unwrap();

        assert_eq!(dl.called.get("changed"), Some(&1));
        assert_eq!(dl.called.get("unchanged"), None);
    }

    #[tokio::test]
    async fn sync_and_run_nonzero_exit_skipped() {
        let dir = tempfile::tempdir().unwrap();
        let mut dl = MockDownloader::new();

        let fail_script = make_fail_script();
        let good_script = make_script(r#"{"ok":true}"#);
        dl.data.insert("bad-trait".into(), fail_script.clone());
        dl.data.insert("good-trait".into(), good_script.clone());

        let exec = Executor::new(dir.path(), Duration::from_secs(10));
        let manifest = vec![
            ManifestEntry { name: "bad-trait".into(), sha256: compute_sha256(&fail_script) },
            ManifestEntry { name: "good-trait".into(), sha256: compute_sha256(&good_script) },
        ];

        let result = exec.sync_and_run(&mut dl, &manifest).await.unwrap();
        let custom = result.get("custom").unwrap().as_object().unwrap();

        assert!(!custom.contains_key("bad-trait"));
        assert!(custom.contains_key("good-trait"));
    }

    #[tokio::test]
    async fn sync_and_run_hash_mismatch_not_written_or_run() {
        let dir = tempfile::tempdir().unwrap();
        let mut dl = MockDownloader::new();

        // The downloader serves bytes that do NOT match the manifest hash
        // (tampered/corrupt download). It must be neither written to the
        // cache dir nor executed.
        let served = make_script(r#"{"evil":true}"#);
        dl.data.insert("tampered".into(), served);

        let exec = Executor::new(dir.path(), Duration::from_secs(10));
        let manifest = vec![ManifestEntry {
            name: "tampered".into(),
            sha256: compute_sha256(b"the bytes the server actually vouched for"),
        }];

        let result = exec.sync_and_run(&mut dl, &manifest).await.unwrap();

        // Downloaded once, but rejected: nothing landed on disk...
        assert_eq!(dl.called.get("tampered"), Some(&1));
        assert!(!dir.path().join("tampered").exists(), "mismatched binary was written");
        // ...and it never ran.
        assert!(result.get("custom").is_none());
    }

    #[tokio::test]
    async fn sync_and_run_invalid_json_skipped() {
        let dir = tempfile::tempdir().unwrap();
        let mut dl = MockDownloader::new();

        let bad_script = make_script("not valid json");
        dl.data.insert("bad-json".into(), bad_script.clone());

        let exec = Executor::new(dir.path(), Duration::from_secs(10));
        let manifest = vec![ManifestEntry {
            name: "bad-json".into(),
            sha256: compute_sha256(&bad_script),
        }];

        let result = exec.sync_and_run(&mut dl, &manifest).await.unwrap();

        // Should have no custom key (or empty).
        if let Some(custom) = result.get("custom") {
            assert!(!custom.as_object().unwrap().contains_key("bad-json"));
        }
    }

    #[tokio::test]
    async fn sync_and_run_timeout_killed() {
        let dir = tempfile::tempdir().unwrap();
        let mut dl = MockDownloader::new();

        let slow_script = make_sleep_script(15);
        dl.data.insert("slow-trait".into(), slow_script.clone());

        let exec = Executor::new(dir.path(), Duration::from_millis(500));
        let manifest = vec![ManifestEntry {
            name: "slow-trait".into(),
            sha256: compute_sha256(&slow_script),
        }];

        let result = exec.sync_and_run(&mut dl, &manifest).await.unwrap();

        if let Some(custom) = result.get("custom") {
            assert!(!custom.as_object().unwrap().contains_key("slow-trait"));
        }
    }

    #[tokio::test]
    async fn sync_and_run_empty_manifest() {
        let dir = tempfile::tempdir().unwrap();
        let mut dl = MockDownloader::new();

        let exec = Executor::new(dir.path(), Duration::from_secs(10));
        let result = exec.sync_and_run(&mut dl, &[]).await.unwrap();

        assert!(result.as_object().unwrap().is_empty() || !result.as_object().unwrap().contains_key("custom"));
    }

    #[tokio::test]
    async fn sync_and_run_stale_files_cleaned() {
        let dir = tempfile::tempdir().unwrap();
        let mut dl = MockDownloader::new();

        // Create a stale file not in manifest.
        let stale = dir.path().join("old-trait");
        write_executable(&stale, &make_script(r#"{"stale":true}"#)).unwrap();

        let active_script = make_script(r#"{"active":true}"#);
        dl.data.insert("active-trait".into(), active_script.clone());

        let exec = Executor::new(dir.path(), Duration::from_secs(10));
        let manifest = vec![ManifestEntry {
            name: "active-trait".into(),
            sha256: compute_sha256(&active_script),
        }];

        exec.sync_and_run(&mut dl, &manifest).await.unwrap();

        assert!(!stale.exists(), "stale file should be deleted");
        assert!(dir.path().join("active-trait").exists());
    }

    /// A server-supplied name that is not one plain file-name component
    /// must never be joined onto the cache dir: `../x` or `/abs/x` would let
    /// the manifest write and run a root executable anywhere.
    #[tokio::test]
    async fn sync_and_run_rejects_path_traversal_names() {
        let root = tempfile::tempdir().unwrap();
        let cache = root.path().join("cache");
        let abs_target = root.path().join("abs-escape");
        let mut dl = MockDownloader::new();
        let script = make_script(r#"{"escaped":true}"#);
        let names = vec![
            "../escape".to_string(),
            abs_target.to_string_lossy().to_string(),
            "a/b".to_string(),
            "..".to_string(),
            String::new(),
        ];
        for n in &names {
            dl.data.insert(n.clone(), script.clone());
        }
        let manifest: Vec<ManifestEntry> = names
            .iter()
            .map(|n| ManifestEntry { name: n.clone(), sha256: compute_sha256(&script) })
            .collect();

        let exec = Executor::new(&cache, Duration::from_secs(10));
        let result = exec.sync_and_run(&mut dl, &manifest).await.unwrap();

        assert!(dl.called.is_empty(), "an unsafe name was downloaded: {:?}", dl.called);
        assert!(!root.path().join("escape").exists(), "../ escaped the cache dir");
        assert!(!abs_target.exists(), "absolute name was written");
        assert!(result.get("custom").is_none());
    }

    #[test]
    fn safe_trait_name_accepts_plain_names_only() {
        assert!(is_safe_trait_name("app-version"));
        assert!(is_safe_trait_name("x.sh"));
        for bad in ["", ".", "..", "../x", "/etc/x", "a/b", "a\\b", "nul\0x"] {
            assert!(!is_safe_trait_name(bad), "{bad:?} accepted");
        }
    }

    /// A cached executable from an older manifest must not run when the
    /// new download fails verification — it is removed instead.
    #[tokio::test]
    async fn sync_and_run_hash_mismatch_removes_stale_cached_copy() {
        let dir = tempfile::tempdir().unwrap();
        let mut dl = MockDownloader::new();
        let old_script = make_script(r#"{"v":"old"}"#);
        write_executable(&dir.path().join("t"), &old_script).unwrap();
        dl.data.insert("t".into(), make_script(r#"{"v":"tampered"}"#));

        let exec = Executor::new(dir.path(), Duration::from_secs(10));
        let manifest = vec![ManifestEntry {
            name: "t".into(),
            sha256: compute_sha256(b"what the server vouched for"),
        }];
        let result = exec.sync_and_run(&mut dl, &manifest).await.unwrap();

        assert!(result.get("custom").is_none(), "stale cached trait ran: {result}");
        assert!(!dir.path().join("t").exists(), "stale cached trait was left on disk");
    }

    /// Same when the download itself fails: the outdated cached copy is
    /// neither run nor kept.
    #[tokio::test]
    async fn sync_and_run_download_failure_does_not_run_stale_copy() {
        let dir = tempfile::tempdir().unwrap();
        let mut dl = MockDownloader::new();
        write_executable(&dir.path().join("t"), &make_script(r#"{"v":"old"}"#)).unwrap();
        dl.fail_name = Some("t".into());

        let exec = Executor::new(dir.path(), Duration::from_secs(10));
        let manifest = vec![ManifestEntry {
            name: "t".into(),
            sha256: compute_sha256(&make_script(r#"{"v":"new"}"#)),
        }];
        let result = exec.sync_and_run(&mut dl, &manifest).await.unwrap();

        assert!(result.get("custom").is_none(), "stale cached trait ran: {result}");
        assert!(!dir.path().join("t").exists());
    }

    /// Output past MAX_TRAIT_OUTPUT is refused rather than buffered.
    #[tokio::test]
    async fn run_one_caps_output() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("big");
        // ~2 MiB of output from a trait that would otherwise be valid JSON-ish.
        write_executable(&path, b"#!/bin/sh\nhead -c 2097152 /dev/zero | tr '\\0' 'a'\n").unwrap();
        let exec = Executor::new(dir.path(), Duration::from_secs(10));
        let err = exec.run_one("big", &path).await.unwrap_err();
        assert!(format!("{err:#}").contains("more than"), "got: {err:#}");
    }

    /// On timeout the whole process group dies — a background grandchild
    /// the trait started is killed too, not left running.
    #[cfg(unix)]
    #[tokio::test]
    async fn run_one_timeout_kills_grandchildren() {
        let dir = tempfile::tempdir().unwrap();
        let pidfile = dir.path().join("grandchild.pid");
        let path = dir.path().join("hang");
        let script = format!(
            "#!/bin/sh\nsleep 30 &\necho $! > {}\nsleep 30\n",
            pidfile.display()
        );
        write_executable(&path, script.as_bytes()).unwrap();
        let exec = Executor::new(dir.path(), Duration::from_millis(500));
        assert!(exec.run_one("hang", &path).await.is_err());

        let pid: i32 = std::fs::read_to_string(&pidfile).unwrap().trim().parse().unwrap();
        let mut alive = true;
        for _ in 0..50 {
            // kill(pid, 0) fails with ESRCH once the grandchild is gone; a
            // reaped-by-init zombie also disappears quickly.
            alive = unsafe { libc::kill(pid, 0) } == 0;
            if !alive {
                break;
            }
            std::thread::sleep(Duration::from_millis(50));
        }
        assert!(!alive, "grandchild {pid} survived the trait timeout");
    }
}
