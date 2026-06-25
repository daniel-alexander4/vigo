<!-- vigo-i18n source_sha:b1b41242fa73bf81ea5c658490f94ba66c39941ce76da4052e3d2d15304fc420 -->

# Mettre en place la supervision

À la fin de cette page, vous disposerez d'une instance Prometheus qui scrape le serveur Vigo, des cinq tableaux de bord Grafana fournis et chargés, et de règles d'alerte qui se déclenchent sur les conditions qui comptent : Envoys obsolètes, échecs de convergence, dérive de licence et nombre de CVE.

**Quand l'utiliser :** tout déploiement où quelqu'un d'autre que vous doit être informé des anomalies de la flotte. Pour un home lab à opérateur unique, `vigocli doctor` + le tableau de bord de l'interface web suffisent ; Prometheus + Grafana commence à devenir rentable autour de cinq Envoys, ou lorsque vous avez besoin d'historique.

**Quand le sauter :** développement sur un portable unique. Le point d'accès `/metrics` existe de toute façon — vous pourrez y revenir quand vous en aurez besoin.

![La surface des intégrations de Vigo — Prometheus, Slack, PagerDuty, ServiceNow, Datadog depuis un seul bloc de configuration](../../img/screenshots/vigo.integrations.png)

Le serveur Vigo expose les métriques Prometheus sur `GET /metrics` sur le port REST (8443 par défaut).

## Métriques disponibles

### Jauges de flotte

| Métrique | Type | Description |
|--------|------|-------------|
| `vigo_nodes_total` | Gauge | Nombre total d'Envoys enrôlés |
| `vigo_nodes_active_total` | Gauge | Envoys ayant fait un check-in dans la dernière heure |
| `vigo_convergence_pct` | Gauge | Pourcentage de conformité à l'échelle de la flotte (0.0 à 1.0) |
| `vigo_database_size_bytes` | Gauge | Taille actuelle du fichier de base de données SQLite en octets |

### Conformité

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_compliance_status` | Gauge | `envoy`, `status` | Statut de convergence par Envoy (1 pour le statut courant). Valeurs de statut : converged, degraded, failed, no data (axe échec) ; changed, diverged (axe dérive) ; offline |

### Check-ins

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_checkins_total` | Counter | `status` | Nombre total de check-ins d'agents |
| `vigo_checkin_age_seconds` | Histogram | | Distribution des secondes écoulées depuis le dernier check-in sur l'ensemble des Envoys |
| `vigo_envoy_state_transitions_total` | Counter | `transition` | Bascules d'accessibilité : `offline` (non-obsolète→obsolète) et `online` (obsolète→non-obsolète). Une hausse des transitions `offline` alors que les Envoys sont actifs indique que le seuil d'obsolescence les fait osciller sous charge |

L'histogramme `vigo_checkin_age_seconds` utilise des buckets personnalisés : 5s, 15s, 30s, 45s, 60s, 120s, 300s, 600s, 900s, 1800s, 3600s, 7200s. Les buckets sous les 60s chevauchent le seuil d'obsolescence de 30s (`freshness.MinStaleThreshold`) afin que l'histogramme puisse mesurer à quel point des check-ins sains s'approchent de la limite d'obsolescence sous une cadence rapide (1s par défaut). Cela remplace les jauges de dernière-vue par Envoy pour éviter une explosion de cardinalité à l'échelle.

### gRPC

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_grpc_requests_total` | Counter | `method`, `code` | Nombre total de requêtes gRPC par nom complet de méthode et code de statut |
| `vigo_grpc_request_duration_seconds` | Histogram | `method` | Latence des requêtes gRPC en secondes |
| `vigo_grpc_handler_panics_total` | Counter | `method` | Paniques de handler gRPC récupérées par l'intercepteur de récupération. **Toute valeur non nulle est un bug** — le handler a paniqué et le RPC a renvoyé `Internal` au lieu de faire planter le processus. Alertez sur `rate(...) > 0`. |

Couvre tous les RPC unaires (CheckIn, ReportResult, ReportTraits, Register). Les flux bidirectionnels (AgentStream) ne sont pas inclus — ils utilisent à la place les métriques de check-in et de delta.

### Exécutions

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_run_duration_seconds` | Histogram | `envoy` | Durée des exécutions de convergence de l'agent en secondes |
| `vigo_drift_detected_total` | Counter | `envoy`, `configcrate` | Événements de détection de dérive |
| `vigo_configcrate_actions_total` | Counter | `type`, `action` | Actions de configcrate effectuées (par type de ressource et action) |

### Orchestration

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_task_runs_total` | Counter | `status` | Exécutions de tâches terminées par statut final (complete, failed) |
| `vigo_workflow_runs_total` | Counter | `status` | Exécutions de workflows terminées par statut final (complete, failed) |

### Webhooks

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_webhook_deliveries_total` | Counter | `status` | Résultats de livraison de webhook (success, failed, rejected, cancelled, error) |

| `vigo_webhook_duration_seconds` | Histogram | | Latence de livraison de webhook incluant les nouvelles tentatives |

