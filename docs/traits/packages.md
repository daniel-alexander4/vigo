# Packages

Enumerates all installed system packages and their versions.

## Trait Path
`packages`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `packages.installed` | object | `{"bash": "5.1-6", "curl": "7.81.0-1"}` | Map of package name to installed version |

## Collection Method
Tries package managers in order of priority:
1. **dpkg** (Debian/Ubuntu) -- runs `dpkg-query -W -f '${Package}\t${Version}\n'`
2. **rpm** (RHEL/Fedora/SUSE) -- runs `rpm -qa --queryformat '%{NAME}\t%{VERSION}-%{RELEASE}\n'`
3. **brew** (macOS) -- runs `brew list --versions`

Returns an empty object if no supported package manager is found.

## Using in When Expressions
```yaml
- name: configure-nginx
  type: file
  targetpath: /etc/nginx/nginx.conf
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: package-report
  type: file
  targetpath: /var/lib/hobnob/package-report.txt
  content: |
    Installed packages on {{ .Traits.network.hostname }}:
    {{ range $name, $ver := .Traits.packages.installed }}{{ $name }}={{ $ver }}
    {{ end }}
```

## Platform
Linux
