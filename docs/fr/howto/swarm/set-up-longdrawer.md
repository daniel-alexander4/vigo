<!-- vigo-i18n source_sha:3056a192068f4754745621c0141361cd16b08a0c8995cf49c33b807a4fe93143 -->

# Mettre en place Longdrawer

À la fin de cette page, vous aurez `~/longdrawer/` synchronisé sur le LAN : déposez un fichier sur n'importe quel Envoy où vous avez un compte, il atterrit sur tous les autres Envoys du même domaine de diffusion en ~60 s ; supprimez-le et le tombstone se propage de la même manière. Aucun fichier de config, aucune commande CLI, aucune implication du serveur. L'activation consiste à « créer `~/longdrawer/` sur au moins un Envoy ».

**Quand l'utiliser :** des fichiers de travail ambiants que vous voulez présents sur chaque machine où vous vous connectez — notes de brouillon, ébauches de dotfiles, signets d'historique shell, travaux en cours que vous ne voulez pas transférer par scp. La portée LAN-uniquement est un choix de conception : les octets ne quittent pas la frontière de confiance du domaine de diffusion.

**Quand l'éviter :** tout ce que vous voulez au repos sur disque sous forme de texte chiffré (utilisez [lockbox](set-up-lockbox.md) — forme identique, chiffrement inclus) ; tout ce pour quoi vous avez besoin d'une diffusion à travers le WAN (longdrawer ne franchit pas les sous-réseaux sans une liste explicite de pairs) ; un état mieux conservé sous forme de dépôt git (utilisez [gitback](set-up-gitback.md)).

## L'activer

Longdrawer s'active par l'existence d'un répertoire — pas de flag usercrate, pas de gate dans server.yaml. **Créez `~/longdrawer/` sur au moins un Envoy** et l'agent le détecte lors du prochain scan de 30 secondes. Les autres Envoys hébergeant le même utilisateur avec `~/longdrawer/` commenceront à se synchroniser automatiquement.

Pour les pairs sur d'autres sous-réseaux (le multicast LAN ne les atteint pas), ajoutez-les à la config longdrawer de l'utilisateur :

```yaml
# /home/<user>/.longdrawer/config.yaml
cross_subnet_peers:
  - peer-host.example.com
  - 10.20.30.40
```

## L'utiliser

Il n'y a aucune commande CLI. Déposez un fichier dans `~/longdrawer/` ; il apparaît sur chaque pair. Retirez-le ; le tombstone se propage. C'est toute la surface opérateur.

Si un fichier ne se propage pas, vérifiez :

- `~/longdrawer/` existe des deux côtés (c'est un signal d'adhésion par existence).
- Le trafic multicast n'est pas filtré par un pare-feu sur `224.0.0.43:1532` (ou que la liste des pairs cross-subnet est définie, si les pairs sont sur des sous-réseaux différents).
- Le disque dispose de plus de 10 % d'espace libre des deux côtés (le seuil swarm-wide de refus en dessous s'applique).

## Comment ça marche

![Longdrawer — annonce multicast sur le LAN, une divergence de digest déclenche un pull, réconciliation fichier par fichier](../../img/longdrawer.svg)

Longdrawer est l'un des quatre sous-systèmes de contenu qui s'appuient sur le substrat [swarm](set-up-swarm.md). Le serveur ne joue aucun rôle dans le contenu ou la découverte — longdrawer réutilise le canal pair mTLS du swarm (port 1531) pour les transferts et exécute son propre groupe multicast (`224.0.0.43:1532`) pour les annonces.

Fonctionnalité pure pair-à-pair sur le LAN. Les Envoys se découvrent via le multicast UDP et échangent les fichiers directement en mTLS sur le port pair du swarm.

