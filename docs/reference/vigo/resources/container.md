
# container

Manages containers idempotently against a local container runtime. Docker today; Podman and containerd land as alternate runtimes when those backends are added. Pulls the image if needed, then creates, starts, stops, or removes the container. Waits for healthcheck to report healthy before reporting the resource as settled.

## Parameters

### Required

| Parameter | Description |
|-----------|-------------|
| `container` | Container name. Must be unique per host (Docker constraint). |
| `image` *or* `artifact` | Image source. `image:` is a normal registry reference (e.g., `nginx:latest` or `myregistry.example.com/app:v1`). `artifact:` references a [curator](../../../howto/swarm/set-up-curator.md) artifact and routes the pull through the local registry shim. Mutually exclusive. Required for `state: running`. |

### Curator-backed images

For `artifact:`, the operator must opt the envoy into the curator registry shim by setting `swarm.curator.local_port` to a non-zero value in `server.yaml`. `artifact:` accepts the same value forms as the `artifact:` param on `nonrepo_package` and `source_package`:

| Form | Example |
|------|---------|
| Bare `<name>:<tag>` (implicit puddle namespace) | `redis:7.2-alpine` |
| Bare `<name>@<version>` | `redis@1.4` |
| `<puddle-name>/<name>:<tag>` | `alexander4/redis:7.2-alpine` |
| `<hex-artifact-id>/<name>@<version>` | `e3810a25.../redis@1.4` |

The agent translates the artifact ref into a docker-pull URL (`127.0.0.1:<port>/<repo>:<tag>`) and lets the daemon pull through the shim. Docker only speaks `:tag` on the wire, so `@version` separators collapse to `:` — the shim resolves either form identically.

### Lifecycle

| Parameter | Default | Description |
|-----------|---------|-------------|
| `state` | `running` | Desired state: `running`, `stopped`, or `absent`. |
| `pull` | `missing` | Image pull policy: `missing` (pull only if not present), `always`, or `never`. |
| `command` | -- | Command to run in the container (overrides the image default). Space-separated tokens. |

### Networking

| Parameter | Description |
|-----------|-------------|
| `ports` | Comma-separated port mappings (e.g., `8080:80,8443:443` or `127.0.0.1:9121:9121`). |
| `network` | Container network to attach to. |

### Storage

| Parameter | Description |
|-----------|-------------|
| `volumes` | Comma-separated volume mounts (e.g., `/data:/var/lib/data,/logs:/var/log:ro`). |
| `tmpfs` | Comma-separated tmpfs mount paths inside the container (e.g., `/tmp,/run`). |
| `read_only` | `"true"` makes the root filesystem read-only. |

### Identity & runtime config

| Parameter | Description |
|-----------|-------------|
| `env` | Comma-separated environment variables (e.g., `FOO=bar,BAZ=qux`). |
| `user` | UID or `UID:GID` to run as (e.g., `999:999`). |
| `cap_drop` | Comma-separated capabilities to drop (e.g., `ALL`). |
| `cap_add` | Comma-separated capabilities to add (e.g., `SETUID,SETGID`). |
| `labels` | Comma-separated `key=value` labels. |
| `restart` | Restart policy (`no`, `on-failure`, `always`, `unless-stopped`). |

### Logging

| Parameter | Description |
|-----------|-------------|
| `log_driver` | Log driver name (e.g., `json-file`, `journald`). |
| `log_options` | Comma-separated `key=value` log-driver options (e.g., `max-size=10m,max-file=5`). |

### Healthcheck

| Parameter | Description |
|-----------|-------------|
| `healthcheck_command` | Command to run for the healthcheck. Setting this enables the post-start health wait. |
| `healthcheck_interval` | Time between checks (e.g., `10s`, `1m`). Defaults to Docker's `30s`. |
| `healthcheck_timeout` | Maximum time a single check may take. Defaults to Docker's `30s`. |
| `healthcheck_retries` | Consecutive failures before the container is marked unhealthy. Defaults to Docker's `3`. |
| `healthcheck_start_period` | Grace period before failures count. Defaults to Docker's `0s`. |

When `healthcheck_command` is set and `state: running`, the agent waits up to 2 minutes for the container to report `healthy` before returning. An `unhealthy` status returns an error.

## States

- **`running`** -- Ensure the container is running. If it does not exist, create and start it. If it exists but is stopped, start it. If it exists and is running but its spec has drifted (image or any other managed setting -- ports, env, volumes, restart policy, etc.), recreate it.
- **`stopped`** -- Ensure the container is stopped. If running, stop it.
- **`absent`** -- Remove the container. If running, stop it first.

## Idempotency

At create time the agent stamps a `vigo.spec-hash` label -- a fingerprint of the full desired spec (image plus every create setting) -- onto the container. On each reconcile it reads that label back via `docker inspect`; for `state: running`, if the running container's fingerprint matches the desired spec, no action is taken, and any drift (a changed image, port, env var, volume, restart policy, and so on) triggers a stop-remove-recreate cycle. A container created before this label existed is recreated once to adopt the fingerprint. Daemon calls have a 10-second timeout and image pulls a 5-minute timeout, so a hung Docker daemon or a slow registry never stalls convergence; a failed pull surfaces as an error rather than silently using a stale local image.

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

### Curator-backed image with healthcheck

```yaml
resources:
  - name: redis-cache
    type: container
    container: redis-cache
    artifact: alexander4/redis:7-alpine    # pulled via the curator shim
    state: running
    ports: "6379:6379"
    volumes: "/var/lib/redis:/data"
    healthcheck_command: "redis-cli ping"
    healthcheck_interval: 10s
    restart: unless-stopped
```

### Hardened workload with healthcheck

```yaml
resources:
  - name: redis-cache
    type: container
    container: redis-cache
    image: redis:7-alpine
    state: running
    ports: "6379:6379"
    volumes: "/var/lib/redis:/data"
    user: "999:999"
    cap_drop: "ALL"
    cap_add: "SETUID,SETGID"
    read_only: "true"
    tmpfs: "/tmp,/run"
    log_driver: json-file
    log_options: "max-size=10m,max-file=5"
    labels: "vigo.module=redis"
    healthcheck_command: "redis-cli ping"
    healthcheck_interval: 10s
    healthcheck_retries: 3
    healthcheck_start_period: 5s
    restart: unless-stopped
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
- `secret:` references inside `volumes:` are rejected explicitly — that pattern looks like it should bind-mount the decrypted secret value as a file, but no such behavior exists yet. Use environment variables for secret values, or write the secret to a file with a separate `file:` resource and mount that file in.
- The `pull` parameter controls when images are pulled. `missing` (default) pulls only when the image is not locally available; `always` re-pulls every convergence; `never` skips pull entirely (assumes the image is already local).
- Port, env, volume, tmpfs, cap_drop, cap_add, labels, and log_options parameters are comma-separated lists.
- Image removal is not part of this resource. To garbage-collect unused images, run `exec: docker image prune -f` on a schedule (a first-class `container_image_prune:` resource may land later).
