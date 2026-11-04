<!-- vigo-i18n source_sha:32f56b39ee41792ac45eeddd803fad9f76248e7fc54f74c7c33906cbd4a46e09 -->

# Démarrage rapide

Mettez en route le serveur Vigo, enrôlez votre premier Envoy et appliquez votre premier configcrate. Une dizaine de minutes de copier-coller une fois l'image compilée. Si vous préférez être guidé pas à pas, suivez plutôt les [tutoriels](start/) — même parcours, plus d'explications.

## 1. Démarrer le serveur

Aucune image précompilée n'est encore publiée : compilez d'abord l'image du serveur depuis les sources. La compilation s'exécute dans Docker : Docker avec le plugin Compose suffit, et la première compilation prend environ 20 minutes. La clé de signature, facultative, permet aux envoys de mettre à jour leur agent d'eux-mêmes ; sans elle, chaque mise à niveau oblige à relancer le bootstrap sur chaque envoy. Sa création demande Go 1.25, et la clé doit être conservée en lieu sûr.

```bash
git clone https://github.com/daniel-alexander4/vigo.git && cd vigo
go run ./server/hardening/cmd/sign -genkey -output signing.key   # facultatif : mise à jour automatique des envoys
sudo scripts/upgrade.sh full --norun   # compile l'image du serveur et l'agent amd64
IMAGE=vigo:latest   # le tag que la compilation donne à l'image locale
```

```bash
sudo mkdir -p /srv/vigo
sudo docker run --rm -v /srv/vigo:/srv/vigo "$IMAGE" --seed-only
cd /srv/vigo
sudo docker compose up -d
```

`--seed-only` écrit `/srv/vigo/docker-compose.yml`, `server.yaml`, `.env` et des configurations d'exemple, puis se termine. `docker compose up -d` démarre le serveur, qui génère son autorité de certification et ses certificats TLS sous `/srv/vigo/tls/` au premier démarrage. REST + interface Web sur `https://localhost:8443`, gRPC sur `localhost:1530`.

`docker run` unique si vous ne voulez pas de compose :

```bash
sudo mkdir -p /srv/vigo
docker run -d --name vigo \
  --network host \
  -v /srv/vigo:/srv/vigo \
  "$IMAGE"
```

`--network host` lie 8443 (REST/UI), 1530 (gRPC) et 1531 (Swarm) directement sur l'hôte — correspondant au compose généré, qui place le serveur sur le réseau de l'hôte afin que le trafic des agents ne passe pas par le proxy userland par port de Docker. (Le sidecar `guacd` de Scrier n'est pas démarré par ce `docker run` nu ; utilisez compose si vous avez besoin de sessions Scrier RDP/VNC.)

Ouvrez `https://localhost:8443` et acceptez le certificat auto-signé. Il n'existe pas de connexion admin par défaut : créez la vôtre après l'étape suivante, comme décrit dans [Installer](howto/operate/install.md#2-démarrer-le-serveur).

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

Après l'enrôlement, chaque Envoy apparaît dans le tableau de bord à `https://localhost:8443/envoys` et commence à faire un check-in toutes les 5 minutes.

## 3. Écrire un configcrate

```yaml
# /srv/vigo/stacks/configcrates/motd.vgo
name: motd
resources:
  - name: motd-file
    type: file
    target_path: /etc/motd
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

## 4. Affecter le configcrate aux Envoys

L'affectation la plus simple — chaque Envoy le reçoit :

```yaml
# /srv/vigo/stacks/common.vgo
configcrates: [motd]
```

Pour une affectation par hôte, utilisez un hostcrate :

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

## 5. Publier

```bash
sudo vigocli config publish
```

Valide l'arborescence, synchronise vers `/srv/vigo/.live/`, déclenche un rechargement du serveur. Si la validation échoue, l'assistant IA intégré affiche des suggestions de correction dans votre terminal.

## 6. Forcer un check-in immédiat (n'attendez pas 5 minutes)

```bash
sudo vigocli envoys push --all
```

## 7. Vérifier

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