### SMTP

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_smtp_sends_total` | Counter | `event`, `status` | Tentatives d'envoi d'e-mail par type d'événement et résultat (success, error) |

### Licence

| Métrique | Type | Description |
|--------|------|-------------|
| `vigo_license_envoys_used` | Gauge | Nombre actuel d'Envoys actifs (non révoqués) |
| `vigo_license_envoys_max` | Gauge | Nombre maximal d'Envoys autorisés par la licence |
| `vigo_license_stage` | Gauge | Étape d'application : 0=compliant, 1=grace, 2=enrollment\_block, 3=service\_stop |
| `vigo_license_days_remaining` | Gauge | Jours avant expiration de la licence (négatif si expirée) |
| `vigo_license_expiry_timestamp` | Gauge | Timestamp Unix de la date d'expiration de la licence |
| `vigo_license_enrollments_blocked_total` | Counter | Enrôlements rejetés par le plafond strict du nombre de nœuds (active\_envoys >= max) |
| `vigo_license_enforcement_errors_total` | Counter | Erreurs internes d'application (décompte indisponible, index de flotte manquant) — le plafond échoue en mode fermé à chaque fois |

### FleetIndex

| Métrique | Type | Description |
|--------|------|-------------|
| `vigo_fleetindex_flush_duration_seconds` | Histogram | Temps pour vider l'état d'Envoy modifié du FleetIndex vers SQLite |
| `vigo_fleetindex_dirty_count` | Gauge | Nombre d'entrées modifiées lors du dernier vidage |

### Serveur

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_uptime_seconds` | Gauge | | Secondes écoulées depuis le démarrage du processus serveur |
| `vigo_build_info` | Gauge | `version` | Informations de build du serveur (toujours 1, version en label) |
| `vigo_streams_active` | Gauge | | Nombre de flux d'agents actuellement connectés |

### Bootstrap

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_enrollments_total` | Counter | `status` | Tentatives d'enrôlement bootstrap (success, denied, error) |

### Secrets

| Métrique | Type | Description |
|--------|------|-------------|
| `vigo_secret_rotations_total` | Counter | Événements de rotation de secret détectés par le watcher |
| `vigo_secret_resolution_errors_total` | Counter | Échecs de résolution de secret durant le chargement de la configuration |

### Sécurité

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_sig_verify_failed_total` | Counter | `reason` | Échecs de vérification de signature |

| `vigo_security_cve_critical` | Gauge | | Nombre de CVE critiques à l'échelle de la flotte sur tous les hôtes scannés |
| `vigo_security_cve_high` | Gauge | | Nombre de CVE élevées à l'échelle de la flotte sur tous les hôtes scannés |
| `vigo_security_cve_medium` | Gauge | | Nombre de CVE moyennes à l'échelle de la flotte sur tous les hôtes scannés |
| `vigo_security_cve_low` | Gauge | | Nombre de CVE faibles à l'échelle de la flotte sur tous les hôtes scannés |
| `vigo_security_hardening_avg` | Gauge | | Score de durcissement moyen sur tous les hôtes scannés (0-100) |
| `vigo_security_rootkit_warnings` | Gauge | | Nombre d'alertes de rootkit à l'échelle de la flotte |
| `vigo_security_malware_detections` | Gauge | | Nombre de détections de malware à l'échelle de la flotte |
| `vigo_security_integrity_issues` | Gauge | | Nombre de problèmes d'intégrité de fichiers à l'échelle de la flotte |
| `vigo_security_scanned_hosts` | Gauge | | Nombre d'hôtes disposant de données d'analyse de sécurité |

### Convergence

| Métrique | Type | Description |
|--------|------|-------------|
| `vigo_convergence_converged` | Gauge | Nombre d'Envoys à l'état converged |
| `vigo_convergence_degraded` | Gauge | Nombre d'Envoys à l'état degraded (des configcrates ont échoué à la dernière exécution) |
| `vigo_convergence_changed` | Gauge | Nombre d'Envoys dont la dernière exécution a comporté des changements (axe dérive) |
| `vigo_convergence_diverged` | Gauge | Nombre d'Envoys à l'état diverged |
| `vigo_convergence_failed` | Gauge | Nombre d'Envoys en état d'erreur |
| `vigo_convergence_offline` | Gauge | Nombre d'Envoys passés offline |
| `vigo_convergence_no_data` | Gauge | Nombre d'Envoys sans données de convergence |
| `vigo_convergence_pct` | Gauge | Pourcentage de convergence à l'échelle de la flotte (0.0 à 1.0) |
| `vigo_publish_convergence_seconds` | Histogram | Secondes écoulées entre le moment où une version de configuration devient courante et sa première application par un Envoy. Une observation par Envoy et par adoption de version vers l'avant ; sans label et à l'échelle de la flotte. Les reconvergences de routine et le rattrapage de versions longtemps obsolètes sont exclus. |

