
# Kernel

Reports the Linux kernel version string, release, and hostname as seen by the kernel.

## Trait Path
`kernel`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `kernel.version` | string | `"Linux version 6.8.0-101-generic ..."` | Full kernel version string |
| `kernel.release` | string | `"6.8.0-101-generic"` | Kernel release identifier |
| `kernel.hostname` | string | `"web01"` | Kernel hostname |

## Collection Method
Reads three files from `/proc`:
- `/proc/version` for the full version string
- `/proc/sys/kernel/osrelease` for the release identifier
- `/proc/sys/kernel/hostname` for the kernel-level hostname

## Using in When Expressions
```yaml
- name: install-kernel-headers
  type: package
  package: linux-headers-generic
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: kernel-info
  type: file
  target_path: /var/lib/vigo/kernel.txt
  content: |
    Kernel: {{ .Traits.kernel.release }}
    Hostname: {{ .Traits.kernel.hostname }}
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
