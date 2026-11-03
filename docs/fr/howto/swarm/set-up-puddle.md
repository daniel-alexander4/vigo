<!-- vigo-i18n source_sha:50b5273f9d643148add08f1bc15d57afce137534bb468e4d3bb325682d337f49 -->

# Configurer un Puddle

À la fin de cette page, vous aurez un puddle (identité Ed25519 par utilisateur) initialisé sur un envoy et propagé à chaque autre envoy qu'un utilisateur donné touche — apparié pair-à-pair avec un code à usage unique valable 5 minutes. Une fois le puddle en place, les sous-systèmes de contenu par utilisateur ([lockbox](set-up-lockbox.md), [gitback](set-up-gitback.md)) s'amorcent par-dessus.

**Quand l'utiliser :** avant d'activer lockbox ou gitback pour un utilisateur. Le puddle est l'identité substrat que ces sous-systèmes consomment.

**Quand l'ignorer :** les utilisateurs qui n'ont besoin que de contenu poussé par l'administrateur (filecast, curator) — ceux-ci ne requièrent pas de puddle. Le réglage désactivé par défaut sur le champ `puddle:` de l'usercrate convient aux comptes de service et aux utilisateurs humains qui n'ont pas besoin d'état chiffré par utilisateur.

Un **puddle** est l'identité par utilisateur qui relie « la collection d'envoys de cet utilisateur, traitée comme une seule unité de confiance ». C'est le prérequis de lockbox (répertoire chiffré par utilisateur) et de gitback (dépôts git clés sur puddle) — les deux sous-systèmes consomment l'appartenance au puddle pour exprimer qui peut lire / écrire.

## L'activer

Le puddle est par utilisateur, pas par flotte. Accordez à un utilisateur le flag `puddle: true` dans son usercrate :

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

Sur le premier envoy de l'utilisateur (l'**envoy fondateur**) :

```bash
vigo swarm puddle init       # generate keypair, set passphrase
vigo swarm puddle status     # confirm
```

Sur chaque envoy supplémentaire où l'utilisateur veut la même identité puddle :

```bash
# On the founder envoy:
vigo swarm puddle pair       # prints a 5-minute single-use code

# On the new envoy:
vigo swarm puddle join --from <founder-host>   # claims the pair code over swarm mTLS
vigo swarm puddle unlock                       # enter passphrase, bring the local session up
```

L'arbre complet des verbes du puddle : `init,pair,join,rekey-pair,rekey,unlock,lock,pubkey,members,rotate-passphrase,status,name {set,clear,show}`. Au quotidien, l'opérateur ne touche qu'à `unlock` (à chaque connexion) et à `status` / `members` par curiosité.


## Comment ça marche

![Puddle — appariement pair-à-pair plus un assistant de session que les sous-systèmes voisins consomment](../../img/puddle.svg)

Un puddle est une paire de clés Ed25519 par utilisateur représentant « la collection d'envoys de cet utilisateur, traitée comme une seule unité de confiance ». Sans lui, chaque sous-système par utilisateur (lockbox, gitback) devrait refaire la cérémonie d'appariement pair-à-pair par envoy. Le puddle est ce primitif : une paire de clés, plusieurs envoys, distribuable pair-à-pair.

La paire de clés du puddle est générée localement par `vigo swarm puddle init`. La moitié privée est enveloppée sous la phrase de passe de l'utilisateur via Argon2id (64 Mio / 3 itér. / 1 voie) + XChaCha20-Poly1305 avec un nonce frais — la même enveloppe que lockbox utilise pour ses clés d'identité. Le blob enveloppé est **portable** entre envoys ; chaque envoy membre détient des octets identiques pour `~/.vigo-puddle/identity.wrapped`. La phrase de passe de déchiffrement n'est détenue que par l'utilisateur.

### Appartenance

Un envoy est membre d'un puddle lorsque ces trois fichiers existent ensemble :

- `~/.vigo-puddle/identity.wrapped` (mode 0600) — clé privée enveloppée par phrase de passe
- `~/.vigo-puddle/identity.salt` (mode 0600) — entrée Argon2id
- `~/.vigo-puddle/identity.pub` (mode 0644) — clé publique du puddle `ed25519:<hex>`

Il n'y a pas de registre central. Les sous-systèmes voisins (lockbox, gitback) détectent « l'utilisateur X a-t-il un puddle activé ici ? » en vérifiant ces fichiers.

