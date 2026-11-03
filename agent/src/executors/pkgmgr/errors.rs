// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Package error classification — parses stderr from apt/dnf/yum to classify
//! errors into actionable categories with clear messages.

/// Classification of a package manager error.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ErrorClass {
    /// Repository is unreachable, 404, or has no Release file.
    RepoError,
    /// GPG key expired, missing, or invalid.
    GpgError,
    /// Insufficient disk space for the operation.
    DiskFull,
    /// DNS resolution failure, connection timeout, or transient network issue.
    NetworkTransient,
    /// Package dependency conflict or held package blocks the operation.
    DependencyConflict,
    /// Package or version not found in any enabled repository.
    PackageNotFound,
    /// Architecture mismatch — package unavailable for this arch.
    ArchMismatch,
    /// Permission denied — agent not running as root or sudo misconfigured.
    Permission,
    /// Corrupted package cache — checksum mismatch on downloaded package.
    CacheCorruption,
    /// dpkg lock held by another process (already handled by retry logic).
    LockContention,
    /// Error does not match any known pattern.
    Other,
}

impl ErrorClass {
    /// Returns true for infrastructure errors that should trigger the circuit
    /// breaker (the resource can't be applied regardless of config correctness).
    pub fn is_infrastructure(&self) -> bool {
        matches!(
            self,
            ErrorClass::RepoError
                | ErrorClass::GpgError
                | ErrorClass::DiskFull
        )
    }
}

/// Classify an error string from apt-get or dpkg.
pub fn classify_apt_error(stderr: &str) -> ErrorClass {
    let lower = stderr.to_lowercase();

    // Lock contention (already retried before reaching classification).
    if lower.contains("could not get lock") || lower.contains("unable to acquire") {
        return ErrorClass::LockContention;
    }

    // Disk space.
    if lower.contains("no space left on device")
        || lower.contains("you don't have enough free space")
    {
        return ErrorClass::DiskFull;
    }

    // GPG errors.
    if lower.contains("no_pubkey")
        || lower.contains("expkeysig")
        || lower.contains("the following signatures were invalid")
        || lower.contains("the following signatures couldn't be verified")
        || lower.contains("is not signed")
        || lower.contains("gpg error")
    {
        return ErrorClass::GpgError;
    }

    // Cache corruption (must be checked before repo/network — "Failed to fetch" overlap).
    if lower.contains("hash sum mismatch")
        || lower.contains("size mismatch")
        || lower.contains("failed to verify")
    {
        return ErrorClass::CacheCorruption;
    }

    // Network transient.
    if lower.contains("temporary failure resolving")
        || lower.contains("could not resolve")
        || lower.contains("could not connect")
        || lower.contains("connection timed out")
        || lower.contains("connection refused")
        || lower.contains("failed to fetch") && lower.contains("connection")
    {
        return ErrorClass::NetworkTransient;
    }

    // Repo errors (must be checked after network and cache — some overlap).
    if lower.contains("does not have a release file")
        || lower.contains("404  not found")
        || lower.contains("404 not found")
        || lower.contains("the repository") && lower.contains("no longer has a release file")
        || lower.contains("failed to fetch") && !lower.contains("connection")
    {
        return ErrorClass::RepoError;
    }

    // Architecture mismatch. Checked before PackageNotFound: it is the more
    // specific reading of the same "has no installation candidate" string, so
    // the broader test below would otherwise shadow it and this arm could
    // never be reached.
    if lower.contains("has no installation candidate")
        && (lower.contains("but it is not installable") || lower.contains("wrong architecture"))
    {
        return ErrorClass::ArchMismatch;
    }

    // Package not found.
    if lower.contains("unable to locate package")
        || lower.contains("has no installation candidate")
        || lower.contains("couldn't find any package")
        || lower.contains("no candidate ver")
    {
        return ErrorClass::PackageNotFound;
    }

    // Dependency conflicts.
    if lower.contains("depends:")
        || lower.contains("conflicts:")
        || lower.contains("breaks:")
        || lower.contains("the following packages have unmet dependencies")
        || lower.contains("held broken packages")
        || lower.contains("you have held broken packages")
    {
        return ErrorClass::DependencyConflict;
    }

    // Permission errors.
    if lower.contains("permission denied")
        || lower.contains("are you root")
        || lower.contains("operation not permitted")
    {
        return ErrorClass::Permission;
    }

    ErrorClass::Other
}