1. **Scan.** Toutes les 30 secondes, chaque agent scanne `~/longdrawer/` pour chaque utilisateur local. Les fichiers sont hachés (SHA-256), et l'état en mémoire (liste de fichiers + tombstones de suppression) est comparé au snapshot précédent. Les fichiers ajoutés apparaissent dans le nouvel état ; les fichiers supprimés reçoivent un tombstone horodaté à l'instant courant.
2. **Annonce.** Chaque agent diffuse en multicast une **annonce limitée au digest** sur `224.0.0.43:1532` : `{machine, user, port, generation, state_digest}`. Le digest est un SHA-256 tronqué à 16 octets sur l'ensemble trié des fichiers + tombstones. **Les chemins de fichiers, sha256 et mtimes ne circulent jamais sur le réseau** — tout ce qu'un observateur du LAN peut voir, c'est l'identité du pair et un court digest opaque. TTL=1, LAN-local.
3. **Comparaison du digest.** Chaque récepteur calcule son propre state_digest pour le même utilisateur. En cas de correspondance, on saute — déjà synchronisé. En cas de divergence, on récupère le `UserState` complet du pair via `GET /longdrawer/state/{user}` sur le canal mTLS swarm existant sur le port 1531.
4. **Réconciliation.** Avec l'état complet en main, le récepteur compare entrée par entrée :
    - **Le pair a un fichier que nous n'avons pas** → le récupérer.
    - **Le pair a une version plus récente** (mtime plus élevé) → la récupérer.
    - **Le pair a un tombstone que nous n'avons pas, et le mtime de notre fichier est antérieur à l'heure de suppression** → supprimer le fichier.
    - **Le pair a un tombstone mais notre fichier est plus récent que l'heure de suppression** → conserver le fichier (il a été ré-ajouté après la suppression) et notre prochaine annonce le repropagera.

    Ré-ajouter un fichier précédemment marqué d'un tombstone en le `mv`-ant de nouveau dans `~/longdrawer/` préserve le mtime d'origine, ce qui perdrait la comparaison face au tombstone (plus récent). Le scanner détecte ce cas — un fichier actuellement présent qui a un tombstone local — et avance le mtime du fichier sur disque à l'heure du scan courant avant l'annonce. Sans cet ajustement, les tombstones des pairs re-supprimeraient le fichier au prochain cycle de synchronisation.
5. **Transfert.** Les fichiers sont récupérés en HTTPS depuis le port pair swarm de l'émetteur (1531) via une route dédiée `/longdrawer/{user}/{filename}`. Le contenu est vérifié contre le SHA-256 annoncé avant d'être écrit à destination.
6. **Application.** L'agent récepteur écrit le fichier dans `~/{user}/longdrawer/{filename}` avec la bonne propriété (`chown user:user`) et le temps de modification préservé.

**Modèle de confidentialité (0.30.11+).** Les builds antérieurs diffusaient le JSON `UserState` complet (fichiers + tombstones avec chemins, hachages et mtimes) dans chaque paquet multicast. Sur tout domaine de diffusion partagé, cela divulguait le listing du répertoire de chaque utilisateur à quiconque écoutait. Le modèle limité au digest comble cette fuite : le réseau public ne transporte aucune métadonnée de fichier. Les noms de fichiers et les hachages ne sont révélés que sur le canal pair authentifié en mTLS, et seulement lorsque le récepteur a déjà une raison de demander (digest modifié).

## Comportement

| Action | Comportement |
|--------|----------|
| Ajouter un fichier | Synchronisé sur les autres machines en ~60 s |
| Mettre à jour un fichier | Le mtime le plus récent gagne, écrase sur les autres machines |
| Supprimer un fichier | Le tombstone se propage, le fichier disparaît des autres machines en ~60 s |
| Ré-ajouter un fichier supprimé | Le mtime plus récent l'emporte sur le tombstone, le fichier réapparaît partout |
| Éditions conflictuelles sur deux machines | Last-write-wins par mtime (faites confiance à l'horloge de l'OS — exécutez NTP) |
| `rm -rf ~/longdrawer/*` (vidage total) | **NE se propage PAS.** Les pairs conservent leurs copies et regarnissent cet Envoy au prochain cycle de synchronisation. Traitez ceci comme « efface ma copie locale », pas « efface partout ». |
| `rm -rf ~/longdrawer` (supprimer entièrement le répertoire) | Cet Envoy devient dormant pour l'utilisateur ; les pairs continuent sans lui. Un `mkdir ~/longdrawer/` ultérieur le réengage et le resynchronise depuis les pairs. |
| `userdel -r <user>` | L'état de cet Envoy pour l'utilisateur est balayé au prochain scan ; réutiliser le nom d'utilisateur plus tard repart de zéro (aucun tombstone résiduel de l'utilisateur précédent). |

