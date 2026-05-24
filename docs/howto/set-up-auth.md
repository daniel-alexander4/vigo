
---
title: Authentication
---

# Set up authentication

You'll finish this page with a Web UI and REST API that only authenticated users can reach — either via local username/password (default), OIDC against your existing identity provider, or `isowebauth` (Alexander4's hosted WebAuthn service). Plus API tokens for automation.

**When you'd use this:** every deployment past the first laptop. You're picking *which* method.

**When you'd skip this:** you can't — the server refuses unauthenticated requests by construction.

All access to the Web UI and REST API requires authentication. Three methods are supported:

| Method | Description | Use Case |
|--------|-------------|----------|
| `basic` | Username/password (default) | Most deployments |
| `oidc` | OpenID Connect SSO | Enterprise SSO (Keycloak, Okta, Auth0, Google, Azure AD) |
| `isowebauth` | SSH key-based signing | Sysadmin teams using SSH keys |

## Configuration

Set `auth.method` in `server.yaml`:

```yaml
auth:
  method: basic   # or: oidc, isowebauth
  session_idle_timeout: "15m"   # configurable idle timeout
```

When `method` is omitted, basic authentication is enabled by default.

### Session Idle Timeout

Sessions automatically expire after a configurable period of inactivity. Configure with `session_idle_timeout`:

```yaml
auth:
  session_idle_timeout: "30m"   # 30 minutes; use "15m" for HIPAA
```

Set to `"0s"` to disable idle timeout (not recommended for regulated environments).

Role changes take effect immediately. When an admin updates another user's role (`PUT /api/v1/users/{id}` or `vigocli webusers set-role`) the server destroys that user's cached browser sessions on the spot — they re-authenticate on the next request and the new role applies. The same happens on user delete. Without this, a demotion would stay invisible to the demoted user's open browser until the idle timeout fired. API tokens already resolve permissions on every call, so they need nothing extra.

## Basic (Username/Password)

Basic authentication stores user accounts in the database and the password itself in the unlock-gated secrets vault (reversibly encrypted under the master key). The login handler resolves the stored value on every attempt and constant-time-compares it against the submitted password. This separation keeps credentials out of the database; the vault's unlock gate keeps casual access off the operator surface.

### Initial Setup

There is no auto-seeded `admin` user; the server starts up with no web users at all. Create your per-operator admin from the server host after enrolling the local agent:

```bash
# First enroll the local agent so the server can verify the OS-user mapping:
curl -sSfk https://localhost:8443/bootstrap | sudo sh

# Then create yourself as the first admin. <yourname> must be a real human
# OS account on this host (uid≥1000, real shell). The CLI auto-reads your
# pubkey from ~<yourname>/.ssh/id_ed25519.pub (fallback id_rsa.pub).
sudo vigocli webusers create --username <yourname> --role admin

# Set your login password:
vigocli secrets set password vigo/web/auth/<yourname>
```

Sign in at `https://localhost:8443/login`. Passwords are stored verbatim at `vigo/web/auth/<username>` inside the master-key AES-256-GCM envelope; the login handler does a `subtle.ConstantTimeCompare` against the resolved value on every attempt.

**Admin webusers map 1:1 with OS users.** The username on the web side must equal a human OS account on the server host — this gates audit attribution to a real person and seeds the pubkey for browser scrier sessions. Viewer and compliance users are unconstrained; they're one-offs (auditors, partners, read-only access) without OS counterparts.

### Adding Users

```bash
# Another per-operator admin (real OS user required):
sudo vigocli webusers create --username alice --role admin

# Viewer / compliance — no OS-user constraint, --ssh-key-file optional:
sudo vigocli webusers create --username auditor --role viewer --email auditor@example.com
sudo vigocli webusers create --username carol --role compliance --email carol@example.com

# Set passwords:
vigocli secrets set password vigo/web/auth/alice
```

### Resetting Passwords

```bash
vigocli secrets set password vigo/web/auth/<username>
```

### CLI Access (Local-Admin Token)

When `vigocli` runs on the server host it authenticates with the **local-admin token** — a credential the server mints on first boot and persists at `/srv/vigo/cli-admin-token` (mode `0600`, `root`-owned). `vigocli` reads it automatically when no per-user token is configured, so `sudo vigocli ...` works on the server host with no setup. Remote CLI access still requires a per-user API token via `vigocli auth set-token`.

The token gates on the *filesystem*, not the connecting IP: only a caller that can read the `0600` root-owned file is admin. A non-root user with a shell on the server host — who cannot read the file — gets `401`. This is the same gate that already protects the secrets master key and the `vigocli` binary itself.

