// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Build a signed apt / dnf / apk package-repository tree from a directory of
//! loose packages and tar it for `vigo swarm curator push` (the ADR-024
//! package-repo facade — curator slice 11, the `repo-publish` verb).
//!
//! This is orchestration of standard tools — `dpkg-scanpackages`,
//! `createrepo_c`, `apk`, `gpg`, `abuild-sign` — not a reimplementation of
//! them. The publishing host must have the toolchain matching the repo kind;
//! a missing tool yields a clear "install …" error. Deliberately thin: one
//! suite, one component, the arches present; the long tail (source packages,
//! `Contents`, multi-component) stays on the hand-built recipe.

use std::fs;
use std::path::{Path, PathBuf};
use std::time::Duration;

use anyhow::{bail, Context, Result};
use sha2::{Digest, Sha256};

use crate::executors::CommandRunner;
use crate::swarm::curator::catalog::{KIND_APK_REPO, KIND_APT_REPO, KIND_DNF_REPO};

/// Per-tool timeout. Repo-metadata generation runs in seconds to a couple of
/// minutes for a modest private repo; 5 min is generous headroom.
const TOOL_TIMEOUT: Duration = Duration::from_secs(300);

/// A built repo tarball. The temp dir is held alive by `_tmp` and cleaned when
/// the value drops — the caller must keep it in scope until `push` has read
/// `tarball`.
#[derive(Debug)]
pub struct BuiltRepo {
    pub tarball: PathBuf,
    _tmp: tempfile::TempDir,
}

/// build assembles a signed repo tree for `kind` from the packages in
/// `pkg_dir`, signs the metadata with `sign_key`, and tars it (plain,
/// uncompressed — what the package-repo facade serves). `suite`/`component`
/// apply to `apt-repo`; `apk_arch` to `apk-repo`.
pub fn build(
    runner: &dyn CommandRunner,
    kind: &str,
    pkg_dir: &Path,
    sign_key: &str,
    suite: &str,
    component: &str,
    apk_arch: &str,
) -> Result<BuiltRepo> {
    if !pkg_dir.is_dir() {
        bail!(
            "repo-publish: --dir {} is not a directory",
            pkg_dir.display()
        );
    }
    let tmp = tempfile::TempDir::new().context("creating a temp dir for the repo tree")?;
    let tree = tmp.path().join("tree");
    fs::create_dir_all(&tree)?;

    match kind {
        KIND_APT_REPO => build_apt(runner, pkg_dir, &tree, sign_key, suite, component)?,
        KIND_DNF_REPO => build_dnf(runner, pkg_dir, &tree, sign_key)?,
        KIND_APK_REPO => build_apk(runner, pkg_dir, &tree, sign_key, apk_arch)?,
        other => bail!("repo-publish: kind {other:?} is not a package-repo kind"),
    }

    let tarball = tmp.path().join("repo.tar");
    tar_tree(&tree, &tarball).context("tarring the repo tree")?;
    Ok(BuiltRepo { tarball, _tmp: tmp })
}

// ---------- apt ----------

fn build_apt(
    runner: &dyn CommandRunner,
    pkg_dir: &Path,
    tree: &Path,
    sign_key: &str,
    suite: &str,
    component: &str,
) -> Result<()> {
    let pool = tree.join("pool").join(component);
    fs::create_dir_all(&pool)?;
    copy_packages(pkg_dir, &pool, "deb")?;

    // One scan to discover the architectures present; `dpkg-scanpackages
    // --arch` then re-scans per arch (it includes `all`-arch packages in each).
    let scan = run_sh(
        runner,
        "scanning .deb packages",
        &format!("cd {} && dpkg-scanpackages pool/{}", quote(tree), component),
    )?;
    let mut arches: Vec<String> = Vec::new();
    for line in String::from_utf8_lossy(&scan).lines() {
        if let Some(a) = line.strip_prefix("Architecture: ") {
            let a = a.trim().to_string();
            if a != "all" && !arches.contains(&a) {
                arches.push(a);
            }
        }
    }
    if arches.is_empty() {
        // An `all`-only repo still needs per-arch indices for apt to look in.
        arches = vec!["amd64".into(), "arm64".into()];
    }

    let dist = tree.join("dists").join(suite);
    for arch in &arches {
        let bindir = dist.join(component).join(format!("binary-{arch}"));
        fs::create_dir_all(&bindir)?;
        let pkgs = run_sh(
            runner,
            "indexing .deb packages",
            &format!(
                "cd {} && dpkg-scanpackages --arch {} pool/{}",
                quote(tree),
                arch,
                component
            ),
        )?;
        fs::write(bindir.join("Packages"), &pkgs)?;
    }

    write_release(runner, &dist, suite, component, &arches)?;

    // Sign: InRelease (inline) + Release.gpg (detached) — both forms apt accepts.
    let release = dist.join("Release");
    gpg(
        runner,
        sign_key,
        &[
            "--clearsign",
            "-o",
            &lossy(&dist.join("InRelease")),
            &lossy(&release),
        ],
    )?;
    gpg(
        runner,
        sign_key,
        &[
            "--detach-sign",
            "--armor",
            "-o",
            &lossy(&dist.join("Release.gpg")),
            &lossy(&release),
        ],
    )?;
    Ok(())
}

