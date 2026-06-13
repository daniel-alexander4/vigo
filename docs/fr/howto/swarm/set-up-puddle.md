<!-- vigo-i18n source_sha:956d4c3bca751f70afe8bdc3c48255c3442277971ac08cdbbac85a27a89b4b13 -->

# Configurer Puddle

À la fin de cette page, vous aurez un Puddle (identité Ed25519 par utilisateur) initialisé sur un Envoy et propagé à tous les autres Envoys qu'un utilisateur donné touche — appairés en pair-à-pair avec un code à usage unique valable 5 minutes. Une fois Puddle en place, les sous-systèmes de contenu par utilisateur ([lockbox](set-up-lockbox.md), [gitback](set-up-gitback.md)) s'amorcent par-dessus.

**Quand l'utiliser :** avant d'activer Lockbox ou Gitback pour un utilisateur. Puddle est l'identité de substrat que ces sous-systèmes consomment.

**Quand vous en passer :** les utilisateurs qui n'ont besoin que de contenu poussé par l'administrateur (Filecast, Curator) — ceux-là ne requièrent pas Puddle. La valeur désactivée par défaut du champ `puddle:` de l'usercrate convient aux comptes de service et aux utilisateurs humains qui n'ont pas besoin d'état chiffré par utilisateur.

Un **Puddle** est l'identité par utilisateur qui relie « la collection d'Envoys de cet utilisateur, traitée comme une seule unité de confiance ». C'est le prérequis pour Lockbox (répertoire chiffré par utilisateur) et Gitback (dépôts git indexés par Puddle) — les deux sous-systèmes consomment l'appartenance au Puddle pour exprimer qui peut lire / écrire.

## L'activer

Puddle est par utilisateur, pas par flotte. Accordez à un utilisateur le flag `puddle: true` dans son usercrate :

```yaml
# /srv/vigo/stacks/usercrates/dan.vgo
resources:
  - type: user
    username: dan
    home: /home/dan
    shell: /bin/bash
    puddle: true              # enables puddle + (as of 0.40.0) lockbox
    gitback: true             # opt-in for git-over-swarm too
    authorized_keys: |
      ssh-ed25519 AAAA... operator@laptop
```

Publiez (`sudo vigocli config publish`) ; la prochaine convergence de l'agent crée les répertoires nécessaires et installe les dépendances.

## L'utiliser

Sur le premier Envoy de l'utilisateur (l'**Envoy fondateur**) :

```bash
vigo swarm puddle init       # generate keypair, set passphrase
vigo swarm puddle status     # confirm
```

Sur chaque Envoy supplémentaire où l'utilisateur veut la même identité Puddle :

```bash
# On the founder envoy:
vigo swarm puddle pair       # prints a 5-minute single-use code

# On the new envoy:
vigo swarm puddle join --from <founder-host>   # claims the pair code over swarm mTLS
vigo swarm puddle unlock                       # enter passphrase, bring the local session up
```

L'arbre complet des verbes du Puddle : `init,pair,join,rekey-pair,rekey,unlock,lock,pubkey,members,rotate-passphrase,status,name {set,clear,show}`. Au quotidien, l'opérateur ne touche qu'à `unlock` (à chaque connexion) et à `status` / `members` par curiosité.


## Comment ça marche

![Puddle — appairage pair-à-pair plus un assistant de session que les sous-systèmes voisins consomment](../../img/puddle.svg)

Un Puddle est une paire de clés Ed25519 par utilisateur représentant « la collection d'Envoys de cet utilisateur, traitée comme une seule unité de confiance ». Sans lui, chaque sous-système par utilisateur (Lockbox, Gitback) devrait refaire la cérémonie d'appairage pair-à-pair par Envoy. Puddle est cette primitive : une paire de clés, plusieurs Envoys, distribuable en pair-à-pair.

La paire de clés du Puddle est générée localement par `vigo swarm puddle init`. La moitié privée est enveloppée sous la passphrase de l'utilisateur via Argon2id (64 Mio / 3 itér / 1 voie) + XChaCha20-Poly1305 avec un nonce frais — la même enveloppe que Lockbox utilise pour ses clés d'identité. Le blob enveloppé est **portable** entre Envoys ; chaque Envoy membre détient des octets identiques pour `~/.vigo-puddle/identity.wrapped`. La passphrase de déchiffrement n'est détenue que par l'utilisateur.

### Appartenance

Un Envoy est membre d'un Puddle lorsque ces trois fichiers existent ensemble :

- `~/.vigo-puddle/identity.wrapped` (mode 0600) — clé privée enveloppée par passphrase
- `~/.vigo-puddle/identity.salt` (mode 0600) — entrée Argon2id
- `~/.vigo-puddle/identity.pub` (mode 0644) — clé publique du Puddle `ed25519:<hex>`