**Root-only on the server.** The `vigocli` binary on the server host is installed at `/usr/local/bin/vigocli` with mode `0700` owned by `root:root`, and the local-admin token file is `0600 root`. Non-root users can read neither, so admin stays out of their reach. The install paths (`Dockerfile.server`, `entrypoint.sh`, `scripts/upgrade.sh`) enforce the binary mode — if you package vigocli onto a server by any other route, set the same mode by hand. Members of the `docker` group are effectively root on the host and can still run vigocli via `docker exec`; keep that group restricted.

## OIDC (OpenID Connect)

OIDC integrates with any OpenID Connect-compliant identity provider.

### Setup

```yaml
auth:
  method: oidc
  oidc:
    issuer: "https://accounts.google.com"
    client_id: "your-client-id"
    client_secret: "secret:vigo/auth/oidc_client_secret"
    redirect_url: "https://vigo.example.com:8443/auth/callback"
    scopes: [openid, profile, email]
```

### Provider Examples

**Keycloak:**
```yaml
oidc:
  issuer: "https://keycloak.example.com/realms/vigo"
  client_id: "vigo"
  client_secret: "secret:vigo/auth/oidc_client_secret"
  redirect_url: "https://vigo.example.com:8443/auth/callback"
```

**Okta:**
```yaml
oidc:
  issuer: "https://your-org.okta.com"
  client_id: "0oaXXXXXXXXXXXXX"
  client_secret: "secret:vigo/auth/oidc_client_secret"
  redirect_url: "https://vigo.example.com:8443/auth/callback"
```

**Google:**
```yaml
oidc:
  issuer: "https://accounts.google.com"
  client_id: "xxxx.apps.googleusercontent.com"
  client_secret: "secret:vigo/auth/oidc_client_secret"
  redirect_url: "https://vigo.example.com:8443/auth/callback"
```

**Azure AD:**
```yaml
oidc:
  issuer: "https://login.microsoftonline.com/{tenant-id}/v2.0"
  client_id: "your-app-id"
  client_secret: "secret:vigo/auth/oidc_client_secret"
  redirect_url: "https://vigo.example.com:8443/auth/callback"
```

### First-User Bootstrap

The first user to log in via OIDC is automatically promoted to **admin**. All subsequent users are created as **viewers** by default. Admins can change roles from the Users admin page.

## Isowebauth (SSH Key-Based)