/// write_release hand-writes the apt `Release` index — headers plus a `SHA256:`
/// block over every `Packages` file. Hand-written rather than wrapping
/// `apt-ftparchive release` (no conf file, fewer moving parts); `Packages`
/// itself still comes from `dpkg-scanpackages`.
fn write_release(
    runner: &dyn CommandRunner,
    dist: &Path,
    suite: &str,
    component: &str,
    arches: &[String],
) -> Result<()> {
    let mut sha = String::new();
    for arch in arches {
        let rel = format!("{component}/binary-{arch}/Packages");
        let bytes = fs::read(dist.join(&rel))
            .with_context(|| format!("reading {rel} for the Release index"))?;
        let mut h = Sha256::new();
        h.update(&bytes);
        sha.push_str(&format!(
            " {} {} {}\n",
            hex::encode(h.finalize()),
            bytes.len(),
            rel
        ));
    }
    // `date -uR` is RFC-2822 — exactly apt's Release `Date:` format.
    let date = String::from_utf8_lossy(&run_tool(runner, "stamping Release", "date", &["-uR"])?)
        .trim()
        .to_string();
    let body = format!(
        "Origin: Vigo curator\nLabel: {suite}\nSuite: {suite}\nCodename: {suite}\n\
         Components: {component}\nArchitectures: {}\nDate: {date}\nSHA256:\n{sha}",
        arches.join(" "),
    );
    fs::write(dist.join("Release"), body)?;
    Ok(())
}

// ---------- dnf ----------

fn build_dnf(
    runner: &dyn CommandRunner,
    pkg_dir: &Path,
    tree: &Path,
    sign_key: &str,
) -> Result<()> {
    copy_packages(pkg_dir, tree, "rpm")?;
    run_tool(runner, "building repodata", "createrepo_c", &[&lossy(tree)])?;
    // dnf reads repodata/repomd.xml.asc as the detached signature.
    gpg(
        runner,
        sign_key,
        &[
            "--detach-sign",
            "--armor",
            &lossy(&tree.join("repodata/repomd.xml")),
        ],
    )?;
    Ok(())
}

// ---------- apk ----------

fn build_apk(
    runner: &dyn CommandRunner,
    pkg_dir: &Path,
    tree: &Path,
    sign_key: &str,
    apk_arch: &str,
) -> Result<()> {
    let archdir = tree.join(apk_arch);
    fs::create_dir_all(&archdir)?;
    copy_packages(pkg_dir, &archdir, "apk")?;
    run_sh(
        runner,
        "building APKINDEX",
        &format!(
            "cd {} && apk index -o APKINDEX.tar.gz *.apk",
            quote(&archdir)
        ),
    )?;
    // apk repos are RSA-signed (not GPG) — `sign_key` is an abuild private key.
    run_tool(
        runner,
        "signing APKINDEX",
        "abuild-sign",
        &["-k", sign_key, &lossy(&archdir.join("APKINDEX.tar.gz"))],
    )?;
    Ok(())
}

// ---------- shared helpers ----------

/// copy_packages copies every `*.<ext>` file from `src` into `dst`, returning
/// the count. Errors if `src` holds none.
fn copy_packages(src: &Path, dst: &Path, ext: &str) -> Result<usize> {
    let mut n = 0;
    for entry in fs::read_dir(src).with_context(|| format!("reading {}", src.display()))? {
        let path = entry?.path();
        if path.extension().and_then(|e| e.to_str()) == Some(ext) {
            if let Some(name) = path.file_name() {
                fs::copy(&path, dst.join(name))
                    .with_context(|| format!("copying {}", path.display()))?;
                n += 1;
            }
        }
    }
    if n == 0 {
        bail!("repo-publish: no .{ext} files in {}", src.display());
    }
    Ok(n)
}

/// gpg runs a `gpg` signing operation under the operator's key. The signing
/// key is the operator's own gnupg key — Vigo does not manage a keystore.
fn gpg(runner: &dyn CommandRunner, sign_key: &str, op: &[&str]) -> Result<()> {
    let mut args = vec!["--batch", "--yes", "--default-key", sign_key];
    args.extend_from_slice(op);
    run_tool(runner, "signing repository metadata", "gpg", &args)?;
    Ok(())
}

