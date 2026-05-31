
# vm

Manages libvirt KVM virtual machines idempotently. Defines the domain, builds a qcow2 overlay on top of the base disk, generates a NoCloud cloud-init seed (if user-data is set), and reconciles the running state. Linux hypervisor hosts only — guarded by the [`hypervisor`](../traits/hypervisor.md) trait (`when: "host_is_libvirt"`).

## Parameters

### Required

| Parameter | Description |
|-----------|-------------|
| `vm_name` | libvirt domain name. Also the idempotency key — two configcrates can't manage the same domain. |
| `memory_mib` | RAM in MiB. Positive integer. |
| `vcpu` | vCPU count. Positive integer. |
| `disk_path` *or* `artifact` | Base disk source. `disk_path:` is a local file (qcow2/raw) or `pool:volume`. `artifact:` references a [curator](../../../howto/set-up-curator.md) artifact of `kind: generic` (a published qcow2/raw disk image). Mutually exclusive. |

### Lifecycle

| Parameter | Default | Description |
|-----------|---------|-------------|
| `state` | `running` | Desired state: `running`, `stopped`, `defined` (powered off, domain exists), or `absent`. |

### Storage & networking

| Parameter | Description |
|-----------|-------------|
| `disk_size_gib` | Cap the qcow2 overlay at this size (uses `qemu-img create … <N>G`). cloud-init's `growpart` configcrate expands the partition + filesystem to fill it at first boot. Omit to let the overlay grow to the base image's max. |
| `network_bridge` | Host bridge for the single virtio NIC (e.g. `br0`, `virbr0`). Omit for a no-NIC VM. |

### Cloud-init

| Parameter | Description |
|-----------|-------------|
| `cloud_init_user_data` | Inline NoCloud user-data (full `#cloud-config` document or shell script). The agent builds a seed ISO via `mkisofs`/`genisoimage` containing `user-data` + `meta-data` and attaches it as a second virtio CD-ROM. The ISO filename hashes the user-data, so any edit forces a rebuild + domain redefine. |

## Curator-backed base disks

`artifact:` accepts the same value forms as the `artifact:` param on `container` / `nonrepo_package` / `source_package`:

| Form | Example |
|------|---------|
| Bare `<name>:<tag>` (implicit puddle namespace) | `ubuntu-22.04-cloud:1.4` |
| Bare `<name>@<version>` | `ubuntu-22.04-cloud@1.4` |
| `<puddle-name>/<name>:<tag>` | `alexander4/ubuntu-22.04-cloud:1.4` |
| `<hex-artifact-id>/<name>@<version>` | `e3810a25.../ubuntu-22.04-cloud@1.4` |

The curator resolver materializes the blob to `/var/lib/vigo/artifacts/<artifact_id>/<name>/<version>/<os>-<arch>` and the executor uses that path as the overlay's backing file. The curator-materialized file is never modified — all VM writes land in the overlay.

Publish a qcow2 disk image via `vigo swarm curator push --kind generic …`. No new curator kind is needed; `generic` is the catch-all for raw bytes.

## Lifecycle states

| `state:` | Behavior |
|----------|----------|
| `running` | Define the domain if absent; build/refresh overlay; start (or restart if structural drift). |
| `stopped` | Define the domain if absent; build/refresh overlay; ensure powered off (`virsh shutdown`). |
| `defined` | Define the domain if absent; build/refresh overlay; leave the run state where it is. |
| `absent` | `virsh destroy` (if running) + `virsh undefine --remove-all-storage`. Removes the managed overlay and seed ISO; pre-staged `disk_path:` files are kept. |

## Overlay model

```
/var/lib/vigo/artifacts/…/ubuntu-22.04-cloud/1.4/linux-amd64   ← curator-materialized base (immutable)
/var/lib/libvirt/images/web01.qcow2                            ← qcow2 overlay (backing = base)
/var/lib/libvirt/images/web01-seed-abc12345.iso                ← NoCloud cloud-init seed
```

Multiple VMs from the same base image share storage — each overlay only stores its own divergence from the base. The 8-hex suffix on the seed ISO is `sha256(user-data)[:8]` so editing the cloud-init forces a rebuild.

