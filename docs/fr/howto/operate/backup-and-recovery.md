<!-- vigo-i18n source_sha:44cd988f2b84f52c99945cd45b93b69d141d78c0b006c27cba95b2d862d24a16 -->

# Sauvegarde et restauration

À la fin de cette page, vous disposerez de trois chemins de restauration indépendants : **Litestream** répliquant en continu le WAL SQLite hors site (RPO quasi nul), la **configcrate `backups`** produisant des instantanés quotidiens de `/srv/vigo` dédupliqués par liens physiques, et la CLI `vigocli backup create / restore` pour des archives tarball point-in-time que vous pouvez expédier n'importe où. Les trois sont complémentaires — Litestream capte la base de données active, la configcrate couvre l'état du système de fichiers que Litestream ne couvre pas, la CLI couvre les instantanés ponctuels avant des changements risqués.

**Quand l'utiliser :** tout déploiement de production. Même une flotte à serveur unique devrait en avoir au moins un d'activé ; un véritable déploiement de production devrait avoir les trois.

**Quand l'éviter :** les serveurs de développement et de pré-production où l'ensemble de la pile est reproductible depuis git. Conservez `stacks/` sous contrôle de version dans tous les cas — la source de vérité de la configuration de Vigo est du YAML simple, donc git est la sauvegarde de première ligne la moins coûteuse dont vous disposez.

La sauvegarde n'est applicable que lorsque `database.driver` vaut `sqlite` (la valeur par défaut). Les déploiements PostgreSQL effectuent leur sauvegarde via leur propre mécanisme.

## Configuration

Ajoutez une section `backup:` à `server.yaml` :

```yaml
backup:
  url: s3://my-bucket/vigo/backup
  access_key_id: "secret:vigo/backup/aws_access_key"
  secret_access_key: "secret:vigo/backup/aws_secret_key"
  region: us-east-1
  retention: "720h"       # 30 days (default)
  sync_interval: "1s"     # WAL sync frequency (default)
```

## Backends pris en charge

| Backend | Format d'URL | Notes |
|---------|-----------|-------|
| Amazon S3 | `s3://bucket/path` | Définir `region`, et optionnellement `endpoint` pour les backends compatibles S3 |
| Google Cloud Storage | `gcs://bucket/path` | Utilise les Application Default Credentials ou des clés explicites |
| SFTP | `sftp://host/path` | Transfert de fichiers basé sur SSH |
| Fichier local | `file:///path/to/dir` | Utile pour les montages NFS ou les tests |

### Stockage compatible S3 (MinIO, Backblaze)

Pour les endpoints compatibles S3, définissez `endpoint` et `force_path_style` :

```yaml
backup:
  url: s3://vigo-backup/data
  endpoint: https://minio.internal:9000
  force_path_style: true
  access_key_id: "secret:vigo/backup/access_key"
  secret_access_key: "secret:vigo/backup/secret_key"
```

## Options de configuration

| Champ | Défaut | Description |
|-------|---------|-------------|
| `url` | (requis) | URL de destination de la réplique |
| `access_key_id` | | Clé d'accès S3/GCS (prend en charge le préfixe `secret:`) |
| `secret_access_key` | | Clé secrète S3/GCS (prend en charge le préfixe `secret:`) |
| `region` | défaut du fournisseur | Région AWS |
| `endpoint` | | Endpoint compatible S3 |
| `force_path_style` | false | Utiliser des URL en mode path-style (requis pour MinIO) |
| `retention` | `720h` (30 jours) | Durée de rétention des instantanés |
| `sync_interval` | `1s` | Fréquence de synchronisation du WAL |

## Fonctionnement

1. Au démarrage, si `backup.url` est défini, le serveur génère un fichier de configuration Litestream et lance `litestream replicate` comme sous-processus.
2. Litestream surveille en continu le WAL SQLite et réplique les changements vers le backend configuré.

3. Des instantanés périodiques sont créés et les anciens sont élagués selon le paramètre `retention`.
4. À l'arrêt du serveur, Litestream est arrêté proprement, garantissant que toutes les données WAL en attente sont vidées.

## Prérequis

Le binaire `litestream` doit être présent dans le PATH du serveur. Installez-le depuis https://litestream.io/install/.

## Procédure de restauration

