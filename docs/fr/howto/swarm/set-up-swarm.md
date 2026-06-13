<!-- vigo-i18n source_sha:5d4922da3dad5258337c470d5e3cada34db83d91f0d8077279997d272507fdf7 -->

# Mettre en place Swarm

À la fin de cette page, vous aurez activé le substrat pair-à-pair des Envoys — les agents se découvrant les uns les autres sur le LAN via multicast, échangeant du contenu sur le port mTLS 1531, et prêts à héberger n'importe lequel des cinq sous-systèmes qui s'appuient dessus (filecast, gitback, lockbox, longdrawer, curator).

**Quand l'utiliser :** chaque fois que vous voulez une distribution de contenu interne à la flotte sans repasser par le serveur — répertoires chiffrés par utilisateur ([lockbox](set-up-lockbox.md)), git P2P ([gitback](set-up-gitback.md)), distribution d'artefacts ([curator](set-up-curator.md)) ou synchronisation de fichiers sur le LAN ([longdrawer](set-up-longdrawer.md)). C'est aussi le prérequis de [puddle](set-up-puddle.md), la primitive d'identité par utilisateur que consomme chaque sous-système par utilisateur.

**Quand y renoncer :** déploiements mono-Envoy et déploiements où le serveur est la seule entité qui a besoin de pousser du contenu vers les Envoys (les fichiers gérés par l'administrateur via le verbe filecast fonctionnent toujours sans Swarm activé sur l'Envoy qui les reçoit — mais le contenu ne peut venir que du serveur, pas des pairs).

Cette page configure le substrat. Les cinq sous-systèmes de contenu ont chacun leur propre guide : [longdrawer](set-up-longdrawer.md), [gitback](set-up-gitback.md), [lockbox](set-up-lockbox.md), [curator](set-up-curator.md), plus filecast (traité directement ci-dessous).

![Vue d'ensemble de Swarm — les Envoys communiquent en pair-à-pair sur le port mTLS 1531 ; cinq sous-systèmes de contenu s'appuient sur le substrat](../../img/swarm-overview.svg)

## Activer Swarm

Ajoutez la section `swarm:` à `server.yaml`. Chaque sous-système est filtré par Envoy au moyen d'une liste de motifs de nom d'hôte (0.41.0+) :

```yaml
swarm:
  enabled: ["*"]                     # which envoys run the substrate
  port: 1531                         # agent peer server port (default)
  max_bandwidth_percent: 50          # agent download throttle (default)
  puddle:    { enabled: ["*"] }      # lockbox derives from this
  gitback: { enabled: ["*"] }
  longdrawer:  { enabled: ["*"] }
  payload:   { enabled: ["*"] }
```

Sémantique des motifs : premier-trouvé-gagne, le préfixe `-` refuse, refus par défaut en l'absence de correspondance. Une liste vide désactive un sous-système pour toute la flotte. Voir [Configuration du serveur](../../reference/server-yaml.md#swarm) pour le tableau complet.

Redémarrez le serveur. Les agents démarrent automatiquement leur serveur de pairs HTTPS sur le port 1531 lorsqu'ils reçoivent des affectations Swarm.

## Distribuer un fichier

Distribuez en une seule commande :

```bash
# Distribute to all envoys
vigocli swarm distribute agent-0.8.4-linux-amd64.tar.gz --target '*'

# Distribute to web servers only
vigocli swarm distribute app-v2.tar.gz --target '*.web.*' --label app-v2

# Distribute by trait
vigocli swarm distribute firmware.bin --target 'dmi.product_name=PowerEdge R750'

# Custom retention (default is 7 days)
vigocli swarm distribute logs.tar.gz --target '*' --retention 30d
```

La CLI transmet le fichier vers le répertoire de seed du serveur (en le hachant en une seule passe) et écrit un manifeste `.swarm`. L'agent hôte découvre le seed au prochain cycle de check-in, crée une entrée de manifeste locale avec sa propre adresse, et la diffuse aux pairs par multicast. Les agents évaluent la spécification de cible localement et se téléchargent les uns aux autres en P2P sur le port 1531. Le serveur n'est pas dans le chemin de distribution — Swarm est entièrement pair-à-pair.

## Suivre la progression

Vérifiez l'état de distribution par Envoy :

