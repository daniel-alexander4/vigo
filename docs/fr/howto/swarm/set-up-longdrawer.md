<!-- vigo-i18n source_sha:1306f79061af384ad4873c4d5def842e25c32fa2a7f6739a44183ba6a059d031 -->

# Mettre en place Longdrawer

À la fin de cette page, `~/longdrawer/` se synchronisera sur le LAN — déposez un fichier sur n'importe quel Envoy où vous avez un compte, il apparaît sur tous les autres Envoys du même domaine de diffusion en ~60 s ; supprimez-le et le tombstone se propage de la même façon. Aucun fichier de config, aucun verbe CLI, aucune implication du serveur. L'activation, c'est « créer `~/longdrawer/` sur au moins un Envoy ».

**Quand l'utiliser :** des fichiers de travail ambiants que vous voulez présents sur chaque machine où vous vous connectez — notes de brouillon, ébauches de dotfiles, signets d'historique shell, travaux en cours que vous ne voulez pas trimballer par scp. La portée LAN-only est voulue : les octets ne quittent pas la frontière de confiance du domaine de diffusion.

**Quand l'éviter :** tout ce que vous voulez au repos sur disque sous forme de texte chiffré (utilisez [lockbox](set-up-lockbox.md) — forme identique, chiffrement inclus) ; tout ce qui nécessite une propagation à travers le WAN (longdrawer ne franchit pas les sous-réseaux sans liste de pairs explicite) ; un état mieux conservé sous forme de dépôt git (utilisez [gitback](set-up-gitback.md)).

## Activez-le

Longdrawer est opt-in par existence de répertoire — pas de drapeau usercrate, pas de barrière server.yaml. **Créez `~/longdrawer/` sur au moins un Envoy** et l'agent le détecte au prochain scan de 30 secondes. Les autres Envoys hébergeant le même utilisateur avec `~/longdrawer/` commenceront à se synchroniser automatiquement.

Pour les pairs hors sous-réseau (le multicast LAN ne les atteint pas), ajoutez-les à la config longdrawer de l'utilisateur :

```yaml
# /home/<user>/.longdrawer/config.yaml
cross_subnet_peers:
  - peer-host.example.com
  - 10.20.30.40
```

## Utilisez-le

Il n'y a aucun verbe CLI. Déposez un fichier dans `~/longdrawer/` ; il apparaît sur chaque pair. Retirez-le ; le tombstone se propage. C'est toute la surface opérateur.

Si un fichier ne se propage pas, vérifiez :