Pour restaurer depuis une sauvegarde :

1. Arrêtez le serveur Vigo.

2. Supprimez ou renommez le fichier de base de données existant.

3. Exécutez la commande de restauration :
   ```bash
   litestream restore -config /path/to/litestream.yml -o /path/to/vigo.db /path/to/vigo.db
   ```

   Ou laissez le serveur Vigo s'en charger -- la méthode `Replicator.Restore()` effectue la même opération de manière programmatique.

4. Démarrez le serveur Vigo. Le FleetIndex se réhydratera depuis la base de données restaurée.

## Surveillance

Consultez la sortie du sous-processus Litestream dans les journaux du serveur. Litestream journalise l'état de la réplication, la création des instantanés et toute erreur sur stdout/stderr, que le serveur capture.

Si le binaire Litestream est introuvable dans le PATH, le serveur journalise un avertissement mais continue de fonctionner sans sauvegarde.

---

## Instantanés manuels

Pour des sauvegardes point-in-time avant des changements risqués, des migrations matérielles ou une reprise après sinistre, utilisez les commandes de sauvegarde de la CLI. Elles créent une unique archive tar.gz contenant la base de données, les configs, les secrets, les certificats TLS et `server.yaml`.

Aucune API serveur n'est nécessaire — ces commandes opèrent directement sur le système de fichiers local.

### Créer une sauvegarde

```bash
vigocli backup create /tmp/vigo-backup.tar.gz
```

La commande lit `server.yaml` pour trouver le chemin de la base de données, puis :

1. Crée un instantané de la base SQLite via l'API de sauvegarde sqlite3 (sûr avec des writers WAL concurrents)
2. Copie `stacks/` (source de vérité de la configuration)
3. Copie `secrets/` (déjà chiffrés en AES-256-GCM au repos)
4. Copie `tls/` (CA + certificats serveur)
5. Copie `server.yaml`
6. Écrit un `manifest.json` avec des métadonnées

Chaque sauvegarde génère automatiquement un fichier annexe de somme de contrôle SHA-256 (`.sha256`) pour la vérification d'intégrité.

**Drapeaux :**

| Drapeau | Défaut | Description |
|------|---------|-------------|
| `--server-yaml` | `/srv/vigo/server.yaml` | Chemin vers server.yaml |

| `--server-root` | `/srv/vigo` | Répertoire racine contenant stacks/, secrets/, tls/ |
| `--encrypt` | `false` | Chiffrer l'archive en AES-256-GCM (demande un mot de passe) |

### Sauvegardes chiffrées

Pour les environnements conformes HIPAA, chiffrez les sauvegardes au repos :

```bash
# Create an encrypted backup (prompts for password)
vigocli backup create /tmp/vigo-backup.tar.gz --encrypt

# Restore an encrypted backup (prompts for password)
vigocli backup restore /tmp/vigo-backup.tar.gz --decrypt
```

Le chiffrement utilise AES-256-GCM avec une clé dérivée par Argon2id. Le mot de passe n'est jamais stocké — conservez-le dans un endroit sûr (par exemple, un gestionnaire de mots de passe ou un HSM).

### Inspecter une sauvegarde

```bash
vigocli backup inspect /tmp/vigo-backup.tar.gz
```

Affiche le JSON du manifeste sans extraire l'archive complète.

### Restaurer depuis une sauvegarde

```bash
# Stop the server first
systemctl stop vigosrv

# Preview what will be restored
vigocli backup restore /tmp/vigo-backup.tar.gz --dry-run

# Restore everything
vigocli backup restore /tmp/vigo-backup.tar.gz

# Publish configs and restart
vigocli config publish
systemctl start vigosrv
```

**Drapeaux :**

| Drapeau | Défaut | Description |
|------|---------|-------------|
| `--target` | `/srv/vigo` | Répertoire racine dans lequel restaurer |
| `--db-only` | `false` | Restaurer uniquement la base de données, ignorer config/secrets/tls |
| `--dry-run` | `false` | Afficher ce qui serait restauré sans écrire |

### Disposition de l'archive

```
vigo-backup-2026-03-13T14:30:00Z.tar.gz
├── manifest.json
├── db/
│   └── vigo.db
├── stacks/
│   ├── configcrates/
│   ├── roles/
│   ├── envoys/
│   └── vars/
├── secrets/
│   └── (encrypted files, directory structure preserved)
├── tls/
│   └── (cert, key, ca, ca_key PEM files)
└── server.yaml
```

