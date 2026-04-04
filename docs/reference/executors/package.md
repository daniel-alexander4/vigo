
# package

Installs, upgrades, and removes OS packages. Auto-detects the system's package manager (APT, DNF, YUM, APK, Pacman, Zypper) based on the OS family and distro.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `package` | Yes | -- | Package name. Supports comma-separated lists for multiple packages. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `version` | No | -- | Specific version to install. Format depends on the package manager (e.g., `1.2.3-1` for APT/DNF). |
| `allow_downgrade` | No | `false` | When `true`, uses downgrade-safe commands when changing to a different version. APT uses `--allow-downgrades`, DNF/YUM use `dnf downgrade`/`yum downgrade`. Other package managers fall back to a regular install. |

## States

- **`present`** -- Ensure the package is installed. If `version` is specified and the installed version differs, upgrade to the specified version.
- **`absent`** -- Remove the package.

## Idempotency

Queries the package manager to check if the package is installed and its current version. Only installs, upgrades, or removes when drift is detected.

## Package Manager Detection

The package manager is auto-detected at agent startup based on OS family:

| OS Family | Package Manager | Install Command |
|-----------|----------------|-----------------|
| Debian, Ubuntu | APT | `apt-get install -y` |
| RedHat, Fedora | DNF or YUM | `dnf install -y` / `yum install -y` |
| Alpine | APK | `apk add --no-cache` |
| Arch | Pacman | `pacman -S --noconfirm` |
| SUSE, openSUSE | Zypper | `zypper install -y` |

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

Blocked resources report `action: blocked` to the server, which maps to `diverged` convergence status — the node is not converged, but alerts are not fired repeatedly.

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
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
