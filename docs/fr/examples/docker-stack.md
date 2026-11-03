<!-- vigo-i18n source_sha:11cb82200674aaf072471a3957a9d37b602ef44881794828d8f1873b229c3097 -->

# Gestion d'une stack Docker

Cet exemple utilise l'[exécuteur](../reference/vigo/resources/_index.md) `container` pour gérer des services conteneurisés. L'exécuteur `container` télécharge les images à la demande — aucune ressource distincte de téléchargement d'image n'est nécessaire.

## Définition du configcrate

`stacks/configcrates/redis.vgo` :

```yaml
name: redis
vars:
  redis_version: "7-alpine"
  redis_port: "6379"
  redis_maxmemory: 256mb
resources:
  - name: redis-container
    type: container
    container: redis
    image: "redis:{{ .Vars.redis_version }}"
    state: running
    ports: "{{ .Vars.redis_port }}:6379"
    command: "redis-server --maxmemory {{ .Vars.redis_maxmemory }}"
    restart: unless-stopped
```

## Stack multi-conteneurs

`stacks/configcrates/app-stack.vgo` :

```yaml
name: app-stack
depends_on:
  - redis
vars:
  app_image: myregistry.example.com/webapp
  app_tag: latest
  app_replicas: "1"
resources:
  - name: app-container
    type: container
    container: webapp
    image: "{{ .Vars.app_image }}:{{ .Vars.app_tag }}"
    state: running
    ports: "8080:8080"
    env: "REDIS_URL=redis://localhost:{{ .Vars.redis_port }},DATABASE_URL=secret:vigo/webapp/database_url"
    restart: unless-stopped
    depends_on:
      - redis-container
```

## Définition du rôle

`stacks/roles/docker-app.vgo` :

```yaml
name: docker-app
configcrates:
  - redis
  - app-stack
```

## Affectation des nœuds

`stacks/envoys/envoys.vgo` :

```yaml
envoys:
  - match: "*.app.example.com"
    environment: production
    roles: [docker-app]
    vars:
      app_tag: "v2.1.0"
      redis_maxmemory: 512mb
```

## Fonctionnement

1. Le configcrate `redis` est appliqué en premier, car `app-stack` déclare `depends_on: [redis]`.
2. Chaque ressource `container` télécharge son image (si elle est absente) puis crée et démarre le conteneur en une seule étape idempotente.
3. Si le tag de l'image change (par exemple lors du déploiement d'une nouvelle version), le conteneur est recréé avec la nouvelle image.

## Déploiements progressifs

Pour déployer une nouvelle version par lots sur l'ensemble de votre flotte :

```bash
# Update the app_tag in envoys.vgo, then:
vigocli envoys push --all
```

Ou utilisez une tâche pour plus de contrôle :

```bash
vigocli task run \
  "docker pull myregistry.example.com/webapp:v2.2.0" \
  --target "*.app.example.com" \
  --batch-size 25% \
  --health-check "curl -sf http://localhost:8080/health"
```

## Vous avez déjà un `docker-compose.yml` ?

La forme privilégiée par Vigo est une ressource `container:` par service, reliées par `depends_on` et (quand la primitive pod sera disponible) le partage de namespace. Pour les opérateurs disposant d'un `docker-compose.yml` maintenu qu'ils ne souhaitent pas réécrire, la recette de migration consiste en une ressource `file:` pour le fichier compose plus un wrapper `exec:` protégé par une vérification de l'état du service :

`stacks/configcrates/compose-app.vgo` :

```yaml
name: compose-app
resources:
  - name: compose-file
    type: file
    target_path: /opt/myapp/docker-compose.yml
    content: |
      services:
        web:
          image: nginx:latest
          ports:
            - "80:80"
        api:
          image: myregistry.example.com/api:v2.1.0
          ports:
            - "8080:8080"
        redis:
          image: redis:7-alpine
    owner: root
    group: root
    mode: "0644"
    notify: app-stack

  - name: app-stack
    type: exec
    command: "docker compose -f /opt/myapp/docker-compose.yml up -d --remove-orphans --pull always"
    # Idempotency: re-up only when at least one service is not running, or
    # when the compose file changed (notify path).
    unless: "docker compose -f /opt/myapp/docker-compose.yml ps --format '{{.State}}' | grep -qv running"
    subscribes: compose-file
```

Le garde `unless:` converge vers « tous les services en cours d'exécution » comme le faisait l'exécuteur `docker_compose:` retiré, sans aucune glu spécifique à Vigo. Une migration complète vers des ressources `container:` natives (une par service) est la forme visée à long terme — voir la référence [`container`](../reference/vigo/resources/container.md).

## Cycle de vie du conteneur

L'exécuteur `container` est idempotent :

- Si le conteneur n'existe pas, l'image est téléchargée (si `pull: missing` ou `always`) puis le conteneur est créé et démarré
- Si le conteneur existe avec la mauvaise image, il est arrêté, supprimé et recréé
- Si le conteneur existe et est arrêté, il est démarré
- Si le conteneur existe et correspond à l'état désiré, aucune action n'est entreprise
