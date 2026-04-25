// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! `vigo cairn source <verb>` — authoring surface for cairn sidecars.
//!
//! The CLI is the supported way to create, edit, pause, resume, and
//! remove `.cairn.yaml` files. Hand-editing the file still works —
//! the reactor doesn't care where the YAML came from — but every
//! sidecar field and every limit lives in one place (`sidecar.rs`),
//! so the CLI catches mistakes that hand-editing would let silently
//! slip past until the next reactor tick logs a parse error.
//!
//! Sub-verbs:
//!
//! ```text
//! vigo cairn source add <path>       # write a new sidecar
//! vigo cairn source remove <project> # delete a sidecar
//! vigo cairn source pause <project>  # set paused: true
//! vigo cairn source resume <project> # set paused: false
//! vigo cairn source show <project>   # print the sidecar
//! vigo cairn source validate <path>  # parse+validate, no write
//! vigo cairn source list             # (lives in cli.rs, uses scan_local)
//! ```
//!
//! Design notes:
//! - `<project>` accepts a project name (resolved under the invoking
//!   user's `~/cairn/<project>/`) or an absolute path.
//! - `add` refuses if `.cairn.yaml` already exists; point the user at
//!   `source remove` + re-run, or `source show`/`pause` if they want
//!   lifecycle verbs instead of rewriting.
//! - `add` defaults to appending `.cairn.yaml` to `.gitignore` in the
//!   target repo so a personal sidecar doesn't leak into an upstream
//!   PR. `--no-gitignore` suppresses that.

use anyhow::{anyhow, bail, Context, Result};
use std::path::{Path, PathBuf};

use super::sidecar::{self, SidecarConfig};
use super::{MAX_MIRRORS, MIN_MIRRORS};

/// Top-level dispatch for `vigo cairn source <verb>`.
pub fn dispatch(args: &[String]) -> Result<()> {
    let sub = args.first().map(String::as_str).unwrap_or("");
    match sub {
        "" | "--help" | "-h" | "help" => {
            print_usage();
            Ok(())
        }
        "add" => cmd_add(&args[1..]),
        "remove" | "rm" => cmd_remove(&args[1..]),
        "pause" => cmd_pause(&args[1..]),
        "resume" => cmd_resume(&args[1..]),
        "show" => cmd_show(&args[1..]),
        "validate" => cmd_validate(&args[1..]),
        // `list` is dispatched by the parent cli.rs.
        other => bail!(
            "unknown `vigo cairn source` subcommand: {}\nrun `vigo cairn source --help` for usage",
            other
        ),
    }
}

fn print_usage() {
    eprintln!(
        "vigo cairn source — manage .cairn.yaml sidecars

USAGE:
    vigo cairn source <subcommand> [args]

SUBCOMMANDS:
    add <path> [opts]             create a new sidecar in a repo
    remove <project>              delete an existing sidecar
    pause <project>               set paused: true (stop publishing)
    resume <project>              set paused: false (resume publishing)
    show <project>                print a sidecar + validation state
    validate <path>               parse and validate without writing
    list                          list repos this envoy publishes

`add` options:
    --label <L>                   sidecar description label (default: project name)
    --max-mirrors <N>             max receivers to pick ({MIN_MIRRORS}..={MAX_MIRRORS})
    --tags <a,b,c>                comma-separated tag list
    --include <pattern>           hostname glob to include (repeatable)
    --exclude <pattern>           hostname glob to exclude (repeatable)
    --cross-subnet <h1,h2>        explicit cross-subnet mirror hostnames
    --description <\"text\">        human-readable description (≤ 256 chars)
    --owner <hostname>            override owner field (default: local hostname)
    --no-gitignore                don't touch .gitignore (default: append .cairn.yaml)
    --paused                      create the sidecar already paused
    --yes                         skip confirmation prompts

`<project>` can be a project name (resolved under ~<invoking user>/cairn/)
or an absolute path to a project directory.",
        MIN_MIRRORS = MIN_MIRRORS,
        MAX_MIRRORS = MAX_MIRRORS,
    );
}

// ---------- add ----------

