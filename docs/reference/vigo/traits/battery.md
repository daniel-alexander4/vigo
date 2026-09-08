
# Battery

Reports laptop battery charge level, wear health, cycle count, and status. Emitted only on hosts with at least one system battery (`BAT*`); servers and desktops report nothing.

## Trait Path
`battery`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `battery.health_pct` | number | `66.5` | Worst battery's wear health (`energy_full` ÷ `energy_full_design` × 100). The value the server classifies onto the Agent Health card |
| `battery.ac_online` | boolean | `false` | Whether an AC adapter is connected |
| `battery.batteries.<name>.manufacturer` | string | `"LGC"` | Cell manufacturer (omitted when firmware doesn't report it) |
| `battery.batteries.<name>.model_name` | string | `"DELL ABC1234"` | Battery model (omitted when absent) |
| `battery.batteries.<name>.serial_number` | string | `"0001"` | Battery serial number (omitted when absent) |
| `battery.batteries.<name>.technology` | string | `"Li-poly"` | Cell chemistry, e.g. `Li-ion` / `Li-poly` (omitted when absent) |
| `battery.batteries.<name>.capacity_pct` | integer | `73` | Current charge level (0–100) |
| `battery.batteries.<name>.health_pct` | number | `66.5` | This cell's wear health |
| `battery.batteries.<name>.status` | string | `"Discharging"` | `Charging` / `Discharging` / `Full` / `Not charging` / `Unknown` |
| `battery.batteries.<name>.cycle_count` | integer | `412` | Charge cycles (omitted when the battery doesn't report it) |
| `battery.batteries.<name>.energy_full_wh` | number | `35.9` | Present full-charge capacity in watt-hours |
| `battery.batteries.<name>.energy_full_design_wh` | number | `54.0` | Design capacity in watt-hours |
| `battery.batteries.<name>.voltage_now_v` | number | `12.27` | Present terminal voltage, volts (omitted when firmware doesn't report it) |
| `battery.batteries.<name>.voltage_min_design_v` | number | `11.1` | Firmware's `voltage_min_design`, volts — see the caution below (omitted when absent or zero) |
| `battery.batteries.<name>.power_now_w` | number | `5.0` | Instantaneous load, watts. Read from `power_now` where available, otherwise synthesized as `voltage_now × current_now` |
| `battery.batteries.<name>.time_to_empty_secs` | integer | `6300` | Estimated seconds until empty — present only while `Discharging` |
| `battery.batteries.<name>.time_to_full_secs` | integer | `2700` | Estimated seconds until full — present only while `Charging` |

The `batteries` keys are sysfs names (e.g. `BAT0`, `BAT1`).

`time_to_empty_secs` / `time_to_full_secs` are instantaneous-rate estimates — they jitter with system load and are display-only (they do not feed the health classification).

### Reading the delivery fields

`voltage_now_v`, `voltage_min_design_v` and `power_now_w` describe a pack's ability to **deliver current**, which is a different property from the wear health above and is not classified. Two cautions, both measured rather than assumed:

- **`voltage_min_design_v` is not a discharge floor.** Firmware routinely populates `voltage_min_design` with the pack's *nominal* voltage instead — on two laptops in the reference fleet it reads 11.25 V and 11.1 V across 3 cells, i.e. 3.7–3.75 V per cell, which is nominal rather than the ~3.0 V/cell empty cutoff. A healthy pack therefore sits *below* this figure for the bottom third of an ordinary discharge. Do not compute a "margin" from it and do not alert on one.
- **These are check-in-cadence samples.** A pack that fails to deliver current sags for milliseconds and the machine is gone; a sample taken once per check-in cannot see that. The fields trend a pack over weeks and give a diagnosis something to work from after the fact — they are not an early-warning trigger, and Vigo deliberately ships no threshold on them.

## Health Classification
On the per-envoy **Agent Health** card, `battery.health_pct` renders as a colored pill and rolls into the composite worst-signal score:

- **warn** below 80% of design capacity
- **fail** below 70%

A worn cell therefore raises the envoy's overall Agent Health severity, surfacing on the dashboard Agent Health card and the `/agent-health` page automatically.

## Collection Method
Walks `/sys/class/power_supply/`. Each `Battery`-type entry named `BAT*` contributes a row; `Mains`-type entries supply `ac_online`. Identity fields (`manufacturer`, `model_name`, `serial_number`, `technology`) are read straight from the matching sysfs attributes and omitted when the firmware leaves them blank. Wear health uses `energy_full` ÷ `energy_full_design` (µWh), falling back to `charge_full` ÷ `charge_full_design` (µAh) — the ratio is unit-independent. Time-to-empty/full uses `energy_now` ÷ `power_now` (µWh ÷ µW), falling back to `charge_now` ÷ `current_now` (µAh ÷ µA); both divide to hours. Voltages come from `voltage_now` / `voltage_min_design` (µV). Load prefers `power_now` (µW); on packs that report only the charge family — which have no `power_now` node at all — it is synthesized from `voltage_now × current_now`, using the magnitude of `current_now` because drivers disagree on its sign while discharging. Pure sysfs reads; no subprocess, no upower dependency.

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
