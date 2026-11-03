<!-- vigo-i18n source_sha:d2b5eaeed58c23ca59bf17ab63cdf56bd92a68d5081fe1c94898aa4929a512e7 -->

# Sauvegarder et restaurer

À la fin de cette page, vous aurez mis en place trois chemins de récupération indépendants : **Litestream** répliquant en continu le WAL SQLite hors hôte (RPO quasi nul), le **configcrate `backups`** produisant des instantanés quotidiens dédupliqués par liens physiques de `/srv/vigo`, et l'interface en ligne de commande `vigocli backup create / restore` pour des archives tar à un instant donné que vous pouvez expédier n'importe où. Les trois sont complémentaires : Litestream capture la base de données vivante, le configcrate couvre l'état du système de fichiers que Litestream ne couvre pas, l'interface en ligne de commande couvre les instantanés ponctuels avant des changements risqués.

**Quand l'utiliser :** tout déploiement de production. Même une flotte à serveur unique devrait avoir au moins l'un de ces mécanismes actif ; un véritable déploiement de production devrait avoir les trois.

**Quand l'ignorer :** les serveurs de développement et de pré-production où l'ensemble de la pile est reproductible à partir de git. Conservez `stacks/` sous gestion de versions quoi qu'il arrive — la source de vérité de la configuration de Vigo est du YAML brut, donc git est la sauvegarde de première ligne la moins coûteuse dont vous disposez.

La sauvegarde n'est applicable que lorsque `database.driver` vaut `sqlite` (la valeur par défaut). Les déploiements PostgreSQL se sauvegardent via leur propre mécanisme.

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
| Amazon S3 | `s3://bucket/path` | Définissez `region`, éventuellement `endpoint` pour les services compatibles S3 |
| Google Cloud Storage | `gcs://bucket/path` | Utilise les Application Default Credentials ou des clés explicites |
| SFTP | `sftp://host/path` | Transfert de fichiers basé sur SSH |
| Fichier local | `file:///path/to/dir` | Utile pour les montages NFS ou les tests |

### Stockage compatible S3 (MinIO, Backblaze)

Pour les points d'accès compatibles S3, définissez `endpoint` et `force_path_style` :

```yaml
backup:
  url: s3://vigo-backup/data
  endpoint: https://minio.internal:9000
  force_path_style: true
  access_key_id: "secret:vigo/backup/access_key"
  secret_access_key: "secret:vigo/backup/secret_key"
```

## Options de configuration

| Champ | Valeur par défaut | Description |
|-------|---------|-------------|
| `url` | (obligatoire) | URL de destination du réplica |
| `access_key_id` | | Clé d'accès S3/GCS (prend en charge le préfixe `secret:`) |
| `secret_access_key` | | Clé secrète S3/GCS (prend en charge le préfixe `secret:`) |
| `region` | valeur par défaut du fournisseur | Région AWS |
| `endpoint` | | Point d'accès compatible S3 |
| `force_path_style` | false | Utiliser des URL de style chemin (obligatoire pour MinIO) |
| `retention` | `720h` (30 jours) | Période de rétention des instantanés |
| `sync_interval` | `1s` | Fréquence de synchronisation du WAL |

## Fonctionnement

1. Au démarrage, si `backup.url` est défini, le serveur génère un fichier de configuration Litestream et lance `litestream replicate` en tant que sous-processus.
2. Litestream surveille en continu le WAL SQLite et réplique les changements vers le backend configuré.

3. Des instantanés périodiques sont créés et les anciens sont élagués selon le paramètre `retention`.
4. À l'arrêt du serveur, Litestream est stoppé proprement, garantissant que toutes les données WAL en attente sont vidées.

## Prérequis

Le binaire `litestream` doit être présent dans le PATH du serveur. Installez-le depuis https://litestream.io/install/.

## Procédure de restauration

Pour restaurer à partir d'une sauvegarde :

1. Arrêtez le serveur Vigo.

2. Supprimez ou renommez le fichier de base de données existant.

3. Exécutez la commande de restauration :
   ```bash
   litestream restore -config /path/to/litestream.yml -o /path/to/vigo.db /path/to/vigo.db
   ```

   Ou laissez le serveur Vigo s'en charger -- la méthode `Replicator.Restore()` effectue la même opération de manière programmatique.

4. Démarrez le serveur Vigo. Le FleetIndex se réhydratera à partir de la base de données restaurée.

## Surveillance

Vérifiez la sortie du sous-processus Litestream dans les journaux du serveur. Litestream consigne l'état de la réplication, la création d'instantanés et toute erreur sur stdout/stderr, que le serveur capture.

Si le binaire Litestream est introuvable dans le PATH, le serveur consigne un avertissement mais continue de fonctionner sans sauvegarde.

---

## Instantanés manuels

