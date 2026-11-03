<!-- vigo-i18n source_sha:ab8e6393de2a62ec7ea478a460d0ff31b808a0a0cdcc9f89b5ee83c984af0f0f -->

# Mettre en place un Spanner (fédération entre pairs égaux)

Un spanner est un ensemble de serveurs vigosrv — les **bolts** — qui partagent une même flotte en tant que pairs. Chaque bolt possède une partition de la flotte par motif de nom d'hôte, détient le registre complet des admissions (répliqué en CRDT par gossip) et répond aux requêtes d'administration à l'échelle de la flotte en agrégeant à partir de ce registre. Il n'y a ni hub ni primaire : chaque bolt exécute un code identique et est administré directement.

**Quand l'utiliser :** lorsqu'un serveur unique ne peut plus tenir confortablement la cadence de check-in de la flotte, la charge de requêtes ou la rétention de l'historique de convergence. Les points d'inflexion pratiques se situent généralement au-delà de ~5 000 Envoys par serveur, ou lorsque vous souhaitez un partitionnement géographique afin qu'une panne régionale ne mette pas tout le plan de contrôle hors service.

**Quand l'éviter :** les flottes plus petites (un déploiement `standalone` à serveur unique est le défaut et la bonne réponse pour presque tous les opérateurs) ; les flottes qui ont besoin de HA mais pas de sharding horizontal (utilisez plutôt la réplication primaire/secondaire de `server/peer/` — voir la section `peers:` dans `server.yaml`).

> **Exigence de licence :** Spanner nécessite une licence **commerciale**. Tout palier non commercial — la licence gratuite intégrée (100 Envoys), une licence communautaire/illimitée émise à la main, ou tout palier qui n'exige pas de liaison machine — ne prend pas en charge le mode `spanner` ; le serveur refuse de démarrer avec `spanner.mode: spanner` sur une telle licence. Les licences commerciales sont liées à un serveur spécifique via une [empreinte machine](troubleshoot.md#machine-fingerprint-binding) — déplacer un bolt vers une nouvelle machine nécessite une nouvelle licence.

## Modes

Deux modes, définis par serveur dans `server.yaml` :

- `standalone` (défaut) — un seul vigosrv gère toute la flotte. Aucune configuration spanner nécessaire.
- `spanner` — ce vigosrv est un bolt pair-égal : il possède une partition par motif de nom d'hôte et partage le registre des admissions avec tous les autres bolts.

Les modes `founder`/`joiner` d'avant la Phase 3 sont retirés — « founder » n'a jamais été que « celui qui a exécuté `vigocli spanner init` en premier », un fait historique, pas un rôle d'exécution. Le `server.yaml` de chaque bolt a la même forme.

## Configurer chaque bolt

Le `server.yaml` de chaque bolt :

```yaml
spanner:
  mode: spanner
  spanner_id: alexander4-prod        # the federation id — identical on every bolt
  patterns: ["*.east.example.com"]   # the hostname partition THIS bolt owns
  peers: ["10.0.0.2", "10.0.0.3"]    # config-driven hands-off peering (optional) — see below
  snapshot:
    tier1_interval: 30s
    tier2_interval: 5m
  transport:
    multicast_group: "224.0.0.45"
    multicast_port: 1534
  fallback_admin: local
```

`spanner_id` doit correspondre sur tous les bolts. Les `patterns` de chaque bolt ne doivent chevaucher ceux d'aucun autre bolt — le chargeur refuse une configuration avec chevauchement au moment de la publication et au démarrage.

Il existe deux façons d'admettre des bolts dans le spanner : le **peering piloté par la configuration** (`spanner.peers`, sans intervention — recommandé) et la **cérémonie manuelle d'invitation/jonction** (un jeton à usage unique). Les deux sont routées par le fondateur : le fondateur (celui qui a exécuté `vigocli spanner init`) est la seule autorité d'admission, et une fois un bolt admis, le registre répliqué en CRDT est dès lors la liste des pairs. Choisissez l'une ; vous n'avez pas besoin des deux.

## Fonder le spanner

Sur le premier serveur, après avoir défini `mode: spanner` + `spanner_id` + `patterns` et redémarré :

```bash
vigocli spanner init
```

