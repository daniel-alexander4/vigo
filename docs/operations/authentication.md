---
title: Authentication
---

# Authentication

All access to the web UI and REST API requires authentication. Three methods are supported:

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

## Basic (Username/Password)

Basic authentication stores user accounts in the database and password hashes (bcrypt) in the local secrets provider. This separation ensures credentials never appear in the database.

### Initial Setup

The server automatically creates an `admin` account on first startup. The web UI redirects to a setup page with the one command needed to activate it:

```bash
vigogo secrets set-web-password vigo/web/auth/admin
```

Until a password is set, all web UI requests redirect to the setup page. Password hashes are stored at `vigo/web/auth/<username>` in the secrets provider.

### Adding Users

```bash
# Create a viewer account
vigogo webusers create --username alice --role viewer --email alice@example.com

# Set their password
vigogo secrets set-web-password vigo/web/auth/alice
```

### Resetting Passwords

```bash
vigogo secrets set-web-password vigo/web/auth/admin
```

### CLI Access (Localhost Trust)

When the CLI runs on the same host as the server (or Docker host), API requests are automatically trusted with admin privileges — no token needed. This works like PostgreSQL's `peer` authentication: shell access to the server host implies full access.

Local and container-network connections are trusted. Remote CLI access requires an API token via `vigogo auth set-token`.

**Reverse proxy warning:** Localhost trust relies on `RemoteAddr` from the Go HTTP server, which reflects the actual TCP connection source. If Vigo is behind a reverse proxy (nginx, HAProxy), all requests appear to come from the proxy's IP, not the original client. Do not deploy behind a reverse proxy that forwards to `localhost:8443` from an untrusted network — the proxy's requests would be trusted as local.

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

Unlike OIDC, isowebauth requires users to be pre-created with their SSH public keys. The auto-seeded admin account can be used to create them:

```bash
# First, set the admin password to get CLI access:
vigogo secrets set-web-password vigo/web/auth/admin

# Create users with SSH keys:
vigogo webusers create --username bob --role viewer --ssh-key-file /path/to/bob.pub
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

Two roles are supported:

| Action | Admin | Viewer |
|--------|-------|--------|
| View dashboards, envoys, runs, compliance | Yes | Yes |
| View tasks, workflows, query results | Yes | Yes |
| View docs, spanner status | Yes | Yes |
| Dispatch tasks | Yes | No |
| Run workflows | Yes | No |
| Execute queries | Yes | No |
| Manage enrollment patterns | Yes | No |
| Manage webhooks | Yes | No |
| Manage users | Yes | No |
| Run health checks | Yes | No |
| Create/download backups | Yes | No |
| Create/revoke API tokens | Yes | Yes (own only) |

### Fine-Grained Permissions

Beyond the admin/viewer roles, each user has a set of permissions stored as a JSON array. Admins always have all permissions regardless of stored values. Viewer permissions default to `fleet.read` and `audit.read`.

| Permission | Description | Admin | Viewer |
|------------|-------------|-------|--------|
| `fleet.read` | View envoys, runs, compliance | Yes | Yes |
| `fleet.write` | Modify envoy tags, maintenance | Yes | No |
| `config.publish` | Publish configuration | Yes | No |
| `config.rollback` | Rollback configuration | Yes | No |
| `push.execute` | Dispatch ad-hoc pushes | Yes | No |
| `query.execute` | Execute live queries | Yes | No |
| `workflow.execute` | Run workflows | Yes | No |
| `users.manage` | Create/delete/modify users | Yes | No |
| `tokens.manage` | Manage API tokens | Yes | No |
| `webhooks.manage` | Manage webhooks | Yes | No |
| `backup.manage` | Create/download backups | Yes | No |
| `enrollment.manage` | Manage enrollment patterns | Yes | No |
| `audit.read` | View audit trail | Yes | Yes |

Custom permission sets can be assigned per-user via the REST API to create roles between viewer and admin (e.g., an operator who can push but not manage users).

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
vigogo auth set-token $(curl -s -X POST \
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
vigogo auth set-token <token>
vigogo auth status    # verify
vigogo auth clear     # remove
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
vigogo webusers list
vigogo webusers create --username alice --role admin [--email alice@example.com] [--ssh-key-file ~/.ssh/id.pub]
vigogo secrets set-web-password vigo/web/auth/alice
vigogo webusers delete --username alice
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

Login and failed login events include the client IP address in the payload field (`{"ip":"..."}`) for brute-force detection. Failed login events record the attempted username (not the password).

View audit events:

```bash
vigogo audit list
vigogo audit list --type auth.login_failed
```

## Troubleshooting

**OIDC callback fails with "invalid state parameter":**
The session may have expired between redirect and callback. Check that `redirect_url` exactly matches what's registered with your OIDC provider.

**Isowebauth "Cannot connect to isowebauth app":**
Ensure the isowebauth desktop app is running and listening on `127.0.0.1:7890`. Check that your Vigo server's origin is in isowebauth's allowed origins.

**"ssh-keygen: command not found":**
The server requires `ssh-keygen` in the PATH for isowebauth signature verification. Install OpenSSH on the server.

**API returns 401 with valid token:**
Check that the token hasn't expired. List your tokens with `vigogo auth status` or the tokens API.
