
# Multi-Cloud Configuration

This example shows how to use cloud traits to manage a fleet spanning AWS, GCP, and Azure with a single set of modules. Cloud traits are collected automatically from each provider's Instance Metadata Service (IMDS) -- no manual tagging required.

## Cloud-Aware Modules

### Cloud-specific monitoring agents

Each cloud provider has its own monitoring agent. Use `when:` expressions to install the right one.

`stockpile/modules/cloud-monitoring.vgo`:

```yaml
name: cloud-monitoring
resources:
  - name: cloudwatch-agent
    type: package
    package: amazon-cloudwatch-agent
    when: "cloud_provider('aws')"

  - name: cloudwatch-config
    type: file
    target_path: /opt/aws/amazon-cloudwatch-agent/etc/config.json
    source: templates/cloudwatch.json.tmpl
    when: "cloud_provider('aws')"
    notify: cloudwatch-service

  - name: cloudwatch-service
    type: service
    service: amazon-cloudwatch-agent
    ensure: running
    when: "cloud_provider('aws')"

  - name: ops-agent
    type: package
    package: google-cloud-ops-agent
    when: "cloud_provider('gcp')"

  - name: ops-agent-service
    type: service
    service: google-cloud-ops-agent
    ensure: running
    when: "cloud_provider('gcp')"

  - name: azure-monitor
    type: exec
    command: /usr/bin/az extension add --name monitor-control-service
    onlyif: "which az"
    when: "cloud_provider('azure')"
```

### Region-based data residency

Enforce GDPR compliance for EU-region instances:

`stockpile/modules/gdpr-audit.vgo`:

```yaml
name: gdpr-audit
resources:
  - name: audit-config
    type: file
    target_path: /etc/audit/gdpr.conf
    content: |
      # GDPR audit configuration
      # Region: {{ .Traits.cloud.region }}
      # Instance: {{ .Traits.cloud.instance_id }}
      log_retention_days=2555
      encrypt_at_rest=true
      data_residency={{ .Traits.cloud.region }}
    when: "cloud_region_prefix('eu-')"

  - name: audit-daemon
    type: service
    service: auditd
    ensure: running
    subscribes: audit-config
```

### Cloud inventory report

Generate a local inventory file with cloud metadata for asset tracking:

`stockpile/modules/cloud-inventory.vgo`:

```yaml
name: cloud-inventory
resources:
  - name: inventory-file
    type: file
    target_path: /var/lib/vigo/cloud-inventory.json
    content: |
      {
        "hostname": "{{ .Traits.network.hostname }}",
        "provider": "{{ .Traits.cloud.provider }}",
        "instance_id": "{{ .Traits.cloud.instance_id }}",
        "instance_type": "{{ .Traits.cloud.instance_type }}",
        "region": "{{ .Traits.cloud.region }}",
        "zone": "{{ .Traits.cloud.zone }}",
        "account_id": "{{ .Traits.cloud.account_id }}",
        "public_ip": "{{ .Traits.cloud.public_ip }}",
        "os": "{{ .Traits.os.distro }} {{ .Traits.os.release }}",
        "collected_at": "{{ .Traits.time.utc }}"
      }
    when: "cloud_provider_any()"
```

## Node Assignments

Use cloud traits to target entire providers or regions:

`stockpile/envoys.vgo`:

```yaml
match:
  - pattern: "*.aws.example.com"
    roles: [base, cloud-monitoring, gdpr-audit]
    environment: production

  - pattern: "*.gcp.example.com"
    roles: [base, cloud-monitoring]
    environment: production

  - pattern: "*.azure.example.com"
    roles: [base, cloud-monitoring]
    environment: staging
```

## Inventory Queries

Query your fleet by cloud attributes:

```bash
# All AWS instances
vigocli inventory query cloud.provider=aws

# All instances in us-west-2
vigocli inventory query cloud.region=us-west-2

# All m5.xlarge instances
vigocli inventory query cloud.instance_type=m5.xlarge

# All instances in a specific AWS account
vigocli inventory query cloud.account_id=123456789012

# Cross-cloud: all instances in EU regions
vigocli inventory query 'cloud.region=eu-*'
```

## Key Traits Used

| Trait | Description |
|-------|-------------|
| `cloud.provider` | `aws`, `gcp`, `azure`, `digitalocean`, `hetzner`, `oracle`, or `none` |
| `cloud.region` | Cloud region (e.g., `us-west-2`, `europe-west1`, `eastus`) |
| `cloud.zone` | Availability zone |
| `cloud.instance_type` | Instance size (e.g., `m5.xlarge`, `e2-medium`, `Standard_D4s_v3`) |
| `cloud.instance_id` | Cloud-assigned instance ID |
| `cloud.account_id` | AWS account, GCP project, Azure subscription, or Oracle tenant |
| `cloud.public_ip` | Public IPv4 if assigned |
| `cloud.tags` | Instance tags/labels as key-value pairs |

All traits are collected automatically from the cloud provider's IMDS. No configuration needed.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/license.md).*
