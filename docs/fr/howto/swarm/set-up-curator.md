<!-- vigo-i18n source_sha:e9992ff9b2df17103c5bd4ac38dc4c75159f15fe02643ca3e4dffad8f45c901a -->

# Configurer Curator

À la fin de cette page, vous aurez un artefact (binaire, archive, paquet, image de conteneur) publié depuis un Envoy et récupérable sur chaque Envoy de la flotte — en pair-à-pair quand c'est joignable, relayé par le serveur sinon. Le blob est signé par le Puddle de l'éditeur et vérifié de bout en bout à chaque lecture. Les lectures sont universelles ; les écritures sont réservées aux membres du Puddle.

**Quand l'utiliser :** chaque fois que les mêmes octets doivent atterrir sur plus d'un Envoy — binaires d'agent, tarballs de fournisseur, paquets `.deb` / `.rpm` personnalisés, fichiers de modèles ML, images de conteneur, fixtures volumineuses. Curator se justifie quand l'alternative que vous évitez est un serveur de fichiers HTTP.

**Quand vous en passer :** secrets et identifiants (utilisez [lockbox](set-up-lockbox.md) ou le préfixe de config `secret:`) ; état qui devrait vivre comme un dépôt git (utilisez [gitback](set-up-gitback.md)) ; fichiers de config ad hoc poussés par l'admin (utilisez Filecast).

## Activation

Par Envoy : optez via `server.yaml` :

```yaml
swarm:
  curator:
    enabled:
      - "*"                   # pattern list, first-match-wins; "-" prefix denies
    max_bytes: 2147483648     # per-blob cap; default 2 GiB
```

Par utilisateur : accordez `curator: true` sur l'usercrate (plus lourd que `gitback:` — accès en écriture à un registre d'artefacts lisible par la flotte) :

```yaml
resources:
  - type: user
    username: dan
    puddle: true              # required
    curator: true             # opt-in for curator publish
```

Publiez (`sudo vigocli config publish`).

## Utilisation

Depuis un Envoy où l'opérateur a déverrouillé son Puddle :

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

Depuis le serveur d'admin (`vigocli`) — mêmes verbes plus la modération :

```bash
vigocli swarm curator list
vigocli swarm curator block <artifact_id>         # server refuses to resolve/relay
vigocli swarm curator unblock <artifact_id>
```

## Fonctionnement

![Curator — la publication est filtrée par l'appartenance au Puddle ; la lecture est universelle via le serveur avec une récupération d'octets P2P ou relayée par le serveur](../../img/curator.svg)

Curator combine deux primitives de flotte existantes : le substrat de blobs du Swarm (adressé par contenu, découpé en morceaux, scopé par cible, P2P) gère les *octets*, et la machinerie d'identité/accès de gitback à clé de Puddle gère le *qui-peut-faire-quoi*. Une fine couche de catalogue signé les relie.

### Identité

`artifact_id = sha256(founder_puddle_pubkey ‖ name)` — même forme qu'un identifiant de projet gitback, de sorte que deux opérateurs peuvent tous deux publier `redis` sans collision. Référence destinée aux humains : `<founder-puddle-name>/<name>` (résolu en une pubkey via la carte de noms de flotte ADR-022, puis en un artifact_id). Le propre Puddle de l'opérateur est l'espace de noms implicite par défaut — `vigo-agent:latest` ≡ `<your-puddle>/vigo-agent:latest`. La forme hexadécimale `<artifact_id>/<name>` fonctionne toujours (auto-certifiante).

### Entrée de catalogue

Une enveloppe signée par artefact, re-signée en dernier-écrivain-gagne à chaque mutation :

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

La signature s'ancre à la pubkey du fondateur : tout Envoy vérifiant l'entrée la rejette si la signature ne remonte pas (via la chaîne de pont de re-clé ADR-019) jusqu'à la pubkey qui hache l'artifact_id. Un fondateur ne peut pas muter l'artefact d'un autre fondateur.

### Publication — chemin d'écriture

`vigo swarm curator push <name>:<version> <file>` s'exécute en tant qu'utilisateur réel avec `curator: true` sur son usercrate et un Puddle déverrouillé. La CLI :

