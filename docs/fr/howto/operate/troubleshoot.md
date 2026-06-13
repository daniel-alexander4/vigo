<!-- vigo-i18n source_sha:f999757d7bc5dec3e4122d83cb90e10e21fd96ddd80ea9d76e37c37e31a674cb -->

# Résoudre les problèmes courants

Cette page est conçue pour la recherche par grep : lorsqu'un Envoy lève une erreur ou qu'une session échoue, recherchez dans cette page la chaîne exacte que voit l'opérateur. Chaque symptôme renvoie à son correctif. La référence des outils de diagnostic se trouve en bas de page, pour les cas où aucun des symptômes connus ne correspond.

## Symptômes (sous forme grep-able pour l'opérateur)

### `SSH pubkey not authorized on this envoy`

La session Scrier-SSH échoue immédiatement avec cette erreur côté serveur. La clé publique de l'opérateur (depuis `users.ssh_public_key` pour les sessions navigateur, ou détectée automatiquement par la CLI pour `vigocli scrier ssh`) ne figure dans le fichier `~/.ssh/authorized_keys` d'aucun utilisateur régulier sur l'Envoy cible.

**Correctif :** ajoutez la clé publique de l'opérateur à un utilisateur régulier sur l'Envoy. Chemin habituel : déclarez l'utilisateur dans un usercrate avec `authorized_keys:` et publiez. Puis réessayez. Voir [Configurer Scrier](set-up-scrier.md#make-sure-the-operators-pubkey-is-in-some-regular-users-authorized_keys-on-each-envoy).

### `no SSH pubkey available — either run vigocli scrier ssh...`

L'utilisateur web n'a aucune `ssh_public_key` définie ET la requête de connexion n'en portait aucune (c'est-à-dire qu'elle provenait du navigateur, et non de la CLI).

**Correctif :** exécutez une fois `vigocli scrier ssh <envoy>` depuis un hôte où réside votre clé SSH — la CLI la détecte automatiquement et écrit votre clé publique dans `users.ssh_public_key`. Les sessions navigateur fonctionnent ensuite. Ou bien utilisez `PUT /api/v1/users/{id}` avec `ssh_public_key` directement.

### `agent <uuid> did not open TunnelStream within 90s`

La session Scrier expire en attendant que l'agent de l'Envoy rappelle. L'agent a reçu le signal de réveil (ou aurait dû le recevoir) mais n'a pas ouvert le TunnelStream.

**Correctif :** vérifiez l'état de l'agent de l'Envoy — `vigocli envoys list <uuid>` pour la péremption ; `vigocli traits livequery <uuid> host_self.fs_root_pct` pour la pression disque (un agent affamé peut échouer à ouvrir de nouvelles sockets) ; inspectez l'hôte de l'agent avec `vigo status` sur la machine. Récupération après pression disque : videz journald (`journalctl --vacuum-size=200M`) et le cache apt (`apt clean`) ; redémarrez l'agent.

### `scrier: verify pubkey not found in any regular user's authorized_keys`

Erreur côté agent issue du scan Scrier. La clé publique de l'opérateur ne figure dans `/home/*/.ssh/authorized_keys` pour aucun utilisateur régulier (uid != 0).

**Correctif :** identique au premier symptôme ci-dessus. root n'est **jamais** une cible d'atterrissage Scrier ; si la clé de l'opérateur réside uniquement dans `/root/.ssh/authorized_keys`, le scan ne la trouvera pas. Ajoutez-la à un utilisateur régulier.

### L'Envoy apparaît comme périmé alors que l'agent tourne

Le prédicat de fraîcheur a marqué l'Envoy comme hors ligne alors que l'agent tourne. Probablement : l'intervalle de check-in est configuré trop bas et le cycle de convergence prend plus de temps que l'intervalle, si bien que les check-ins s'accumulent.

**Correctif :** vérifiez `vigo status` sur l'Envoy pour la cadence inter-CheckIn observée. Si elle est nettement supérieure au `checkin.interval` configuré, la passe de convergence est sous goulot d'étranglement. Augmentez l'intervalle, ou définissez l'override `checkin.offline_threshold` dans `server.yaml` (voir [reference/server-yaml.md](../../reference/server-yaml.md)).

