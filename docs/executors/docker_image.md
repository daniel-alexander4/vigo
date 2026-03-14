# docker_image

Manages Docker images -- pulls, checks for, and removes images.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `image` | Yes | -- | Image name (with or without tag). Also accepts `name`. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `tag` | No | `latest` | Image tag. Only used when the `image` parameter does not include a tag. |
| `force` | No | `false` | If `true`, pull the image even if it already exists locally (useful for updating `latest` tags). |

## States

- **`present`** -- Ensure the image exists locally. If not, pull it.
- **`absent`** -- Remove the image.

## Idempotency

Uses `docker image inspect` to check if the image exists locally. For `state: present` without `force`, if the image exists, no action is taken. With `force: true`, the image is always re-pulled.

## Examples

### Basic

```yaml
resources:
  - name: Pull nginx
    type: docker_image
    image: nginx:latest
```

### With auto-tagged image

```yaml
resources:
  - name: Pull redis
    type: docker_image
    image: redis
    tag: "7-alpine"
```

### Force re-pull

```yaml
resources:
  - name: Update app image
    type: docker_image
    image: registry.internal/myapp:latest
    force: "true"
```

### Remove an image

```yaml
resources:
  - name: Remove old image
    type: docker_image
    image: old-app:v1
    state: absent
```

## Platform

Cross-platform. Requires Docker to be installed.

## Notes

- When the image name does not include a `:`, the `tag` parameter is appended (default: `latest`). For example, `image: nginx` becomes `nginx:latest`.
- The `force` parameter is useful for mutable tags like `latest` where you want to ensure the newest version is pulled.
- Image removal uses `docker rmi`.