Cela écrit la ligne d'auto-admission du bolt fondateur, signée avec l'identité Ed25519 du bolt (`/srv/vigo/bolt/identity.wrapped`, générée au premier démarrage en mode spanner).

## Établir la CA de flotte partagée

Quel que soit le chemin d'admission que vous utilisez, chaque bolt doit partager **une seule CA de flotte** — le mTLS entre pairs et le check-in des Envoys routés se vérifient tous deux contre elle. La clé privée de la CA est établie **hors bande ; elle ne traverse jamais le réseau.** Sur le fondateur :

```bash
vigocli spanner ca export --out fleet-ca.pem   # CA cert + private key PEM bundle, 0600
```

Le lot contient la clé privée la plus précieuse de la flotte — gardez-le en `0600` et déplacez-le sur un canal sécurisé (scp, pas par e-mail). Sur chaque serveur qui rejoint :

```bash
vigocli spanner ca import --from fleet-ca.pem  # installs the CA + re-signs this bolt's server cert from it
```

Redémarrez vigosrv sur le serveur qui rejoint ensuite pour charger la CA adoptée.

Garder la CA hors du réseau est ce qui rend sûre la barrière d'admission pilotée par la configuration (ci-dessous) : un bolt malveillant qui parvient à franchir la liste d'autorisation d'IP est borné — il peut polluer l'observabilité et jouer avec le décompte des licences, mais il ne peut pas lire les données de la flotte, recevoir des Envoys routés, ni générer des certificats de confiance, car tout cela se trouve derrière le mTLS de la CA de flotte qu'il ne détient pas.

## Admettre les autres bolts — peering piloté par la configuration (recommandé)

Définissez `spanner.peers` et redémarrez ; pas de jetons, pas de cérémonie. La liste est **asymétrique** :

- Sur le **fondateur**, `peers` est la *liste d'autorisation de qui-peut-rejoindre* — listez l'adresse de chaque bolt qui rejoint. La requête d'admission au démarrage d'un bolt qui rejoint n'est honorée que si son adresse source correspond à une entrée ici.
- Sur chaque **bolt qui rejoint**, `peers` pointe vers le fondateur (vous pouvez aussi lister les autres bolts pour le gossip). Après avoir défini `mode: spanner`, le `spanner_id` partagé, ses propres `patterns` sans chevauchement, et importé la CA de flotte, redémarrez.

Au démarrage, un bolt qui rejoint et qui n'a pas encore de registre fait un POST de son identité auto-signée vers chaque pair jusqu'à ce que le fondateur l'admette et renvoie un registre signé par le fondateur ; le bolt qui rejoint le fusionne en CRDT et commence à diffuser par gossip. Cela s'exécute hors du chemin critique du check-in avec retry/backoff, de sorte qu'un fondateur brièvement injoignable est toléré. Un bolt qui a déjà un registre (le fondateur, ou un bolt qui rejoint et qui s'est déjà amorcé) ne fait rien.

Les entrées de `spanner.peers` sont au format `host` ou `host:port` (port REST HTTPS, `8443` par défaut).

## Admettre les autres bolts — cérémonie manuelle d'invitation/jonction

Si vous préférez distribuer des jetons à usage unique plutôt que de lister des adresses, laissez `spanner.peers` vide et exécutez la cérémonie. Sur n'importe quel **membre existant**, générez un lot d'invitation à usage unique :

```bash
vigocli spanner bolt invite --ttl 24h
```

Le lot est un secret — quiconque le détient peut rejoindre le spanner jusqu'à son expiration ou sa consommation. Remettez-le à l'opérateur du serveur qui rejoint, hors bande.

Sur le **serveur qui rejoint** — après avoir défini son `server.yaml` (`mode: spanner`, le `spanner_id` partagé, ses propres `patterns` sans chevauchement) et redémarré :

```bash
vigocli spanner bolt join --from https://member.example.com:8443 --code "$(cat bundle.txt)"
```

Le serveur qui rejoint présente le jeton au membre, reçoit une ligne d'admission signée plus le registre actuel, et commence à participer au CRDT des admissions diffusé par gossip. Le jeton est à usage unique — le G-Set `consumed_tokens` garantit qu'il ne peut jamais être consommé deux fois.

## L'utiliser

