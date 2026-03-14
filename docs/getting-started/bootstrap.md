# Bootstrap Enrollment

Bootstrap is the process by which a new machine becomes a managed thrall. The server provides a one-line bootstrap script that handles agent installation and enrollment.

## How It Works

```
┌──────────────┐         ┌──────────────┐         ┌──────────────┐
│   Admin      │         │   Server     │         │   New Thrall  │
│              │         │  (hobd)      │         │              │
└──────┬───────┘         └──────┬───────┘         └──────┬───────┘
       │                        │                        │
       │  1. (optional)         │                        │
       │  hobnobcli tokens      │                        │
       │  generate --pattern    │                        │
       │  "*.web.*"             │                        │
       │ ──────────────────────>│                        │
       │                        │                        │
       │                        │  2. curl bootstrap     │
       │                        │<───────────────────────│
       │                        │                        │
       │                        │  3. Return shell script│
       │                        │  + agent binary        │
       │                        │───────────────────────>│
       │                        │                        │
       │                        │  4. Register RPC       │
       │                        │  (hostname, IP, pubkey,│
       │                        │   token)               │
       │                        │<───────────────────────│
       │                        │                        │
       │                        │  5. Return UUID +      │
       │                        │  signed client cert    │
       │                        │───────────────────────>│
       │                        │                        │
       │                        │  6. Start check-in loop│
       │                        │<───────────────────────│
```

## Bootstrap Script

The server serves a shell script at `GET /bootstrap`:

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

The script:
1. Detects OS and architecture
2. Downloads the correct agent binary from the server
3. Generates an ED25519 keypair
4. Calls the server's Register gRPC endpoint
5. Receives a client UUID and signed TLS certificate
6. Writes agent config to `/etc/hobnob-thrall/agent.yaml`
7. Installs and enables the `hobnob-thrall` systemd service
8. Starts the service

## Enrollment Methods

### Token-Free (Trusted Enrollment)

Configure trusted patterns in `server.yaml`:

```yaml
bootstrap:
  trusted_enrollment:
    - pattern: "*.web.example.com"
      cidrs: ["10.0.0.0/8"]
    - pattern: "*.dev.*"
      cidrs: ["192.168.0.0/16"]
```

Thralls matching both the hostname glob and source CIDR enroll without a token.

### One-Time Token

For stricter environments, generate a token:

```bash
hobnobcli tokens generate --pattern "web3.example.com"
```

Pass the token to the bootstrap script:

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh -s -- --token <token>
```

Tokens are:
- One-time use (consumed on successful enrollment)
- Bcrypt-hashed at rest in the database
- Pattern-matched against the enrolling hostname

### Approved Patterns

Pre-approve hostname patterns so matching thralls can enroll:

```bash
hobnobcli approved add "*.prod.example.com"
```

## Certificate Signing

When `server.tls.ca_key_file` is configured, the server acts as a Certificate Authority:

1. Agent generates a CSR during bootstrap
2. Server signs the CSR with the CA key
3. Agent receives a signed client certificate
4. All subsequent gRPC communication uses this certificate for mTLS

Certificate validity is configurable:

```yaml
bootstrap:
  cert_validity: "8760h"  # 1 year (default)
```

## Security

- All gRPC traffic uses mTLS — the bootstrap HTTP endpoint is the only plaintext path
- Agent public keys are stored server-side and verified on every check-in (ED25519 signatures)
- One-time tokens prevent replay attacks
- Revoked agents receive an error on check-in and cannot re-enroll with the same UUID
- The bootstrap endpoint can be restricted to specific CIDRs via reverse proxy

## Troubleshooting

**Agent fails to register:**
- Check that the server's gRPC port (1530) is reachable from the thrall
- Verify the token hasn't already been used
- Check hostname matches the token pattern or trusted enrollment pattern

**Certificate errors after enrollment:**
- Verify `ca_key_file` is configured in server.yaml
- Check certificate hasn't expired (`cert_validity` setting)

**Agent binary download fails:**
- Ensure `dist/agent/linux/{amd64,arm64}/hobnob` exists on the server
- Run `make dist-agent` to build agent binaries

## Related

- [Installation](installation.md)
- [Server Configuration](server-config.md)
- [Architecture](../concepts/architecture.md)
