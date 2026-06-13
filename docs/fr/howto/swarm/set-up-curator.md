<!-- vigo-i18n source_sha:e9992ff9b2df17103c5bd4ac38dc4c75159f15fe02643ca3e4dffad8f45c901a -->

# Mettre en place Curator

À la fin de cette page, vous aurez un artefact (binaire, archive, paquet, image de conteneur) publié depuis un envoy et récupérable sur tous les envoys de la flotte — en pair-à-pair quand c'est joignable, relayé par le serveur sinon. Le blob est signé par le Puddle du publieur et vérifié de bout en bout à chaque lecture. Les lectures sont universelles ; les écritures sont réservées aux membres du Puddle.

**Quand l'utiliser :** chaque fois que les mêmes octets doivent atterrir sur plus d'un envoy — binaires d'agent, archives tar de fournisseurs, paquets `.deb` / `.rpm` personnalisés, fichiers de modèles ML, images de conteneurs, gros fixtures. Curator gagne sa place quand un serveur de fichiers HTTP est l'alternative que vous cherchez à éviter.

**Quand le sauter :** secrets et identifiants (utilisez [lockbox](set-up-lockbox.md) ou le préfixe de config `secret:`) ; état qui devrait vivre comme un dépôt git (utilisez [gitback](set-up-gitback.md)) ; fichiers de config poussés ponctuellement par l'admin (utilisez Filecast).

## L'activer

Par envoy : adhésion via `server.yaml` :

```yaml
swarm:
  curator:
    enabled:
      - "*"                   # pattern list, first-match-wins; "-" prefix denies
    max_bytes: 2147483648     # per-blob cap; default 2 GiB
```

