
# OS (Windows)

Detects the Windows edition, version, build number, and architecture.

## Trait Path
`os`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `os.family` | string | `"windows"` | Always `"windows"` |
| `os.distro` | string | `"windows"` | Always `"windows"` |
| `os.name` | string | `"Windows 11 Pro"` | Windows product name |
| `os.version` | string | `"10.0.22631"` | Version in `CurrentVersion.CurrentBuildNumber` format |
| `os.arch` | string | `"amd64"` | CPU architecture (amd64, arm64, 386, ia64) |

## Collection Method
Reads from the Windows registry at `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion`:
- `ProductName` for the Windows edition name
- `CurrentVersion` for the major version
- `CurrentBuildNumber` for the build number

Architecture is detected from the `PROCESSOR_ARCHITECTURE` environment variable, falling back to Rust's compile-time `std::env::consts::ARCH`.

## Using in When Expressions
```yaml
- name: install-defender-config
  type: file
  target_path: C:\ProgramData\vigo\defender.conf
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: system-info
  type: file
  target_path: C:\ProgramData\vigo\system-info.txt
  content: |
    OS: {{ .Traits.os.name }}
    Version: {{ .Traits.os.version }}
    Arch: {{ .Traits.os.arch }}
```

## Platform
Windows
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