## Activation

Aucune configuration. Longdrawer s'active automatiquement lorsque :

1. L'agent est en cours d'exécution et possède des certificats d'enrôlement (mTLS)
2. Un utilisateur a créé `~/longdrawer/` dans son répertoire personnel

Le répertoire n'est **pas** créé automatiquement — les utilisateurs y adhèrent en le créant. Symétriquement, si le répertoire est absent, le chemin de réception de l'agent rejette les annonces de pairs entrantes pour cet utilisateur (pas de recréation silencieuse) et le chemin de scan ignore complètement l'utilisateur. Pour quitter longdrawer sur un Envoy, supprimez `~/longdrawer/`.

## Stockage de l'état

L'agent conserve l'état par utilisateur (snapshots de fichiers + tombstones) sous `/var/lib/vigo/swarm/longdrawer/<user>.json`. Cela survit aux redémarrages de l'agent. **Les tombstones sont nettoyés par garbage collection après 30 jours** — après quoi, une machine longtemps hors ligne qui possède encore le fichier le ressuscitera lors de sa reconnexion. Si cela vous importe, ne laissez pas les machines rester hors ligne plus d'un mois.

Le fichier d'état d'un utilisateur est **supprimé automatiquement** lorsque cet utilisateur est retiré de `/etc/passwd` (par ex. via `userdel -r`). Cela empêche un état résiduel de se rattacher plus tard à un utilisateur de même nom — un risque réel si les noms d'utilisateur sont réutilisés avec des UID différents.

## Limites

| Limite | Valeur |
|-------|-------|
| Taille de fichier max | 2 Go par fichier |
| Contenu pris en charge | Fichiers réguliers dans le répertoire de premier niveau uniquement (pas de sous-répertoires, pas de liens symboliques) |
| Dotfiles | Ignorés (fichiers commençant par `.`) |
| Portée | LAN uniquement (multicast TTL=1, pas de routage cross-subnet) |
| Identité machine | Hostname — si deux machines partagent un hostname, longdrawer se brouillera lui-même |

**Garde-fou de pression disque.** Les annonces cross-subnet (`POST /longdrawer/announce`) sondent d'abord la cible à `GET /health/storage?for=longdrawer&user=<user>` et passent si le pair rapporte `free_pct <= 10` sur `~<user>/longdrawer`. Les annonces sont minuscules, mais si le destinataire ne peut pas stocker les fichiers qu'elles désignent, inutile d'annoncer non plus. Les annonces multicast LAN ne sont pas sondées — l'annonce elle-même fait moins d'1 Ko et les récepteurs récupèrent les fichiers via `GET /longdrawer/{user}/{filename}` à leur propre discrétion (ils s'auto-régulent déjà sur le disque avant d'écrire). Voir [Espace disque dans les opérations swarm](../../howto/swarm/set-up-swarm.md#disk-space).

## Prise en charge des plateformes

