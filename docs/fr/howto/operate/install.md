<!-- vigo-i18n source_sha:982a0113345063d90aa2ab7d9e1164dfacdec55c9e7ed8a6d39aaced45c10bfc -->

# Installer le serveur Vigo

À la fin de cette page, vous disposerez d'un serveur Vigo fonctionnant dans Docker sur un hôte de votre choix, à l'écoute sur `:8443` (REST + interface web) et `:1530` (gRPC), avec le matériel TLS auto-généré et une arborescence de configuration initialisée sous `/srv/vigo/`. À partir de là, [Enrôler un envoy](enroll-an-envoy.md) est l'étape suivante naturelle.

**Quand l'utiliser :** pour mettre en place Vigo pour la première fois, ou pour le réinstaller après avoir effacé un hôte. Si vous avez déjà un serveur en fonctionnement et souhaitez le *mettre à niveau*, voyez la note de mise à niveau au bas de cette page.

**Quand la sauter :** si quelqu'un d'autre a mis en place le serveur et que vous devez seulement enrôler votre portable ou un nœud géré, passez directement à [Enrôler un envoy](enroll-an-envoy.md).

Vigo comporte trois composants : le serveur (`vigosrv`), la CLI (`vigocli`) et l'agent (`vigo`). Le serveur et la CLI sont livrés sous forme d'image conteneur. L'agent est installé sur chaque machine gérée via un script de bootstrap.

![Tableau de bord Vigo après l'installation et le premier check-in](../../img/screenshots/vigo.dashboard.png)

## Serveur

### 1. Initialiser le répertoire de données

Vous pouvez récupérer l'image publiée du serveur ou la compiler vous-même. Dans les deux cas, l'image porte le tag `vigo:latest`, le nom qu'exécute le fichier compose généré.

**Récupérer l'image publiée.** C'est la voie rapide. L'hôte a besoin de Docker avec le plugin Compose et d'un processeur x86_64 ; sous Ubuntu, `sudo apt-get install -y docker.io docker-compose-v2` les installe. L'image est signée avec la clé de publication du projet : les envoys mettent donc à jour leur agent d'eux-mêmes lorsque vous passez à une image plus récente. Elle contient l'agent Linux pour amd64 et arm64. Pour figer une version, utilisez son tag de version, par exemple `:0.77.0`, à la place de `:latest`.

```bash
sudo docker pull ghcr.io/daniel-alexander4/vigo:latest
sudo docker tag ghcr.io/daniel-alexander4/vigo:latest vigo:latest
IMAGE=vigo:latest
```

**Ou la compiler depuis les sources.** La compilation s'exécute dans Docker. L'hôte a besoin de Docker avec les plugins Compose et Buildx, ainsi que de `git` et `rsync` ; sous Ubuntu, `sudo apt-get install -y git rsync docker.io docker-compose-v2 docker-buildx` les installe. La première compilation prend environ 20 minutes. La clé de signature, facultative, permet aux envoys de mettre à jour leur agent d'eux-mêmes ; sans elle, chaque mise à niveau oblige à relancer le bootstrap sur chaque envoy. Sa création demande Go 1.25, et la clé doit être conservée en lieu sûr.

```bash
git clone https://github.com/daniel-alexander4/vigo.git && cd vigo
go run ./server/hardening/cmd/sign -genkey -output signing.key   # facultatif : mise à jour automatique des envoys
sudo scripts/upgrade.sh full --norun   # compile l'image du serveur et l'agent amd64
IMAGE=vigo:latest   # le tag que la compilation donne à l'image locale
```

```bash
sudo mkdir -p /srv/vigo
sudo docker run --rm -v /srv/vigo:/srv/vigo "$IMAGE" --seed-only
```

Cela peuple `/srv/vigo/` avec tout ce qui est nécessaire pour exécuter le serveur :

```
/srv/vigo/
├── docker-compose.yml       # production compose file
├── server.yaml              # server config
├── .env                     # non-secret env vars
├── tls/                     # mTLS certificates (auto-generated)
├── db/                      # SQLite database
├── secrets/                 # encrypted secret files
├── stacks/                  # operator-edited config tree
│   ├── configcrates/        # reusable configcrate definitions
│   ├── usercrates/          # user-scoped configcrates (inert until included via `usercrates:`)
│   ├── templates/           # Go template fragments
│   ├── tasks/               # reusable task definitions
│   ├── workflows/           # workflow definitions
│   ├── envoys.vgo           # hostcrate (any .vgo with envoys: works)
│   ├── roles.vgo            # role definitions (single, fleet-wide)
│   ├── common.vgo           # directory-inherited defaults
│   ├── waivers.vgo          # compliance waivers (any depth)
│   ├── environments.vgo     # per-env configcrate/var overrides
│   └── compliance.vgo       # directory-inherited claims
├── stacks-examples/         # read-only install templates
│                            #   (rebuilt from image on every container start;
│                            #    vigocli config examples copy)
├── .live/                   # published config (locked between publishes)
└── config-history/          # rollback snapshots
```

### 2. Démarrer le serveur

```bash
cd /srv/vigo
sudo docker compose up -d
```

Le serveur démarre sur les ports **1530** (gRPC, check-in des agents) et **8443** (HTTPS — API REST, interface web, métriques, bootstrap), plus **1531** pour le transfert de blobs swarm entre envoys. Ouvrez les trois aux machines que vous enrôlerez si un pare-feu est en travers. Trois étapes CLI mettent l'installation en route à partir d'ici, **dans l'ordre** :

1. **Initialiser le coffre à secrets** — `sudo vigocli secrets init` (le serveur a déjà créé la clé maîtresse ; cette commande définit la phrase secrète de déverrouillage), puis `sudo vigocli secrets unlock`. Le coffre doit être initialisé avant de pouvoir stocker des secrets avec `vigocli secrets set`. Sur l'hôte, `vigocli` est réservé à root : lancez-le avec `sudo`.
2. **Enrôler l'agent local** — `curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh` depuis l'hôte du serveur. **Utilisez `127.0.0.1`, pas `localhost` ni le nom d'hôte propre de cette machine** — l'agent que le script installe compose, à chaque check-in ultérieur, l'adresse exacte utilisée par l'URL de curl, et seule la valeur littérale de bouclage IPv4 est indépendante du résolveur (pas de nsswitch, pas de mDNS, pas de piège IPv6 link-local). L'enrôlement de cet hôte par son adresse LAN ou son nom d'hôte est refusé, car ce nom fait partie des noms TLS du serveur. Cette étape enrôle *uniquement* l'hôte propre du serveur ; toute autre machine que vous souhaitez gérer suit la procédure distincte dans [Enrôler un envoy](enroll-an-envoy.md), qui compose le serveur par son nom d'hôte joignable. Le serveur a une hypothèse canonique (selon CLAUDE.md) qu'un agent `vigo` s'exécute sur le même hôte aux côtés du conteneur ; l'étape de création d'admin ci-dessous utilise le trait `users` de cet agent pour vérifier la correspondance utilisateur-OS. Attendez que l'agent ait remonté un check-in (~30s) avant de continuer.
3. **Créer votre admin par opérateur** — `sudo vigocli webusers create --username <yourname> --role admin` depuis l'hôte du serveur, où `<yourname>` est un véritable compte OS humain sur cette machine (uid≥1000, shell réel). La CLI lit automatiquement votre clé publique depuis `/home/<yourname>/.ssh/id_ed25519.pub` (repli sur `id_rsa.pub`) ; pas de drapeau `--ssh-key-file` pour le rôle admin. Ensuite, `sudo vigocli webusers set-password --username <yourname>` pour définir votre mot de passe de connexion (il stocke un hash bcrypt ; n'écrivez pas directement la clé du coffre).

