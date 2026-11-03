<!-- vigo-i18n source_sha:c1c916efa94131fd05f9dd6cf4d24710d65cea6f485ebed79f8e979a22c8fd4a -->

# Haute disponibilité

**Qu'arrive-t-il à la flotte si le serveur tombe en panne ?** Moins que vous ne le craignez. Les Envoys continuent de converger depuis leur cache, et vous disposez de deux moyens indépendants de maintenir le plan de contrôle lui-même en vie.

## D'abord : l'agent n'a pas besoin du serveur à chaque instant

C'est le fondement. Chaque agent met en cache son dernier [PolicyBundle](architecture.md), ses traits et une file de résultats dans un magasin LMDB embarqué. Lorsque le serveur est injoignable, l'agent **réapplique le bundle mis en cache contre des traits frais, met les résultats en file et les vide à la reconnexion** — avec un backoff exponentiel et sans plantage. Une panne du serveur dégrade l'observabilité et les nouveaux pushs, pas l'application.

## Ensuite : deux façons de maintenir le serveur en service

Ces deux mécanismes résolvent des problèmes différents — ne choisissez pas l'un en pensant qu'il fait le travail de l'autre.

| | **Réplication par pairs** (`server/peer/`) | **[Spanner](spanner-federation.md)** (`server/spanner/`) |
|---|---|---|
| Problème | Le serveur tombe en panne | Un seul serveur ne peut pas contenir toute la flotte |
| Forme | Réplicas primaire → secours de la **même** flotte | Pairs égaux en écriture, **différentes** tranches de flotte |
| Réplique | `stack/`, `secrets/`, `tls/`, `server.yaml` | le registre des admissions (CRDT) ; **pas** les secrets/TLS |
| Bascule | `vigocli server promote` | s.o. — les pairs sont déjà égaux |

Vous pouvez exécuter les deux : HA *au sein* d'une partition, Spanner *entre* les partitions.

## État durable en dessous

La base de données SQLite du serveur fonctionne en mode WAL avec une réplication WAL **Litestream** optionnelle vers un stockage compatible S3 — de sorte que même une perte totale de l'hôte se restaure à un point récent. Les sauvegardes (`vigocli backup`) capturent la base de données, les configurations, les secrets et le TLS comme une seule unité.

## Le scénario de récupération, de bout en bout

1. Le serveur tombe en panne → les Envoys continuent de converger depuis le cache, mettant les résultats en file.
2. Un secours est promu (peer) ou un Bolt frère couvre déjà la tranche (Spanner).
3. Le nouveau primaire restaure l'état (répliqué, ou depuis Litestream/sauvegarde).
4. Les Envoys se reconnectent, vident leurs résultats en file et reprennent leurs check-ins normaux.

Aucun Envoy n'a jamais été « hors service » — seulement non observé.

## Où cela apparaît

- [Sauvegarde et récupération](../howto/operate/backup-and-recovery.md) · [Reprise après sinistre](../howto/operate/disaster-recovery.md).
- [Fédération Spanner](spanner-federation.md) — l'axe de montée en charge.
- [`vigocli server`](../reference/vigocli/server.md) · [`vigocli backup`](../reference/vigocli/backup.md).
