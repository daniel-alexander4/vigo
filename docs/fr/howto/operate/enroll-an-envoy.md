<!-- vigo-i18n source_sha:8be02f81149387c443c6e2b9f1df5a13cb2bc5a5d9fe25e86c745ddabb0ae7ae -->

# Enrôler un Envoy

À la fin de cette page, vous disposerez d'une nouvelle machine exécutant l'agent Vigo, authentifiée auprès du serveur, visible dans la liste des Envoys de l'interface Web, et effectuant ses check-ins selon l'intervalle configuré. Une seule ligne de shell sur la nouvelle machine suffit à tout réaliser.

**Quand l'utiliser :** chaque fois que vous voulez placer une machine *distante* sous la gestion de Vigo — un nouvel ordinateur portable, une VM neuve, un nœud matériel fraîchement réimagé. L'Envoy distant compose vers le serveur par son nom d'hôte ou son IP LAN, ce qui correspond au reste de cette page.

**Quand l'ignorer :** l'hôte *propre* du serveur suit une procédure distincte — il s'enrôle lui-même via la boucle locale IPv4 (`127.0.0.1`) afin que le chemin ne dépende pas de la résolution de noms d'hôte. Voir [Installer le serveur Vigo § Enrôler l'Envoy propre du serveur](install.md#4-enroll-the-servers-own-envoy-do-this-first). Faites celle-ci en premier, puis revenez ici pour toutes les autres machines.

**Quand l'ignorer complètement :** jamais, pour tout hôte que vous voulez placer sous gestion. L'agent est le moyen par lequel Vigo applique l'état.

Le bootstrap est le processus par lequel une nouvelle machine devient un Envoy géré. Le serveur fournit un script de bootstrap d'une ligne qui prend en charge l'installation et l'enrôlement de l'agent.

## Comment ça fonctionne

```mermaid
sequenceDiagram
    autonumber
    participant N as New envoy
    participant S as Vigo server
    N->>S: GET /bootstrap (HTTPS)
    Note right of S: server returns OS/arch-aware<br/>install script
    S-->>N: install script
    N->>N: download `vigo` binary,<br/>generate ED25519 keypair,<br/>install per-OS service
    N->>S: Register (gRPC over mTLS)<br/>hostname, IP, pubkey, one-time token
    Note right of S: matches hostname against<br/>hostcrate patterns;<br/>validates token (bcrypt-hashed at rest)
    S-->>N: client UUID + initial policy bundle
    N->>S: CheckIn (every checkin.interval)
    Note over N,S: steady state: ed25519-signed CheckIn ↔ signed response,<br/>both sides mTLS-pinned
```

(L'ancien fichier SVG `bootstrap-flow.svg` présent dans l'arborescence couvre le même chemin ; le diagramme ci-dessus en est la version native Markdown.)

## Script de bootstrap

Le serveur sert un script shell à `GET /bootstrap` :

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Le drapeau `-k` permet à curl de se connecter au certificat TLS auto-signé du serveur. Une fois l'agent enrôlé, toute communication ultérieure utilise mTLS avec le certificat CA reçu lors du bootstrap. Si votre serveur utilise un certificat de confiance (par exemple, issu de Let's Encrypt), vous pouvez omettre `-k`.

Le script :
1. Détecte le système d'exploitation (Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos) et l'architecture
2. Arrête tout service vigo-envoy existant et supprime les données d'enrôlement précédentes pour un départ propre
3. Télécharge le bon binaire de l'agent depuis le serveur (remplace toujours l'existant)
4. Vérifie la somme de contrôle SHA256 du binaire téléchargé (abandonne en cas de non-correspondance)
5. Génère une paire de clés ED25519
6. Appelle le point de terminaison d'enregistrement de bootstrap du serveur
7. Reçoit un UUID client et un certificat TLS signé
8. Écrit la configuration de l'agent dans `/etc/vigo-envoy/config`
9. Installe le service approprié pour la plateforme :
   - **Linux :** unité systemd (`vigo-envoy.service`)
   - **macOS :** plist launchd (`com.vigo.envoy`)
   - **FreeBSD / NetBSD :** script rc.d (`/usr/local/etc/rc.d/vigo_envoy`)
   - **OpenBSD :** enregistré via `rcctl`
   - **illumos :** manifeste de service SMF (`svc:/system/vigo-envoy:default`)
10. Démarre le service

### Bootstrap Windows

Pour Windows, utilisez le bootstrap PowerShell :

```powershell
# PowerShell 7+
irm https://<server>:8443/bootstrap?os=windows -SkipCertificateCheck | iex

# PowerShell 5
[Net.ServicePointManager]::ServerCertificateValidationCallback = {$true}; irm https://<server>:8443/bootstrap?os=windows | iex
```

