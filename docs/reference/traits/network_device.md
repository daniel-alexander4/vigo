
# Network Device Traits

Collects facts from network devices (Cisco IOS/NX-OS, Juniper Junos) over SSH during the gateway proxy convergence loop. Unlike standard trait collectors that run locally, this collector operates remotely through the gateway envoy.

## Trait Namespace

All traits are stored under the `device` key.

## Cisco IOS/NX-OS

| Command | Traits Produced |
|---------|----------------|
| `show version` | `device.model`, `device.firmware`, `device.serial`, `device.uptime`, `device.hostname` |
| `show interfaces status` | `device.interfaces[]` -- name, status, vlan, speed |
| `show vlan brief` | `device.vlans[]` -- id, name, status, ports |
| `show ip route summary` | `device.routes.total`, `device.routes.connected`, `device.routes.static`, `device.routes.bgp` |

## Juniper Junos

| Command | Traits Produced |
|---------|----------------|
| `show version` | `device.model`, `device.firmware`, `device.hostname` |
| `show chassis hardware` | `device.modules[]` -- name, serial, description |
| `show interfaces terse` | `device.interfaces[]` -- name, admin, link, proto, local |
| `show route summary` | `device.routes.total`, `device.routes.direct`, `device.routes.static`, `device.routes.bgp` |

## Example Output

```json
{
  "device": {
    "type": "cisco_ios",
    "model": "WS-C3750X-48T",
    "firmware": "16.12.4",
    "serial": "FOC1234ABCD",
    "hostname": "core-sw01",
    "uptime": "15 days, 3 hours, 22 minutes",
    "interfaces": [
      {"name": "Gi0/1", "status": "connected", "vlan": "100", "speed": "a-1000"},
      {"name": "Gi0/2", "status": "notconnect", "vlan": "1"}
    ],
    "vlans": [
      {"id": "1", "name": "default", "status": "active"},
      {"id": "100", "name": "Production", "status": "active"}
    ],
    "routes": {
      "total": 42,
      "connected": 5,
      "static": 3,
      "bgp": 34
    }
  }
}
```

## Usage in Templates

```yaml
content: |
  # Device: {{ .Traits.device.hostname }}
  # Model:  {{ .Traits.device.model }}
  # Firmware: {{ .Traits.device.firmware }}
```

## Usage in Conditionals

```yaml
when: "trait('device.firmware', '>= 16.12')"
```

## Error Handling

Individual show commands that fail are logged and skipped -- the collector returns whatever traits it can gather. A complete SSH connection failure returns an empty trait set with a warning.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
