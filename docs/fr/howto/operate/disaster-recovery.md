<!-- vigo-i18n source_sha:c7ec93a73da9b666b79383ac7b969112b5800743476e0bb3bafa044b45fe9763 -->

# Reprise après sinistre

Vous terminerez cette page avec un manuel de reprise écrit et testé pour « le serveur Vigo a disparu — panne de disque, hôte perdu, région hors service, un `rm -rf` tombé au mauvais endroit » — et une estimation calibrée du temps de reprise. La forme du manuel est `provision → restauration → publication → démarrage` ; le temps de reprise est dominé par le provisionnement, pas par Vigo. Cinq scénarios concrets sont couverts ci-dessous, par ordre croissant de difficulté.

**Quand l'utiliser :** avant d'en avoir besoin. Déroulez le scénario 1 contre une vraie archive de sauvegarde sur un hôte de préproduction une fois par trimestre. Ne découvrez pas pendant la vraie panne que votre clé maîtresse n'était pas réellement dans l'archive.

**Quand vous en passer :** jamais — même les flottes mono-serveur qui subissent une panne s'en sortent mieux avec cette lecture faite à l'avance.

Pour la configuration et la création des sauvegardes, voir [Sauvegarde & reprise](backup-and-recovery.md). Un fait important ci-dessous : **les agents survivent indéfiniment aux pannes du serveur** grâce à la politique mise en cache dans LMDB et aux résultats en file d'attente, donc une panne serveur de plusieurs heures n'est pas une panne de flotte.

## Ce dont vous avez besoin pour la reprise

Un serveur Vigo complet se compose de cinq éléments, tous stockés sous `/srv/vigo/` :

| Composant | Chemin | Rôle | Méthode de sauvegarde |
|-----------|------|---------|---------------|
| Base de données | `db/vigo.db` | Envoys enrôlés, historique des exécutions, traits, utilisateurs, jetons | `vigocli backup` ou Litestream |
| Fichiers de config | `stacks/` | Configcrates, rôles, mappages d'envoys, vars | `vigocli backup` (aussi : git) |
| Secrets | `secrets/` | Fichiers chiffrés AES-256-GCM + `.master.key` | `vigocli backup` |
| Certificats TLS | `tls/` | CA + cert/clé serveur (PEM en clair) | `vigocli backup` |
| Config serveur | `server.yaml` | Ports, intervalles, backends, SMTP, export | `vigocli backup` |

Rien d'autre n'a besoin d'être sauvegardé. Le FleetIndex, les compteurs de conformité et le cache de politique sont tous reconstruits à partir de la base de données au démarrage.

## Ce qui arrive aux agents pendant une panne

Les agents sont conçus pour survivre indéfiniment à l'indisponibilité du serveur :

- **Backoff exponentiel** : les tentatives de check-in se font à 5 s, 10 s, 20 s ... jusqu'à des intervalles de 5 minutes. Pas de crash, pas de perte de données.
- **Convergence hors ligne** : si l'agent dispose d'un bundle de politique mis en cache (stocké dans LMDB à `/var/lib/vigo/state/`), il continue d'appliquer les ressources localement selon son intervalle normal.
- **File d'attente des résultats en attente** : les résultats d'exécution et les mises à jour de traits sont mis en file d'attente localement et délivrés automatiquement au retour du serveur.
- **Aucun ré-enrôlement** : les agents se reconnectent en utilisant leur UUID et leur paire de clés ED25519 existants. Le serveur vérifie leur signature contre la clé publique stockée dans la base de données restaurée. Aucun jeton ni intervention manuelle nécessaires.

