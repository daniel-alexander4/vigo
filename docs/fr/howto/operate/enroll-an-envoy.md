<!-- vigo-i18n source_sha:ee5df72a9bf3a8c1039658fd777362696e4046c353651f0513857325457da2f9 -->

# Enrôler un Envoy

À la fin de cette page, vous aurez une nouvelle machine exécutant l'agent Vigo, authentifiée auprès du serveur, visible dans la liste des Envoys de l'interface Web, et effectuant ses check-ins selon son intervalle configuré. Une seule ligne de shell sur la nouvelle machine fait tout cela.

**Quand l'utiliser :** chaque fois que vous voulez placer une machine *distante* sous gestion Vigo — un nouvel ordinateur portable, une VM neuve, un nœud matériel fraîchement réimagé. L'Envoy distant joint le serveur par nom d'hôte ou IP LAN, ce qui est précisément ce que documente le reste de cette page.

**Quand vous passeriez votre chemin :** l'hôte *propre* au serveur suit une procédure distincte — il s'enrôle lui-même via le loopback IPv4 (`127.0.0.1`) afin que le chemin ne dépende pas de la résolution de nom d'hôte. Voir [Installer le serveur Vigo § Enrôler l'Envoy propre au serveur](install.md#4-enroll-the-servers-own-envoy-do-this-first). Faites celui-ci d'abord, puis revenez ici pour toutes les autres machines.

**Quand vous l'éviteriez totalement :** jamais, pour aucun hôte que vous voulez sous gestion. L'agent est le moyen par lequel Vigo applique l'état.

Le bootstrap est le processus par lequel une nouvelle machine devient un Envoy géré. Le serveur fournit un script de bootstrap en une ligne qui prend en charge l'installation de l'agent et son enrôlement.

## Fonctionnement

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

(Le précédent SVG `bootstrap-flow.svg` présent dans l'arborescence couvre le même chemin ; le diagramme ci-dessus en est la version native Markdown.)

## Script de bootstrap

Le serveur diffuse un script shell sur `GET /bootstrap` :

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

L'option `-k` autorise curl à se connecter au certificat TLS auto-signé du serveur. Une fois l'agent enrôlé, toute communication ultérieure utilise mTLS avec le certificat CA reçu durant le bootstrap. Si votre serveur utilise un certificat de confiance (par exemple via Let's Encrypt), vous pouvez omettre `-k`.

Le script :
1. Détecte le système d'exploitation (Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos) et l'architecture
2. Arrête tout service vigo-envoy existant et supprime les données d'enrôlement précédentes pour repartir proprement
3. Télécharge le binaire d'agent correct depuis le serveur (remplace toujours l'existant)
4. Vérifie la somme de contrôle SHA256 du binaire téléchargé (abandonne en cas de divergence)
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

### Bootstrap sous Windows

Pour Windows, utilisez le bootstrap PowerShell :

```powershell
# PowerShell 7+
irm https://<server>:8443/bootstrap?os=windows -SkipCertificateCheck | iex

# PowerShell 5
[Net.ServicePointManager]::ServerCertificateValidationCallback = {$true}; irm https://<server>:8443/bootstrap?os=windows | iex
```

L'option `-SkipCertificateCheck` (ou le `ServerCertificateValidationCallback` sous PowerShell 5) est l'équivalent du `-k` de curl — elle autorise la connexion au certificat TLS auto-signé du serveur. Une fois dans le script, la vérification TLS est automatiquement contournée pour tous les appels de téléchargement. Après l'enrôlement, toute communication ultérieure utilise mTLS avec le certificat CA reçu durant le bootstrap.

Le script PowerShell effectue les mêmes étapes mais installe un service Windows via `sc.exe` avec redémarrage automatique en cas d'échec. Voir [Prise en charge de Windows](../../reference/vigo/) pour les détails.

## Méthodes d'enrôlement

### Sans token (enrôlement de confiance)

Configurez des motifs de confiance dans `server.yaml` :

```yaml
bootstrap:
  trusted_enrollment:
    - pattern: "*.web.example.com"
      cidrs: ["10.0.0.0/8"]
    - pattern: "*.dev.*"
      cidrs: ["192.168.0.0/16"]
```


Les Envoys correspondant à la fois au glob de nom d'hôte et au CIDR source s'enrôlent sans token.

### Token à usage unique

Pour les environnements plus stricts, générez un token :

```bash
vigocli tokens generate --pattern "web3.example.com"
```

Transmettez le token au script de bootstrap :

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh -s -- --token <token>
```

Les tokens sont :
- À usage unique (consommés lors d'un enrôlement réussi)
- Hachés en bcrypt au repos dans la base de données
- Comparés par motif au nom d'hôte qui s'enrôle

### Motifs approuvés

Pré-approuvez des motifs de nom d'hôte pour que les Envoys correspondants puissent s'enrôler :

```bash
vigocli approved add "*.prod.example.com"
```

## Signature de certificat

Lorsque `server.tls.ca_key_file` est configuré, le serveur agit en autorité de certification :

1. L'agent génère une CSR durant le bootstrap
2. Le serveur signe la CSR avec la clé CA
3. L'agent reçoit un certificat client signé
4. Toute communication gRPC ultérieure utilise ce certificat pour mTLS

La validité du certificat est configurable :

```yaml
bootstrap:
  cert_validity: "8760h"  # 1 year (default)
```

## Intégrité du binaire

Le script de bootstrap vérifie la somme de contrôle SHA256 du binaire d'agent téléchargé avant de poursuivre l'enrôlement. Après avoir téléchargé le binaire, il récupère la somme de contrôle attendue depuis le point de terminaison `/api/v1/agent/latest` du serveur et la compare au condensé calculé localement. Si les sommes de contrôle ne correspondent pas, le script supprime le binaire et abandonne avec une erreur.

Sur les systèmes Unix, le script utilise `sha256sum`, `shasum -a 256` ou `digest -a sha256` (selon ce qui est disponible). Sous Windows, le script PowerShell utilise `Get-FileHash`.

Pour les builds de distribution, les binaires d'agent sont signés avec une clé ED25519. Le pipeline de build (`make dist-agent`, `scripts/upgrade.sh` et l'étape go-builder de `Dockerfile.server`) dérive une `VIGO_AGENT_SIGNING_PUBKEY` à partir de `SIGN_KEY` et l'intègre dans chaque binaire d'agent au moment de la compilation ; la même `SIGN_KEY` signe ensuite chaque binaire cross-compilé, produisant un sidecar `.sig` diffusé via `/api/v1/agent/latest` (champ signature). Durant l'auto-mise à jour, l'agent :

1. Compare sa version compilée à la version annoncée par le serveur — si elles correspondent déjà, la mise à jour est ignorée (disjoncteur évitant les boucles d'exec)
2. Télécharge le nouveau binaire via mTLS (en présentant son certificat client de bootstrap)
3. Vérifie la somme de contrôle SHA256
4. **Fermeture sécurisée :** vérifie la signature ED25519 contre la **clé publique intégrée** de l'agent — et non une clé publique récupérée depuis le serveur. Une signature vide, une clé publique sentinelle ou une divergence entraînent toutes un abandon (ADR-031). L'agent n'installe jamais un binaire non vérifié.
5. Remplace atomiquement le binaire et redémarre par exec

Parce que la clé publique de vérification est compilée dans chaque agent, un serveur compromis ne peut pas substituer sa propre clé à côté de sa propre signature — la vérification est ancrée à l'agent, non au serveur. Les mises à jour peuvent être déclenchées à distance via `vigocli envoys rebootstrap`.

Lorsque `SIGN_KEY` n'est pas défini au moment du build, `build.rs` retombe sur une clé publique sentinelle de 32 octets nuls et l'agent résultant refuse l'auto-mise à jour à l'exécution (abandon par contrôle de sentinelle). Définissez `SIGN_KEY=<path>` avant d'invoquer `make` / `scripts/upgrade.sh` pour activer l'auto-mise à jour de la flotte. La clé privée de signature est un matériel côté opérateur — sauvegardez-la ; sa perte impose un redéploiement complet de la flotte avec une nouvelle clé.


## Sécurité

- Tout le trafic gRPC utilise mTLS — le point de terminaison HTTP de bootstrap est le seul chemin en clair
- Les clés publiques des agents sont stockées côté serveur et vérifiées à chaque check-in (signatures ED25519)
- Les tokens à usage unique préviennent les attaques par rejeu
- Les agents révoqués reçoivent une erreur au check-in et ne peuvent pas se réenrôler avec le même UUID
- Les points de terminaison de bootstrap (`/bootstrap`, `/api/v1/agent/*`, `/api/v1/bootstrap/*`) sont exemptés de l'authentification de l'interface Web afin que les agents puissent s'enrôler sans identifiants
- Le point de terminaison de bootstrap peut être restreint à des CIDRs spécifiques via un reverse proxy

## Dépannage

**« An active envoy with this hostname already exists » :**
- Un Envoy portant le même nom d'hôte est déjà enrôlé. Le script de bootstrap gère cela automatiquement (il passe toujours `--force` pour révoquer et réenrôler). Si vous utilisez `vigo register` directement, passez `--force` :

```bash
vigo register --token <token> --force
```

**L'agent échoue à s'enregistrer :**
- Vérifiez que le port gRPC du serveur (1530) est joignable depuis l'Envoy
- Vérifiez que le token n'a pas déjà été utilisé
- Vérifiez que le nom d'hôte correspond au motif du token ou au motif d'enrôlement de confiance

**Erreurs de certificat après l'enrôlement :**
- Vérifiez que `ca_key_file` est configuré dans server.yaml
- Vérifiez que le certificat n'a pas expiré (paramètre `cert_validity`)

**Le téléchargement du binaire d'agent échoue :**
- Assurez-vous que l'image de conteneur du serveur inclut les binaires d'agent pour la plateforme cible
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

![Liste des Envoys — une ligne par Envoy enrôlé, avec nom d'hôte, dernière activité, indicateur de dérive](../../img/screenshots/vigo.envoys.png)

## Et ensuite

- **Appliquer un état déclaratif à l'Envoy** → [Écrire votre premier configcrate](../configcrates/write-your-first-configcrate.md).
- **L'Envoy s'est enrôlé mais n'effectue pas de check-in** → [Dépanner les problèmes courants](troubleshoot.md#envoy-shows-as-stale-but-the-agent-is-running).
- **Répéter la procédure sur d'autres Envoys** — la même ligne de commande fonctionne pour chaque machine supplémentaire que vous souhaitez gérer.

## Référence

- [Installation](install.md)
- [Référence de la configuration serveur (`server.yaml`)](../../reference/server-yaml.md)
- [Référence d'écriture de configcrate](../../reference/configcrate-language.md)

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
