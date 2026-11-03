<!-- vigo-i18n source_sha:c0637bf20ec88a1762cb5895b6ec5cf42d355361ba2ac71befbb9eda6559c361 -->

---
title: Acheminer les événements vers Grafana Loki
---

# Acheminer les événements vers Grafana Loki

À la fin de cette page, le serveur Vigo poussera chaque événement (`envoy.offline`, `run.failure`, `security.cve.critical`, `audit.*`, etc.) vers une instance Loki sous forme de flux de logs étiquetés que vous pourrez interroger en LogQL.

**Quand l'utiliser :** toute personne qui exploite déjà une stack Grafana et qui veut le flux d'événements de Vigo aux côtés de ses autres logs applicatifs — même Grafana, mêmes routes d'alerte, même politique de rétention. Particulièrement utile pour les événements `audit.*` : le journal d'audit chaîné par hachage reste la source de vérité dans SQLite, mais Loki vous offre l'interrogation ad hoc et l'exploration de type Explore sans écrire de SQL.

**Quand s'en passer :** toutes vos alertes routent déjà vers Slack / PagerDuty / Sentinel / Splunk via les [autres intégrations](set-up-monitoring.md#integrations) — Loki est une cible de plus, pas un remplacement.

## Comment cela s'intègre

Loki est une cible de plus sur le dispatcher d'événements de Vigo. Même tuyauterie que Splunk HEC, Datadog, Sentinel, Elastic — chaque événement émis par le serveur est diffusé vers tout ce qui est activé. La cible Loki :

- Pousse vers `<url>/loki/api/v1/push` au format JSON streams.
- N'utilise que des **étiquettes à faible cardinalité** : `service=vigo`, `severity=<info|warning|critical>`, `event_name=<envoy.offline|run.failure|audit.config_published|...>`.
- Place les **données à forte cardinalité** (`hostname`, `envoy_id`, `summary`, `details`) dans le corps de la ligne de log sous forme de JSON — à interroger avec le parseur `| json` de LogQL.
- Fait le pont avec la **piste d'audit** : chaque entrée que l'écrivain d'audit chaîne dans SQLite est aussi dispatchée comme `audit.<eventType>`, de sorte qu'elle apparaît dans Loki aux côtés de tout le reste.
- Transporte un **`trace_id`** par opération dans `Details`, ce qui vous permet de pivoter depuis n'importe quel événement vers chaque ligne de log côté serveur et côté agent pour le même check-in / report / dispatch de tâche — voir [Corréler les logs serveur + agent par trace_id](log-correlation.md).

Cette forme évite l'anti-pattern Loki le plus courant : des étiquettes à forte cardinalité (une valeur d'étiquette par Envoy) qui font exploser l'index.

## Configuration

Ajoutez un bloc `loki:` sous `integrations:` dans `server.yaml` :

```yaml
integrations:
  loki:
    enabled: true
    url: "https://logs-prod-us-central1.grafana.net"
    tenant_id: "123456"              # X-Scope-OrgID for multi-tenant Loki; omit for single-tenant
    username: "123456"               # basic-auth username (Grafana Cloud: your instance ID)
    password: "secret:vigo/loki/password"
    # bearer_token: "secret:vigo/loki/token"  # alternative to basic-auth; mutually exclusive
    events: []                        # empty = forward everything (recommended)
```

Puis publiez :

```sh
vigocli config publish
```

Vigo recharge le dispatcher d'intégrations ; l'événement suivant est le premier que Loki voit.

### Modes d'authentification