### Risque

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_risk_avg_score` | Gauge | | Score de risque moyen à l'échelle de la flotte (0-100) |
| `vigo_risk_max_score` | Gauge | | Score de risque le plus élevé sur l'ensemble des Envoys (0-100) |
| `vigo_risk_distribution` | Gauge | `level` | Nombre d'Envoys à chaque niveau de risque (low, medium, high, critical) |

### Config

| Métrique | Type | Description |
|--------|------|-------------|
| `vigo_config_reload_duration_seconds` | Histogram | Durée des rechargements de configuration |
| `vigo_config_reload_errors_total` | Counter | Échecs de rechargement de configuration |

### Swarm & Spanner

Jauges totalisées sur la flotte pour le substrat Swarm, les sous-systèmes de contenu et le roster Spanner. Toutes sont des totaux à l'échelle de la flotte sans label par entité. Elles se rafraîchissent toutes les 5 minutes (indépendamment de `metrics_interval`) — les agrégateurs sous-jacents se reconstruisent à partir des traits par Envoy, et les décomptes Swarm évoluent lentement.

| Métrique | Type | Description |
|--------|------|-------------|
| `vigo_swarm_envoys_substrate_active` | Gauge | Envoys participant activement au substrat de blobs |
| `vigo_swarm_manifest_entries_active` | Gauge | Entrées de manifeste de seed actives (non révoquées) |
| `vigo_swarm_footprint_bytes` | Gauge | Total des octets de blobs en cache sur l'ensemble de la flotte |
| `vigo_gitback_projects` | Gauge | Projets Gitback distincts sur l'ensemble de la flotte |
| `vigo_gitback_envoys` | Gauge | Envoys hébergeant au moins un projet Gitback |
| `vigo_gitback_divergent_refs` | Gauge | Refs Gitback dont le SHA diffère entre hôtes |
| `vigo_curator_artifacts` | Gauge | Artefacts distincts dans le catalogue Curator de la flotte |
| `vigo_curator_publishers` | Gauge | Envoys publiant des artefacts Curator |
| `vigo_lockbox_users` | Gauge | Utilisateurs avec un état Lockbox sur l'ensemble de la flotte |
| `vigo_lockbox_files` | Gauge | Total des fichiers chiffrés sur l'ensemble des utilisateurs Lockbox |
| `vigo_lockbox_divergent_files` | Gauge | Fichiers Lockbox dont le hash/la taille diffère entre Envoys |
| `vigo_lockbox_failed_files` | Gauge | Artefacts Lockbox `.failed` — fichiers dont le chiffrement a échoué de manière permanente, restant en clair et ne se synchronisant jamais (une valeur non nulle est un échec actionnable, pas de la dérive) |
| `vigo_lockbox_recipient_drifts` | Gauge | Utilisateurs Lockbox dont l'ensemble de destinataires diffère entre Envoys (les fichiers nouvellement chiffrés ne se déchiffreront pas sur les pairs manquants) |
| `vigo_longdrawer_users` | Gauge | Utilisateurs avec un état de synchronisation Longdrawer sur l'ensemble de la flotte |
| `vigo_longdrawer_files` | Gauge | Total des fichiers synchronisés sur l'ensemble des utilisateurs Longdrawer |
| `vigo_longdrawer_divergent_files` | Gauge | Fichiers Longdrawer dont le hash/la taille diffère entre Envoys |
| `vigo_spanner_bolts_active` | Gauge | Bolts actifs (non révoqués) dans le roster Spanner |
| `vigo_spanner_bolts_revoked` | Gauge | Bolts révoqués conservés dans le roster Spanner |

Les jauges `*_divergent_*` sont le signal de santé — une valeur non nulle signifie que des Envoys divergent sur un contenu censé être identique, ce qui justifie une investigation.

## Métriques de l'assistant IA

Définies dans `server/ai/metrics.go`, enregistrées séparément.

| Métrique | Type | Labels | Description |
|--------|------|--------|-------------|
| `vigo_ai_requests_total` | Counter | | Requêtes à l'assistant IA |
| `vigo_ai_tokens_total` | Counter | | Tokens consommés par les réponses de l'IA |
| `vigo_ai_request_duration_seconds` | Histogram | | Latence des requêtes IA |
| `vigo_ai_tool_calls_total` | Counter | `tool` | Invocations d'outils IA par nom d'outil |
| `vigo_ai_errors_total` | Counter | | Erreurs de requête IA |

## Configuration du scrape

Ajoutez le serveur Vigo à votre configuration de scrape Prometheus. Le point d'accès des métriques est sur le port REST (8443), qui est en HTTPS — le job nécessite `scheme: https` et un `tls_config` qui accepte le certificat auto-signé du serveur. `/metrics` ne requiert aucune authentification.

```yaml
scrape_configs:
  - job_name: vigo
    scheme: https
    metrics_path: /metrics
    tls_config:
      insecure_skip_verify: true   # self-signed server cert
    static_configs:
      - targets: ['vigo-server:8443']
    scrape_interval: 30s
