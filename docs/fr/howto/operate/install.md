<!-- vigo-i18n source_sha:4e8bf12e3c01d5b6077537d84790756b66ef85c26eac72c2ddc122db0cf0fdf6 -->

# Installer le serveur Vigo

À la fin de cette page, vous aurez un serveur Vigo qui tourne dans Docker sur l'hôte de votre choix, à l'écoute sur `:8443` (REST + interface Web) et `:1530` (gRPC), avec le matériel TLS généré automatiquement et une arborescence de configuration initialisée sous `/srv/vigo/`. À partir de là, [Enrôler un envoy](enroll-an-envoy.md) est l'étape naturelle suivante.

**Quand l'utiliser :** lors de la première mise en place de Vigo, ou lors d'une réinstallation après avoir effacé un hôte. Si vous avez déjà un serveur en fonctionnement et que vous voulez le *mettre à niveau*, voyez la note de mise à niveau au bas de cette page.

**Quand la sauter :** si quelqu'un d'autre a mis en place le serveur et que vous avez seulement besoin d'enrôler votre ordinateur portable ou un nœud géré, passez directement à [Enrôler un envoy](enroll-an-envoy.md).

Vigo comporte trois composants : le serveur (`vigosrv`), la CLI (`vigocli`) et l'agent (`vigo`). Le serveur et la CLI sont livrés sous forme d'image de conteneur. L'agent est installé sur chaque machine gérée via un script de bootstrap.

