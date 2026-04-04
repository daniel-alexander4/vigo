
# archive

Downloads and extracts archive files from URLs. Supports tar.gz, tar.bz2, tar.xz, zip, deb, and rpm formats. Optionally verifies SHA-256 checksums and extracts specific binaries.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `url` | Yes | -- | URL to download the archive from. |
| `target_path` | Yes | -- | Directory to extract the archive into. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `binary_path` | No | -- | Name of a specific binary to extract. Searched within the extracted contents and copied to `target_path`. |
| `sha256` | No | -- | Expected SHA-256 hash of the downloaded archive. Verification fails if the hash does not match. |
| `unless` | No | -- | Guard command. If it exits 0, the archive extraction is skipped. |
| `mode` | No | `0755` | Permission mode for extracted binary files. |

## States

- **`present`** -- Download and extract the archive. If `target_path` already exists and `unless` guard passes, skip.
- **`absent`** -- Remove the `target_path` directory.

## Idempotency

The archive executor uses three idempotency mechanisms, checked in order:

1. **`unless` guard** -- if the command exits 0, skip entirely.
2. **`sha256` checksum** -- if `target_path` exists and its hash matches, skip.
3. **Package check (.deb/.rpm only)** -- the package name is extracted from the URL filename and checked via `dpkg -s` (Debian/Ubuntu) or `rpm -q` (RHEL/Fedora). If the package is already installed, the download and install are skipped.

For non-package archives, if `target_path` exists and no `sha256` is provided, the executor will re-download and overwrite.

## Examples

### Basic

```yaml
resources:
  - name: Install terraform
    type: archive
    url: https://releases.hashicorp.com/terraform/1.5.0/terraform_1.5.0_linux_amd64.zip
    target_path: /usr/local/bin
    binary_path: terraform
    sha256: "abc123..."
```

### Extract a tarball

```yaml
resources:
  - name: Extract app release
    type: archive
    url: https://releases.example.com/myapp-v2.0.tar.gz
    target_path: /opt/myapp
```

### Install a .deb package

```yaml
resources:
  - name: Install Waveterm
    type: archive
    url: "https://github.com/wavetermdev/waveterm/releases/download/v0.9.2/waveterm_0.9.2_amd64.deb"
    target_path: /tmp/waveterm-install
```

The executor checks `dpkg -s waveterm` before downloading. If the package is already installed, no work is done.

### With unless guard

```yaml
resources:
  - name: Install kubectl
    type: archive
    url: https://dl.k8s.io/release/v1.28.0/bin/linux/amd64/kubectl
    target_path: /usr/local/bin/kubectl
    unless: "kubectl version --client | grep v1.28.0"
```


### Absent

```yaml
resources:
  - name: Remove old app
    type: archive
    target_path: /opt/old-app
    url: https://example.com/placeholder
    state: absent
```

## Platform

Cross-platform.

## Notes

- Supported archive formats: `.tar.gz`/`.tgz`, `.tar.bz2`, `.tar.xz`, `.zip`, `.deb`, `.rpm`.
- The `binary_path` parameter finds a file with that name within the extracted archive and copies it to `target_path` with the specified `mode`.
- SHA-256 verification happens after download and before extraction. A mismatch produces an error.
- Archives are downloaded to a temp file, extracted, then the temp file is removed.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
