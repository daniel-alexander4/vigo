
# repository

Manages OS package repository configurations. Supports APT (Debian/Ubuntu), DNF/YUM (RHEL/Fedora), APK (Alpine), Pacman (Arch), and Zypper (SUSE) backends.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `repository` | Yes | -- | Repository identifier name. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `repo` | Yes (for present) | -- | Repository URL or definition string. For APT: a deb line (e.g., `deb https://... stable main`). For DNF/YUM: the baseurl (or a full INI-formatted `.repo` file starting with `[`). For APK: the repo URL. For Pacman: the server URL. For Zypper: the repo URL. |
| `key_url` | No | -- | URL to the GPG signing key for the repository. Downloaded and imported automatically. For Pacman, this is treated as a key fingerprint for `pacman-key`. |
| `key_fingerprint` | No | -- | Expected GPG key fingerprint. After downloading the key, the executor verifies the fingerprint matches before installing. APT only. If the fingerprint does not match, the key is rejected with a clear error. |
| `enabled` | No | `true` | Whether the repository is active. When `false`, the repo config is written but disabled (APT: line commented out; DNF: `enabled=0`; APK: line commented out; Pacman: section commented out; Zypper: `zypper modifyrepo -d`). Distinct from `state: absent`, which removes the config entirely. Accepts `true`/`false`, `1`/`0`, `yes`/`no`. |
| `format` | No | `auto` | APT only. Controls the repo file format: `list` (traditional `.list` file), `deb822` (modern `.sources` file), or `auto` (auto-detect: uses `deb822` if `/etc/apt/sources.list.d/ubuntu.sources` exists, otherwise `list`). |

## States

- **`present`** -- Ensure the repository is configured. Backend-specific file locations described below.
- **`absent`** -- Remove the repository configuration.

## Content Drift Detection

The executor checks both file existence and content. If a repository config file exists but its content does not match the desired configuration, the file is updated (`Action::Updated`). This catches changes to the repo URL, enabled state, format, or any other parameter without requiring a delete-and-recreate cycle.

## Backends

### APT (Debian/Ubuntu)

- **OS family:** `debian`
- **File path:** `/etc/apt/sources.list.d/<name>.list` (list format) or `/etc/apt/sources.list.d/<name>.sources` (DEB822 format)
- **GPG keys:** Stored in `/etc/apt/keyrings/<name>.gpg` (modern signed-by path, never `apt-key`)
- **Refresh:** Runs `apt-get update -qq` after adding a repo
- **Disabled:** Comments out the deb line with `# ` prefix (list format) or adds `Enabled: no` (DEB822 format)

#### DEB822 Format

Modern APT (Ubuntu 24.04+) uses the DEB822 `.sources` format. Set `format: deb822` to use it, or leave as `auto` to auto-detect. The executor parses the `repo:` deb line into DEB822 fields:

```
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: jammy
Components: stable
Signed-By: /etc/apt/keyrings/docker-ce.gpg
```

Options from the deb line (e.g., `[arch=amd64 signed-by=...]`) are extracted into their DEB822 equivalents (`Architectures:`, `Signed-By:`).

### DNF/YUM (RHEL/Fedora/CentOS)

- **OS family:** `redhat`, `fedora`
- **File path:** `/etc/yum.repos.d/<name>.repo`
- **GPG keys:** Imported via `rpm --import`
- **Disabled:** Sets `enabled=0` in the INI file
- **Auto-format:** If `repo:` starts with `[`, assumes the user provided a full INI-formatted `.repo` file and writes it verbatim. Otherwise, generates proper INI format:

```ini
[epel]
name=epel
baseurl=https://dl.fedoraproject.org/pub/epel/9/Everything/x86_64/
enabled=1
gpgcheck=1
gpgkey=https://dl.fedoraproject.org/pub/epel/RPM-GPG-KEY-EPEL-9
```

### APK (Alpine Linux)

- **OS family:** `alpine`
- **File path:** `/etc/apk/repositories` (single file, one repo per line)
- **Add:** Appends the repo URL as a new line if not present
- **Remove:** Removes the matching line
- **Disabled:** Comments out the line with `# ` prefix
- **No GPG key handling** (Alpine uses a different trust model)
- **Refresh:** Runs `apk update` after adding

### Pacman (Arch Linux)

- **OS family:** `arch`, `archlinux`, `manjaro`
- **File path:** `/etc/pacman.conf` (section-based)
- **Add:** Appends a `[name]` + `Server = <url>` section block
- **Remove:** Removes the section block
- **Disabled:** Comments out the section block
- **GPG keys:** Uses `pacman-key --recv-keys` and `pacman-key --lsign-key` with the `key_fingerprint` (or `key_url` if it looks like a fingerprint)
- **Refresh:** Runs `pacman -Sy` after adding

### Zypper (SUSE/openSUSE)

- **OS family:** `suse`, `opensuse`
- **Add/Remove:** Uses `zypper addrepo` / `zypper removerepo` commands
- **Disabled:** Uses `zypper modifyrepo -d` after adding
- **GPG keys:** Imported via `rpm --import`
- **Refresh:** Runs `zypper refresh` after adding