Le drapeau `-SkipCertificateCheck` (ou le `ServerCertificateValidationCallback` sous PowerShell 5) est l'équivalent du `-k` de curl — il permet de se connecter au certificat TLS auto-signé du serveur. Une fois dans le script, la vérification TLS est automatiquement ignorée pour tous les appels de téléchargement. Après l'enrôlement, toute communication ultérieure utilise mTLS avec le certificat CA reçu lors du bootstrap.

Le script PowerShell exécute les mêmes étapes mais installe un service Windows via `sc.exe` avec redémarrage automatique en cas d'échec. Voir [Prise en charge de Windows](../../reference/vigo/) pour plus de détails.

## Méthodes d'enrôlement

### Sans jeton (enrôlement de confiance)

Configurez des motifs de confiance dans `server.yaml` :

```yaml
bootstrap:
  trusted_enrollment:
    - pattern: "*.web.example.com"
      cidrs: ["10.0.0.0/8"]
    - pattern: "*.dev.*"
      cidrs: ["192.168.0.0/16"]
```


Les Envoys correspondant à la fois au glob de nom d'hôte et au CIDR source s'enrôlent sans jeton.

### Jeton à usage unique

Pour les environnements plus stricts, générez un jeton :

```bash
vigocli tokens generate --pattern "web3.example.com"
```

Passez le jeton au script de bootstrap :

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh -s -- --token <token>
```

Les jetons sont :
- À usage unique (consommés lors d'un enrôlement réussi)
- Hachés avec bcrypt au repos dans la base de données
- Mis en correspondance par motif avec le nom d'hôte qui s'enrôle

### Motifs approuvés

Pré-approuvez des motifs de noms d'hôte afin que les Envoys correspondants puissent s'enrôler :

```bash
vigocli approved add "*.prod.example.com"
```

## Signature de certificat

Lorsque `server.tls.ca_key_file` est configuré, le serveur agit comme une autorité de certification :

1. L'agent génère une CSR lors du bootstrap
2. Le serveur signe la CSR avec la clé CA
3. L'agent reçoit un certificat client signé
4. Toute communication gRPC ultérieure utilise ce certificat pour mTLS

La validité du certificat est configurable :

```yaml
bootstrap:
  cert_validity: "8760h"  # 1 year (default)