#[cfg_attr(test, derive(Debug))]
struct AddOpts {
    path: PathBuf,
    owner: Option<String>,
    max_mirrors: Option<usize>,
    tags: Vec<String>,
    description: Option<String>,
    include: Vec<String>,
    exclude: Vec<String>,
    cross_subnet: Vec<String>,
    paused: bool,
    no_gitignore: bool,
    yes: bool,
}

fn parse_add(args: &[String]) -> Result<AddOpts> {
    let mut out = AddOpts {
        path: PathBuf::new(),
        owner: None,
        max_mirrors: None,
        tags: Vec::new(),
        description: None,
        include: Vec::new(),
        exclude: Vec::new(),
        cross_subnet: Vec::new(),
        paused: false,
        no_gitignore: false,
        yes: false,
    };
    let mut positional: Option<PathBuf> = None;
    let mut i = 0;
    while i < args.len() {
        let a = &args[i];
        match a.as_str() {
            "--owner" => {
                out.owner = Some(next_val(args, &mut i, "--owner")?);
            }
            "--max-mirrors" => {
                let v = next_val(args, &mut i, "--max-mirrors")?;
                out.max_mirrors = Some(
                    v.parse::<usize>()
                        .with_context(|| format!("parsing --max-mirrors={}", v))?,
                );
            }
            "--tags" => {
                let v = next_val(args, &mut i, "--tags")?;
                out.tags
                    .extend(v.split(',').map(|s| s.trim().to_string()).filter(|s| !s.is_empty()));
            }
            "--description" => out.description = Some(next_val(args, &mut i, "--description")?),
            "--include" => out.include.push(next_val(args, &mut i, "--include")?),
            "--exclude" => out.exclude.push(next_val(args, &mut i, "--exclude")?),
            "--cross-subnet" => {
                let v = next_val(args, &mut i, "--cross-subnet")?;
                out.cross_subnet
                    .extend(v.split(',').map(|s| s.trim().to_string()).filter(|s| !s.is_empty()));
            }
            "--paused" => {
                out.paused = true;
                i += 1;
            }
            "--no-gitignore" => {
                out.no_gitignore = true;
                i += 1;
            }
            "--yes" | "-y" => {
                out.yes = true;
                i += 1;
            }
            "--help" | "-h" => {
                print_usage();
                std::process::exit(0);
            }
            other if other.starts_with("--") => bail!("unknown flag: {}", other),
            _ => {
                if positional.is_some() {
                    bail!("unexpected extra argument: {}", a);
                }
                positional = Some(PathBuf::from(a));
                i += 1;
            }
        }
    }
    out.path = positional.ok_or_else(|| anyhow!("usage: vigo cairn source add <path> [opts]"))?;
    Ok(out)
}

fn next_val(args: &[String], i: &mut usize, flag: &str) -> Result<String> {
    let v = args
        .get(*i + 1)
        .with_context(|| format!("{} requires a value", flag))?
        .clone();
    *i += 2;
    Ok(v)
}