![Tableau de bord Vigo après l'installation et le premier check-in](../../img/screenshots/vigo.dashboard.png)

## Serveur

### 1. Initialiser le répertoire de données

```bash
sudo mkdir -p /srv/vigo
docker run --rm -v /srv/vigo:/srv/vigo us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest --seed-only
```

Cette commande remplit `/srv/vigo/` avec tout ce qui est nécessaire pour exécuter le serveur :

```
/srv/vigo/
├── docker-compose.yml       # production compose file
├── server.yaml              # server config
├── .env                     # non-secret env vars
├── tls/                     # mTLS certificates (auto-generated)
├── license/                 # license .lic files
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
├── config-history/          # rollback snapshots
└── docs/                    # browsable documentation
```

### 2. Démarrer le serveur

```bash
cd /srv/vigo
docker compose up -d
```

Le serveur démarre sur les ports **1530** (gRPC, check-in des agents) et **8443** (HTTPS — API REST, interface Web, métriques, bootstrap). Trois étapes CLI achèvent l'installation à partir d'ici, **dans l'ordre** :

1. **Initialiser le coffre-fort de secrets** — `vigocli secrets init --key-file /srv/vigo/secrets/.master.key` (demande la phrase secrète de déverrouillage), puis `vigocli secrets unlock`.
2. **Enrôler l'agent local** — `curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh` depuis l'hôte du serveur. **Utilisez `127.0.0.1`, pas `localhost` ni le nom d'hôte de cette machine** — l'agent que le script installe compose à chaque check-in ultérieur l'adresse utilisée dans l'URL du curl, et seul le littéral IPv4 de bouclage est indépendant du résolveur (pas de nsswitch, pas de mDNS, pas de piège lié au link-local IPv6). Cette étape n'enrôle *que* l'hôte du serveur lui-même ; toute autre machine que vous voulez gérer suit la procédure distincte décrite dans [Enrôler un envoy](enroll-an-envoy.md), qui compose le serveur par son nom d'hôte joignable. Le serveur part de l'hypothèse canonique (selon CLAUDE.md) qu'un agent `vigo` tourne sur le même hôte aux côtés du conteneur ; l'étape de création d'admin ci-dessous utilise le trait `users` de cet agent pour vérifier la correspondance des utilisateurs du système d'exploitation. Attendez que l'agent ait rapporté un check-in (~30 s) avant de continuer.
3. **Créer votre admin par opérateur** — `sudo vigocli webusers create --username <yourname> --role admin` depuis l'hôte du serveur, où `<yourname>` est un vrai compte humain du système d'exploitation sur cette machine (uid≥1000, shell réel). La CLI lit automatiquement votre clé publique depuis `/home/<yourname>/.ssh/id_ed25519.pub` (repli sur `id_rsa.pub`) ; pas de drapeau `--ssh-key-file` pour le rôle admin. Ensuite `vigocli secrets set password vigo/web/auth/<yourname>` pour définir votre mot de passe de connexion.

Connectez-vous à `https://localhost:8443/login` en tant que `<yourname>`. L'utilisateur `admin` par défaut n'est délibérément pas créé automatiquement — les webusers admin correspondent 1:1 à de vrais utilisateurs du système d'exploitation, de sorte que l'attribution d'audit nomme toujours un humain réel et que les sessions de scrier dans le navigateur initialisent votre clé publique automatiquement. Si vous avez besoin de plusieurs opérateurs, répétez l'étape 3 pour chacun.

Si aucun envoy n'a encore fait de check-in, le tableau de bord affiche un panneau de premier démarrage avec la commande de bootstrap de l'étape 2 et une navigation par ailleurs vide — les deux se remplissent automatiquement une fois le premier envoy enrôlé.

### 3. Vérifier

```bash
docker compose ps                    # container running
curl -sk https://127.0.0.1:8443/healthz # health check
vigocli doctor                     # full diagnostic
```

### 4. Enrôler l'envoy propre au serveur (faites-le en premier)

Vigo suppose que chaque serveur exécute un agent `vigo` co-hébergé sur le même hôte que le conteneur vigosrv. L'envoy propre au serveur est ce qui rend le serveur visible dans `vigocli swarm status`, lui permet de participer à puddle / lockbox / gitback, et amorce les charges utiles vers les pairs via le substrat Swarm. Cette étape est la première chose canonique à faire une fois le conteneur démarré — et elle est **distincte de l'enrôlement des autres envoys** (couvert dans [Enrôler un envoy](enroll-an-envoy.md)), car l'agent propre au serveur se compose *lui-même*, et ce chemin ne doit pas dépendre de la résolution de nom d'hôte.

Depuis l'hôte du serveur lui-même :

```bash
curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh
```

`127.0.0.1` est le littéral IPv4 de bouclage — pas de nsswitch, pas de DNS, pas de mDNS, pas de link-local IPv6. Le script de bootstrap écrit dans `/etc/vigo-envoy/config` l'adresse utilisée dans l'URL du curl, de sorte que l'agent compose exactement cette adresse à chaque check-in. Si vous utilisez `localhost` ou le nom d'hôte de cette machine, le chemin de check-in de l'agent hérite silencieusement d'une dépendance à la pile de résolution de noms du système — et dès qu'une de ses couches change (rotation de la délégation de préfixe Starlink, ré-annonce de NetworkManager, renommage de l'hôte), l'agent ne parvient plus à joindre la maison. Utilisez `127.0.0.1` même si `localhost` fonctionne aujourd'hui.

Le script de bootstrap télécharge le binaire de l'agent, génère une CSR, s'enregistre via le chemin d'enrôlement de confiance (le bouclage figure dans la liste CIDR `trusted_enrollment` par défaut — aucun jeton nécessaire), et installe `vigo-envoy.service` sous systemd. Une fois terminé :

```bash
systemctl is-active vigo-envoy       # → active
vigocli envoys list                  # the server should appear under its host's hostname
```

Le nom d'hôte enrôlé de l'agent est ce que renvoie `hostname` sur l'hôte. Faites-le correspondre à `server.hostname` dans `server.yaml` (utilisé pour l'affichage de conformité) afin que les deux vues concordent.

