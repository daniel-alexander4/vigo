// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Archive executor — downloads and extracts archives idempotently.

use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::io::Read;
use std::time::Instant;

pub struct ArchiveExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl ArchiveExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }
}

impl Executor for ArchiveExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let url = params.get("url").cloned().unwrap_or_default();
        let target = params
            .get("targetpath")
            .cloned()
            .unwrap_or_default();

        let name = if !target.is_empty() { &target } else { &url };
        let mut result = ResourceResult::new("archive", name);

        if url.is_empty() {
            result.error = Some("archive executor: url param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }
        if target.is_empty() {
            result.error = Some("archive executor: targetpath param is required".into());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        if state == "absent" {
            if !std::path::Path::new(&target).exists() {
                result.action = Action::None;
            } else {
                result.action = Action::Deleted;
                result.changed = true;
                if !dry_run {
                    if let Err(e) = std::fs::remove_file(&target) {
                        result.error = Some(format!("remove {}: {}", target, e));
                    }
                }
            }
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let binary = params.get("binary").cloned().unwrap_or_default();
        let sha256 = params.get("sha256").cloned().unwrap_or_default();
        let unless_cmd = params.get("unless").cloned().unwrap_or_default();
        let mode = params.get("mode").map(|s| s.as_str()).unwrap_or("0755");

        // Check unless guard.
        if !unless_cmd.is_empty() {
            if let Ok(o) = self.runner.run("sh", &["-c", &unless_cmd]) {
                if o.status.success() {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        }

        // Check if file exists with correct hash.
        if std::path::Path::new(&target).exists() && !sha256.is_empty() {
            if let Ok(hash) = file_sha256(&target) {
                if hash == sha256 {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
            }
        }

        result.action = if std::path::Path::new(&target).exists() {
            Action::Updated
        } else {
            Action::Created
        };
        result.changed = true;

        if dry_run {
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Download to temp dir.
        let tmp_dir = format!("/tmp/hobnob-archive-{}", uuid::Uuid::new_v4());
        if let Err(e) = std::fs::create_dir_all(&tmp_dir) {
            result.error = Some(format!("create temp dir: {}", e));
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let filename = url.rsplit('/').next().unwrap_or("download");
        let download_path = format!("{}/{}", tmp_dir, filename);

        // Download.
        match self.runner.run("curl", &["-fsSL", "-o", &download_path, &url]) {
            Ok(o) if !o.status.success() => {
                // Try wget.
                match self.runner.run("wget", &["-qO", &download_path, &url]) {
                    Ok(o2) if !o2.status.success() => {
                        let _ = std::fs::remove_dir_all(&tmp_dir);
                        result.error = Some(format!("failed to download {}", url));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    Err(e) => {
                        let _ = std::fs::remove_dir_all(&tmp_dir);
                        result.error = Some(format!("download {}: {}", url, e));
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                    _ => {}
                }
            }
            Err(e) => {
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(format!("download {}: {}", url, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            _ => {}
        }

        // Detect format and extract.
        let extract_dir = format!("{}/extracted", tmp_dir);
        let _ = std::fs::create_dir_all(&extract_dir);

        let dl_lower = download_path.to_lowercase();
        let extract_result = if dl_lower.ends_with(".tar.gz") || dl_lower.ends_with(".tgz") {
            self.runner.run("tar", &["-xzf", &download_path, "-C", &extract_dir])
        } else if dl_lower.ends_with(".tar.bz2") {
            self.runner.run("tar", &["-xjf", &download_path, "-C", &extract_dir])
        } else if dl_lower.ends_with(".tar.xz") {
            self.runner.run("tar", &["-xJf", &download_path, "-C", &extract_dir])
        } else if dl_lower.ends_with(".zip") {
            self.runner.run("unzip", &["-o", &download_path, "-d", &extract_dir])
        } else if dl_lower.ends_with(".deb") {
            self.runner.run("dpkg", &["-i", &download_path])
        } else if dl_lower.ends_with(".rpm") {
            self.runner.run("rpm", &["-i", &download_path])
        } else {
            // Unknown format — just copy the downloaded file.
            if let Err(e) = std::fs::copy(&download_path, &target) {
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(format!("copy {} to {}: {}", download_path, target, e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            let _ = std::fs::remove_dir_all(&tmp_dir);
            set_file_mode(&target, mode);
            verify_sha256(&target, &sha256, &mut result);
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        };

        match extract_result {
            Ok(o) if !o.status.success() => {
                let stderr = String::from_utf8_lossy(&o.stderr);
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(format!("extract archive: {}", stderr));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            Err(e) => {
                let _ = std::fs::remove_dir_all(&tmp_dir);
                result.error = Some(format!("extract archive: {}", e));
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            _ => {}
        }

        // For deb/rpm, the file is installed directly.
        if dl_lower.ends_with(".deb") || dl_lower.ends_with(".rpm") {
            let _ = std::fs::remove_dir_all(&tmp_dir);
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        // Find the target binary in extracted files.
        let source_file = if !binary.is_empty() {
            find_file_recursive(&extract_dir, &binary)
        } else {
            find_first_executable(&extract_dir)
        };

        match source_file {
            Some(src) => {
                // Ensure parent dir exists.
                if let Some(parent) = std::path::Path::new(&target).parent() {
                    let _ = std::fs::create_dir_all(parent);
                }

                if let Err(e) = std::fs::copy(&src, &target) {
                    let _ = std::fs::remove_dir_all(&tmp_dir);
                    result.error = Some(format!("copy {} to {}: {}", src, target, e));
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }
                set_file_mode(&target, mode);
                verify_sha256(&target, &sha256, &mut result);
            }
            None => {
                result.error = Some("archive executor: no suitable file found in extracted archive".into());
            }
        }

        let _ = std::fs::remove_dir_all(&tmp_dir);
        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

fn file_sha256(path: &str) -> Result<String, std::io::Error> {
    let mut f = std::fs::File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = [0u8; 8192];
    loop {
        let n = f.read(&mut buf)?;
        if n == 0 {
            break;
        }
        hasher.update(&buf[..n]);
    }
    Ok(hasher
        .finalize()
        .iter()
        .map(|b| format!("{:02x}", b))
        .collect())
}

fn verify_sha256(path: &str, expected: &str, result: &mut ResourceResult) {
    if expected.is_empty() {
        return;
    }
    match file_sha256(path) {
        Ok(hash) if hash == expected => {}
        Ok(hash) => {
            result.error = Some(format!(
                "sha256 mismatch: expected {}, got {}",
                expected, hash
            ));
        }
        Err(e) => {
            result.error = Some(format!("hash {}: {}", path, e));
        }
    }
}

fn set_file_mode(path: &str, mode: &str) {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if let Ok(m) = u32::from_str_radix(mode.trim_start_matches('0'), 8) {
            let _ = std::fs::set_permissions(path, std::fs::Permissions::from_mode(m));
        }
    }
}

fn find_file_recursive(dir: &str, name: &str) -> Option<String> {
    for entry in walkdir(dir) {
        if let Some(fname) = std::path::Path::new(&entry).file_name() {
            if fname.to_string_lossy() == name {
                return Some(entry);
            }
        }
    }
    None
}

fn find_first_executable(dir: &str) -> Option<String> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        for entry in walkdir(dir) {
            if let Ok(meta) = std::fs::metadata(&entry) {
                if meta.is_file() && meta.permissions().mode() & 0o111 != 0 {
                    return Some(entry);
                }
            }
        }
    }
    #[cfg(not(unix))]
    {
        for entry in walkdir(dir) {
            if std::path::Path::new(&entry).is_file() {
                return Some(entry);
            }
        }
    }
    None
}

fn walkdir(dir: &str) -> Vec<String> {
    let mut files = Vec::new();
    if let Ok(entries) = std::fs::read_dir(dir) {
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_dir() {
                files.extend(walkdir(path.to_str().unwrap_or("")));
            } else {
                files.push(path.to_string_lossy().to_string());
            }
        }
    }
    files
}

#[cfg(test)]
#[cfg(unix)]
mod tests {
    use super::*;
    use std::os::unix::process::ExitStatusExt;
    use std::process::Output;
    use std::sync::{Arc, Mutex};

    struct MockRunner {
        responses: Mutex<Vec<std::io::Result<Output>>>,
        calls: Arc<Mutex<Vec<(String, Vec<String>)>>>,
    }

    impl MockRunner {
        fn new(responses: Vec<Output>) -> Self {
            Self {
                responses: Mutex::new(responses.into_iter().map(Ok).collect()),
                calls: Arc::new(Mutex::new(Vec::new())),
            }
        }

        fn ok_output(stdout: &str) -> Output {
            Output {
                status: std::process::ExitStatus::from_raw(0),
                stdout: stdout.as_bytes().to_vec(),
                stderr: Vec::new(),
            }
        }

        #[allow(dead_code)]
        fn fail_output(stderr: &str) -> Output {
            Output {
                status: std::process::ExitStatus::from_raw(256),
                stdout: Vec::new(),
                stderr: stderr.as_bytes().to_vec(),
            }
        }
    }

    impl CommandRunner for MockRunner {
        fn run(&self, name: &str, args: &[&str]) -> std::io::Result<Output> {
            self.calls.lock().unwrap().push((
                name.to_string(),
                args.iter().map(|s| s.to_string()).collect(),
            ));
            self.responses.lock().unwrap().remove(0)
        }

        fn run_with_stdin(
            &self,
            _: &[u8],
            name: &str,
            args: &[&str],
        ) -> std::io::Result<Output> {
            self.run(name, args)
        }
    }

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    #[test]
    fn missing_url_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = ArchiveExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(&make_params(&[]), false);
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("url param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn missing_targetpath_errors() {
        let mock = MockRunner::new(vec![]);
        let exec = ArchiveExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[("url", "https://example.com/file.tar.gz")]),
            false,
        );
        assert!(result.error.is_some());
        assert!(
            result.error.as_deref().unwrap().contains("targetpath param is required"),
            "unexpected error: {:?}",
            result.error
        );
    }

    #[test]
    fn absent_removes_file() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");
        std::fs::write(&target, b"binary content").unwrap();
        assert!(target.exists());

        let mock = MockRunner::new(vec![]);
        let exec = ArchiveExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("targetpath", target.to_str().unwrap()),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::Deleted);
        assert!(result.changed);
        assert!(result.error.is_none());
        assert!(!target.exists(), "file should have been removed");
    }

    #[test]
    fn absent_noop_when_missing() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("nonexistent");

        let mock = MockRunner::new(vec![]);
        let exec = ArchiveExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("targetpath", target.to_str().unwrap()),
                ("state", "absent"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn dry_run_does_not_download() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");

        let mock = MockRunner::new(vec![]);
        let calls = Arc::clone(&mock.calls);
        let exec = ArchiveExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("targetpath", target.to_str().unwrap()),
            ]),
            true,
        );
        assert_eq!(result.action, Action::Created);
        assert!(result.changed);
        assert!(result.error.is_none());
        // No curl/wget calls
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 0, "dry_run should not call any commands, got {:?}", *calls);
    }

    #[test]
    fn sha256_match_skips_download() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");
        let content = b"known binary content for hash test";
        std::fs::write(&target, content).unwrap();

        // Compute the actual sha256 of the content
        let hash = file_sha256(target.to_str().unwrap()).unwrap();

        let mock = MockRunner::new(vec![]);
        let exec = ArchiveExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("targetpath", target.to_str().unwrap()),
                ("sha256", &hash),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
    }

    #[test]
    fn unless_guard_skips() {
        let dir = tempfile::TempDir::new().unwrap();
        let target = dir.path().join("mybin");

        // unless command succeeds -> skip
        let mock = MockRunner::new(vec![
            MockRunner::ok_output(""),  // sh -c "unless_cmd" succeeds
        ]);
        let calls = Arc::clone(&mock.calls);
        let exec = ArchiveExecutor {
            runner: Box::new(mock),
        };
        let result = exec.execute(
            &make_params(&[
                ("url", "https://example.com/file.tar.gz"),
                ("targetpath", target.to_str().unwrap()),
                ("unless", "test -f /usr/bin/something"),
            ]),
            false,
        );
        assert_eq!(result.action, Action::None);
        assert!(!result.changed);
        assert!(result.error.is_none());
        let calls = calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].0, "sh");
    }
}
