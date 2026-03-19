# Packages (Windows)

Enumerates installed software packages using Chocolatey or winget.

## Trait Path
`packages`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `packages.packages` | object | `{"7zip": "23.01", "git": "2.43.0"}` | Map of package name to installed version |
| `packages.count` | integer | `42` | Number of installed packages |
| `packages.manager` | string | `"chocolatey"` | Package manager used (chocolatey, winget, or none) |

## Collection Method
Tries package managers in order of priority:
1. **Chocolatey** -- runs `choco list -l` and parses the "name version" output (skipping Chocolatey header and summary lines)
2. **winget** -- runs `winget list --disable-interactivity` and parses the tabular output by locating the separator line and "Version" column header

Returns `manager: "none"` with an empty package map if neither package manager is available.

## Using in When Expressions
```yaml
- name: install-git
  type: package
  package: git
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: package-report
  type: file
  target_path: C:\ProgramData\vigo\packages.txt
  content: |
    Package manager: {{ .Traits.packages.manager }}
    Installed: {{ .Traits.packages.count }} packages
```

## Platform
Windows
