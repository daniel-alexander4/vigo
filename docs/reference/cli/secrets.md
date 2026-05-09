
# vigocli secrets

Manage secrets stored as AES-256-GCM encrypted files in the local secrets provider.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `set` | Encrypt and store a secret (raw, os, or web type) |
| `reveal` | Decrypt and display a secret |
| `reveal-password` | Decrypt and display the original plaintext password |
| `delete` | Remove an encrypted secret |
| `list` | List all secret keys |
| `init` | Generate a new master key file |
| `generate-certs` | Generate self-signed CA and server TLS certificates |

## Usage

```bash
vigocli secrets [subcommand] [flags]
```

## Persistent Flags

These flags apply to all subcommands:

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--key-file` | | | Path to master encryption key (omit to auto-generate) |
| `--secrets-dir` | | `/srv/vigo/secrets` | Directory for secret files |

## set

Encrypt and store a secret value. The type determines how the value is processed before storage:

| Type | Hashing | Use case |
|------|---------|----------|
| `raw` | None (verbatim) | API keys, DSNs, certificates, arbitrary values |
| `os` | SHA-512 crypt (`$6$...`) | Linux `/etc/shadow` via user executor `password` param |
| `web` | Bcrypt (`$2b$...`) | Vigo web UI authentication |

```bash
vigocli secrets set <type> <key> [flags]
```

For `raw` secrets, the value can be provided via `--from-file`, stdin pipe, or interactive prompt. For `os` and `web` types, the value is always prompted interactively with longdrawer disabled and password complexity enforcement (12+ chars, uppercase, lowercase, digit, special character).

The `os` and `web` types store the original plaintext at `<key>.txt` (encrypted) for recovery via `reveal-password`. The `os` type requires `openssl` to be installed.

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--from-file` | | | Read secret value from a file (raw type only) |

### Examples

#### Store a raw secret (interactive prompt, longdrawer disabled)

```bash
vigocli secrets set raw vigo/db/password
```

```
Enter secret value:
  Secret "vigo/db/password" stored.
  Server notified — affected envoys will apply on next check-in.
```

#### Store a raw secret from a file

```bash
vigocli secrets set raw vigo/db/dsn --from-file /tmp/dsn.txt
```

#### Store a raw secret from stdin pipe

```bash
longdrawer "s3cret" | vigocli secrets set raw vigo/api/key
```

#### Set an OS user password (SHA-512 crypt)

```bash
vigocli secrets set os vigo/os-users/dan
```

```
Enter password:
Confirm password:
  SHA-512 password hash stored at vigo/os-users/dan
  Plaintext password stored at vigo/os-users/dan.txt
```

Then reference it in a user resource:

```yaml
resources:
  - name: dan
    type: user
    username: dan
    password: "secret:vigo/os-users/dan"
```

#### Set a web UI password (bcrypt)

```bash
vigocli secrets set web vigo/web/auth/admin
```

```
Enter password:
Confirm password:
  Bcrypt password hash stored at vigo/web/auth/admin
  Plaintext password stored at vigo/web/auth/admin.txt
```

The key must follow the pattern `vigo/web/auth/<username>` to match the account created with `vigocli webusers create`.

## reveal

Decrypt and display a secret value. Each reveal is recorded in the tamper-evident audit trail as a `secret_accessed` event (requires the server to be running).

```bash
vigocli secrets reveal <key>
```

### Example

```bash
vigocli secrets reveal vigo/db/dsn
```

```
postgres://vigo:secret@localhost:5432/vigo?sslmode=disable
```

For secrets stored via `secrets set web` or `secrets set os`, the revealed value is a hash (bcrypt or SHA-512 crypt), not the original plaintext password. This is expected -- the CLI prints a hint to clarify. Use `reveal-password` to retrieve the original plaintext.

## reveal-password

Decrypt and display the original plaintext password for a secret that was stored via `secrets set web` or `secrets set os`. Each access is recorded in the tamper-evident audit trail.

```bash
vigocli secrets reveal-password <key>
```

### Example

```bash
vigocli secrets reveal-password vigo/web/auth/admin
```

```
MyS3cur3P@ssword!
```

The plaintext is stored at `<key>.txt` alongside the hash at `<key>`, both encrypted with AES-256-GCM. If no plaintext exists (password was set before this feature), an error is returned -- reset the password with `secrets set web` to store both.

## delete

Remove an encrypted secret.

```bash
vigocli secrets delete <key>
```

### Example

```bash
vigocli secrets delete vigo/test/key
```

```
  Secret "vigo/test/key" deleted.
```

## list

List all stored secret keys (values are not shown).

```bash
vigocli secrets list
```

### Example

```bash
vigocli secrets list
```

```
  KEY
  vigo/db/dsn
  vigo/db/password
```

## init

Generate a new 256-bit master encryption key file.

```bash
vigocli secrets init --key-file <path>
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--key-file` | | | Path to write the new master key (required) |

### Example

```bash
vigocli secrets init --key-file /srv/vigo/master.key
```

```
  Master key written to /srv/vigo/master.key (mode 0600).
  Keep this file safe — losing it means losing access to all encrypted secrets.
```

The command refuses to overwrite an existing key file.

## generate-certs

Generate a self-signed CA keypair and server TLS certificate as plain PEM files. Uses ECDSA P-256 keys.

```bash
vigocli secrets generate-certs [flags]
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
vigocli secrets generate-certs --san vigo.example.com --san 10.0.1.5
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

## Related

- [webusers](webusers.md) -- manage web UI user accounts
- [doctor](doctor.md) -- verify secret resolution with health checks
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
