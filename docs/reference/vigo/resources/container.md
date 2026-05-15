
# container

Manages containers idempotently against a local container runtime (Docker today; Podman and containerd backends land as alternate runtimes when those are added). Pulls the image if needed, then creates, starts, stops, or removes the container.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `container` | Yes | -- | Container name. |
| `state` | No | `running` | Desired state: `running`, `stopped`, or `absent`. |
| `image` | Yes (for running) | -- | Image reference (e.g., `nginx:latest`). |
| `ports` | No | -- | Comma-separated port mappings (e.g., `8080:80,8443:443`). |
| `env` | No | -- | Comma-separated environment variables (e.g., `FOO=bar,BAZ=qux`). |
| `volumes` | No | -- | Comma-separated volume mounts (e.g., `/data:/var/lib/data,/logs:/var/log`). |
| `network` | No | -- | Container network to attach to. |
| `restart` | No | -- | Restart policy (e.g., `always`, `unless-stopped`, `on-failure`). |
| `command` | No | -- | Command to run in the container (overrides the image default). |
| `pull` | No | `missing` | Image pull policy: `missing` (pull only if not present), `always`, or `never`. |

## States

- **`running`** -- Ensure the container is running. If it does not exist, create and start it. If it exists but is stopped, start it. If it exists and is running with a different image, recreate it.
- **`stopped`** -- Ensure the container is stopped. If running, stop it.
- **`absent`** -- Remove the container. If running, stop it first.

## Idempotency

Uses `docker inspect` to check the container's current status and image. For `state: running`, if the container is running with the correct image, no action is taken. Image changes trigger a stop-remove-recreate cycle. Every daemon call has a 10-second timeout so a hung Docker daemon never stalls convergence.

## Examples

### Basic

```yaml
resources:
  - name: web
    type: container
    container: web
    image: nginx:latest
    ports: "80:80,443:443"
    restart: unless-stopped
```

### With volumes and environment

```yaml
resources:
  - name: postgres
    type: container
    container: postgres
    image: postgres:16
    ports: "5432:5432"
    env: "POSTGRES_PASSWORD=secret:db/password,POSTGRES_DB=myapp"
    volumes: "/opt/pgdata:/var/lib/postgresql/data"
    restart: always
```

### With depends_on

```yaml
resources:
  - name: app-network
    type: exec
    command: docker network create app-net
    unless: docker network inspect app-net

  - name: redis
    type: container
    container: redis
    image: redis:7
    network: app-net
    depends_on: app-network
```

### Stop a container

```yaml
resources:
  - name: maintenance-worker
    type: container
    container: maintenance-worker
    state: stopped
```

### Remove a container

```yaml
resources:
  - name: old-container
    type: container
    container: old-container
    state: absent
```

## Platform

Cross-platform. Requires Docker installed and the Docker daemon running. Declare the daemon explicitly:

```yaml
- name: docker-pkg
  type: package
  package: docker-ce
  state: present

- name: docker-svc
  type: service
  service: docker
  state: running
  enabled: true
  depends_on: docker-pkg
```

`container:` resources should `depends_on:` the service resource to guarantee the daemon is up before any container operation runs.

## Notes

- When `state: running` and the container exists with a different image, the executor stops and removes the old container before creating a new one with the updated image.
- The `pull` parameter controls when images are pulled. `missing` (default) pulls only when the image is not locally available; `always` re-pulls every convergence; `never` skips pull entirely (assumes the image is already local).
- Port, env, and volume parameters are comma-separated lists.
- Image removal is not part of this resource. To garbage-collect unused images, run `exec: docker image prune -f` on a schedule (a first-class `container_image_prune:` resource may land later).
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