/// run_tool runs an external tool with a timeout and returns its stdout. A
/// missing tool becomes an actionable "install …" error.
fn run_tool(runner: &dyn CommandRunner, what: &str, name: &str, args: &[&str]) -> Result<Vec<u8>> {
    match runner.run_niced_timeout(name, args, 0, TOOL_TIMEOUT) {
        Ok(out) if out.status.success() => Ok(out.stdout),
        Ok(out) => bail!(
            "{what}: `{name}` failed — {}",
            String::from_utf8_lossy(&out.stderr)
                .lines()
                .next()
                .unwrap_or("(no output)")
        ),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            bail!("{what}: `{name}` is not installed on this host — install the package that provides it")
        }
        Err(e) => bail!("{what}: running `{name}`: {e}"),
    }
}

/// run_sh runs a `sh -c` compound command (used where a tool needs a working
/// directory or a shell glob). Same precedent as the `repository` executor.
fn run_sh(runner: &dyn CommandRunner, what: &str, script: &str) -> Result<Vec<u8>> {
    run_tool(runner, what, "sh", &["-c", script])
}

/// tar_tree writes the directory `tree` as a plain (uncompressed) `.tar` whose
/// entries are rooted at the tree — a request for `dists/stable/InRelease`
/// reads exactly that entry. This is what the package-repo facade serves.
fn tar_tree(tree: &Path, out: &Path) -> Result<()> {
    let f = fs::File::create(out).with_context(|| format!("creating {}", out.display()))?;
    let mut ar = tar::Builder::new(f);
    append_recursive(&mut ar, tree, tree)?;
    ar.finish().context("finishing the tar archive")?;
    Ok(())
}

fn append_recursive<W: std::io::Write>(
    ar: &mut tar::Builder<W>,
    root: &Path,
    dir: &Path,
) -> Result<()> {
    let mut entries: Vec<PathBuf> = fs::read_dir(dir)?
        .filter_map(|e| e.ok().map(|e| e.path()))
        .collect();
    entries.sort(); // deterministic tar layout
    for path in entries {
        if path.is_dir() {
            append_recursive(ar, root, &path)?;
        } else {
            let rel = path
                .strip_prefix(root)
                .context("relativizing a tree path")?;
            ar.append_path_with_name(&path, rel)
                .with_context(|| format!("adding {} to the tar", rel.display()))?;
        }
    }
    Ok(())
}

fn lossy(p: &Path) -> String {
    p.to_string_lossy().into_owned()
}

