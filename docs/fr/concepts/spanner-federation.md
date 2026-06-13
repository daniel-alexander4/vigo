<!-- vigo-i18n source_sha:75901714d6c4ca60a6520d55c77bfd889d4fda40f5eae360c6311b9cf198f61f -->

# Fédération Spanner

**Que se passe-t-il quand un seul serveur ne suffit pas ?** Vous fédérez. **Spanner** (ADR-026) permet à plusieurs serveurs d'agir comme un seul plan de contrôle — non pas un primaire avec des réplicas, mais des **pairs égaux en écriture**, chacun possédant une tranche de la flotte.

## La réponse courte

En mode `standalone`, un seul `vigosrv` possède tout. En mode `spanner`, chaque serveur est un **Bolt** — un pair égal en écriture qui possède une partition d'Envoys par motif de nom d'hôte et détient le **registre d'admissions complet** (répliqué par CRDT). Il n'y a pas de hub : les lectures admin, le routage d'enrôlement, le force-push, la réassignation et le drainage sont tous égaux entre pairs, pilotés par un cache d'observabilité alimenté par gossip et du gRPC direct de pair à pair.

C'est ainsi que la montée en charge à cadence serrée de Vigo passe de « des dizaines de milliers d'Envoys par serveur » à **illimité** — ajoutez des Bolts.

## Comment les pairs restent cohérents sans leader

Deux niveaux de gossip :

- **Niveau 1** — gossip de compteurs en multicast (léger, fréquent) pour la vivacité et l'observabilité.
- **Niveau 2** — extraction REST d'instantanés du registre pour l'état d'admissions complet.

Chaque Bolt a sa propre identité Ed25519 ; il signe sa ligne d'admission et chaque instantané de registre diffusé par gossip, et les pairs épinglent la clé publique de chaque Bolt lors de l'admission. Un G-Set `consumed_tokens` (CRDT à croissance seule) impose des jetons d'adhésion à usage unique — un jeton de jonction ne peut pas être rejoué.

## Spanner n'est pas de la haute disponibilité

Ne confondez pas les deux :

- **[La haute disponibilité](high-availability.md)** (`server/peer/`) réplique l'état de la *même* flotte (config, secrets, TLS) vers des réplicas de secours pour le basculement.
- **Spanner** partitionne *différentes* tranches de flotte entre les pairs et ne **réplique pas** les secrets ni le matériel TLS entre eux.

Vous pouvez exécuter les deux : la HA au sein d'une partition, Spanner entre les partitions.

## Adhésion

`init` fonde la fédération ; `invite` génère un jeton de jonction à usage unique ; `join` le réclame. Le vocabulaire hub-spoke retiré (`role`, `hub_addr`, …) provoque une erreur bloquante avec un pointeur de migration.

## Où cela apparaît

- [Configurer Spanner](../howto/operate/set-up-spanner.md) — fonder, inviter, joindre.
- L'interface `/spanner` + [`vigocli spanner`](../reference/vigocli/spanner.md).
- [Haute disponibilité](high-availability.md) — l'autre axe de la montée en charge.

---
*Confidentiel — Alexander4, LLC. Ne pas redistribuer.*