- `~/longdrawer/` existe des deux côtés (c'est un signal opt-in par existence).
- Le trafic multicast n'est pas filtré par un pare-feu sur `224.0.0.43:1532` (ou que la liste de pairs hors sous-réseau est définie, si les pairs sont sur des sous-réseaux différents).
- Le disque a plus de 10 % de libre des deux côtés (le seuil de refus-en-dessous à l'échelle du swarm s'applique).

## Comment ça marche

![Longdrawer — annonce multicast sur le LAN, une divergence de digest déclenche un pull, réconciliation par fichier](../../img/longdrawer.svg)

Longdrawer est l'un des quatre sous-systèmes de contenu qui s'appuient sur le substrat [swarm](set-up-swarm.md). Le serveur ne joue aucun rôle dans le contenu ni la découverte — longdrawer réutilise le canal pair mTLS du swarm (port 1531) pour les transferts et exécute son propre groupe multicast (`224.0.0.43:1532`) pour les annonces.

Fonctionnalité LAN purement pair-à-pair. Les Envoys se découvrent via multicast UDP et échangent des fichiers directement en mTLS sur le port pair du swarm.

1. **Scan.** Toutes les 30 secondes, chaque agent scanne `~/longdrawer/` pour chaque utilisateur local. Les fichiers sont hachés (SHA-256), et l'état en mémoire (liste de fichiers + tombstones de suppression) est comparé au snapshot précédent. Les fichiers ajoutés apparaissent dans le nouvel état ; les fichiers supprimés reçoivent un tombstone horodaté à l'instant courant.
2. **Annonce.** Chaque agent diffuse en multicast une **annonce digest-only** sur `224.0.0.43:1532` : `{machine, user, port, generation, state_digest}`. Le digest est un SHA-256 tronqué de 16 octets sur l'ensemble trié des fichiers + tombstones. **Les chemins de fichiers, sha256 et mtimes ne transitent jamais sur le fil** — tout ce qu'un observateur LAN peut voir, c'est l'identité du pair et un court digest opaque. TTL=1, local au LAN.
3. **Comparaison du digest.** Chaque récepteur calcule son propre state_digest pour le même utilisateur. En cas de correspondance, on passe — déjà synchronisé. En cas de divergence, on tire le `UserState` complet du pair via `GET /longdrawer/state/{user}` sur le canal mTLS swarm existant du port 1531.
4. **Réconciliation.** L'état complet désormais en main, le récepteur compare entrée par entrée :
    - **Le pair a un fichier que nous n'avons pas** → le récupérer.
    - **Le pair a une version plus récente** (mtime plus élevé) → la récupérer.
    - **Le pair a un tombstone que nous n'avons pas, et le mtime de notre fichier est plus ancien que l'heure de suppression** → supprimer le fichier.
    - **Le pair a un tombstone mais notre fichier est plus récent que l'heure de suppression** → conserver le fichier (il a été ré-ajouté après la suppression) et notre prochaine annonce le repropagera.

    Ré-ajouter un fichier précédemment tombstoné en le `mv`-ant de nouveau dans `~/longdrawer/` préserve le mtime d'origine, ce qui perdrait la comparaison face au tombstone (plus récent). Le scanner détecte ce cas — un fichier actuellement présent qui possède un tombstone local — et avance le mtime sur disque du fichier à l'heure du scan courant avant l'annonce. Sans cet ajustement, les tombstones des pairs re-supprimeraient le fichier au prochain tour de synchronisation.
5. **Transfert.** Les fichiers sont récupérés en HTTPS depuis le port pair swarm de l'émetteur (1531) via une route dédiée `/longdrawer/{user}/{filename}`. Le contenu est vérifié contre le SHA-256 annoncé avant d'être écrit sur la destination.
6. **Application.** L'agent récepteur écrit le fichier dans `~/{user}/longdrawer/{filename}` avec le bon propriétaire (`chown user:user`) et le temps de modification préservé.

**Modèle de confidentialité (0.30.11+).** Les versions antérieures diffusaient le JSON `UserState` complet (fichiers + tombstones avec chemins, hachages et mtimes) dans chaque paquet multicast. Sur tout domaine de diffusion partagé, cela fuyait le listing de répertoire de chaque utilisateur à quiconque écoutait. Le modèle digest-only ferme cette fuite : le fil public ne porte aucune métadonnée de fichier. Les noms et hachages de fichiers ne sont révélés que sur le canal pair authentifié en mTLS, et seulement quand le récepteur a déjà une raison de demander (digest modifié).

## Comportement

| Action | Comportement |
|--------|--------|
| Ajouter un fichier | Synchronisé vers les autres machines en ~60 s |
| Mettre à jour un fichier | Le mtime le plus récent gagne, écrase sur les autres machines |
| Supprimer un fichier | Le tombstone se propage, le fichier disparaît des autres machines en ~60 s |
| Ré-ajouter un fichier supprimé | Le mtime plus récent bat le tombstone, le fichier réapparaît partout |
| Éditions en conflit sur deux machines | Last-write-wins par mtime (faites confiance à l'horloge de l'OS — exécutez NTP) |
| `rm -rf ~/longdrawer/*` (vidage complet) | **Ne se propage PAS.** Les pairs conservent leurs copies et regarnissent cet Envoy au prochain sync. À traiter comme « efface ma copie locale », pas « efface partout ». |
| `rm -rf ~/longdrawer` (suppression du répertoire entier) | Cet Envoy se met en sommeil pour l'utilisateur ; les pairs continuent sans lui. Un `mkdir ~/longdrawer/` ultérieur le réengage et resynchronise depuis les pairs. |
| `userdel -r <user>` | L'état de cet Envoy pour l'utilisateur est balayé au prochain scan ; réutiliser le nom d'utilisateur plus tard repart à zéro (pas de tombstones obsolètes de l'utilisateur précédent). |

