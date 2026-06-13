<!-- vigo-i18n source_sha:62d99560f518d533896d47c955ee02840bba737d13136e886561e8215226f448 -->

# Identité Puddle

**À qui appartiennent ces données, d'une machine à l'autre ?** Cette question doit trouver une réponse avant qu'un quelconque sous-système pair-à-pair puisse fonctionner. Le **Puddle** est cette réponse (ADR-014).

## La réponse courte

Un Puddle est **l'identité d'un seul utilisateur Unix, partagée entre les Envoys qu'il utilise** — une unique paire de clés Ed25519, générée une seule fois, transportée sur chaque machine. Chaque sous-système de contenu du Swarm ([gitback](the-swarm.md), Curator, Poolq, Lockbox, Longdrawer) est ancré à une clé publique de Puddle, de sorte que « cet artefact est le mien » et « ces machines sont les miennes » ont un sens cryptographique qui ne dépend pas du serveur.

La moitié privée est protégée par phrase secrète dans `~/.vigo-puddle/identity.wrapped`. Il n'existe **aucun registre central de Puddles** — l'appartenance est le simple fait que la même clé publique est présente sur N Envoys.

## Comment une identité se propage vers une seconde machine

**Par appairage entre pairs**, et non par copie d'une clé d'une machine à l'autre :

1. Sur une machine qui possède déjà le Puddle, `vigo swarm puddle pair` émet un code à usage unique valable 5 minutes.
2. Sur la nouvelle machine, `vigo swarm puddle join --from <host>` réclame le code via le mTLS du Swarm et reçoit la clé protégée. La phrase secrète ne quitte jamais la machine de destination.

Un `session helper` conserve la clé déprotégée en mémoire pendant que vous travaillez (délai d'inactivité, surveillance du PID parent), afin que les sous-systèmes puissent signer sans nouvelle invite.

## Noms conviviaux (ADR-022)

Une clé publique de Puddle est peu maniable, aussi un Puddle peut-il revendiquer un nom humain (`dan`, `acme`) — une revendication signée qui se propage sous forme de trait et se résout à l'échelle de la flotte. Les noms sont **attribués au premier arrivé et de façon coopérative** : le serveur publie toutes les revendications et n'arbitre rien, de sorte qu'un nom contesté se résout en *ambigu* (la clé publique hexadécimale reste la preuve), jamais en *erroné*.

## Pourquoi des clés par utilisateur et par Envoy (et non une clé unique de flotte)

Le cloisonnement. Un seul Envoy compromis ne doit pas permettre de déchiffrer tout le corpus d'un Puddle — c'est pourquoi [lockbox](the-swarm.md) conserve délibérément une clé *distincte* par Envoy sous le Puddle. Le Puddle est l'identité ; il n'est pas lui-même la clé de déchiffrement de tout.

## Où cela apparaît

- [Le Swarm](the-swarm.md) — ce que les Puddles autorisent.
- [Configurer un Puddle](../howto/swarm/set-up-puddle.md) — appairer, rejoindre, nommer.
- L'interface `/swarm/puddle` + [`vigocli swarm puddle`](../reference/vigocli/swarm-puddle.md) — vue de la flotte.

---
*Confidentiel — Alexander4, LLC. Ne pas redistribuer.*
