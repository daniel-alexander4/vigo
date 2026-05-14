
# docker_compose

Manages Docker Compose stacks idempotently. Brings multi-container applications up or down using a compose file.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `compose_file` | Yes | -- | Path to the docker-compose.yml file. |
| `state` | No | `up` | Desired state: `up` or `down`. |
| `project` | No | -- | Override the Compose project name. |
| `services` | No | -- | Comma-separated list of specific services to manage. Defaults to all. |
| `build` | No | `false` | Rebuild images before starting (`--build`). |
| `pull` | No | `missing` | Image pull policy: `always`, `missing`, or `never`. |
| `remove_orphans` | No | `false` | Remove containers for services not defined in the compose file. |
| `volumes` | No | `false` | Remove named volumes when `state: down` (`--volumes`). |

## States

- **`up`** -- Ensure all services are running. If any service is not running, runs `docker compose up -d`.
- **`down`** -- Tear down the stack. If any services exist, runs `docker compose down`.

## Idempotency

Uses `docker compose ps` to check whether all services are in the `running` state. For `state: up`, if all services are already running, no action is taken. For `state: down`, if no services exist, no action is taken.

To trigger a re-up when the compose file changes, use `notify`/`subscribes` with a file resource that manages the compose file content.

## Examples

### Basic

```yaml
resources:
  - name: app-stack
    type: docker_compose
    compose_file: /opt/myapp/docker-compose.yml
```

### With build and pull

```yaml
resources:
  - name: app-stack
    type: docker_compose
    compose_file: /opt/myapp/docker-compose.yml
    build: "true"
    pull: always
    remove_orphans: "true"
```

### Specific services

```yaml
resources:
  - name: backend-services
    type: docker_compose
    compose_file: /opt/myapp/docker-compose.yml
    services: api,worker,redis
```

### With depends_on on compose file

```yaml
resources:
  - name: compose-config
    type: file
    target_path: /opt/myapp/docker-compose.yml
    content: |
      services:
        web:
          image: nginx:latest
          ports:
            - "80:80"
    notify: app-stack

  - name: app-stack
    type: docker_compose
    compose_file: /opt/myapp/docker-compose.yml
    subscribes: compose-config
```

### Tear down a stack

```yaml
resources:
  - name: old-stack
    type: docker_compose
    compose_file: /opt/old-app/docker-compose.yml
    state: down
    volumes: "true"
```

## Platform

Cross-platform. Requires Docker with the Compose plugin (`docker compose`) to be installed.

## Notes

- Uses `docker compose` (V2 plugin syntax), not the legacy `docker-compose` standalone binary.
- The `build` parameter triggers `--build` which rebuilds images from Dockerfiles before starting.
- When `state: up` and services are already running, `docker compose up` is not re-run. Use `notify`/`subscribes` to force re-up on config changes.
- The `volumes` parameter only applies to `state: down` and removes named volumes declared in the compose file.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
