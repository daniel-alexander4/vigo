
# Secrets

Vigo never stores secrets in config files, environment variables, logs, or database records. All secret values use the `secret:` prefix and are resolved through a pluggable backend at runtime.

## The secret: Prefix

Any config value starting with `secret:` is resolved through the secrets provider:

```yaml
database:
  dsn: "secret:vigo/db/dsn"

smtp:
  password: "secret:vigo/smtp/password"
```

At startup, the server resolves `secret:vigo/db/dsn` by calling `provider.Get("vigo/db/dsn")`. The actual secret value is never written to disk in plaintext.

## Two Backends

### local (default, recommended)

Secrets are stored as AES-256-GCM encrypted files on disk. The master key is a 32-byte hex key stored in a 0600-permission file.

```yaml
secrets:
  backend: local
  key_file: "/srv/vigo/master.key"
  secrets_dir: "/srv/vigo/secrets"
```

Secret path mapping: `secret:vigo/db/dsn` → file at `secrets_dir/vigo/db/dsn`.

On first start, the local backend auto-generates:
- The master key (if `key_file` doesn't exist)
- TLS certificates (self-signed CA + server cert)
- A random database DSN

### isopass (production)

Resolves secrets from an external isopass API server. Supports versioning for rotation detection.

```yaml
secrets:
  backend: isopass
  url: "https://isopass.internal:8443"
  token_file: "/srv/vigo/isopass-token"   # 0600, contains bearer token
```

The `token_file` is read at startup. The file should be owned by root with mode `0600`.

## Secret Resolution Flow

![Secret Resolution Flow](img/secret-resolution.svg)

## Fail-Fast

If the secrets provider is unreachable or a secret path doesn't exist, the server **refuses to start**. It never falls back to insecure defaults.


```
$ vigosrv
ERROR server failed error="loading server config: resolving secret 'vigo/db/dsn': file not found"
```

## Secret Rotation

### Local backend

When you change a secret with the CLI, rotation is automatic:

```bash
vigocli secrets set vigo/grafana/admin-password "newpass"
  Secret "vigo/grafana/admin-password" stored.
  Server notified — affected envoys will apply on next check-in.
```

The CLI writes the encrypted file and notifies the server. The server:
1. Reloads config to resolve the new value
2. Finds envoys whose configs reference the secret
3. Queues `_secret_triggered=true` for resources with `watch_secret: ["path"]`
4. Sets force-push on affected envoys

On the next check-in, watch_secret resources re-execute with the new value.

### Isopass backend

The server polls isopass for version changes:

```yaml
watcher:
  enabled: true
  poll_interval: "5m"
```

When a version change is detected, the same rotation flow triggers automatically.

## Secrets in Modules

Modules reference secrets via vars:

```yaml
# envoys.vgo
envoys:
  - match: "db*.example.com"
    roles: [database]
    vars:
      db_password: "secret:vigo/db/password"
```

The `content:` template renders the resolved value:

```yaml
# modules/database.vgo
resources:
  - name: pg-config
    type: file
    target_path: /etc/postgresql/conf.d/auth.conf
    content: |
      password = {{ .Vars.db_password }}
    watch_secret: ["vigo/db/password"]
    notify: [pg-service]
```

## Exec Secret Safety

Never embed secrets in command strings (they appear in process listings). Use `stdin` or `secret_file`:

```yaml
# GOOD — pipe to stdin, only runs on secret rotation
- name: set-grafana-password
  type: exec
  command: "grafana-cli admin reset-admin-password --password-from-stdin"
  stdin: "secret:vigo/grafana/admin_password"
  watch_secret: ["vigo/grafana/admin_password"]
  when: "changed"

# GOOD — temporary 0600 file
- name: deploy-with-key
  type: exec
  command: "deploy.sh --key-file {{secret_file}}"
  secret_file: "secret:vigo/deploy/key"

# BAD — secret visible in ps output
- name: bad-example
  type: exec
  command: "grafana-cli admin reset-admin-password MySecret123"
```

## Related

- [Server Configuration](../getting-started/server-config.md) — secrets: section
- [Resource DAG](../engineering/architecture/resource-dag.md) — watch_secret
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
