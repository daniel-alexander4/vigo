
# Cloud

Detects the cloud provider and collects instance metadata from the provider's Instance Metadata Service (IMDS).

## Trait Path
`cloud`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `cloud.provider` | string | `"aws"` | Detected cloud provider, or `"none"` if bare metal |
| `cloud.instance_id` | string | `"i-0abc123def456789"` | Cloud-assigned instance ID |
| `cloud.instance_type` | string | `"m5.xlarge"` | Instance size/shape |
| `cloud.instance_name` | string | `"web-prod-01"` | Instance display name (GCP, Azure, DO, Hetzner, Oracle) |
| `cloud.region` | string | `"us-west-2"` | Cloud region |
| `cloud.zone` | string | `"us-west-2a"` | Availability zone |
| `cloud.account_id` | string | `"123456789012"` | AWS account ID, GCP project ID, Azure subscription ID, or Oracle tenant ID |
| `cloud.vpc_id` | string | `"vpc-0abc123"` | VPC/network ID (AWS, GCP) |
| `cloud.public_ip` | string | `"54.1.2.3"` | Public IPv4 address, if assigned |
| `cloud.image_id` | string | `"ami-0abc123"` | AMI, image, or marketplace image reference |
| `cloud.resource_group` | string | `"my-rg"` | Azure resource group |
| `cloud.tags` | object | `{"Environment": "prod"}` | Instance tags/labels (provider-dependent) |

Not all fields are available on every provider. Only `cloud.provider` is always present. Fields that the provider doesn't expose or that aren't applicable are omitted (not set to empty).

### Provider Support Matrix

| Field | AWS | GCP | Azure | DigitalOcean | Hetzner | Oracle |
|-------|-----|-----|-------|--------------|---------|--------|
| `provider` | yes | yes | yes | yes | yes | yes |
| `instance_id` | yes | yes | yes | yes | yes | yes |
| `instance_type` | yes | yes | yes | - | - | yes |
| `instance_name` | - | yes | yes | yes | yes | yes |
| `region` | yes | yes | yes | yes | yes | yes |
| `zone` | yes | yes | yes | - | - | yes |
| `account_id` | yes | yes | yes | - | - | yes |
| `vpc_id` | yes | yes | - | - | - | - |
| `public_ip` | yes | yes | yes | yes | yes | - |
| `image_id` | yes | yes | yes | yes | - | yes |
| `resource_group` | - | - | yes | - | - | - |
| `tags` | yes | yes | yes | yes | - | yes |

Supported provider values: `aws`, `gcp`, `azure`, `digitalocean`, `oracle`, `hetzner`, `vultr`, `linode`, `none`.

Vultr and Linode are detected via DMI but do not have IMDS metadata collection yet.

## Collection Method

**Phase 1 — Provider detection (zero cost):** Reads DMI fields (`sys_vendor`, `board_vendor`, `bios_vendor`, `product_name`) from `/sys/class/dmi/id/`. No subprocess, no network I/O.

**Phase 2 — IMDS metadata (periodic, ~hourly):** If a provider is detected, queries its Instance Metadata Service at `169.254.169.254` (link-local, <1ms latency). Each provider has a different API:

- **AWS:** IMDSv2 with PUT token exchange, then GET calls per field
- **GCP:** GET with `Metadata-Flavor: Google` header
- **Azure:** Single GET returning full JSON document with `Metadata: true` header
- **DigitalOcean:** Plain GET per field, no auth headers
- **Hetzner:** Plain GET per field, no auth headers
- **Oracle:** GET with `Authorization: Bearer Oracle` header, returns JSON

All IMDS calls have a 2-second timeout. If IMDS is unreachable (firewalled, non-cloud environment), the collector returns just `{ "provider": "none" }` without error.

Cloud metadata rarely changes during instance lifetime, so this collector runs on the periodic schedule (~1 hour) rather than every check-in cycle.

**AWS tags note:** Instance tags via IMDS require the "Allow tags in instance metadata" option to be enabled on the EC2 instance. If disabled, the `tags` field is omitted.

## Using in When Expressions

```yaml
# Only on AWS instances
- name: install-cloudwatch-agent
  type: package
  package: amazon-cloudwatch-agent
  when: "cloud_provider('aws')"

# Only in eu-west-1 (data residency)
- name: gdpr-audit-config
  type: file
  target_path: /etc/audit/gdpr.conf
  when: "cloud_region('eu-west-1')"

# Only on large instances
- name: high-memory-tuning
  type: exec
  command: /opt/scripts/tune-memory.sh
  when: "cloud_instance_type('m5.4xlarge') || cloud_instance_type('m5.8xlarge')"
```

## Using in Templates

```yaml
- name: cloud-inventory
  type: file
  target_path: /var/lib/vigo/cloud.txt
  content: |
    Provider:      {{ .Traits.cloud.provider }}
    Instance ID:   {{ .Traits.cloud.instance_id }}
    Instance Type: {{ .Traits.cloud.instance_type }}
    Region:        {{ .Traits.cloud.region }}
    Zone:          {{ .Traits.cloud.zone }}
    Account:       {{ .Traits.cloud.account_id }}
    Public IP:     {{ .Traits.cloud.public_ip }}
```

## Platform
Linux (all providers), Windows (AWS, Azure, GCP — IMDS is accessible on Windows instances)
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
