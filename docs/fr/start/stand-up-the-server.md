<!-- vigo-i18n source_sha:d07fc5f5063b590c0185114604637fcfc659eeabab7ab8d3492164cf8a2cb14f -->

# 1 · Mettre le serveur en service

À la fin de cette page, vous aurez un serveur Vigo en fonctionnement, vous serez connecté à son interface web, et la machine du serveur elle-même sera enrôlée comme premier envoy. Comptez dix minutes.

C'est le premier des quatre tutoriels — suivez-les dans l'ordre ; chacun s'appuie sur le précédent.

## Prérequis

- Un hôte Linux avec Docker + Docker Compose, Go 1.25 et une chaîne d'outils Rust.
- Les droits root (ou `sudo`) sur cet hôte.
- C'est tout. Aucun service externe.

## 1. Le démarrer

Aucune image précompilée n'est encore publiée : compilez d'abord l'image du serveur depuis les sources. Il vous faut Docker, Go 1.25 et une chaîne d'outils Rust.

```bash
git clone https://github.com/daniel-alexander4/vigo.git && cd vigo
sudo scripts/upgrade.sh full --norun   # compile l'image du serveur et l'agent amd64
IMAGE=us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest   # le tag que la compilation donne à l'image locale
```

Initialisez ensuite le répertoire de données et démarrez le serveur :

```bash
sudo mkdir -p /srv/vigo
docker run --rm -v /srv/vigo:/srv/vigo "$IMAGE" --seed-only
cd /srv/vigo && docker compose up -d
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