### Découverte et restauration

Les instantanés résident sur disque sous `paths.snapshots` (par défaut `/srv/vigo/snapshots`). Le listage, le téléchargement et la restauration sont tous des opérations CLI via `vigocli backup`. La restauration nécessite d'arrêter le serveur au préalable.

### Instantanés basés sur une configcrate

L'approche recommandée est la configcrate `vigo-server-backup`, qui crée des instantanés incrémentaux basés sur des liens physiques. Assignez-la au nœud exécutant le serveur Vigo :

```yaml
- match: "vigoserver.example.com"
  configcrates: [vigo-server-backup]
  vars:
    backup_retain: "90"
```

La configcrate utilise l'exécuteur `backup` de l'agent pour créer des répertoires d'instantanés datés avec déduplication par liens physiques. Les fichiers inchangés partagent leurs inodes avec l'instantané précédent, ne consommant aucun espace disque supplémentaire. Les instantanés s'exécutent chaque nuit (configurable) et apparaissent automatiquement dans l'interface web.

Voir `example-configs/stacks/configcrates/system/vigo-server-backup.vgo.example` pour la configcrate complète et toutes les vars configurables.

Pour des sauvegardes ponctuelles avant des changements risqués, `vigocli backup create` reste disponible. Voir [Reprise après sinistre](disaster-recovery.md) pour les procédures complètes de récupération en cas de défaillance totale du serveur.

## Sauvegarde intégrée du système de fichiers

Vigo fournit une configcrate `backups` qui utilise l'exécuteur natif `backup` pour créer des instantanés incrémentaux quotidiens de `/srv/vigo` basés sur des liens physiques. Les fichiers inchangés partagent leurs inodes avec l'instantané précédent, ne consommant aucun espace disque supplémentaire. Seuls les fichiers modifiés ou nouveaux sont copiés.

### Fonctionnement

Lorsque l'hôte du serveur est enrôlé comme Envoy et que la configcrate `backups` est assignée à son nom d'hôte dans vos mappages d'Envoys, l'agent sur l'hôte du serveur va :

1. Créer des répertoires d'instantanés datés sous `target_path` (format : `YYYY.JJJ.HH.MM`)
2. Lier par lien physique les fichiers inchangés depuis l'instantané précédent (déduplication zéro-copie)
3. Copier uniquement les fichiers nouveaux ou modifiés
4. Élaguer les instantanés plus anciens que `retain` jours

La vérification du calendrier est intégrée à l'exécuteur — si un instantané existe déjà pour le jour courant, la sauvegarde est ignorée.

### Mise en place

Ajoutez la configcrate `backups` au mappage d'Envoy de votre serveur :

```yaml
envoys:
  - match: "my-vigo-server"
    configcrates: [backups]
```

La configcrate `backups` sauvegarde `/srv/vigo` vers `/var/backups/vigo` quotidiennement avec une rétention de 14 jours. Les bases de données sont sauvegardées séparément via l'exécuteur `db_backup`, qui réalise un dump via `sqlite3 .dump` afin que la sortie soit un fichier SQL déterministe sur lequel les liens physiques fonctionnent efficacement :

```yaml
name: backups
resources:
  - name: srv-vigo-backup
    type: backup
    source_dir: /srv/vigo
    target_path: /var/backups/vigo
    retain: 14
    schedule: daily

  - name: vigo-db-backup
    type: db_backup
    db_type: sqlite
    source_db: /srv/vigo/db/vigo.db
    target_path: /var/backups/vigo/vigo-db
    retain: 14
    schedule: daily

  - name: sandgorgon-db-backup
    type: db_backup
    db_type: sqlite
    source_db: /srv/vigo/sandgorgon/sandgorgon.db
    target_path: /var/backups/vigo/vigo-sandgorgon-db
    retain: 14
    schedule: daily
```

**Pourquoi des ressources db_backup séparées ?** Les fichiers de base de données SQLite changent à chaque transaction, ce qui rend inopérante la déduplication par liens physiques de l'exécuteur `backup`. L'exécuteur `db_backup` réalise un dump vers un fichier SQL compressé qui ne change que lorsque les données changent, rendant les instantanés par liens physiques efficaces.