```bash
vigocli swarm status <sha256>
```

```
Blob: a1b2c3d4e5f6...  (app-v2)

Summary: 8 complete, 2 downloading, 1 skipped  (11 total)

HOSTNAME                 STATUS        CHUNKS     SKIP REASON
web-01.example.com       complete      47/47
web-02.example.com       complete      47/47
web-03.example.com       downloading   31/47
db-01.example.com        skipped       0/47       insufficient disk space
```

Listez tous les seeds Swarm :

```bash
vigocli swarm list
```

L'interface web à [`/payload`](../operate/troubleshoot.md) affiche tous les seeds actifs et révoqués avec leur progression de distribution. Les pages de détail se rafraîchissent automatiquement toutes les 5 secondes.

## Distribution amorcée par un Envoy

N'importe quel Envoy peut être à l'origine d'une distribution — pas seulement le serveur. Déposez un fichier et un manifeste `.swarm` dans le répertoire de seed :

```bash
# On any envoy:
cp firmware.bin /var/lib/vigo/swarm/seed/
cat > /var/lib/vigo/swarm/seed/firmware.bin.swarm <<'EOF'
{
  "target": "*",
  "retention": "7d"
}
EOF
```

Les champs du manifeste :

| Champ | Requis | Description |
|-------|----------|-------------|
| `target` | Oui | Spécification de cible : `*` (tous), glob de nom d'hôte (`*.web.*`) ou filtre de trait (`os.distro=ubuntu`) |
| `retention` | Oui | Durée de conservation du blob (p. ex. `"7d"`, `"168h"`) |

Au prochain check-in, l'agent hache le fichier et le signale au serveur. Le serveur enregistre le seed dans le CRDT de manifeste et le diffuse à tous les Envoys. Chaque Envoy évalue la spécification de cible et s'auto-affecte s'il correspond.

`vigocli swarm distribute` utilise le même mécanisme — il place le fichier dans le répertoire de seed du serveur. Le serveur n'est qu'un Envoy de plus.

Pour modifier la rétention ou la cible après l'amorçage, éditez le fichier `.swarm` — les changements prennent effet au prochain cycle de check-in.

## Prérequis réseau

| Port | Protocole | Direction | Objet |
|------|----------|-----------|---------|
| 1531 | TCP (mTLS) | Entrant + Sortant | Échange de chunks entre pairs |
| 1531 | UDP | Entrant + Sortant | Découverte multicast des pairs (`224.0.0.42`, LAN uniquement) |

**TCP et UDP doivent tous deux être autorisés sur le port 1531.** Une erreur de configuration fréquente consiste à n'autoriser que TCP — le serveur de pairs fonctionne mais la découverte multicast échoue en silence, laissant les agents incapables de trouver des pairs. Utilisez l'exemple de configcrate `swarm-firewall` pour faire respecter les deux règles.