**Le seul scénario nécessitant un ré-enrôlement** est la perte de la base de données sans sauvegarde (les enregistrements d'envoys ont disparu) ou la suppression des fichiers d'état de l'agent à `/var/lib/vigo/state/`.

## Procédure de panne de disque

Cette section déroule le scénario de sinistre le plus courant : le disque de votre serveur tombe en panne.

**Moment de la panne.** Le disque tombe. Le conteneur vigosrv s'arrête. Les points d'accès gRPC et REST deviennent inaccessibles.

**Ce qui arrive aux envoys.** Rien de grave. Chaque agent dispose d'un bundle de politique signé mis en cache dans LMDB (`/var/lib/vigo/state/`). Ils continuent de converger selon leur intervalle de check-in normal. Le check-in échoue, l'agent journalise un avertissement, applique un backoff exponentiel aux tentatives, et continue d'appliquer la politique mise en cache. Les résultats d'exécution et les mises à jour de traits sont mis en file d'attente localement dans la file d'attente LMDB. Aucun envoy ne cesse d'appliquer l'état.

**Ce que vous avez perdu.** La visibilité du tableau de bord, la possibilité de publier des changements de config, l'enrôlement de nouveaux envoys, la répartition des tâches et le reporting des traits. Aucun envoy ne perd sa conformité.

**Reprise.**

1. **Provisionner un nouveau serveur.** Lancez une nouvelle VM ou un nouvel hôte de conteneurs. Quelques secondes dans le cloud, quelques minutes sur du bare metal.

2. **Restaurer `/srv/vigo`.** C'est là que votre stratégie de sauvegarde détermine le temps de reprise :

   | Méthode de sauvegarde | Temps de reprise | Fenêtre de perte de données |
   |---|---|---|
   | Snapshot de volume cloud (horaire) | ~1-2 min (attacher le snapshot) | Jusqu'à 1 heure de changements de config |
   | Litestream + sauvegarde du système de fichiers | ~1-2 min | Quelques secondes pour la BD, selon la fréquence de sauvegarde de la config |
   | rsync/Borg/restic vers un site distant (horaire) | ~2-5 min | Jusqu'à 1 heure |
   | Snapshot complet de VM | ~1-2 min (démarrage depuis le snapshot) | Depuis le dernier snapshot |

3. **Démarrer le conteneur.** `docker run` avec le volume `/srv/vigo` restauré monté. vigosrv démarre, charge la config, réhydrate le FleetIndex depuis SQLite. Quelques secondes, même à 10 000 envoys.

4. **Les envoys se reconnectent.** Les agents en backoff exponentiel réessaient en quelques secondes à quelques minutes. Lors d'un check-in réussi, les résultats en attente se vident de la file d'attente LMDB. Le FleetIndex se peuple avec des horodatages frais. Le tableau de bord reprend vie.

**Temps de reprise total : 5-15 minutes.** L'essentiel est le provisionnement du nouveau serveur et la restauration du volume. Le démarrage de vigosrv lui-même prend quelques secondes.


**Le point à surveiller.** Litestream ne couvre que la base de données SQLite. Les fichiers de config (`stacks/`), les secrets, les certificats TLS et `server.yaml` ont besoin d'une sauvegarde du système de fichiers distincte. Si votre dernière sauvegarde du système de fichiers date d'une heure et que vous avez publié des changements de config 10 minutes avant la panne du disque, ces changements sont perdus. Conservez `stacks/` dans git pour éliminer ce risque.

## Procédures de reprise

### Scénario 1 : restauration complète du serveur depuis une sauvegarde manuelle

Vous disposez d'une archive de sauvegarde créée par `vigocli backup create`.

```bash
# 1. Install Vigo on the new host
#    (Docker image, package, or build from source)

# 2. Restore from backup archive
vigocli backup restore /path/to/vigo-backup.tar.gz

# 3. Publish config to activate it
vigocli config publish

# 4. Start the server
systemctl start vigosrv
# or: docker compose up -d
```

Le serveur va :
1. Charger les secrets en utilisant la `.master.key` restaurée
2. Ouvrir la base de données restaurée, exécuter les migrations en attente
3. Reconstruire le FleetIndex à partir de la base de données (tous les envoys, traits, statut de conformité)
4. Commencer à accepter les check-ins d'agents sur les ports 1530 (gRPC) et 8443 (REST)

Les agents se reconnecteront automatiquement dans leur fenêtre de backoff (au plus 5 minutes après que le serveur devient joignable).

### Scénario 2 : restauration depuis une réplique Litestream

Vous avez une réplication continue configurée et la base de données est perdue, mais `/srv/vigo/` (configs, secrets, TLS) est intact.

```bash
# 1. Stop the server
systemctl stop vigosrv

# 2. Remove the corrupted database
rm /srv/vigo/db/vigo.db
rm -f /srv/vigo/db/vigo.db-wal
rm -f /srv/vigo/db/vigo.db-shm

# 3. Restore from Litestream replica
litestream restore -o /srv/vigo/db/vigo.db \
  s3://my-bucket/vigo/backup

# 4. Start the server
systemctl start vigosrv
```

### Scénario 3 : restauration de la base de données uniquement

La config et les secrets sont intacts, mais la base de données est perdue ou corrompue.

```bash
systemctl stop vigosrv
vigocli backup restore /path/to/backup.tar.gz --db-only

systemctl start vigosrv
```

### Scénario 4 : clé maîtresse perdue (secrets irrécupérables)

Si la clé maîtresse (`.master.key`) est perdue et que vous n'avez aucune sauvegarde l'incluant, les secrets chiffrés ne peuvent pas être déchiffrés. Vous devez les recréer :

```bash
# 1. Restore everything except secrets
vigocli backup restore /path/to/backup.tar.gz --db-only

# 2. A new master key will be auto-generated on next start.
#    Re-create all secrets:
vigocli secrets set raw vigo/db/dsn
vigocli secrets set raw vigo/smtp/password
# ... repeat for each secret referenced in your config

# 3. Publish and start
vigocli config publish
systemctl start vigosrv
```

### Scénario 5 : perte complète (aucune sauvegarde)

Si le serveur et toutes les sauvegardes sont perdus :

1. Installez Vigo à neuf, configurez `server.yaml` et les certificats TLS
2. Recréez les fichiers de config (`stacks/`) depuis git ou la documentation
3. Recréez les secrets
4. Démarrez le serveur — il initialisera une base de données vide
5. **Les agents doivent se ré-enrôler** : configurez les motifs d'enrôlement de confiance, puis soit :
   - Attendez que les agents réessaient (ils finiront par atteindre le serveur, échoueront à l'authentification, et auront besoin d'un bootstrap)
   - Exécutez `vigocli envoys rebootstrap --all` pour signaler à tous les agents de se mettre à jour
   - Ou bootstrappez manuellement chaque nœud : `curl -sSfk https://<server>:8443/bootstrap | sudo sh`

## Vérification après restauration

Après la restauration, vérifiez que le serveur est sain :

```bash
# Check the server is up (prints ok once it is ready)
curl -sk https://127.0.0.1:8443/healthz

# Run the full diagnostic
sudo vigocli doctor

# Verify enrolled envoys are visible and checking in again
# (each one reports at its next check-in cycle; watch LAST SEEN)
sudo vigocli envoys list

# Check the dashboard
open https://<server-ip>:8443/
```

Si des envoys apparaissent comme inactifs après la restauration, c'est attendu : chacun retrouve son état normal après son prochain check-in.


## Reprise Spanner

Dans une topologie spanner hub + bolts :

- **Panne d'un Bolt** : restaurez le Bolt indépendamment. Les envoys enrôlés sur ce Bolt se reconnectent automatiquement. Le hub reprendra la réception des données agrégées au prochain heartbeat du Bolt.
- **Panne du hub** : restaurez le hub. Les Bolts se reconnecteront et reprendront les heartbeats spanner. Les réaffectations d'envoys et les opérations de drain devraient être re-vérifiées après la restauration du hub.
- **Le hub et le Bolt ont des bases de données indépendantes** — restaurer l'un n'affecte pas l'autre.

## Bonnes pratiques

### Sauvegarder les fichiers de config dans Git

La source de vérité de la config (`stacks/`) est du YAML en clair sur le système de fichiers. Suivez-la dans git pour un historique versionné, une visibilité des diffs et une restauration facile :

```bash
cd /srv/vigo/stacks
git init && git add -A && git commit -m "initial config"

# After changes:
git add -A && git commit -m "add redis configcrate for cache nodes"
```

Cela vous donne `git log`, `git diff` et `git revert` pour les changements de config, indépendamment des sauvegardes de la base de données.

### Protéger la clé maîtresse

La clé maîtresse des secrets (`secrets/.master.key`) est le fichier le plus critique de tous. Sans elle, tous les secrets chiffrés sont irrécupérables.

- Incluez-la dans chaque sauvegarde (le `vigocli backup create` par défaut le fait)
- Envisagez de stocker une copie dans un emplacement sécurisé distinct (gestionnaire de mots de passe, module de sécurité matériel, ou un système de sauvegarde différent)
- Ne la committez jamais dans git

### Planifier des sauvegardes régulières

```bash
# Daily backup, 7-day retention
0 2 * * * /usr/local/bin/vigocli backup create \
  /backup/vigo-$(date +\%Y\%m\%d).tar.gz \
  && find /backup -name 'vigo-*.tar.gz' -mtime +7 -delete
```

Pour une protection continue, configurez Litestream en plus des snapshots quotidiens. Litestream offre un RPO quasi nul (< 1 seconde), tandis que les snapshots fournissent des points de reprise portables et autonomes.

### Tester vos restaurations

Vérifiez périodiquement que les sauvegardes sont restaurables :

```bash
# Dry-run to verify archive integrity
vigocli backup restore /backup/vigo-latest.tar.gz --dry-run

# Full test restore to a staging environment
vigocli backup restore /backup/vigo-latest.tar.gz \
  --target /tmp/vigo-test
```

## Attentes en matière de temps de reprise

| Scénario | Temps de reprise typique |
|----------|----------------------|
| Restauration de la base de données depuis Litestream | Minutes (téléchargement + démarrage serveur) |
| Restauration complète depuis une archive de sauvegarde | Minutes (extraction + publication + démarrage) |
| Reconnexion des agents après le retour du serveur | 0–5 minutes (fenêtre de backoff) |
| Vidage des résultats en attente | Secondes (résultats en file d'attente délivrés au premier check-in) |
| Réhydratation du FleetIndex | Secondes (même à plus de 10 000 envoys) |
| Perte complète, aucune sauvegarde | Heures (réinstaller, reconfigurer, ré-enrôler tous les agents) |

## Liens connexes

- [Sauvegarde & reprise](backup-and-recovery.md) — Configuration Litestream et commandes de snapshot manuel
- [Base de données](../../reference/server-yaml.md) — Configuration SQLite et Postgres
- [FleetIndex](troubleshoot.md) — Index en mémoire reconstruit au démarrage
- [Dépannage](troubleshoot.md) — Problèmes courants et diagnostics

## Et ensuite

- **Planifiez un exercice de reprise après sinistre trimestriel** → restaurez une archive de sauvegarde sur un hôte jetable. Vérifiez `/healthz`, puis faites passer un envoy par un check-in contre le serveur restauré. Considérez les sauvegardes non testées comme inopérantes.
- **Vous opérez un spanner hub-spoke** → restaurez chaque rôle indépendamment (le hub et le Bolt ont des bases de données séparées). Re-vérifiez les réaffectations et les drains après la restauration du hub.
- **Une étape de restauration a échoué** → [Dépanner les problèmes courants](troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
