<!-- vigo-i18n source_sha:076bf1dcb709f58680c37cd9ae7dfa8860d0e618b3209b6380125bb2d4e903fd -->

# Résoudre les problèmes courants

Cette page se prête bien au grep : lorsqu'un Envoy renvoie une erreur ou qu'une session échoue, recherchez dans cette page la chaîne exacte que voit l'opérateur. Chaque symptôme renvoie vers sa correction. La référence des outils de diagnostic se trouve en bas de page pour les cas où aucun symptôme connu ne correspond.

## Symptômes (sous forme greppable par l'opérateur)

### `SSH pubkey not authorized on this envoy`

La session Scrier-SSH échoue immédiatement avec cette erreur côté serveur. La clé publique de l'opérateur (issue de `users.ssh_public_key` pour les sessions navigateur, ou détectée automatiquement par la CLI pour `vigocli scrier ssh`) ne figure dans le `~/.ssh/authorized_keys` d'aucun utilisateur régulier sur l'Envoy cible.

**Correction :** ajoutez la clé publique de l'opérateur à un utilisateur régulier sur l'Envoy. Chemin typique : déclarez l'utilisateur dans un usercrate avec `authorized_keys:` et publiez. Puis réessayez. Voir [Configurer Scrier](set-up-scrier.md#make-sure-the-operators-pubkey-is-in-some-regular-users-authorized_keys-on-each-envoy).

### `no SSH pubkey available — either run vigocli scrier ssh...`

L'utilisateur web n'a pas de `ssh_public_key` défini ET la requête de connexion n'en portait pas (c.-à-d. qu'elle provenait du navigateur, pas de la CLI).

**Correction :** exécutez `vigocli scrier ssh <envoy>` une fois depuis un hôte où réside votre clé SSH — la CLI la détecte automatiquement et écrit votre clé publique dans `users.ssh_public_key`. Les sessions navigateur fonctionnent ensuite. Ou bien faites `PUT /api/v1/users/{id}` avec `ssh_public_key` directement.

### `agent <uuid> did not open TunnelStream within 90s`

La session Scrier expire en attendant que l'agent de l'Envoy rappelle. L'agent a reçu le signal de réveil (ou aurait dû) mais n'a pas ouvert le TunnelStream.

**Correction :** vérifiez l'état de l'agent de l'Envoy — `vigocli envoys list <uuid>` pour la péremption ; `vigocli traits livequery <uuid> host_self.fs_root_pct` pour la pression disque (un agent affamé peut ne pas réussir à ouvrir de nouveaux sockets) ; inspectez l'hôte de l'agent avec `vigo status` sur la machine. Récupérations en cas de pression disque : videz journald (`journalctl --vacuum-size=200M`) et le cache apt (`apt clean`) ; redémarrez l'agent.

### `scrier: verify pubkey not found in any regular user's authorized_keys`

Erreur côté agent issue du scan scrier. La clé publique de l'opérateur ne figure dans `/home/*/.ssh/authorized_keys` pour aucun utilisateur régulier (uid != 0).

**Correction :** identique au premier symptôme ci-dessus. root n'est **jamais** une cible d'atterrissage scrier ; si la clé de l'opérateur ne réside que dans `/root/.ssh/authorized_keys`, le scan ne la trouvera pas. Ajoutez-la à un utilisateur régulier.

### L'Envoy apparaît comme périmé alors que l'agent tourne

Le prédicat de fraîcheur a marqué l'Envoy hors ligne alors que l'agent tourne. Probablement : l'intervalle de check-in est configuré trop bas et le cycle de convergence prend plus de temps que l'intervalle, si bien que les check-ins s'accumulent.

**Correction :** vérifiez `vigo status` sur l'Envoy pour la cadence inter-CheckIn observée. Si elle est bien supérieure à l'intervalle `checkin.interval` configuré, la passe de convergence est le goulet d'étranglement. Augmentez l'intervalle, ou définissez l'override `checkin.offline_threshold` dans `server.yaml` (voir [reference/server-yaml.md](../../reference/server-yaml.md)).

### `server does not recognize this envoy (unknown id) — re-bootstrap required`

