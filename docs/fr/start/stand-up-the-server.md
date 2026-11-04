<!-- vigo-i18n source_sha:1c3e8dddc73dd6b805362a492b218e18d325e2c72c29091bcf495c1ec28e1003 -->

# 1 · Mettre le serveur en service

À la fin de cette page, vous aurez un serveur Vigo en fonctionnement, vous serez connecté à son interface web, et la machine du serveur elle-même sera enrôlée comme premier envoy. Comptez dix minutes, plus environ 20 pour la première compilation de l'image.

C'est le premier des quatre tutoriels — suivez-les dans l'ordre ; chacun s'appuie sur le précédent.

## Prérequis

- Un hôte Linux avec Docker (plugins Compose et Buildx), `git` et `rsync`, plus Go 1.25 si vous créez la clé de signature facultative.
- Les ports 8443, 1530 et 1531 accessibles depuis les machines que vous enrôlerez.
- Les droits root (ou `sudo`) sur cet hôte.
- C'est tout. Aucun service externe.

## 1. Le démarrer

Aucune image précompilée n'est encore publiée : compilez d'abord l'image du serveur depuis les sources. La compilation s'exécute dans Docker. L'hôte a besoin de Docker avec les plugins Compose et Buildx, ainsi que de `git` et `rsync` ; sous Ubuntu, `sudo apt-get install -y git rsync docker.io docker-compose-v2 docker-buildx` les installe. La première compilation prend environ 20 minutes. La clé de signature, facultative, permet aux envoys de mettre à jour leur agent d'eux-mêmes ; sans elle, chaque mise à niveau oblige à relancer le bootstrap sur chaque envoy. Sa création demande Go 1.25, et la clé doit être conservée en lieu sûr.

```bash
git clone https://github.com/daniel-alexander4/vigo.git && cd vigo
go run ./server/hardening/cmd/sign -genkey -output signing.key   # facultatif : mise à jour automatique des envoys
sudo scripts/upgrade.sh full --norun   # compile l'image du serveur et l'agent amd64
IMAGE=vigo:latest   # le tag que la compilation donne à l'image locale
```

Initialisez ensuite le répertoire de données et démarrez le serveur :

```bash
sudo mkdir -p /srv/vigo
sudo docker run --rm -v /srv/vigo:/srv/vigo "$IMAGE" --seed-only
cd /srv/vigo && sudo docker compose up -d
```

Le serveur écoute sur **`:1530`** (gRPC, pour les agents) et **`:8443`** (REST + interface web). Laissez-lui quelques secondes, puis vérifiez qu'il est en bonne santé :

```bash
docker compose ps
```

Vous verrez `vigo` et `vigo-guacd` tous deux en `Up (healthy)`. Tant que le serveur démarre, `vigo` affiche `Up (health: starting)` ; `curl -sk https://127.0.0.1:8443/healthz` affiche `ok` dès qu'il est prêt.

## 2. Se connecter

Ouvrez **`https://127.0.0.1:8443`** dans un navigateur et acceptez le certificat auto-signé. Il n'existe pas de connexion admin par défaut. Faites d'abord l'étape 3, puis créez votre admin à partir d'un véritable compte OS de cet hôte avec `sudo vigocli webusers create --username <yourname> --role admin` et `sudo vigocli webusers set-password --username <yourname>`, et connectez-vous en tant que `<yourname>`.

Vous arriverez sur le tableau de bord — vide pour l'instant, car aucun envoy n'existe encore.

## 3. Enrôler la machine du serveur elle-même

Chaque serveur Vigo exécute un agent local à ses côtés, enrôlé via la loopback. C'est la **première chose à faire après l'installation** — exécutez-la depuis l'hôte du serveur :

```bash
curl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh
```

> Utilisez le littéral IPv4 **`127.0.0.1`** — pas `localhost`, ni le nom d'hôte. L'agent inscrit cette adresse exacte dans sa configuration et la compose à chaque check-in, donc le chemin ne doit pas dépendre de la résolution de noms.

La politique d'enrôlement de confiance par défaut (CIDR privés + loopback) signe celui-ci automatiquement, sans token. Au bout d'un intervalle de check-in, **rafraîchissez le tableau de bord — un envoy apparaît.** C'est la machine du serveur elle-même, désormais gérée.

![Le tableau de bord Vigo — santé du parc, sécurité, conformité et risque en un coup d'œil](../img/screenshots/vigo.dashboard.png)

## Point de contrôle

Vous disposez de : un serveur en bonne santé, une session navigateur et un envoy en cours de convergence. Si le tableau de bord affiche toujours zéro envoy après une minute, voir [Installation](../howto/operate/install.md#troubleshooting).

**Suite :** [Enrôler votre premier envoy →](enroll-your-first-envoy.md) — une machine *distincte*.