```

## Intégrité du binaire

Le script de bootstrap vérifie la somme de contrôle SHA256 du binaire de l'agent téléchargé avant de poursuivre l'enrôlement. Après avoir téléchargé le binaire, il récupère la somme de contrôle attendue depuis le point de terminaison `/api/v1/agent/latest` du serveur et la compare au condensé calculé localement. Si les sommes de contrôle ne correspondent pas, le script supprime le binaire et abandonne avec une erreur.

Sur les systèmes Unix, le script utilise `sha256sum`, `shasum -a 256` ou `digest -a sha256` (selon ce qui est disponible). Sous Windows, le script PowerShell utilise `Get-FileHash`.

Pour les builds de distribution, les binaires de l'agent sont signés avec une clé ED25519. Le pipeline de build (`make dist-agent`, `scripts/upgrade.sh` et l'étape go-builder de `Dockerfile.server`) dérive une `VIGO_AGENT_SIGNING_PUBKEY` à partir de `SIGN_KEY` et l'intègre dans chaque binaire de l'agent au moment de la compilation ; la même `SIGN_KEY` signe ensuite chaque binaire cross-compilé, produisant un fichier annexe `.sig` servi via `/api/v1/agent/latest` (champ signature). Lors de la mise à jour automatique, l'agent :

1. Compare sa version compilée à la version annoncée par le serveur — si elles correspondent déjà, la mise à jour est ignorée (disjoncteur empêchant les boucles d'exec)
2. Télécharge le nouveau binaire en utilisant mTLS (en présentant son certificat client de bootstrap)
3. Vérifie la somme de contrôle SHA256
4. **Fail-closed :** vérifie la signature ED25519 par rapport à la **clé publique intégrée** de l'agent — et non une clé publique récupérée depuis le serveur. Une signature vide, une clé publique sentinelle ou une non-correspondance entraînent toutes un abandon (ADR-031). L'agent n'installe jamais un binaire non vérifié.
5. Remplace le binaire de manière atomique et redémarre via exec

Comme la clé publique de vérification est compilée dans chaque agent, un serveur compromis ne peut pas substituer sa propre clé accompagnée de sa propre signature — la vérification s'ancre dans l'agent, pas dans le serveur. Les mises à jour peuvent être déclenchées à distance via `vigocli envoys rebootstrap`.

Lorsque `SIGN_KEY` n'est pas défini au moment du build, `build.rs` se rabat sur une clé publique sentinelle de 32 octets nuls et l'agent résultant refuse la mise à jour automatique à l'exécution (abandon sur vérification de la sentinelle). Définissez `SIGN_KEY=<path>` avant d'invoquer `make` / `scripts/upgrade.sh` pour activer la mise à jour automatique du parc. La clé privée de signature est un élément côté opérateur — sauvegardez-la ; sa perte implique un redéploiement complet du parc avec une nouvelle clé.


## Sécurité

- Tout le trafic gRPC utilise mTLS — le point de terminaison HTTP de bootstrap est le seul chemin en clair
- Les clés publiques des agents sont stockées côté serveur et vérifiées à chaque check-in (signatures ED25519)
- Les jetons à usage unique préviennent les attaques par rejeu
- Les agents révoqués reçoivent une erreur au check-in et ne peuvent pas se réenrôler avec le même UUID
- Les points de terminaison de bootstrap (`/bootstrap`, `/api/v1/agent/*`, `/api/v1/bootstrap/*`) sont exemptés de l'authentification de l'interface Web afin que les agents puissent s'enrôler sans identifiants
- Le point de terminaison de bootstrap peut être restreint à des CIDR spécifiques via un reverse proxy

## Dépannage

**« An active envoy with this hostname already exists » :**
- Un Envoy portant le même nom d'hôte est déjà enrôlé. Le script de bootstrap gère cela automatiquement (il passe toujours `--force` pour révoquer et réenrôler). Si vous utilisez directement `vigo register`, passez `--force` :

```bash
vigo register --token <token> --force
```

**L'agent échoue à s'enregistrer :**
- Vérifiez que le port gRPC du serveur (1530) est accessible depuis l'Envoy
- Vérifiez que le jeton n'a pas déjà été utilisé
- Vérifiez que le nom d'hôte correspond au motif du jeton ou au motif d'enrôlement de confiance

**Erreurs de certificat après l'enrôlement :**
- Vérifiez que `ca_key_file` est configuré dans server.yaml
- Vérifiez que le certificat n'a pas expiré (paramètre `cert_validity`)

**Le téléchargement du binaire de l'agent échoue :**
- Assurez-vous que l'image de conteneur du serveur inclut les binaires de l'agent pour la plateforme cible
- Vérifiez que le volume `/srv/vigo/` est correctement monté

## À quoi ressemble la réussite

Une fois le script terminé et le premier check-in de l'agent effectué, l'Envoy apparaît dans `vigocli envoys list` :

```text
$ docker exec vigo /usr/local/bin/vigocli envoys list
ID                                    HOSTNAME     IP             ENROLLED          LAST SEEN         STATUS
63dfa092-5262-44ac-b04b-1533c709ab42  danlap       192.168.1.2    2026-05-05 13:03  2026-05-14 00:21  active
cb4acb1f-f995-4026-a01e-f5390283ca6c  annlap       192.168.1.74   2026-05-05 13:03  2026-05-14 00:21  active
64cf705c-0caa-4a8c-a536-f660d3f8c625  raspberrypi  192.168.1.78   2026-05-05 13:03  2026-05-14 00:20  active
d1f86266-139f-419a-b760-902c0616f586  plex         192.168.1.219  2026-05-05 13:03  2026-05-14 00:21  active
6403aa29-096b-4b50-85b5-6ee4460665a0  girlslaptop  192.168.1.215  2026-05-05 13:03  2026-05-13 23:37  active
```

…et sur l'hôte de l'agent, `vigo status` rapporte le bundle de politique mis en cache :

```text
$ sudo vigo status
Envoy ID:         cb4acb1f-f995-4026-a01e-f5390283ca6c
Server:           192.168.1.2:1530
Data directory:   /etc/vigo-envoy
State store:      /var/lib/vigo/state

Policy:
  Cached:         yes
  Version:        1
  Configcrates:   27 (cis-ubuntu-access, cis-ubuntu-filesystem, …)
  Resources:      172
```

Dans l'interface Web, le nouvel Envoy apparaît dans `/envoys` avec une heure de check-in et un nombre de ressources renseignés.

![Liste des Envoys — une ligne par Envoy enrôlé, avec le nom d'hôte, la dernière activité et l'indicateur de dérive](../../img/screenshots/vigo.envoys.png)

## Et ensuite

- **Appliquer un état déclaratif à l'Envoy** → [Écrire votre premier configcrate](../configcrates/write-your-first-configcrate.md).
- **L'Envoy s'est enrôlé mais n'effectue pas de check-in** → [Dépanner les problèmes courants](troubleshoot.md#envoy-shows-as-stale-but-the-agent-is-running).
- **Répéter la procédure sur d'autres Envoys** — le même one-liner fonctionne pour chaque machine supplémentaire que vous voulez gérer.

## Référence

- [Installation](install.md)
- [Référence de la configuration serveur (`server.yaml`)](../../reference/server-yaml.md)
- [Référence pour l'écriture de configcrates](../../reference/configcrate-language.md)

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel -- Alexander4, LLC. Ne pas redistribuer. Voir [licence](../../legal/license.md).*
