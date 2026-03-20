
# kernel_module

Loads and unloads Linux kernel modules, with optional persistence across reboots.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `module` | Yes | -- | Kernel module name (e.g., `br_netfilter`). |
| `state` | No | `present` | Desired state: `present`, `absent`, `loaded`, or `unloaded`. |
| `module_params` | No | -- | Module parameters (space-separated key=value pairs passed to `modprobe`). |
| `persist` | No | `true` | If `true`, persist the module configuration to `/etc/modules-load.d/`. |

## States

- **`present`** -- Load the module and persist (if `persist: true`). Checks both runtime and persistence.
- **`absent`** -- Unload the module and remove the persistence file.
- **`loaded`** -- Only ensure the module is loaded at runtime (no persistence check).
- **`unloaded`** -- Only ensure the module is unloaded at runtime (no persistence change).

## Idempotency

Parses `lsmod` output to check if the module is currently loaded. Checks for the existence of `/etc/modules-load.d/vigo-<module>.conf` for persistence. Module name matching normalizes hyphens to underscores.

## Examples

### Basic

```yaml
resources:
  - name: Load br_netfilter
    type: kernel_module
    module: br_netfilter
```

### With parameters

```yaml
resources:
  - name: Load vfio with IOMMU
    type: kernel_module
    module: vfio
    module_params: "enable_unsafe_noiommu_mode=1"
```

### Runtime only (no persistence)

```yaml
resources:
  - name: Load overlay module
    type: kernel_module
    module: overlay
    state: loaded
```

### Unload a module

```yaml
resources:
  - name: Remove unused module
    type: kernel_module
    module: unused_module
    state: absent
```

### With when

```yaml
resources:
  - name: Load br_netfilter
    type: kernel_module
    module: br_netfilter
    when: "!is_container"
```

## Platform

Linux only.

## Notes

- Uses `modprobe` to load and `modprobe -r` to unload modules.
- Persistence files are written to `/etc/modules-load.d/vigo-<module>.conf`.
- Module name comparison normalizes hyphens to underscores (e.g., `br-netfilter` matches `br_netfilter` in lsmod).
