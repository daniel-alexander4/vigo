<!-- vigo-i18n source_sha:7896a6d17b1cc44356748f370a4903ccda7a2438695670b4f6dd573f450c900d -->

# 1 · Mettre le serveur en service

À la fin de cette page, vous aurez un serveur Vigo en fonctionnement, vous serez connecté à son interface web, et la machine du serveur elle-même sera enrôlée comme premier envoy. Comptez dix minutes.

C'est le premier des quatre tutoriels — suivez-les dans l'ordre ; chacun s'appuie sur le précédent.

## Prérequis

- Un hôte Linux avec Docker + Docker Compose.
- Les droits root (ou `sudo`) sur cet hôte.
- C'est tout. Aucun service externe.

## 1. Le démarrer

Depuis un checkout du bundle serveur :

```bash
make up        # docker compose up -d (vigosrv + guacd)
```

Le serveur écoute sur **`:1530`** (gRPC, pour les agents) et **`:8443`** (REST + interface web). Laissez-lui quelques secondes, puis vérifiez qu'il est en bonne santé :

```bash
docker compose ps
```

Vous verrez le conteneur `vigo` en `Up` et `vigo-guacd` en `Up (healthy)`.

## 2. Se connecter

Ouvrez **`https://127.0.0.1:8443`** dans un navigateur et acceptez le certificat auto-signé. Connectez-vous avec les identifiants admin de bootstrap affichés dans les journaux du serveur (`docker compose logs vigo | grep -i admin`).

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
