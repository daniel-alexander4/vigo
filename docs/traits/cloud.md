# Cloud

Detects the cloud provider hosting the system, if any.

## Trait Path
`cloud`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `cloud.provider` | string | `"aws"` | Detected cloud provider, or `"none"` if bare metal |

Supported providers: `aws`, `gcp`, `azure`, `digitalocean`, `oracle`, `hetzner`, `vultr`, `linode`, `none`.

## Collection Method
Reads DMI fields (`sys_vendor`, `board_vendor`, `bios_vendor`, `product_name`) from `/sys/class/dmi/id/` and checks the combined lowercase text for known cloud provider identifiers: "amazon"/"ec2" (AWS), "google" (GCP), "microsoft" (Azure), "digitalocean", "oracle", "hetzner", "vultr", "linode"/"akamai". Returns `"none"` if no provider is detected.

## Using in When Expressions
```yaml
- name: install-cloud-agent
  type: package
  package: amazon-ssm-agent
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: cloud-info
  type: file
  target_path: /var/lib/vigo/cloud.txt
  content: |
    Cloud Provider: {{ .Traits.cloud.provider }}
```

## Platform
Linux
