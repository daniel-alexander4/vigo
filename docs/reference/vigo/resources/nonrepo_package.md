
# nonrepo_package

Installs `.deb`, `.rpm`, `.pkg`, and `.msi` packages downloaded from URLs (not from package manager repos). Uses `dpkg -i` / `rpm -U` / `installer -pkg` / `msiexec /i` directly.

For packages available in system repositories (apt, dnf, brew, etc.), use the [package](package.md) executor. For downloading and extracting archive files (tar.gz, zip), use [source_package](source_package.md).

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `url` | Conditional | -- | Download URL for the package file. Required unless `artifact:` is set; the two are mutually exclusive. |
| `artifact` | Conditional | -- | Curator artifact reference (ADR-024, 0.51.5+) — one of `<name>:<tag>` (implicit-namespace; the unique fleet name claim), `<puddle-name>/<name>:<tag>`, or `<artifact_id>/<name>:<tag>`. `@<version>` form also accepted. The executor resolves via curator's [Resolver](../../../howto/swarm/set-up-curator.md#read-side--server-mediated-universal), pulls + verifies the bytes, and installs from the materialized path under `/var/lib/vigo/artifacts/`. The signed `blob_sha` is the integrity guarantee, so `sha256:` alongside `artifact:` is redundant and rejected at validate time. Format is sniffed from magic bytes (`.deb` / `.rpm` / `.pkg` / `.msi`). Package name defaults to the catalog entry's `name`; override with `package:` if the dpkg/rpm package name differs. |
| `package` | No | auto-detect | Package name for installed-state checks. Auto-detected from URL filename for `url:`; defaults to the artifact's catalog `name` for `artifact:`. |
| `version` | No | -- | Expected installed version. Enables upgrade detection -- if the installed version differs, the new package is downloaded and installed. |
| `state` | No | `present` | Desired state: `present`, `absent`, or `latest`. |
| `sha256` | No | -- | Expected SHA-256 hash of the downloaded file. Verified before install. Only valid with `url:` — for `artifact:`, the catalog entry's signed blob_sha is the integrity guarantee. |
| `lock_version` | No | `false` | Hold/lock the package version after install (`apt-mark hold` / `dnf versionlock`). |
| `allow_downgrade` | No | `false` | Allow installing an older version than what is currently installed (`dpkg --force-downgrade` / `rpm --oldpackage`). |
| `purge` | No | `false` | When `state: absent`, remove configuration files too (`dpkg --purge`). |
| `update_cache` | No | `false` | Update the package cache before install. Helps dependency resolution after `dpkg -i` (runs `apt-get update` / `dnf check-update`). |
| `post_cleanup` | No | `false` | Remove orphaned dependencies after successful install (`apt-get autoremove` / `dnf autoremove`). Non-fatal if cleanup fails. |
| `flags` | No | -- | Pass-through flags to the installer command. |
| `pre_install` | No | -- | Shell command to run before install. |
| `post_install` | No | -- | Shell command to run after install. |
| `headers` | No | -- | HTTP headers for authenticated downloads (newline-separated). |
| `timeout` | No | `300` | Download timeout in seconds. |
| `retries` | No | `2` | Download retry count. |
| `retry_delay` | No | `5` | Seconds between retries. |

*One of `url` or `source` is required.

## States

- **`present`** -- Install the package if not present, or upgrade if `version` is specified and differs from the installed version.
- **`absent`** -- Remove the package if installed. Add `purge: true` to also remove configuration files (Debian only).
- **`latest`** -- Always download and install (useful when the URL points to a "latest" build with no stable version number). Hash comparison via `sha256` avoids redundant installs.

## Format Detection

The package format is auto-detected from the URL file extension:

| Extension | Install command | Remove command | Platform |
|-----------|----------------|----------------|----------|
| `.deb` | `dpkg -i` | `dpkg -r` / `dpkg --purge` | Debian/Ubuntu |
| `.rpm` | `rpm -U` | `rpm -e` | RHEL/CentOS/Fedora |
| `.pkg` | `installer -pkg` | `pkgutil --forget` | macOS |
| `.msi` | `msiexec /i` | `msiexec /x` | Windows |

## Package Name Detection

If the `package` parameter is not specified, the package name is extracted from the URL filename:

- `.deb`: `name_version_arch.deb` -- splits on first `_` to get name
- `.rpm`: `name-version-release.arch.rpm` -- walks backwards past arch, release, version
- `.pkg` / `.msi`: strips extension, takes segments before the first version-like part

## Idempotency