Connectez-vous à `https://localhost:8443/login` en tant que `<yourname>`. L'utilisateur `admin` par défaut n'est délibérément pas créé automatiquement — les webusers admin correspondent 1:1 à de véritables utilisateurs OS afin que l'attribution d'audit nomme toujours un humain réel et que les sessions Scrier dans le navigateur initialisent automatiquement votre clé publique. Si vous avez besoin de plusieurs opérateurs, répétez l'étape 3 pour chacun.

Si aucun envoy n'a encore fait de check-in, le tableau de bord affiche un panneau de premier démarrage avec la commande de bootstrap de l'étape 2 et une navigation par ailleurs vide — les deux se remplissent automatiquement dès que le premier envoy s'enrôle.

### 3. Vérifier

```bash
sudo docker compose ps                # containers running
curl -sk https://127.0.0.1:8443/healthz # ok = serveur prêt (503 pendant le démarrage ou l'arrêt)
sudo vigocli doctor                   # full diagnostic
```

### 4. Enrôler l'envoy propre du serveur (faites-le en premier)

Vigo suppose que chaque serveur exécute un agent `vigo` co-hébergé sur le même hôte que le conteneur vigosrv. L'envoy propre du serveur est ce qui rend le serveur visible dans `vigocli swarm status`, lui permet de participer à Puddle / Lockbox / Gitback, et d'amorcer des charges utiles vers les pairs via le substrat Swarm. Cette étape est la première chose canonique à faire une fois le conteneur démarré — et elle est **distincte de l'enrôlement des autres envoys** (couvert dans [Enrôler un envoy](enroll-an-envoy.md)) car l'agent propre du serveur se compose *lui-même*, et ce chemin ne doit pas dépendre de la résolution de noms d'hôtes.

