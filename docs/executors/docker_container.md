# docker_container

Manages Docker containers idempotently. Supports creating, starting, stopping, and removing containers with configurable ports, volumes, environment variables, and networks.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `container` | Yes | -- | Container name. Also accepts `name`. |
| `state` | No | `running` | Desired state: `running`, `stopped`, `present`, or `absent`. |
| `image` | Yes (for running/present) | -- | Docker image to use (e.g., `nginx:latest`). |
| `ports` | No | -- | Comma-separated port mappings (e.g., `8080:80,8443:443`). |
| `env` | No | -- | Comma-separated environment variables (e.g., `FOO=bar,BAZ=qux`). |
| `volumes` | No | -- | Comma-separated volume mounts (e.g., `/data:/var/lib/data,/logs:/var/log`). |
| `network` | No | -- | Docker network to connect to. |
| `restart` | No | -- | Restart policy (e.g., `always`, `unless-stopped`, `on-failure`). |
| `command` | No | -- | Command to run in the container (overrides the image default). |
| `pull` | No | `missing` | Image pull policy: `missing` (pull only if not present), `always`, or `never`. |

## States

- **`running`** -- Ensure the container is running. If it does not exist, create and start it. If it exists but is stopped, start it. If it exists and is running with a different image, recreate it.
- **`stopped`** -- Ensure the container is stopped. If running, stop it.
- **`present`** -- Ensure the container exists (created) but do not start it.
- **`absent`** -- Remove the container. If running, stop it first.

## Idempotency

Uses `docker inspect` to check the container's current status and image. For `state: running`, if the container is running with the correct image, no action is taken. Image changes trigger a stop-remove-recreate cycle.

## Examples

### Basic

```yaml
resources:
  - name: web
    type: docker_container
    container: web
    image: nginx:latest
    ports: "80:80,443:443"
    restart: unless-stopped
```

### With volumes and environment

```yaml
resources:
  - name: postgres
    type: docker_container
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
    type: docker_container
    container: redis
    image: redis:7
    network: app-net
    depends_on: app-network
```

### Stop a container

```yaml
resources:
  - name: maintenance-worker
    type: docker_container
    container: maintenance-worker
    state: stopped
```

### Remove a container

```yaml
resources:
  - name: old-container
    type: docker_container
    container: old-container
    state: absent
```

## Platform

Cross-platform. Requires Docker to be installed and the Docker daemon to be running.

## Notes

- When `state: running` and the container exists with a different image, the executor stops and removes the old container before creating a new one with the updated image.
- The `pull` parameter controls when images are pulled. `missing` (default) pulls only when the image is not locally available.
- Port, env, and volume parameters are comma-separated lists.