### `server does not recognize this envoy (unknown id) — re-bootstrap required`

L'identité enrôlée de l'agent (son UUID client) n'a aucune ligne sur le serveur, donc chaque check-in est rejeté avec un gRPC `NotFound` et l'agent se met en attente (5 min) tout en continuant d'appliquer hors ligne sa dernière politique en cache. Cause : la base du serveur a été réinitialisée/reconstruite, ou l'Envoy a été supprimé (`vigocli envoys delete`) — pendant que l'agent continuait de tourner avec son ancien UUID. (À distinguer d'un Envoy *révoqué*, qui est rejeté avec `PermissionDenied "envoy is revoked"` et doit le rester.) Le serveur journalise l'orphelin une fois toutes les 10 min sous `WARN check-in from unknown envoy`, et `vigo_sig_verify_failed_total{reason="envoy_not_found"}` grimpe.

**Correctif :** re-bootstrappez l'agent sur cet hôte pour le ré-enrôler avec une identité fraîche : `curl -sSfk https://<server>:8443/bootstrap | sudo sh`. (L'agent ne se ré-enrôle **pas** lui-même — le ré-enrôlement reste un acte délibéré et contrôlé.) Si l'hôte est mis hors service, arrêtez plutôt son agent.

### `vigocli config publish` échoue avec des erreurs de validation

Le config publié n'a pas franchi l'une des barrières de validation (syntaxe YAML, intégrité des références croisées, détection de dépendances circulaires, idempotence, garde-fous, vérification d'accessibilité des URL).

**Correctif :** l'assistant IA intégré diffuse des suggestions de correction vers votre terminal en cas d'échec de validation. Lisez-les. Pour les barrières spécifiques, voir [Publier votre config](../configcrates/publish-config.md). Pour ignorer les vérifications d'accessibilité des URL dans les environnements isolés, utilisez `--skip-url-checks`.

### vigosrv s'arrête au démarrage avec `config load failed`

L'arbre de config publié dans `/srv/vigo/.live/` comporte une erreur structurelle (typiquement des noms en double, une syntaxe YAML, des références pendantes, ou une violation de disposition). vigosrv refuse de démarrer plutôt que de tourner sans politique — un serveur d'application de config sans config laisse silencieusement la flotte bloquée sur son dernier état en cache, ce qui est pire qu'un crash bruyant.

**Correctif :** réparez la source sous `/srv/vigo/stacks/`, puis lancez `vigocli config publish` (les opérations de fichiers s'exécutent même lorsque le serveur est arrêté), puis redémarrez vigosrv. Le message d'erreur de démarrage nomme la règle en échec.

### Boucle de convergence de configcrate — la même ressource change à chaque cycle

Une ressource déclare un état que quelque processus externe ne cesse d'annuler (un gestionnaire de paquets restaure automatiquement la version, un autre outil réécrit le fichier, l'OS met à jour une valeur).

**Correctif :** `vigocli config trace <envoy> <configcrate>` montre la chaîne de résolution. Inspectez ce qui écrit le fichier en dehors de Vigo. Le compteur de dérive sur la page de détail de l'Envoy distingue `Diverged` (≥5 exécutions consécutives avec changements — c'est ce cas-ci) de `Changed` (ponctuel).

### Un rollback de convergence s'est déclenché en pleine publication

`server/publish/` annule automatiquement une publication dont la convergence passe sous le seuil.

**Correctif :** le dernier `.live/` valide est restauré automatiquement ; l'opérateur voit le rollback dans `vigocli config history` et dans le journal d'audit des publications. Inspectez les exécutions qui ont déclenché le rollback pour trouver ce qui a changé ; corrigez et republiez.

### Refus d'enrôlement lié à la licence

L'enregistrement de l'agent a été refusé par le serveur parce que le nombre d'Envoys enrôlés dépasserait le plafond de la licence.