Il n'y a pas de registre central. Les sous-systèmes voisins (Lockbox, Gitback) détectent « l'utilisateur X est-il activé pour Puddle ici ? » en vérifiant ces fichiers.

### Intégrer un nouvel Envoy — appairage pair-à-pair (méthode principale)

```
   founder envoy (A)              new envoy (B)
   ──────────────────             ─────────────────
   vigo swarm puddle pair
   → prints 8-char code           sudo vigo swarm puddle join --from A
   (5-min TTL, single-use)        → POST /swarm/puddle/pair-claim/<user>
                                    with {"code": "4827-9163"}
   (handler verifies code,
    returns wrapped blob)         (B writes blob + salt + pubkey to
                                   ~/.vigo-puddle/, prompts passphrase,
                                   verifies unwrap)
                                  vigo swarm puddle unlock   (no sudo)
```

Le blob enveloppé et le sel transitent sur le réseau via mTLS Swarm sur le port 1531. La **passphrase ne quitte jamais B**. Le code d'appairage est l'autorisation humaine explicite qui empêche tout autre pair de confiance dans la CA de demander silencieusement le blob enveloppé — le même modèle que l'appairage Signal / Bluetooth.

L'autorisation est double : (1) mTLS — l'appelant est un Envoy enrôlé dans la même CA Vigo ; (2) le code d'appairage — un secret partagé à usage unique valable 5 minutes que l'utilisateur vient de dicter.

`sudo` sur `join` parce que le fichier de clé mTLS de l'agent (`tls.key`, mode 0600 appartenant à root) est nécessaire pour l'appel mTLS sortant. Les autres verbes du Puddle (`init`, `pair`, `unlock`, `lock`, etc.) restent dans le répertoire personnel de l'utilisateur et n'ont pas besoin de root.

### Intégration via scp manuel (repli pour environnements isolés)

Pour les Envoys sans connectivité Swarm vers un membre actif du Puddle :

```
[A]  scp ~/.vigo-puddle/identity.{wrapped,salt,pub}  usb:/path/
[B]  cp -r usb:/path/  ~/.vigo-puddle/
[B]  vigo swarm puddle unlock
```

Le blob enveloppé est un texte chiffré opaque sur le disque ; la frontière de sécurité est l'enveloppe par passphrase, pas le transport.

### Assistant de session

`unlock` lit le blob enveloppé, demande la passphrase, le déchiffre en une clé de signature Ed25519, puis lance un démon par utilisateur détaché par `setsid` qui garde la clé en mémoire de processus et sert SIGN / STATUS / PUBKEY / LOCK sur un socket Unix protégé par SO_PEERCRED (Linux : `$XDG_RUNTIME_DIR/vigo/puddle.sock` ; macOS : `$TMPDIR/lockbox-<UID>/puddle.sock` ; les deux en mode 0600).

La clé ne réside jamais sur le disque en clair ; les octets déchiffrés sont passés via stdin à l'assistant et immédiatement remis à zéro aux deux bouts.

Trois déclencheurs indépendants effacent chacun la clé et quittent :

- `vigo swarm puddle lock` explicite,
- le PID du shell parent disparaît (chien de garde par sondage toutes les 10 s),
- délai d'inactivité (24 heures par défaut ; réinitialisé à chaque SIGN / STATUS ; surchargeable par déverrouillage avec `--idle-mins <N>`).

### Noms conviviaux (ADR-022)

`gitback://<project_id>/<repo>` est auto-certifiant mais impossible à mémoriser. Une **revendication de nom** lie un nom humain à une clé publique de Puddle pour que `gitback://<name>/<repo>` se résolve. `vigo swarm puddle name set <name>` (requiert un Puddle déverrouillé) écrit un blob signé `~/.vigo-puddle/name-claim.json` ; le collecteur de traits `puddle` le rapporte ; `server/swarm/puddlemesh/` joint les revendications à l'échelle de la flotte (et `gitbackmesh` consomme cette carte pour l'indication d'affichage `gitback://<name>/<handle>`) ; les agents mettent en cache la carte résultante dans `/var/lib/vigo/swarm/puddle/names.json` (rafraîchie au démarrage + toutes les 15 min, hors du chemin critique de check-in).

**Premier arrivé, appliqué de façon coopérative ; ambigu en cas de collision.** `name set` refuse un nom qu'un autre Puddle détient dans la vue actuelle de l'agent ; il ne peut pas devancer la fenêtre de propagation. Deux revendications non liées pour le même nom → les résolveurs renvoient une erreur et exigent la forme hexadécimale. Pas de départage automatique. **Un nom est une indication, pas une ancre de confiance** ; la forme infalsifiable est toujours `gitback://<project_id>/<repo>`.

### Fichiers d'état

