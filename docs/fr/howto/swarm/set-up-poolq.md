<!-- vigo-i18n source_sha:579867ba31bd8e61c4cd0838082810863c8fa782a4e85345642e245db6d0d094 -->

# Configurer Poolq

Au terme de cette page, vous disposerez d'un journal ordonné de type Kafka sur la flotte — un Envoy publie des messages signés et numérotés par séquence sur un topic nommé, chaque autre Envoy peut les récupérer dans l'ordre, rejouer depuis un offset choisi, et faire confiance au contenu parce que la clé Puddle du fondateur a signé chacun d'eux de bout en bout. Les publications sont réservées aux membres du Puddle ; les lectures sont universelles.

**Quand l'utiliser :** des événements petits, ordonnés et en ajout seul que vous voulez propager à toute la flotte — éléments de travail de tâches de build, marqueurs de déploiement, avis « X-publié-à-Y », bulletins de sécurité, partout où `journalctl` serait trop local et un e-mail trop lourd. Poolq fait gagner sa place quand le même petit lot d'octets doit atterrir sur plus d'un Envoy *dans l'ordre*.

**Quand l'éviter :** les charges utiles volumineuses (>16 KiB par message ; associez un artefact [curator](set-up-curator.md) à un message poolq qui le référence par id) ; le contenu confidentiel (utilisez [lockbox](set-up-lockbox.md) — poolq est lisible par toute la flotte au repos) ; les fichiers non structurés ad hoc (utilisez filecast) ; un état qui devrait vivre sous forme de dépôt git (utilisez [gitback](set-up-gitback.md)).

## L'activer

Par Envoy : adhérez via `server.yaml` :

```yaml
swarm:
  poolq:
    enabled:
      - "*"                   # pattern list, first-match-wins; "-" prefix denies
    retention: "7d"           # age window; older messages prune, stale-on-ingest refused
    max_msg_bytes: 16384      # per-message body cap; default 16 KiB, hard ceiling 64 KiB
```

Par utilisateur : accordez `poolq: true` sur l'usercrate (plus lourd que `gitback:` — accès en écriture à une file lisible par toute la flotte) :

```yaml
resources:
  - type: user
    username: dan
    puddle: true              # required
    poolq: true               # opt-in for poolq publish
```

Publiez (`sudo vigocli config publish`).

## L'utiliser

Depuis un Envoy où l'opérateur a déverrouillé le Puddle :

```bash
# Post a message (the topic is implicitly created by the first post):
vigo swarm poolq post build-tasks --body '{"task":"deploy","host":"plex"}'

# Post from stdin:
echo '{"task":"rollback"}' | vigo swarm poolq post build-tasks --stdin

# List every topic the fleet knows about:
vigo swarm poolq topics

# Subscribe + read (offset tracked locally per user):
vigo swarm poolq subscribe build-tasks
vigo swarm poolq read build-tasks                # advances the offset
vigo swarm poolq read build-tasks --limit 5

# Inspect without touching the offset:
vigo swarm poolq tail build-tasks --limit 20     # latest N messages

# Replay:
vigo swarm poolq seek build-tasks 0              # next read returns from seq 1
vigo swarm poolq offset build-tasks              # show current offset
```

Depuis le serveur d'administration (`vigocli`) — vues opérateur plus modération :

```bash
vigocli swarm poolq list                          # fleet topic table
vigocli swarm poolq show <topic_id>               # full signed log
vigocli swarm poolq read <topic_id> --from 1 --to 10
vigocli swarm poolq block <topic_id>              # server refuses to serve the log
vigocli swarm poolq unblock <topic_id>
```

## Comment ça marche

Poolq est le sixième sous-système de contenu Swarm. Il réutilise trois éléments de la machinerie de flotte existante et ajoute une chose nouvelle — un numéro de séquence monotone estampillé par le fondateur — qui transforme un ensemble répliqué en CRDT en un journal ordonné.

### Identité

