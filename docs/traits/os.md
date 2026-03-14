# OS

Detects the operating system distribution, version, kernel release, architecture, and packaging family.

## Trait Path
`os`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `os.family` | string | `"debian"` | Root packaging lineage (debian, rhel, suse, arch, gentoo, slackware) |
| `os.distro` | string | `"ubuntu"` | Distribution ID from `/etc/os-release` |
| `os.version` | string | `"22.04"` | Distribution version from `VERSION_ID` |
| `os.kernel` | string | `"6.8.0-101-generic"` | Kernel release string |
| `os.arch` | string | `"amd64"` | CPU architecture (amd64, arm64, 386) |

## Collection Method
Parses `/etc/os-release` for distribution identity fields (`ID`, `ID_LIKE`, `VERSION_ID`). The `family` field is derived from `ID_LIKE` by scanning for a known root packaging lineage (debian, rhel, suse, arch, gentoo, slackware); if none match, the first `ID_LIKE` entry is used. Falls back to `ID` if `ID_LIKE` is absent. The kernel release is read from `/proc/sys/kernel/osrelease`. Architecture is mapped from `std::env::consts::ARCH`.

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
  targetpath: /etc/motd
  content: |
    Distribution: {{ .Traits.os.distro }} {{ .Traits.os.version }}
    Kernel: {{ .Traits.os.kernel }}
    Architecture: {{ .Traits.os.arch }}
```

## Platform
Linux
