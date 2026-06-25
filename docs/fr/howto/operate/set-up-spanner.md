<!-- vigo-i18n source_sha:dd6dffe09bb3f0e9f346e7723edff416c1d021822c8d487d27bc38141d99581f -->

# Mettre en place Spanner (fédération entre pairs égaux)

Un spanner est un ensemble de serveurs vigosrv — les **bolts** — qui partagent une même flotte en tant que pairs. Chaque bolt possède une partition d'Envoys par motif de nom d'hôte, détient le registre complet des admissions (répliqué en CRDT par gossip) et répond aux requêtes d'administration à l'échelle de la flotte en agrégeant à partir de ce registre. Il n'y a ni hub ni primaire : chaque bolt exécute le même code et s'administre directement.

**Quand l'utiliser :** lorsqu'un seul serveur ne peut plus absorber confortablement la cadence de check-in de la flotte, la charge de requêtes ou la rétention de l'historique de convergence. Les points d'inflexion pratiques se situent généralement au-delà de ~5 000 Envoys par serveur, ou lorsque vous souhaitez un partitionnement géographique afin qu'une panne régionale ne mette pas tout le plan de contrôle hors service.

**Quand l'éviter :** les flottes plus petites (un déploiement `standalone` à serveur unique est le choix par défaut et la bonne réponse pour presque tous les opérateurs) ; les flottes qui ont besoin de HA mais pas de sharding horizontal (utilisez plutôt la réplication primaire/secondaire de `server/peer/` — voir la section `peers:` dans `server.yaml`).

> **Exigence de licence :** Spanner nécessite une licence payante. La licence gratuite intégrée (100 Envoys) ne prend pas en charge le mode `spanner` ; le serveur refuse de démarrer avec `spanner.mode: spanner` sous la licence gratuite. Les licences payantes sont liées à un serveur spécifique via l'[empreinte machine](troubleshoot.md#machine-fingerprint-binding) — déplacer un bolt vers une nouvelle machine nécessite une nouvelle licence.

## Modes

Deux modes, définis par serveur dans `server.yaml` :

- `standalone` (par défaut) — un seul vigosrv gère toute la flotte. Aucune configuration spanner nécessaire.
- `spanner` — ce vigosrv est un bolt pair-égal : il possède une partition par motif de nom d'hôte et partage le registre des admissions avec tous les autres bolts.

Les modes `founder`/`joiner` antérieurs à la Phase 3 sont retirés — « founder » n'a jamais été que « celui qui a exécuté `vigocli spanner init` en premier », un fait historique, pas un rôle d'exécution. Le `server.yaml` de chaque bolt a la même forme.

## Configurer chaque bolt

Le `server.yaml` de chaque bolt :

```yaml
spanner:
  mode: spanner
  spanner_id: alexander4-prod        # the federation id — identical on every bolt
  patterns: ["*.east.example.com"]   # the hostname partition THIS bolt owns
  snapshot:
    tier1_interval: 30s
    tier2_interval: 5m
  transport:
    multicast_group: "224.0.0.45"
    multicast_port: 1534
  fallback_admin: local
```

`spanner_id` doit correspondre sur chaque bolt. Les `patterns` de chaque bolt ne doivent chevaucher ceux d'aucun autre bolt — le chargeur refuse une configuration en chevauchement à la publication comme au démarrage.

La découverte des pairs est pilotée par gossip. Il n'y a pas de liste de pairs d'amorçage dans `server.yaml` : un bolt qui rejoint apprend le registre via la cérémonie d'admission ci-dessous, et le registre répliqué en CRDT constitue la liste des pairs à partir de là.

## Fonder le spanner

Sur le premier serveur, après avoir défini `mode: spanner` + `spanner_id` + `patterns` et redémarré :

```bash
vigocli spanner init
```

Cela écrit la ligne d'auto-admission du bolt fondateur, signée avec l'identité Ed25519 du bolt (`/srv/vigo/bolt/identity.wrapped`, générée au premier démarrage en mode spanner).

## Admettre les autres bolts

Pour chaque serveur supplémentaire, exécutez la cérémonie d'admission. Sur n'importe quel **membre existant**, générez un paquet d'invitation à usage unique :

```bash
vigocli spanner bolt invite --ttl 24h
```

Le paquet est un secret — quiconque le détient peut rejoindre le spanner jusqu'à son expiration ou sa consommation. Remettez-le hors bande à l'opérateur du serveur qui rejoint.

Sur le **serveur qui rejoint** — après avoir défini son `server.yaml` (`mode: spanner`, le `spanner_id` partagé, ses propres `patterns` non chevauchants) et redémarré :

```bash
vigocli spanner bolt join --from https://member.example.com:8443 --code "$(cat bundle.txt)"
```