### Intégrer un nouvel envoy — appariement pair-à-pair (principal)

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

Le blob enveloppé et le sel traversent le réseau via swarm mTLS sur le port 1531. La **phrase de passe ne quitte jamais B**. Le code d'appariement est l'autorisation humaine explicite empêchant tout autre pair de confiance CA de demander silencieusement le blob enveloppé — même modèle que l'appariement Signal / Bluetooth.

L'autorisation est double : (1) mTLS — l'appelant est un envoy enrôlé dans la même CA Vigo ; (2) le code d'appariement — un secret partagé à usage unique valable 5 minutes que l'utilisateur vient de dicter.

`sudo` sur `join` parce que le fichier de clé mTLS de l'agent (`tls.key`, mode 0600 appartenant à root) est nécessaire pour l'appel mTLS sortant. Les autres verbes du puddle (`init`, `pair`, `unlock`, `lock`, etc.) restent dans le home de l'utilisateur et n'ont pas besoin de root.

### Intégration via scp manuel (repli en environnement isolé)

Pour les envoys sans connectivité swarm vers un membre actif du puddle :

```
[A]  scp ~/.vigo-puddle/identity.{wrapped,salt,pub}  usb:/path/
[B]  cp -r usb:/path/  ~/.vigo-puddle/
[B]  vigo swarm puddle unlock
```

Le blob enveloppé est du chiffré opaque sur le disque ; la frontière de sécurité est l'enveloppement par phrase de passe, pas le transport.

### Assistant de session

`unlock` lit le blob enveloppé, demande la phrase de passe, le déchiffre en une clé de signature Ed25519, puis lance un démon par utilisateur détaché par `setsid` qui garde la clé en mémoire de processus et sert SIGN / STATUS / PUBKEY / LOCK sur un socket Unix protégé par SO_PEERCRED (Linux : `$XDG_RUNTIME_DIR/vigo/puddle.sock` ; macOS : `$TMPDIR/lockbox-<UID>/puddle.sock` ; tous deux mode 0600).

La clé ne réside jamais en clair sur le disque ; les octets déchiffrés sont passés via stdin à l'assistant et immédiatement remis à zéro aux deux extrémités.

Trois déclencheurs indépendants effacent chacun la clé et quittent :

- `vigo swarm puddle lock` explicite,
- le PID du shell parent disparaît (chien de garde par sondage à 10 s),
- expiration d'inactivité (24 heures par défaut ; réinitialisée à chaque SIGN / STATUS ; surchargeable par déverrouillage avec `--idle-mins <N>`).

### Noms conviviaux (ADR-022)

`gitback://<project_id>/<repo>` est auto-certifiant mais peu mémorisable. Une **revendication de nom** lie un nom humain à une clé publique de puddle afin que `gitback://<name>/<repo>` se résolve. `vigo swarm puddle name set <name>` (requiert un puddle déverrouillé) écrit un blob signé `~/.vigo-puddle/name-claim.json` ; le collecteur de trait `puddle` le rapporte ; `server/swarm/puddlemesh/` joint les revendications à l'échelle de la flotte (et `gitbackmesh` consomme cette carte pour l'indice d'affichage `gitback://<name>/<handle>`) ; les agents mettent en cache la carte résultante à `/var/lib/vigo/swarm/puddle/names.json` (rafraîchie au démarrage + toutes les 15 min, hors du chemin critique du check-in).

**Premier arrivé, appliqué de façon coopérative ; ambigu en cas de collision.** `name set` refuse un nom qu'un autre puddle détient dans la vue actuelle de l'agent ; il ne peut pas battre la fenêtre de propagation. Deux revendications indépendantes pour le même nom → les résolveurs renvoient une erreur et exigent la forme hexadécimale. Pas de départage automatique. **Un nom est un indice, pas une ancre de confiance** ; la forme infalsifiable reste toujours `gitback://<project_id>/<repo>`.

### Fichiers d'état