`topic_id = sha256(founder_puddle_pubkey ‖ topic_name)` — la forme du `project_id` de gitback / `artifact_id` de curator. Deux opérateurs peuvent tous deux publier `build-tasks` sans collision ; le Puddle propre de l'opérateur est l'espace de noms par défaut implicite, donc `vigo swarm poolq read build-tasks` se résout vers le topic de l'utilisateur local. Les lectures inter-Puddle utilisent le `topic_id` hexadécimal (fonctionne toujours, auto-certifiant).

Il n'existe pas de verbe « créer un topic » distinct. Un topic existe dès que son premier message signé atterrit.

### Le message

Chaque message est une petite enveloppe JSON signée :

```jsonc
{
  "body": {
    "schema_version": 1,
    "topic_name": "build-tasks",
    "seq": 17,                            // founder-monotonic, gap-free, inside the signed bytes
    "body": "{\"task\":\"deploy\"}",      // UTF-8 text, capped at swarm.poolq.max_msg_bytes
    "posted_at": 1714521600123,           // unix ms
    "author_puddle_pubkey": "<hex 32 B>"  // == founder in v1
  },
  "signature": "<hex 64 B ed25519 over canonical_bytes(body)>"
}
```

`seq` est **à l'intérieur** des octets canoniques signés — une tentative de réordonnancement invalide la signature. Le domaine de signature est `poolq-msg-v1\0` (distinct de toute autre forme signée de Vigo), de sorte qu'une signature provenant d'un sous-système Vigo ne peut pas être rejouée sous un autre.

### Publication — chemin d'écriture

`vigo swarm poolq post <topic> --body <text>` s'exécute en tant qu'utilisateur réel avec `poolq: true` et un Puddle déverrouillé. La CLI (pas de démon assistant — voir ADR-029) :

1. Valide le nom du topic + la taille du corps (plafond opérateur et le plafond strict de 64 KiB).
2. Acquiert l'allocateur de seq par topic à `~/.vigo-poolq/<topic_id>/allocator.json` sous `flock(LOCK_EX)`, lit `next_seq`, l'attribue, incrémente le fichier, libère le verrou.
3. Construit les octets canoniques (préfixe POOLQ_MSG_DOMAIN + schema_version + author_pubkey + topic_name + seq + posted_at + body) et les envoie au verbe `SIGN` de l'assistant de session Puddle (`$XDG_RUNTIME_DIR/vigo/puddle.sock`).
4. Assemble le Message + signature, l'écrit atomiquement dans `~/.vigo-poolq/<topic_id>/messages/<msg_id>.json` (msg_id = sha256 des canonical_bytes — adressé par contenu, idempotent).

Les publications concurrentes du même utilisateur sur le même Envoy se sérialisent sur le flock ; deux terminaux ne peuvent pas frapper le même seq.

### Réplication — trait → mesh → REST

À chaque cycle de check-in, le collecteur de traits `poolq` de chaque Envoy parcourt `~/<user>/.vigo-poolq/<topic_id>/messages/*.json` pour chaque utilisateur Unix, écarte tout ce dont le nom de fichier n'est pas égal à son propre `msg_id` ou dont la signature ne se vérifie pas, et émet le reste verbatim au serveur. L'agrégateur `poolqmesh` du serveur :

- Redérive `topic_id` à partir des `(author_pubkey, topic_name)` de chaque message et le range par seau. Deux fondateurs publiant sous le même `topic_name` se retrouvent dans des seaux différents — c'est l'application v1 fondateur-seul, implicite dans la dérivation du topic_id.
- Fusionne le G-Set par topic sur `msg_id` (messages immuables → octets identiques → id identique → une seule ligne).
- Applique un seul prédicat à la fois comme purge par âge et refus stale-on-ingest : `posted_at < (now - swarm.poolq.retention)` ⇒ rejet. Stable face au flap sans tombstones — une fois qu'un message expire partout, il reste écarté.

Le serveur ne vérifie **pas** les signatures Ed25519 — `poolqmesh` est un relais, la vérification de bout en bout a lieu côté consommateur (`vigo swarm poolq read`, `vigocli swarm poolq show`). Même posture que celle adoptée par les revendications de noms de curator et gitback ; évite un jumeau côté Go de `agent/src/swarm/poolq/message.rs`.