Le serveur qui rejoint présente le jeton au membre, reçoit une ligne d'admission signée ainsi que le registre actuel, et commence à participer au CRDT des admissions diffusé par gossip. Le jeton est à usage unique — le G-Set `consumed_tokens` garantit qu'il ne peut jamais être consommé deux fois.

## L'utiliser

`vigocli spanner status` affiche le mode de ce bolt, la clé publique de son identité, et l'observabilité adossée au gossip pour chaque bolt pair — nombre d'Envoys, répartition de convergence, et l'ancienneté du dernier instantané de chaque pair.

```bash
vigocli spanner bolt list            # the admissions roster
vigocli spanner bolt show <pubkey>   # one bolt's convergence breakdown
vigocli spanner bolt envoys <pubkey> # the envoys that bolt serves
```

Les requêtes d'administration à l'échelle de la flotte — `vigocli envoys list`, `vigocli runs`, force-push — fonctionnent contre n'importe quel bolt. Chaque bolt répond en agrégeant : `fallback_admin: local` (par défaut) fusionne le cache d'observabilité adossé au gossip ; `fallback_admin: fanout` interroge en direct le gRPC d'administration de chaque bolt du registre. Les deux produisent la même réponse. (`vigocli runs` fait toujours du fan-out — les runs ne sont jamais diffusés par gossip.)

## Routage des enrôlements

Lorsqu'un Envoy s'amorce contre n'importe quel bolt, ce bolt détermine quel bolt du registre possède le nom d'hôte de l'Envoy (premier-match-gagne sur les `patterns` de chaque bolt, le bolt admis le plus anciennement l'emporte en cas de chevauchement). Si un bolt **différent** le possède, le bolt contacté enrôle l'Envoy sur le bolt propriétaire et renvoie son adresse ; l'agent fait son check-in là-bas à partir de ce moment. Aucun hub ne médiatise cela — chaque bolt calcule le même propriétaire à partir du registre répliqué en CRDT.

## Réassignation et drainage des Envoys

Déplacer un Envoy entre bolts (les ids de bolt sont la clé publique en hexadécimal issue de `vigocli spanner bolt list`) :

```bash
vigocli spanner bolt reassign <envoy-id> --from <bolt-pubkey> --to <bolt-pubkey>
```

Drainer tous les Envoys d'un bolt — pour maintenance ou mise hors service :

```bash
vigocli spanner bolt drain <bolt-pubkey>            # auto-route each envoy by hostname
vigocli spanner bolt drain <bolt-pubkey> --to <bolt-pubkey>
```

L'agent gère la reconnexion automatiquement — à son prochain check-in, il est redirigé vers le nouveau bolt.

## Gestion des pannes

Il n'y a pas de sonde de santé côté hub ni de drainage automatique. Un bolt silencieux est détecté par les seuils de fraîcheur du gossip (`spanner.freshness.stale_after`) et apparaît dans `vigocli spanner status` sous la forme d'une ancienneté d'instantané obsolète. La reprise est automatique lorsque le bolt recommence à diffuser par gossip. Pour retirer délibérément un bolt du service, drainez-le (ci-dessus) avant de l'arrêter.

## Application de la licence

Avec `license.mode: aggregate`, chaque bolt additionne indépendamment le nombre de nœuds à l'échelle de la flotte à partir du registre répliqué par gossip et applique la limite en conséquence — il n'y a pas d'applicateur désigné. Avec `license.mode: local` (par défaut), chaque bolt ne compte que ses propres Envoys.

## Interface web

La page `/spanner` affiche une ligne par bolt pair à partir du cache d'observabilité adossé au gossip ; `/spanner/bolts/{pubkey}` détaille la répartition de convergence d'un bolt et son registre d'Envoys.

## Voir aussi

- [Configuration du serveur](../../reference/server-yaml.md) — la section `spanner:`
- ADR-026 : admissions entre pairs égaux — la conception

## Et ensuite

- **HA pour un bolt individuel** → réplication primaire/secondaire de `server/peer/`, configurée par bolt indépendamment du spanner.
- **Un bolt est bloqué** → `vigocli spanner status` affiche une ancienneté d'instantané obsolète ; drainez-le avec `vigocli spanner bolt drain <pubkey>`.
- **La licence est trop petite** → spanner nécessite une licence payante ; voir [troubleshoot.md#machine-fingerprint-binding](troubleshoot.md#machine-fingerprint-binding) pour les reliaisons d'empreinte.
- **La convergence inter-bolts est en retard** → la cadence de counter-gossip Tier-1 est de `30s` par défaut ; réduisez `spanner.snapshot.tier1_interval` pour une convergence inter-bolts plus serrée.

---
*Vérifié sur Vigo 0.66.12 · 2026-05-21.*

*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [../legal/license.md](../../legal/license.md).*