La découverte multicast ne fonctionne qu'au sein d'un segment LAN. Entre sous-réseaux, les agents découvrent les pairs par sondage actif (interrogation d'adresses connues), en-têtes d'échange de pairs et indices du serveur.

Si votre réseau bloque le multicast, Swarm fonctionne quand même — les agents sondent directement les adresses de pairs connues et reçoivent des indices du serveur au check-in. L'état des pairs persiste entre redémarrages dans `peers.json`.

## Traits de l'agent

L'agent signale des traits liés à Swarm :

| Trait | Description |
|-------|-------------|
| `swarm.blob_count` | Nombre de blobs en cache |
| `swarm.blobs` | Empreintes SHA256 des blobs en cache |
| `swarm.chunk_sources` | Ventilation des sources de téléchargement par blob (inclut `"cached"` pour les chunks déjà sur disque au démarrage de l'exécution) |
| `swarm.chunk_details` | Source et chronométrage par chunk |
| `swarm.active_download_ms` | Temps d'horloge mural actif de l'ordonnanceur par blob (exclut les chunks pré-mis en cache) |
| `swarm.downloading` | Téléchargements en cours (sha → chunks_have) |

Ces traits sont visibles dans la page de détail de l'Envoy et peuvent être interrogés avec `vigocli query`.

## Stockage de l'agent

Les fichiers Swarm sont stockés sous `/var/lib/vigo/swarm/` sur Linux (`/Library/Vigo/swarm/` sur macOS) :

```
/var/lib/vigo/swarm/
├── state.json      # Resolved subsystem gates + last check-in (mode 0644, 0.41.1+)
├── manifest.json   # LWW-CRDT manifest — blob lifecycle state (mode 0644)
├── peers.json      # Persistent peer registry — survives restarts (mode 0644)
├── .blobs/         # Content-addressed storage (SHA256 filenames, root-only)
│   └── 9faa44b9...
├── files/          # Symlinks with original filenames
│   └── firmware.bin → ../.blobs/9faa44b9...
├── seed/           # Drop files here with .swarm manifests to seed distribution
│   ├── firmware.bin
│   └── firmware.bin.swarm
└── graveyard/      # Revoked blobs awaiting hard-delete (auto-cleaned after 7 days)
    └── a1b2c3d4...
```

Les trois fichiers `.json` à la racine (`state.json`, `manifest.json`, `peers.json`) sont en mode 0644 depuis 0.41.1 pour que les verbes CLI en mode utilisateur puissent les lire. Inspectez via la CLI de diagnostic plutôt que d'analyser le JSON directement :

```bash
vigo swarm status              # gates, manifest version, peer count, last checkin
vigo swarm seeds list          # active blobs this envoy holds
vigo swarm seeds show <key>    # full detail (sha-prefix or label)
vigo swarm peers list          # gossip-table snapshot
```

Voir [`vigo swarm` (verbes de diagnostic)](../../reference/vigocli/agent-swarm.md) pour la référence complète.

Le répertoire `.blobs/` contient les données réelles, nommées par empreinte SHA256. Le répertoire `files/` contient des liens symboliques utilisant le label du blob (nom de fichier d'origine) :

```bash
ls -la /var/lib/vigo/swarm/files/
```

Si un nouveau blob est distribué avec le même label, le lien symbolique est mis à jour pour pointer vers le nouveau blob.

## Rétention

Tous les blobs Swarm ont une rétention par défaut de **7 jours**.

**Récepteurs :** `vigocli swarm distribute` utilise par défaut `--retention 7d`. L'agent enregistre l'horodatage d'achèvement du téléchargement et expulse le blob lorsque la période de rétention expire. Surchargez par distribution avec `--retention 30d`, ou `--retention 0` pour conserver indéfiniment.

**Amorceurs :** une fois la rétention écoulée, l'élagueur de rétention du serveur enregistre une révocation dans le CRDT de manifeste. La révocation est diffusée à tous les Envoys (consommateurs comme amorceur d'origine). Sur l'amorceur, le blob passe de `.blobs/` à `graveyard/` et le fichier source + le manifeste `.swarm` sont retirés de `seed/` pour que le collecteur de traits ne le re-signale pas. Sur les consommateurs, le blob en cache est expulsé. Les fichiers du graveyard sont supprimés définitivement après 7 jours supplémentaires ; pendant cette fenêtre de grâce, un ré-amorçage du même SHA ressuscite le contenu sans nouveau téléchargement.

**GC des pierres tombales de manifeste :** le même élagueur de rétention retire aussi les entrées révoquées du magasin de manifeste en mémoire une fois qu'elles ont dépassé la fenêtre de rétention — le marqueur de révocation a fait son travail (chaque agent atteignable l'a observé et a expulsé le blob). Sans cette passe, les entrées révoquées s'accumuleraient indéfiniment dans le manifeste du serveur ; les agents élaguent leur manifeste persisté sur la même fenêtre via la boucle de check-in.

**Révocation manuelle :** pour révoquer immédiatement un blob sur toute la flotte :

```bash
vigocli swarm revoke <sha256>
```

Cela marque le seed comme révoqué dans le tracker, enregistre une révocation dans le CRDT de manifeste, et propage la révocation à tous les Envoys par gossip. Chaque Envoy expulse le blob de son cache dès qu'il reçoit la révocation. Sur un LAN, toutes les copies sont supprimées en quelques secondes. Les Envoys inter-sous-réseaux reçoivent la révocation à leur prochain check-in. La révocation déclenchée par l'opérateur et celle pilotée par la rétention passent par un code identique — le seul `unlink` sur le système de fichiers intervient 7 jours plus tard dans `cleanup_graveyard`.

**Ré-amorçage :** pour redistribuer un contenu révoqué, replacez le fichier et le manifeste `.swarm` dans `seed/`. Au prochain check-in, le collecteur de traits re-hache le fichier, le serveur enregistre une nouvelle entrée active dans le CRDT de manifeste (le nouvel horodatage l'emporte sur l'ancienne révocation), et la distribution reprend. L'empreinte SHA256 est basée sur le contenu — même fichier = même empreinte — donc les téléchargements partiels des rounds précédents sont automatiquement repris. Les entrées de la carte des pairs pour le SHA révoqué sont purgées lors de l'expulsion, de sorte que le téléchargement ré-amorcé démarre avec une découverte de pairs fraîche plutôt que de se fier à des affirmations périmées du round précédent.

## Manifeste

Chaque Envoy maintient un fichier de manifeste local à `/var/lib/vigo/swarm/manifest.json` qui suit l'état du cycle de vie des blobs. Le manifeste est un CRDT (Conflict-free Replicated Data Type) qui se diffuse automatiquement entre Envoys.

Vous n'avez pas besoin de gérer le manifeste directement — il est maintenu par l'agent. Le manifeste garantit que :

- Les révocations se propagent à toute la flotte en quelques secondes (via multicast + synchronisation de pairs)
- Les blobs révoqués ne peuvent pas être ré-enregistrés par des rapports de traits périmés
- Les Envoys hors ligne se mettent à jour à leur reconnexion
- La panne du serveur n'empêche pas les révocations entre Envoys sur le même LAN

Les entrées de manifeste révoquées sont élaguées après 7 jours.

## Décalage de téléchargement (jitter)

Lorsque tous les Envoys reçoivent une affectation Swarm au même cycle de check-in, ils échelonnent le démarrage de leurs téléchargements pour éviter de saturer l'amorceur. Chaque Envoy calcule un délai déterministe (0–1 seconde) en hachant son nom d'hôte + le SHA256 du blob. Cela garantit que :

- Chaque Envoy obtient un délai cohérent pour le même blob (les redémarrages ne le changent pas)
- Différents Envoys démarrent à des moments différents, répartissant la charge initiale
- Le jitter est assez court pour ne pas retarder sensiblement la distribution

Aucune configuration n'est nécessaire — le jitter est automatique.

## Espace disque

Chaque chemin de livraison pair-à-pair de la flotte — filecast (blobs Swarm distribués par l'administrateur), lockbox (texte chiffré par utilisateur), gitback (bundles de dépôts) et longdrawer (annonces par utilisateur) — refuse d'écrire des octets sur un système de fichiers ayant **10 % d'espace libre ou moins**. La vérification s'applique symétriquement des deux côtés de chaque transfert :

- **Côté émetteur** — avant un push, l'émetteur interroge le serveur de pairs du destinataire `GET /health/storage?for=<subsystem>[&user=…]` sur le mTLS existant du port 1531 et compare le `free_pct` retourné au seuil. Un pair à 10 % d'espace libre ou moins est ignoré sans bruit ; les diffusions vers de nombreux pairs sondent chacun une fois par minute (cache de 60 s) pour qu'une rafale ne martèle pas le point de terminaison de santé.
- **Côté récepteur** — le gestionnaire de push (`/lockbox/push`, `/gitback/push`) revérifie son propre disque à l'entrée et renvoie **507 Insufficient Storage** avant de lire le moindre octet du corps. Cela ferme la course entre le sondage mis en cache d'un émetteur et l'arrivée du push lorsque le disque s'est rempli entretemps.
- **Swarm (basé sur le pull)** — le téléchargeur se filtre lui-même dans `BlobCache::check_disk_space`, qui applique désormais à la fois la vérification de réserve absolue classique (`available >= blob_size + 512 MiB`) ET le seuil de 10 % d'espace libre. Un récepteur avec 9 % d'espace libre abandonne le téléchargement avant d'écrire des chunks.

Les push ignorés sont des no-ops, pas des échecs : la cadence de push suivante re-sonde, et dès que le pair repasse au-dessus de 10 % d'espace libre les push reprennent d'eux-mêmes. Aucun état de backoff ne s'accumule. Les émetteurs journalisent les abandons en `warn` (ou `debug` pour les chemins d'annonce uniquement comme longdrawer) pour que les opérateurs voient quels pairs sont en pression disque.

Le seuil de 10 % est la valeur par défaut, surchargeable avec `VIGO_MIN_FREE_PCT=<f64>` dans l'environnement de l'agent (principalement pour les tests). La surcharge de politique à l'échelle de la flotte via `server.yaml` est une évolution prévue.

Chaque sous-système mesure le système de fichiers où ses données atterrissent réellement — `/var/lib/vigo/swarm/.blobs` pour les charges utiles filecast, `/var/lib/vigo/swarm/gitback` pour les miroirs gitback, `~<user>/lockbox` pour le texte chiffré lockbox, `~<user>/longdrawer` pour le texte clair longdrawer. Ceux-ci peuvent se trouver sur des montages différents ; la vérification utilise le bon selon l'usage.

## Dépannage

### Envoy bloqué sur « pending »

« Pending » signifie que le serveur n'a reçu aucune donnée de trait Swarm pour cet Envoy concernant ce blob — l'Envoy n'a pas encore commencé à télécharger. Les Envoys qui téléchargent activement apparaissent comme « downloading » avec une barre de progression. Vérifiez dans l'ordre :

1. **Le serveur a-t-il été redémarré ?** Le PeerTracker en mémoire est perdu au redémarrage. L'état devient vide jusqu'à ce que les Envoys re-signalent leurs traits. Les Envoys qui ont déjà le blob réapparaîtront comme « complete » en 1-2 cycles de check-in (~15 s). Aucune action nécessaire — patientez.

2. **L'Envoy est-il en ligne ?** `vigocli envoys` — vérifiez l'heure de dernière vue.

3. **Swarm est-il activé sur le serveur ?** `swarm.enabled: true` dans `server.yaml`.

4. **Incompatibilité de version d'agent ?** Comparez `vigo version` sur l'Envoy à `cat VERSION` sur le serveur. Après un exec() de `force_update`, le nouveau binaire peut ne prendre effet qu'au prochain redémarrage. Vérifiez `journalctl -u vigo-envoy | grep "swarm manager started"`.

5. **Espace disque critiquement bas ?** Si `BlobCache::check_disk_space` se déclenche — soit la réserve absolue (size + 512 MiB), soit le seuil de 10 % d'espace libre — le téléchargement est ignoré et l'Envoy affiche « skipped ». Libérez de l'espace ou faites tourner d'anciens blobs ; le prochain check-in retente.

### Les téléchargements échouent avec « no available peers »

L'ordonnanceur trouve 0 pair et abandonne immédiatement. C'est le problème Swarm le plus courant. Diagnostiquez dans l'ordre :

1. **Pare-feu : TCP et UDP 1531 doivent tous deux être ouverts.** Une erreur de configuration fréquente consiste à n'autoriser que TCP — le serveur de pairs fonctionne mais la découverte multicast échoue en silence. Vérifiez avec `ufw status | grep 1531`. Utilisez la configcrate `swarm-firewall` pour faire respecter les deux règles.

2. **L'annonceur multicast est mort.** L'amorceur doit annoncer les blobs par multicast. Vérifiez si l'amorceur envoie des annonces :
   ```bash
   sudo timeout 10 tcpdump -i <iface> udp port 1531 -A -c 5 2>&1 | grep "HAVE\|ADDR"
   ```
   Si l'amorceur envoie `ADDR` mais pas de `HAVE` pour le blob, ou n'envoie rien du tout, la tâche d'annonceur a peut-être quitté. Recherchez `multicast announcer exited` dans les journaux de l'agent (0.19.32+). Redémarrez l'agent.

3. **PeerMap vide.** Sur l'Envoy récepteur, vérifiez s'il a découvert des pairs (pas besoin de sudo, 0.41.1+) :
   ```bash
   vigo swarm peers list
   ```
   Si vide ou s'il manque l'adresse de l'amorceur pour le blob cible, les paquets multicast n'atteignent pas l'agent (pare-feu, sous-réseau différent, ou annonceur mort). Pour la vue par blob, utilisez `vigo swarm seeds show <sha-or-label>` — il liste les pairs connus pour détenir ce blob précis.

4. **Multicast qui part mais n'arrive pas.** Si tcpdump sur l'amorceur montre des paquets qui partent mais tcpdump sur le récepteur ne montre rien venant de l'IP de l'amorceur, le problème est au niveau réseau :
   ```bash
   # On the seeder:
   sudo timeout 10 tcpdump -i any 'udp port 1531 and src host <seeder-ip>' -A -c 2
   # On the receiver:
   sudo timeout 10 tcpdump -i any 'udp port 1531 and src host <seeder-ip>' -c 1
   ```
   Si l'amorceur envoie mais que le récepteur ne reçoit rien, vérifiez : les réglages d'IGMP snooping du point d'accès WiFi, le forwarding multicast du routeur, les interfaces VPN/tunnel qui captent la route par défaut (Tailscale, WireGuard — corrigé en 0.19.37 pour l'adresse de l'annonceur, mais le réseau doit tout de même transmettre les paquets).

5. **Tailscale/VPN installé sur l'amorceur.** Avant 0.19.37, `local_ipv4()` choisissait l'IP du tunnel VPN au lieu de l'IP du LAN. Mettez à niveau vers 0.19.37+ ou définissez `swarm.announce_address` dans la configuration de l'agent pour surcharger.

6. **Incompatibilité de version d'agent.** Le correctif de blocage de l'ordonnanceur (0.19.29) et le rafraîchissement des pairs (0.19.30) sont requis pour les téléchargements multi-chunks. Vérifiez `vigo version` sur l'Envoy.

### Envoy bloqué sur « downloading »

L'Envoy a commencé à télécharger mais n'a pas terminé. Vérifiez :

- La connectivité réseau entre pairs (le port 1531 doit être joignable)
- L'espace disque sur l'Envoy (`df -h`)
- Les journaux de l'agent pour des erreurs de téléchargement (`journalctl -u vigo-envoy --since "5 minutes ago" | grep -i swarm`)
- Les connexions périmées : `ss -tnp | grep 1531` — des connexions CLOSE-WAIT indiquent que le serveur de pairs a fermé la connexion. Mettez à niveau vers 0.19.28+ (correctif header_read_timeout)

### « skipped — insufficient disk space »

L'Envoy n'a pas assez d'espace disque libre pour le blob. Libérez de l'espace ou agrandissez le disque, puis redistribuez.

### Téléchargement depuis un seul pair

Si un Envoy télécharge tous les chunks depuis l'amorceur alors que d'autres pairs sont disponibles :

1. **Version d'agent < 0.23.2 ?** Deux bugs causaient la domination de l'amorceur : des entrées PeerMap périmées après révocation/ré-amorçage provoquaient des avalanches de 404 venant des pairs (repli sur l'amorceur pour chaque chunk), et l'orchestrateur sautait la découverte de pairs quand seul l'amorceur était connu. Les deux corrigés en 0.23.2.

2. **Version d'agent < 0.19.30 ?** L'ordonnanceur ne rafraîchissait pas la carte des pairs en cours de téléchargement. Corrigé en 0.19.30 : l'ordonnanceur se rafraîchit tous les 100 chunks (~10 secondes), captant les nouveaux pairs annoncés par multicast pendant le transfert.

3. **Multicast fonctionne ?** Vérifiez que d'autres pairs annoncent des lignes HAVE pour le blob — voir la section « Les téléchargements échouent avec no available peers » ci-dessus.

Vérifiez la distribution multi-pairs dans la sortie d'état — la colonne Sources affiche le nombre de chunks par pair :

```
danlap: 2000  girlslaptop: 3000  plex: 2378
```

### Délais d'attente lors de téléchargements de pairs

Si les agents journalisent des erreurs `timeout: connect` lors du téléchargement depuis des pairs, l'adresse du pair est peut-être incorrecte. Le serveur utilise l'IP que l'agent a signalée à l'enrôlement. Si l'agent a été enrôlé depuis une machine exécutant Docker, il a pu capturer l'IP du pont Docker au lieu de la véritable IP du LAN.

Vérifiez l'IP stockée :

```bash
vigocli envoys | grep <hostname>   # look at the IP column
```

Si l'IP est l'adresse d'une interface Docker/virtuelle, ré-enrôlez l'agent. L'agent filtre les interfaces virtuelles (docker, br-, veth, virbr) lors de la détection de son IP sortante, et rafraîchit l'IP à chaque cycle de check-in.

### Les téléchargements de chunks échouent avec « unexpected end of file »

Toutes les tentatives de téléchargement de chunks vers un pair échouent immédiatement avec `io: unexpected end of file`. Cela indique que le serveur HTTPS du pair a épuisé son sémaphore de 100 connexions. Deux causes ont été corrigées en 0.19.25 :

1. **Téléchargements concurrents en double** — l'agent lançait une nouvelle tâche de téléchargement à chaque cycle de check-in pour le même blob, chacune avec son propre sémaphore de 8 connexions. Cinq tâches concurrentes = 40 connexions depuis un seul Envoy. Corrigé par un garde-fou « en cours » n'autorisant qu'un seul téléchargement par blob à la fois.
2. **Accumulation de connexions inactives** — les connexions keep-alive HTTP sans délai d'inactivité retenaient indéfiniment des permis du sémaphore. Corrigé par un délai de lecture d'en-tête de 15 secondes à l'inactivité sur le serveur de pairs (les connexions ne sont fermées que lorsqu'elles sont inactives entre requêtes, pas pendant des transferts actifs).

Mettez à niveau tous les agents vers 0.19.28+.

### Échec de la poignée de main TLS entre pairs

Si les agents journalisent `certificate does not allow extended key usage for server authentication`, le certificat d'enrôlement de l'Envoy a été émis avant que la CA n'inclue l'EKU ServerAuth. Ré-enrôlez les Envoys concernés pour obtenir de nouveaux certificats avec à la fois ClientAuth et ServerAuth.

### Aucun pair découvert (inter-sous-réseaux)

Si les Envoys sont sur des sous-réseaux différents, le multicast ne les atteindra pas. Les pairs sont découverts via :

- **Sondage actif** — l'agent interroge les adresses de pairs connues (depuis `peers.json`) pour le blob cible
- **Échange de pairs** — l'en-tête `X-Peers` sur les réponses de chunks partage les adresses de pairs de manière transitive

L'état des pairs persiste entre redémarrages dans `peers.json`, donc un agent qui a déjà téléchargé depuis un pair le réessaiera pour de nouveaux blobs.

### L'annonceur multicast ne démarre pas

Si `journalctl | grep "multicast announcer"` montre `multicast announcer exited` avec une erreur, la création du socket UDP a échoué. Causes fréquentes :

- Limite de descripteurs de fichiers atteinte — vérifiez `ulimit -n`
- Un autre processus détient le socket (peu probable — l'annonceur utilise un port éphémère)
- Restrictions multicast au niveau de l'OS

Si ni « multicast announcer started » ni « multicast announcer exited » n'apparaît dans les journaux, l'agent exécute une version antérieure à 0.19.32 qui journalisait les erreurs d'annonceur au niveau debug (invisible). Mettez à niveau et revérifiez.

## Scénarios de cycle de vie d'un seed

Ce qui se passe pour chaque action qu'un opérateur peut effectuer sur l'amorceur ou le téléchargeur.

### Actions de l'amorceur

| Action | Ce qui se passe | Résultat |
|--------|-------------|--------|
| **Déposer fichier + .swarm** | Le collecteur de traits hache le fichier, crée le lien symbolique `.blobs/`, signale le seed. Le serveur l'enregistre dans le CRDT de manifeste, le diffuse à la flotte. | La distribution démarre. |
| **Retirer le fichier de seed/** | Le collecteur de traits cesse de signaler le seed. Le serveur auto-révoque (tracker + CRDT de manifeste). Tous les Envoys expulsent le blob par gossip. | L'amorçage est intentionnel — retirer le fichier est une révocation autoritaire quel que soit le nombre de récepteurs détenant le blob. |
| **Remplacer le fichier (même nom, contenu différent)** | Le collecteur de traits calcule une nouvelle empreinte → collision de label détectée → ancienne empreinte révoquée, nouvelle empreinte enregistrée. | Ancien contenu révoqué, nouveau contenu distribué. |
| **Éditer .swarm (changer cible/rétention)** | Le PeerTracker capte les champs mis à jour au prochain check-in. Note : le ManifestStore ne re-diffuse pas les changements de cible — les affectations d'Envoy existantes ne sont pas affectées. | Les changements ne prennent effet que pour les nouvelles affectations. |
| **Ré-amorcer après révocation (même contenu)** | Même empreinte SHA256. Le serveur crée une nouvelle entrée de manifeste active (le nouvel horodatage l'emporte sur l'ancienne révocation). Les Envoys fusionnent l'entrée active, vident leur ensemble achevé, et re-téléchargent. | Fonctionne. Les chunks partiels des rounds précédents sont réutilisés. Dans les 7 jours, la copie du graveyard est aussi réutilisée. |
| **Amorçage interrompu (fichier partiel, puis complet)** | Le fichier partiel obtient une empreinte SHA256 différente. Quand le fichier complet le remplace, l'empreinte partielle est auto-révoquée par collision de label et l'empreinte correcte est enregistrée. La fonction `evict_revoked_blobs` vérifie les liens symboliques partagés avant de supprimer les fichiers source de seed. | Fonctionne. |
| **Tuer/redémarrer l'agent pendant l'amorçage** | L'état du manifeste et des pairs persiste sur disque. Le fichier de seed est toujours dans `seed/`. Le collecteur de traits le redécouvre au prochain cycle. | Reprise transparente. |

### Actions du téléchargeur

| Action | Ce qui se passe | Résultat |
|--------|-------------|--------|
| **Téléchargement partiel interrompu (redémarrage de l'agent)** | Les chunks survivent dans `.blobs/<sha>.partial/`. Au redémarrage, le SwarmManager est neuf (ensemble `completed` vide), le manifeste est chargé depuis le disque, l'ordonnanceur reprend à partir des chunks existants. | Reprise automatique. |
| **Supprimer le blob partiel** (`rm -rf .blobs/<sha>.partial/`) | L'ordonnanceur recrée le répertoire partiel et repart du chunk 0. | Le téléchargement repart de zéro. |
| **Supprimer le blob complet** (`rm .blobs/<sha>`) | L'ensemble `completed` contient encore l'empreinte, mais à la prochaine évaluation ManifestUpdated, le gestionnaire détecte que le blob manque dans le cache et efface l'entrée périmée. Le téléchargement repart. | Auto-réparation en un cycle de check-in. |
| **Une révocation arrive en cours de téléchargement** | L'ordonnanceur sonde `manifest.is_revoked` entre les rounds de retry, dans la boucle de production de chunks, et dans le consommateur avant d'écrire chaque chunk. À la révocation, il abandonne avec `REVOKED_ERR`, et le pipeline externe efface immédiatement le répertoire partiel pour que le collecteur de traits cesse de signaler le sha comme `downloading`. | Propre. Le partiel disparaît au même cycle de gossip ; le serveur fait passer l'Envoy de `revoking` → `revoked` au prochain check-in. |
| **Disque plein ou sous pression** | `check_disk_space` échoue soit le seuil de réserve absolue (size + 512 MiB), soit le seuil de 10 % d'espace libre. Téléchargement ignoré avec « insufficient disk space » ou « disk pressure ». L'état affiche « skipped ». Retente automatiquement à chaque check-in. | Auto-réparation quand l'espace est libéré. |
| **Aucun pair disponible** | L'ordonnanceur abandonne avec « no progress » après l'attente multicast + le sondage. Retente à chaque cycle de check-in. | Se résout quand des pairs arrivent en ligne. |
| **L'amorceur passe hors ligne** | Les téléchargements de chunks échouent → backoff exponentiel → abandon « no progress ». Reprend quand l'amorceur (ou tout pair détenant le blob) revient. | Retente automatiquement. |

## Voir aussi

- [vigocli swarm filecast](../../reference/vigocli/swarm-filecast.md) — référence CLI pour les filecasts poussés par l'administrateur sur ce substrat
- [Configuration du serveur](../../reference/server-yaml.md) — section `swarm:`

## Et ensuite

- **Activez maintenant les sous-systèmes de contenu** — choisissez celui qu'il vous faut : [puddle](set-up-puddle.md) d'abord (prérequis des sous-systèmes par utilisateur), puis [lockbox](set-up-lockbox.md) / [gitback](set-up-gitback.md) / [curator](set-up-curator.md) / [longdrawer](set-up-longdrawer.md).
- **Un Envoy ne reçoit pas de contenu** → [Dépanner les problèmes courants](../operate/troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel -- Alexander4, LLC. Ne pas redistribuer. Voir la [licence](../../legal/license.md).*