1. Hache le fichier (`sha256` + taille), puis dépose le fichier ainsi qu'une entrée de catalogue re-signée dans le démon assistant à `/var/run/vigo/curator.sock`.
2. L'assistant (root) valide la signature + la chaîne de délégation de bout en bout, confirme que les octets diffusés correspondent à `blob_sha` / `size`, et applique le plafond par blob `swarm.curator.max_bytes` (par défaut 2 Gio ; relevez-le pour les images de conteneur).
3. Met en attente le blob dans le cache de blobs du Swarm et enregistre une entrée de manifeste avec le `target` de l'artefact — le distributeur Filecast existant fait alors essaimer les octets vers les Envoys correspondants activés pour le Swarm, exactement comme un blob Filecast.
4. Dépose l'entrée signée comme `~<user>/.vigo-curator/<artifact_id>/entry.json.pending`. Le réacteur sentinelle root (balayage de 60 s) la revérifie et la promeut en `entry.json`, où le collecteur de traits de l'agent la récupère → l'agrégateur de catalogue côté serveur → le catalogue de flotte.

Les re-signatures qui n'expédient pas de nouveaux octets (`tag` / `untag` / `set-recipients` / `set-target` / `rm`) empruntent le même socket via des verbes ENTRY à en-tête seul. `set-target` ré-enregistre aussi la graine de manifeste pour les blobs que cet Envoy a semés, afin que la distribution vers le nouvel ensemble de cibles se déclenche au prochain passage.

### Lecture — universelle, médiée par le serveur

Chaque Envoy (activé ou non pour le Swarm) résout via le serveur :

```
GET /api/v1/swarm/curator/resolve?artifact_id=<hex>&tag=<t>|&version=<v>&os=<os>&arch=<arch>
```

Le serveur renvoie `{blob_sha, size, signed_entry}` depuis son catalogue agrégé à TTL de 30 secondes (block/unblock invalide le cache). L'agent récupère ensuite les octets :

- **Envoy activé pour le Swarm** — P2P depuis le cache du Swarm (détient généralement déjà la réplique DR parce que `target` lui correspondait).
- **Envoy désactivé pour le Swarm** — `GET /api/v1/swarm/curator/blob/<sha>` depuis le serveur, qui relaie depuis l'Envoy co-résident du serveur. Limité aux shas du catalogue courant uniquement — pas une porte dérobée vers des blobs Swarm arbitraires.

Dans les deux cas, l'agent vérifie le sha et matérialise vers `/var/lib/vigo/artifacts/<artifact_id>/<name>/<version>/<os>-<arch>` (mode 0644), en mettant à jour `/var/lib/vigo/artifacts/cache.json` avec les alias `:<tag>` et `@<version>`.

Les agents mettent aussi en cache le catalogue de flotte à `/var/lib/vigo/artifacts/catalog.json` (rafraîchi à chaque check-in), si bien qu'un Envoy activé pour le Swarm peut résoudre et matérialiser **localement** pendant une panne du serveur pour tout artefact qu'il détient déjà.

### Autorisation

Deux couches filtrent la publication. Toutes deux doivent dire oui :

| Couche | Où | Question |
|---|---|---|
| Envoy | `server.yaml#swarm.curator.enabled` pattern list | Curator est-il disponible sur cet Envoy ? |
| Utilisateur | drapeau `curator: bool` de l'usercrate | Cet utilisateur Unix peut-il publier ? |

Plus la couche de Puddle d'opérateur au moment de la signature : l'assistant de session du Puddle de l'utilisateur doit être en cours d'exécution (`vigo swarm puddle unlock`).

`curator: true` est une concession **plus lourde** que `gitback: true` — « peut publier dans le registre de la flotte, lisible par la flotte par défaut » contre « peut fonder des dépôts git personnels ». Le retirer d'un usercrate déclenche un nettoyage à la prochaine réconciliation (`~/.vigo-curator/` effacé pour cet utilisateur). Les artefacts déjà publiés restent ; vous ne pouvez pas les rappeler en perdant un drapeau.

La lecture est **non filtrée** — les artefacts sont lisibles par la flotte. `pull` matérialise dans une arborescence détenue par root, donc cela nécessite `sudo`.

Le garde-fou de modération de l'opérateur est `vigocli swarm curator block <ref>` (admin seulement). Le serveur cesse de résoudre/relayer l'artefact bloqué dans toute la flotte, quel que soit son éditeur. L'entrée signée existe toujours et se propage par gossip, et les octets DR siègent toujours sur les Envoys activés pour le Swarm — seul un `vigo swarm curator rm` de l'éditeur purge réellement.

### Quitter Curator / décommissionner un utilisateur

Contrairement à longdrawer/lockbox, il n'y a pas d'état par utilisateur sous `/var/lib/vigo/` à orpheliner — l'état côté root de Curator (`/var/lib/vigo/artifacts/catalog.json`, `cache.json`, l'arborescence de blobs matérialisés) est global à la flotte, non partitionné par utilisateur. Et `userdel -r <user>` supprime `~/.vigo-curator/` avec le reste du répertoire personnel, donc le cas courant se nettoie tout seul.