/// Classify an error string from dnf/yum.
pub fn classify_dnf_error(stderr: &str) -> ErrorClass {
    let lower = stderr.to_lowercase();

    // Disk space.
    if lower.contains("no space left on device")
        || lower.contains("insufficient space")
    {
        return ErrorClass::DiskFull;
    }

    // GPG errors.
    if lower.contains("gpg key")
        || lower.contains("public key is not installed")
        || lower.contains("signature not ok")
    {
        return ErrorClass::GpgError;
    }

    // Network transient.
    if lower.contains("could not resolve host")
        || lower.contains("failure in name resolution")
        || lower.contains("connection timed out")
        || lower.contains("connection refused")
    {
        return ErrorClass::NetworkTransient;
    }

    // Repo errors.
    if lower.contains("failed to download metadata")
        || lower.contains("cannot find a valid baseurl")
        || lower.contains("error: failed to synchronize")
        || lower.contains("repodata")
            && (lower.contains("not found") || lower.contains("error"))
    {
        return ErrorClass::RepoError;
    }

    // Package not found.
    if lower.contains("no match for argument")
        || lower.contains("no package")
        || lower.contains("nothing to do")
            && lower.contains("no packages marked for")
    {
        return ErrorClass::PackageNotFound;
    }

    // Dependency conflicts.
    if lower.contains("conflicting requests")
        || lower.contains("problem:")
            && lower.contains("requires")
        || lower.contains("depsolve")
    {
        return ErrorClass::DependencyConflict;
    }

    // Permission errors.
    if lower.contains("permission denied")
        || lower.contains("access denied")
    {
        return ErrorClass::Permission;
    }

    // Cache corruption.
    if lower.contains("checksum")
        || lower.contains("repodata is over 2 weeks old")
    {
        return ErrorClass::CacheCorruption;
    }

    ErrorClass::Other
}

/// Classify a package manager error from any supported backend.
pub fn classify_error(pkg_mgr_cmd: &str, stderr: &str) -> ErrorClass {
    match pkg_mgr_cmd {
        "apt-get" | "dpkg" => classify_apt_error(stderr),
        "dnf" | "yum" => classify_dnf_error(stderr),
        // Backend unknown — the runner's post-execution classify has the
        // resource type but not the manager the executor actually shelled out
        // to, so it passes "". Try apt patterns first (many error strings are
        // shared), then fall back to dnf rather than surrendering to `Other`:
        // a dnf-only string like "depsolve" would otherwise never classify on
        // an RPM host.
        _ => match classify_apt_error(stderr) {
            ErrorClass::Other => classify_dnf_error(stderr),
            class => class,
        },
    }
}

/// Format a clear, actionable error message for a classified package error.
pub fn format_error(
    operation: &str,
    package: &str,
    class: &ErrorClass,
    raw_stderr: &str,
) -> String {
    // Extract a concise detail from stderr (first meaningful line, max 200 chars).
    let detail = extract_detail(raw_stderr);

    match class {
        ErrorClass::RepoError => format!(
            "package {} {}: repository unavailable — {}",
            operation, package, detail,
        ),
        ErrorClass::GpgError => format!(
            "package {} {}: GPG key error — {}",
            operation, package, detail,
        ),
        ErrorClass::DiskFull => format!(
            "package {} {}: insufficient disk space",
            operation, package,
        ),
        ErrorClass::NetworkTransient => format!(
            "package {} {}: network error — {}",
            operation, package, detail,
        ),
        ErrorClass::DependencyConflict => format!(
            "package {} {}: dependency conflict — {}",
            operation, package, detail,
        ),
        ErrorClass::PackageNotFound => format!(
            "package {} {}: not found in any enabled repository",
            operation, package,
        ),
        ErrorClass::ArchMismatch => format!(
            "package {} {}: not available for this architecture — add a when: guard to restrict by architecture",
            operation, package,
        ),
        ErrorClass::Permission => format!(
            "package {} {}: insufficient permissions — agent must run as root",
            operation, package,
        ),
        ErrorClass::CacheCorruption => format!(
            "package {} {}: cache corruption — run apt-get clean or dnf clean all to clear cache",
            operation, package,
        ),
        ErrorClass::LockContention => format!(
            "package {} {}: dpkg lock held by another process",
            operation, package,
        ),
        ErrorClass::Other => format!(
            "package {} {}: {}",
            operation, package, detail,
        ),
    }
}