## Activation

Aucune configuration. Longdrawer s'active automatiquement quand :

1. L'agent tourne et possède des certificats d'enrôlement (mTLS)
2. Un utilisateur a créé `~/longdrawer/` dans son répertoire personnel

Le répertoire n'est **pas** créé automatiquement — les utilisateurs s'inscrivent en le créant. Symétriquement, si le répertoire est absent, le chemin de réception de l'agent rejette les annonces de pairs entrantes pour cet utilisateur (pas de recréation silencieuse) et le chemin de scan ignore entièrement l'utilisateur. Pour quitter longdrawer sur un Envoy, supprimez `~/longdrawer/`.

## Stockage de l'état

L'agent conserve l'état par utilisateur (snapshots de fichiers + tombstones) sous `/var/lib/vigo/swarm/longdrawer/<user>.json`. Cela survit aux redémarrages de l'agent. **Les tombstones sont collectés par le ramasse-miettes après 30 jours** — passé ce délai, une machine longtemps hors-ligne qui détient encore le fichier le ressuscitera à la reconnexion. Si cela vous importe, ne laissez pas les machines hors-ligne plus d'un mois.

Le fichier d'état d'un utilisateur est **supprimé automatiquement** quand cet utilisateur est retiré de `/etc/passwd` (par ex. via `userdel -r`). Cela empêche un état obsolète de se rattacher plus tard à un utilisateur de même nom — un risque réel si les noms d'utilisateurs sont réutilisés avec des UID différents.

## Limites

| Limite | Valeur |
|-------|-------|
| Taille de fichier max | 2 Go par fichier |
| Contenu pris en charge | Fichiers réguliers dans le répertoire de premier niveau uniquement (pas de sous-répertoires, pas de liens symboliques) |
| Dotfiles | Ignorés (fichiers commençant par `.`) |
| Portée | LAN uniquement (multicast TTL=1, pas de routage hors sous-réseau) |
| Identité machine | Nom d'hôte — si deux machines partagent un nom d'hôte, longdrawer s'auto-embrouille |

**Garde-fou de pression disque.** Les annonces hors sous-réseau (`POST /longdrawer/announce`) sondent d'abord la cible via `GET /health/storage?for=longdrawer&user=<user>` et passent si le pair signale `free_pct <= 10` sur `~<user>/longdrawer`. Les annonces sont minuscules, mais si le destinataire ne peut pas stocker les fichiers qu'elles désignent, il est inutile d'annoncer aussi. Les annonces multicast LAN ne sont pas sondées — l'annonce elle-même fait moins d'1 Ko et les récepteurs tirent les fichiers via `GET /longdrawer/{user}/{filename}` à leur propre discrétion (ils s'auto-régulent déjà sur le disque avant d'écrire). Voir [Espace disque dans les opérations swarm](../../howto/swarm/set-up-swarm.md#disk-space).

## Prise en charge des plateformes