fn cmd_add(args: &[String]) -> Result<()> {
    let opts = parse_add(args)?;

    // Resolve the project directory: accept absolute or relative path,
    // canonicalize, enforce it's a real directory with a .git/.
    let canon = opts
        .path
        .canonicalize()
        .with_context(|| format!("resolving path: {}", opts.path.display()))?;
    if !canon.is_dir() {
        bail!("{} is not a directory", canon.display());
    }
    if !canon.join(".git").exists() {
        bail!(
            "{} does not contain a .git/ directory — cairn only mirrors git repos",
            canon.display()
        );
    }

    let sidecar_path = canon.join(".cairn.yaml");
    if sidecar_path.exists() {
        bail!(
            "{} already exists — use `vigo cairn source show` to inspect, or \
             `vigo cairn source remove` then re-run `add` to replace",
            sidecar_path.display()
        );
    }

    let owner = match opts.owner {
        Some(o) => o.trim().to_ascii_lowercase(),
        None => local_hostname()?,
    };

    let max_mirrors = opts.max_mirrors.unwrap_or(5);
    if !(MIN_MIRRORS..=MAX_MIRRORS).contains(&max_mirrors) {
        bail!(
            "--max-mirrors {} is outside the valid range {}..={}",
            max_mirrors,
            MIN_MIRRORS,
            MAX_MIRRORS
        );
    }

    // Build a validated config by serializing through the parser —
    // cheapest way to run every sidecar.rs invariant with zero
    // duplicated logic.
    let draft = SidecarConfig {
        owner,
        max_mirrors,
        tags: opts.tags,
        paused: opts.paused,
        description: opts.description.unwrap_or_default(),
        include_envoys: opts.include,
        exclude_envoys: opts.exclude,
        when: String::new(),
        cross_subnet_mirrors: opts.cross_subnet,
    };
    let yaml = sidecar::to_yaml(&draft).context("serializing draft sidecar")?;
    let validated = sidecar::parse_str(&yaml).context("draft failed sidecar validation")?;

    sidecar::save(&sidecar_path, &validated)
        .with_context(|| format!("writing {}", sidecar_path.display()))?;
    println!("\u{2713} wrote {}", sidecar_path.display());

    if !opts.no_gitignore {
        match sidecar::append_to_gitignore(&canon) {
            Ok(true) => println!("\u{2713} appended .cairn.yaml to {}/.gitignore", canon.display()),
            Ok(false) => println!("  .cairn.yaml already listed in .gitignore"),
            Err(e) => eprintln!("warning: failed to update .gitignore: {}", e),
        }
    }

    println!(
        "  reactor picks up new sources within 5 seconds — run `vigo cairn source list` \
         to confirm."
    );
    Ok(())
}

// ---------- remove ----------

fn cmd_remove(args: &[String]) -> Result<()> {
    let (project, yes) = parse_project_with_yes(args, "remove")?;
    let path = resolve_project_dir(&project)?;
    let sidecar_path = path.join(".cairn.yaml");
    if !sidecar_path.exists() {
        bail!("{} does not exist", sidecar_path.display());
    }

    if !yes && !confirm(&format!(
        "Delete {}? The project's local mirror on peers is NOT removed.",
        sidecar_path.display()
    ))? {
        println!("aborted.");
        return Ok(());
    }

    std::fs::remove_file(&sidecar_path)
        .with_context(|| format!("removing {}", sidecar_path.display()))?;
    println!("\u{2713} removed {}", sidecar_path.display());
    println!(
        "  reactor stops publishing on next tick. Receiver mirrors on other envoys \
         persist until their own `vigo cairn prune` runs."
    );
    Ok(())
}

// ---------- pause / resume ----------

fn cmd_pause(args: &[String]) -> Result<()> {
    set_paused(args, true)
}

fn cmd_resume(args: &[String]) -> Result<()> {
    set_paused(args, false)
}

fn set_paused(args: &[String], paused: bool) -> Result<()> {
    let (project, _yes) = parse_project_with_yes(args, if paused { "pause" } else { "resume" })?;
    let path = resolve_project_dir(&project)?;
    let sidecar_path = path.join(".cairn.yaml");
    let mut cfg = sidecar::load(&sidecar_path)
        .with_context(|| format!("loading {}", sidecar_path.display()))?;
    if cfg.paused == paused {
        println!(
            "  already {} — no change.",
            if paused { "paused" } else { "active" }
        );
        return Ok(());
    }
    cfg.paused = paused;
    sidecar::save(&sidecar_path, &cfg)
        .with_context(|| format!("writing {}", sidecar_path.display()))?;
    println!(
        "\u{2713} {} {}",
        if paused { "paused" } else { "resumed" },
        sidecar_path.display()
    );
    Ok(())
}

// ---------- show ----------