| Plateforme | Répertoire personnel | Propriété |
|----------|---------------|-----------|
| Linux | `/home/{user}/longdrawer/` | `chown user:user` |
| macOS | `/Users/{user}/longdrawer/` | `chown user:user` |
| Windows | `C:\Users\{user}\longdrawer\` | (non testé) |

## Sécurité

- Tout le transport est en mTLS, mêmes certificats d'amorçage que le serveur pair du swarm
- Les fichiers sont stockés **en clair sur le disque**. Longdrawer est une synchronisation de confiance LAN ; quiconque a un shell sur une machine participante voit le contenu.
- La route HTTP de longdrawer canonicalise les chemins et rejette tout ce qui se résout en dehors de `~/{user}/longdrawer/` pour empêcher une évasion par lien symbolique
- Les noms de fichiers sont validés : pas de `/`, `\`, `\0`, `.`, `..`, ni de `.` en tête
- Les utilisateurs ne peuvent synchroniser des fichiers que dans leur propre répertoire personnel sur les autres machines

**Pour une synchronisation chiffrée**, utilisez [lockbox](set-up-lockbox.md) — le sous-système frère qui conserve le texte chiffré au repos sur chaque Envoy et conditionne le déchiffrement derrière une identité age enveloppée par un mot de passe par utilisateur. Longdrawer est délibérément non chiffré aujourd'hui ; l'option `encrypt: true` de la 0.25.0 a été retirée en 0.26.1 car maintenir deux modèles de chiffrement qui se chevauchaient brouillait le discours sur le modèle de menace.

## Récepteurs cross-subnet

Par défaut, longdrawer est LAN-uniquement (multicast TTL=1). Pour atteindre un pair sur un autre sous-réseau, listez-le dans `~/.longdrawer/config.yaml` :

```yaml
cross_subnet_peers:
  - offsite-box.example.com
```

L'émetteur POST l'annonce de chaque cycle de scan directement vers `https://offsite-box.example.com:1531/longdrawer/announce` en mTLS. Le récepteur alimente l'annonce dans le même chemin de réconciliation que le multicast — aucun changement de transport, aucune implication du serveur.

Les cross_subnet_peers injoignables loguent une erreur de niveau debug et réessaient au cycle suivant ; les pannes transitoires ne bloquent pas la boucle de scan.

## Ce que Longdrawer NE fait PAS

- Pas de synchronisation de sous-répertoires (uniquement des fichiers plats au niveau supérieur)
- Pas de synchronisation cross-LAN via multicast (TTL=1 par conception). Utilisez `cross_subnet_peers:` pour des cibles cross-subnet explicites.
- Pas d'état côté serveur, pas d'API, pas d'interface web
- Pas de résolution de conflit au-delà du last-write-wins
- Pas de versioning ni d'historique
- Pas de correction de décalage d'horloge — de mauvaises horloges signifient une mauvaise synchronisation
- Pas de chiffrement. Utilisez [lockbox](set-up-lockbox.md) si vous avez besoin d'une synchronisation chiffrée.

## Observabilité (interface `/longdrawer`)

Chaque Envoy ayant un état longdrawer par utilisateur sur disque émet un trait `longdrawer` contenant l'ensemble des fichiers (sha + taille + mtime), les tombstones et un digest stable de 16 octets sur l'ensemble complet des fichiers. L'agrégateur côté serveur (`server/swarm/longdrawermesh/`) les joint à travers la flotte pour la page `/longdrawer`.

Les pages `/longdrawer`, `/lockbox`, `/gitback` et chaque endpoint mesh (`/api/v1/swarm/longdrawer/*`, `/api/v1/swarm/lockbox/*`, `/api/v1/swarm/gitback/fleet`, ainsi que les drill-downs de fragments UI) sont réservés aux admins — les listes de fichiers par utilisateur à l'échelle de la flotte ne conviennent pas aux rôles non-admin. Les utilisateurs aux rôles viewer et compliance reçoivent un 403 en accès direct et ne voient pas les liens de navigation. L'ancienne URL `/mesh` redirige en 301 vers `/longdrawer` (ou `/lockbox` avec `?tab=crypt`) pendant une release, le temps que la mémoire musculaire migre.

Surfaces :

- **Matrice de présence** — par (utilisateur × Envoy × nom de fichier) live / tombstoné / manquant / divergent (sha non concordants)
- **Rapport d'Envoy à trois états** — reporting / stale (>5 min) / not_reporting
- **Endpoint de drill-down** — `GET /api/v1/swarm/longdrawer/envoy/{id}/users/{user}/files` pour la liste de fichiers non bornée lorsqu'un opérateur clique pour y accéder.