```


## Suggestions de tableaux de bord Grafana

### Panneau Vue d'ensemble de la flotte

- **Total Envoys** : `vigo_nodes_total`
- **Envoys actifs (1h)** : `vigo_nodes_active_total`
- **Conformité %** : `vigo_convergence_pct * 100`
- **Taux de check-in** : `rate(vigo_checkins_total[5m])`

### Répartition de la conformité

- Jauge empilée ou diagramme circulaire utilisant `vigo_compliance_status` groupé par `status`
- Série temporelle de `vigo_convergence_pct` pour l'analyse de tendance

### Performance des exécutions

- **Durée d'exécution P95** : `histogram_quantile(0.95, rate(vigo_run_duration_seconds_bucket[5m]))`
- **Taux d'échec** : `rate(vigo_checkins_total{status="failure"}[5m])`
- **Taux de dérive** : `rate(vigo_drift_detected_total[5m])`

### Latence gRPC

- **Latence de check-in P99** : `histogram_quantile(0.99, rate(vigo_grpc_request_duration_seconds_bucket{method="/vigo.VigoAgent/CheckIn"}[5m]))`
- **Taux d'erreur par méthode** : `rate(vigo_grpc_requests_total{code!="OK"}[5m])`

### Orchestration

- **Débit de tâches** : `rate(vigo_task_runs_total[5m])`
- **Taux d'échec de tâches** : `rate(vigo_task_runs_total{status="failed"}[5m])`
- **Taux de réussite de workflows** : `rate(vigo_workflow_runs_total{status="complete"}[5m]) / rate(vigo_workflow_runs_total[5m])`

### Webhooks

- **Taux de réussite de livraison** : `rate(vigo_webhook_deliveries_total{status="success"}[5m]) / rate(vigo_webhook_deliveries_total[5m])`
- **Latence de livraison P95** : `histogram_quantile(0.95, rate(vigo_webhook_duration_seconds_bucket[5m]))`
- Alertez sur `rate(vigo_webhook_deliveries_total{status="failed"}[5m]) > 0`

### Licence

- **Utilisation des Envoys** : `vigo_license_envoys_used / vigo_license_envoys_max * 100`
- **Jours avant expiration** : `vigo_license_days_remaining`
- **Date d'expiration** : `timestamp(vigo_license_expiry_timestamp)` pour un affichage en temps absolu dans Grafana
- Alertez sur `vigo_license_stage > 0` (toute violation)
- Alertez sur `vigo_license_envoys_used / vigo_license_envoys_max >= 0.9` (approche de la limite — le plafond strict commencera à rejeter à 1.0)
- Alertez sur `vigo_license_envoys_used >= vigo_license_envoys_max` (à pleine capacité — le prochain enrôlement sera rejeté)
- Alertez sur `increase(vigo_license_enrollments_blocked_total[1h]) > 0` (tout rejet par le plafond strict — l'opérateur doit agir)
- Alertez sur `increase(vigo_license_enforcement_errors_total[5m]) > 0` (erreurs internes d'application — à investiguer, le plafond échoue en mode fermé)
- Alertez sur `vigo_license_days_remaining < 30` (licence expirant bientôt)

### Santé du serveur

- **Uptime** : `vigo_uptime_seconds` (détecte les redémarrages dans Grafana)
- **Version** : label `version` de `vigo_build_info` (suivre le déploiement sur les instances)
- **Flux actifs** : `vigo_streams_active` (surveiller la connectivité des agents)
- Alertez sur `vigo_uptime_seconds < 300` pour détecter un redémarrage récent
- Alertez sur `vigo_streams_active == 0` quand `vigo_nodes_active_total > 0` (agents ne se connectant pas via le flux)

### Alertes de sécurité


- Alertez sur `rate(vigo_sig_verify_failed_total[5m]) > 0` pour détecter les échecs de vérification de signature
- Alertez sur les augmentations de `vigo_config_reload_errors_total`
- Alertez sur `rate(vigo_secret_resolution_errors_total[5m]) > 0`

### Obsolescence des check-ins

- **Âge médian des check-ins** : `histogram_quantile(0.5, rate(vigo_checkin_age_seconds_bucket[5m]))`
- Alertez quand `histogram_quantile(0.95, ...)` dépasse votre intervalle de check-in attendu

## Intervalle de rafraîchissement des métriques

Les métriques de type jauge (décomptes de nœuds, conformité, taille de base de données) sont rafraîchies selon un intervalle configurable :

```yaml
tuning:
  metrics_interval: "30s"    # default