Côté utilisateur, tous sous `~/.vigo-puddle/` (mode 0700, appartenant à l'utilisateur) :

| Fichier | Mode | Rôle |
|---|---|---|
| `identity.wrapped` | 0600 | Clé privée Ed25519, enveloppée par phrase de passe. Portable entre envoys. |
| `identity.salt` | 0600 | 16 octets aléatoires, entrée Argon2id. Portable entre envoys. |
| `identity.pub` | 0644 | Clé publique du puddle `ed25519:<hex>`, publique ; les sous-systèmes voisins la lisent. |
| `pair.pending` | 0600 | Session de code d'appariement active. Supprimée à la première revendication ou à l'expiration du TTL. |
| `rekey.pending` | 0600 | Session de revendication de rekey active (même forme que `pair.pending`). |
| `retired_puddles.json` | 0600 | Journal de rotation (ADR-018). |
| `session.unlocked` | 0600 | PID de l'assistant de session ; les voisins sondent ceci + `pid_alive` pour détecter l'état de verrouillage. |
| `name-claim.json` | 0644 | Revendication de nom convivial signée (ADR-022). Publique par construction. |

Cache côté démon (hôte serveur uniquement) : `/var/lib/vigo/swarm/puddle/names.json` — la carte des noms conviviaux de la flotte.

### Modes de défaillance et limites

- **Phrase de passe oubliée.** Pas de récupération en v1. La réinitialisation crée un nouveau puddle (clé publique différente) ; les jetons de projet gitback et les destinataires lockbox existants deviennent orphelins. `vigo swarm puddle status` fait remonter les instructions de récupération.
- **Toutes les copies de clé enveloppée perdues sur tous les envoys.** Comme phrase de passe oubliée — réinitialisation à partir de zéro.
- **Un seul envoy compromis** (l'attaquant a le blob enveloppé *et* la phrase de passe). Le chiffrement de disque au niveau système est fortement recommandé ; l'expiration d'inactivité de l'assistant de session limite l'exposition du matériel de clé en mémoire.
- **Force brute du code d'appariement.** Espace de 100 M de codes (`NNNN-NNNN`) + TTL de 5 min + usage unique + appelants mTLS uniquement. Chaque tentative est imputable de façon forensique ; la force brute est infaisable.

La phase 1 diffère délibérément : multi-puddle par utilisateur (un seul puddle par utilisateur pour l'instant), récupération Shamir / quorum / séquestre (aucune — la perte de la phrase de passe est totale), et gossip inter-envoys de l'appartenance au puddle (`members` ne montre que la vue locale).

### Où regarder quand ça ne marche pas

- **`vigo swarm puddle status`** — état `init` / `unlock` / `pair-pending` / `rekey` ; indices de récupération.
- **Sentinelle `session.unlocked`** — si absente, l'assistant de session est arrêté ; les sous-systèmes voisins (lockbox, signature gitback) ne fonctionneront pas.
- **Les journaux de l'agent** comportent des lignes WARN pour les non-correspondances de pair-claim, les échecs de signature et les plantages de l'assistant.
- **Les ADR** pour la justification de conception : ADR-014 (le primitif), ADR-015 (gitback sur puddle), ADR-016 (lockbox sur puddle), ADR-017 (pas de révocation au niveau puddle), ADR-018 (rekey), ADR-022 (noms conviviaux).

## Le voir à l'échelle de la flotte

La page **Puddles** de l'interface web (`/swarm/puddle`) agrège chaque puddle sur toute la flotte — propriétaire (nom convivial, ou `(unnamed)`), clé publique courte, combien d'envoys le portent, à quels utilisateurs Unix il appartient, si une session est déverrouillée, et un badge de santé. C'est l'unique source de vérité en lecture seule pour « qui a un puddle, où, et est-il en bonne santé » — le substrat sur lequel lockbox, gitback, curator et poolq reposent tous.

![La vue flotte des Puddles — une ligne par puddle, avec le nombre d'envoys, les utilisateurs propriétaires, l'état de déverrouillage et la santé](../../img/screenshots/vigo.swarm-puddle.png)

La santé est `Healthy` / `Warning` / `Degraded` ; un puddle dont les envoys sont en désaccord sur l'appartenance ou dont les copies de clé ont dérivé apparaît ici avant que ses sous-systèmes dépendants ne commencent à échouer.

## Et ensuite

- **Activer les sous-systèmes de contenu par utilisateur** → [Configurer Lockbox](set-up-lockbox.md), [Configurer Gitback](set-up-gitback.md).
- **Vous avez oublié la phrase de passe** → réinitialisation à partir de zéro (le puddle n'a pas de récupération en v1) ; les destinataires lockbox et les jetons de projet gitback existants pour l'ancienne clé publique du puddle deviennent orphelins.
- **Un `pair` / `join` échoue** → [Dépanner les problèmes courants](../operate/troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