1. Extract or use the explicit `package` name.
2. Check installed status: `dpkg -s` for .deb, `rpm -q` for .rpm, `pkgutil --pkg-info` for .pkg.
3. If installed and `version` matches the installed version, no-op.
4. If installed but `version` differs, download and upgrade.
5. `state: latest` without `version` always downloads and installs (use `sha256` to skip redundant installs).
6. `state: absent` uninstalls if present.

## Dependency Resolution

After `dpkg -i` or `rpm -U`, the executor automatically resolves unmet dependencies:

- **Debian/Ubuntu**: Runs `apt-get install -f -y` to pull in missing deps from configured repos. If dep resolution fails, the resource reports an error — the package may be left in a broken state that will be repaired on the next cycle.
- **RPM (RHEL/Fedora)**: Checks for broken deps via `rpm --verify`, then runs `dnf install -y` (or `yum install -y`) to fix them.
- **macOS/Windows**: No additional dep resolution (package managers handle deps internally).

Use `update_cache: true` to refresh the package cache before install, ensuring dependency resolution has access to current repo metadata.

## Lock Contention Retry

If `dpkg` or `rpm` reports a lock contention error (another package manager process is running), the executor retries up to 3 times with exponential backoff (3s, 6s, 12s). This matches the behavior of the regular `package` executor.

## Error Classification

Install errors from `dpkg` and `rpm` flow through the agent's existing package error classification (`classify_apt_error()` / `classify_dnf_error()`), which means broken dependency errors and disk-full conditions are reported as `blocked` rather than `failed`.

## Examples

### Install a .deb from a URL

```yaml
resources:
  - name: Install tailscale
    type: nonrepo_package
    url: https://pkgs.tailscale.com/stable/tailscale_1.56.1_amd64.deb
    version: "1.56.1"
```

### Install a .rpm with version lock

```yaml
resources:
  - name: Install datadog-agent
    type: nonrepo_package
    url: https://s3.amazonaws.com/dd-agent/datadog-agent-7.50.0-1.x86_64.rpm
    version: "7.50.0-1"
    lock_version: true
```

### Install with SHA-256 verification

```yaml
resources:
  - name: Install custom tool
    type: nonrepo_package
    url: https://internal.example.com/releases/mytool_2.0.0_amd64.deb
    sha256: "abc123..."
    version: "2.0.0"
```

### Install with pre/post hooks

```yaml
resources:
  - name: Install postgres
    type: nonrepo_package
    url: https://download.postgresql.org/pub/repos/apt/pool/main/p/postgresql-16/postgresql-16_16.1-1_amd64.deb
    pre_install: "systemctl stop postgresql || true"
    post_install: "systemctl start postgresql"
    version: "16.1-1"
```

### Install with authenticated download

```yaml
resources:
  - name: Install licensed tool
    type: nonrepo_package
    url: https://packages.example.com/enterprise/tool_3.0_amd64.deb
    headers: "Authorization: Bearer secret:vigo/packages/token"
    version: "3.0"
```

### Remove a package

```yaml
resources:
  - name: Remove old agent
    type: nonrepo_package
    url: https://example.com/old-agent_1.0_amd64.deb
    package: old-agent
    state: absent
```

### Purge a package (remove config files too)

```yaml
resources:
  - name: Purge legacy tool
    type: nonrepo_package
    url: https://example.com/legacy_1.0_amd64.deb
    package: legacy
    state: absent
    purge: true
```

### Install with dependency resolution and cleanup

```yaml
resources:
  - name: Install prometheus-node-exporter
    type: nonrepo_package
    url: https://github.com/prometheus/node_exporter/releases/download/v1.7.0/prometheus-node-exporter_1.7.0-1_amd64.deb
    version: "1.7.0-1"
    update_cache: true
    post_cleanup: true
```

## Platform

The executor compiles on all platforms. Install/remove functionality works on:
- **Linux**: `.deb` (Debian/Ubuntu) and `.rpm` (RHEL/Fedora/CentOS/SUSE)
- **macOS**: `.pkg`
- **Windows**: `.msi`

## Notes

- Download failures are **not** subject to the circuit breaker. A 404 indicates a broken configcrate definition, not a transient infrastructure issue.
- The circuit breaker **does** apply to install/remove failures via `is_package_type()`, which includes `nonrepo_package`. This means repeated dpkg/rpm failures will trip the breaker and skip subsequent package resources.
- Disk pre-flight checks on `/var` are inherited from the runner's package-type handling.
- The executor cleans up its temp download directory after install, whether the install succeeds or fails.
- `lock_version` uses `apt-mark hold` on Debian or `dnf versionlock` / `yum versionlock` on RHEL. No lock mechanism exists for macOS `.pkg` or Windows `.msi`.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