Depuis l'hôte du serveur lui-même :

```bash
curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh
```

`127.0.0.1` est la valeur littérale de bouclage IPv4 — pas de nsswitch, pas de DNS, pas de mDNS, pas d'IPv6 link-local. Le script de bootstrap écrit dans `/etc/vigo-envoy/config` l'adresse exacte utilisée par l'URL de curl, si bien que l'agent compose cette adresse exacte à chaque check-in. Si vous utilisez `localhost` ou le nom d'hôte propre de cette machine, le chemin de check-in de l'agent hérite silencieusement d'une dépendance à la pile de résolution de noms du système — et dès qu'une de ses couches change (rotation de la délégation de préfixe Starlink, réannonce de NetworkManager, renommage de l'hôte), l'agent ne peut plus composer son point de ralliement. Utilisez `127.0.0.1` même si `localhost` fonctionne aujourd'hui.

Le script de bootstrap télécharge le binaire de l'agent, génère une CSR, s'enregistre via le chemin d'enrôlement de confiance (le bouclage figure dans la liste CIDR `trusted_enrollment` par défaut — aucun token nécessaire), et installe `vigo-envoy.service` sous systemd. Une fois terminé :

```bash
systemctl is-active vigo-envoy       # → active
sudo vigocli envoys list             # the server should appear under its host's hostname
```

Le nom d'hôte enrôlé de l'agent est ce que renvoie `hostname` sur l'hôte. Faites-le correspondre avec `server.hostname` dans `server.yaml` (utilisé pour l'affichage de conformité) afin que les deux vues s'accordent.

