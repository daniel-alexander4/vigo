<!-- vigo-i18n source_sha:fba1ff6f4def8e5d8dd1dd3d45ca86b51c75228b604eee6561af4af7c744e5c6 -->

---
title: Corréler les logs serveur + agent par trace_id
---

# Corréler les logs serveur + agent par trace_id

À la fin de cette page, vous saurez prendre un seul check-in, rapport, force-push ou dispatch de tâche et interroger depuis un seul endroit l'**intégralité** de l'ensemble des lignes de log serveur + agent qui le concernent — typiquement via Loki avec `{trace_id="…"}`.

**Quand l'utiliser :** vous avez vu un run en échec, un check-in lent ou un événement de drift inattendu dans le tableau de bord ou dans `vigocli runs`, et vous voulez chaque ligne de log que les deux processus ont écrite à son sujet. Ou un force-push depuis `vigocli push` a déclenché un problème à l'échelle de la flotte et vous voulez suivre le parcours d'un seul Envoy à travers l'incident.

**Quand vous en passer :** vous êtes déjà satisfait du tailing par processus. La corrélation des logs ne vous coûte rien — elle est toujours active — mais vous n'en tirez de la valeur qu'une fois que vous interrogez le même `trace_id` à travers les deux sources.

## Ce qui est réellement câblé

Chaque requête initiée par l'agent (`CheckIn`, `ReportResult`, `ReportTraits`) et chaque événement poussé par le serveur que l'agent traite (`TaskDispatch`, `ForcePush`) porte une chaîne W3C `traceparent` dans le corps du message proto. Le serveur et l'agent extraient tous deux la portion `trace_id` (32 hex) et l'attachent à :

- **Serveur** — chaque ligne de log `slog` pour cette opération. Champ : `trace_id`.
- **Serveur** — la charge utile de l'événement d'intégration à `Event.Details["trace_id"]` pour chaque `run.*`, `drift.detected`, `resource.blocked`. Loki, Splunk, Sentinel, etc. la voient tous.
- **Agent** — chaque ligne de log `tracing` émise sous le span de l'opération. Même nom de champ : `trace_id`.

`TaskDispatch` fait l'aller-retour de bout en bout : le serveur génère le `trace_id` lors du dispatch, l'agent l'utilise pour les logs d'exécution, et le renvoie dans `TaskResult` — ainsi le « received task result » côté serveur et le « executing task » côté agent partagent le même champ.

Le format est conforme au standard W3C `traceparent` (`00-<trace_id_hex>-<span_id_hex>-<flags>`). La portion trace_id (32 caractères hex minuscules) est ce sur quoi vous faites vos requêtes. Voir l'ADR-028 pour le raisonnement sur le placement du champ.

## Interroger dans Loki

Si vous avez suivi [Acheminer les événements vers Grafana Loki](loki-integration.md), `trace_id` est déjà dans votre charge utile d'événement. Dans Explore :

```logql
{service="vigo"} | json | trace_id="b3a1c0f2eb1a4c7e8d9f0123abcdef01"
```

Pour remonter d'un événement spécifique vers tous les logs le concernant :

1. Démarrez dans Loki Explore avec `{service="vigo"} | json`.
2. Filtrez sur l'événement qui vous intéresse — p. ex. `| event_name="run.failure"`.
3. Sélectionnez le champ `trace_id` dans la charge utile JSON, puis étendez vers une nouvelle requête : `| trace_id="<value>"`.
4. Supprimez le filtre sur le nom d'événement — vous avez maintenant la chaîne complète par opération à travers le serveur et l'agent.

Les logs côté agent n'arrivent dans Loki que si vous acheminez aussi les logs hôte de l'agent vers Loki (Promtail, Vector, Grafana Alloy — n'importe quel expéditeur de logs hôte) ; l'agent lui-même n'a pas d'intégration Loki. Une fois que c'est fait, le champ `trace_id` est déjà présent car l'agent l'émet sur chaque ligne de log `tracing` de l'opération. Interrogez de la même façon.

## Interroger directement les logs vigosrv

Si vous êtes en SSH sur l'hôte vigosrv ou si vous exécutez `docker logs vigo` :

```bash
docker logs vigo 2>&1 | grep 'trace_id=b3a1c0f2eb1a4c7e8d9f0123abcdef01'
```

Le formateur texte `slog` émet les attributs sous forme `key=value`, donc un simple `grep` fonctionne. Côté agent (`journalctl -u vigo` sous systemd, sinon `/var/log/vigo/agent.log`) :

```bash
journalctl -u vigo | grep 'trace_id="b3a1c0f2eb1a4c7e8d9f0123abcdef01"'
```

Le crate `tracing` émet les attributs sous la forme `key="value"`, d'où les guillemets.

## Limitations connues

- **Table d'audit.** Le journal d'audit chaîné par hash dans SQLite ne porte **pas** de `trace_id` par enregistrement — cela nécessiterait une migration de schéma et un bump du format de chaîne. À la place, chaque entrée d'audit est diffusée comme un événement d'intégration `audit.<eventType>` qui **inclut** bien le `trace_id` issu du scope de la requête (`server/audit/` → `Writer.OnRecord` → `integrations.Dispatch`). Interrogez l'historique d'audit via Loki, pas via `vigocli audit` pour l'instant.
- **Sessions scrier `TunnelStream`.** Les sessions SSH/RDP/VNC pilotées par navigateur passent par `TunnelStream`, qui est un relais d'octets et hors périmètre pour cette version. Une session Scrier a son propre ID de session ; le trace_id y arrivera dans une évolution ultérieure.
- **Workflows déclenchés par trait.** Lorsqu'un changement de trait déclenche un workflow (`server/grpc/traits.go` → `checkTraitTriggers`), le workflow s'exécute dans sa propre goroutine avec un contexte neuf. Le `trace_id` de `ReportTraits` ne se propage **pas** dans les logs du workflow — le workflow est logiquement sa propre opération. Utilisez le `run_id` du workflow pour trouver ses logs, puis remontez vers le rapport de trait via `envoy_id` + fenêtre temporelle si nécessaire.
- **Agents plus anciens.** Un agent qui n'envoie pas encore de `traceparent` se voit simplement attacher une valeur générée par le serveur à la frontière de la requête. Aucune rupture de protocole.

## Comment vérifier que ça fonctionne

Après une mise à niveau vers la version 0.66.50 ou ultérieure, faites un force-push sur un Envoy et faites le tail des deux logs :

```bash
# Terminal 1: server
docker logs -f vigo 2>&1 | grep trace_id

# Terminal 2: agent
ssh <envoy> 'sudo journalctl -fu vigo' | grep trace_id

# Terminal 3
vigocli push --envoy <envoy>
```

Vous devriez voir le serveur logger un trace_id issu d'un `TaskDispatch`, le « received force push via stream » de l'agent porter la même valeur, et le cycle de check-in / rapport suivant générer ses propres trace_ids frais (chacun étant sa propre opération logique).