**Correctif :** consultez `vigocli license` pour l'usage / plafond actuels. Révoquez les Envoys inutilisés (`vigocli envoys revoke <uuid>`) avant d'en ajouter de nouveaux, ou montez de palier de licence.

### `kex_exchange_identification: read: Connection reset by peer` sur le sshd d'un Envoy (pas Scrier)

Le SSH direct vers l'hôte Envoy (pas via Scrier) est rejeté. Généralement dû à une pression disque ou à une privation de ressources sur l'hôte.

**Correctif :** vérifiez le disque + la charge. Les sous-systèmes Swarm de Vigo refusent les pushs en dessous de 10 % d'espace disque libre — ce qui corrèle souvent avec un dysfonctionnement de sshd sur le même hôte. Videz journald + le cache apt ; redémarrez sshd si nécessaire.

## Outils de diagnostic

### vigo status (côté agent)

À exécuter sur n'importe quel Envoy pour inspecter l'état local de l'agent sans contacter le serveur :

```bash
sudo vigo status
```

Cela affiche l'UUID de l'Envoy, l'adresse du serveur, les détails de la politique en cache (version, configcrates, ressources), l'âge des traits en cache, la profondeur de la file de résultats en attente, et l'état de la clé publique du serveur. Il lit uniquement depuis le fichier de config et le magasin d'état LMDB -- aucun réseau requis.

Utile pour vérifier :
- Si l'agent est enregistré (`Envoy ID` vs `(not registered)`)
- Quelle version de politique l'agent exécute (`Policy > Version`)
- Si des résultats hors ligne sont en file d'attente (compteur `Pending results`)
- Si l'agent dispose de la clé de signature de bundle du serveur (`Server pubkey`)