Côté utilisateur, tous sous `~/.vigo-puddle/` (mode 0700, appartenant à l'utilisateur) :

| Fichier | Mode | Rôle |
|---|---|---|
| `identity.wrapped` | 0600 | Clé privée Ed25519, enveloppée par passphrase. Portable entre Envoys. |
| `identity.salt` | 0600 | 16 octets aléatoires, entrée Argon2id. Portable entre Envoys. |
| `identity.pub` | 0644 | Clé publique du Puddle `ed25519:<hex>`, publique ; les sous-systèmes voisins la lisent. |
| `pair.pending` | 0600 | Session de code d'appairage active. Supprimée à la première revendication ou à l'expiration du TTL. |
| `rekey.pending` | 0600 | Session de revendication de rekey active (même forme que `pair.pending`). |
| `retired_puddles.json` | 0600 | Journal de rotation (ADR-018). |
| `session.unlocked` | 0600 | PID de l'assistant de session ; les voisins sondent ceci + `pid_alive` pour détecter l'état de verrouillage. |
| `name-claim.json` | 0644 | Revendication de nom convivial signée (ADR-022). Publique par construction. |

Cache côté démon (hôte serveur uniquement) : `/var/lib/vigo/swarm/puddle/names.json` — la carte des noms conviviaux de la flotte.

### Modes de défaillance et limites

- **Passphrase oubliée.** Pas de récupération en v1. Une ré-initialisation crée un nouveau Puddle (clé publique différente) ; les jetons de projet Gitback existants et les destinataires Lockbox deviennent orphelins. `vigo swarm puddle status` fait remonter les instructions de récupération.
- **Toutes les copies de la clé enveloppée perdues sur tous les Envoys.** Identique à passphrase oubliée — ré-initialisation à partir de zéro.
- **Un seul Envoy compromis** (l'attaquant a le blob enveloppé *et* la passphrase). Le chiffrement du disque au niveau système est fortement recommandé ; le délai d'inactivité de l'assistant de session limite l'exposition du matériel de clé en mémoire.
- **Force brute du code de revendication d'appairage.** Espace de 100 M de codes (`NNNN-NNNN`) + TTL de 5 min + usage unique + appelants mTLS uniquement. Chaque tentative est attribuable de façon forensique ; la force brute est irréalisable.

La Phase 1 diffère délibérément : plusieurs Puddles par utilisateur (un seul Puddle par utilisateur pour l'instant), la récupération Shamir / quorum / séquestre (aucune — la perte de passphrase est totale), et le gossip inter-Envoys de l'appartenance au Puddle (`members` ne montre que la vue locale).

### Où chercher quand ça ne marche pas

- **`vigo swarm puddle status`** — état `init` / `unlock` / `pair-pending` / `rekey` ; pistes de récupération.
- **Sentinelle `session.unlocked`** — si absente, l'assistant de session est arrêté ; les sous-systèmes voisins (Lockbox, signature Gitback) ne fonctionneront pas.
- **Les journaux de l'agent** portent des lignes WARN pour les non-concordances de revendication d'appairage, les échecs de signature et les plantages de l'assistant.
- **Les ADR** pour la justification de conception : ADR-014 (la primitive), ADR-015 (Gitback sur Puddle), ADR-016 (Lockbox sur Puddle), ADR-017 (pas de révocation au niveau Puddle), ADR-018 (rekey), ADR-022 (noms conviviaux).

## La voir à l'échelle de la flotte

La page **Puddles** de l'interface web (`/swarm/puddle`) agrège chaque Puddle de toute la flotte — propriétaire (nom convivial, ou `(unnamed)`), clé publique courte, combien d'Envoys le portent, à quels utilisateurs Unix il appartient, si une session est déverrouillée, et un badge de santé. C'est la source unique de vérité en lecture seule pour « qui possède un Puddle, où, et est-il sain » — le substrat sur lequel Lockbox, Gitback, Curator et Poolq reposent tous.

![La vue de flotte des Puddles — une ligne par Puddle, avec le nombre d'Envoys, les utilisateurs propriétaires, l'état de déverrouillage et la santé](../../img/screenshots/vigo.swarm-puddle.png)

La santé est `Healthy` / `Warning` / `Degraded` ; un Puddle dont les Envoys sont en désaccord sur l'appartenance ou dont les copies de clé ont dérivé apparaît ici avant que ses sous-systèmes dépendants ne commencent à échouer.

## Et ensuite

- **Activer les sous-systèmes de contenu par utilisateur** → [Configurer Lockbox](set-up-lockbox.md), [Configurer Gitback](set-up-gitback.md).
- **Vous avez oublié la passphrase** → ré-initialisation à partir de zéro (Puddle n'a pas de récupération en v1) ; les destinataires Lockbox existants et les jetons de projet Gitback pour l'ancienne clé publique du Puddle deviennent orphelins.
- **Un `pair` / `join` échoue** → [Résoudre les problèmes courants](../operate/troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel — Alexander4, LLC. Reproduction interdite. Voir [../legal/license.md](../../legal/license.md).*
