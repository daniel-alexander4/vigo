<!-- vigo-i18n source_sha:a628cf5566f5e1a8d0efa51050e42ca0cd33fe0c32a0f2655eb89512b0ad6d4f -->

# Configurer Lockbox

À la fin de cette page, `~/lockbox/` se synchronisera sur chaque envoy où un utilisateur est enrôlé — le texte en clair y est déposé, le texte chiffré est diffusé, chaque envoy conserve les octets au repos en mode 0600 et seule la phrase de passe du puddle de l'utilisateur les déchiffre. La cérémonie de déverrouillage s'appuie sur `puddle unlock` — pas de mot de passe séparé.

**Quand l'utiliser :** tout utilisateur disposant d'un secret / d'un identifiant / d'un jeu de clés SSH / de fichiers personnels par utilisateur qu'il veut rendre disponibles sur chaque machine qu'il utilise, sans faire confiance au disque d'aucune d'entre elles. Les opérateurs utilisent couramment lockbox pour les clés SSH, les exports de gestionnaires de mots de passe, les fichiers de licence, les clés GPG.

**Quand ne pas l'utiliser :** des fichiers qui devraient résider sur le LAN en clair (utilisez [longdrawer](set-up-longdrawer.md) — même forme, sans chiffrement) ; des fichiers qu'un administrateur pousse (utilisez filecast) ; des secrets qui ne devraient jamais quitter le magasin de secrets du serveur (utilisez le préfixe de configuration `secret:`).

Lockbox maintient `~/lockbox/` synchronisé sur chaque envoy où un utilisateur est enrôlé — **texte chiffré au repos** sur chaque disque. Déposez un fichier en clair sur n'importe quel envoy ; lockbox le chiffre vers la clé publique de chaque pair et diffuse le texte chiffré. Déverrouillez avec la phrase de passe du puddle de l'utilisateur sur n'importe quel envoy pour déchiffrer.

## L'activer

Lockbox est une capacité obligatoire de l'appartenance à un [puddle](set-up-puddle.md) — tout utilisateur avec `puddle: true` sur son usercrate l'obtient, sans drapeau séparé. Assurez-vous que le puddle est configuré d'abord ([Configurer Puddle](set-up-puddle.md)), puis rien de plus n'est requis pour lockbox.

La barrière à l'échelle de la flotte se trouve également dans le même bloc `server.yaml` que puddle :

```yaml
swarm:
  puddle:
    enabled:
      - "*"
```

## L'utiliser

Sur n'importe quel envoy où l'utilisateur a déverrouillé son puddle :

```bash
# First-time setup on this envoy (runs automatically on `puddle unlock`):
vigo swarm puddle unlock                           # also bootstraps lockbox

# Encrypt-on-write:
cp /tmp/secrets.txt ~/lockbox/secrets.txt          # auto-encrypted in place

# Or explicit:
vigo swarm lockbox encrypt ~/Downloads/foo.pdf

# Decrypt out of lockbox on demand:
vigo swarm lockbox unlock                          # session-helper holds the key in RAM
vigo swarm lockbox decrypt secrets.txt -o /tmp/out

# Status / list:
vigo swarm lockbox status
vigo swarm lockbox list
```

L'arbre complet des verbes : `unlock,lock,encrypt,decrypt,delete,list,status,reencrypt,resurrect,purge`, plus `vigo swarm lockbox puddle init` / `puddle renew-cert` pour la récupération. Les verbes de cycle de vie (`init / leave / forget`) sont retirés — la sortie se fait via `vigo swarm puddle leave`, l'éviction par l'administrateur via `vigocli swarm puddle evict <host>`.

Lockbox est le frère chiffrement-d'abord de [Longdrawer](set-up-longdrawer.md) :