Voir [Agent CLI](../../reference/vigocli/agent.md#status) pour des exemples de sortie complets.

### vigocli doctor

La commande `doctor` vérifie tous les sous-systèmes du serveur et rapporte leur santé :

```bash
vigocli doctor
```

Elle exécute 8 vérifications : base de données, certificats TLS, fournisseur de secrets, licence, Spanner, SMTP, config, et Envoys périmés. Chaque vérification rapporte `pass`, `warn`, `fail`, ou `skip`.

### vigocli runs

Listez les exécutions de convergence récentes pour repérer les échecs :

```bash
vigocli runs list
vigocli runs list --envoy web-01.example.com
vigocli runs show <run-id>
```

Affichez ce qui dérive à travers la flotte en une seule commande :

```bash
vigocli runs drift
vigocli runs drift --envoy web-01.example.com
```

### Métriques Prometheus

Surveillez le point de terminaison `/metrics` pour :

- `vigo_checkins_total{status="error"}` -- échecs de check-in
- `vigo_sig_verify_failed_total` -- échecs de vérification de signature
- `vigo_config_reload_errors_total` -- problèmes de rechargement de config
- `vigo_checkin_age_seconds` -- détection d'Envoy périmé


Voir [Métriques](set-up-monitoring.md) pour la liste complète.

---

## Problèmes courants

### Le bootstrap échoue avec « Connection refused »

**Symptômes :** `curl: (7) Failed to connect to <server> port 8443: Connection refused` immédiatement après le démarrage du serveur.

**Cause :** Le serveur met 30 à 60 secondes à démarrer — il compile les certificats TLS au premier lancement, exécute les migrations de base de données, charge les configs, et réhydrate le FleetIndex. Le point de terminaison de bootstrap n'est pas disponible tant que le serveur n'a pas journalisé `server ready`.

**Correctif :** Attendez que le serveur termine son démarrage :

```bash
# Watch for "server ready" in the logs
docker compose logs -f vigo | grep -m1 "server ready"

# Then bootstrap
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

### L'agent ne se connecte pas

**Symptômes :** Les journaux de l'agent montrent des échecs de handshake TLS ou une connexion refusée.

**Diagnostic :**

1. Vérifiez que le serveur écoute sur le port 1530 :
   ```bash
   ss -tlnp | grep 1530
   ```

2. Vérifiez que l'agent dispose du bon certificat CA :
   ```bash
   openssl s_client -connect server:1530 -CAfile /etc/vigo/ca.pem
   ```

3. Vérifiez que le certificat client de l'agent a été signé par le même CA :
   ```bash
   openssl verify -CAfile /etc/vigo/ca.pem /etc/vigo/client.crt
   ```

4. Inspectez le journal de l'agent pour des erreurs spécifiques :
   ```bash
   journalctl -u vigo-envoy -f
   ```

**Causes courantes :**
- Mésappariement du certificat CA entre serveur et agent
- Certificats TLS expirés (vérifiez avec `vigocli doctor`)
- Pare-feu bloquant le port 1530

### Échecs de check-in

**Symptômes :** L'agent fait son check-in mais reçoit des erreurs. Les journaux du serveur montrent des échecs de vérification de signature.

**Diagnostic :**


1. Vérifiez le journal du serveur pour des erreurs de signature :
   ```
   vigo_sig_verify_failed_total
   ```

2. Vérifiez la synchronisation d'horloge entre l'agent et le serveur. La fenêtre de signature est de 5 minutes par défaut (`tuning.signature_window`). Si les horloges sont décalées au-delà de cette valeur, les signatures seront rejetées.

3. Vérifiez si l'Envoy est révoqué :
   ```bash
   vigocli nodes list
   ```

4. Vérifiez que la paire de clés de l'agent correspond à la clé publique stockée sur le serveur. L'agent génère une paire de clés ED25519 à l'enrôlement, et la clé publique est stockée dans la table `envoys`.

**Causes courantes :**
- Décalage d'horloge dépassant la fenêtre de signature
- Agent ré-enrôlé avec une nouvelle paire de clés mais un ancien UUID
- L'Envoy a été révoqué

### Le serveur a démarré avec zéro configcrate

**Symptômes :** Le serveur tourne mais les Envoys reçoivent une politique vide (aucune ressource appliquée). Les journaux montrent `config loaded with validation errors — starting with empty config`.

**Cause :** Erreurs de validation d'idempotence dans les configcrates de `.live/`. Le serveur les traite comme non fatales — il démarre avec une config vide afin que l'API REST, l'UI et gRPC restent disponibles pour le diagnostic.

**Correctif :**
1. Vérifiez les détails de l'erreur : `vigocli doctor` ou consultez les journaux du serveur
2. Corrigez le configcrate dans `stacks/` (le linter de configcrate détectera et réparera la plupart des problèmes)
3. Republiez : `vigocli config publish`

### Erreurs de rechargement de config

**Symptômes :** Les changements de config ne sont pas pris en compte. `vigo_config_reload_errors_total` augmente.

**Diagnostic :**

1. Vérifiez les journaux du serveur pour des erreurs de rechargement :
   ```
   grep "config reload" /var/log/vigo-server.log
   ```

2. Validez les fichiers de config manuellement :
   ```bash
   vigocli config publish --dry-run
   ```

3. Erreurs YAML courantes :
   - Clés en double dans le même mapping
   - Expressions `when:` invalides
   - Références `depends_on` circulaires
   - Définitions de configcrate manquantes référencées par des rôles

**Causes courantes :**
- Erreurs de syntaxe YAML
- Configcrate référencé dans un rôle mais non défini dans `configcrates/`
- Dépendance circulaire dans `depends_on`

### L'Envoy se déclare conforme mais les ressources ne sont pas appliquées

**Symptômes :** Un Envoy s'affiche `compliant` avec `CHANGED 0, FAILED 0`, mais les changements attendus (fichiers, utilisateurs, sudoers, etc.) ne sont pas présents sur la machine.


**Cause :** Le binaire de l'agent est trop ancien pour comprendre les paramètres de ressource plus récents. L'agent voit la ressource, vérifie les champs qu'il connaît (par ex. l'utilisateur existe, le shell est correct), rapporte « déjà conforme », et ignore silencieusement les paramètres qu'il ne reconnaît pas (par ex. `sudo_nopasswd`, `password`, `authorized_keys`).

**Diagnostic :**

1. Vérifiez la version de l'agent :
   ```bash
   vigocli envoys show <hostname>
   ```
   Regardez la ligne `Agent:`. Comparez avec la version que le serveur distribue.

2. Vérifiez que la ressource figure dans la config résolue :
   ```bash
   vigocli config trace <hostname>
   ```
   Si le configcrate est listé mais que les ressources ne sont pas appliquées, c'est un mésappariement de version d'agent.

3. Vérifiez le résultat d'exécution pour l'Envoy spécifique :
   ```bash
   vigocli runs list --envoy <envoy-uuid> --limit 1
   ```

**Correctif :** Mettez à jour le binaire de l'agent. Si vous pouvez pousser à travers l'agent existant :

```bash
# Re-bootstrap via push — downloads and installs the latest agent binary
vigocli task run "curl -sSfk https://<server>:8443/bootstrap | sh" --target "<hostname>"
```

Si vous pouvez vous connecter en SSH avec une clé (aucun sudo nécessaire pour la connexion) :

```bash
make build-agent
scp bin/vigo user@hostname:/tmp/vigo
ssh user@hostname "sudo mv /tmp/vigo /usr/local/sbin/vigo && sudo systemctl restart vigo-envoy"
```

Si vous êtes totalement bloqué dehors (pas de sudo, pas de push possible), vous aurez besoin d'un accès console ou physique à la machine.

### Des Envoys apparaissent comme périmés

**Symptômes :** Des Envoys apparaissent comme « périmés » dans l'UI ou la CLI alors qu'ils sont en ligne.

**Diagnostic :**

1. Vérifiez le seuil de péremption. Un Envoy est périmé s'il n'a pas fait de check-in dans les `2.5 × checkin.interval` :
   ```yaml
   checkin:
     interval: "5m"   # stale after 12 minutes 30 seconds
   ```

2. Sur l'Envoy, vérifiez que l'agent tourne :
   ```bash
   systemctl status vigo-envoy
   ```

3. Vérifiez les journaux de l'agent pour des erreurs :
   ```bash
   journalctl -u vigo-envoy --since "10 minutes ago"
   ```


4. Vérifiez l'état local de l'agent :
   ```bash
   sudo vigo status
   ```
   Regardez `Pending results` (en file mais non livrés) et `Policy > Version` (cache périmé).

5. Vérifiez la connectivité réseau :
   ```bash
   curl -k https://server:1530   # should get a TLS error, not a connection error
   ```

**Causes courantes :**
- Le processus de l'agent a planté ou a été arrêté
- Partition réseau entre l'agent et le serveur
- Échecs de résolution DNS sur l'agent

### Utilisation mémoire élevée

**Symptômes :** La mémoire du serveur croît avec le temps.

**Diagnostic :**

1. Vérifiez la taille du FleetIndex -- il conserve tout l'état des Envoys en mémoire. Chaque Envoy avec ses traits consomme environ 2-5 Ko :
   ```
   vigo_nodes_total * ~4KB = expected memory for FleetIndex
   ```

2. Vérifiez le tampon circulaire des exécutions récentes -- il conserve les 1000 dernières exécutions en mémoire.

3. Si vous utilisez `run_store: memory`, chaque Envoy conserve jusqu'à `run_store_capacity` exécutions en mémoire.

4. Surveillez `vigo_database_size_bytes` pour la croissance de la base de données.

**Atténuation :**
- Réduisez `database.retention` (défaut `30d`) pour rétrécir la fenêtre conservée
- Utilisez `run_store: database` plutôt que `memory` pour les grandes flottes

### Échecs de résolution de secret

**Symptômes :** Le serveur ne démarre pas ou la config affiche des valeurs `secret:` non résolues.

**Diagnostic :**

1. Vérifiez quel backend de secrets est configuré :
   ```yaml
   secrets:
     backend: local
   ```

2. Pour le backend `local`, vérifiez que les fichiers de secrets existent :
   ```bash
   ls -la /srv/vigo/secrets/
   ```

3. Les secrets utilisent le format de chemin `secret:path/to/secret`, qui correspond à un fichier dans le répertoire des secrets.

**Causes courantes :**
- Le fichier de secret n'existe pas au chemin attendu
- Les permissions du fichier empêchent le processus serveur de lire le secret

- Le backend de secrets est mal configuré

### Erreur de certificat de l'agent : « not valid for name »

**Symptômes :** L'agent se connecte mais obtient `certificate not valid for name "192.168.x.x"`.

**Cause :** Le certificat TLS serveur auto-généré n'inclut pas l'IP LAN de votre hôte. Les conteneurs Docker reçoivent leur propre nom d'hôte et IP, qui sont les valeurs par défaut du certificat.

**Correctif :** Ajoutez l'IP ou le nom DNS de votre serveur à `tls_sans` dans `server.yaml` :

```yaml
server:
  tls_sans:
    - "192.168.1.2"              # your server's LAN IP
    - "vigo.example.com"       # or DNS name agents use
```

Puis régénérez les certificats et redémarrez :

```bash
cd /srv/vigo
rm -f tls/cert tls/key
docker compose restart
```

Re-bootstrappez les Envoys pour qu'ils récupèrent le nouveau CA :

```bash
sudo systemctl stop vigo-envoy
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

### Le bootstrap échoue avec « Failure writing output »

**Symptômes :** `curl: (23) Failure writing output to destination` pendant le bootstrap.

**Cause :** L'ancien binaire de l'agent à `/usr/local/sbin/vigo` est verrouillé par un processus en cours. curl ne peut pas écraser un binaire en cours d'exécution.

**Correctif :**

```bash
# Stop the agent first
sudo systemctl stop vigo-envoy

# Then re-run bootstrap
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

### Réinitialiser un Envoy

Pour réinitialiser complètement un Envoy et re-bootstrapper de zéro :

```bash
# 1. Stop and remove the service
sudo systemctl stop vigo-envoy
sudo systemctl disable vigo-envoy
sudo rm -f /etc/systemd/system/vigo-envoy.service
sudo systemctl daemon-reload

# 2. Remove agent data and binary
sudo rm -rf /etc/vigo-envoy
sudo rm -rf /var/lib/vigo
sudo rm -f /usr/local/sbin/vigo

# 3. Re-bootstrap
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Ou utilisez le script de réinitialisation :

```bash
sudo scripts/reset-envoy.sh
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

L'Envoy se ré-enrôlera avec un nouvel UUID et une nouvelle paire de clés. L'ancienne entrée d'Envoy sur le serveur devient orpheline — révoquez-la avec :

```bash
vigocli envoys revoke <old-uuid>
```

### Réinitialiser le serveur

Pour effacer tout l'état du serveur et repartir à neuf (la licence est préservée) :

```bash
cd /srv/vigo
docker compose down
sudo find /srv/vigo -mindepth 1 -path /srv/vigo/license -prune -o -print0 | xargs -0 rm -rf
docker run --rm -v /srv/vigo:/srv/vigo us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest --seed-only
docker compose up -d
```

Cela re-sème `server.yaml`, `.env`, `docker-compose.yml`, les certificats TLS, et les exemples. Votre fichier de licence est préservé.

---

## Messages de journal à surveiller

| Message de journal | Signification |
|------------|---------|
| `config reload failed` | Erreur d'analyse ou de validation YAML |
| `signature verification failed` | L'agent a envoyé une signature ED25519 invalide |
| `envoy revoked` | Un check-in a été rejeté parce que l'Envoy est révoqué |
| `state flusher: batch last_seen failed` | Erreur d'écriture SQLite pendant le flush du FleetIndex |
| `webhook: delivery failed after retries` | Toutes les tentatives de relivraison d'un webhook épuisées |
| `litestream exited` | Le sous-processus de réplication de sauvegarde a planté |
| `rate limit exceeded` | Limite de débit des requêtes de l'assistant IA atteinte |

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel — Alexander4, LLC. Ne pas redistribuer. Voir [../legal/license.md](../../legal/license.md).*
