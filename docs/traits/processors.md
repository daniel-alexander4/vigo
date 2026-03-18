# Processors

Provides detailed CPU information including model, vendor, core counts, and clock speed.

## Trait Path
`processors`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `processors.physical_cores` | integer | `6` | Number of physical CPU cores |
| `processors.logical_cores` | integer | `12` | Number of logical CPU cores (including hyperthreading) |
| `processors.model` | string | `"Intel(R) Core(TM) i7-9750H CPU @ 2.60GHz"` | CPU model name |
| `processors.vendor` | string | `"GenuineIntel"` | CPU vendor ID |
| `processors.mhz` | number | `2600` | CPU clock speed in MHz |

## Collection Method
Parses `/proc/cpuinfo` to count logical processors, identify unique physical/core ID pairs for physical core count, and extract the model name, vendor ID, and clock speed. Prefers the stable max frequency from `/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq` (in kHz, converted to MHz) over the fluctuating `cpu MHz` field in `/proc/cpuinfo`.

## Using in When Expressions
```yaml
- name: install-compiler
  type: package
  package: build-essential
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: cpu-info
  type: file
  target_path: /var/lib/hobnob/cpu-info.txt
  content: |
    CPU: {{ .Traits.processors.model }}
    Cores: {{ .Traits.processors.physical_cores }} physical, {{ .Traits.processors.logical_cores }} logical
    Speed: {{ .Traits.processors.mhz }} MHz
```

## Platform
Linux