L'identité enrôlée de l'agent (son UUID client) n'a aucune ligne sur le serveur, si bien que chaque check-in est rejeté avec un `NotFound` gRPC et que l'agent recule (5 min) en continuant d'appliquer sa dernière politique en cache hors ligne. Cause : la base du serveur a été réinitialisée/reconstruite, ou l'Envoy a été supprimé (`vigocli envoys delete`) — pendant que l'agent continuait de tourner avec son ancien UUID. (À distinguer d'un Envoy *révoqué*, qui est rejeté avec `PermissionDenied "envoy is revoked"` et doit rester rejeté.) Le serveur journalise l'orphelin une fois toutes les 10 min sous `WARN check-in from unknown envoy`, et `vigo_sig_verify_failed_total{reason="envoy_not_found"}` grimpe.

**Correction :** re-bootstrappez l'agent sur cet hôte pour le ré-enrôler avec une identité fraîche : `curl -sSfk https://<server>:8443/bootstrap | sudo sh`. (L'agent ne se ré-enrôle **pas** lui-même — le ré-enrôlement reste un acte délibéré et contrôlé.) Si l'hôte est mis hors service, arrêtez son agent à la place.

### Check-in refusé : `envoy has no enrolled client certificate pin`

L'Envoy a été enrôlé avant que le serveur n'épingle les certificats clients, ou son épingle n'a pas pu être enregistrée à l'enrôlement : le serveur n'a donc rien pour prouver que la connexion est directe et refuse chaque check-in avec un `Unauthenticated` gRPC. Le serveur liste ces Envoys dans un `WARN cert-pin: envoys with no enrolled client-cert pin` au démarrage, `vigocli doctor` les signale sous `unpinned_envoys`, et `vigo_sig_verify_failed_total{reason="cert_pin_missing"}` grimpe.

**Correction :** ré-enrôlez l'agent sur chaque hôte listé : `curl -sSfk https://<server>:8443/bootstrap | sudo sh`. Le ré-enrôlement capture une nouvelle épingle.

### Check-ins refusés avec `too many failed check-in verifications from this address`

Avec `rate_limit.enabled`, une IP source qui envoie plus de `rate_limit.checkin_failures_per_ip` vérifications échouées par minute (identifiants d'Envoy inconnus ou révoqués, signatures invalides, refus d'épingle de certificat) voit ses check-ins refusés avec `ResourceExhausted` jusqu'à ce que le budget se reconstitue ; `vigo_checkin_ip_throttled_total` compte ces refus. Les check-ins sains ne consomment jamais ce budget : cela signifie que quelque chose derrière cette adresse échoue de façon répétée.

**Correction :** trouvez l'agent défaillant derrière l'adresse (les lignes `WARN` du serveur pour les Envoys inconnus, révoqués ou sans épingle le nomment) et ré-enrôlez-le ou arrêtez-le. Si de nombreux Envoys partagent l'adresse et que quelques-uns doivent échouer un moment, augmentez `checkin_failures_per_ip` (voir [reference/server-yaml.md](../../reference/server-yaml.md#rate_limit)).

### `vigocli config publish` échoue avec des erreurs de validation

La config publiée n'a pas franchi l'une des portes de validation (syntaxe YAML, intégrité des références croisées, détection de dépendances circulaires, idempotence, garde-fous, validité des URL).

**Correction :** l'assistant IA intégré diffuse des suggestions de correction vers votre terminal lorsque la validation échoue. Lisez-les. Pour des portes spécifiques, voir [Publier votre config](../configcrates/publish-config.md). Pour ignorer les contrôles de validité des URL en environnement isolé (air-gapped), utilisez `--skip-url-checks`.

### vigosrv s'arrête au démarrage avec `config load failed`

L'arbre de config publié dans `/srv/vigo/.live/` comporte une erreur structurelle (typiquement noms en double, syntaxe YAML, références pendantes, ou violation de structure). vigosrv refuse de démarrer plutôt que de tourner sans politique — un serveur d'enforcement de config sans config laisse silencieusement la flotte échouée sur son dernier état en cache, ce qui est pire qu'un crash bruyant.

