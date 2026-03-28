
# Virtual

Detects whether the system is running in a virtual machine or container, and identifies the hypervisor.

## Trait Path
`virtual`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `virtual.is_virtual` | boolean | `true` | Whether the system is virtualized |
| `virtual.system` | string | `"kvm"` | Virtualization system name (empty if bare metal) |
| `virtual.role` | string | `"guest"` | Virtualization role (empty if bare metal) |

Detected systems: `xen`, `virtualbox`, `vmware`, `kvm`, `hyperv`, `docker`, `lxc`.

## Collection Method
Checks multiple sources in order:
1. `/sys/hypervisor/type` for Xen guests
2. `/sys/class/dmi/id/product_name` for VirtualBox, VMware, KVM/QEMU, and Hyper-V
3. `/sys/class/dmi/id/sys_vendor` for cloud providers (Amazon/EC2, Google, Microsoft, QEMU)
4. `/.dockerenv` file existence for Docker containers
5. `/proc/1/cgroup` contents for Docker, containerd, and LXC

Returns empty strings for `system` and `role` on bare metal, with `is_virtual` set to `false`.

## Using in When Expressions
```yaml
- name: install-vm-tools
  type: package
  package: open-vm-tools
  when: "!is_container"
```

## Using in Templates
```yaml
- name: vm-info
  type: file
  target_path: /var/lib/vigo/vm-info.txt
  content: |
    Virtual: {{ .Traits.virtual.is_virtual }}
    System: {{ .Traits.virtual.system }}
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