## Idempotency

Checks if the repository configuration file exists and whether its content matches the desired configuration. Only writes when there is drift. Drift detection compares the full content of the on-disk file against the content that would be generated from the current parameters.

## Examples

### APT repository with GPG key

```yaml
resources:
  - name: docker-ce
    type: repository
    repository: docker-ce
    repo: "deb [arch=amd64] https://download.docker.com/linux/ubuntu jammy stable"
    key_url: https://download.docker.com/linux/ubuntu/gpg
```

### APT with DEB822 format and fingerprint verification

```yaml
resources:
  - name: docker-ce
    type: repository
    repository: docker-ce
    repo: "deb [arch=amd64] https://download.docker.com/linux/ubuntu jammy stable"
    key_url: https://download.docker.com/linux/ubuntu/gpg
    key_fingerprint: "9DC8 5822 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88"
    format: deb822
```

### DNF/YUM repository

```yaml
resources:
  - name: epel
    type: repository
    repository: epel
    repo: "https://dl.fedoraproject.org/pub/epel/9/Everything/x86_64/"
    key_url: "https://dl.fedoraproject.org/pub/epel/RPM-GPG-KEY-EPEL-9"
```

### DNF with full INI content

```yaml
resources:
  - name: custom-repo
    type: repository
    repository: custom-repo
    repo: |
      [custom-repo]
      name=Custom Repository
      baseurl=https://repo.example.com/centos/$releasever/$basearch/
      enabled=1
      gpgcheck=1
      gpgkey=https://repo.example.com/RPM-GPG-KEY
```

### Alpine APK repository

```yaml
resources:
  - name: community
    type: repository
    repository: community
    repo: "https://dl-cdn.alpinelinux.org/alpine/v3.19/community"
```

### Arch Pacman repository

```yaml
resources:
  - name: multilib
    type: repository
    repository: multilib
    repo: "https://mirror.example.com/$repo/os/$arch"
    key_fingerprint: "DEADBEEF12345678"
```

### SUSE Zypper repository

```yaml
resources:
  - name: packman
    type: repository
    repository: packman
    repo: "https://ftp.gwdg.de/pub/linux/misc/packman/suse/openSUSE_Tumbleweed/"
    key_url: "https://ftp.gwdg.de/pub/linux/misc/packman/suse/openSUSE_Tumbleweed/repodata/repomd.xml.key"
```

### Disable a repository without removing it

```yaml
resources:
  - name: testing-repo
    type: repository
    repository: testing-repo
    repo: "deb https://test.example.com/repo jammy main"
    enabled: false
```

### Remove a repository

```yaml
resources:
  - name: old-repo
    type: repository
    repository: old-repo
    state: absent
```

### With depends_on

```yaml
resources:
  - name: docker-repo
    type: repository
    repository: docker-ce
    repo: "deb [arch=amd64] https://download.docker.com/linux/ubuntu jammy stable"
    key_url: https://download.docker.com/linux/ubuntu/gpg

  - name: docker-ce
    type: package
    package: docker-ce
    depends_on: docker-repo
```

## Platform

Cross-platform (compiles everywhere) but operationally Linux-specific. The backend is selected based on the OS family reported by the agent.

## GPG Key Handling

APT repositories use the modern `signed-by` keyring path (`/etc/apt/keyrings/<name>.gpg`). The legacy `apt-key add` path is never used.

| Scenario | Behavior |
|----------|----------|
| Key URL provided | Downloaded via curl (wget fallback), dearmored to binary GPG format |
| Key fingerprint provided | After download, fingerprint is verified via `gpg --with-colons --import-options show-only --import` — rejected if mismatch |
| Key expired (`EXPKEYSIG`) | Automatically re-fetched from `key_url` if configured |
| Key missing (`NO_PUBKEY`) | Reports error with the missing fingerprint — add `key_url` to fix |
| Key URL returns 404 | Reports error with the failed URL — update `key_url` in the module |
| Key download fails | Tries curl then wget, reports clear error if both fail |

When `apt-get update` fails after adding a repository, the executor parses the error and reports a classified message. GPG and repo errors feed into the package circuit breaker (see [package executor](package.md)).

## Notes

- For APT, GPG keys are stored in `/etc/apt/keyrings/<name>.gpg` and referenced via `signed-by` in the sources file. The legacy `apt-key` path is never used.
- For DNF/YUM, GPG keys are imported via `rpm --import` and the repo file includes `gpgcheck=1` and `gpgkey=<key_url>` when a key URL is provided. When no key URL is given, `gpgcheck=0` is set.
- For Pacman, GPG keys are managed via `pacman-key` commands using the fingerprint.
- For Zypper, GPG keys are imported via `rpm --import`.
- Alpine APK does not use GPG keys directly — Alpine uses its own trust model.
- The executor auto-detects the package manager backend based on the OS family.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