Isowebauth uses the [isowebauth](https://github.com/vigo/isowebauth) desktop app to sign challenges with the user's SSH private key. The server verifies signatures using `ssh-keygen -Y verify`.

### Setup

1. Install the isowebauth desktop app on each admin workstation.
2. Configure isowebauth to allow your Vigo server's origin and the `vigo` namespace.
3. Set `auth.method: isowebauth` in `server.yaml`:

```yaml
auth:
  method: isowebauth
  isowebauth:
    namespace: "vigo"   # default
```

### User Management

Unlike OIDC, isowebauth requires users to be pre-created with their SSH public keys. After standing up your per-operator admin account (see *Initial Setup* above), create the other users from the server host:

```bash
# Viewer / compliance — --ssh-key-file accepted:
sudo vigocli webusers create --username bob --role viewer --ssh-key-file /path/to/bob.pub

# Per-operator admin — pubkey auto-read from the matching OS user's
# home dir; --ssh-key-file rejected.
sudo vigocli webusers create --username alice --role admin
```

### Sign-In Flow

1. User enters their username on the login page.
2. Browser fetches a random challenge from the server.
3. Browser sends the challenge to the isowebauth app on `localhost:7890`.
4. Isowebauth signs the challenge with the user's SSH key via `ssh-keygen -Y sign`.
5. Browser sends the signature back to the server for verification.
6. Server verifies using `ssh-keygen -Y verify` against the stored public key.
7. On success, a session cookie is created.

## Public Endpoints

The following paths are accessible without authentication regardless of the configured auth method. This ensures agent bootstrap, health checks, and metrics scraping work without credentials.

| Path prefix | Purpose |
|-------------|---------|
| `/bootstrap` | Agent bootstrap script and certificate endpoints |
| `/api/v1/agent/` | Agent binary downloads |
| `/api/v1/bootstrap/` | Agent enrollment registration |
| `/metrics` | Prometheus metrics scraping |
| `/healthz` | Health check probe |
| `/static/` | CSS, JavaScript, and image assets |
| `/auth/` | Login and SSO callback handlers |
| `/login` | Login page |
| `/setup` | First-user setup page |

## Roles and Permissions

Three roles are supported:

| Action | Admin | Viewer | Compliance |
|--------|-------|--------|------------|
| View dashboards, envoys, runs | Yes | Yes | No |
| View compliance pages, framework reports | Yes | Yes | Yes |
| Upload supporting compliance documentation | Yes | No | Yes |
| Download audit-bundle exports | Yes | Yes | Yes |
| View raw audit log (`/audit`) | Yes | Yes | **No** |
| View tasks, workflows, query results | Yes | Yes | No |
| Dispatch tasks | Yes | No | No |
| Run workflows | Yes | No | No |
| Execute queries | Yes | No | No |
| Manage enrollment patterns | Yes | No | No |
| Manage webhooks | Yes | No | No |
| Manage users | Yes | No | No |
| Run health checks | Yes | No | No |
| Create/download backups | Yes | No | No |
| Create/revoke API tokens | Yes | Yes (own only) | Yes (own only) |

The `compliance` role is for GRC staff and compliance-document uploaders. It is **not for auditors** — auditors should be assigned `admin` or `viewer` depending on the depth of access required. The role exists so compliance staff can do their job (upload BAA, IR plans, training records; view framework reports; download audit-bundle evidence) without being granted broader fleet, query, or audit-log access.

### Fine-Grained Permissions

Beyond the role labels, each user has a permission set stored as a JSON array. Admins always have all permissions regardless of the stored slice. Viewer defaults: `fleet.read`, `audit.read`, `compliance.read`. Compliance defaults: `compliance.read`, `compliance.docs.write` (no `audit.read`).

| Permission | Description | Admin | Viewer | Compliance |
|------------|-------------|-------|--------|------------|
| `fleet.read` | View envoys, runs, inventory | Yes | Yes | No |
| `fleet.write` | Modify envoy tags, maintenance, force update | Yes | No | No |
| `config.publish` | Publish / freeze / reload configuration | Yes | No | No |
| `config.rollback` | Roll back to a prior config version | Yes | No | No |
| `push.execute` | Dispatch ad-hoc tasks | Yes | No | No |
| `query.execute` | Execute live trait queries (incl. AI ask) | Yes | No | No |
| `workflow.execute` | Run / abort workflows | Yes | No | No |
| `users.manage` | Create/delete/modify users | Yes | No | No |
| `tokens.manage` | Manage API tokens | Yes | No | No |
| `webhooks.manage` | Manage webhooks | Yes | No | No |
| `backup.manage` | Create/download backups | Yes | No | No |
| `enrollment.manage` | Manage enrollment patterns | Yes | No | No |
| `audit.read` | View raw audit-log chain | Yes | Yes | **No** |
| `compliance.read` | View framework reports + audit bundle | Yes | Yes | Yes |
| `compliance.docs.write` | Upload supporting compliance documentation | Yes | No | Yes |
| `compliance.docs.purge` | Hard-delete soft-deleted compliance docs | Yes | No | No |

The `audit.read` exclusion on the compliance role is deliberate: the raw hash-chained audit log is admin-and-viewer-only, while the audit-bundle export (a per-framework compliance evidence artifact built from the same data) rides `compliance.read` so the role can download what an auditor asks for without being handed the entire audit stream.

Custom permission sets can be assigned per-user via the REST API to tailor access outside the three named roles (e.g., an operator who can dispatch tasks but not manage users, or a viewer who can also publish config).

## Two-Factor Authentication (TOTP)

TOTP (Time-based One-Time Password) adds a second factor to basic authentication (HIPAA 164.312(d)). When enabled, users must enter a 6-digit code from an authenticator app after their password.

### Setup

1. Log in to the web UI with your password
2. Call the TOTP setup API:
   ```bash
   curl -X POST https://vigo:8443/api/v1/users/totp/setup \
     -H "Authorization: Bearer $TOKEN"
   ```
3. Scan the returned `url` as a QR code in your authenticator app (Google Authenticator, Authy, 1Password, etc.)
4. Confirm with a valid code:
   ```bash
   curl -X POST https://vigo:8443/api/v1/users/totp/confirm \
     -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"code": "123456"}'
   ```
5. Save the returned recovery codes in a secure location

### Recovery Codes

Eight recovery codes are generated when TOTP is enabled. Each code can only be used once. If you lose access to your authenticator app, enter a recovery code instead of a TOTP code at the login prompt.

### Disabling TOTP

```bash
curl -X POST https://vigo:8443/api/v1/users/totp/disable \
  -H "Authorization: Bearer $TOKEN"
```

### TOTP Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/users/totp/setup` | POST | Generate TOTP secret (returns secret + otpauth URL) |
| `/api/v1/users/totp/confirm` | POST | Confirm setup with valid code, enables TOTP |
| `/api/v1/users/totp/disable` | POST | Disable TOTP for your account |

## API Tokens

API tokens allow the CLI and scripts to authenticate without a browser session. Tokens are hashed at rest.

### Creating a Token

Via the CLI (requires an active session or existing token):

```bash
# Create a token (the plaintext is shown once):
vigocli auth set-token $(curl -s -X POST \
  -H "Authorization: Bearer $EXISTING_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "ci-deploy", "expires_in": "720h"}' \
  https://vigo.example.com:8443/api/v1/auth/tokens | jq -r .token)
```

Or via the REST API directly:

```bash
curl -X POST https://vigo.example.com:8443/api/v1/auth/tokens \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "my-token", "expires_in": "720h"}'
```

### CLI Authentication


Store a token for CLI use:

```bash
vigocli auth set-token <token>
vigocli auth status    # verify
vigocli auth clear     # remove
```

The token is stored securely in the user's home directory.

### Token Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/auth/tokens` | POST | Create a new token |
| `/api/v1/auth/tokens` | GET | List your tokens (metadata only) |
| `/api/v1/auth/tokens/{id}` | DELETE | Revoke a token |

## User Management

### REST API

| Endpoint | Method | Auth | Description |
|----------|--------|------|-------------|
| `/api/v1/users` | GET | Admin | List all users |
| `/api/v1/users` | POST | Admin | Create a user |
| `/api/v1/users/{id}` | PUT | Admin | Update a user |
| `/api/v1/users/{id}` | DELETE | Admin | Delete a user |

### CLI

```bash
vigocli webusers list
vigocli webusers create --username alice --role admin [--email alice@example.com] [--ssh-key-file ~/.ssh/id.pub]
vigocli secrets set password vigo/web/auth/alice
vigocli webusers delete --username alice
```

## Audit Logging

All authentication and user management events are recorded in the tamper-evident audit chain (`audit_entries` table). These events support HIPAA 164.312(b) audit controls and intrusion detection.

| Event Type | Trigger | Actor |
|------------|---------|-------|
| `auth.login` | Successful login (basic, OIDC, isowebauth) | Username |
| `auth.login_failed` | Failed login (bad password, unknown user, bad signature) | Attempted username |
| `auth.logout` | User logout | Username |
| `user.create` | User account created | Admin who created |
| `user.delete` | User account deleted | Admin who deleted |
| `user.role_change` | Role updated (only when role actually changes) | Admin who changed |
| `token.create` | API token created | Token owner |
| `token.revoke` | API token revoked | Token owner |
| `compliance.waiver_created` | Compliance waiver created via `POST /api/v1/compliance/waivers` | Session user (or request-supplied approver) |
| `compliance.waiver_revoked` | Compliance waiver revoked via `DELETE /api/v1/compliance/waivers/{id}` | Session user (or waiver's original approver) |

Login and failed login events include the client IP address in the payload field (`{"ip":"..."}`) for brute-force detection. Failed login events record the attempted username (not the password).

View audit events:

```bash
vigocli audit list
vigocli audit list --type auth.login_failed
```

## Troubleshooting

**OIDC callback fails with "invalid state parameter":**
The session may have expired between redirect and callback. Check that `redirect_url` exactly matches what's registered with your OIDC provider.

**Isowebauth "Cannot connect to isowebauth app":**
Ensure the isowebauth desktop app is running and listening on `127.0.0.1:7890`. Check that your Vigo server's origin is in isowebauth's allowed origins.

**"ssh-keygen: command not found":**
The server requires `ssh-keygen` in the PATH for isowebauth signature verification. Install OpenSSH on the server.

**API returns 401 with valid token:**
Check that the token hasn't expired. List your tokens with `vigocli auth status` or the tokens API.

## What's next

- **Add per-user SSH pubkeys** so Scrier sessions auto-detect them → [Set up Scrier](set-up-scrier.md).
- **Audit who logged in when** → every auth event lands in the [hash-chained audit log](compliance-reporting.md).
- **Auth is misbehaving** → [Troubleshoot common issues](troubleshoot.md).

---
*Verified on Vigo 0.51.6 · 2026-05-13.*

*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