fn cmd_show(args: &[String]) -> Result<()> {
    let (project, _yes) = parse_project_with_yes(args, "show")?;
    let path = resolve_project_dir(&project)?;
    let sidecar_path = path.join(".cairn.yaml");
    let cfg = sidecar::load(&sidecar_path)
        .with_context(|| format!("loading {}", sidecar_path.display()))?;
    println!("project:     {}", project);
    println!("path:        {}", path.display());
    println!("sidecar:     {}", sidecar_path.display());
    println!("owner:       {}", cfg.owner);
    println!("max_mirrors: {}", cfg.max_mirrors);
    println!("paused:      {}", cfg.paused);
    if !cfg.tags.is_empty() {
        println!("tags:        {}", cfg.tags.join(", "));
    }
    if !cfg.description.is_empty() {
        println!("description: {}", cfg.description);
    }
    if !cfg.include_envoys.is_empty() {
        println!("include:     {}", cfg.include_envoys.join(", "));
    }
    if !cfg.exclude_envoys.is_empty() {
        println!("exclude:     {}", cfg.exclude_envoys.join(", "));
    }
    if !cfg.when.is_empty() {
        println!("when:        {}", cfg.when);
    }
    if !cfg.cross_subnet_mirrors.is_empty() {
        println!("cross_subnet_mirrors: {}", cfg.cross_subnet_mirrors.join(", "));
    }
    Ok(())
}

// ---------- validate ----------

fn cmd_validate(args: &[String]) -> Result<()> {
    let path = args
        .first()
        .ok_or_else(|| anyhow!("usage: vigo cairn source validate <path>"))?;
    let p = PathBuf::from(path);
    let cfg = sidecar::load(&p).with_context(|| format!("loading {}", p.display()))?;
    println!(
        "\u{2713} {} parses and validates (owner={}, max_mirrors={}, paused={})",
        p.display(),
        cfg.owner,
        cfg.max_mirrors,
        cfg.paused
    );
    Ok(())
}

// ---------- helpers ----------

fn parse_project_with_yes(args: &[String], verb: &str) -> Result<(String, bool)> {
    let mut yes = false;
    let mut positional: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        match args[i].as_str() {
            "--yes" | "-y" => {
                yes = true;
                i += 1;
            }
            "--help" | "-h" => {
                print_usage();
                std::process::exit(0);
            }
            other if other.starts_with("--") => bail!("unknown flag: {}", other),
            _ => {
                if positional.is_some() {
                    bail!("unexpected extra argument: {}", args[i]);
                }
                positional = Some(args[i].clone());
                i += 1;
            }
        }
    }
    let project = positional
        .ok_or_else(|| anyhow!("usage: vigo cairn source {} <project>", verb))?;
    Ok((project, yes))
}

/// Resolve a project argument to an absolute project directory. Accepts
/// an absolute path, a relative path, or a bare project name (resolved
/// under the invoking user's `~/cairn/<name>/`).
fn resolve_project_dir(project: &str) -> Result<PathBuf> {
    let raw = Path::new(project);
    if raw.is_absolute() || project.contains('/') {
        let canon = raw
            .canonicalize()
            .with_context(|| format!("resolving path: {}", project))?;
        if !canon.is_dir() {
            bail!("{} is not a directory", canon.display());
        }
        return Ok(canon);
    }
    // Bare name — resolve under the invoking user's ~/cairn/<name>/.
    let home = home_dir()?;
    let target = home.join("cairn").join(project);
    if !target.is_dir() {
        bail!(
            "no such project: {} (expected directory at {})",
            project,
            target.display()
        );
    }
    Ok(target.canonicalize().unwrap_or(target))
}

fn home_dir() -> Result<PathBuf> {
    if let Ok(home) = std::env::var("HOME") {
        if !home.is_empty() {
            return Ok(PathBuf::from(home));
        }
    }
    bail!("$HOME is not set — cannot resolve ~/cairn/");
}

fn local_hostname() -> Result<String> {
    let raw = std::fs::read_to_string("/etc/hostname")
        .context("reading /etc/hostname to determine owner")?;
    let trimmed = raw.trim().to_ascii_lowercase();
    if trimmed.is_empty() {
        bail!("/etc/hostname is empty — pass --owner <hostname> explicitly");
    }
    Ok(trimmed)
}

