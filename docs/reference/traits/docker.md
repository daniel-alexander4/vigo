
# Docker

Reports Docker installation status, version, running containers, and engine configuration.

## Trait Path
`docker`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `docker.installed` | boolean | `true` | Whether Docker is installed |
| `docker.version` | string | `"24.0.7"` | Docker server version (empty if not installed) |
| `docker.running_count` | integer | `3` | Number of currently running containers |
| `docker.running` | array of strings | `["nginx", "redis", "app"]` | Names of running containers |
| `docker.storage_driver` | string | `"overlay2"` | Docker storage driver |
| `docker.cgroup_driver` | string | `"systemd"` | Docker cgroup driver |

## Collection Method
First checks if `docker` is available via `which`. If not installed, returns defaults with `installed: false`. Otherwise runs three Docker commands:
- `docker version --format '{{.Server.Version}}'` for the version
- `docker info --format '{{.Driver}}'` for the storage driver
- `docker info --format '{{.CgroupDriver}}'` for the cgroup driver
- `docker ps --format '{{.Names}}'` for running container names

## Using in When Expressions
```yaml
- name: configure-docker-daemon
  type: file
  target_path: /etc/docker/daemon.json
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: docker-report
  type: file
  target_path: /var/lib/vigo/docker.txt
  content: |
    Docker {{ .Traits.docker.version }}
    Running containers: {{ .Traits.docker.running_count }}
    Storage: {{ .Traits.docker.storage_driver }}
```

## Platform
Linux
