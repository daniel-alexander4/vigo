
# vigogo secrets

Manage secrets stored as AES-256-GCM encrypted files in the local secrets provider.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `set` | Encrypt and store a secret |
| `reveal` | Decrypt and display a secret |
| `reveal-password` | Decrypt and display the original plaintext password |
| `delete` | Remove an encrypted secret |
| `list` | List all secret keys |
| `init` | Generate a new master key file |
| `generate-certs` | Generate self-signed CA and server TLS certificates |
| `set-os-password` | Hash a password with SHA-512 crypt and store it for OS user accounts |
| `set-web-password` | Hash a password with bcrypt and store it for web UI login |

## Usage

```bash
vigogo secrets [subcommand] [flags]
```

## Persistent Flags

These flags apply to all subcommands:

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--key-file` | | | Path to master encryption key (omit to auto-generate) |
| `--secrets-dir` | | `/srv/vigo/secrets` | Directory for secret files |

## set

Encrypt and store a secret value. The value can be provided in three ways (in priority order):

1. `--from-file <path>` -- read value from a file
2. Piped stdin -- `echo "myvalue" | vigogo secrets set mykey`
3. Interactive prompt -- reads from terminal with echo disabled (default, recommended)

The interactive prompt is the default and safest method — the value never appears in shell history or process listings.

```bash
vigogo secrets set <key> [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--from-file` | | | Read secret value from a file |

### Examples

#### Interactive prompt (echo disabled, recommended)

```bash
vigogo secrets set vigo/db/password
```

```
Enter secret value:
  Secret "vigo/db/password" stored.
  Server notified — affected envoys will apply on next check-in.
```

#### Store from a file

```bash
vigogo secrets set vigo/db/dsn --from-file /tmp/dsn.txt
```

```
  Secret "vigo/db/dsn" stored.
  Server notified — affected envoys will apply on next check-in.
```

#### Store from stdin pipe

```bash
echo "s3cret" | vigogo secrets set vigo/api/key
```

## reveal

Decrypt and display a secret value. Each reveal is recorded in the tamper-evident audit trail as a `secret_accessed` event (requires the server to be running).

```bash
vigogo secrets reveal <key>
```

### Example

```bash
vigogo secrets reveal vigo/db/dsn
```

```
postgres://vigo:secret@localhost:5432/vigo?sslmode=disable
```

For secrets stored via `set-web-password` or `set-os-password`, the revealed value is a hash (bcrypt or SHA-512 crypt), not the original plaintext password. This is expected — the CLI prints a hint to clarify. Use `reveal-password` to retrieve the original plaintext.

## reveal-password

Decrypt and display the original plaintext password for a secret that was stored via `set-web-password` or `set-os-password`. Each access is recorded in the tamper-evident audit trail.

```bash
vigogo secrets reveal-password <key>
```

### Example

```bash
vigogo secrets reveal-password vigo/web/auth/admin
```

```
MyS3cur3P@ssword!
```

The plaintext is stored at `<key>.txt` alongside the hash at `<key>`, both encrypted with AES-256-GCM. If no plaintext exists (password was set before this feature), an error is returned — reset the password with `set-web-password` to store both.

## delete

Remove an encrypted secret.

```bash
vigogo secrets delete <key>
```

### Example

```bash
vigogo secrets delete vigo/test/key
```

```
  Secret "vigo/test/key" deleted.
```

## list

List all stored secret keys (values are not shown).

```bash
vigogo secrets list
```

### Example

```bash
vigogo secrets list
```

```
  KEY
  vigo/db/dsn
  vigo/db/password
```

## init

Generate a new 256-bit master encryption key file.

```bash
vigogo secrets init --key-file <path>
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--key-file` | | | Path to write the new master key (required) |

### Example

```bash
vigogo secrets init --key-file /srv/vigo/master.key
```

```
  Master key written to /srv/vigo/master.key (mode 0600).
  Keep this file safe — losing it means losing access to all encrypted secrets.
```

The command refuses to overwrite an existing key file.

## generate-certs

Generate a self-signed CA keypair and server TLS certificate as plain PEM files. Uses ECDSA P-256 keys.

```bash
vigogo secrets generate-certs [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--tls-dir` | | `/srv/vigo/tls` | Directory for TLS certificate files |
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
vigogo secrets generate-certs --san vigo.example.com --san 10.0.1.5
```

```
  CA certificate       -> /srv/vigo/tls/ca
  CA private key       -> /srv/vigo/tls/ca_key
  Server certificate   -> /srv/vigo/tls/cert
  Server private key   -> /srv/vigo/tls/key

  CA valid for 3650 days, server cert valid for 365 days.
  SANs: localhost, myhost, vigo.example.com
  IPs:  127.0.0.1, 10.0.1.5
```

## set-os-password

Hash a plaintext password with SHA-512 crypt (`$6$...`) and store it as an encrypted secret. The resulting hash is suitable for Linux `/etc/shadow` via the user executor's `password` parameter. The original plaintext is also stored at `<key>.txt` (encrypted) for recovery via `reveal-password`.

Requires `openssl` to be installed on the machine running the CLI.

```bash
vigogo secrets set-os-password <key>
```

### Example

```bash
vigogo secrets set-os-password vigo/users/dan-password
```

```
Enter password:
Confirm password:
SHA-512 password hash stored at vigo/users/dan-password
```

Then reference it in a user resource:

```yaml
resources:
  - name: dan
    type: user
    username: dan
    password: "secret:vigo/users/dan-password"
```

## set-web-password

Hash a plaintext password with bcrypt and store it as an encrypted secret. Used for Vigo web UI login accounts (basic auth). The original plaintext is also stored at `<key>.txt` (encrypted) for recovery via `reveal-password`.

Password complexity requirements (HIPAA 164.308(a)(5)(ii)(D)):
- Minimum 12 characters
- At least one uppercase letter
- At least one lowercase letter
- At least one digit
- At least one special character

```bash
vigogo secrets set-web-password <key>
```

### Example

```bash
vigogo secrets set-web-password vigo/web/auth/admin
```

```
Enter password:
Confirm password:
Bcrypt password hash stored at vigo/web/auth/admin
```

The key must follow the pattern `vigo/web/auth/<username>` to match the account created with `vigogo webusers create`.

## Related

- [webusers](webusers.md) -- manage web UI user accounts
- [doctor](doctor.md) -- verify secret resolution with health checks