Ce qui SUBSISTE après un `userdel -r` si aucun pré-nettoyage n'a été fait : les **annonces de graines côté manifeste** de cet Envoy pour chaque blob publié par l'utilisateur parti. Les caches de flotte des pairs continuent de résoudre ces entrées jusqu'à ce que le prochain GC de manifeste les fasse vieillir, et `vigocli swarm curator fleet` continuera de lister l'utilisateur parti comme semeur.

**Procédure avant `userdel -r`** — en tant que l'utilisateur, exécutez :

```bash
vigo swarm curator purge --yes      # wipes ~/.vigo-curator/ + revokes this envoy's manifest seeds
```

Ensuite `userdel -r` est sûr. `purge` ne dépublie PAS à travers la flotte — les pairs conservent la dernière entrée qu'ils ont vue. Pour une rétractation complète d'un artefact spécifique, l'éditeur exécute `vigo swarm curator rm <artifact_id>` (signe et propage par gossip une pierre tombale) AVANT `purge`.

Si `userdel -r` a déjà eu lieu sans `purge` préalable : le `~/.vigo-curator/` de l'utilisateur a disparu, les entrées publiées sont toujours dans le catalogue de flotte de chaque pair pour toujours (jusqu'à ce que l'identité du Puddle éditeur revienne pour les rétracter — généralement jamais). La modération d'opérateur `vigocli swarm curator block <ref>` est le seul remède à l'échelle de la flotte.

### Surfaces CLI

Deux clients minces du même backend :

- **`vigo swarm curator`** (côté agent, s'exécute en tant qu'utilisateur) — surface fondateur + consommateur. Les verbes mutants signent via l'assistant de Puddle et soumettent au socket local. Les verbes de lecture (`status`, `list`, `inspect`, `versions`, `resolve`, `pull`) fonctionnent localement + contre le serveur. Les verbes de nettoyage (`stale`, `prune`, `gc`, `purge`) entretiennent l'Envoy.
- **`vigocli swarm curator`** (côté serveur, interface opérateur) — vues de lecture + modération admin (`block` / `unblock`). Les verbes mutants ne vivent délibérément pas ici — ils nécessitent un Puddle par utilisateur déverrouillé, ce qui ne cadre pas avec la posture root-seul / peer-auth de vigocli.

### Accélérer les mises à jour de l'agent (0.51.8+)

Curator peut absorber le coût en bande passante des auto-mises à jour de l'agent. Le déroulement :

1. Après avoir cross-compilé un nouvel agent, poussez le binaire résultant comme artefact Curator sous votre Puddle d'opérateur, une plateforme par cible de cross-compilation. Le nom de l'artefact est **`vigo-agent`** et le tag est la chaîne de version — ce sont les conventions que l'agent recherche.

   ```bash
   vigo swarm curator push vigo-agent:0.51.8 ./dist/agent/linux/amd64/vigo --platform linux/amd64
   vigo swarm curator push vigo-agent:0.51.8 ./dist/agent/linux/arm64/vigo --platform linux/arm64
   # ... one push per platform you ship
   ```

2. À chaque check-in, lorsqu'un Envoy décide de s'auto-mettre à jour vers la version V, il demande d'abord à Curator `vigo-agent:V` pour sa plateforme hôte. Si le catalogue dit oui et que le `blob_sha` de l'artefact correspond à ce que rapporte le `/api/v1/agent/latest` du serveur, l'Envoy récupère les octets en P2P depuis le cache local du Swarm (ou via le relais du serveur s'il est désactivé pour le Swarm) au lieu de re-télécharger depuis `/dist/agent/<os>/<arch>/vigo`.

3. L'agent vérifie SHA256 + signature binaire ed25519 sur les octets, quelle que soit leur provenance. **Le point d'accès REST reste l'autorité** — si l'artefact Curator est absent, si les SHA ne correspondent pas, ou si Curator est défaillant d'une quelconque manière, l'agent retombe silencieusement sur `/dist/agent/<os>/<arch>/vigo` et s'auto-met à jour comme il l'a toujours fait.

C'est opportuniste. Vous n'avez jamais à publier `vigo-agent` pour que la flotte se mette à jour ; vous le faites quand vous voulez épargner l'egress de votre serveur lors d'une montée de version à l'échelle de la flotte. Un `blob_sha` non concordant (vous avez poussé le mauvais build) produit un `warn!` dans le log de l'agent et retombe sur REST — l'artefact publié est simplement ignoré jusqu'à ce que vous le republiez.

