
# Monitoring Stack

This example sets up Prometheus node_exporter, Prometheus server, and Grafana using the package-config-service pattern.

## Node Exporter Module

Deployed to every envoy for metrics collection.

`policy/stage/modules/node-exporter.vgo`:

```yaml
name: node-exporter
vars:
  node_exporter_port: "9100"
resources:
  - name: node-exporter-package
    type: package
    package: prometheus-node-exporter
    state: present
    when: "os_family('debian')"

  - name: node-exporter-package-rhel
    type: package
    package: node_exporter
    state: present
    when: "!os_family('debian')"

  - name: node-exporter-config
    type: file
    target_path: /etc/default/prometheus-node-exporter
    owner: root
    group: root
    mode: "0644"
    content: |
      ARGS="--web.listen-address=:{{ .Vars.node_exporter_port }}"
    notify:
      - node-exporter-service

  - name: node-exporter-service
    type: service
    service: prometheus-node-exporter
    state: running
    enabled: true
```

## Prometheus Module

Deployed to monitoring servers.

`policy/stage/modules/prometheus.vgo`:

```yaml
name: prometheus
vars:
  prometheus_port: "9090"
  prometheus_retention: 30d
  prometheus_scrape_interval: 15s
resources:
  - name: prometheus-package
    type: package
    package: prometheus
    state: present

  - name: prometheus-config
    type: file
    target_path: /etc/prometheus/prometheus.yml
    owner: prometheus
    group: prometheus
    mode: "0644"
    content: |
      global:
        scrape_interval: {{ .Vars.prometheus_scrape_interval }}
        evaluation_interval: 15s

      scrape_configs:
        - job_name: prometheus
          static_configs:
            - targets: ['localhost:{{ .Vars.prometheus_port }}']

        - job_name: node
          file_sd_configs:
            - files:
                - /etc/prometheus/targets/*.json
              refresh_interval: 30s

        - job_name: vigo
          static_configs:
            - targets: ['{{ .Vars.vigo_server }}:8443']
    notify:
      - prometheus-service

  - name: prometheus-targets-dir
    type: directory
    path: /etc/prometheus/targets
    owner: prometheus
    group: prometheus
    mode: "0755"

  - name: prometheus-service
    type: service
    service: prometheus
    state: running
    enabled: true
```

## Grafana Module

`policy/stage/modules/grafana.vgo`:

```yaml
name: grafana
depends_on:
  - prometheus
vars:
  grafana_port: "3000"
  grafana_admin_user: admin
resources:
  - name: grafana-package
    type: package
    package: grafana
    state: present

  - name: grafana-config
    type: file
    target_path: /etc/grafana/grafana.ini
    owner: root
    group: grafana
    mode: "0640"
    content: |
      [server]
      http_port = {{ .Vars.grafana_port }}

      [security]
      admin_user = {{ .Vars.grafana_admin_user }}

      [auth.anonymous]
      enabled = false
    notify:
      - grafana-service

  - name: grafana-datasource
    type: file
    target_path: /etc/grafana/provisioning/datasources/prometheus.yaml
    owner: root
    group: grafana
    mode: "0640"
    content: |
      apiVersion: 1
      datasources:
        - name: Prometheus
          type: prometheus
          url: http://localhost:{{ .Vars.prometheus_port }}
          isDefault: true
          access: proxy
    notify:
      - grafana-service

  - name: grafana-service
    type: service
    service: grafana-server
    state: running
    enabled: true
```

## Role Definition

`policy/stage/roles/monitoring.vgo`:

```yaml
name: monitoring
modules:
  - node-exporter
  - prometheus
  - grafana
```

## Node Assignment

`policy/stage/envoys/nodes.vgo`:

```yaml
envoys:
  # All servers get node_exporter
  - match: "*.example.com"
    modules:
      - node-exporter

  # Monitoring servers get the full stack
  - match: "mon-*.example.com"
    environment: production
    roles: [monitoring]
    vars:
      vigo_server: vigo.internal
      prometheus_retention: 90d
      grafana_port: "3000"
```

## Execution Order

1. **node-exporter** -- no dependencies, runs first
2. **prometheus** -- no explicit dependency on node-exporter (it discovers targets via file_sd)
3. **grafana** -- depends on prometheus (needs the datasource to be available)

## Adding Vigo Metrics

Point Prometheus at the Vigo server's `/metrics` endpoint to monitor the configuration management system itself. See [Prometheus Metrics](../operations/metrics.md) for available metrics and Grafana dashboard suggestions.
