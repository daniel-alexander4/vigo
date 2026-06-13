<!-- vigo-i18n source_sha:424fc26baeb18a111d3296c652a85906901aefc86ca5186c7319de192cbc5d31 -->

# Configurer Lockbox

À la fin de cette page, `~/lockbox/` sera synchronisé sur chaque Envoy où un utilisateur est enrôlé — le texte clair y est déposé, le texte chiffré est diffusé, chaque Envoy conserve les octets au repos en mode 0600 et seule la phrase de passe du Puddle de l'utilisateur les déchiffre. La cérémonie de déverrouillage se greffe sur `puddle unlock` — aucun mot de passe distinct.

**Quand l'utiliser :** tout utilisateur disposant d'un jeu de secrets / d'identifiants / de clés SSH / de fichiers personnels par utilisateur qu'il veut rendre disponible sur chaque machine qu'il utilise, sans faire confiance au disque de l'une d'elles. Les opérateurs utilisent couramment Lockbox pour les clés SSH, les exports de gestionnaire de mots de passe, les fichiers de licence et les clés GPG.

**Quand le sauter :** les fichiers qui doivent vivre sur le LAN en texte clair (utilisez [longdrawer](set-up-longdrawer.md) — même forme, sans chiffrement) ; les fichiers qu'un admin pousse (utilisez Filecast) ; les secrets qui ne devraient jamais quitter le magasin de secrets du serveur (utilisez le préfixe de configuration `secret:`).

Lockbox maintient `~/lockbox/` synchronisé sur chaque Envoy où un utilisateur est enrôlé — **texte chiffré au repos** sur chaque disque. Déposez un fichier en texte clair sur n'importe quel Envoy ; Lockbox le chiffre vers la clé publique de chaque pair et diffuse le texte chiffré. Déverrouillez avec la phrase de passe du Puddle de l'utilisateur sur n'importe quel Envoy pour déchiffrer.

## L'activer

Lockbox est une capacité obligatoire de l'appartenance à un [puddle](set-up-puddle.md) — tout utilisateur ayant `puddle: true` sur son usercrate l'obtient, sans drapeau distinct nécessaire. Assurez-vous d'abord que le Puddle est configuré ([Configurer Puddle](set-up-puddle.md)), puis rien de plus n'est requis pour Lockbox.

La porte à l'échelle de la flotte se trouve aussi dans le même bloc `server.yaml` que le Puddle :

```yaml
swarm:
  puddle:
    enabled:
      - "*"
```

## L'utiliser

Sur n'importe quel Envoy où l'utilisateur a déverrouillé son Puddle :

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

L'arbre complet des verbes : `unlock,lock,encrypt,decrypt,delete,list,status,reencrypt,resurrect,purge`, plus `vigo swarm lockbox puddle init` / `puddle renew-cert` pour la récupération. Les verbes de cycle de vie (`init / leave / forget`) sont retirés — la sortie se fait via `vigo swarm puddle leave`, l'éviction par l'admin via `vigocli swarm puddle evict <host>`.

Lockbox est le frère « chiffrement d'abord » de [Longdrawer](set-up-longdrawer.md) :