| Plateforme | Répertoire personnel | Propriétaire |
|----------|---------------|-----------|
| Linux | `/home/{user}/longdrawer/` | `chown user:user` |
| macOS | `/Users/{user}/longdrawer/` | `chown user:user` |
| Windows | `C:\Users\{user}\longdrawer\` | (non testé) |

## Sécurité

- Tout le transport est en mTLS, mêmes certificats de bootstrap que le serveur pair swarm
- Les fichiers sont stockés **en clair sur disque**. Longdrawer est une sync de confiance LAN ; quiconque a un shell sur une machine participante voit le contenu.
- La route HTTP longdrawer canonicalise les chemins et rejette tout ce qui se résout hors de `~/{user}/longdrawer/` pour prévenir l'évasion par lien symbolique
- Les noms de fichiers sont validés : pas de `/`, `\`, `\0`, `.`, `..`, ni `.` en tête
- Les utilisateurs ne peuvent synchroniser des fichiers que dans leur propre répertoire personnel sur les autres machines

**Pour une sync chiffrée**, utilisez [lockbox](set-up-lockbox.md) — le sous-système frère qui garde du texte chiffré au repos sur chaque Envoy et verrouille le déchiffrement derrière une identité age enveloppée par mot de passe par utilisateur. Longdrawer est délibérément non chiffré désormais ; l'opt-in `encrypt: true` de 0.25.0 a été retiré en 0.26.1 parce que maintenir deux modèles de chiffrement chevauchants brouillait le récit du modèle de menace.

## Récepteurs hors sous-réseau

Par défaut, longdrawer est LAN-only (multicast TTL=1). Pour atteindre un pair sur un sous-réseau différent, listez-le dans `~/.longdrawer/config.yaml` :

```yaml
cross_subnet_peers:
  - offsite-box.example.com
```

L'émetteur POSTe l'annonce de chaque cycle de scan directement à `https://offsite-box.example.com:1531/longdrawer/announce` en mTLS. Le récepteur alimente l'annonce dans le même chemin de réconciliation que le multicast — aucun changement de transport, aucune implication du serveur.

Les cross_subnet_peers inatteignables loguent une erreur de niveau debug et réessaient au cycle suivant ; les pannes transitoires ne bloquent pas la boucle de scan.

## Ce que Longdrawer ne fait PAS

- Pas de sync de sous-répertoires (seulement des fichiers plats au premier niveau)
- Pas de sync cross-LAN via multicast (TTL=1 par conception). Utilisez `cross_subnet_peers:` pour des cibles hors sous-réseau explicites.
- Pas d'état côté serveur, pas d'API, pas d'UI web
- Pas de résolution de conflit au-delà du last-write-wins
- Pas de versionnage ni d'historique
- Pas de correction de dérive d'horloge — de mauvaises horloges signifient une mauvaise sync
- Pas de chiffrement. Utilisez [lockbox](set-up-lockbox.md) si vous avez besoin d'une sync chiffrée.

## Observabilité (UI `/longdrawer`)

Chaque Envoy ayant un état longdrawer par utilisateur sur disque émet un trait `longdrawer` contenant l'ensemble des fichiers (sha + taille + mtime), les tombstones, et un digest stable de 16 octets sur l'ensemble complet des fichiers. L'agrégateur côté serveur (`server/swarm/longdrawermesh/`) les joint à l'échelle de la flotte pour la page `/longdrawer`.

Les pages `/longdrawer`, `/lockbox`, `/gitback` et chaque endpoint mesh (`/api/v1/swarm/longdrawer/*`, `/api/v1/swarm/lockbox/*`, `/api/v1/swarm/gitback/fleet`, et les drill-downs de fragments UI) sont réservés aux admins — les listes de fichiers par utilisateur à l'échelle de la flotte ne conviennent pas aux rôles non-admin. Les utilisateurs des rôles viewer et compliance reçoivent un 403 en accès direct et ne voient pas les liens de navigation. L'URL héritée `/mesh` redirige en 301 vers `/longdrawer` (ou `/lockbox` avec `?tab=crypt`) pendant une version, le temps que la mémoire musculaire migre.

Surfaces :

- **Matrice de présence** — par (utilisateur × Envoy × nom de fichier) vivant / tombstoné / manquant / divergent (sha non concordants)
- **Rapport d'Envoy à trois états** — reporting / périmé (>5 min) / not_reporting
- **Endpoint de drill-down** — `GET /api/v1/swarm/longdrawer/envoy/{id}/users/{user}/files` pour la liste de fichiers non bornée quand un opérateur clique pour explorer.