| Paramètre | Défaut | Description |
|-----------|---------|-------------|
| `source_dir` | — | Répertoire à sauvegarder (requis) |
| `target_path` | — | Destination de l'instantané (requis, doit être sur le même système de fichiers pour les liens physiques) |
| `retain` | `30` | Nombre de jours de conservation des instantanés avant élagage |
| `schedule` | `daily` | Fenêtre de planification : `daily`, `hourly` ou `weekly` |
| `exclude` | — | Motifs glob séparés par des virgules à ignorer |
| `cross_fs` | `fail` | Comportement inter-systèmes de fichiers : `fail`, `copy` ou `warn` |
| `min_free_space` | `1G` | Espace libre minimum requis avant l'exécution |
| `pre_command` | — | Commande shell à exécuter avant la sauvegarde (par exemple, vider les journaux) |
| `post_command` | — | Commande shell à exécuter après la sauvegarde (par exemple, redémarrer un service) |

### Sécurité

Les instantanés par liens physiques sont économes en espace mais partagent leurs inodes — supprimer un fichier d'un instantané ne libère pas d'espace disque tant que tous les liens physiques ne sont pas supprimés. Pour une résistance à l'altération, envisagez de monter le système de fichiers de sauvegarde en lecture seule entre les exécutions ou d'utiliser un volume distinct.

## Recommandations de sauvegarde supplémentaires

Litestream couvre la base de données SQLite avec une réplication quasi temps réel. La configcrate intégrée `backups` couvre l'ensemble du système de fichiers. Pour une protection maximale :

| Chemin | Contenu | Criticité |
|------|----------|-------------|
| `stacks/` | Configcrates, rôles, mappages d'Envoys, vars | Élevée — l'intégralité de votre politique |
| `secrets/` | Fichiers chiffrés en AES-256-GCM + `.master.key` | Critique — irrécupérable sans la clé maîtresse |
| `tls/` | Certificat/clé de CA, certificat/clé du serveur | Élevée — les agents s'authentifient contre ceux-ci |
| `server.yaml` | Configuration du serveur | Moyenne — peut être recréée |
| `db/vigo.db` | Base de données SQLite | Couverte par Litestream si configuré |

1. **Conservez `stacks/` dans git.** La source de vérité de la configuration est du YAML simple. Le contrôle de version vous donne l'historique, les diffs et une récupération instantanée indépendamment de la fraîcheur des sauvegardes.

2. **Utilisez la configcrate `backups`** pour des instantanés quotidiens par liens physiques vers `/var/backups/vigo/`. Utilisez des ressources `db_backup` pour les bases de données — les fichiers DB bruts changent à chaque transaction, rendant inopérante la déduplication par liens physiques. L'exécuteur `db_backup` réalise un dump vers du SQL compressé sur lequel les liens physiques fonctionnent efficacement.

3. **Configurez Litestream pour un RPO de base de données quasi nul.** Le rsync quotidien fournit un point de récupération, mais Litestream réplique les changements du WAL en quelques secondes.

4. **Protégez la clé maîtresse séparément.** `secrets/.master.key` est le fichier le plus critique de tous. Stockez-en une copie dans un gestionnaire de mots de passe, un HSM ou un autre emplacement sécurisé distinct. Sans elle, tous les secrets chiffrés sont irrécupérables.

5. **Testez les restaurations périodiquement.** Voir [Reprise après sinistre](disaster-recovery.md) pour le parcours de restauration complet.

## Et ensuite

- **Réalisez un exercice complet de reprise après sinistre** → [Reprise après sinistre](disaster-recovery.md). Testez avant d'en avoir besoin, selon un calendrier.
- **Expédiez la clé maîtresse dans un endroit sûr** → `secrets/.master.key` est le fichier le plus critique du déploiement ; la perte de la clé maîtresse est la perte de tous les secrets chiffrés. Gestionnaire de mots de passe / HSM / enveloppe scellée dans un coffre — selon ce qui convient à votre modèle d'exploitation.
- **Une restauration échoue ou l'archive ne s'ouvre pas** → [Résoudre les problèmes courants](troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [../legal/license.md](../../legal/license.md).*