Si vous sautez cette étape, les fonctionnalités Swarm qui transitent par l'envoy du serveur ne feront silencieusement rien pour le nom d'hôte propre du serveur. `vigocli swarm filecast distribute` fonctionne encore (écrit directement dans le répertoire d'amorçage monté en bind), mais la plupart des autres opérations Swarm n'impliquent pas le serveur dans l'ensemble des pairs.

### Services optionnels

Éditez `COMPOSE_PROFILES` dans `/srv/vigo/.env` pour activer des conteneurs optionnels :

```bash
# In .env, uncomment one of:
COMPOSE_PROFILES=ollama              # local AI assistant (free, CPU-only)
COMPOSE_PROFILES=postgres            # Postgres instead of SQLite
COMPOSE_PROFILES=ollama,postgres     # both
```

Le profil `postgres` exige aussi `VIGO_POSTGRES_PASSWORD` dans le même fichier. Les deux conteneurs optionnels sont en réseau hôte et n'écoutent que sur `127.0.0.1` : le serveur joint Ollama à `http://127.0.0.1:11434` et Postgres à `127.0.0.1:5432`.

### Fuseau horaire

Le conteneur hérite du fuseau horaire de l'hôte via un montage en lecture seule de `/etc/localtime`. Tous les horodatages dans l'interface web, les rapports et les journaux d'audit s'affichent dans le fuseau horaire local du serveur. Si vous devez forcer le fuseau horaire, définissez la variable d'environnement `TZ` dans `.env` :

```bash
TZ=America/New_York
```

### Configuration

Éditez `server.yaml` pour les paramètres du serveur (intervalle de check-in, chemins TLS, fédération Spanner, SMTP, intégrations). Éditez `.env` pour les services optionnels et les variables d'environnement. Voir [Configuration du serveur](../../reference/server-yaml.md) pour la référence complète.

### Mise à niveau

Avec l'image publiée, récupérez la nouvelle version, appliquez le tag, puis recréez les conteneurs :

```bash
sudo docker pull ghcr.io/daniel-alexander4/vigo:latest
sudo docker tag ghcr.io/daniel-alexander4/vigo:latest vigo:latest
cd /srv/vigo && sudo docker compose down
sudo docker run --rm -v /srv/vigo:/srv/vigo vigo:latest --seed-only
sudo docker compose up -d
```

Depuis une copie des sources, recompilez l'image et redéployez en une seule étape :

```bash
sudo scripts/upgrade.sh full
```

Ou compilez maintenant et déployez plus tard, par exemple pour redémarrer à un moment calme :

```bash
sudo scripts/upgrade.sh full --norun
sudo scripts/upgrade.sh deploy
```

Ne faites pas la mise à niveau avec un simple `docker compose up -d`. À chaque démarrage, le conteneur `vigo` réécrit `/srv/vigo/docker-compose.yml` à partir de l'image qu'il exécute : tant qu'un conteneur de l'ancienne image existe, le fichier sur disque peut encore être l'ancien, et le conteneur annexe `guacd` serait créé à partir de celui-ci. Le `docker compose down` ci-dessus, comme `upgrade.sh deploy`, arrête les anciens conteneurs avant que le nouveau fichier soit écrit.

Votre configuration, votre base de données, vos secrets et vos certificats TLS persistent dans `/srv/vigo/` à travers les mises à niveau.

## CLI

Le conteneur installe automatiquement `vigocli` dans `/usr/local/bin` sur l'hôte via le montage de volume `/host-bin`. Si le montage n'est pas configuré, installez manuellement :

```bash
docker cp vigo:/usr/local/bin/vigocli /usr/local/bin/vigocli
```

La CLI détecte automatiquement le serveur à `https://localhost:8443`. Pour les serveurs distants :

```bash
export VIGO_SERVER="http://vigo-server:8443"
```

## Agent

L'agent s'exécute sur les machines gérées (envoys). Il prend en charge Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos et Windows.

### Linux

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Télécharge le binaire de l'agent, génère une paire de clés, s'enregistre auprès du serveur, et installe un service systemd (`vigo-envoy`).

### macOS

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```


Détecte macOS et installe un plist launchd (`com.vigo.envoy`).

### FreeBSD / NetBSD

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Installe un script de service rc.d à `/usr/local/etc/rc.d/vigo_envoy`.

### OpenBSD

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Enregistre le service via `rcctl` et l'active au démarrage.

### illumos

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Importe un manifeste de service SMF et l'active via `svcadm`.

### Windows

```powershell
# PowerShell 7+
irm https://<server>:8443/bootstrap?os=windows -SkipCertificateCheck | iex

# PowerShell 5
[Net.ServicePointManager]::ServerCertificateValidationCallback = {$true}; irm https://<server>:8443/bootstrap?os=windows | iex
```

Télécharge l'agent, s'enregistre auprès du serveur, et installe un service Windows. Les drapeaux de contournement de certificat sont l'équivalent du `-k` de curl pour la CA auto-signée.

Voir [Bootstrap](enroll-an-envoy.md) pour les détails sur le flux d'enrôlement, l'authentification par token et les motifs d'enrôlement de confiance.

## Ports

| Port | Protocole | Rôle |
|------|----------|---------|
| 1530 | gRPC (mTLS) | Check-in des agents, remontée de résultats, streaming |
| 8443 | HTTPS | API REST, interface web, métriques, bootstrap |

## Disposition du répertoire de l'agent

```
/etc/vigo-envoy/            # config + identity
├── envoy.uuid                # registration ID
├── certs/                     # TLS keypair
│   ├── ca.crt
│   ├── tls.crt
│   └── tls.key
└── executors/                 # custom executor scripts

/var/lib/vigo/               # persistent state
├── state/                     # LMDB (policy cache, checksums, traits, pending results)
├── snapshots/                 # file snapshots for rollback
└── flags/                     # flag markers for when: expressions
```

## À quoi ressemble la réussite

Un serveur sain remonte ses composants individuellement via `vigocli doctor` :

```text
$ docker exec vigo /usr/local/bin/vigocli doctor
  database ................ OK
  tls ..................... OK (expires 2027-05-05)
  secrets ................. OK (local)
  spanner .................. SKIP (standalone)
  integrations ............ SKIP (no integrations configured)
  config .................. OK (43 configcrates, 5 entries)
  swarm_pattern_order ..... OK (all swarm.*.enabled pattern lists well-ordered)
  stale_envoys ............ OK
  Status: healthy
```

`SKIP` est attendu pour `spanner` (vous n'exécutez pas en mode hub-spoke) et `integrations` (vous n'avez pas encore branché d'alerting externe). `database / tls / secrets / config` devraient tous être `OK` sur une installation fraîche. Un `WARN` sur `stale_envoys` après le premier cycle de check-in signifie qu'au moins un envoy n'a pas remonté depuis `2.5 × max(interval, observed_cycle)` secondes — allez voir ce qui se passe.

## Et ensuite

- **Ajouter un nœud géré** → [Enrôler un envoy](enroll-an-envoy.md).
- **Écrire votre première configuration déclarative** → [Écrire votre premier configcrate](../configcrates/write-your-first-configcrate.md).
- **Si le serveur n'a pas démarré proprement** → [Dépanner les problèmes courants](troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*
