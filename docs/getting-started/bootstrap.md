# Bootstrap Enrollment

Bootstrap is the process by which a new machine becomes a managed thrall. The server provides a one-line bootstrap script that handles agent installation and enrollment.

## How It Works

![Bootstrap Enrollment Flow](img/bootstrap-flow.svg)

## Bootstrap Script

The server serves a shell script at `GET /bootstrap`:

```bash
curl -sSf http://<server>:8181/bootstrap | sudo sh
```

The script:
1. Detects OS (Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos) and architecture
2. Stops any existing hobnob-thrall service and removes previous enrollment data for a clean start
3. Downloads the correct agent binary from the server (always replaces existing)
4. Generates an ED25519 keypair
5. Calls the server's bootstrap registration endpoint
6. Receives a client UUID and signed TLS certificate
7. Writes agent config to `/etc/hobnob-thrall/config`
8. Installs the appropriate service for the platform:
   - **Linux:** systemd unit (`hobnob-thrall.service`)
   - **macOS:** launchd plist (`com.hobnob.thrall`)
   - **FreeBSD / NetBSD:** rc.d script (`/usr/local/etc/rc.d/hobnob_thrall`)
   - **OpenBSD:** registered via `rcctl`
   - **illumos:** SMF service manifest (`svc:/system/hobnob-thrall:default`)
9. Starts the service

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
- Bootstrap endpoints (`/bootstrap`, `/api/v1/agent/*`, `/api/v1/bootstrap/*`) are exempt from web UI authentication so agents can enroll without credentials
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
- Ensure the server's container image includes agent binaries for the target platform
- Check that the `/srv/hobnob/` volume is correctly mounted

## Related

- [Installation](installation.md)
- [Server Configuration](server-config.md)
- [Architecture](../concepts/architecture.md)