Si vous sautez cette étape, les fonctionnalités Swarm qui transitent par l'envoy du serveur seront silencieusement sans effet pour le nom d'hôte propre au serveur. `vigocli swarm filecast distribute` fonctionne toujours (écrit directement dans le répertoire d'amorçage monté en bind), mais la plupart des autres opérations Swarm n'impliquent pas le serveur dans l'ensemble des pairs.

### Services optionnels

Modifiez `VIGO_SERVICES` dans `/srv/vigo/.env` pour activer des conteneurs optionnels :

```bash
# In .env, uncomment one of:
VIGO_SERVICES=ollama              # local AI assistant (free, CPU-only)
VIGO_SERVICES=postgres            # Postgres instead of SQLite
VIGO_SERVICES=ollama,postgres     # both
```

### Fuseau horaire

Le conteneur hérite du fuseau horaire de l'hôte via un montage en lecture seule de `/etc/localtime`. Tous les horodatages de l'interface Web, des rapports et des journaux d'audit s'affichent dans le fuseau horaire local du serveur. Si vous devez remplacer le fuseau horaire, définissez la variable d'environnement `TZ` dans `.env` :

```bash
TZ=America/New_York
```

### Configuration

Modifiez `server.yaml` pour les paramètres du serveur (intervalle de check-in, chemins TLS, fédération Spanner, SMTP, webhooks). Modifiez `.env` pour les services optionnels et les variables d'environnement. Voir [Configuration du serveur](../../reference/server-yaml.md) pour la référence complète.

### Mise à niveau

```bash
cd /srv/vigo
docker compose pull
docker compose up -d
```

Votre configuration, votre base de données, vos secrets et vos certificats TLS persistent dans `/srv/vigo/` à travers les mises à niveau.

## CLI

Le conteneur installe automatiquement `vigocli` dans `/usr/local/bin` sur l'hôte via le montage de volume `/host-bin`. Si le montage n'est pas configuré, installez manuellement :

```bash
docker cp vigo:/usr/local/bin/vigocli /usr/local/bin/vigocli
```

La CLI détecte automatiquement le serveur à `https://localhost:8443`. Pour des serveurs distants :

```bash
export VIGO_SERVER="http://vigo-server:8443"
```

## Agent

L'agent tourne sur les machines gérées (envoys). Il prend en charge Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos et Windows.

### Linux

```bash
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

Télécharge le binaire de l'agent, génère une paire de clés, s'enregistre auprès du serveur et installe un service systemd (`vigo-envoy`).

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

Télécharge l'agent, s'enregistre auprès du serveur et installe un service Windows. Les drapeaux d'ignorance du certificat sont l'équivalent du `-k` de curl pour l'autorité de certification auto-signée.

Voir [Bootstrap](enroll-an-envoy.md) pour les détails sur le flux d'enrôlement, l'authentification par jeton et les motifs d'enrôlement de confiance.

## Ports

| Port | Protocole | Objet |
|------|----------|---------|
| 1530 | gRPC (mTLS) | Check-in des agents, rapport des résultats, streaming |
| 8443 | HTTPS | API REST, interface Web, métriques, bootstrap |

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

## À quoi ressemble le succès

Un serveur en bonne santé rapporte ses composants individuellement via `vigocli doctor` :

```text
$ docker exec vigo /usr/local/bin/vigocli doctor
  database ................ OK
  tls ..................... OK (expires 2027-05-05)
  secrets ................. OK (local)
  license ................. OK (Compliant, 5/100 envoys)
  spanner .................. SKIP (standalone)
  integrations ............ SKIP (no integrations configured)
  config .................. OK (43 configcrates, 5 entries)
  swarm_pattern_order ..... OK (all swarm.*.enabled pattern lists well-ordered)
  stale_envoys ............ OK
  Status: healthy
```

`SKIP` est attendu pour `spanner` (vous n'exécutez pas de hub-spoke) et `integrations` (vous n'avez pas encore câblé d'alerte externe). `database / tls / secrets / license / config` devraient tous être à `OK` sur une installation neuve. Un `WARN` sur `stale_envoys` après le premier cycle de check-in signifie qu'au moins un envoy n'a pas fait de rapport depuis `2.5 × max(interval, observed_cycle)` secondes — allez vérifier.

## Et ensuite

- **Ajouter un nœud géré** → [Enrôler un envoy](enroll-an-envoy.md).
- **Écrire votre première configuration déclarative** → [Écrire votre première configcrate](../configcrates/write-your-first-configcrate.md).
- **Si le serveur n'a pas démarré proprement** → [Dépanner les problèmes courants](troubleshoot.md).

---
*Vérifié sur Vigo 0.51.6 · 2026-05-13.*

*Confidentiel -- Alexander4, LLC. Ne pas redistribuer. Voir [licence](../../legal/license.md).*