`vigocli spanner status` affiche le mode de ce bolt, sa clé publique d'identité, et l'observabilité adossée au gossip pour chaque bolt pair — nombre d'Envoys, répartition de la convergence, et l'ancienneté du dernier instantané de chaque pair.

```bash
vigocli spanner bolt list            # the admissions roster
vigocli spanner bolt show <pubkey>   # one bolt's convergence breakdown
vigocli spanner bolt envoys <pubkey> # the envoys that bolt serves
```

Les requêtes d'administration à l'échelle de la flotte — `vigocli envoys list`, `vigocli runs`, force-push — fonctionnent contre n'importe quel bolt. Chaque bolt répond en agrégeant : `fallback_admin: local` (le défaut) fusionne le cache d'observabilité adossé au gossip ; `fallback_admin: fanout` interroge en direct le gRPC d'administration de chaque bolt du registre. Les deux produisent la même réponse. (`vigocli runs` fait toujours du fan-out — les runs ne sont jamais diffusés par gossip.)

## Routage des enrôlements

Lorsqu'un Envoy s'amorce contre un bolt quelconque, ce bolt détermine quel bolt du registre possède le nom d'hôte de l'Envoy (premier-correspond-gagne sur les `patterns` de chaque bolt, le bolt admis le plus anciennement gagne en cas de chevauchement). Si un bolt **différent** le possède, le bolt contacté enrôle l'Envoy sur le bolt propriétaire et renvoie son adresse ; l'agent fait son check-in là-bas dès lors. Aucun hub ne médie cela — chaque bolt calcule le même propriétaire à partir du registre répliqué en CRDT.

## Réassignation et drainage d'Envoys

Déplacer un Envoy entre bolts (les ids de bolt sont des clés publiques en hexadécimal issues de `vigocli spanner bolt list`) :

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

Il n'y a pas de sonde de santé côté hub ni de drainage automatique. Un bolt silencieux est détecté par les seuils de fraîcheur du gossip (`spanner.freshness.stale_after`) et apparaît dans `vigocli spanner status` comme une ancienneté d'instantané périmée. La récupération est automatique lorsque le bolt reprend la diffusion par gossip. Pour retirer un bolt du service délibérément, drainez-le (ci-dessus) avant de l'arrêter.

## Application de la licence

Avec `license.mode: aggregate`, chaque bolt somme indépendamment le nombre de nœuds à l'échelle de la flotte à partir du registre répliqué par gossip et applique la licence contre ce total — il n'y a pas d'applicateur désigné. Avec `license.mode: local` (le défaut), chaque bolt ne compte que ses propres Envoys.

## Interface web

La page `/spanner` liste chaque bolt du spanner — une ligne par bolt, le bolt qui sert la page étant étiqueté **this bolt**. Les lignes des pairs proviennent du cache d'observabilité adossé au gossip (avec une ancienneté d'instantané) ; la ligne locale est en direct depuis l'index de flotte de ce bolt, de sorte que la vue est identique quel que soit le bolt dont vous ouvrez l'UI. `/spanner/bolts/{pubkey}` détaille la répartition de la convergence d'un bolt et son registre d'Envoys.

## Voir aussi

- [Configuration du serveur](../../reference/server-yaml.md) — la section `spanner:`
- ADR-026 : admissions entre pairs égaux — la conception

## Et ensuite

- **HA pour un bolt individuel** → réplication primaire/secondaire `server/peer/`, configurée par bolt indépendamment du spanner.
- **Un bolt est bloqué** → `vigocli spanner status` affiche une ancienneté d'instantané périmée ; drainez-le avec `vigocli spanner bolt drain <pubkey>`.
- **La licence est trop petite / du mauvais palier** → spanner nécessite une licence commerciale ; une licence gratuite ou de palier communautaire est refusée quel que soit le nombre de nœuds. Voir [troubleshoot.md#machine-fingerprint-binding](troubleshoot.md#machine-fingerprint-binding) pour les reliaisons d'empreinte.
- **La convergence inter-bolts est en retard** → la cadence du gossip de compteurs Tier-1 est de `30s` par défaut ; réduisez `spanner.snapshot.tier1_interval` pour une convergence inter-bolts plus serrée.

---
*Vérifié sur Vigo 0.66.12 · 2026-05-21.*
