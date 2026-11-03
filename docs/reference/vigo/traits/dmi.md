
# DMI

Reads Desktop Management Interface (SMBIOS) fields describing the system's BIOS, motherboard, and chassis.

## Trait Path
`dmi`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `dmi.bios_vendor` | string | `"American Megatrends Inc."` | BIOS vendor name |
| `dmi.bios_version` | string | `"F8"` | BIOS version string |
| `dmi.bios_date` | string | `"05/17/2019"` | BIOS release date |
| `dmi.board_vendor` | string | `"Gigabyte Technology Co., Ltd."` | Motherboard vendor |
| `dmi.board_name` | string | `"Z390 AORUS PRO"` | Motherboard model name |
| `dmi.product_name` | string | `"System Product Name"` | System product name |
| `dmi.product_serial` | string | `"PF2K1ABC"` | System serial number (root-only on most systems; omitted when unreadable) |
| `dmi.product_vendor` | string | `"Gigabyte Technology Co., Ltd."` | System vendor (from `sys_vendor`) |
| `dmi.chassis_type` | string | `"3"` | Chassis type code (e.g., 3 = Desktop, 10 = Notebook) |
| `dmi.memory_devices` | array | see below | Per-DIMM physical inventory from SMBIOS type-17 (x86/UEFI only; absent on ARM/no-SMBIOS hosts) |

Each entry in `dmi.memory_devices` describes one populated memory slot:

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `locator` | string | `"DIMM_A1"` | Slot label as silk-screened on the board |
| `manufacturer` | string | `"Samsung"` | DIMM manufacturer (omitted when absent) |
| `part_number` | string | `"M471A1K43DB1-CTD"` | DIMM part number (omitted when absent) |
| `serial_number` | string | `"S5GXNX0R123456"` | DIMM serial number (omitted when absent) |
| `size_bytes` | integer | `8589934592` | Module capacity in bytes (omitted when the firmware reports it as unknown) |
| `speed_mts` | integer | `3200` | Configured speed in MT/s (omitted when unknown) |

Empty slots (size 0) are skipped. `dmi.memory_devices` carries per-DIMM **identity**; live RAM **usage** (total/used/free/swap) is the separate [`memory`](memory.md) trait, read from `/proc/meminfo`.

## Collection Method
Reads files from `/sys/class/dmi/id/`: `bios_vendor`, `bios_version`, `bios_date`, `board_vendor`, `board_name`, `product_name`, `product_serial`, `sys_vendor`, and `chassis_type`. Empty values are omitted from the output. `product_serial` (and the raw SMBIOS entries below) require root access on most systems.

Per-DIMM `memory_devices` are parsed directly from the raw SMBIOS type-17 records the kernel exposes at `/sys/firmware/dmi/entries/17-*/raw` — no `dmidecode` subprocess or dependency. On ARM boards without DMI/SMBIOS the key is simply absent, and the collector falls back to Device Tree (`/proc/device-tree/*`) for the system-level fields.

## Using in When Expressions
```yaml
- name: install-firmware-tools
  type: package
  package: fwupd
  when: "!is_container"
```

## Using in Templates
```yaml
- name: hardware-info
  type: file
  target_path: /var/lib/vigo/hardware-info.txt
  content: |
    Product: {{ .Traits.dmi.product_name }}
    Board: {{ .Traits.dmi.board_name }}
    BIOS: {{ .Traits.dmi.bios_vendor }} {{ .Traits.dmi.bios_version }}
```

## Platform
Linux