/// Extract the most informative line from raw stderr (skip blank lines, "WARNING:",
/// and lines that are just "E: " prefixed noise). Returns at most 200 chars.
fn extract_detail(stderr: &str) -> String {
    let meaningful = stderr
        .lines()
        .map(|l| l.trim())
        .filter(|l| !l.is_empty())
        .filter(|l| !l.starts_with("WARNING:"))
        .filter(|l| *l != "E:")
        .map(|l| l.strip_prefix("E: ").unwrap_or(l))
        .next()
        .unwrap_or(stderr.trim());

    if meaningful.len() > 200 {
        format!("{}...", &meaningful[..197])
    } else {
        meaningful.to_string()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn apt_repo_404() {
        let stderr = "E: The repository 'http://ppa.launchpad.net/foo/ppa/ubuntu jammy Release' does not have a Release file.";
        assert_eq!(classify_apt_error(stderr), ErrorClass::RepoError);
    }

    #[test]
    fn apt_gpg_expired() {
        let stderr = "W: GPG error: http://repo.example.com stable InRelease: The following signatures were invalid: EXPKEYSIG ABC123 Example <admin@example.com>";
        assert_eq!(classify_apt_error(stderr), ErrorClass::GpgError);
    }

    #[test]
    fn apt_gpg_missing() {
        let stderr = "W: GPG error: http://packages.example.com jammy InRelease: The following signatures couldn't be verified because the public key is not available: NO_PUBKEY 1234567890ABCDEF";
        assert_eq!(classify_apt_error(stderr), ErrorClass::GpgError);
    }

    #[test]
    fn apt_disk_full() {
        let stderr = "E: You don't have enough free space in /var/cache/apt/archives/.";
        assert_eq!(classify_apt_error(stderr), ErrorClass::DiskFull);
    }

    #[test]
    fn arch_mismatch_wins_over_package_not_found() {
        // Both arms match "has no installation candidate"; the arch-specific
        // reading is the more precise one and must not be shadowed.
        let stderr = "E: Package 'foo:i386' has no installation candidate but it is not installable";
        assert_eq!(classify_apt_error(stderr), ErrorClass::ArchMismatch);
    }

    #[test]
    fn plain_missing_candidate_is_package_not_found() {
        let stderr = "E: Package 'nosuchpkg' has no installation candidate";
        assert_eq!(classify_apt_error(stderr), ErrorClass::PackageNotFound);
    }

    #[test]
    fn unknown_backend_falls_through_to_dnf_patterns() {
        // The runner classifies with an empty backend name because it knows the
        // resource type, not the manager the executor shelled out to. A
        // dnf-only string must still classify rather than landing in `Other`.
        let stderr = "Error: Depsolve Error occurred: problem with installed package";
        assert_eq!(classify_apt_error(stderr), ErrorClass::Other);
        assert_eq!(classify_error("", stderr), ErrorClass::DependencyConflict);
    }

    #[test]
    fn unknown_backend_still_prefers_apt_classification() {
        let stderr = "E: You don't have enough free space in /var/cache/apt/archives/.";
        assert_eq!(classify_error("", stderr), ErrorClass::DiskFull);
    }

    #[test]
    fn apt_network_dns() {
        let stderr = "E: Failed to fetch http://archive.ubuntu.com/ubuntu/dists/jammy/InRelease  Temporary failure resolving 'archive.ubuntu.com'";
        assert_eq!(classify_apt_error(stderr), ErrorClass::NetworkTransient);
    }

    #[test]
    fn apt_not_found() {
        let stderr = "E: Unable to locate package nonexistent-pkg";
        assert_eq!(classify_apt_error(stderr), ErrorClass::PackageNotFound);
    }

    #[test]
    fn apt_dependency() {
        let stderr = "E: Unable to correct problems, you have held broken packages.";
        assert_eq!(classify_apt_error(stderr), ErrorClass::DependencyConflict);
    }

    #[test]
    fn apt_permission() {
        let stderr = "E: Could not open lock file - open (13: Permission denied)";
        assert_eq!(classify_apt_error(stderr), ErrorClass::Permission);
    }

    #[test]
    fn apt_cache_corruption() {
        let stderr = "E: Failed to fetch http://archive.ubuntu.com/ubuntu/pool/main/n/nginx/nginx_1.24.0.deb  Hash Sum mismatch";
        assert_eq!(classify_apt_error(stderr), ErrorClass::CacheCorruption);
    }

    #[test]
    fn dnf_repo_error() {
        let stderr = "Error: Failed to download metadata for repo 'custom': Cannot find a valid baseurl for repo: custom";
        assert_eq!(classify_dnf_error(stderr), ErrorClass::RepoError);
    }

    #[test]
    fn dnf_gpg_error() {
        let stderr = "warning: /var/cache/dnf/custom/packages/example-1.0.rpm: Header V4 RSA/SHA256 Signature, key ID abcdef: public key is not installed";
        assert_eq!(classify_dnf_error(stderr), ErrorClass::GpgError);
    }

    #[test]
    fn dnf_not_found() {
        let stderr = "Error: No match for argument: nonexistent-pkg";
        assert_eq!(classify_dnf_error(stderr), ErrorClass::PackageNotFound);
    }

    #[test]
    fn format_error_repo() {
        let msg = format_error(
            "install",
            "nginx",
            &ErrorClass::RepoError,
            "E: The repository 'http://example.com jammy Release' does not have a Release file.",
        );
        assert!(msg.contains("package install nginx"));
        assert!(msg.contains("repository unavailable"));
    }

    #[test]
    fn format_error_disk() {
        let msg = format_error("install", "nginx", &ErrorClass::DiskFull, "");
        assert_eq!(msg, "package install nginx: insufficient disk space");
    }

    #[test]
    fn format_error_arch() {
        let msg = format_error("install", "steam", &ErrorClass::ArchMismatch, "");
        assert!(msg.contains("when: guard"));
    }

    #[test]
    fn format_error_permission() {
        let msg = format_error("install", "nginx", &ErrorClass::Permission, "");
        assert!(msg.contains("run as root"));
    }

    #[test]
    fn format_error_cache() {
        let msg = format_error("install", "nginx", &ErrorClass::CacheCorruption, "");
        assert!(msg.contains("apt-get clean"));
    }

    #[test]
    fn detail_truncated() {
        let long = "a".repeat(300);
        let detail = extract_detail(&long);
        assert!(detail.len() <= 203); // 200 + "..."
    }
}
