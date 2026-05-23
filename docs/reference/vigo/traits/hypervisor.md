
# Hypervisor

Detects whether the host is a libvirt hypervisor — i.e., whether the `vm:` resource executor can manage KVM domains on this envoy. This is the *host-side* counterpart to the [`virtual`](virtual.md) collector, which answers the *guest-side* question "am I running inside a VM."

## Trait Path
`hypervisor`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `hypervisor.host_is_libvirt` | boolean | `true` | Whether the libvirt daemon's Unix socket is present (the host is acting as a KVM hypervisor) |

## Collection Method

Pure presence check on the libvirt system-bus Unix socket — no subprocess spawn, no version probe — keeping the collector cheap and deterministic per the hot-path doctrine. The first existing path wins:

1. `/var/run/libvirt/libvirt-sock`
2. `/run/libvirt/libvirt-sock`

If neither path exists, `host_is_libvirt` is `false`.

## Using in When Expressions

The canonical gate for the [`vm:`](../resources/vm.md) executor — only declares VM domains on hosts that can actually manage them:

```yaml
- name: app-server-vm
  type: vm
  vm_name: app01
  memory_mib: 4096
  vcpu: 2
  disk_path: /var/lib/libvirt/images/ubuntu-22.04.qcow2
  when: "host_is_libvirt"
```

## Using in Templates

```yaml
- name: hypervisor-status
  type: file
  target_path: /var/lib/vigo/hypervisor.txt
  content: |
    libvirt host: {{ .Traits.hypervisor.host_is_libvirt }}
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