| | Longdrawer | Lockbox |
|---|---|---|
| État au repos | Texte clair | **Texte chiffré** partout |
| Matériel de clé | S.O. | Identité age par Envoy, enveloppée par mot de passe |
| Accès en lecture | Toute connexion utilisateur | Nécessite `vigo swarm lockbox unlock` (mot de passe) |
| Déclencheur de diffusion | Analyse du répertoire toutes les 30 s | Surveillance du système de fichiers + CLI explicite |
| Appartenance | Tout utilisateur sur le LAN avec `~/longdrawer/` | Appartenance au Puddle (`puddle: true` sur l'usercrate) — Lockbox s'amorce sur `puddle unlock` |

Choisissez Longdrawer pour les fichiers de travail ambiants que vous voulez sur plusieurs machines. Choisissez Lockbox pour tout ce que vous voudriez garder illisible sur un Envoy volé.

## Comment ça marche

![Lockbox — déposez le texte clair, chiffrement automatique vers la clé publique de chaque pair, le texte chiffré est diffusé en 0600 partout](../../img/lockbox.svg)

Pur pair-à-pair — le serveur Vigo ne voit jamais ni le contenu ni les clés. Les Envoys se découvrent via le multicast UDP et échangent le texte chiffré directement sur mTLS sur le port pair du Swarm.

**Surface réseau** (à ouvrir pour le fonctionnement inter-hôtes) :

| Port / adresse | Objet |
|---|---|
| Multicast UDP `224.0.0.44:1533` | Annonces Lockbox par utilisateur toutes les 30 s. LAN uniquement par défaut. |
| TCP `1531` (mTLS) | Port pair du Swarm. Lockbox l'utilise pour `/lockbox/push/...`, `/lockbox/tombstones/...`, `/lockbox/leaves/...`, `/lockbox/resurrects/...`. |

Pairs inter-sous-réseaux (le multicast ne traverse pas les sous-réseaux) — listez-les explicitement :

```yaml
# ~/.lockbox/config.yaml
cross_subnet_peers:
  - envoy-london.internal
  - envoy-tokyo.internal
idle_timeout_minutes: 60       # default 60; resets on every DECRYPT/STATUS
```

Les deux côtés doivent faire confiance à la même CA de flotte et avoir les ports 1531 + 1533 accessibles.

### La boucle de convergence

1. **Amorçage (au premier `vigo swarm puddle unlock`).** L'agent génère une nouvelle paire de clés age X25519 pour cet Envoy, enveloppe la moitié privée sous une clé dérivée de l'assistant de session du Puddle (Argon2id + XChaCha20-Poly1305), écrit le blob enveloppé dans `~/.lockbox/identity.age` (mode 0600). La moitié publique va dans `~/.lockbox/recipients.txt` et commence à voyager dans les annonces.
2. **Découverte.** Toutes les 30 s, chaque Envoy enrôlé diffuse en multicast `{machine, user, pubkey, port}`. Les pairs exécutant Lockbox pour le même utilisateur le reçoivent, ajoutent la clé publique à leur table de pairs locale, régénèrent `recipients.txt`. Les pairs périmés (aucune annonce pendant 90 s) sont évincés.
3. **Chiffrement.** Soit explicite (`vigo swarm lockbox encrypt …`), soit déposer-et-oublier (copiez le texte clair dans `~/lockbox/` ; un observateur inotify/kqueue capte l'événement et chiffre sur place). La sortie est en mode 0600, possédée par l'utilisateur appelant, écrite via temp+renommage.
4. **Diffusion.** L'agent surveille `~/lockbox/` et fait un `POST` de chaque changement de texte chiffré vers chaque pair actif sur `https://<peer>:1531/lockbox/push/<user>/<filename>`. Les diffusions sont parallèles avec une concurrence bornée.
5. **Déchiffrement.** `vigo swarm lockbox unlock` demande la phrase de passe du Puddle (juste celle-là — la même que pour le Puddle), désenveloppe l'identité age dans un assistant de session en RAM, la conserve jusqu'au délai d'inactivité ou à la déconnexion. `decrypt` transmet via le socket de l'assistant vers le chemin de sortie choisi par l'opérateur.

### Chiffrement automatique à l'écriture

L'observateur chiffre automatiquement le texte clair qui atterrit dans `~/lockbox/`. Trois états :

| État | Comportement |
|---|---|
| Déverrouillé ET fichier ≤ 1 Gio | Chiffré sur place, renommage atomique. La diffusion se déclenche depuis l'événement inotify résultant. |
| Verrouillé | WARN + laisse le texte clair sur place. Le prochain `vigo swarm puddle unlock` exécute un **balayage de déverrouillage** qui parcourt `~/lockbox/` et chiffre tout ce qui échoue à la vérification du magic age. |
| Fichier > 1 Gio | WARN + laisse le texte clair. Correspond à la limite de diffusion de 1 Gio — un fichier qui ne peut pas être transmis ne devrait pas brûler de CPU au chiffrement. |

Les échecs de chiffrement sont renommés en `<filename>.<unix_ts>.failed` pour les garder hors du pipeline de diffusion. Récupération : renommez l'artefact vers son nom d'origine ; l'observateur déclenche un nouvel événement et le chiffrement réessaie depuis un état propre.

`vigo swarm lockbox status` décompose le nombre de fichiers en sous-totaux pour que vous voyiez d'un coup d'œil ce que l'observateur a fait :

```
files:            5
  ciphertext:     3
  plaintext:      1   (run 'vigo swarm lockbox unlock' to encrypt and sync)
  failed:         1
                  vigo-lic-backup.tar.gz.20260429.failed
```

### La suppression est une opération de maillage — pierres tombales

Supprimer un fichier (soit `vigo swarm lockbox delete <name>`, soit un simple `rm ~/lockbox/<name>`) se propage via une **enveloppe DELETE signée** diffusée à chaque pair. Chaque pair enregistre une **pierre tombale** (rétention : 90 jours) et supprime sa copie locale. Un pair qui n'a pas encore appris une suppression renvoie **410 Gone** pour ce nom de fichier — c'est ce qui empêche la copie périmée d'un pair en retard de le ressusciter.

**Réajouter un fichier supprimé fonctionne tout simplement.** Réajoutez le fichier — déposez-le dans `~/lockbox/` (le chiffrement automatique le prend en charge) ou exécutez `vigo swarm lockbox encrypt <name>` — et l'agent le ressuscite automatiquement en une étape : il enregistre et diffuse une résurrection signée (qui lève la porte 410 pour ce nom à travers le maillage) puis chiffre. Éditez d'abord le fichier et c'est la version éditée qui revient. Aucune étape distincte n'est nécessaire ; le verbe autonome `vigo swarm lockbox resurrect <file>` reste un synonyme explicite. (La porte 410 demeure absolue sur le chemin de *synchronisation* — seul un réajout local explicite enregistre la résurrection qui l'ouvre, de sorte qu'un pair périmé ne peut pas ressusciter automatiquement.)

Chaque Envoy possède sa propre clé de signature Ed25519 (`/var/lib/vigo/swarm/lockbox/<user>/signing.key`, possédée par root, gérée par le démon) utilisée pour autoriser les enveloppes DELETE / LEAVE / RESURRECT. Possédée par le démon (non enveloppée par mot de passe) afin que la suppression réactive fonctionne sans que l'utilisateur soit déverrouillé — l'éviction d'une clé de signature compromise se fait via `vigocli swarm puddle evict <host>` depuis le plan de contrôle.

Rattrapage après une partition : chaque annonce porte trois empreintes de 16 octets (`tombstones_digest`, `leaves_digest`, `resurrects_digest`). En cas de divergence, le pair tire la table complète correspondante depuis `GET /lockbox/{tombstones,leaves,resurrects}/<user>` et fusionne.

### Quitter le cercle

Deux chemins, même état final :

- **Auto-départ (opérateur sur l'Envoy qui part) :** `vigo swarm puddle leave --yes` — couvre le LEAVE de Lockbox, le nettoyage du Puddle et le nettoyage de Gitback. Le démon signe une enveloppe LEAVE et la diffuse ; chaque pair retire la clé publique partie de `recipients.txt` et met en file un rechiffrement. Le nettoyage local efface `~/.vigo-puddle/`, `~/.lockbox/`, `~/lockbox/`, `~/.vigo-gitback/` pour cet utilisateur.
- **Éviction par l'admin (l'Envoy est parti ou injoignable) :** `vigocli swarm puddle evict <host>` — répartition par le plan de contrôle, aucune enveloppe requise (l'autorité est le jeton admin, pas une signature P2P). Chaque agent enrôlé reçoit la tâche et retire la clé publique de la cible de sa table de pairs.

`vigo swarm lockbox purge --yes` est l'alternative locale uniquement : efface `~/.lockbox/` + `~/lockbox/` pour l'utilisateur appelant sans diffusion. Les pairs font vieillir cet Envoy via la règle de péremption de 90 secondes. Utilisez-la quand vous ne voulez pas annoncer le départ ; utilisez `puddle leave` quand vous le voulez.

**Dormance côté réception.** Si `~/lockbox/` est absent sur un Envoy (l'utilisateur ne s'est jamais enrôlé, l'a `rm -rf`'é, ou est entre un `purge` et un re-`init`), `POST /lockbox/push/...` renvoie **HTTP 410 Gone** avec `status: dormant`. L'agent ne recrée jamais silencieusement `~/lockbox/` à partir d'une diffusion d'un pair — l'adhésion doit venir du côté utilisateur (en exécutant `vigo swarm lockbox … init` ou `encrypt`). Quand l'utilisateur est retiré de `/etc/passwd` (p. ex. `userdel -r`), la prochaine réconciliation de l'observateur supprime entièrement `/var/lib/vigo/swarm/lockbox/<user>/` (y compris `leaves.json`) ; cela empêche un état périmé de se rattacher plus tard à un utilisateur du même nom.

### Limites

| Limite | Valeur |
|---|---|
| Taille de fichier max par diffusion | 1 Gio (correspond au plafond de chiffrement) |
| Contenu pris en charge | Fichiers réguliers dans `~/lockbox/` uniquement — pas de sous-répertoires, pas de liens symboliques |
| Confidentialité nom de fichier / taille / mtime | Non chiffrée — seul le *contenu* du fichier l'est |
| Récupération de mot de passe | Aucune — perdez la phrase de passe sur chaque Envoy et les données sont irrécupérables |
| Plateformes | Linux + macOS (Phase 1) |
| Refus en cas de pression disque | Lockbox saute les diffusions vers un pair signalant ≤ 10 % d'espace disque libre ; le `/lockbox/push/...` côté récepteur renvoie 507. Voir [Espace disque](set-up-swarm.md#disk-space). |

### Modèle de menace

| Lockbox protège contre | Lockbox NE protège PAS contre |
|---|---|
| Une compromission root sur un Envoy *sans* la phrase de passe du Puddle de l'utilisateur | Une compromission root sur un Envoy avec une session de déverrouillage active (root lit la RAM de l'assistant de session) |
| Le vol physique d'un Envoy sans session de déverrouillage active | Les enregistreurs de frappe / shells compromis / capture de phrase de passe |
| L'écoute réseau (mTLS + chiffrement de contenu age — défense en profondeur) | Les phrases de passe faibles (Argon2id ralentit le force brute ; ne l'empêche pas) |
| L'accès aux données par un hôte évincé (un `reencrypt` après éviction coupe l'hôte parti) | La fuite de nom de fichier / taille / mtime (délibéré ; correspond à la frontière de Longdrawer) |