/// quote single-quotes a path for embedding in a `sh -c` script.
fn quote(p: &Path) -> String {
    format!("'{}'", p.to_string_lossy().replace('\'', r"'\''"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::process::Output;
    use std::sync::Mutex;

    /// recordingRunner records every command and returns canned stdout. Tools
    /// that write files (gpg, createrepo_c, apk) are no-ops under it — the
    /// tests verify the *invocations* and the Rust-authored artifacts
    /// (Packages from stdout, Release, the tar); the produced-repo-installs
    /// check is a home-lab verify.
    struct RecordingRunner {
        calls: Mutex<Vec<String>>,
        scan_stdout: Vec<u8>,
        missing: Option<String>, // a tool name to report as not-installed
    }

    impl RecordingRunner {
        fn new(scan_stdout: &str) -> Self {
            Self {
                calls: Mutex::new(Vec::new()),
                scan_stdout: scan_stdout.as_bytes().to_vec(),
                missing: None,
            }
        }
        fn calls(&self) -> Vec<String> {
            self.calls.lock().unwrap().clone()
        }
        fn ok() -> Output {
            use std::os::unix::process::ExitStatusExt;
            Output {
                status: std::process::ExitStatus::from_raw(0),
                stdout: Vec::new(),
                stderr: Vec::new(),
            }
        }
    }

    impl CommandRunner for RecordingRunner {
        fn run(&self, name: &str, args: &[&str]) -> std::io::Result<Output> {
            if self.missing.as_deref() == Some(name) {
                return Err(std::io::Error::from(std::io::ErrorKind::NotFound));
            }
            let line = format!("{name} {}", args.join(" "));
            self.calls.lock().unwrap().push(line.clone());
            let mut out = Self::ok();
            // `dpkg-scanpackages` (run via `sh -c`) emits the Packages text.
            if line.contains("dpkg-scanpackages") {
                out.stdout = self.scan_stdout.clone();
            }
            if name == "date" {
                out.stdout = b"Fri, 22 May 2026 12:00:00 +0000\n".to_vec();
            }
            Ok(out)
        }
        fn run_with_stdin(&self, _: &[u8], name: &str, args: &[&str]) -> std::io::Result<Output> {
            self.run(name, args)
        }
        fn run_niced_timeout(
            &self,
            name: &str,
            args: &[&str],
            _: i32,
            _: Duration,
        ) -> std::io::Result<Output> {
            self.run(name, args)
        }
    }

    fn write_fixture_pkgs(dir: &Path, ext: &str, names: &[&str]) {
        for n in names {
            fs::write(dir.join(format!("{n}.{ext}")), b"PKG").unwrap();
        }
    }

    #[test]
    fn build_apt_invokes_the_toolchain_and_writes_packages() {
        let tmp = tempfile::TempDir::new().unwrap();
        let pkgs = tmp.path().join("debs");
        fs::create_dir_all(&pkgs).unwrap();
        write_fixture_pkgs(&pkgs, "deb", &["acme_1_amd64"]);

        let scan = "Package: acme\nArchitecture: amd64\nFilename: pool/main/acme_1_amd64.deb\n\n";
        let runner = RecordingRunner::new(scan);
        let built = build(
            &runner,
            KIND_APT_REPO,
            &pkgs,
            "ABCD1234",
            "stable",
            "main",
            "x86_64",
        )
        .unwrap();

        let calls = runner.calls();
        assert!(
            calls.iter().any(|c| c.contains("dpkg-scanpackages")),
            "no dpkg-scanpackages: {calls:?}"
        );
        assert!(
            calls
                .iter()
                .any(|c| c.contains("gpg") && c.contains("--clearsign")),
            "no clearsign: {calls:?}"
        );
        assert!(
            calls
                .iter()
                .any(|c| c.contains("gpg") && c.contains("--detach-sign")),
            "no detach-sign: {calls:?}"
        );

        // The tar is a plain archive carrying the apt tree at its root.
        let entries = tar_entry_names(&built.tarball);
        assert!(
            entries.iter().any(|e| e == "pool/main/acme_1_amd64.deb"),
            "{entries:?}"
        );
        assert!(
            entries
                .iter()
                .any(|e| e == "dists/stable/main/binary-amd64/Packages"),
            "{entries:?}"
        );
        assert!(
            entries.iter().any(|e| e == "dists/stable/Release"),
            "{entries:?}"
        );
    }

    #[test]
    fn build_dnf_runs_createrepo_and_signs() {
        let tmp = tempfile::TempDir::new().unwrap();
        let pkgs = tmp.path().join("rpms");
        fs::create_dir_all(&pkgs).unwrap();
        write_fixture_pkgs(&pkgs, "rpm", &["acme-1"]);

        let runner = RecordingRunner::new("");
        build(
            &runner,
            KIND_DNF_REPO,
            &pkgs,
            "ABCD1234",
            "stable",
            "main",
            "x86_64",
        )
        .unwrap();
        let calls = runner.calls();
        assert!(
            calls.iter().any(|c| c.starts_with("createrepo_c")),
            "{calls:?}"
        );
        assert!(
            calls
                .iter()
                .any(|c| c.contains("gpg") && c.contains("repomd.xml")),
            "{calls:?}"
        );
    }

    #[test]
    fn empty_package_dir_is_rejected() {
        let tmp = tempfile::TempDir::new().unwrap();
        let pkgs = tmp.path().join("empty");
        fs::create_dir_all(&pkgs).unwrap();
        let runner = RecordingRunner::new("");
        let err = build(
            &runner,
            KIND_APT_REPO,
            &pkgs,
            "K",
            "stable",
            "main",
            "x86_64",
        )
        .unwrap_err();
        assert!(err.to_string().contains("no .deb files"), "{err}");
    }

    #[test]
    fn missing_tool_is_an_actionable_error() {
        let tmp = tempfile::TempDir::new().unwrap();
        let pkgs = tmp.path().join("rpms");
        fs::create_dir_all(&pkgs).unwrap();
        write_fixture_pkgs(&pkgs, "rpm", &["acme-1"]);
        let mut runner = RecordingRunner::new("");
        runner.missing = Some("createrepo_c".into());
        let err = build(
            &runner,
            KIND_DNF_REPO,
            &pkgs,
            "K",
            "stable",
            "main",
            "x86_64",
        )
        .unwrap_err();
        assert!(err.to_string().contains("not installed"), "{err}");
    }

    fn tar_entry_names(tarball: &Path) -> Vec<String> {
        let f = fs::File::open(tarball).unwrap();
        let mut ar = tar::Archive::new(f);
        ar.entries()
            .unwrap()
            .filter_map(|e| e.ok())
            .filter_map(|e| e.path().ok().map(|p| p.to_string_lossy().into_owned()))
            .collect()
    }
}
