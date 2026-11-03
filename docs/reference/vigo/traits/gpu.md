
# GPU

Detects and reports graphics processing units with driver and memory details.

## Trait Path
`gpu`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `gpu.count` | integer | `1` | Number of detected GPUs |
| `gpu.devices` | array of objects | (see below) | List of GPU device details |

For NVIDIA GPUs (via nvidia-smi):

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `gpu.devices[].vendor` | string | `"nvidia"` | GPU vendor |
| `gpu.devices[].name` | string | `"NVIDIA GeForce RTX 3080"` | GPU model name |
| `gpu.devices[].driver_version` | string | `"535.129.03"` | Driver version |
| `gpu.devices[].vbios_version` | string | `"94.02.42.00.92"` | VBIOS version |
| `gpu.devices[].memory_mb` | string | `"10240"` | Total GPU memory in MB |

For DRM-detected GPUs (fallback):

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `gpu.devices[].device` | string | `"card0"` | DRM device name |
| `gpu.devices[].driver` | string | `"i915"` | Kernel driver name |

## Collection Method
Tries `nvidia-smi` first with `--query-gpu=name,driver_version,vbios_version,memory.total --format=csv,noheader,nounits`. If nvidia-smi is unavailable, falls back to scanning `/sys/class/drm/` for card devices (filtering out connector entries), reading the `DRIVER=` field from each card's `device/uevent` file.

## Using in When Expressions
```yaml
- name: install-cuda
  type: package
  package: cuda-toolkit
  when: "!os_family('debian')"
```

## Using in Templates
```yaml
- name: gpu-report
  type: file
  target_path: /var/lib/vigo/gpu.txt
  content: |
    GPUs detected: {{ .Traits.gpu.count }}
```

## Platform
Linux