La Phase 1 n'a pas de séquestre de clé. Perdez la phrase de passe sur chaque Envoy → les données sont irrécupérables.

### Où chercher quand ça ne marche pas

- **`vigo swarm lockbox status`** sur l'Envoy — affiche l'ensemble des destinataires, la ventilation du nombre de fichiers, l'état de l'assistant de session, la ligne d'écart d'absence prolongée.
- **Les journaux de l'agent** portent un WARN spécifique par échec actionnable (`recipients empty`, `disk full`, `permission denied`, `source vanished mid-read`, etc.) — pas de tempêtes de réessais, pas d'« encryption failed » opaque.
- **L'interface web `/lockbox`** (admin uniquement) — matrice de présence par (utilisateur × Envoy × nom de fichier), détection de dérive des destinataires, indicateurs de session active, rapport d'état à trois états des Envoys (reporting / stale / not_reporting).
- **`vigo swarm puddle status`** — si l'assistant de session du Puddle est en panne, Lockbox est verrouillé partout ; c'est la première chose à vérifier.

## Et ensuite

- **Ajoutez git sur le même substrat** → [Configurer Gitback](set-up-gitback.md). Même identité de Puddle ; dépôts de projet privés avec des blobs à contenu chiffré.
- **Un frère LAN non chiffré pour les fichiers de travail ambiants** → [Configurer Longdrawer](set-up-longdrawer.md). Même forme ; texte clair au repos ; ne traverse jamais le LAN.
- **Vous avez oublié une phrase de passe de Puddle** → aucune récupération en v1 ; réinitialisez le Puddle et rejoignez Lockbox. Voir les notes de récupération dans [Configurer Puddle](set-up-puddle.md).
- **Les fichiers ne se diffusent pas** → [Dépanner les problèmes courants](../operate/troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [../legal/license.md](../../legal/license.md).*
