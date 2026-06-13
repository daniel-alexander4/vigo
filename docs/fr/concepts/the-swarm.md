<!-- vigo-i18n source_sha:d2fb12b07d960130f31e24e40ee9bd758ee0a1123a1c66c1ef95654a5929eb49 -->

# Le Swarm

**Pourquoi les envoys se parlent-ils, et qu'est-ce qui circule entre eux ?**

L'essentiel de Vigo repose sur des échanges serveur↔agent. Le **Swarm** fait exception : c'est un substrat pair-à-pair où les Envoys échangent du contenu directement, en mTLS, sans router chaque octet par le serveur. Il existe pour que la distribution d'une image de 2 Go, d'un dépôt git ou d'un fichier à l'échelle de la flotte ne sature pas un seul serveur — le blob circule de nœud à nœud, le serveur ne publie que des pointeurs.

## Un substrat, six sous-systèmes de contenu

Le substrat gère la gestion des seeds, le gossip CRDT des manifestes et le suivi des pairs. Six sous-systèmes l'utilisent (les agents servent les blobs en mTLS sur le port 1531) :

| Sous-système | Ce qu'il déplace | Confidentialité |
|---|---|---|
| **filecast** | Distribution de fichiers à la flotte poussée par l'admin | — |
| **gitback** | Git via le Swarm (dépôts personnels de DR, ADR-015) | disponibilité, pas confidentialité |
| **longdrawer** | Synchro de répertoire par utilisateur en LAN, en clair | aucune (LAN uniquement) |
| **lockbox** | Synchro de répertoire par utilisateur, chiffrée au repos | chiffré partout |
| **curator** | Registre d'artefacts P2P — binaires, images (ADR-024) | signé, vérifiable |
| **poolq** | File à journal ordonné P2P, façon Kafka (ADR-029) | signé, rejouable |

Les six sont enracinés dans une primitive d'identité unique — le [puddle](puddle-identity.md).

## Comment il reste digne de confiance sans serveur de confiance

Le serveur **publie, il n'arbitre jamais.** Il agrège ce que les agents rapportent (via des traits) en vues de flotte en lecture seule, mais les agents vérifient chaque signature et chaque chaîne de délégation **de bout en bout** — ils ne se fient pas à la parole du serveur. L'agrégateur mesh d'un sous-système de contenu (`gitbackmesh`, `curatormesh`, …) est une *vue*, pas une autorité.

## Autorisation à deux couches

Les deux couches doivent dire oui ; refus par défaut sur les deux :

1. **Couche Envoy** — le sous-système est-il activé sur cet Envoy ? (liste de motifs `server.yaml#swarm.*.enabled` confrontée au nom d'hôte).
2. **Couche utilisateur** — quels utilisateurs Unix peuvent l'utiliser ? (drapeaux usercrate `puddle:` / `gitback:` / `curator:`).

Le levier destructeur est le **drapeau usercrate** — le retirer efface les données détenues par l'utilisateur lors de la prochaine réconciliation. Les changements de liste de motifs n'agissent que sur l'accès ; les données détenues survivent.

## Où cela apparaît

- [Identité puddle](puddle-identity.md) — l'ancre par utilisateur sur laquelle tout repose.
- [Mettre en place le Swarm](../howto/swarm/set-up-swarm.md), puis [gitback](../howto/swarm/set-up-gitback.md) · [lockbox](../howto/swarm/set-up-lockbox.md) · [curator](../howto/swarm/set-up-curator.md) · [poolq](../howto/swarm/set-up-poolq.md) · [longdrawer](../howto/swarm/set-up-longdrawer.md).
- Les pages d'interface `/swarm/*` et [`vigocli swarm`](../reference/vigocli/swarm.md).

---
*Confidentiel — Alexander4, LLC. Reproduction interdite.*
