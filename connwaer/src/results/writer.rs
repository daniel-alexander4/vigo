use anyhow::{Context, Result};
use std::fs;
use std::path::Path;

use crate::capability::CapabilityResult;

/// Atomically write a CapabilityResult as JSON.
/// Writes to <results_dir>/<capability>.json.tmp then renames to .json.
pub fn write_result(results_dir: &Path, result: &CapabilityResult) -> Result<()> {
    fs::create_dir_all(results_dir)
        .with_context(|| format!("creating results dir: {}", results_dir.display()))?;

    let filename = format!("{}.json", result.capability);
    let final_path = results_dir.join(&filename);
    let tmp_path = results_dir.join(format!("{}.json.tmp", result.capability));

    let json = serde_json::to_string_pretty(result)
        .with_context(|| format!("serializing result for {}", result.capability))?;

    fs::write(&tmp_path, json.as_bytes())
        .with_context(|| format!("writing {}", tmp_path.display()))?;

    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(&tmp_path, fs::Permissions::from_mode(0o644));
    }

    fs::rename(&tmp_path, &final_path)
        .with_context(|| format!("renaming {} -> {}", tmp_path.display(), final_path.display()))?;

    Ok(())
}

/// Prune the results directory to at most `max_files` JSON files,
/// deleting the oldest by modification time.
pub fn prune_results(results_dir: &Path, max_files: usize) -> Result<()> {
    let entries = match fs::read_dir(results_dir) {
        Ok(e) => e,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(e) => return Err(e).context("reading results dir"),
    };

    let mut files: Vec<(std::time::SystemTime, std::path::PathBuf)> = Vec::new();
    for entry in entries {
        let entry = entry?;
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) != Some("json") {
            continue;
        }
        let mtime = entry.metadata()
            .and_then(|m| m.modified())
            .unwrap_or(std::time::UNIX_EPOCH);
        files.push((mtime, path));
    }

    if files.len() <= max_files {
        return Ok(());
    }

    // Sort oldest first.
    files.sort_by_key(|(mtime, _)| *mtime);
    let to_remove = files.len() - max_files;
    for (_, path) in files.iter().take(to_remove) {
        let _ = fs::remove_file(path);
    }

    tracing::debug!("pruned {to_remove} old result files");
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::capability::{CapabilityResult, VerificationStatus};
    use tempfile::TempDir;

    fn sample_result() -> CapabilityResult {
        CapabilityResult {
            capability: "test_cap".to_string(),
            status: VerificationStatus::Pass,
            summary: "All checks passed".to_string(),
            evidence: vec![],
            controls: vec!["TEST-1".to_string()],
            started_at: "2026-03-27T00:00:00Z".to_string(),
            completed_at: "2026-03-27T00:00:01Z".to_string(),
            duration_ms: 1000,
            platform: "linux".to_string(),
            hostname: "test-node".to_string(),
            version: "0.1.0".to_string(),
            error: None,
        }
    }

    #[test]
    fn write_creates_json_file() {
        let dir = TempDir::new().unwrap();
        let result = sample_result();
        write_result(dir.path(), &result).unwrap();

        let path = dir.path().join("test_cap.json");
        assert!(path.exists());

        let content = fs::read_to_string(&path).unwrap();
        let parsed: CapabilityResult = serde_json::from_str(&content).unwrap();
        assert_eq!(parsed.capability, "test_cap");
        assert_eq!(parsed.status, VerificationStatus::Pass);
    }

    #[test]
    fn write_cleans_up_tmp() {
        let dir = TempDir::new().unwrap();
        let result = sample_result();
        write_result(dir.path(), &result).unwrap();

        let tmp = dir.path().join("test_cap.json.tmp");
        assert!(!tmp.exists());
    }

    #[test]
    fn write_creates_results_dir() {
        let dir = TempDir::new().unwrap();
        let nested = dir.path().join("deep").join("results");
        let result = sample_result();
        write_result(&nested, &result).unwrap();

        assert!(nested.join("test_cap.json").exists());
    }
}