Par utilisateur : accordez `curator: true` sur la usercrate (plus lourd que `gitback:` — accès en écriture à un registre d'artefacts lisible par toute la flotte) :

```yaml
resources:
  - type: user
    username: dan
    puddle: true              # required
    curator: true             # opt-in for curator publish
```

Publiez (`sudo vigocli config publish`).

## L'utiliser

Depuis un envoy où l'opérateur a un Puddle déverrouillé :

```bash
# Publish a binary or archive (the implicit namespace is the operator's puddle name):
vigo swarm curator push vigo-agent:1.2.3 ./vigo-agent-linux-amd64 \
  --platform linux/amd64

# Tag mutably:
vigo swarm curator tag vigo-agent:latest 1.2.3

# Resolve / pull anywhere (no puddle needed for read):
vigo swarm curator resolve vigo-agent:latest      # show coordinates
vigo swarm curator pull vigo-agent:latest         # fetch to /var/lib/vigo/artifacts/...

# Operator views:
vigo swarm curator list                           # everything published
vigo swarm curator inspect vigo-agent             # full signed entry
vigo swarm curator versions vigo-agent
```

Depuis le serveur admin (`vigocli`) — mêmes verbes plus la modération :

```bash
vigocli swarm curator list
vigocli swarm curator block <artifact_id>         # server refuses to resolve/relay
vigocli swarm curator unblock <artifact_id>
```

## Comment ça marche

![Curator — la publication est conditionnée par l'appartenance au Puddle ; la lecture est universelle via le serveur avec récupération d'octets en P2P ou relayée par le serveur](../../img/curator.svg)

Curator combine deux primitives de flotte existantes : le substrat de blobs du Swarm (adressé par contenu, fragmenté, restreint par cible, P2P) gère les *octets*, et la machinerie d'identité/d'accès clés-Puddle de gitback gère le *qui-peut-faire-quoi*. Une fine couche de catalogue signé assemble le tout.

### Identité

`artifact_id = sha256(founder_puddle_pubkey ‖ name)` — même forme qu'un ID de projet gitback, de sorte que deux opérateurs peuvent tous deux publier `redis` sans collision. Référence destinée à l'humain : `<founder-puddle-name>/<name>` (se résout en une pubkey via la carte de noms de flotte de l'ADR-022, puis en un artifact_id). Le Puddle propre à l'opérateur est l'espace de noms par défaut implicite — `vigo-agent:latest` ≡ `<your-puddle>/vigo-agent:latest`. La forme hexadécimale `<artifact_id>/<name>` fonctionne toujours (auto-certifiante).

### Entrée de catalogue

Une enveloppe signée par artefact, re-signée en dernière-écriture-gagne à chaque mutation :

```jsonc
{
  "body": {
    "name": "vigo-agent",
    "founder_puddle_pubkey": "<hex 32-byte ed25519>",   // frozen forever
    "signing_puddle_pubkey": "<hex 32-byte ed25519>",   // == founder until rekey
    "versions": {
      "1.2.3": { "platforms": [
        { "os": "linux",  "arch": "amd64", "blob_sha": "<hex>", "size": 12345678 },
        { "os": "darwin", "arch": "arm64", "blob_sha": "<hex>", "size": 13402011 }
      ]}
    },
    "tags":   { "latest": "1.2.3", "stable": "1.2.0" },   // mutable
    "target": "*",                                         // which envoys hold the DR replica
    "issued_at": 1714521600000                             // unix ms; LWW key
  },
  "signature": "<hex 64-byte ed25519 sig over canonical_bytes(body)>"
}
```

La signature s'ancre à la pubkey du fondateur : tout envoy qui vérifie l'entrée la rejette si la signature ne remonte pas (via la chaîne pont-de-rekey de l'ADR-019) jusqu'à la pubkey qui hache l'artifact_id. Un fondateur ne peut pas muter l'artefact d'un autre fondateur.

### Publication — chemin d'écriture

`vigo swarm curator push <name>:<version> <file>` s'exécute en tant qu'utilisateur réel avec `curator: true` sur sa usercrate et un Puddle déverrouillé. La CLI :

1. Hache le fichier (`sha256` + taille), puis dépose le fichier ainsi qu'une entrée de catalogue re-signée dans le démon assistant à `/var/run/vigo/curator.sock`.
2. L'assistant (root) valide la signature + la chaîne de délégation de bout en bout, confirme que les octets diffusés correspondent à `blob_sha` / `size`, et applique le plafond par blob `swarm.curator.max_bytes` (2 Gio par défaut ; à relever pour les images de conteneurs).
3. Met le blob en attente dans le cache de blobs du Swarm et enregistre une entrée de manifeste avec la `target` de l'artefact — le distributeur Filecast existant fait alors essaimer les octets vers les envoys correspondants où le Swarm est activé, exactement comme un blob Filecast.
4. Dépose l'entrée signée sous `~<user>/.vigo-curator/<artifact_id>/entry.json.pending`. Le réacteur de sentinelle root (balayage de 60 s) la re-vérifie et la promeut en `entry.json`, où le collecteur de traits de l'agent la récupère → l'agrégateur de catalogue côté serveur → le catalogue de flotte.

Les re-signatures qui n'embarquent pas de nouveaux octets (`tag` / `untag` / `set-recipients` / `set-target` / `rm`) empruntent le même socket via des verbes ENTRY purement en-tête. `set-target` ré-enregistre aussi la graine de manifeste pour les blobs que cet envoy a semés, de sorte que la distribution vers le nouvel ensemble cible se déclenche à la passe suivante.

### Lecture — universelle, médiée par le serveur

Chaque envoy (Swarm activé ou non) se résout via le serveur :

```
GET /api/v1/swarm/curator/resolve?artifact_id=<hex>&tag=<t>|&version=<v>&os=<os>&arch=<arch>
```

Le serveur renvoie `{blob_sha, size, signed_entry}` depuis son catalogue agrégé à TTL de 30 secondes (block/unblock invalide le cache). L'agent récupère ensuite les octets :

- **Envoy avec Swarm activé** — en P2P depuis le cache du Swarm (il détient généralement déjà la réplique DR parce que `target` y correspondait).
- **Envoy avec Swarm désactivé** — `GET /api/v1/swarm/curator/blob/<sha>` depuis le serveur, qui relaie depuis l'envoy co-résident du serveur. Restreint aux sha du catalogue courant uniquement — pas une porte dérobée vers des blobs arbitraires du Swarm.

Dans les deux cas, l'agent vérifie le sha et matérialise dans `/var/lib/vigo/artifacts/<artifact_id>/<name>/<version>/<os>-<arch>` (mode 0644), en mettant à jour `/var/lib/vigo/artifacts/cache.json` avec les alias `:<tag>` et `@<version>`.

Les agents mettent aussi en cache le catalogue de flotte dans `/var/lib/vigo/artifacts/catalog.json` (rafraîchi à chaque check-in), de sorte qu'un envoy avec Swarm activé peut résoudre et matérialiser **localement** pendant une panne du serveur tout artefact qu'il détient déjà.

### Autorisation

Deux couches conditionnent la publication. Les deux doivent dire oui :

| Couche | Où | Question |
|---|---|---|
| Envoy | liste de patterns `server.yaml#swarm.curator.enabled` | Curator est-il disponible sur cet envoy, tout court ? |
| Utilisateur | drapeau usercrate `curator: bool` | Cet utilisateur Unix peut-il publier ? |

Plus la couche du Puddle-opérateur au moment de la signature : l'assistant de session du Puddle de l'utilisateur doit être en cours d'exécution (`vigo swarm puddle unlock`).

`curator: true` est une autorisation **plus lourde** que `gitback: true` — « peut publier vers le registre de la flotte, lisible par la flotte par défaut » contre « peut fonder des dépôts git personnels ». La retirer d'une usercrate déclenche un nettoyage à la prochaine réconciliation (`~/.vigo-curator/` effacé pour cet utilisateur). Les artefacts déjà publiés restent ; vous ne pouvez pas les rappeler en perdant un drapeau.

La lecture est **non conditionnée** — les artefacts sont lisibles par la flotte. `pull` matérialise dans une arborescence appartenant à root, il requiert donc `sudo`.

Le filet de sécurité de modération par l'opérateur est `vigocli swarm curator block <ref>` (admin uniquement). Le serveur cesse de résoudre/relayer l'artefact bloqué à l'échelle de la flotte, quel qu'en soit le publieur. L'entrée signée existe toujours et se propage en gossip, et les octets DR restent sur les envoys avec Swarm activé — seul un `vigo swarm curator rm` du publieur purge réellement.

### Quitter Curator / décommissionner un utilisateur

Contrairement à longdrawer/lockbox, il n'y a pas d'état par utilisateur sous `/var/lib/vigo/` à orpheliner — l'état côté root de Curator (`/var/lib/vigo/artifacts/catalog.json`, `cache.json`, l'arborescence de blobs matérialisés) est global à la flotte, pas partitionné par utilisateur. Et `userdel -r <user>` supprime `~/.vigo-curator/` en même temps que le reste du répertoire personnel, donc le cas courant s'auto-nettoie.

Ce qui SUBSISTE après un `userdel -r` si aucun pré-nettoyage n'a été fait : les **annonces de graines côté manifeste** de cet envoy pour chaque blob publié par l'utilisateur parti. Les caches de flotte des pairs continuent de résoudre ces entrées jusqu'à ce que le prochain GC de manifeste les fasse expirer, et `vigocli swarm curator fleet` continuera de lister l'utilisateur parti comme semeur.

**Procédure avant `userdel -r`** — en tant qu'utilisateur, exécutez :

```bash
vigo swarm curator purge --yes      # wipes ~/.vigo-curator/ + revokes this envoy's manifest seeds
```

Ensuite `userdel -r` est sûr. `purge` ne dépublie PAS à l'échelle de la flotte — les pairs conservent la dernière entrée qu'ils ont vue. Pour une rétractation complète d'un artefact spécifique, le publieur exécute `vigo swarm curator rm <artifact_id>` (signe et propage en gossip une pierre tombale) AVANT `purge`.

Si `userdel -r` a déjà eu lieu sans `purge` préalable : le `~/.vigo-curator/` de l'utilisateur a disparu, les entrées publiées restent dans le catalogue de flotte de chaque pair pour toujours (jusqu'à ce que l'identité du Puddle publieur revienne les rétracter — typiquement jamais). La modération par l'opérateur `vigocli swarm curator block <ref>` est le seul remède à l'échelle de la flotte.

### Surfaces CLI

Deux clients minces du même backend :

- **`vigo swarm curator`** (côté agent, s'exécute en tant qu'utilisateur) — surface fondateur + consommateur. Les verbes mutateurs signent via l'assistant du Puddle et soumettent au socket local. Les verbes de lecture (`status`, `list`, `inspect`, `versions`, `resolve`, `pull`) fonctionnent localement + contre le serveur. Les verbes de nettoyage (`stale`, `prune`, `gc`, `purge`) assainissent l'envoy.
- **`vigocli swarm curator`** (côté serveur, front-end opérateur) — vues de lecture + modération admin (`block` / `unblock`). Les verbes mutateurs ne vivent délibérément pas ici — ils ont besoin d'un Puddle par utilisateur déverrouillé, ce qui ne cadre pas avec la posture root-seulement / auth-par-pair de vigocli.

### Accélérer les mises à jour d'agent (0.51.8+)

Curator peut absorber le coût en bande passante des auto-mises à jour de l'agent. Le flux :

1. Après avoir compilé un nouvel agent en cross-compilation, poussez le binaire résultant comme artefact Curator sous votre Puddle d'opérateur, une plateforme par cible de cross-compilation. Le nom de l'artefact est **`vigo-agent`** et le tag est la chaîne de version — ce sont les conventions que l'agent recherche.

   ```bash
   vigo swarm curator push vigo-agent:0.51.8 ./dist/agent/linux/amd64/vigo --platform linux/amd64
   vigo swarm curator push vigo-agent:0.51.8 ./dist/agent/linux/arm64/vigo --platform linux/arm64
   # ... one push per platform you ship
   ```

2. À chaque check-in, lorsqu'un envoy décide de s'auto-mettre à jour vers la version V, il demande d'abord à Curator `vigo-agent:V` pour sa plateforme hôte. Si le catalogue dit oui et que le `blob_sha` de l'artefact correspond à ce que rapporte le `/api/v1/agent/latest` du serveur, l'envoy récupère les octets en P2P depuis le cache local du Swarm (ou via le relais du serveur s'il a le Swarm désactivé) au lieu de les re-télécharger depuis `/dist/agent/<os>/<arch>/vigo`.

3. L'agent vérifie le SHA256 + la signature binaire ed25519 sur les octets quelle qu'en soit la provenance. **Le point de terminaison REST reste l'autorité** — si l'artefact Curator est absent, si les SHA ne correspondent pas, ou si Curator est cassé de quelque façon que ce soit, l'agent retombe silencieusement sur `/dist/agent/<os>/<arch>/vigo` et s'auto-met à jour comme il l'a toujours fait.

C'est opportuniste. Vous n'avez jamais à publier `vigo-agent` pour que la flotte se mette à jour ; vous le faites quand vous voulez épargner l'egress de votre serveur sur une montée de version à l'échelle de la flotte. Un `blob_sha` non concordant (vous avez poussé le mauvais build) produit un seul `warn!` dans le journal de l'agent et retombe sur REST — l'artefact publié est simplement ignoré jusqu'à ce que vous le repoussiez.

### Récupérer des images de conteneurs via Curator

Lorsque `swarm.curator.local_port` est réglé sur une valeur non nulle dans `server.yaml`, l'agent expose un shim OCI Distribution v2 local sur `127.0.0.1:<port>` qui sert les artefacts Curator comme un registre Docker. Cela signifie qu'une ressource `container:` peut utiliser `artifact:` au lieu de `image:` :

```yaml
- name: redis-cache
  type: container
  container: redis-cache
  artifact: alexander4/redis:7.2-alpine    # pulled via the curator shim
  state: running
  ports: "6379:6379"
  healthcheck_command: "redis-cli ping"
  restart: unless-stopped
```

L'agent traduit `artifact: <ns>/<name>:<tag>` en `docker pull 127.0.0.1:<port>/<ns>/<name>:<tag>` au moment de l'application. Pour publier des images de conteneurs, poussez-les comme archives OCI (`docker save --format=oci`) avec `--kind oci-archive`, et `vigo swarm curator push` comme n'importe quel autre artefact. `image:` (une référence de registre normale) et `artifact:` sont mutuellement exclusifs — choisissez l'un par ressource. Voir [`container`](../../reference/vigo/resources/container.md) pour la surface complète des paramètres.

### Héberger un dépôt de paquets privé

Le même écouteur `swarm.curator.local_port` sert aussi une **façade de dépôt de paquets** à `http://127.0.0.1:<port>/repo/<name>/…` — un dépôt apt/dnf/apk privé, distribué en P2P, sans serveur de dépôt (pas d'Aptly, reprepro, Pulp ni nginx). Utilisez-le pour héberger *vos propres* paquets `.deb`/`.rpm`/`.apk` — builds maison, tiers vendorisés, ensembles air-gapped. (Pointer des hôtes vers un dépôt de distribution *amont* reste le travail de la ressource [`repository`](../../reference/vigo/resources/repository.md) ; ceci concerne les dépôts que *vous* publiez.)

**Publier.** `vigo swarm curator repo-publish` construit l'arborescence du dépôt, signe les métadonnées, les met en tar, et les publie comme artefact Curator — une seule commande depuis un répertoire de paquets en vrac :

```sh
# ./debs/ holds your .deb files; the signing key is your own gnupg key.
vigo swarm curator repo-publish acme-repo \
    --dir ./debs --kind apt-repo --sign-key ACME-SIGNING-KEY-ID
```

`repo-publish` invoque la chaîne d'outils correspondant à `--kind` — `dpkg-scanpackages` + `gpg` pour `apt-repo`, `createrepo_c` + `gpg` pour `dnf-repo`, `apk index` + `abuild-sign` pour `apk-repo` — donc l'hôte de publication doit l'avoir installée (un outil manquant donne une erreur claire « install … »). `--sign-key` est un id de clé GPG pour `apt-repo`/`dnf-repo` et un chemin de clé privée RSA abuild pour `apk-repo` (les dépôts apk sont signés en RSA, pas en GPG). `--suite`/`--component` valent par défaut `stable`/`main` (apt uniquement) ; `--version` vaut par défaut la date UTC du jour, `--tag` vaut `latest`. `--kind` est fixé une fois à la première publication et est immuable. Les URL apt/dnf/apk ne portent pas de tag, donc la façade sert toujours le tag `latest`.

`repo-publish` est délibérément mince — une suite, un composant, les architectures présentes dans `--dir`. Pour la longue traîne (paquets source, composants multiples, ajouts incrémentaux), construisez vous-même l'arborescence signée avec les outils standard, empaquetez-la comme un **`.tar` simple et non compressé** (la façade exige du non compressé), incluez la clé publique de signature comme fichier dans l'arborescence pour que les consommateurs puissent la récupérer depuis la même façade, et `vigo swarm curator push <name> --file <tar> --version <v> --tag latest --os linux --arch all --kind apt-repo` (une archive tar de dépôt est indépendante de l'architecture — `--os linux --arch all` est un placeholder que la façade ignore).

**Consommer.** Pointez une ressource `repository` vers la façade — pas de nouveau type de ressource, l'existant importe déjà une clé de signature et écrit l'entrée de source :

```yaml
- name: acme-internal
  type: repository
  repository: acme-internal
  repo: "deb http://127.0.0.1:5000/repo/alexander4/acme-repo stable main"
  key_url: "http://127.0.0.1:5000/repo/alexander4/acme-repo/key.asc"
```

À partir de là, c'est de l'`apt` natif : `apt update` puis `apt install <your-package>` résout les dépendances et récupère les `.deb` depuis le dépôt hébergé par Curator. `dnf` et `apk` fonctionnent de la même façon contre un artefact `dnf-repo` / `apk-repo`. Pointer un hôte `apt` vers un `dnf-repo` (ou l'inverse) échoue rapidement avec une erreur claire de non-correspondance de kind plutôt qu'un 404 déroutant.

**Mise en garde.** Un dépôt est un seul artefact tarball — ajouter un paquet signifie reconstruire et republier tout le dépôt comme nouvelle version d'artefact. Très bien pour des dépôts privés modestes ; relevez `swarm.curator.max_bytes` pour les gros.

### Exposer des artefacts via une API S3

`swarm.curator.s3_port` ouvre un point de terminaison HTTPS compatible S3 en lecture seule (ADR-027) afin que des clients **hors envoy** — runners CI, portables de dev, tout ce qui possède un SDK S3 — puissent récupérer des artefacts Curator. Toute chaîne d'outils S3 fonctionne contre lui sans modification. Il réutilise le matériel TLS du serveur et requiert le backend de secrets `local`.

Activez-le dans `server.yaml` (`0`, la valeur par défaut, le désactive) :

```yaml
swarm:
  curator:
    s3_port: 9000
```

Émettez un identifiant — admin uniquement ; le secret est **dérivé, jamais stocké**, et affiché une seule fois :

```
$ vigocli swarm curator s3-credentials create --name ci-runner
Curator S3 credential created.
  Access key ID:     VIGO2X4QPL7N8K3M9R6T
  Secret access key: 9f3a…  (shown once — store it now)
  Scope:             *
```

Pointez n'importe quel client S3 vers le point de terminaison avec ces clés :

```
$ aws --endpoint-url https://vigo.example.com:9000 s3 ls s3://vigo-curator/
$ aws --endpoint-url https://vigo.example.com:9000 s3 cp \
    s3://vigo-curator/alexander4/vigo-agent/0.66.28/linux-amd64 ./vigo
```

Les clés d'objet sont `<publisher>/<artifact>/<version-or-tag>/<os>-<arch>` — `<publisher>` est le nom convivial du Puddle quand il est revendiqué sans ambiguïté, sinon l'hexadécimal de la pubkey du fondateur (les deux formes se résolvent sur `GetObject`). `HeadObject` porte le kind de l'artefact dans un en-tête `x-amz-meta-vigo-kind`.

L'API est en **lecture seule** — chaque verbe d'écriture ou de gestion renvoie un `405` S3 ; la publication reste `vigo swarm curator push`. Un identifiant peut être restreint à un seul publieur (`s3-credentials create --scope <publisher>`) ; les artefacts bloqués par l'opérateur sont invisibles via S3 aussi. Notez que le `GetObject` d'un artefact de kind dépôt (`apt-repo` / `dnf-repo` / `apk-repo`) renvoie le *tarball* du dépôt — pour consommer un dépôt apt/dnf/apk privé de manière navigable, pointez une ressource `repository` vers la façade `/repo/` locale à l'agent ci-dessus, pas vers le point de terminaison S3. La rotation de la clé maîtresse re-dérive chaque secret S3, donc les opérateurs réémettent les identifiants après une rotation.

### Où regarder quand ça ne marche pas

- **`vigo swarm curator status`** — état du cache local, pushes en cours, vivacité du démon assistant.
- **`vigocli swarm curator list / fleet`** — vue côté serveur de qui a publié quoi, qui a récupéré quoi, l'ensemble bloqué.
- **Les journaux de l'agent** portent un WARN spécifique par échec actionnable (`sha mismatch`, `blob exceeds max_bytes`, `entry signature invalid`, `target glob matched zero envoys`).
- **Pression disque** : comme les autres sous-systèmes du Swarm, Curator saute les pushes vers les pairs à ≤10 % d'espace disque libre. Voir [Espace disque](set-up-swarm.md#disk-space).

## Et ensuite

- **Référencer des artefacts depuis une ressource `package` / `source_package` / `file` d'une configcrate** → `resources/package` dans le vocabulaire de référence. Les URI Curator fonctionnent partout où une URL de téléchargement convient.
- **Le blob dépasse 2 Gio** → relevez `swarm.curator.max_bytes` par envoy. Republiez ; les tags se ré-ancrent automatiquement.
- **Rappeler une mauvaise release** → `vigocli swarm curator block <artifact_id>` bloque la résolution à l'échelle de la flotte ; seul un `rm` du publieur purge réellement les octets.
- **`pull` se bloque ou produit des non-concordances de sha** → [Résoudre les problèmes courants](../operate/troubleshoot.md).

---
*Vérifié sur Vigo 0.66.28 · 2026-05-22.*

*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [../legal/license.md](../../legal/license.md).*
