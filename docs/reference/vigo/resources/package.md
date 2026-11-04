
# package

Installs, upgrades, and removes OS packages. Auto-detects the system's package manager (APT, DNF, YUM, APK, Pacman, Zypper) based on the OS family and distro.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `package` | Yes | -- | Package name. Supports comma-separated lists for multiple packages. Use `"*"` with `version: latest` to upgrade all installed packages. |
| `state` | Yes | -- | Desired state: `present` or `absent`. |
| `version` | No | -- | Which version to hold. A specific version pins it — format depends on the package manager (e.g., `1.2.3-1` for APT/DNF). The special value `latest` keeps the package at the newest version the repository offers. Omitted: any installed version satisfies the resource. |
| `allow_downgrade` | No | `false` | When `true`, uses downgrade-safe commands when changing to a different version. APT uses `--allow-downgrades`, DNF/YUM use `dnf downgrade`/`yum downgrade`. Other package managers fall back to a regular install. |
| `purge` | No | `false` | When `true` and `state: absent`, removes config files too. APT uses `apt-get purge`, others fall back to regular remove. |
| `update_cache` | No | `false` | When `true`, updates the package cache before install/upgrade. APT runs `apt-get update -qq`, DNF/YUM run `check-update`, APK runs `apk update`, etc. |
| `lock_version` | No | `false` | When `true`, holds the package at its installed version after install/upgrade. Requires the `version` parameter (except on `state: absent`) and conflicts with `version: latest` — a hold blocks exactly the upgrades `latest` exists to perform. APT uses `apt-mark hold/unhold`, DNF uses `dnf versionlock add/delete`, Zypper uses `addlock/removelock`, Brew uses `pin/unpin`. On `state: absent`, automatically unholds before removing. |
| `post_cleanup` | No | `false` | When `true`, removes orphaned dependencies after a successful install or upgrade. APT uses `apt-get autoremove -y`, DNF/YUM use `autoremove -y`, FreeBSD pkg uses `autoremove -y`, Brew uses `cleanup`. Non-fatal — if cleanup fails, the install/upgrade still reports success. |
| `flags` | No | -- | Extra flags appended to install/upgrade/remove commands. Space-separated string (e.g., `"--no-install-recommends --fix-broken"`). Flags are passed directly to the underlying package manager command. |
| `timeout` | No | -- | Execution timeout in seconds. Reserved for future use — currently parsed and validated but not wired into execution (built-in bounds apply: 60s for status queries, 1h for install/remove/upgrade; the runner additionally reports any resource exceeding 300s). |
| `retries` | No | -- | Number of retry attempts. Reserved for future use — APT and the runner already handle retries internally. |
| `retry_delay` | No | -- | Seconds between retries. Reserved for future use. |

## States

- **`present`** -- Ensure the package is installed. Which version is the `version` parameter's axis: a specific version upgrades (or downgrades if `allow_downgrade: true`) on mismatch; `version: latest` keeps it at the newest available (see below); omitted, any installed version satisfies the resource.
- **`absent`** -- Remove the package. If `purge: true`, also removes config files.

`state: latest` was retired — declare `version: latest` instead. State is the resource's lifecycle lever (`present`/`absent`); which version to hold belongs to `version`.

## `version: latest`

Keeps the package at the latest available version. If not installed, install it. If installed, always run an upgrade to pick up the newest version from the repository. APT uses `apt-get install --only-upgrade -y`, DNF uses `dnf upgrade -y`, others use their native upgrade commands.

## Idempotency

Queries the package manager to check if the package is installed and its current version. Only installs, upgrades, or removes when drift is detected. `version: latest` always attempts an upgrade when the package is already installed (the package manager itself is idempotent if already at the latest version).