### Lecture — universelle, médiée par le serveur

La surface REST se trouve à `/api/v1/swarm/poolq/` :

```
GET /topics                              → every known topic + summary
GET /topic/{topic_id}/head                → one topic's summary (cheap poll)
GET /topic/{topic_id}/range?from&to       → message slice for a consumer's catch-up
GET /topic/{topic_id}/log                 → full topic for inspection
```

Les quatre sont publics par construction — mTLS est le garde du transport — de sorte qu'un Envoy avec Swarm désactivé peut consommer des topics de la même façon qu'il récupère les octets d'artefact curator via `/blob/<sha>`.

Les offsets de consommateur vivent par utilisateur dans `~/.vigo-poolq/subscriptions.json` et ne sont **jamais répliqués**. C'est la propriété qui fait que le modèle de type Kafka s'adapte à un substrat CRDT sans consensus : chaque consommateur possède son curseur, et rejouer un topic ne coûte rien (`vigo swarm poolq seek build-tasks 0` rembobine ; le prochain `read` repart depuis le seq 1).

### Autorisation

Deux couches contrôlent la publication. Les deux doivent dire oui :

| Couche | Où | Question |
|---|---|---|
| Envoy | liste de patterns `server.yaml#swarm.poolq.enabled` | poolq est-il disponible sur cet Envoy tout court ? |
| Utilisateur | flag `poolq: bool` de l'usercrate | cet utilisateur Unix peut-il publier ? |

Plus la couche Puddle-opérateur au moment de la signature : l'assistant de session Puddle de l'utilisateur doit être en cours d'exécution (`vigo swarm puddle unlock`).

`poolq: true` est un octroi **plus lourd** que `gitback: true` — « peut publier sur une file lisible par toute la flotte » contre « peut fonder des dépôts git personnels ». Le retirer d'un usercrate déclenche un nettoyage à la prochaine réconciliation (`~/.vigo-poolq/` effacé pour cet utilisateur). Les messages déjà publiés restent dans la vue de flotte jusqu'à leur expiration — vous ne pouvez pas les rappeler en perdant un flag, même posture que celle adoptée par ADR-024 pour curator.

La lecture est **non contrôlée** — les messages sont lisibles par toute la flotte.

Le filet de sécurité de modération opérateur est `vigocli swarm poolq block <topic_id>` (réservé aux admins). Le serveur cesse de servir les endpoints `/range` et `/log` du topic à l'échelle de la flotte, peu importe qui l'a fondé (le résumé du topic apparaît toujours dans `/topics` avec `"blocked": true`, de sorte que consommateurs et opérateurs voient qu'il a été bloqué). Les messages signés existent toujours dans les répertoires `~/.vigo-poolq/` des utilisateurs ayant publié — seul un `rm -rf` sur le répertoire d'état du fondateur purge vraiment.

### Quitter poolq / mettre hors service un utilisateur

`userdel -r <user>` supprime `~/.vigo-poolq/` avec le reste du répertoire personnel ; le collecteur de traits cesse de voir les messages de l'utilisateur au cycle suivant, et ils expirent de la vue de flotte selon `swarm.poolq.retention`. Retirer `poolq: true` de l'usercrate (sans supprimer l'utilisateur) déclenche le nettoyage de `~/.vigo-poolq/` par utilisateur à la prochaine réconciliation — même forme que le retrait de flag de curator et gitback.

### Hors périmètre v1

Voir la section « Out of v1 scope » de l'ADR-029 pour les fonctionnalités différées : consommateurs concurrents / ack-redeliver (la forme file de travail de type RabbitMQ), partitions, publieurs non-fondateurs délégués, fast-path multicast Tier-1 pour une latence inférieure à la cadence, hooks de livraison push actifs (wall / MOTD / exec-on-message), topics chiffrés/privés, cache de journal de flotte côté agent, blocage par publieur.
