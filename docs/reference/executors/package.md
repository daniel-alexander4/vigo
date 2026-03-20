
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

## Notes

- Comma-separated package names are installed individually, stopping on the first error.
- APT backend includes smart repair: detects broken (half-configured) packages and runs `dpkg --configure` before falling back to `apt-get install`.
- `DEBIAN_FRONTEND=noninteractive` is set for APT operations.
- If no supported package manager is detected, the executor logs a warning and all `package` resources will fail with an error.