fn confirm(prompt: &str) -> Result<bool> {
    use std::io::{self, BufRead, Write};
    print!("{} [y/N] ", prompt);
    io::stdout().flush().ok();
    let stdin = io::stdin();
    let mut line = String::new();
    stdin.lock().read_line(&mut line)?;
    let trimmed = line.trim().to_ascii_lowercase();
    Ok(trimmed == "y" || trimmed == "yes")
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    fn s(v: &str) -> String {
        v.to_string()
    }

    fn args(v: &[&str]) -> Vec<String> {
        v.iter().map(|s| s.to_string()).collect()
    }

    // ---------- parse_add ----------

    #[test]
    fn parse_add_minimal() {
        let o = parse_add(&args(&["/tmp/proj"])).unwrap();
        assert_eq!(o.path, PathBuf::from("/tmp/proj"));
        assert!(o.owner.is_none());
        assert!(o.max_mirrors.is_none());
        assert!(!o.paused);
        assert!(!o.no_gitignore);
    }

    #[test]
    fn parse_add_all_flags() {
        let o = parse_add(&args(&[
            "/tmp/p",
            "--owner",
            "danlap",
            "--max-mirrors",
            "7",
            "--tags",
            "a,b, c",
            "--include",
            "*.lan",
            "--include",
            "plex",
            "--exclude",
            "guest-*",
            "--cross-subnet",
            "h1,h2",
            "--description",
            "test",
            "--paused",
            "--no-gitignore",
            "--yes",
        ]))
        .unwrap();
        assert_eq!(o.owner, Some(s("danlap")));
        assert_eq!(o.max_mirrors, Some(7));
        assert_eq!(o.tags, vec![s("a"), s("b"), s("c")]);
        assert_eq!(o.include, vec![s("*.lan"), s("plex")]);
        assert_eq!(o.exclude, vec![s("guest-*")]);
        assert_eq!(o.cross_subnet, vec![s("h1"), s("h2")]);
        assert_eq!(o.description, Some(s("test")));
        assert!(o.paused);
        assert!(o.no_gitignore);
        assert!(o.yes);
    }

    #[test]
    fn parse_add_rejects_unknown_flag() {
        let err = parse_add(&args(&["/tmp/p", "--bogus"])).unwrap_err();
        assert!(err.to_string().contains("unknown flag"));
    }

    #[test]
    fn parse_add_rejects_missing_path() {
        let err = parse_add(&args(&["--owner", "danlap"])).unwrap_err();
        assert!(err.to_string().contains("source add <path>"));
    }

    #[test]
    fn parse_add_rejects_missing_flag_value() {
        let err = parse_add(&args(&["/tmp/p", "--owner"])).unwrap_err();
        assert!(err.to_string().contains("requires a value"));
    }

    // ---------- cmd_add end-to-end ----------

    /// Build a bare repo-like directory structure (has .git/) under a
    /// tempdir and return the canonical path.
    fn make_repo(dir: &Path, name: &str) -> PathBuf {
        let repo = dir.join(name);
        std::fs::create_dir_all(repo.join(".git")).unwrap();
        repo.canonicalize().unwrap()
    }

    #[test]
    fn add_writes_sidecar_and_updates_gitignore() {
        let dir = tempdir().unwrap();
        let repo = make_repo(dir.path(), "proj");
        let repo_str = repo.to_str().unwrap().to_string();
        cmd_add(&args(&[&repo_str, "--owner", "danlap"])).unwrap();

        let sidecar_path = repo.join(".cairn.yaml");
        assert!(sidecar_path.exists());
        let cfg = sidecar::load(&sidecar_path).unwrap();
        assert_eq!(cfg.owner, "danlap");
        assert_eq!(cfg.max_mirrors, 5);
        assert!(!cfg.paused);

        let gi = std::fs::read_to_string(repo.join(".gitignore")).unwrap();
        assert!(gi.contains(".cairn.yaml"));
    }

    #[test]
    fn add_no_gitignore_flag_leaves_gitignore_alone() {
        let dir = tempdir().unwrap();
        let repo = make_repo(dir.path(), "proj");
        let repo_str = repo.to_str().unwrap().to_string();
        cmd_add(&args(&[&repo_str, "--owner", "danlap", "--no-gitignore"])).unwrap();
        assert!(!repo.join(".gitignore").exists());
    }

    #[test]
    fn add_refuses_non_git_directory() {
        let dir = tempdir().unwrap();
        let not_repo = dir.path().join("not-a-repo");
        std::fs::create_dir_all(&not_repo).unwrap();
        let p = not_repo.canonicalize().unwrap();
        let err = cmd_add(&args(&[p.to_str().unwrap(), "--owner", "danlap"])).unwrap_err();
        assert!(err.to_string().contains(".git/"));
    }

    #[test]
    fn add_refuses_if_sidecar_already_exists() {
        let dir = tempdir().unwrap();
        let repo = make_repo(dir.path(), "proj");
        let repo_str = repo.to_str().unwrap().to_string();
        cmd_add(&args(&[&repo_str, "--owner", "danlap", "--no-gitignore"])).unwrap();
        let err = cmd_add(&args(&[&repo_str, "--owner", "danlap", "--no-gitignore"])).unwrap_err();
        assert!(err.to_string().contains("already exists"));
    }

    #[test]
    fn add_rejects_out_of_range_max_mirrors() {
        let dir = tempdir().unwrap();
        let repo = make_repo(dir.path(), "proj");
        let repo_str = repo.to_str().unwrap().to_string();
        let err = cmd_add(&args(&[
            &repo_str,
            "--owner",
            "danlap",
            "--max-mirrors",
            "999",
            "--no-gitignore",
        ]))
        .unwrap_err();
        assert!(err.to_string().contains("outside the valid range"));
    }

    // ---------- pause / resume ----------

    #[test]
    fn pause_and_resume_flip_the_field() {
        let dir = tempdir().unwrap();
        let repo = make_repo(dir.path(), "proj");
        let repo_str = repo.to_str().unwrap().to_string();
        cmd_add(&args(&[&repo_str, "--owner", "danlap", "--no-gitignore"])).unwrap();

        set_paused(&args(&[&repo_str]), true).unwrap();
        let cfg = sidecar::load(&repo.join(".cairn.yaml")).unwrap();
        assert!(cfg.paused);

        set_paused(&args(&[&repo_str]), false).unwrap();
        let cfg = sidecar::load(&repo.join(".cairn.yaml")).unwrap();
        assert!(!cfg.paused);
    }

    // ---------- validate ----------

    #[test]
    fn validate_accepts_good_sidecar() {
        let dir = tempdir().unwrap();
        let path = dir.path().join(".cairn.yaml");
        std::fs::write(
            &path,
            "owner: danlap\nmax_mirrors: 3\ntags: []\npaused: false\ndescription: \"\"\n",
        )
        .unwrap();
        cmd_validate(&args(&[path.to_str().unwrap()])).unwrap();
    }

    #[test]
    fn validate_rejects_malformed() {
        let dir = tempdir().unwrap();
        let path = dir.path().join(".cairn.yaml");
        std::fs::write(&path, "this is: not: valid\n").unwrap();
        let err = cmd_validate(&args(&[path.to_str().unwrap()])).unwrap_err();
        assert!(err.to_string().contains("loading") || err.to_string().contains("parse"));
    }

    // ---------- remove ----------

    #[test]
    fn remove_deletes_sidecar_with_yes() {
        let dir = tempdir().unwrap();
        let repo = make_repo(dir.path(), "proj");
        let repo_str = repo.to_str().unwrap().to_string();
        cmd_add(&args(&[&repo_str, "--owner", "danlap", "--no-gitignore"])).unwrap();
        cmd_remove(&args(&[&repo_str, "--yes"])).unwrap();
        assert!(!repo.join(".cairn.yaml").exists());
    }

    #[test]
    fn remove_errors_when_sidecar_missing() {
        let dir = tempdir().unwrap();
        let repo = make_repo(dir.path(), "proj");
        let repo_str = repo.to_str().unwrap().to_string();
        let err = cmd_remove(&args(&[&repo_str, "--yes"])).unwrap_err();
        assert!(err.to_string().contains("does not exist"));
    }
}