Longdrawer est du texte en clair LAN-uniquement, il n'y a donc aucun signal de clé de signature ou d'état de chiffrement à exposer (contrairement à lockbox).

## Dépannage

Si les fichiers ne se synchronisent pas entre deux machines :

1. **Même LAN ?** Longdrawer ne franchit pas les routeurs. Vérifiez que les deux Envoys peuvent se multicaster mutuellement — `tcpdump -i <iface> host 224.0.0.43` devrait montrer les annonces du pair.
2. **Même compte utilisateur ?** Les deux machines ont besoin d'un utilisateur avec exactement le même nom d'utilisateur. La synchronisation se fait par utilisateur.
3. **`~/longdrawer/` existe-t-il des deux côtés ?** Un utilisateur sans le répertoire ne participe pas.
4. **Horloges synchronisées ?** Le last-write-wins fait confiance aux mtimes — des horloges fortement décalées feront que les fichiers font des allers-retours ou que les perdants apparaissent.
5. **mTLS en bonne santé ?** Longdrawer utilise les mêmes certificats d'enrôlement que le serveur pair du swarm. Si `curl https://<peer>:1531/health` échoue d'une machine à une autre, longdrawer échoue aussi.
6. **Vérifiez `/var/lib/vigo/swarm/longdrawer/<user>.json`** pour le snapshot courant et le journal des tombstones.

## Opérabilité

Longdrawer est conditionné par Envoy via la liste de patterns `swarm.longdrawer.enabled` dans server.yaml (0.41.0+). À la fois `swarm.enabled` ET `swarm.longdrawer.enabled` doivent autoriser l'hôte. Il n'y a pas de flag usercrate — l'adhésion par utilisateur se fait en créant `~/longdrawer/` sur sa propre machine.

```yaml
swarm:
  enabled: ["*"]
  longdrawer:
    enabled: ["-rasp1", "*"]   # everything except rasp1
```

Un hôte qui ne correspond pas à la liste de patterns cesse d'annoncer en multicast (`224.0.0.43:1532`), cesse de répondre aux requêtes des pairs et met son gestionnaire de push au repos — gate câblé via `Substrate::Longdrawer` dans `agent/src/swarm/gates.rs`. Le contenu utilisateur sous `~user/longdrawer/` est préservé à travers les bascules de pattern (gate-only par conception — le contenu longdrawer est des données utilisateur en clair ; une suppression destructive est toujours une action opérateur manuelle).

Les changements de la liste de patterns enregistrent des entrées de chaîne d'audit infalsifiables (`swarm.longdrawer.enabled`) sous `actor=config.reload` au moment de la publication. Voir [Opérabilité du swarm](set-up-swarm.md#operability--pattern-list-authorization) pour le tableau complet.

## Voir aussi

- [Swarm](set-up-swarm.md) — Distribution de blobs P2P au niveau infrastructure (contrôlée par l'opérateur, orchestrée par le serveur)
- [Opérations Swarm](../../howto/swarm/set-up-swarm.md) — Configuration et dépannage du swarm
- [Gitback](set-up-gitback.md) — Miroir git pour DR personnel (sous-système jumeau ; pur P2P depuis la 0.23.0, les récepteurs ne détiennent que des dépôts bare détenus par root)

## Et ensuite

- **Vous avez besoin de confidentialité au repos** → [Mettre en place Lockbox](set-up-lockbox.md). Même forme « déposer-un-fichier » ; texte chiffré sur chaque Envoy.
- **Vous devez atteindre un Envoy sur un autre sous-réseau** → ajoutez-le à `~/.longdrawer/config.yaml` sous `cross_subnet_peers:` (le scan multicast ne franchit jamais les routeurs par conception).
- **Un fichier ne se propage pas** → exécutez la liste de dépannage ci-dessus ; s'il persiste, [Dépanner les problèmes courants](../operate/troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [../legal/license.md](../../legal/license.md).*
