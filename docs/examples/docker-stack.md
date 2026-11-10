
# Docker Stack Management

This example uses the `container` [executor](../reference/vigo/resources/_index.md) to manage containerized services. The `container` executor pulls images on demand — no separate image-pull resource is needed.

## Configcrate Definition

`stacks/configcrates/redis.vgo`:

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

## Multi-Container Stack

`stacks/configcrates/app-stack.vgo`:

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

## Role Definition

`stacks/roles/docker-app.vgo`:

```yaml
name: docker-app
configcrates:
  - redis
  - app-stack
```

## Node Assignment

`stacks/envoys/envoys.vgo`:

```yaml
envoys:
  - match: "*.app.example.com"
    environment: production
    roles: [docker-app]
    vars:
      app_tag: "v2.1.0"
      redis_maxmemory: 512mb
```

## How It Works

1. The `redis` configcrate is applied first because `app-stack` declares `depends_on: [redis]`.
2. Each `container` resource pulls its image (if missing) and creates + starts the container in one idempotent step.
3. If the image tag changes (e.g., deploying a new version), the container is recreated with the new image.

## Rolling Deployments

To deploy a new version across your fleet in batches:

```bash
# Update the app_tag in envoys.vgo, then publish; each envoy picks it
# up at its next check-in:
vigocli config publish
```

Or use a task for more control:

```bash
vigocli task run \
  "docker pull myregistry.example.com/webapp:v2.2.0" \
  --target "*.app.example.com" \
  --batch-size 25% \
  --health-check "curl -sf http://localhost:8080/health"
```

## Already have a `docker-compose.yml`?

Vigo's preferred shape is one `container:` resource per service, joined with `depends_on` and (when the pod primitive lands) namespace-sharing. For operators with a maintained `docker-compose.yml` they don't want to rewrite, the migration recipe is a `file:` resource for the compose file plus an `exec:` wrapper guarded by a service-state check:

`stacks/configcrates/compose-app.vgo`:

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

The `unless:` guard converges on "all services running" the way the retired `docker_compose:` executor did, with no Vigo-specific glue. A full migration to native `container:` resources (one per service) is the long-term shape — see the [`container`](../reference/vigo/resources/container.md) reference.

## Container Lifecycle

The `container` executor is idempotent:

- If the container does not exist, the image is pulled (if `pull: missing` or `always`) and the container is created and started
- If the container exists with the wrong image, it is stopped, removed, and recreated
- If the container exists and is stopped, it is started
- If the container exists and matches the desired state, no action is taken