```

Voir [Paramètres de réglage](troubleshoot.md) pour toutes les options de réglage.
---
*Confidentiel -- Alexander4, LLC. Reproduction interdite. Voir la [licence](../../legal/license.md).*


---

## Tableaux de bord Grafana


Tableaux de bord Grafana préconçus pour superviser Vigo via Prometheus.

## Téléchargement

| Tableau de bord | Panneaux | Téléchargement |
|-----------|--------|----------|
| **Fleet Overview** | 40 panneaux sur 11 lignes : statistiques de flotte, répartition de conformité, check-ins & latence gRPC, convergence & dérive, opérations, infrastructure, webhooks & SMTP, secrets, assistant IA, posture de sécurité, posture de risque | [vigo-overview.json](../grafana/vigo-overview.json) |
| **Security Posture** | 14 panneaux sur 4 lignes : nombre de CVE par sévérité, jauge + tendance du score de durcissement, détection de menaces (rootkits, malware, intégrité), couverture d'analyse | [vigo-security.json](../grafana/vigo-security.json) |
| **Convergence** | 9 panneaux sur 3 lignes : diagramme circulaire de statut + compteurs, % de conformité + tendances du taux de dérive, heatmap de l'âge des check-ins | [vigo-convergence.json](../grafana/vigo-convergence.json) |
| **Risk Posture** | 8 panneaux sur 3 lignes : scores de risque moyen + max, distribution par niveau (low/medium/high/critical), tendances du score de risque dans le temps | [vigo-risk.json](../grafana/vigo-risk.json) |
| **Swarm & Spanner** | 13 panneaux sur 5 lignes : empreinte du substrat, décomptes des sous-systèmes de contenu (gitback/curator/lockbox/longdrawer), divergence entre Envoys, roster des Bolts Spanner | [vigo-swarm.json](../grafana/vigo-swarm.json) |
| **Publish Bursts** | 14 panneaux sur 4 lignes : en un coup d'œil CheckIns en vol / nœuds actifs / Mo·s sortants / % de convergence, taux + latence gRPC + handshakes TLS, octets/s de bundle + quantiles de taille + force pushes, actions de configcrate + dérive + échecs de vérif-signature + convergence dans le temps + quantiles de temps-de-convergence. Des annotations verticales marquent chaque publication de configuration. | [vigo-bursts.json](../grafana/vigo-bursts.json) |

## Installation via les configcrates Vigo (recommandé)

Pour tout hôte exécutant Grafana sous gestion Vigo, déposez le configcrate `vigo-grafana-dashboards` dans le rôle et convergez :

```yaml
# stacks/scaffolding/roles.vgo
- name: grafana-host
  configcrates:
    - grafana                  # installs the daemon
    - vigo-grafana-dashboards  # drops all six dashboards into provisioning