**Correction :** réparez la source sous `/srv/vigo/stacks/`, puis `vigocli config publish` (les opérations sur fichiers s'exécutent même serveur arrêté), puis redémarrez vigosrv. Le message d'erreur de démarrage nomme la règle en échec.

### Boucle de convergence d'un configcrate — la même ressource change à chaque cycle

Une ressource déclare un état qu'un processus externe ne cesse d'annuler (un gestionnaire de paquets revient automatiquement à une version, un autre outil réécrit le fichier, l'OS met à jour une valeur).

**Correction :** `vigocli config trace <envoy> <configcrate>` montre la chaîne de résolution. Inspectez ce qui écrit le fichier en dehors de Vigo. Le compteur de dérive sur la page de détail de l'Envoy distingue `Diverged` (≥5 exécutions consécutives avec changements — ce cas-ci) de `Changed` (ponctuel).

### Un rollback de convergence s'est déclenché en pleine publication

`server/publish/` annule automatiquement une publication dont la convergence passe sous le seuil.

**Correction :** le précédent `.live/` valide est restauré automatiquement ; l'opérateur voit le rollback dans `vigocli config history` et dans le journal d'audit de publication. Inspectez les exécutions qui ont déclenché le rollback pour trouver ce qui a changé ; corrigez et republiez.

### `kex_exchange_identification: read: Connection reset by peer` sur le sshd d'un Envoy (pas scrier)

La connexion SSH directe vers l'hôte de l'Envoy (pas via scrier) est rejetée. Généralement pression disque ou pénurie de ressources sur l'hôte.

**Correction :** vérifiez le disque + la charge. Les sous-systèmes Swarm de Vigo refusent les pushs en dessous de 10 % d'espace disque libre — ce qui corrèle souvent avec un dysfonctionnement de sshd sur le même hôte. Videz journald + le cache apt ; redémarrez sshd si nécessaire.

### `vigo run` reste sans sortie

La sortie d'une commande est capturée, pas diffusée en continu : une ressource longue ressemble donc à une ressource bloquée jusqu'à ce qu'elle se termine. Depuis la 0.76.89, l'agent tranche lui-même : toutes les 15 secondes, il journalise une ligne `still running` avec l'état du processus et les octets transférés, si bien que `state=D` avec un `io_delta_bytes` qui grimpe vous indique qu'une décompression de paquet lente est bien en cours. Voir [Progress reporting](../../reference/vigo/resources/exec.md#progress-reporting).

**S'il est réellement bloqué**, l'agent le signale désormais et le tue : `child process is STOPPED and cannot make progress`. La cause est une commande qui a tenté de lire le terminal. Les commandes au premier plan reçoivent `/dev/null` sur stdin, donc cela n'arrive que lorsque quelque chose ouvre directement `/dev/tty` — certains scripts de mainteneur Debian le font, et `DEBIAN_FRONTEND=noninteractive` ne l'empêche pas.

**Correction :** rendez la commande non interactive. Pour confirmer le diagnostic à la main :

```bash
ps -o pid,stat,wchan:20,args -p <pid>   # STAT of T = stopped; D = working on disk
ls -l /proc/<pid>/fd/0                  # should be /dev/null, never /dev/pts/N
```

Un `T` dans `STAT` n'est jamais sain. Un `D` l'est presque toujours.

## Outils de diagnostic

### vigo status (côté agent)

À exécuter sur n'importe quel Envoy pour inspecter l'état local de l'agent sans contacter le serveur :

```bash
sudo vigo status
```

Cela affiche l'UUID de l'Envoy, l'adresse du serveur, les détails de la politique en cache (version, configcrates, ressources), l'âge des traits en cache, la profondeur de la file de résultats en attente, et l'état de la clé publique du serveur. Il lit uniquement le fichier de config et le store d'état LMDB -- aucun réseau requis.

Utile pour vérifier :
- Si l'agent est enregistré (`Envoy ID` vs `(not registered)`)
- Quelle version de politique l'agent exécute (`Policy > Version`)
- Si des résultats hors ligne sont en file (compteur `Pending results`)
- Si l'agent dispose de la clé de signature des bundles du serveur (`Server pubkey`)

Voir [CLI de l'agent](../../reference/vigocli/agent.md#status) pour des exemples de sortie complets.

### vigocli doctor

La commande `doctor` vérifie tous les sous-systèmes du serveur et rapporte leur santé :

```bash
vigocli doctor
```

Elle effectue 7 contrôles : base de données, certificats TLS, fournisseur de secrets, spanner, SMTP, config, et Envoys périmés. Chaque contrôle rapporte `pass`, `warn`, `fail` ou `skip`.

### vigocli runs

Listez les exécutions de convergence récentes pour repérer les échecs :

```bash
vigocli runs list
vigocli runs list --envoy web-01.example.com
vigocli runs show <run-id>
```

Affichez ce qui dérive dans toute la flotte en une commande :

```bash
vigocli runs drift
vigocli runs drift --envoy web-01.example.com
```

### Métriques Prometheus

Surveillez l'endpoint `/metrics` pour :

- `vigo_checkins_total{status="error"}` -- échecs de check-in
- `vigo_sig_verify_failed_total` -- échecs de vérification de signature
- `vigo_config_reload_errors_total` -- problèmes de rechargement de config
- `vigo_checkin_age_seconds` -- détection d'Envoy périmé


Voir [Métriques](set-up-monitoring.md) pour la liste complète.

---

## Problèmes courants

### Le bootstrap échoue avec « Connection refused »

**Symptômes :** `curl: (7) Failed to connect to <server> port 8443: Connection refused` immédiatement après le démarrage du serveur.

**Cause :** le serveur met 30 à 60 secondes à démarrer — il compile les certificats TLS au premier lancement, exécute les migrations de base de données, charge les configs, et réhydrate le FleetIndex. L'endpoint de bootstrap n'est pas disponible tant que le serveur n'a pas journalisé `server ready`.

**Correction :** attendez la fin du démarrage du serveur :

```bash
# Watch for "server ready" in the logs
docker compose logs -f vigo | grep -m1 "server ready"

# Then bootstrap
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

### L'agent ne se connecte pas

**Symptômes :** les logs de l'agent montrent des échecs de handshake TLS ou « connection refused ».

**Diagnostic :**

1. Vérifiez que le serveur écoute sur le port 1530 :
   ```bash
   ss -tlnp | grep 1530
   ```

2. Vérifiez que l'agent a le bon certificat CA :
   ```bash
   openssl s_client -connect server:1530 -CAfile /etc/vigo/ca.pem
   ```

3. Vérifiez que le certificat client de l'agent a été signé par la même CA :
   ```bash
   openssl verify -CAfile /etc/vigo/ca.pem /etc/vigo/client.crt
   ```

4. Consultez le log de l'agent pour des erreurs précises :
   ```bash
   journalctl -u vigo-envoy -f
   ```

**Causes courantes :**
- Décalage du certificat CA entre serveur et agent
- Certificats TLS expirés (à vérifier avec `vigocli doctor`)
- Pare-feu bloquant le port 1530

### Échecs de check-in

**Symptômes :** l'agent fait son check-in mais reçoit des erreurs. Les logs du serveur montrent des échecs de vérification de signature.

**Diagnostic :**


1. Consultez le log du serveur pour les erreurs de signature :
   ```
   vigo_sig_verify_failed_total
   ```

2. Vérifiez la synchronisation d'horloge entre agent et serveur. La fenêtre de signature est de 5 minutes par défaut (`tuning.signature_window`). Si les horloges dérivent au-delà, les signatures seront rejetées.

3. Vérifiez si l'Envoy est révoqué :
   ```bash
   vigocli envoys list --all   # STATUS shows revoked
   ```

4. Vérifiez que la paire de clés de l'agent correspond à la clé publique stockée sur le serveur. L'agent génère une paire de clés ED25519 à l'enrôlement, et la clé publique est stockée dans la table `envoys`.

**Causes courantes :**
- Décalage d'horloge dépassant la fenêtre de signature
- Agent ré-enrôlé avec une nouvelle paire de clés mais l'ancien UUID
- L'Envoy a été révoqué

### Le serveur a démarré avec zéro configcrate

**Symptômes :** le serveur tourne mais les Envoys reçoivent une politique vide (aucune ressource appliquée). Les logs montrent `config loaded with validation errors — starting with empty config`.

**Cause :** erreurs de validation d'idempotence dans les configcrates de `.live/`. Le serveur les traite comme non fatales — il démarre avec une config vide afin que l'API REST, l'UI et gRPC restent disponibles pour le diagnostic.

**Correction :**
1. Examinez le détail de l'erreur : `vigocli doctor` ou consultez les logs du serveur
2. Corrigez le configcrate dans `stacks/` (le linter de configcrate détecte et répare la plupart des problèmes)
3. Republiez : `vigocli config publish`

### Erreurs de rechargement de config

**Symptômes :** les changements de config ne sont pas pris en compte. `vigo_config_reload_errors_total` augmente.

**Diagnostic :**

1. Consultez les logs du serveur pour les erreurs de rechargement :
   ```
   grep "config reload" /var/log/vigo-server.log
   ```

2. Validez les fichiers de config manuellement :
   ```bash
   vigocli config publish --dry-run
   ```

3. Erreurs YAML courantes :
   - Clés en double dans un même mapping
   - Expressions `when:` invalides
   - Références circulaires `depends_on`
   - Définitions de configcrate manquantes référencées par des rôles

**Causes courantes :**
- Erreurs de syntaxe YAML
- Configcrate référencé dans un rôle mais non défini dans `configcrates/`
- Dépendance circulaire dans `depends_on`

### L'Envoy se déclare conforme mais les ressources ne sont pas appliquées

**Symptômes :** un Envoy affiche `compliant` avec `CHANGED 0, FAILED 0`, mais les changements attendus (fichiers, utilisateurs, sudoers, etc.) ne sont pas présents sur la machine.


**Cause :** le binaire de l'agent est trop ancien pour comprendre les paramètres de ressource plus récents. L'agent voit la ressource, vérifie les champs qu'il connaît (par ex. l'utilisateur existe, le shell est correct), rapporte « already compliant » et ignore silencieusement les paramètres qu'il ne reconnaît pas (par ex. `sudo_nopasswd`, `password`, `authorized_keys`).

**Diagnostic :**

1. Vérifiez la version de l'agent :
   ```bash
   vigocli envoys show <hostname>
   ```
   Regardez la ligne `Agent:`. Comparez avec la version distribuée par le serveur.

2. Vérifiez que la ressource figure dans la config résolue :
   ```bash
   vigocli config trace <hostname>
   ```
   Si le configcrate est listé mais que les ressources ne sont pas appliquées, c'est un décalage de version de l'agent.

3. Consultez le résultat d'exécution pour l'Envoy concerné :
   ```bash
   vigocli runs list --envoy <envoy-uuid> --limit 1
   ```

**Correction :** mettez à jour le binaire de l'agent. Si vous pouvez pousser via l'agent existant :

```bash
# Re-bootstrap via push — downloads and installs the latest agent binary
vigocli task run "curl -sSfk https://<server>:8443/bootstrap | sh" --target "<hostname>"
```

Si vous pouvez vous connecter en SSH avec une clé (sans sudo nécessaire pour la connexion) :

```bash
make build-agent
scp bin/vigo user@hostname:/tmp/vigo
ssh user@hostname "sudo mv /tmp/vigo /usr/local/sbin/vigo && sudo systemctl restart vigo-envoy"
```

Si vous êtes complètement bloqué (pas de sudo, pas de push), il vous faudra un accès console ou physique à la machine.

### Des Envoys apparaissent comme périmés

**Symptômes :** des Envoys apparaissent comme « périmés » dans l'UI ou la CLI alors qu'ils sont en ligne.

**Diagnostic :**

1. Vérifiez le seuil de péremption. Un Envoy est périmé s'il n'a pas fait de check-in dans `2.5 × checkin.interval` :
   ```yaml
   checkin:
     interval: "5m"   # stale after 12 minutes 30 seconds
   ```

2. Sur l'Envoy, vérifiez que l'agent tourne :
   ```bash
   systemctl status vigo-envoy
   ```

3. Consultez les logs de l'agent pour des erreurs :
   ```bash
   journalctl -u vigo-envoy --since "10 minutes ago"
   ```


4. Vérifiez l'état local de l'agent :
   ```bash
   sudo vigo status
   ```
   Regardez `Pending results` (en file mais non remis) et `Policy > Version` (cache périmé).

5. Vérifiez la connectivité réseau :
   ```bash
   curl -k https://server:1530   # should get a TLS error, not a connection error
   ```

**Causes courantes :**
- Le processus de l'agent a planté ou a été arrêté
- Partition réseau entre l'agent et le serveur
- Échecs de résolution DNS sur l'agent

### Usage mémoire élevé

**Symptômes :** la mémoire du serveur croît avec le temps.

**Diagnostic :**

1. Vérifiez la taille du FleetIndex -- il conserve tout l'état des Envoys en mémoire. Chaque Envoy avec traits consomme environ 2 à 5 Ko :
   ```
   vigo_nodes_total * ~4KB = expected memory for FleetIndex
   ```

2. Vérifiez le ring buffer des exécutions récentes -- il conserve les 1000 dernières exécutions en mémoire.

3. Si vous utilisez `run_store: memory`, chaque Envoy conserve jusqu'à `run_store_capacity` exécutions en mémoire.

4. Surveillez `vigo_database_size_bytes` pour la croissance de la base.

**Atténuation :**
- Réduisez `database.retention` (défaut `30d`) pour rétrécir la fenêtre conservée
- Utilisez `run_store: database` plutôt que `memory` pour les grandes flottes

### Échecs de résolution de secrets

**Symptômes :** le serveur ne démarre pas ou la config affiche des valeurs `secret:` non résolues.

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

3. Les secrets utilisent le format de chemin `secret:path/to/secret` qui correspond à un fichier dans le répertoire des secrets.

**Causes courantes :**
- Le fichier de secret n'existe pas au chemin attendu
- Les permissions du fichier empêchent le processus serveur de lire le secret

- Le backend de secrets est mal configuré

### Erreur de certificat d'agent : « not valid for name »

**Symptômes :** l'agent se connecte mais reçoit `certificate not valid for name "192.168.x.x"`.

**Cause :** le certificat TLS serveur auto-généré n'inclut pas l'IP LAN de votre hôte. Les conteneurs Docker obtiennent leur propre nom d'hôte et IP, qui sont les valeurs par défaut dans le certificat.

**Correction :** ajoutez l'IP ou le nom DNS de votre serveur à `tls_sans` dans `server.yaml` :

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

Re-bootstrappez les Envoys afin qu'ils récupèrent la nouvelle CA :

```bash
sudo systemctl stop vigo-envoy
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

### Le bootstrap échoue avec « Failure writing output »

**Symptômes :** `curl: (23) Failure writing output to destination` pendant le bootstrap.

**Cause :** l'ancien binaire de l'agent à `/usr/local/sbin/vigo` est verrouillé par un processus en cours d'exécution. Curl ne peut pas écraser un binaire en cours d'exécution.

**Correction :**

```bash
# Stop the agent first
sudo systemctl stop vigo-envoy

# Then re-run bootstrap
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

### Réinitialiser un Envoy

Pour réinitialiser complètement un Envoy et le re-bootstrapper de zéro :

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
sudo rm -f /usr/local/bin/git-remote-gitback

# 3. Re-bootstrap
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Ou utilisez le script de réinitialisation, qui exécute les mêmes étapes. Il ne touche jamais à l'état du serveur dans `/srv/vigo`, il est donc sans risque sur un hôte serveur qui exécute son propre Envoy :

```bash
sudo scripts/reset-envoy.sh
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

L'Envoy se ré-enrôlera avec un nouvel UUID et une nouvelle paire de clés. L'ancienne entrée de l'Envoy sur le serveur devient orpheline — révoquez-la avec :

```bash
vigocli envoys revoke <old-uuid>
```

### Réinitialiser le serveur

Pour effacer tout l'état du serveur et repartir à neuf :

```bash
cd /srv/vigo
docker compose down
sudo find /srv/vigo -mindepth 1 -delete
docker run --rm -v /srv/vigo:/srv/vigo vigo:latest --seed-only
docker compose up -d
```

Cela re-sème `server.yaml`, `.env`, `docker-compose.yml`, les certificats TLS et les exemples.

---

## Messages de log à surveiller

| Message de log | Signification |
|------------|---------|
| `config reload failed` | Erreur d'analyse YAML ou de validation |
| `signature verification failed` | L'agent a envoyé une signature ED25519 invalide |
| `envoy revoked` | Un check-in a été rejeté car l'Envoy est révoqué |
| `state flusher: batch last_seen failed` | Erreur d'écriture SQLite pendant le flush du FleetIndex |
| `webhook: delivery failed after retries` | Toutes les tentatives de relivraison d'un webhook épuisées |
| `litestream exited` | Le sous-processus de réplication de sauvegarde a planté |
| `rate limit exceeded` | Limite de débit des requêtes de l'assistant IA atteinte |

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
