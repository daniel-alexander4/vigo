<!-- vigo-i18n source_sha:19d4f2c258ec013e00c3d32c356c4cfa138d61454e2a60efedaba18143674795 -->

# Configuration multi-cloud

Cet exemple montre comment utiliser les traits cloud pour gérer un parc réparti sur AWS, GCP et Azure avec un seul jeu de configcrates. Les traits cloud sont collectés automatiquement depuis le service de métadonnées d'instance (IMDS) de chaque fournisseur -- aucun marquage manuel requis.

## Configcrates adaptés au cloud

### Agents de surveillance spécifiques au cloud

Chaque fournisseur cloud possède son propre agent de surveillance. Utilisez des expressions `when:` pour installer le bon.

`stacks/configcrates/cloud-monitoring.vgo` :

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

### Résidence des données par région

Imposer la conformité au RGPD pour les instances de la région UE :

`stacks/configcrates/gdpr-audit.vgo` :

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

### Rapport d'inventaire cloud

Générer un fichier d'inventaire local avec les métadonnées cloud pour le suivi des actifs :

`stacks/configcrates/cloud-inventory.vgo` :

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

## Affectations de nœuds

Utilisez les traits cloud pour cibler des fournisseurs ou des régions entiers :

`stacks/envoys.vgo` :

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

## Requêtes d'inventaire

Interrogez votre parc selon des attributs cloud :

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

## Principaux traits utilisés

| Trait | Description |
|-------|-------------|
| `cloud.provider` | `aws`, `gcp`, `azure`, `digitalocean`, `hetzner`, `oracle` ou `none` |
| `cloud.region` | Région cloud (p. ex. `us-west-2`, `europe-west1`, `eastus`) |
| `cloud.zone` | Zone de disponibilité |
| `cloud.instance_type` | Taille d'instance (p. ex. `m5.xlarge`, `e2-medium`, `Standard_D4s_v3`) |
| `cloud.instance_id` | ID d'instance attribué par le cloud |
| `cloud.account_id` | Compte AWS, projet GCP, abonnement Azure ou tenant Oracle |
| `cloud.public_ip` | IPv4 publique si attribuée |
| `cloud.tags` | Étiquettes/labels d'instance sous forme de paires clé-valeur |

Tous les traits sont collectés automatiquement depuis l'IMDS du fournisseur cloud. Aucune configuration requise.
