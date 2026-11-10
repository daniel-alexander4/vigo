<!-- vigo-i18n source_sha:a7757244a3abcf80e5912ee095d033f555ae4f00a6a3495691ae36c30704ab9a -->

# Démarrage rapide

Mettez en route le serveur Vigo, enrôlez votre premier Envoy et appliquez votre premier configcrate. Une dizaine de minutes de copier-coller avec l'image publiée. Si vous préférez être guidé pas à pas, suivez plutôt les [tutoriels](start/) — même parcours, plus d'explications.

## 1. Démarrer le serveur

Vous pouvez récupérer l'image publiée du serveur ou la compiler vous-même. Dans les deux cas, l'image porte le tag `vigo:latest`, le nom qu'exécute le fichier compose généré.

**Récupérer l'image publiée.** C'est la voie rapide. L'hôte a besoin de Docker avec le plugin Compose et d'un processeur x86_64 ; sous Ubuntu, `sudo apt-get install -y docker.io docker-compose-v2` les installe. L'image est signée avec la clé de publication du projet : les envoys mettent donc à jour leur agent d'eux-mêmes lorsque vous passez à une image plus récente. Elle contient l'agent Linux pour amd64 et arm64. Pour figer une version, utilisez son tag de version, par exemple `:0.80.1`, à la place de `:latest`.

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
sudo sed -i "s|^  # hostname: .*|  hostname: $(hostname)|" /srv/vigo/server.yaml
cd /srv/vigo
sudo docker compose up -d
```

`--seed-only` écrit `/srv/vigo/docker-compose.yml`, `server.yaml`, `.env` et des configurations d'exemple, puis se termine. La ligne `sed` définit `server.hostname` dans `server.yaml` sur le nom de cet hôte, dont le serveur a besoin pour trouver son propre agent lorsque vous créez votre connexion admin ; vous pouvez aussi modifier le fichier à la main avant le démarrage. `docker compose up -d` démarre le serveur, qui génère son autorité de certification et ses certificats TLS sous `/srv/vigo/tls/` au premier démarrage. REST + interface Web sur `https://localhost:8443`, gRPC sur `localhost:1530`.

`docker run` unique si vous ne voulez pas de compose :

```bash
sudo mkdir -p /srv/vigo
sudo docker run -d --name vigo \
  --network host \
  -v /srv/vigo:/srv/vigo \
  -v /usr/local/bin:/host-bin \
  "$IMAGE"
```

`--network host` lie 8443 (REST/UI), 1530 (gRPC) et 1531 (Swarm) directement sur l'hôte — correspondant au compose généré, qui place le serveur sur le réseau de l'hôte afin que le trafic des agents ne passe pas par le proxy userland par port de Docker. Le montage `/host-bin` permet au conteneur d'installer `vigocli` sur l'hôte. (Le sidecar `guacd` de Scrier n'est pas démarré par ce `docker run` nu ; utilisez compose si vous avez besoin de sessions Scrier RDP/VNC.)

Ouvrez `https://localhost:8443` et acceptez le certificat auto-signé. Il n'existe pas de connexion admin par défaut : vous créez la vôtre à l'étape 3, une fois l'agent propre au serveur enrôlé.

## 2. Enrôler des Envoys

**L'hôte du serveur lui-même en premier.** Chaque serveur Vigo exécute un agent `vigo` co-hébergé afin de pouvoir participer aux opérations Swarm aux côtés de ses Envoys. Depuis l'hôte du serveur — **utilisez le littéral de loopback IPv4 `127.0.0.1`, pas `localhost` ni le propre nom d'hôte de la machine** ; l'agent compose l'adresse utilisée dans l'URL curl à chaque check-in ultérieur, et seul le loopback est indépendant du résolveur :

```bash
curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh
```

Puis toute *autre* machine que vous souhaitez gérer — notez qu'il s'agit d'une forme de commande distincte, par conception, car les Envoys distants joignent le serveur par nom d'hôte / IP LAN tandis que l'Envoy propre au serveur se joint lui-même par loopback :

```bash
# Sur l'Envoy :
curl -sSfk https://<server-ip>:8443/bootstrap | sudo sh
```

Le `-k` est intentionnel. Le serveur Vigo exécute sa propre autorité de certification et présente un certificat auto-signé. La confiance s'établit à l'étape *suivante* : le script de bootstrap télécharge l'agent, génère une paire de clés ED25519 propre à l'hôte et l'enregistre auprès du serveur (sans jeton depuis le loopback et les CIDR de LAN privés par défaut ; exigez un jeton à usage unique pour tout ce qui se trouve en dehors des plages d'enrôlement de confiance — frappez-en un avec `vigocli tokens generate`, liez-le avec `vigocli approved add '*.example.com' --token <token>`, puis récupérez `/bootstrap?token=<token>`). Chaque appel gRPC ultérieur entre l'agent et le serveur est épinglé en mTLS contre cette paire — `-k` ne relâche que la récupération de bootstrap à usage unique, pas la sécurité en régime établi.

