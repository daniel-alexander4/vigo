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
| `dmi.product_vendor` | string | `"Gigabyte Technology Co., Ltd."` | System vendor (from `sys_vendor`) |
| `dmi.chassis_type` | string | `"3"` | Chassis type code (e.g., 3 = Desktop, 10 = Notebook) |

## Collection Method
Reads files from `/sys/class/dmi/id/`: `bios_vendor`, `bios_version`, `bios_date`, `board_vendor`, `board_name`, `product_name`, `sys_vendor`, and `chassis_type`. Empty values are omitted from the output. These files require root access on some systems.

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
  target_path: /var/lib/hobnob/hardware-info.txt
  content: |
    Product: {{ .Traits.dmi.product_name }}
    Board: {{ .Traits.dmi.board_name }}
    BIOS: {{ .Traits.dmi.bios_vendor }} {{ .Traits.dmi.bios_version }}
```

## Platform
Linux
