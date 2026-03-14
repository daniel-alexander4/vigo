# Docker Stack Management

This example uses the `docker_image` and `docker_container` [executors](../executors/_index.md) to manage containerized services.

## Module Definition

`thrall-configs/modules/redis.hnb`:

```yaml
name: redis
vars:
  redis_version: "7-alpine"
  redis_port: "6379"
  redis_maxmemory: 256mb
resources:
  - name: redis-image
    type: docker_image
    image: redis
    tag: "{{ .Vars.redis_version }}"
    state: present

  - name: redis-container
    type: docker_container
    container: redis
    image: "redis:{{ .Vars.redis_version }}"
    state: running
    ports:
      - "{{ .Vars.redis_port }}:6379"
    command: "redis-server --maxmemory {{ .Vars.redis_maxmemory }}"
    restart_policy: unless-stopped
```

## Multi-Container Stack

`thrall-configs/modules/app-stack.hnb`:

```yaml
name: app-stack
depends_on:
  - redis
vars:
  app_image: myregistry.example.com/webapp
  app_tag: latest
  app_replicas: "1"
resources:
  - name: app-image
    type: docker_image
    image: "{{ .Vars.app_image }}"
    tag: "{{ .Vars.app_tag }}"
    state: present

  - name: app-container
    type: docker_container
    container: webapp
    image: "{{ .Vars.app_image }}:{{ .Vars.app_tag }}"
    state: running
    ports:
      - "8080:8080"
    environment:
      REDIS_URL: "redis://localhost:{{ .Vars.redis_port }}"
      DATABASE_URL: "secret:hobnob/webapp/database_url"
    restart_policy: unless-stopped
    depends_on:
      - redis-container
```

## Role Definition

`thrall-configs/roles/docker-app.hnb`:

```yaml
name: docker-app
modules:
  - redis
  - app-stack
```

## Node Assignment

`thrall-configs/nodes.hnb`:

```yaml
thralls:
  - match: "*.app.example.com"
    environment: production
    roles: [docker-app]
    vars:
      app_tag: "v2.1.0"
      redis_maxmemory: 512mb
```

## How It Works

1. The `redis` module is applied first because `app-stack` declares `depends_on: [redis]`.
2. Within each module, the `docker_image` resource ensures the image is pulled.
3. The `docker_container` resource ensures the container is running with the specified configuration.
4. If the image tag changes (e.g., deploying a new version), the container is recreated with the new image.

## Rolling Deployments

To deploy a new version across your fleet in batches:

```bash
# Update the app_tag in nodes.hnb, then:
hobnobcli push --target "*.app.example.com"
```

Or use a task for more control:

```bash
hobnobcli task run \
  --command "docker pull myregistry.example.com/webapp:v2.2.0" \
  --target "*.app.example.com" \
  --batch-size 25% \
  --health-check "curl -sf http://localhost:8080/health"
```

## Container Lifecycle

The `docker_container` executor is idempotent:

- If the container does not exist, it is created and started
- If the container exists with the wrong configuration (image, ports, env), it is recreated
- If the container exists and is stopped, it is started
- If the container exists and matches the desired state, no action is taken