### Récupérer des images de conteneur via Curator

Lorsque `swarm.curator.local_port` est défini à une valeur non nulle dans `server.yaml`, l'agent expose un shim OCI Distribution v2 local sur `127.0.0.1:<port>` qui sert les artefacts Curator comme un registre Docker. Cela signifie qu'une ressource `container:` peut utiliser `artifact:` au lieu de `image:` :

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

L'agent traduit `artifact: <ns>/<name>:<tag>` en `docker pull 127.0.0.1:<port>/<ns>/<name>:<tag>` au moment de l'application. Pour publier des images de conteneur, poussez-les comme archives OCI (`docker save --format=oci`) avec `--kind oci-archive`, et faites-les `vigo swarm curator push` comme tout autre artefact. `image:` (une référence de registre normale) et `artifact:` sont mutuellement exclusifs — choisissez l'un par ressource. Voir [`container`](../../reference/vigo/resources/container.md) pour la surface complète des paramètres.

### Héberger un dépôt de paquets privé

Le même écouteur `swarm.curator.local_port` sert aussi une **façade de dépôt de paquets** à `http://127.0.0.1:<port>/repo/<name>/…` — un dépôt apt/dnf/apk privé, distribué en P2P, sans serveur de dépôt (pas d'Aptly, reprepro, Pulp ni nginx). Utilisez-le pour héberger *vos propres* paquets `.deb`/`.rpm`/`.apk` — builds maison, tiers vendorisés, ensembles en air-gap. (Pointer des hôtes vers un dépôt de distribution *amont* reste le travail de la ressource [`repository`](../../reference/vigo/resources/repository.md) ; ceci concerne les dépôts *que vous* publiez.)

**Publier.** `vigo swarm curator repo-publish` construit l'arborescence du dépôt, signe les métadonnées, les tarre, et le publie comme artefact Curator — une seule commande depuis un répertoire de paquets épars :

```sh
# ./debs/ holds your .deb files; the signing key is your own gnupg key.
vigo swarm curator repo-publish acme-repo \
    --dir ./debs --kind apt-repo --sign-key ACME-SIGNING-KEY-ID
```

`repo-publish` invoque la chaîne d'outils correspondant à `--kind` — `dpkg-scanpackages` + `gpg` pour `apt-repo`, `createrepo_c` + `gpg` pour `dnf-repo`, `apk index` + `abuild-sign` pour `apk-repo` — donc l'hôte de publication doit l'avoir installée (un outil manquant donne une erreur claire « install … »). `--sign-key` est un id de clé GPG pour `apt-repo`/`dnf-repo` et un chemin de clé privée RSA abuild pour `apk-repo` (les dépôts apk sont signés RSA, pas GPG). `--suite`/`--component` ont pour défaut `stable`/`main` (apt seulement) ; `--version` a pour défaut la date UTC du jour, `--tag` a pour défaut `latest`. `--kind` est défini une fois à la première publication et est immuable. Les URL apt/dnf/apk ne portent pas de tag, donc la façade sert toujours le tag `latest`.

`repo-publish` est délibérément mince — une suite, un composant, les architectures présentes dans `--dir`. Pour la longue traîne (paquets source, composants multiples, ajouts incrémentaux), construisez l'arborescence signée vous-même avec les outils standards, empaquetez-la comme un **`.tar` simple et non compressé** (la façade exige du non-compressé), incluez la clé de signature publique comme fichier dans l'arborescence pour que les consommateurs puissent la récupérer depuis la même façade, et faites-le `vigo swarm curator push <name> --file <tar> --version <v> --tag latest --os linux --arch all --kind apt-repo` (un tarball de dépôt est agnostique en architecture — `--os linux --arch all` est un emplacement réservé que la façade ignore).

**Consommer.** Pointez une ressource `repository` vers la façade — pas de nouveau type de ressource, l'existant importe déjà une clé de signature et écrit l'entrée de source :

```yaml
- name: acme-internal
  type: repository
  repository: acme-internal
  repo: "deb http://127.0.0.1:5000/repo/alexander4/acme-repo stable main"
  key_url: "http://127.0.0.1:5000/repo/alexander4/acme-repo/key.asc"
```

À partir de là, c'est de l'`apt` natif : `apt update` puis `apt install <your-package>` résout les dépendances et récupère les `.deb` depuis le dépôt hébergé par Curator. `dnf` et `apk` fonctionnent de la même façon contre un artefact `dnf-repo` / `apk-repo`. Pointer un hôte `apt` vers un `dnf-repo` (ou l'inverse) échoue rapidement avec une erreur claire de non-concordance de type plutôt qu'un 404 déroutant.

**Mise en garde.** Un dépôt est un seul artefact tarball — ajouter un paquet signifie reconstruire et republier le dépôt entier comme une nouvelle version d'artefact. Convient aux dépôts privés modestes ; relevez `swarm.curator.max_bytes` pour les grands.

### Exposer des artefacts via une API S3

`swarm.curator.s3_port` ouvre un point d'accès HTTPS compatible S3 en lecture seule (ADR-027) pour que des clients **non-Envoy** — runners CI, ordinateurs portables de dev, tout ce qui possède un SDK S3 — puissent récupérer des artefacts Curator. Toute chaîne d'outils S3 fonctionne contre lui sans modification. Il réutilise le matériel TLS du serveur et nécessite le backend de secrets `local`.

Activez-le dans `server.yaml` (`0`, le défaut, le désactive) :

```yaml
swarm:
  curator:
    s3_port: 9000
```

Frappez un identifiant — admin seulement ; le secret est **dérivé, jamais stocké**, et affiché une fois :

```
$ vigocli swarm curator s3-credentials create --name ci-runner
Curator S3 credential created.
  Access key ID:     VIGO2X4QPL7N8K3M9R6T
  Secret access key: 9f3a…  (shown once — store it now)
  Scope:             *
```

Pointez n'importe quel client S3 vers le point d'accès avec ces clés :

```
$ aws --endpoint-url https://vigo.example.com:9000 s3 ls s3://vigo-curator/
$ aws --endpoint-url https://vigo.example.com:9000 s3 cp \
    s3://vigo-curator/alexander4/vigo-agent/0.66.28/linux-amd64 ./vigo
```

Les clés d'objet sont `<publisher>/<artifact>/<version-or-tag>/<os>-<arch>` — `<publisher>` est le nom convivial du Puddle lorsqu'il est revendiqué sans ambiguïté, sinon l'hexadécimal de la pubkey du fondateur (les deux formes se résolvent sur `GetObject`). `HeadObject` porte le kind de l'artefact dans un en-tête `x-amz-meta-vigo-kind`.

L'API est en **lecture seule** — chaque verbe d'écriture ou de gestion renvoie un `405` S3 ; la publication reste `vigo swarm curator push`. Un identifiant peut être limité à un seul éditeur (`s3-credentials create --scope <publisher>`) ; les artefacts bloqués par l'opérateur sont invisibles via S3 aussi. Notez qu'un `GetObject` d'un artefact de type dépôt (`apt-repo` / `dnf-repo` / `apk-repo`) renvoie le *tarball* du dépôt — pour consommer un dépôt apt/dnf/apk privé de façon navigable, pointez une ressource `repository` vers la façade `/repo/` locale à l'agent ci-dessus, pas vers le point d'accès S3. La rotation de la clé maîtresse re-dérive chaque secret S3, donc les opérateurs refrappent les identifiants après une rotation.

### Où regarder quand ça ne marche pas

- **`vigo swarm curator status`** — état du cache local, pushes en cours, vivacité du démon assistant.
- **`vigocli swarm curator list / fleet`** — vue côté serveur de qui a publié quoi, qui a récupéré quoi, l'ensemble bloqué.
- **Les logs de l'agent** portent un WARN spécifique par échec actionnable (`sha mismatch`, `blob exceeds max_bytes`, `entry signature invalid`, `target glob matched zero envoys`).
- **Pression disque** : comme les autres sous-systèmes Swarm, Curator saute les pushes vers les pairs ≤10 % d'espace disque libre. Voir [Espace disque](set-up-swarm.md#disk-space).

## Et après

- **Référencer des artefacts depuis une ressource `package` / `source_package` / `file` d'un configcrate** → `resources/package` dans le vocabulaire de référence. Les URI Curator fonctionnent partout où une URL de téléchargement convient.
- **Le blob dépasse 2 Gio** → relevez `swarm.curator.max_bytes` par Envoy. Republiez ; les tags se ré-ancrent automatiquement.
- **Rappeler une mauvaise version** → `vigocli swarm curator block <artifact_id>` bloque la résolution à l'échelle de la flotte ; seul un `rm` d'éditeur purge réellement les octets.
- **`pull` se bloque ou présente une non-concordance de sha** → [Résoudre les problèmes courants](../operate/troubleshoot.md).

---
*Vérifié sur Vigo 0.66.28 · 2026-05-22.*

*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [../legal/license.md](../../legal/license.md).*