Le script de bootstrap installe également le service propre à l'OS (systemd / launchd / rc.d / rcctl / SMF / Windows Service).

Après l'enrôlement, chaque Envoy apparaît dans le tableau de bord à `https://localhost:8443/envoys` et fait un check-in à l'intervalle défini par `checkin.interval` dans `server.yaml`.

## 3. Créer votre connexion

Les connexions admin correspondent à de véritables comptes de l'hôte du serveur : le compte doit avoir un shell de connexion et une clé publique SSH dans `~/.ssh/id_ed25519.pub` ou `id_rsa.pub` (créez-en une avec `ssh-keygen -t ed25519` en tant que cet utilisateur s'il n'en a pas). Le serveur vérifie le compte d'après ce que remonte son propre agent, en se servant du `server.hostname` défini à l'étape 1. Initialisez le coffre à secrets, puis créez l'admin ; `webusers create` demande le mot de passe de connexion.

```bash
sudo vigocli secrets init          # sets the vault passphrase
sudo vigocli secrets unlock
sudo vigocli webusers create --username <you> --role admin
```

Le serveur vérifie le compte d'après la liste des comptes que l'agent a remontée en dernier. Si `webusers create` indique que l'agent n'a pas encore fait de remontée, ou que le compte n'est pas un utilisateur humain parce que vous l'avez créé après l'étape 2, attendez le prochain check-in de l'agent (jusqu'à environ cinq minutes) et relancez la commande.

Connectez-vous à `https://localhost:8443/login` en tant que `<you>`.

## 4. Écrire un configcrate

```yaml
# /srv/vigo/stacks/configcrates/motd.vgo
name: motd
resources:
  - name: motd-file
    type: file
    target_path: /etc/motd
    state: present
    content: |
      ========================================
      Managed by Vigo
      Hostname: {{ .Traits.network.hostname }}
      OS: {{ .Traits.os.distro }}
      ========================================
    owner: root
    group: root
    mode: "0644"
```

## 5. Affecter le configcrate aux Envoys

Un Envoy ne reçoit de configuration que lorsqu'une entrée d'un hostcrate correspond à son nom d'hôte. Le hostcrate le plus simple donne le configcrate à chaque Envoy :

```yaml
# /srv/vigo/stacks/envoys.vgo
envoys:
  - match: "*"
    configcrates: [motd]
```

Pour une affectation par hôte, placez des entrées plus précises au-dessus de l'entrée générique ; la première correspondance l'emporte. Dans cet exemple, `nginx` et `postgres` désignent des configcrates que vous avez écrits vous-même :

```yaml
# /srv/vigo/stacks/envoys.vgo
envoys:
  - match: "web*.prod"
    configcrates: [motd, nginx]
  - match: "db*.prod"
    configcrates: [motd, postgres]
  - match: "*"
    configcrates: [motd]
```

Lister des configcrates dans le `common.vgo` fourni ne les donne, à lui seul, à aucun Envoy.

## 6. Publier

```bash
sudo vigocli config publish
```

Valide l'arborescence, synchronise vers `/srv/vigo/.live/`, déclenche un rechargement du serveur. Si la validation échoue, la commande indique ce qui ne va pas et dans quel fichier. Ajoutez `--ask-ai` pour obtenir aussi des suggestions de correction de l'assistant IA, si vous en avez configuré un.

## 7. Attendre le prochain check-in

Les Envoys récupèrent la configuration publiée à leur prochain check-in. Avec le `checkin.interval` fourni par défaut de cinq minutes (plus jusqu'à 20 % de gigue), cela prend au plus six minutes environ.

## 8. Vérifier

```bash
sudo vigocli runs list --limit 5
```

Ou via l'interface Web à `https://localhost:8443/runs`. Vous devriez voir `motd` appliqué avec succès sur chaque Envoy enrôlé.

## Pour aller plus loin

- [Écrire votre premier vrai configcrate](howto/configcrates/write-your-first-configcrate.md) — paquets, services, dépendances, conditionnels
- [Configurer Scrier](howto/operate/set-up-scrier.md) — accès distant administrateur (SSH / RDP / VNC) sans ports entrants
- [Rapports de conformité](howto/compliance/compliance-reporting.md) — transformez les tags de configcrate en rapports de couverture HIPAA / SOC 2 / PCI / NIST
- [Le langage des configcrates](reference/configcrate-language.md) — le guide complet de rédaction de configcrates
- [Référence / vigocli](reference/vigocli/) — chaque commande CLI
- [Référence / ressources de l'agent](reference/vigo/resources/) — chaque type d'exécuteur
- [Référence / traits de l'agent](reference/vigo/traits/) — chaque collecteur de traits