## Idempotency

The executor checks four axes before acting:

1. **Domain defined.** `virsh domstate <name>` returns a state if defined, errors if not.
2. **Overlay backing file matches the desired base.** `qemu-img info --output=json <overlay>` exposes the recorded `backing-filename`; a mismatch means the base changed and the overlay must be rebuilt.
3. **Seed ISO matches user-data hash.** The hash-suffixed filename is present iff the current user-data still matches.
4. **Run state matches desired.** Cheap `virsh start` / `virsh shutdown` on its own when the rest is already correct.

If axes 1–3 are all correct, a state-only fix is a single `virsh start`/`shutdown`. If any of 1–3 differ, the executor undefines, rebuilds the overlay (and seed ISO if needed), redefines, and starts.

## Examples

### Minimal — pre-staged base disk

A `file:` resource downloads the base; the `vm:` resource consumes it.

```yaml
name: vm-edge-relay

resources:
  - name: ubuntu-base
    type: file
    target_path: /var/lib/libvirt/images/ubuntu-22.04-base.img
    source: https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img
    owner: libvirt-qemu
    group: kvm
    mode: "0640"
    when: "host_is_libvirt"

  - name: edge-relay
    type: vm
    vm_name: edge-relay
    state: running
    memory_mib: 1024
    vcpu: 1
    disk_path: /var/lib/libvirt/images/ubuntu-22.04-base.img
    disk_size_gib: 10
    network_bridge: br0
    depends_on: [ubuntu-base]
    when: "host_is_libvirt"
```

### Curator-sourced fleet with cloud-init

One configcrate pinned to a curator-published base. Each VM gets an overlay + cloud-init seed; the base is fetched once and shared.

```yaml
name: vm-web-fleet

vars:
  base_image: "alexander4/ubuntu-22.04-cloud:1.4"
  memory_mib: "4096"
  vcpu: "4"
  disk_size_gib: "30"
  bridge: "br0"

resources:
  - name: web01
    type: vm
    vm_name: web01
    state: running
    memory_mib: "{{ .Vars.memory_mib }}"
    vcpu: "{{ .Vars.vcpu }}"
    artifact: "{{ .Vars.base_image }}"
    disk_size_gib: "{{ .Vars.disk_size_gib }}"
    network_bridge: "{{ .Vars.bridge }}"
    cloud_init_user_data: |
      #cloud-config
      hostname: web01
      manage_etc_hosts: true
      users:
        - name: ops
          sudo: ALL=(ALL) NOPASSWD:ALL
          ssh_authorized_keys:
            - ssh-ed25519 AAAA...
      packages:
        - nginx
      runcmd:
        - [systemctl, enable, --now, nginx]
    when: "host_is_libvirt"
```

### Powering a VM off without destroying it

```yaml
resources:
  - name: web01
    type: vm
    vm_name: web01
    state: stopped
    memory_mib: 4096
    vcpu: 4
    artifact: alexander4/ubuntu-22.04-cloud:1.4
    network_bridge: br0
```

State changes don't lose data — the overlay is preserved. Switching back to `state: running` boots it again with everything intact.

## Host requirements

- A working libvirt installation (the agent looks for `/var/run/libvirt/libvirt-sock` or `/run/libvirt/libvirt-sock`). The `hypervisor` trait collector flips `host_is_libvirt: true` when one is present — gate VM configcrates with `when: "host_is_libvirt"`.
- `qemu-img` on PATH (for overlay build + `qemu-img info`).
- `mkisofs` or `genisoimage` on PATH if `cloud_init_user_data` is used.
- The agent process must have permission to read the libvirt socket — typically root, or a user in the `libvirt` / `kvm` groups.

## Not supported in v1

- Multiple disks per VM (single virtio disk only).
- Multiple NICs per VM (single bridge NIC only).
- VFIO / PCI passthrough.
- vSphere / Hyper-V backends (Linux-first per the platform cadence).
- Disk snapshots & live migration.
- Copy-rather-than-overlay disk staging (overlays are the only mode).

These are clean follow-on scopes — none of the v1 architecture forecloses them.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
