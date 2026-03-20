
# Network

Collects the system hostname, FQDN, IP addresses, and MAC addresses.

## Trait Path
`network`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `network.hostname` | string | `"web01"` | System hostname |
| `network.fqdn` | string | `"web01.example.com"` | Fully qualified domain name (reverse DNS) |
| `network.ip_addresses` | array of strings | `["10.0.1.5", "2001:db8::1"]` | Non-loopback IP addresses (IPv4 and IPv6) |
| `network.mac_addresses` | array of strings | `["aa:bb:cc:dd:ee:ff"]` | Non-loopback MAC addresses |

## Collection Method
Hostname is retrieved via the `hostname` crate. The FQDN is resolved by reverse DNS lookup on the primary IP. The primary IPv4 address is detected by connecting a UDP socket to `8.8.8.8:80` (no data is sent). Additional MAC addresses are read from `/sys/class/net/*/address`, filtering out loopback and zero addresses. IPv6 addresses are parsed from `/proc/net/if_inet6`, excluding link-local (`fe80:`) and loopback interfaces.

## Using in When Expressions
```yaml
- name: web-config
  type: file
  target_path: /etc/nginx/conf.d/server.conf
  when: "!is_container"
```

## Using in Templates
```yaml
- name: hosts-entry
  type: file
  target_path: /etc/hosts.d/self
  content: |
    {{ index .Traits.network.ip_addresses 0 }} {{ .Traits.network.hostname }} {{ .Traits.network.fqdn }}
```

## Platform
Linux
