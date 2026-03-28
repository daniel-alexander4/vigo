
# OS

Detects the operating system distribution, version, kernel release, architecture, and packaging family. For derivative distributions (Linux Mint, Pop!_OS, Elementary, etc.), also resolves the upstream distro and codename for third-party repository configuration.

## Trait Path
`os`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `os.family` | string | `"debian"` | Root packaging lineage (debian, rhel, suse, arch, gentoo, slackware) |
| `os.distro` | string | `"ubuntu"` | Distribution ID from `/etc/os-release` |
| `os.version` | string | `"22.04"` | Distribution version from `VERSION_ID` |
| `os.kernel` | string | `"6.8.0-101-generic"` | Kernel release string |
| `os.arch` | string | `"amd64"` | CPU architecture in Go/Docker convention (amd64, arm64, 386) |
| `os.arch_raw` | string | `"x86_64"` | CPU architecture in platform convention (x86_64, aarch64) — for download URLs |
| `os.upstream_id` | string | `"ubuntu"` | Upstream distro for repo configs (e.g., Mint → ubuntu, Rocky → rhel) |
| `os.upstream_codename` | string | `"noble"` | Upstream release codename for repo configs (e.g., Mint 22 → noble) |

## Upstream Resolution

Third-party apt repositories (Docker, Node.js, Erlang, etc.) publish packages under the upstream distro name, not the derivative. For example, Docker has packages for `ubuntu noble` but not `linuxmint wilma`. The `upstream_id` and `upstream_codename` fields resolve this automatically:

| Distro | upstream_id | upstream_codename | Source |
|--------|-------------|-------------------|--------|
| Ubuntu 24.04 | `ubuntu` | `noble` | `UBUNTU_CODENAME` |
| Linux Mint 22 | `ubuntu` | `noble` | `UBUNTU_CODENAME` |
| Linux Mint 21.3 | `ubuntu` | `jammy` | `UBUNTU_CODENAME` |
| Pop!_OS 22.04 | `ubuntu` | `jammy` | `UBUNTU_CODENAME` |
| Elementary 7.1 | `ubuntu` | `jammy` | `UBUNTU_CODENAME` |
| Zorin OS 17 | `ubuntu` | `jammy` | `UBUNTU_CODENAME` |
| LMDE 6 | `debian` | `faye` | `ID_LIKE` + `VERSION_CODENAME` |
| Debian 12 | `debian` | `bookworm` | `VERSION_CODENAME` |
| Rocky 9 | `rhel` | `Blue Onyx` | `ID_LIKE` + `VERSION_CODENAME` |
| Fedora 39 | `fedora` | (empty) | No codename |

Use these fields in repository resources instead of hardcoding distro names:

```yaml
- name: docker-repo
  type: repository
  repository: docker-ce
  key_url: "https://download.docker.com/linux/{{ .Traits.os.upstream_id }}/gpg"
  repo: "deb [signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/{{ .Traits.os.upstream_id }} {{ .Traits.os.upstream_codename }} stable"
```

## Architecture Fields

Two architecture fields are provided because download URLs use different conventions:

| Field | Convention | x86 64-bit | ARM 64-bit | Usage |
|-------|-----------|------------|------------|-------|
| `os.arch` | Go/Docker | `amd64` | `arm64` | apt repos, HashiCorp, Docker |
| `os.arch_raw` | Platform/Rust | `x86_64` | `aarch64` | GitHub releases, musl binaries |

```yaml
# HashiCorp uses Go convention (amd64)
url: "https://releases.hashicorp.com/consul/1.18.1/consul_1.18.1_linux_{{ .Traits.os.arch }}.zip"
# GitHub releases use platform convention (x86_64)
url: "https://github.com/sharkdp/bat/releases/download/v0.25.0/bat-v0.25.0-{{ .Traits.os.arch_raw }}-unknown-linux-musl.tar.gz"
```

## Collection Method
Parses `/etc/os-release` for distribution identity fields (`ID`, `ID_LIKE`, `VERSION_ID`, `VERSION_CODENAME`, `UBUNTU_CODENAME`). The `family` field is derived from `ID_LIKE` by scanning for a known root packaging lineage (debian, rhel, suse, arch, gentoo, slackware); if none match, the first `ID_LIKE` entry is used. Falls back to `ID` if `ID_LIKE` is absent. Upstream resolution checks `UBUNTU_CODENAME` first (set by all Ubuntu derivatives), then falls back to `VERSION_CODENAME` with the first `ID_LIKE` entry as the upstream ID. The kernel release is read from `/proc/sys/kernel/osrelease`. Architecture is mapped from `std::env::consts::ARCH`.

## Using in When Expressions
```yaml
- name: install-apt-transport
  type: package
  package: apt-transport-https
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: motd
  type: file
  target_path: /etc/motd
  content: |
    Distribution: {{ .Traits.os.distro }} {{ .Traits.os.version }}
    Upstream:     {{ .Traits.os.upstream_id }} {{ .Traits.os.upstream_codename }}
    Kernel:       {{ .Traits.os.kernel }}
    Architecture: {{ .Traits.os.arch }} ({{ .Traits.os.arch_raw }})
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