`version: latest` reports `changed` honestly: the agent snapshots the installed-package set before and after the upgrade and reports a change **only when something actually moved**. A `latest` upgrade (single package or `package: "*"`) that finds nothing to do reports `Settled` — it does not inflate `ChangedCount` or trip drift detection on a fleet that runs a routine upgrade configcrate every cycle. (If the snapshot can't be taken, the run falls back to reporting a change.)

## Package Manager Detection

The package manager is auto-detected at agent startup based on OS family:

| OS Family | Package Manager | Install Command |
|-----------|----------------|-----------------|
| Debian, Ubuntu | APT | `apt-get install -y` |
| RedHat, Fedora | DNF or YUM | `dnf install -y` / `yum install -y` |
| Alpine | APK | `apk add --no-cache` |
| Arch | Pacman | `pacman -S --noconfirm` |
| SUSE, openSUSE | Zypper | `zypper install -y` |
| FreeBSD | pkg | `pkg install -y` |
| OpenBSD | pkg_add | `pkg_add` |
| NetBSD | pkgin | `pkgin -y install` |
| macOS | Homebrew | `brew install` |
| illumos | IPS | `pkg install --accept` |

## Examples

### Basic

```yaml
resources:
  - name: Install nginx
    type: package
    package: nginx
```

### Multiple packages

```yaml
resources:
  - name: Install build tools
    type: package
    package: "build-essential,curl,git"
```

### Specific version

```yaml
resources:
  - name: Install specific nginx
    type: package
    package: nginx
    version: "1.24.0-1"
```

### Keep at latest

```yaml
resources:
  - name: Keep nginx up to date
    type: package
    package: nginx
    version: latest
    state: present
```

### Upgrade all packages

Use `package: "*"` with `version: latest` to upgrade all installed packages in a single transaction. This is the idiomatic replacement for `apt-get upgrade` / `dnf upgrade` — it routes through the package resilience system (error classification, circuit breaker, blocked outcome) so a single broken repo doesn't fail the entire upgrade.

```yaml
resources:
  - name: Upgrade all
    type: package
    package: "*"
    version: latest
    state: present
    update_cache: "true"
    post_cleanup: "true"
    flags: "--fix-missing"
    when: "os_family('debian')"
```

### Update cache before install

```yaml
resources:
  - name: Install nginx with fresh cache
    type: package
    package: nginx
    update_cache: "true"
```

### With custom flags

```yaml
resources:
  - name: Install nginx without recommends
    type: package
    package: nginx
    flags: "--no-install-recommends"
```

### Pin to a specific version

```yaml
resources:
  - name: Pin nginx to 1.24.0
    type: package
    package: nginx
    version: "1.24.0-1"
    lock_version: "true"
```

### Purge a package

```yaml
resources:
  - name: Purge nginx and config
    type: package
    package: nginx
    state: absent
    purge: "true"
```

### With depends_on

```yaml
resources:
  - name: docker-repo
    type: repository
    repository: docker-ce
    repo: "deb [arch=amd64] https://download.docker.com/linux/ubuntu jammy stable"
    key_url: https://download.docker.com/linux/ubuntu/gpg

  - name: Install Docker
    type: package
    package: docker-ce
    depends_on: docker-repo
```

### With when (platform-specific)

```yaml
resources:
  - name: Install apt-transport-https
    type: package
    package: apt-transport-https
    when: "os_family('debian')"

  - name: Install yum-utils
    type: package
    package: yum-utils
    when: "!os_family('debian')"
```

### Downgrade a package

```yaml
resources:
  - name: Pin nginx to older version
    type: package
    package: nginx
    version: "1.22.0-1"
    allow_downgrade: "true"
```

### Remove a package

```yaml
resources:
  - name: Remove telnet
    type: package
    package: telnet
    state: absent
```

### Full-featured example

```yaml
resources:
  - name: Install and pin nginx
    type: package
    package: nginx
    version: "1.24.0-1"
    state: present
    update_cache: "true"
    lock_version: "true"
    flags: "--no-install-recommends"
```

## Platform

Linux only. On Windows, the same `type: package` maps to the [package_windows](package_windows.md) executor.

## Error Handling

The package executor classifies errors from the underlying package manager and returns clear, actionable messages:

| Error Class | Action | Example Message |
|-------------|--------|-----------------|
| Broken repo (404, no Release file) | `blocked` after 3 failures | `package install nginx: repository unavailable — ...` |
| GPG key expired/missing | `blocked` after 3 failures | `package install nginx: GPG key error — EXPKEYSIG ...` |
| Insufficient disk space | `blocked` (pre-flight check) | `package install nginx: insufficient disk space on /var: 43MB free, 100MB required` |
| Network transient (DNS, timeout) | `failed` with retry | `package install nginx: network error — Temporary failure resolving ...` |
| Dependency conflict | `failed` | `package install nginx: dependency conflict — ...` |
| Package not found | `failed` | `package install nginx: not found in any enabled repository` |
| Architecture mismatch | `failed` | `package install steam: not available for this architecture — add a when: guard` |
| Permission denied | `failed` | `package install nginx: insufficient permissions — agent must run as root` |
| Cache corruption | `failed` | `package install nginx: cache corruption — run apt-get clean to clear cache` |

### Circuit Breaker

Infrastructure errors (broken repo, GPG, disk full) trigger a per-resource circuit breaker. After 3 consecutive infrastructure failures, the resource reports `blocked` and backs off to hourly retries instead of failing every convergence cycle. The breaker resets when the resource succeeds.

Blocked resources report `action: blocked` to the server, which counts each one as a failed resource — the node shows `degraded` (or `failed` at 80% or more failed or blocked resources), but alerts are not fired repeatedly.

### Disk Space Pre-flight

Before attempting a package install, the agent checks available space on `/var`. If below 100 MB, it attempts `apt-get clean` / `dnf clean packages` to free cached packages. If still insufficient after cleanup, the resource reports `blocked`.

### Network Retry

Network transient errors (DNS resolution failure, connection timeout) are retried up to 3 times with exponential backoff (5s, 10s, 20s) within the same convergence cycle before reporting `failed`.

## Notes

- Comma-separated package names are installed individually, stopping on the first error.
- APT backend includes smart repair: detects broken (half-configured) packages and runs `dpkg --configure` before falling back to `apt-get install`.
- APT lock contention is retried with exponential backoff (3s, 6s, 12s).
- `DEBIAN_FRONTEND=noninteractive` is set for APT operations.
- If no supported package manager is detected, the executor logs a warning and all `package` resources will fail with an error.
- `lock_version` requires the `version` parameter on `state: present` but not on `state: absent`, and conflicts with `version: latest` (rejected at publish).
- `purge` is only valid with `state: absent`.
- `flags` are split on whitespace and appended to the package manager command for install, upgrade, downgrade, and remove operations.
- `timeout`, `retries`, and `retry_delay` are parsed and validated but not yet wired into execution — built-in bounds and APT's internal retry logic apply.
- Every package-manager command runs at reduced CPU and I/O priority (nice + ionice) and is bounded: status queries are killed after 60s, mutations (install/remove/upgrade, including their whole process tree) after 1h. A killed command surfaces as a resource error, never as a wrong "not installed" reading.