| Mode | À configurer | Quand |
|---|---|---|
| Non authentifié | laisser `username`, `password`, `bearer_token` vides | Loki sur un réseau privé sans proxy d'authentification |
| Basic-auth | `username` + `password` | Loki derrière nginx, Grafana Cloud Logs (username = ID d'instance) |
| Bearer | `bearer_token` | Loki derrière un proxy OAuth/JWT |

`tenant_id` est indépendant du mode d'authentification — définissez-le dès que le déploiement Loki est multi-tenant (X-Scope-OrgID y est requis).

### Sélection des événements

`events: []` transfère tout. Pour restreindre :

```yaml
    events: ["envoy.offline", "run.failure", "security.cve.critical", "config.reload.failure"]
```

Pour exclure la diffusion d'audit (volumineuse sur les flottes actives) :

```yaml
    # Exclude audit.* by listing only non-audit events; there is no negative pattern.
    events: ["envoy.offline", "envoy.online", "run.failure", "run.success", "drift.detected",
             "security.cve.critical", "security.cve.high", "config.reload.failure",
             "convergence.threshold", "secret.rotated"]
```

## Interrogation

Sélecteur de flux :

```logql
{service="vigo"}
```

Par sévérité :

```logql
{service="vigo", severity="critical"}
```

Uniquement la piste d'audit :

```logql
{service="vigo", event_name=~"audit\\..*"}
```

Filtrer par nom d'hôte (dans le corps, pas une étiquette) :

```logql
{service="vigo"} | json | hostname="web-01"
```

Compter les événements par Envoy sur la dernière heure :

```logql
sum by (hostname) (count_over_time({service="vigo"} | json [1h]))
```

## Alertes via Loki Ruler

Si vous exploitez le [Ruler](https://grafana.com/docs/loki/latest/alert/) de Loki et préférez l'alerting basé sur les logs aux règles d'alerte Prometheus de [grafana/alerts.yaml](../grafana/alerts.yaml) :

```yaml
# loki-rules.yaml
groups:
  - name: vigo-loki
    rules:
      - alert: VigoSigVerifyFailureRate
        expr: |
          sum(rate({service="vigo", event_name="security.sig_verify_failed"} [5m])) > 0
        labels: { severity: critical }
        annotations:
          summary: "Signature verification failures detected"

      - alert: VigoConfigReloadFailures
        expr: |
          sum(rate({service="vigo", event_name="config.reload.failure"} [15m])) > 0
        labels: { severity: warning }
        annotations:
          summary: "Vigo config reload failed"
```

Les fichiers de règles Prometheus et Loki n'entrent pas en conflit — la plupart des opérateurs déploient les deux : Prometheus pour la santé dérivée des métriques (comptes, pourcentages, latences), Loki pour les événements dérivés des logs (échecs, conditions rares).

## Notes opérationnelles

- **La livraison est au mieux, asynchrone.** Chaque événement est dispatché dans une goroutine avec un délai d'expiration de 10 secondes par cible. Les échecs de livraison sont journalisés au niveau `slog.Error` (`integration dispatch failed target=loki ...`) ; ils ne bloquent ni le check-in ni la convergence.
- **La suppression s'applique.** La suppression d'événements à l'échelle de la flotte déjà en place dans Vigo (10 événements identiques ou plus provenant de différents Envoys en 60 s sont regroupés en un seul) s'applique avant le dispatch — Loki voit le même flux supprimé que toute autre cible d'intégration.
- **Loki rejette l'ingestion si les étiquettes d'un flux changent.** N'ajoutez pas d'étiquettes personnalisées ici sans réfléchir ; les trois étiquettes (service/severity/event_name) sont délibérément stables.
- **Le matériel TLS est réutilisé.** Le client HTTP utilise le transport Go standard — Vigo fait confiance à tout ce qui se trouve dans le bundle de CA du système. Il n'existe aujourd'hui aucun moyen d'épingler une CA personnalisée pour la seule cible Loki ; si votre Loki utilise une CA privée, installez-la à l'échelle du système sur l'hôte vigosrv.

## Vérifier

Après publication :

1. Surveillez les logs du serveur à la recherche de `integrations dispatcher enabled targets=N` (le compte inclut Loki lorsqu'il est activé).
2. Déclenchez un événement synthétique — force-push d'un configcrate qui ne s'appliquera pas, ou révoquez + ré-enrôlez un Envoy de test.
3. Dans Grafana, ouvrez Explore → Loki → `{service="vigo"}` et confirmez que la ligne arrive.
4. Exécutez `vigocli config publish` sur des stacks inchangés — cela déclenche un événement `audit.config_published` que vous trouverez sous `{service="vigo", event_name="audit.config_published"}`.

Si Loki renvoie un `4xx`, vérifiez dans les logs du serveur la présence de `integration dispatch failed target=loki` ; le message d'erreur transporte le code de réponse de Loki (généralement 401 / 403 pour l'authentification, ou 400 pour un en-tête de tenant malformé).