Pour des sauvegardes à un instant donné avant des changements risqués, des migrations matérielles ou une reprise après sinistre, utilisez les commandes de sauvegarde de l'interface en ligne de commande. Elles créent une unique archive tar.gz contenant la base de données, les configurations, les secrets, les certificats TLS et server.yaml.

Aucune API serveur n'est nécessaire — ces commandes opèrent directement sur le système de fichiers local.

### Créer une sauvegarde

```bash
vigocli backup create /tmp/vigo-backup.tar.gz
```

La commande lit `server.yaml` pour trouver le chemin de la base de données, puis :

1. Crée un instantané de la base de données SQLite à l'aide de l'API de sauvegarde de sqlite3 (sûr avec des écrivains WAL concurrents)
2. Copie `stacks/` (source de vérité de la configuration)
3. Copie `secrets/` (déjà chiffré au repos en AES-256-GCM)
4. Copie `tls/` (CA + certificats serveur)
5. Copie `server.yaml`
6. Écrit un `manifest.json` contenant les métadonnées

Chaque sauvegarde génère automatiquement un fichier annexe de somme de contrôle SHA-256 (`.sha256`) pour la vérification d'intégrité.

**Options :**

| Option | Valeur par défaut | Description |
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

Le chiffrement utilise AES-256-GCM avec une clé dérivée par Argon2id. Le mot de passe n'est jamais stocké — conservez-le dans un emplacement sécurisé (par exemple, un gestionnaire de mots de passe ou un HSM).

### Inspecter une sauvegarde

```bash
vigocli backup inspect /tmp/vigo-backup.tar.gz
```

Affiche le manifeste JSON sans extraire l'archive complète.

### Restaurer à partir d'une sauvegarde

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

**Options :**

| Option | Valeur par défaut | Description |
|------|---------|-------------|
| `--target` | `/srv/vigo` | Répertoire racine où restaurer |
| `--db-only` | `false` | Restaurer uniquement la base de données, ignorer config/secrets/tls |
| `--dry-run` | `false` | Montrer ce qui serait restauré sans écrire |

### Structure de l'archive

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

Les instantanés résident sur le disque sous `paths.snapshots` (par défaut `/srv/vigo/snapshots`). Le listage, le téléchargement et la restauration sont tous des opérations de l'interface en ligne de commande via `vigocli backup`. La restauration nécessite d'arrêter le serveur au préalable.

### Instantanés basés sur un configcrate

L'approche recommandée est le configcrate `vigo-server-backup`, qui crée des instantanés incrémentaux basés sur des liens physiques. Assignez-le au nœud exécutant le serveur Vigo :

```yaml
- match: "vigoserver.example.com"
  configcrates: [vigo-server-backup]
  vars:
    backup_retain: "90"
```

Le configcrate utilise l'exécuteur `backup` de l'agent pour créer des répertoires d'instantanés datés avec déduplication par liens physiques. Les fichiers inchangés partagent des inodes avec l'instantané précédent, ne consommant aucun espace disque supplémentaire. Les instantanés s'exécutent chaque nuit (configurable) et apparaissent automatiquement dans l'interface web.

Voir `example-configs/stacks/configcrates/system/vigo-server-backup.vgo.example` pour le configcrate complet et toutes les variables configurables.

Pour des sauvegardes ponctuelles avant des changements risqués, `vigocli backup create` reste disponible. Voir [Reprise après sinistre](disaster-recovery.md) pour les procédures complètes de récupération après défaillance du serveur.

## Sauvegarde intégrée du système de fichiers

Vigo fournit un configcrate `backups` qui utilise l'exécuteur natif `backup` pour créer des instantanés incrémentaux quotidiens basés sur des liens physiques de `/srv/vigo`. Les fichiers inchangés partagent des inodes avec l'instantané précédent, ne consommant aucun espace disque supplémentaire. Seuls les fichiers modifiés ou nouveaux sont copiés.

### Fonctionnement

Lorsque l'hôte du serveur est enrôlé comme Envoy et que le configcrate `backups` est assigné à son nom d'hôte dans vos mappages d'Envoy, l'agent sur l'hôte du serveur :

1. Crée des répertoires d'instantanés datés sous `target_path` (format : `YYYY.JJJ.HH.MM`)
2. Crée des liens physiques vers les fichiers inchangés de l'instantané précédent (déduplication sans copie)
3. Copie uniquement les fichiers nouveaux ou modifiés
4. Élague les instantanés plus anciens que `retain` jours

La vérification de la planification est intégrée à l'exécuteur — si un instantané existe déjà pour le jour courant, la sauvegarde est ignorée.

### Mise en place

Ajoutez le configcrate `backups` au mappage d'Envoy de votre serveur :

```yaml
envoys:
  - match: "my-vigo-server"
    configcrates: [backups]
```