| | Longdrawer | Lockbox |
|---|---|---|
| État au repos | Texte en clair | **Texte chiffré** partout |
| Matériel de clé | S/O | Identité age par envoy, protégée par mot de passe |
| Accès en lecture | Toute connexion utilisateur | Nécessite `vigo swarm lockbox unlock` (mot de passe) |
| Déclencheur de diffusion | Analyse du répertoire toutes les 30s | Surveillance du système de fichiers + CLI explicite |
| Appartenance | Tout utilisateur sur le LAN avec `~/longdrawer/` | Appartenance à un puddle (`puddle: true` sur l'usercrate) — lockbox s'amorce au `puddle unlock` |

Choisissez longdrawer pour les fichiers de travail ambiants que vous voulez sur toutes les machines. Choisissez lockbox pour tout ce que vous voudriez garder illisible sur un envoy volé.

## Comment ça marche

![Lockbox — déposez du texte en clair, chiffrement automatique vers la clé publique de chaque pair, le texte chiffré est diffusé en 0600 partout](../../img/lockbox.svg)

Pur pair-à-pair — le serveur Vigo ne voit jamais ni le contenu ni les clés. Les envoys se découvrent mutuellement via multicast UDP et échangent le texte chiffré directement par mTLS sur le port pair du swarm.

**Surface réseau** (ouvrez ces ports pour le fonctionnement inter-hôtes) :

| Port / adresse | Objectif |
|---|---|
| Multicast UDP `224.0.0.44:1533` | Annonces lockbox par utilisateur toutes les 30s. LAN-uniquement par défaut. |
| TCP `1531` (mTLS) | Port pair du swarm. Lockbox l'utilise pour `/lockbox/push/...`, `/lockbox/tombstones/...`, `/lockbox/leaves/...`, `/lockbox/resurrects/...`. |

Pairs inter-sous-réseaux (le multicast ne traverse pas les sous-réseaux) — listez-les explicitement :

```yaml
# ~/.lockbox/config.yaml
cross_subnet_peers:
  - envoy-london.internal
  - envoy-tokyo.internal
idle_timeout_minutes: 60       # default 60; resets on every DECRYPT/STATUS
```

Les deux côtés doivent faire confiance à la même autorité de certification de la flotte et avoir les ports 1531 + 1533 accessibles.

### La boucle de convergence

1. **Amorçage (au premier `vigo swarm puddle unlock`).** L'agent génère une nouvelle paire de clés age X25519 pour cet envoy, protège la moitié privée sous une clé dérivée de l'assistant de session du puddle (Argon2id + XChaCha20-Poly1305), écrit le blob protégé dans `~/.lockbox/identity.age` (mode 0600). La moitié publique va dans `~/.lockbox/recipients.txt` et commence à figurer dans les annonces.
2. **Découverte.** Toutes les 30s, chaque envoy enrôlé diffuse en multicast `{machine, user, pubkey, port}`. Les pairs exécutant lockbox pour le même utilisateur le reçoivent, ajoutent la clé publique à leur table de pairs locale, régénèrent `recipients.txt`. Les pairs périmés (aucune annonce depuis 90s) sont évincés.
3. **Chiffrement.** Soit explicite (`vigo swarm lockbox encrypt …`), soit déposer-et-oublier (copier du texte en clair dans `~/lockbox/` ; un observateur inotify/kqueue capte l'événement et chiffre sur place). La sortie est en mode 0600, détenue par l'utilisateur appelant, écrite via temp+rename.
4. **Poussée.** L'agent surveille `~/lockbox/` et fait un `POST` de chaque modification de texte chiffré vers chaque pair actif à `https://<peer>:1531/lockbox/push/<user>/<filename>`. Les poussées sont parallèles avec une concurrence bornée.
5. **Déchiffrement.** `vigo swarm lockbox unlock` demande la phrase de passe du puddle (juste celle-là — la même que puddle), déprotège l'identité age dans un assistant de session en RAM, la conserve jusqu'au délai d'inactivité ou à la déconnexion. `decrypt` transmet en flux à travers le socket de l'assistant vers le chemin de sortie choisi par l'opérateur.

### Chiffrement automatique à l'écriture

L'observateur chiffre automatiquement le texte en clair qui atterrit dans `~/lockbox/`. Trois états :

| État | Comportement |
|---|---|
| Déverrouillé ET fichier ≤ 1 Gio | Chiffré sur place, renommage atomique. La diffusion de la poussée se déclenche à partir de l'événement inotify résultant. |
| Verrouillé | WARN + laisser le texte en clair sur place. Le prochain `vigo swarm puddle unlock` exécute un **balayage de déverrouillage** qui parcourt `~/lockbox/` et chiffre tout ce qui échoue à la vérification de la signature magique age. |
| Fichier > 1 Gio | WARN + laisser le texte en clair. Correspond à la limite de poussée de 1 Gio — un fichier qui ne peut pas être transmis ne devrait pas brûler de CPU en chiffrement. |

Les échecs de chiffrement sont renommés en `<filename>.<unix_ts>.failed` pour les tenir hors du pipeline de poussée. Récupération : renommez l'artefact à son nom d'origine ; l'observateur déclenche un nouvel événement et le chiffrement réessaie depuis un état propre.

`vigo swarm lockbox status` décompose le nombre de fichiers en sous-totaux pour que vous voyiez d'un coup d'œil ce que l'observateur a fait :

```
files:            5
  ciphertext:     3
  plaintext:      1   (run 'vigo swarm lockbox unlock' to encrypt and sync)
  failed:         1
                  vigo-lic-backup.tar.gz.20260429.failed
```

### La suppression est une opération de maillage — pierres tombales

La suppression d'un fichier (soit `vigo swarm lockbox delete <name>`, soit un simple `rm ~/lockbox/<name>`) se propage via une **enveloppe DELETE signée** diffusée vers chaque pair. Chaque pair enregistre une **pierre tombale** (rétention : 90 jours) et supprime sa copie locale. Un pair qui n'a pas encore appris une suppression retourne **410 Gone** pour ce nom de fichier — c'est ce qui empêche la copie périmée d'un pair en retard de la ressusciter.

**Réajouter un fichier supprimé fonctionne tout simplement.** Réajoutez le fichier — déposez-le dans `~/lockbox/` (le chiffrement automatique le prend en charge) ou exécutez `vigo swarm lockbox encrypt <name>` — et l'agent le ressuscite automatiquement en une seule étape : il enregistre et propage par gossip une résurrection signée (qui lève la barrière 410 pour ce nom à travers le maillage) puis chiffre. Éditez d'abord le fichier et c'est la version éditée qui revient. Aucune étape séparée n'est nécessaire ; le verbe autonome `vigo swarm lockbox resurrect <file>` reste comme synonyme explicite. (La barrière 410 reste absolue sur le chemin de *synchronisation* — seul un réajout local explicite enregistre la résurrection qui l'ouvre, de sorte qu'un pair périmé ne peut pas se ressusciter automatiquement.)

Chaque envoy possède sa propre clé de signature Ed25519 (`/var/lib/vigo/swarm/lockbox/<user>/signing.key`, détenue par root, gérée par le démon) utilisée pour autoriser les enveloppes DELETE / LEAVE / RESURRECT. Détenue par le démon (non protégée par mot de passe) afin que la suppression réactive fonctionne sans que l'utilisateur soit déverrouillé — l'éviction d'une clé de signature compromise se fait via `vigocli swarm puddle evict <host>` depuis le plan de contrôle.

Rattrapage après une partition : chaque annonce porte trois condensés de 16 octets (`tombstones_digest`, `leaves_digest`, `resurrects_digest`). En cas de divergence, le pair récupère la table correspondante complète depuis `GET /lockbox/{tombstones,leaves,resurrects}/<user>` et fusionne.

### Quitter le cercle

Deux chemins, même état final :

- **Auto-départ (opérateur sur l'envoy qui part) :** `vigo swarm puddle leave --yes` — couvre le LEAVE de lockbox, le nettoyage du puddle et le nettoyage de gitback. Le démon signe une enveloppe LEAVE et la diffuse ; chaque pair retire la clé publique partie de `recipients.txt` et met en file un re-chiffrement. Le nettoyage local efface `~/.vigo-puddle/`, `~/.lockbox/`, `~/lockbox/`, `~/.vigo-gitback/` pour cet utilisateur.
- **Éviction par l'administrateur (l'envoy est parti ou inaccessible) :** `vigocli swarm puddle evict <host>` — dispatch par le plan de contrôle, aucune enveloppe requise (l'autorité est le jeton admin, pas une signature P2P). Chaque agent enrôlé reçoit la tâche et retire la clé publique de la cible de sa table de pairs.

`vigo swarm lockbox purge --yes` est l'alternative locale-uniquement : efface `~/.lockbox/` + `~/lockbox/` pour l'utilisateur appelant sans gossip. Les pairs vieillissent cet envoy via la règle de péremption de 90 secondes. Utilisez-la quand vous ne voulez pas annoncer le départ ; utilisez `puddle leave` quand vous le voulez.

**Dormance côté réception.** Si `~/lockbox/` est absent sur un envoy (l'utilisateur ne s'est jamais enrôlé, l'a `rm -rf`'é, ou est entre `purge` et un nouveau `init`), `POST /lockbox/push/...` retourne **HTTP 410 Gone** avec `status: dormant`. L'agent ne recrée jamais silencieusement `~/lockbox/` à partir d'une poussée d'un pair — l'adhésion doit venir du côté utilisateur (exécution de `vigo swarm lockbox … init` ou `encrypt`). Lorsque l'utilisateur est retiré de `/etc/passwd` (par ex. `userdel -r`), la prochaine réconciliation de l'observateur efface entièrement `/var/lib/vigo/swarm/lockbox/<user>/` (y compris `leaves.json`) ; cela empêche un état périmé de se rattacher plus tard à un utilisateur du même nom.

### Limites

| Limite | Valeur |
|---|---|
| Taille de fichier max par poussée | 1 Gio (correspond au plafond de chiffrement) |
| Contenu pris en charge | Fichiers réguliers dans `~/lockbox/` uniquement — pas de sous-répertoires, pas de liens symboliques |
| Confidentialité du nom / de la taille / du mtime | Non chiffrée — seul le *contenu* du fichier l'est |
| Récupération du mot de passe | Aucune — perdez la phrase de passe sur chaque envoy et les données sont irrécupérables |
| Plateformes | Linux + macOS (Phase 1) |
| Refus en cas de pression disque | Lockbox saute les poussées vers un pair signalant ≤10% de disque libre ; côté récepteur, `/lockbox/push/...` retourne 507. Voir [Espace disque](set-up-swarm.md#disk-space). |

### Modèle de menace

| Lockbox protège contre | Lockbox NE protège PAS contre |
|---|---|
| Une compromission root sur un envoy *sans* la phrase de passe du puddle de l'utilisateur | Une compromission root sur un envoy avec une session de déverrouillage active (root lit la RAM de l'assistant de session) |
| Le vol physique d'un envoy sans session de déverrouillage active | Les enregistreurs de frappe / shells compromis / capture de phrase de passe |
| L'écoute réseau (mTLS + chiffrement de contenu age — défense en profondeur) | Les phrases de passe faibles (Argon2id ralentit la force brute ; ne l'empêche pas) |
| L'accès aux données par un hôte évincé (un `reencrypt` après éviction coupe l'hôte parti) | La fuite de nom de fichier / de taille / de mtime (délibérée ; correspond à la frontière de longdrawer) |

La Phase 1 n'a pas de séquestre de clé. Perdez la phrase de passe sur chaque envoy → les données sont irrécupérables.

### Où regarder quand ça ne marche pas

- **`vigo swarm lockbox status`** sur l'envoy — affiche le jeu de destinataires, la ventilation du nombre de fichiers, l'état de l'assistant de session, la ligne d'écart de longue absence.
- **Les journaux de l'agent** portent un WARN spécifique par échec exploitable (`recipients empty`, `disk full`, `permission denied`, `source vanished mid-read`, etc.) — pas de tempêtes de réessai, pas de « encryption failed » opaque.
- **L'interface web `/lockbox`** (admin uniquement) — matrice de présence par (utilisateur × envoy × nom de fichier), détection de dérive des destinataires, indicateurs de session active, rapport d'envoy à trois états (reporting / stale / not_reporting).
- **`vigo swarm puddle status`** — si l'assistant de session du puddle est en panne, lockbox est verrouillé partout ; c'est la première chose à vérifier.

## Et ensuite

- **Ajouter git sur le même substrat** → [Configurer Gitback](set-up-gitback.md). Même identité puddle ; dépôts de projets privés avec des blobs à contenu chiffré.
- **Un frère LAN non chiffré pour les fichiers de travail ambiants** → [Configurer Longdrawer](set-up-longdrawer.md). Même forme ; texte en clair au repos ; ne traverse jamais le LAN.
- **Vous avez oublié une phrase de passe de puddle** → pas de récupération en v1 ; ré-initialisez le puddle et rejoignez lockbox. Voir les notes de récupération dans [Configurer Puddle](set-up-puddle.md).
- **Les fichiers ne se diffusent pas** → [Dépanner les problèmes courants](../operate/troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
