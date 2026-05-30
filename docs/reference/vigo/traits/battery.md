
# Battery

Reports laptop battery charge level, wear health, cycle count, and status. Emitted only on hosts with at least one system battery (`BAT*`); servers and desktops report nothing.

## Trait Path
`battery`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `battery.health_pct` | number | `66.5` | Worst battery's wear health (`energy_full` ÷ `energy_full_design` × 100). The value the server classifies onto the Agent Health card |
| `battery.ac_online` | boolean | `false` | Whether an AC adapter is connected |
| `battery.batteries.<name>.capacity_pct` | integer | `73` | Current charge level (0–100) |
| `battery.batteries.<name>.health_pct` | number | `66.5` | This cell's wear health |
| `battery.batteries.<name>.status` | string | `"Discharging"` | `Charging` / `Discharging` / `Full` / `Not charging` / `Unknown` |
| `battery.batteries.<name>.cycle_count` | integer | `412` | Charge cycles (omitted when the battery doesn't report it) |
| `battery.batteries.<name>.energy_full_wh` | number | `35.9` | Present full-charge capacity in watt-hours |
| `battery.batteries.<name>.energy_full_design_wh` | number | `54.0` | Design capacity in watt-hours |
| `battery.batteries.<name>.time_to_empty_secs` | integer | `6300` | Estimated seconds until empty — present only while `Discharging` |
| `battery.batteries.<name>.time_to_full_secs` | integer | `2700` | Estimated seconds until full — present only while `Charging` |

The `batteries` keys are sysfs names (e.g. `BAT0`, `BAT1`).

`time_to_empty_secs` / `time_to_full_secs` are instantaneous-rate estimates — they jitter with system load and are display-only (they do not feed the health classification).

## Health Classification
On the per-envoy **Agent Health** card, `battery.health_pct` renders as a colored pill and rolls into the composite worst-signal score:

- **warn** below 80% of design capacity
- **fail** below 70%

A worn cell therefore raises the envoy's overall Agent Health severity, surfacing on the dashboard Agent Health card and the `/agent-health` page automatically.

## Collection Method
Walks `/sys/class/power_supply/`. Each `Battery`-type entry named `BAT*` contributes a row; `Mains`-type entries supply `ac_online`. Wear health uses `energy_full` ÷ `energy_full_design` (µWh), falling back to `charge_full` ÷ `charge_full_design` (µAh) — the ratio is unit-independent. Time-to-empty/full uses `energy_now` ÷ `power_now` (µWh ÷ µW), falling back to `charge_now` ÷ `current_now` (µAh ÷ µA); both divide to hours. Pure sysfs reads; no subprocess, no upower dependency.

## Using in Templates
```yaml
- name: battery-report
  type: file
  target_path: /var/lib/vigo/battery.txt
  content: |
    {{ range $name, $bat := .Traits.battery.batteries }}{{ $name }}: {{ $bat.capacity_pct }}% charge, {{ $bat.health_pct }}% health ({{ $bat.status }})
    {{ end }}
```

## Platform
Linux
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
