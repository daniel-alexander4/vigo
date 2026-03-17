# sshkey

Manages SSH known host key entries in a known hosts file.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `hostname` | Yes | -- | The hostname for the SSH key entry. |
| `state` | No | `present` | Desired state: `present` or `absent`. |
| `sshkey_type` | Yes (for present) | -- | Key type (e.g., `ssh-rsa`, `ssh-ed25519`, `ecdsa-sha2-nistp256`). |
| `sshkey` | Yes (for present) | -- | The public key data (base64-encoded). |
| `aliases` | No | -- | Comma-separated list of hostname aliases for the key entry. |
| `file` | No | `/etc/ssh/ssh_known_hosts` | Path to the known hosts file. |

## States

- **`present`** -- Ensure the host key entry exists with the correct key type and data.
- **`absent`** -- Remove any entry for the hostname.

## Idempotency

Reads the known hosts file, searches for an entry matching the hostname, and compares the key type and data. Only writes when there is a difference.

## Examples

### Basic

```yaml
resources:
  - name: github.com SSH key
    type: sshkey
    hostname: github.com
    sshkey_type: ssh-ed25519
    sshkey: AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
```

### With aliases

```yaml
resources:
  - name: internal git server
    type: sshkey
    hostname: git.internal
    sshkey_type: ssh-rsa
    sshkey: AAAAB3NzaC1yc2EAAA...
    aliases: "git.internal.example.com,10.0.1.50"
```

### Custom file

```yaml
resources:
  - name: deploy key
    type: sshkey
    hostname: deploy.internal
    sshkey_type: ssh-ed25519
    sshkey: AAAAC3NzaC1lZDI1NTE5...
    file: /home/deploy/.ssh/known_hosts
```

### Remove a key

```yaml
resources:
  - name: old-server key
    type: sshkey
    hostname: old-server.internal
    state: absent
```

## Platform

Cross-platform.

## Notes

- The default file is `/etc/ssh/ssh_known_hosts` (system-wide known hosts).
- The `aliases` parameter adds additional hostnames to the same key entry line.
