
# package_source_audit

The `package_source_audit` trait collector enumerates configured package-manager sources on Linux hosts (apt `sources.list` and `sources.list.d/`, yum/dnf `yum.repos.d/`) and emits per-source rows with URL, suite, components, and enabled status. Credentials embedded in source URLs (`https://user:pass@host/...`) are stripped before emission so authentication material never appears in trait payloads.

## Scanned Locations

- `/etc/apt/sources.list` (classic apt format)
- `/etc/apt/sources.list.d/*.list` (classic apt format, per-repo)
- `/etc/apt/sources.list.d/*.sources` (deb822 format)
- `/etc/yum.repos.d/*.repo` (yum/dnf INI format)

## Trait Structure

```json
{
  "package_source_audit": {
    "sources": [
      {
        "type": "apt",
        "kind": "deb",
        "file": "/etc/apt/sources.list",
        "line": 3,
        "url": "http://archive.ubuntu.com/ubuntu",
        "suite": "jammy",
        "components": "main restricted universe multiverse",
        "enabled": true
      },
      {
        "type": "yum",
        "file": "/etc/yum.repos.d/docker-ce.repo",
        "id": "docker-ce-stable",
        "name": "Docker CE Stable - $basearch",
        "url": "https://download.docker.com/linux/centos/$releasever/$basearch/stable",
        "enabled": true
      }
    ]
  }
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `sources[].type` | string | Package manager: `apt` or `yum` |
| `sources[].kind` | string | apt only: `deb` or `deb-src` |
| `sources[].file` | string | Absolute path to the file the entry was read from |
| `sources[].line` | int | apt only: 1-indexed line number within the file (0 for deb822 stanzas) |
| `sources[].id` | string | yum only: repository section id (the `[section]` header) |
| `sources[].name` | string | yum only: human-readable `name=` value |
| `sources[].url` | string | Source URL with any `user:pass@` authority stripped |
| `sources[].suite` | string | apt only: distribution suite (e.g., `jammy`, `jammy-updates`) |
| `sources[].components` | string | apt only: space-separated component list (e.g., `main universe`) |
| `sources[].enabled` | bool | Whether the source is enabled. apt entries are always enabled (commented lines are skipped); yum respects `enabled=0` |

## Platform Support

| Platform | Method |
|----------|--------|
| Linux | File reads from `/etc/apt/` and `/etc/yum.repos.d/` |
| macOS | Not supported (returns null) |
| FreeBSD | Not supported (returns null) |
| OpenBSD | Not supported (returns null) |
| NetBSD | Not supported (returns null) |
| illumos | Not supported (returns null) |
| Windows | Not supported (returns null) |

## Use Cases

Detect unauthorized apt repositories added to hosts:

```yaml
# Template fragment that lists all non-Ubuntu sources
{{range .Traits.package_source_audit.sources}}
{{if and (eq .type "apt") (not (hasPrefix .url "http://archive.ubuntu.com"))}}
- {{.file}}:{{.line}} — {{.url}}
{{end}}
{{end}}
```

Server-side allowlist filtering (configured in `server.yaml` under `compliance.approved_sources`) surfaces a computed `approved` flag per source for dashboard/report consumption.

## Security Notes

- URL userinfo (`user:password@`) is stripped before emission. The original credential material never enters the trait payload, the wire, the server, or the fleet index.
- Apt auth files (`/etc/apt/auth.conf`, `/etc/apt/auth.conf.d/`) are **not** read by this collector. Those files are left to the operator.
- Source file paths are emitted verbatim — operators relying on filesystem layout for secrecy should keep sensitive sources in files with filenames that don't leak information.

## Classification

**Stable** — source lists change infrequently. Cached across convergence cycles.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
