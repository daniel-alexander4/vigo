# hobnobcli secrets

Manage secrets stored as AES-256-GCM encrypted files in the local secrets provider.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `set` | Encrypt and store a secret |
| `reveal` | Decrypt and display a secret |
| `delete` | Remove an encrypted secret |
| `list` | List all secret keys |
| `init` | Generate a new master key file |
| `generate-certs` | Generate self-signed CA and server TLS certificates |
| `set-os-password` | Hash a password with SHA-512 crypt and store it for OS user accounts |
| `set-web-password` | Hash a password with bcrypt and store it for web UI login |

## Usage

```bash
hobnobcli secrets [subcommand] [flags]
```

## Persistent Flags

These flags apply to all subcommands:

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--key-file` | | | Path to master encryption key (omit to auto-generate) |
| `--secrets-dir` | | `/srv/hobnob/secrets` | Directory for secret files |

## set

Encrypt and store a secret value. The value can be provided in four ways (in priority order):

1. `--from-file <path>` -- read value from a file
2. Positional argument -- `hobnobcli secrets set mykey myvalue`
3. Piped stdin -- `echo "myvalue" | hobnobcli secrets set mykey`
4. Interactive prompt -- reads from terminal with echo disabled (default)

```bash
hobnobcli secrets set <key> [value] [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--from-file` | | | Read secret value from a file |

### Examples

#### Store from a file (recommended for sensitive values)

```bash
hobnobcli secrets set hobnob/db/dsn --from-file /tmp/dsn.txt
```

```
  Secret "hobnob/db/dsn" stored.
  Server notified — affected thralls will apply on next check-in.
```

#### Interactive prompt (echo disabled)

```bash
hobnobcli secrets set hobnob/db/password
```

```
Enter secret value:
  Secret "hobnob/db/password" stored.
  Server notified — affected thralls will apply on next check-in.
```

#### Store from stdin pipe

```bash
echo "s3cret" | hobnobcli secrets set hobnob/api/key
```

#### Store from positional argument (exposes value in shell history)

```bash
hobnobcli secrets set hobnob/test/key testvalue
```

## reveal

Decrypt and display a secret value.

```bash
hobnobcli secrets reveal <key>
```

### Example

```bash
hobnobcli secrets reveal hobnob/db/dsn
```

```
postgres://hobnob:secret@localhost:5432/hobnob?sslmode=disable
```

## delete

Remove an encrypted secret.

```bash
hobnobcli secrets delete <key>
```

### Example

```bash
hobnobcli secrets delete hobnob/test/key
```

```
  Secret "hobnob/test/key" deleted.
```

## list

List all stored secret keys (values are not shown).

```bash
hobnobcli secrets list
```

### Example

```bash
hobnobcli secrets list
```

```
  KEY
  hobnob/db/dsn
  hobnob/db/password
```

## init

Generate a new 256-bit master encryption key file.

```bash
hobnobcli secrets init --key-file <path>
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--key-file` | | | Path to write the new master key (required) |

### Example

```bash
hobnobcli secrets init --key-file /srv/hobnob/master.key
```

```
  Master key written to /srv/hobnob/master.key (mode 0600).
  Keep this file safe — losing it means losing access to all encrypted secrets.
```

The command refuses to overwrite an existing key file.

## generate-certs

Generate a self-signed CA keypair and server TLS certificate as plain PEM files. Uses ECDSA P-256 keys.

```bash
hobnobcli secrets generate-certs [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--tls-dir` | | `/srv/hobnob/tls` | Directory for TLS certificate files |
| `--san` | | | Additional SANs (DNS names or IPs), repeatable |
| `--ca-days` | | `3650` | CA certificate validity in days |
| `--server-days` | | `365` | Server certificate validity in days |
| `--force` | | `false` | Overwrite existing certificates |

### Generated files

| File | Description |
|------|-------------|
| `tls/ca` | CA certificate (PEM) |
| `tls/ca_key` | CA private key (PEM) |
| `tls/cert` | Server certificate (PEM) |
| `tls/key` | Server private key (PEM) |

The server certificate automatically includes SANs for `localhost`, `127.0.0.1`, and the system hostname.

### Example

```bash
hobnobcli secrets generate-certs --san hobnob.example.com --san 10.0.1.5
```

```
  CA certificate       -> /srv/hobnob/tls/ca
  CA private key       -> /srv/hobnob/tls/ca_key
  Server certificate   -> /srv/hobnob/tls/cert
  Server private key   -> /srv/hobnob/tls/key

  CA valid for 3650 days, server cert valid for 365 days.
  SANs: localhost, myhost, hobnob.example.com
  IPs:  127.0.0.1, 10.0.1.5
```

## set-os-password

Hash a plaintext password with SHA-512 crypt (`$6$...`) and store it as an encrypted secret. The resulting hash is suitable for Linux `/etc/shadow` via the user executor's `password` parameter.

Requires `openssl` to be installed on the machine running the CLI.

```bash
hobnobcli secrets set-os-password <key>
```

### Example

```bash
hobnobcli secrets set-os-password hobnob/users/dan-password
```

```
Enter password:
Confirm password:
SHA-512 password hash stored at hobnob/users/dan-password
```

Then reference it in a user resource:

```yaml
resources:
  - name: dan
    type: user
    username: dan
    password: "secret:hobnob/users/dan-password"
```

## set-web-password

Hash a plaintext password with bcrypt and store it as an encrypted secret. Used for Hobnob web UI login accounts (basic auth).

Password complexity requirements (HIPAA 164.308(a)(5)(ii)(D)):
- Minimum 12 characters
- At least one uppercase letter
- At least one lowercase letter
- At least one digit
- At least one special character

```bash
hobnobcli secrets set-web-password <key>
```

### Example

```bash
hobnobcli secrets set-web-password hobnob/web/auth/admin
```

```
Enter password:
Confirm password:
Bcrypt password hash stored at hobnob/web/auth/admin
```

The key must follow the pattern `hobnob/web/auth/<username>` to match the account created with `hobnobcli webusers create`.

## Related

- [webusers](webusers.md) -- manage web UI user accounts
- [doctor](doctor.md) -- verify secret resolution with health checks