```

Le configcrate source les JSON des tableaux de bord depuis `stacks/templates/vigo-dashboards/` et les écrit dans `/etc/grafana/provisioning/dashboards/vigo/`. Grafana les récupère à sa prochaine analyse de provisioning (30 s par défaut) — pas d'import via l'interface, pas de redémarrage de service.

Le configcrate associé `vigo-prometheus-alerts` livre [`alerts.yaml`](#alert-rules) dans `/etc/prometheus/rules/vigo-alerts.yaml` et notifie le service Prometheus de recharger. Associez-le au configcrate standard `prometheus` — qui monte déjà `rule_files: /etc/prometheus/rules/*.yml`.

## Import manuel

Pour les hôtes où Vigo ne gère pas la pile de supervision, importez chaque tableau de bord à la main :

1. Téléchargez le fichier JSON via le lien dans le tableau ci-dessus
2. Ouvrez Grafana (typiquement http://localhost:3000)
3. Allez dans **Dashboards → Import**
4. Téléversez le fichier JSON ou collez son contenu
5. Sélectionnez votre source de données Prometheus lorsque demandé

## Tableau de bord Fleet Overview

[Télécharger vigo-overview.json](../grafana/vigo-overview.json)

### Ligne 1 — Vue d'ensemble de la flotte

| Panneau | Type | Métrique |
|-------|------|--------|
| Total Nodes | Stat | `vigo_nodes_total` |
| Active Nodes | Stat | `vigo_nodes_active_total` |
| Compliance % | Gauge (0-100, rouge/jaune/vert) | `vigo_compliance_pct` |
| Uptime | Stat | `vigo_uptime_seconds` |
| License Usage | Gauge (utilisé vs max) | `vigo_license_envoys_used` / `vigo_license_envoys_max` |
| License Days Remaining | Stat (rouge < 30, jaune < 90) | `vigo_license_days_remaining` |
| Active Streams | Stat | `vigo_streams_active` |
| Database Size | Stat | `vigo_database_size_bytes` |

### Ligne 2 — Répartition de la conformité

| Panneau | Type | Métrique |
|-------|------|--------|
| Compliance Status by Type | Diagramme circulaire (vert/jaune/rouge/gris) | `vigo_compliance_status` par label `status` |
| Compliance % Over Time | Série temporelle | `vigo_compliance_pct` |

### Ligne 3 — Check-in & gRPC

| Panneau | Type | Métrique |
|-------|------|--------|
| Check-ins / min | Série temporelle | `rate(vigo_checkins_total[5m]) * 60` |
| gRPC Request Rate | Série temporelle par méthode | `rate(vigo_grpc_requests_total[5m])` |
| gRPC Latency (p50/p95/p99) | Série temporelle | `histogram_quantile` sur `vigo_grpc_request_duration_seconds` |

### Ligne 4 — Convergence & dérive

| Panneau | Type | Métrique |
|-------|------|--------|
| Run Duration (p50/p95) | Série temporelle | `histogram_quantile` sur `vigo_run_duration_seconds` |

| Drift Corrections / min | Série temporelle | `rate(vigo_drift_detected_total[5m]) * 60` |
| Configcrate Actions | Série temporelle par action | `rate(vigo_configcrate_actions_total[5m]) * 60` |

### Ligne 5 — Opérations

| Panneau | Type | Métrique |
|-------|------|--------|
| Enrollments | Série temporelle par statut | `rate(vigo_enrollments_total[1h]) * 60` |
| Task Runs | Série temporelle par statut | `rate(vigo_task_runs_total[5m]) * 60` |
| Workflow Runs | Série temporelle par statut | `rate(vigo_workflow_runs_total[5m]) * 60` |
| Signature Verification Failures | Stat (vert/rouge) | `vigo_sig_verify_failed_total` |

### Ligne 6 — Infrastructure

| Panneau | Type | Métrique |
|-------|------|--------|
| Config Reload Duration | Série temporelle | `vigo_config_reload_duration_seconds` |
| Config Reload Errors | Stat (vert/rouge) | `vigo_config_reload_errors_total` |
| FleetIndex Flush Duration (p50/p95) | Série temporelle | `histogram_quantile` sur `vigo_fleetindex_flush_duration_seconds` |
| FleetIndex Dirty Count | Série temporelle | `vigo_fleetindex_dirty_count` |

### Ligne 7 — Webhooks & SMTP

| Panneau | Type | Métrique |
|-------|------|--------|
| Webhook Deliveries / min | Série temporelle par statut | `rate(vigo_webhook_deliveries_total[5m]) * 60` |
| Webhook Latency (p50/p95) | Série temporelle | `histogram_quantile` sur `vigo_webhook_duration_seconds` |
| SMTP Sends | Série temporelle par statut | `rate(vigo_smtp_sends_total[5m]) * 60` |

### Ligne 8 — Secrets

| Panneau | Type | Métrique |
|-------|------|--------|
| Secret Rotations | Stat | `vigo_secret_rotations_total` |
| Secret Resolution Errors | Stat (vert/rouge) | `vigo_secret_resolution_errors_total` |

### Ligne 9 — Assistant IA

| Panneau | Type | Métrique |
|-------|------|--------|
| AI Requests / min | Série temporelle | `rate(vigo_ai_requests_total[5m]) * 60` |
| AI Tokens Used | Stat | `vigo_ai_tokens_total` |
| AI Latency (p50/p95) | Série temporelle | `histogram_quantile` sur `vigo_ai_request_duration_seconds` |
| AI Errors | Stat (vert/rouge) | `vigo_ai_errors_total` |

### Ligne 10 — Posture de sécurité

| Panneau | Type | Métrique |
|-------|------|--------|
| Critical CVEs | Stat (vert/rouge) | `vigo_security_cve_critical` |
| High CVEs | Stat (vert/orange) | `vigo_security_cve_high` |
| Avg Hardening Score | Stat (rouge < 50, jaune < 80, vert) | `vigo_security_hardening_avg` |
| Rootkit Warnings | Stat (vert/rouge) | `vigo_security_rootkit_warnings` |

### Ligne 11 — Posture de risque

| Panneau | Type | Métrique |
|-------|------|--------|
| Avg Risk Score | Stat (vert < 20, jaune < 40, orange < 70, rouge) | `vigo_risk_avg_score` |
| Max Risk Score | Stat (vert < 20, jaune < 40, orange < 70, rouge) | `vigo_risk_max_score` |

| Low Risk | Stat (vert) | `vigo_risk_distribution{level="low"}` |
| Medium Risk | Stat (bleu) | `vigo_risk_distribution{level="medium"}` |
| High Risk | Stat (orange) | `vigo_risk_distribution{level="high"}` |
| Critical Risk | Stat (rouge) | `vigo_risk_distribution{level="critical"}` |

## Tableau de bord Security Posture

[Télécharger vigo-security.json](../grafana/vigo-security.json)

Tableau de bord de supervision de sécurité dédié avec suivi des CVE, scores de durcissement et détection de menaces.

### Ligne 1 — Vue d'ensemble des CVE

| Panneau | Type | Métrique |
|-------|------|--------|
| Critical CVEs | Stat (vert/rouge) | `vigo_security_cve_critical` |
| High CVEs | Stat (vert/orange) | `vigo_security_cve_high` |
| Medium CVEs | Stat (vert/jaune) | `vigo_security_cve_medium` |
| Low CVEs | Stat (bleu) | `vigo_security_cve_low` |

### Ligne 2 — Durcissement

| Panneau | Type | Métrique |
|-------|------|--------|
| Average Hardening Score | Gauge (0-100, rouge < 50, jaune < 80, vert) | `vigo_security_hardening_avg` |
| Hardening Score Over Time | Série temporelle | `vigo_security_hardening_avg` |

### Ligne 3 — Détection de menaces

| Panneau | Type | Métrique |
|-------|------|--------|
| Rootkit Warnings | Stat (vert/rouge) | `vigo_security_rootkit_warnings` |
| Malware Detections | Stat (vert/rouge) | `vigo_security_malware_detections` |
| Integrity Issues | Stat (vert/orange) | `vigo_security_integrity_issues` |

### Ligne 4 — Couverture

| Panneau | Type | Métrique |
|-------|------|--------|
| Scanned Hosts | Stat (vert) | `vigo_security_scanned_hosts` |
| Total Nodes | Stat (bleu) | `vigo_nodes_total` |

## Tableau de bord Convergence

[Télécharger vigo-convergence.json](../grafana/vigo-convergence.json)

Répartition du statut de convergence par Envoy, tendances de conformité et santé des check-ins.

### Ligne 1 — Répartition des statuts

| Panneau | Type | Métrique |
|-------|------|--------|
| Convergence Status | Diagramme circulaire (vert/jaune/orange/rouge/gris) | `vigo_convergence_converged`, `_degraded`, `_changed`, `_diverged`, `_failed`, `_offline`, `_no_data` |
| Converged | Stat (vert) | `vigo_convergence_converged` |
| Degraded | Stat (jaune) | `vigo_convergence_degraded` |
| Diverged | Stat (rouge) | `vigo_convergence_diverged` |
| Errors | Stat (vert/rouge) | `vigo_convergence_failed` |
| Offline | Stat (vert/orange) | `vigo_convergence_offline` |
| No Data | Stat | `vigo_convergence_no_data` |

### Ligne 2 — Tendances


| Panneau | Type | Métrique |
|-------|------|--------|
| Compliance % | Série temporelle (0-100) | `vigo_convergence_pct * 100` |
| Drift Rate | Série temporelle | `rate(vigo_drift_detected_total[5m])` |

### Ligne 3 — Santé des check-ins

| Panneau | Type | Métrique |
|-------|------|--------|
| Check-in Age Distribution | Heatmap | `vigo_checkin_age_seconds_bucket` |

## Tableau de bord Risk Posture

[Télécharger vigo-risk.json](../grafana/vigo-risk.json)

Score de risque à l'échelle de la flotte avec répartition de la distribution et suivi des tendances.

### Ligne 1 — Risque de la flotte

| Panneau | Type | Métrique |
|-------|------|--------|
| Average Risk Score | Stat (vert < 20, jaune < 40, orange < 70, rouge) | `vigo_risk_avg_score` |
| Max Risk Score | Gauge (0-100, vert < 20, jaune < 40, orange < 70, rouge) | `vigo_risk_max_score` |

### Ligne 2 — Distribution

| Panneau | Type | Métrique |
|-------|------|--------|
| Low Risk | Stat (vert) | `vigo_risk_distribution{level="low"}` |
| Medium Risk | Stat (bleu) | `vigo_risk_distribution{level="medium"}` |
| High Risk | Stat (orange) | `vigo_risk_distribution{level="high"}` |
| Critical Risk | Stat (rouge) | `vigo_risk_distribution{level="critical"}` |

### Ligne 3 — Tendances

| Panneau | Type | Métrique |
|-------|------|--------|
| Risk Score Over Time | Série temporelle (moyenne + maximum) | `vigo_risk_avg_score`, `vigo_risk_max_score` |

## Tableau de bord Publish Bursts

[Télécharger vigo-bursts.json](../grafana/vigo-bursts.json)

Observez une publication de configuration se propager de bout en bout à travers la flotte. Chaque panneau lit une métrique déjà exportée par vigosrv (voir `server/metrics/registry.go`) ; le tableau de bord ajoute des annotations verticales à chaque incrément de `vigo_config_publishes_total` afin que chaque changement en forme de rafale ait une cause visible.

### Ligne 1 — Rafale en un coup d'œil

| Panneau | Type | Métrique |
|-------|------|--------|
| In-flight CheckIns | Stat avec sparkline (vert < 100, jaune ≥ 100, rouge ≥ 1000) | `vigo_grpc_checkin_in_flight` |
| Active Nodes (proxy de taille de vague) | Stat | `vigo_nodes_active_total` |
| Outbound Bundle MB/s | Stat avec sparkline | `sum(rate(vigo_policy_bundle_bytes_total[1m])) / 1048576` |
| Convergence % | Gauge (rouge < 80, jaune < 95, vert ≥ 95) | `vigo_convergence_pct * 100` |

### Ligne 2 — Comportement gRPC

| Panneau | Type | Métrique |
|-------|------|--------|
| gRPC Request Rate (par méthode, code) | Série temporelle | `sum by (method, code) (rate(vigo_grpc_requests_total[1m]))` |
| gRPC Latency (p50 / p95 / p99) | Série temporelle | `histogram_quantile(.., rate(vigo_grpc_request_duration_seconds_bucket[1m]))` |
| TLS Handshakes (par statut) | Série temporelle | `sum by (status) (rate(vigo_grpc_tls_handshakes_total[1m]))` |

### Ligne 3 — Livraison de politique

| Panneau | Type | Métrique |
|-------|------|--------|
| Outbound Bundle Bytes/s | Série temporelle (par méthode) | `sum by (method) (rate(vigo_policy_bundle_bytes_total[1m]))` |
| Bundle Size (p50 / p95 / p99) | Série temporelle | `histogram_quantile(.., rate(vigo_policy_bundle_size_bytes_bucket[5m]))` |
| Force Pushes (par scope) | Série temporelle | `sum by (scope) (rate(vigo_force_pushes_total[1m]))` |

### Ligne 4 — Convergence & authentification

| Panneau | Type | Métrique |
|-------|------|--------|
| Configcrate Actions + Drift Corrections /s | Série temporelle (deux séries) | `rate(vigo_configcrate_actions_total[1m])`, `rate(vigo_drift_detected_total[1m])` |
| Signature Verification Failures | Série temporelle (rouge) | `sum(rate(vigo_sig_verify_failed_total[1m]))` |
| Convergence % over Time | Série temporelle (0-100) | `vigo_convergence_pct * 100` |
| Time to Converge after Publish (p50 / p95 / p99) | Série temporelle (secondes, pleine largeur) | `histogram_quantile(0.95, sum by (le) (rate(vigo_publish_convergence_seconds_bucket[10m])))` |

Le panneau temps-de-convergence mesure les secondes d'horloge écoulées entre le moment où une version de configuration devient courante et sa première application par chaque Envoy — un échantillon par Envoy et par adoption de version vers l'avant (les reconvergences de routine sont exclues). La queue suit votre cadence de check-in : une flotte à long intervalle converge plus lentement car les Envoys ne remarquent le changement qu'à leur prochain check-in. Les échantillons proviennent du chemin de check-in en flux (le mode démon par défaut) ; les invocations ponctuelles `vigo run` ne sont pas comptées, car elles sont initiées par l'opérateur et ne font pas partie d'une vague de publication, et pas davantage les Envoys dont le magasin d'état persistant n'a pas pu s'ouvrir (convergence hors-ligne désactivée — un état dégradé qui journalise un avertissement).

> **Cardinalité des labels.** Chaque panneau ici agrège sur `method` / `code` / `status` / `scope` — des labels à cardinalité fixe. Aucune série par Envoy n'est jamais introduite ; à l'échelle de la flotte (25K+ Envoys), des labels par Envoy feraient exploser le budget de séries de Prometheus.

## Configuration de Prometheus

Ajoutez le serveur Vigo comme cible de scrape dans `prometheus.yml`. `:8443` est en HTTPS, donc le job nécessite `scheme: https` plus un `tls_config` pour le certificat auto-signé :

```yaml
scrape_configs:
  - job_name: vigo
    scheme: https
    metrics_path: /metrics
    tls_config:
      insecure_skip_verify: true   # self-signed server cert
    static_configs:
      - targets: ['vigo-server:8443']
    scrape_interval: 30s
```

## Règles d'alerte

Un fichier de règles d'alerte organisé est livré avec les tableaux de bord Grafana :

```
[grafana/alerts.yaml](../grafana/alerts.yaml)
```

Dix-neuf règles Prometheus couvrant la convergence (failed / degraded / diverged / offline), les exécutions (durée p95), la sécurité (échecs de vérif-signature, CVE critiques), l'opérabilité (erreurs de rechargement de config, erreurs de résolution de secret), la licence (erreurs d'application, jours restants, étape), Swarm (roster des Bolts Spanner) et les rafales (taux gRPC soutenu, pic de handshakes TLS, échecs de vérif-signature, niveau haut de CheckIns en vol, p95 publication→convergence dépassant le SLO). Chaque règle référence une métrique définie dans `server/metrics/registry.go` ; les seuils sont des points de départ — ajustez-les à votre flotte.

Chargez dans un Prometheus standard :

```yaml
# prometheus.yml
rule_files:
  - "/etc/prometheus/vigo-alerts.yaml"
```

Ou importez dans Grafana via Alerting → Alert rules → Import. Routez les alertes vers PagerDuty / Slack / ServiceNow / Loki via la section [integrations](#integrations) ci-dessus.

## Et ensuite

- **Une métrique est élevée et vous ne savez pas quel Envoy** → explorez via `vigocli envoys list` puis la page de détail de l'Envoy.
- **Vous devez router une alerte vers PagerDuty / Slack / ServiceNow** → voir la section integrations ci-dessus ; même bloc de configuration, point d'accès différent.
- **Vous voulez des événements sous forme de logs interrogeables aux côtés des métriques** → [Envoyer des événements vers Grafana Loki](loki-integration.md).
- **Une valeur de métrique semble fausse** → [Dépanner les problèmes courants](troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel -- Alexander4, LLC. Reproduction interdite. Voir la [licence](../../legal/license.md).*
