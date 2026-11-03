
# Host Containers

Per-container state on the local Docker daemon — one row per container, regardless of state (running / exited / restarting / paused / created). Pairs with the [`docker`](docker.md) collector, which carries daemon-level metadata (version, drivers, running count). This collector adds the per-container detail needed by the [`container:`](../resources/container.md) resource type and by fleet queries like "which envoys have an unhealthy redis."

## Trait Path
`host_containers` — a JSON array of objects.

## Fields per container

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `host_containers[i].name` | string | `redis-cache` | Container name (Docker's leading `/` is stripped). |
| `host_containers[i].image` | string | `redis:7.2-alpine` | Image reference the container was created with. |
| `host_containers[i].image_id` | string | `sha256:abc123…` | The image's sha256 ID — `image_id` changes when an upstream tag moves; `image` does not. |
| `host_containers[i].state` | string | `running` | Docker's `State.Status` (`created`, `restarting`, `running`, `removing`, `paused`, `exited`, `dead`). |
| `host_containers[i].health` | string | `healthy` | Docker's `State.Health.Status` if a healthcheck is configured; empty string otherwise. |
| `host_containers[i].restart_count` | integer | `0` | Cumulative restart count. Non-zero on a running container = it crashed at least once. |
| `host_containers[i].oom_killed` | boolean | `false` | Whether the most-recent stop was an OOM kill. |

## Collection Method

Two daemon calls per cycle:

1. `docker ps -a -q --no-trunc` to enumerate all container IDs.
2. One `docker inspect` call passing every ID, using a tab-separated `--format` template to pull the seven fields above in a single round-trip.

Returns `[]` when docker is missing or the daemon is unreachable. The collector dispatcher's 5-second timeout protects against a hung daemon.

## Why this matters

The `container:` resource type creates and removes containers; this trait reports on them. Together they enable:

- "Is the redis container actually running on every envoy that should have it?" — fleet query against `host_containers[*].name`.
- "Are any envoys' containers reporting unhealthy?" — `host_containers[*].health == "unhealthy"`.
- "Which envoys had a container OOM-killed recently?" — `host_containers[*].oom_killed == true`.
- "Which envoys are still on the old image after a rolling update?" — group `host_containers[*].image_id` by node.

## Using in When Expressions

```yaml
- name: page-on-restart-loop
  type: exec
  command: /usr/local/bin/alert container-restart-loop
  when: 'host_containers | any(.restart_count > 5 && .state == "running")'
```

## Using in Templates

```yaml
- name: container-inventory
  type: file
  target_path: /var/lib/vigo/containers.txt
  content: |
    {{- range .Traits.host_containers }}
    {{ .name }}  {{ .image }}  {{ .state }}  {{ .health }}  restarts={{ .restart_count }}
    {{- end }}
```

## Platform

Linux + macOS. Other platforms return `null` for the entire trait.
