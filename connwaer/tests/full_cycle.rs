//! Integration test: write a temp config, run a single cycle, verify JSON results appear.

use std::io::Write;
use std::process::Command;
use tempfile::TempDir;

#[test]
fn single_cycle_produces_results() {
    let dir = TempDir::new().unwrap();
    let results_dir = dir.path().join("results");
    let trends_db = dir.path().join("trends.db");
    let config_path = dir.path().join("connwaer.yaml");

    let config = format!(
        r#"
results_dir: "{results}"
trends_db: "{trends}"
license_dir: "/nonexistent"
log_level: warn

capabilities:
  time_sync:
    enabled: true
    schedule: "@daily"
  session_timeout:
    enabled: true
    schedule: "@daily"
    depends_on:
      - time_sync
"#,
        results = results_dir.display(),
        trends = trends_db.display(),
    );

    let mut f = std::fs::File::create(&config_path).unwrap();
    f.write_all(config.as_bytes()).unwrap();

    // Run the binary with --once.
    let binary = env!("CARGO_BIN_EXE_connwaer");
    let output = Command::new(binary)
        .args(["--config", config_path.to_str().unwrap(), "once"])
        .output()
        .expect("failed to execute connwaer");

    let stdout = String::from_utf8_lossy(&output.stdout);
    let stderr = String::from_utf8_lossy(&output.stderr);

    assert!(
        output.status.success(),
        "binary exited with error.\nstdout: {stdout}\nstderr: {stderr}"
    );

    // Verify result files exist.
    assert!(results_dir.exists(), "results directory should exist");

    let time_sync_result = results_dir.join("time_sync.json");
    assert!(time_sync_result.exists(), "time_sync.json should exist");

    let session_timeout_result = results_dir.join("session_timeout.json");
    assert!(session_timeout_result.exists(), "session_timeout.json should exist");

    // Verify JSON is valid and has expected fields.
    let content = std::fs::read_to_string(&time_sync_result).unwrap();
    let parsed: serde_json::Value = serde_json::from_str(&content)
        .expect("time_sync.json should be valid JSON");

    assert_eq!(parsed["capability"], "time_sync");
    assert!(parsed["status"].is_string(), "status should be a string");
    assert!(parsed["summary"].is_string(), "summary should be a string");
    assert!(parsed["platform"].is_string(), "platform should be a string");
    assert!(parsed["hostname"].is_string(), "hostname should be a string");
    assert!(parsed["version"].is_string(), "version should be a string");
    assert!(parsed["started_at"].is_string(), "started_at should be a string");
    assert!(parsed["completed_at"].is_string(), "completed_at should be a string");

    // Verify trends DB was created.
    assert!(trends_db.exists(), "trends.db should exist");
}

#[test]
fn verify_subcommand_outputs_json() {
    let dir = TempDir::new().unwrap();
    let results_dir = dir.path().join("results");
    let trends_db = dir.path().join("trends.db");
    let config_path = dir.path().join("connwaer.yaml");

    let config = format!(
        r#"
results_dir: "{results}"
trends_db: "{trends}"
license_dir: "/nonexistent"
log_level: error
"#,
        results = results_dir.display(),
        trends = trends_db.display(),
    );

    std::fs::write(&config_path, config).unwrap();

    let binary = env!("CARGO_BIN_EXE_connwaer");
    let output = Command::new(binary)
        .args(["--config", config_path.to_str().unwrap(), "verify", "time_sync"])
        .output()
        .expect("failed to execute connwaer");

    assert!(output.status.success(), "verify should succeed");

    let stdout = String::from_utf8_lossy(&output.stdout);
    let parsed: serde_json::Value = serde_json::from_str(&stdout)
        .expect("verify output should be valid JSON");

    assert_eq!(parsed["capability"], "time_sync");
    assert!(parsed["status"].is_string());
}

#[test]
fn status_subcommand_works() {
    let dir = TempDir::new().unwrap();
    let results_dir = dir.path().join("results");
    let config_path = dir.path().join("connwaer.yaml");

    let config = format!(
        r#"
results_dir: "{results}"
trends_db: "{trends}"
license_dir: "/nonexistent"
log_level: error
"#,
        results = results_dir.display(),
        trends = dir.path().join("trends.db").display(),
    );
    std::fs::write(&config_path, &config).unwrap();

    // First run once to generate results.
    let binary = env!("CARGO_BIN_EXE_connwaer");
    let _ = Command::new(binary)
        .args(["--config", config_path.to_str().unwrap(), "verify", "time_sync"])
        .output()
        .unwrap();

    // Then check status.
    let output = Command::new(binary)
        .args(["--config", config_path.to_str().unwrap(), "status"])
        .output()
        .unwrap();

    assert!(output.status.success());
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(stdout.contains("time_sync"), "status should show time_sync result");
}
