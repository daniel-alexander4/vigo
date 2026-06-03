
# source_package

Downloads and extracts archive files from URLs. Supports tar.gz, tar.bz2, tar.xz, and zip formats. Optionally verifies SHA-256 checksums, extracts specific binaries, and sets ownership.

For .deb and .rpm packages downloaded from URLs, use the [nonrepo_package](nonrepo_package.md) executor instead.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `url` | Conditional | -- | URL to download the archive from. Required unless `artifact:` is set; the two are mutually exclusive. |
| `artifact` | Conditional | -- | Curator artifact reference (ADR-024, 0.51.5+) — one of `<name>:<tag>` (implicit-namespace; the unique fleet name claim), `<puddle-name>/<name>:<tag>`, or `<artifact_id>/<name>:<tag>`. `@<version>` form also accepted. The executor resolves via curator's [Resolver](../../../howto/swarm/set-up-curator.md#read-side--server-mediated-universal), pulls + verifies the bytes, and extracts from the materialized path under `/var/lib/vigo/artifacts/`. Archive format is sniffed from magic bytes (gzip / bzip2 / xz / zip; unrecognized magic falls back to raw-binary copy). The signed `blob_sha` is the integrity guarantee, so `sha256:` alongside `artifact:` is redundant and rejected at validate time. |
| `target_path` | Yes | -- | Path to place the extracted binary or file. |
| `state` | Yes | -- | Desired state: `present` or `absent`. |
| `binary_path` | No | -- | Name of a specific binary to extract. Searched within the extracted contents and copied to `target_path`. |
| `sha256` | No | -- | Expected SHA-256 hash of the installed file at `target_path`. Verification fails if the hash does not match. Only valid with `url:` — for `artifact:`, the catalog entry's signed blob_sha is the integrity guarantee. |
| `version` | No | -- | Version string for marker-file idempotency. Stores a `.vigo-version` marker file next to `target_path`. On subsequent runs, if the marker matches, the download is skipped entirely. |
| `unless` | No | -- | Guard command. If it exits 0, the archive extraction is skipped. |
| `mode` | No | `0755` | Permission mode for extracted binary files. |
| `owner` | No | -- | Set file owner (username) on the extracted binary. Unix only. |
| `group` | No | -- | Set file group (group name) on the extracted binary. Unix only. |
| `timeout` | No | `300` | Download timeout in seconds. Passed to curl/wget. |
| `strip_components` | No | `0` | For tar archives, passes `--strip-components=N` to remove leading directory levels. For zip archives, moves contents up N levels after extraction. |

*One of `url` or `source` is required.

## States

- **`present`** -- Download and extract the archive. If idempotency checks pass, skip.
- **`absent`** -- Remove the `target_path` file and its `.vigo-version` marker (if present).

## Idempotency

The source_package executor uses three idempotency mechanisms, checked in order:

1. **`version` marker** -- if `<target_path>.vigo-version` exists and its content matches the `version` parameter, skip entirely. This is the fastest check and runs before all others.
2. **`unless` guard** -- if the command exits 0, skip entirely.
3. **`sha256` checksum** -- if `target_path` exists and its hash matches, skip.

For archives without `version`, `unless`, or `sha256`, if `target_path` exists the executor will re-download and overwrite.

## Disk Pre-flight

Before downloading, the executor checks available disk space on the target filesystem using `statvfs`. If less than 100 MB is available, it returns an error immediately. This check uses the target path's filesystem, not `/tmp`.

## Examples

### Basic with version pinning

```yaml
resources:
  - name: Install consul
    type: source_package
    url: https://releases.hashicorp.com/consul/1.5.0/consul_1.5.0_linux_amd64.zip
    target_path: /usr/local/bin/consul
    binary_path: consul
    version: "1.5.0"
```

On the first run, the archive is downloaded, extracted, and a marker file `/usr/local/bin/consul.vigo-version` is created containing `1.5.0`. On subsequent runs, the marker file is checked first -- if it already says `1.5.0`, the entire download is skipped.

### With SHA-256 verification

```yaml
resources:
  - name: Install terraform
    type: source_package
    url: https://releases.hashicorp.com/terraform/1.5.0/terraform_1.5.0_linux_amd64.zip
    target_path: /usr/local/bin/terraform
    binary_path: terraform
    sha256: "abc123..."
```

### With ownership

```yaml
resources:
  - name: Install node_exporter
    type: source_package
    url: https://github.com/prometheus/node_exporter/releases/download/v1.7.0/node_exporter-1.7.0.linux-amd64.tar.gz
    target_path: /usr/local/bin/node_exporter
    binary_path: node_exporter
    version: "1.7.0"
    owner: prometheus
    group: prometheus
    strip_components: 1
```

### With strip_components

Many upstream tarballs wrap everything in a top-level directory (e.g., `node_exporter-1.7.0.linux-amd64/`). Use `strip_components: 1` to flatten that away during extraction.

```yaml
resources:
  - name: Extract app release
    type: source_package
    url: https://releases.example.com/myapp-v2.0.tar.gz
    target_path: /usr/local/bin/myapp
    binary_path: myapp
    strip_components: 1
```

### With custom timeout

```yaml
resources:
  - name: Install large binary
    type: source_package
    url: https://releases.example.com/large-tool-v1.0.tar.gz
    target_path: /usr/local/bin/large-tool
    binary_path: large-tool
    timeout: 600
    version: "1.0"
```

### With unless guard

```yaml
resources:
  - name: Install kubectl
    type: source_package
    url: https://dl.k8s.io/release/v1.28.0/bin/linux/amd64/kubectl
    target_path: /usr/local/bin/kubectl
    unless: "kubectl version --client | grep v1.28.0"
```

The `version` parameter is usually a better alternative to hand-written `unless` guards for version-pinned binaries.

### Absent

```yaml
resources:
  - name: Remove old app
    type: source_package
    target_path: /opt/old-app/mybin
    url: https://example.com/placeholder
    state: absent
```

Removes both the target file and its `.vigo-version` marker if present.

## Platform

Cross-platform. The `owner` and `group` parameters are Unix-only (no-op on Windows).

## Notes

- Supported archive formats: `.tar.gz`/`.tgz`, `.tar.bz2`, `.tar.xz`, `.zip`. Unknown formats are treated as raw binaries and copied directly to `target_path`.
- For `.deb` and `.rpm` packages, use the [nonrepo_package](nonrepo_package.md) executor.
- The `binary_path` parameter finds a file with that name within the extracted archive and copies it to `target_path` with the specified `mode`.
- SHA-256 verification happens after the binary is placed at `target_path`. A mismatch produces an error and no version marker is written.
- Archives are downloaded to a temp directory, extracted, then the temp directory is removed.
- The `version` marker file (`<target_path>.vigo-version`) is a plain text file containing the version string. It is removed when `state: absent`.
- `strip_components` works identically to tar's `--strip-components` flag. For zip archives, it moves the contents of the Nth-level directory up to the extraction root.
- The disk space pre-flight checks the filesystem where `target_path` lives, not `/tmp`. If the target directory doesn't exist yet, it checks the closest existing ancestor.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