Le configcrate `backups` sauvegarde `/srv/vigo` vers `/var/backups/vigo` quotidiennement avec une rétention de 14 jours. Les bases de données sont sauvegardées séparément à l'aide de l'exécuteur `db_backup`, qui exporte via `sqlite3 .dump` de sorte que la sortie est un fichier SQL déterministe que les liens physiques dédupliquent efficacement :

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

**Pourquoi des ressources db_backup distinctes ?** Les fichiers de base de données SQLite changent à chaque transaction, mettant en échec la déduplication par liens physiques dans l'exécuteur `backup`. L'exécuteur `db_backup` exporte vers un fichier SQL compressé qui ne change que lorsque les données changent, rendant les instantanés par liens physiques efficaces.

| Paramètre | Valeur par défaut | Description |
|-----------|---------|-------------|
| `source_dir` | — | Répertoire à sauvegarder (obligatoire) |
| `target_path` | — | Destination de l'instantané (obligatoire, doit être sur le même système de fichiers pour les liens physiques) |
| `retain` | `30` | Jours de conservation des instantanés avant élagage |
| `schedule` | `daily` | Fenêtre de planification : `daily`, `hourly` ou `weekly` |
| `exclude` | — | Motifs glob séparés par des virgules à ignorer |
| `cross_fs` | `fail` | Comportement inter-systèmes de fichiers : `fail`, `copy` ou `warn` |
| `min_free_space` | `1G` | Espace libre minimum requis avant l'exécution |
| `pre_command` | — | Commande shell à exécuter avant la sauvegarde (par exemple, vider les journaux) |
| `post_command` | — | Commande shell à exécuter après la sauvegarde (par exemple, redémarrer un service) |

### Sécurité

Les instantanés par liens physiques sont économes en espace mais partagent des inodes — supprimer un fichier d'un instantané ne libère pas d'espace disque tant que tous les liens physiques ne sont pas supprimés. Pour résister à l'altération, envisagez de monter le système de fichiers de sauvegarde en lecture seule entre les exécutions ou d'utiliser un volume distinct.

## Recommandations de sauvegarde supplémentaires

Litestream couvre la base de données SQLite avec une réplication quasi en temps réel. Le configcrate intégré `backups` couvre l'ensemble du système de fichiers. Pour une protection maximale :

| Chemin | Contenu | Criticité |
|------|----------|-------------|
| `stacks/` | Configcrates, rôles, mappages d'Envoy, variables | Élevée — l'intégralité de votre politique |
| `secrets/` | Fichiers chiffrés en AES-256-GCM + `.master.key` | Critique — irrécupérable sans la clé maîtresse |
| `tls/` | Cert/clé CA, cert/clé serveur | Élevée — les agents s'authentifient contre eux |
| `server.yaml` | Configuration du serveur | Moyenne — peut être recréée |
| `db/vigo.db` | Base de données SQLite | Couverte par Litestream si configuré |

1. **Conservez `stacks/` dans git.** La source de vérité de la configuration est du YAML brut. La gestion de versions vous offre l'historique, les diffs et une récupération instantanée indépendamment de la fraîcheur de la sauvegarde.

2. **Utilisez le configcrate `backups`** pour des instantanés quotidiens par liens physiques vers `/var/backups/vigo/`. Utilisez des ressources `db_backup` pour les bases de données — les fichiers DB bruts changent à chaque transaction, mettant en échec la déduplication par liens physiques. L'exécuteur `db_backup` exporte vers du SQL compressé que les liens physiques dédupliquent efficacement.

3. **Configurez Litestream pour un RPO de base de données quasi nul.** Le rsync quotidien fournit un point de récupération mais Litestream réplique les changements du WAL en quelques secondes.

4. **Protégez la clé maîtresse séparément.** `secrets/.master.key` est le fichier le plus critique de tous. Conservez-en une copie dans un gestionnaire de mots de passe, un HSM ou un autre emplacement sécurisé. Sans elle, tous les secrets chiffrés sont irrécupérables.

5. **Testez les restaurations périodiquement.** Voir [Reprise après sinistre](disaster-recovery.md) pour la procédure de restauration complète.

## Et ensuite

- **Réalisez un exercice complet de reprise après sinistre** → [Reprise après sinistre](disaster-recovery.md). Testez avant d'en avoir besoin, selon un calendrier.
- **Expédiez la clé maîtresse dans un endroit sûr** → `secrets/.master.key` est le fichier le plus critique du déploiement ; perdre la clé maîtresse, c'est perdre tous les secrets chiffrés. Gestionnaire de mots de passe / HSM / enveloppe scellée dans un coffre — selon ce qui convient à votre modèle d'exploitation.
- **Une restauration échoue ou l'archive ne s'ouvre pas** → [Résoudre les problèmes courants](troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