Longdrawer est du texte clair LAN-only, il n'y a donc pas de signal de clé de signature ou d'état de chiffrement à exposer (contrairement à lockbox).

## Dépannage

Si les fichiers ne se synchronisent pas entre deux machines :

1. **Même LAN ?** Longdrawer ne traverse pas les routeurs. Vérifiez que les deux Envoys peuvent se multicaster l'un à l'autre — `tcpdump -i <iface> host 224.0.0.43` devrait montrer les annonces du pair.
2. **Même compte utilisateur ?** Les deux machines ont besoin d'un utilisateur avec exactement le même nom d'utilisateur. La synchronisation se fait par utilisateur.
3. **`~/longdrawer/` existe sur les deux ?** Un utilisateur sans le répertoire ne participe pas.
4. **Horloges synchronisées ?** Le last-write-wins fait confiance aux mtimes — des horloges fortement décalées feront que les fichiers ping-pongent ou que les perdants apparaissent.
5. **mTLS sain ?** Longdrawer utilise les mêmes certificats d'enrôlement que le serveur pair swarm. Si `curl https://<peer>:1531/health` échoue d'une machine à l'autre, longdrawer échoue aussi.
6. **Vérifiez `/var/lib/vigo/swarm/longdrawer/<user>.json`** pour le snapshot courant et le log de tombstones.

## Opérabilité

Longdrawer est contrôlé par Envoy via la liste de motifs `swarm.longdrawer.enabled` dans server.yaml (0.41.0+). À la fois `swarm.enabled` ET `swarm.longdrawer.enabled` doivent autoriser l'hôte. Il n'y a pas de drapeau usercrate — l'opt-in par utilisateur se fait par la création de `~/longdrawer/` sur sa propre machine par l'utilisateur.

```yaml
swarm:
  enabled: ["*"]
  longdrawer:
    enabled: ["-rasp1", "*"]   # everything except rasp1
```

Un hôte qui ne correspond pas à la liste de motifs cesse d'annoncer en multicast (`224.0.0.43:1532`), cesse de répondre aux requêtes des pairs, et met au repos son gestionnaire de push — barrière câblée via `Substrate::Longdrawer` dans `agent/src/swarm/gates.rs`. Le contenu utilisateur sous `~user/longdrawer/` est préservé à travers les bascules de motif (gate-only par conception — le contenu longdrawer est une donnée utilisateur en clair ; la suppression destructive est toujours une action manuelle de l'opérateur).

Les changements de liste de motifs enregistrent des entrées de chaîne d'audit infalsifiables (`swarm.longdrawer.enabled`) sous `actor=config.reload` au moment de la publication. Voir [Opérabilité du swarm](set-up-swarm.md#operability--pattern-list-authorization) pour le tableau complet.

## Connexe

- [Swarm](set-up-swarm.md) — Distribution de blobs P2P au niveau infrastructure (contrôlée par l'opérateur, orchestrée par le serveur)
- [Opérations Swarm](../../howto/swarm/set-up-swarm.md) — Configuration et dépannage du swarm
- [Gitback](set-up-gitback.md) — Miroir git de DR personnel (sous-système sœur ; P2P pur depuis 0.23.0, les récepteurs ne détiennent que des dépôts bare root-owned)

## La suite

- **Vous avez besoin de confidentialité au repos** → [Mettre en place Lockbox](set-up-lockbox.md). Même forme « déposez un fichier » ; texte chiffré sur chaque Envoy.
- **Vous devez atteindre un Envoy sur un sous-réseau différent** → ajoutez-le à `~/.longdrawer/config.yaml` sous `cross_subnet_peers:` (le scan multicast ne franchit jamais les routeurs, par conception).
- **Un fichier ne se propage pas** → exécutez la liste de dépannage ci-dessus ; s'il persiste, [Dépanner les problèmes courants](../operate/troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
