---
title: Authentication
---

# Authentication

Hobnob supports four authentication methods for the web UI and REST API:

| Method | Description | Use Case |
|--------|-------------|----------|
| `basic` | Username/password (default) | Most deployments |
| `none` | Fully open | Development, private networks |
| `oidc` | OpenID Connect SSO | Enterprise SSO (Keycloak, Okta, Auth0, Google, Azure AD) |
| `isowebauth` | SSH key-based signing | Sysadmin teams using SSH keys |

## Configuration

Set `auth.method` in `server.yaml`:

```yaml
auth:
  method: basic   # or: none, oidc, isowebauth
```

When `method` is omitted, basic authentication is enabled by default. Set to `none` to disable authentication entirely.

## Basic (Username/Password)

Basic authentication stores user accounts in the database and password hashes (bcrypt) in the local secrets provider. This separation ensures credentials never appear in the database.

### Initial Setup

On first access, the web UI displays setup instructions. Use the CLI to create the admin account:

```bash
# 1. Create the admin user
hobnobcli users create --username admin --role admin

# 2. Set the password (prompts securely)
hobnobcli users set-password --username admin
```

The account is not active for login until a password is set. Password hashes are stored at `hobnob/web/auth/<username>` in the secrets provider.

### Adding Users

```bash
# Create a viewer account
hobnobcli users create --username alice --role viewer --email alice@example.com

# Set their password
hobnobcli users set-password --username alice
```

### Resetting Passwords

```bash
hobnobcli users set-password --username admin
```

## OIDC (OpenID Connect)

OIDC integrates with any OpenID Connect-compliant identity provider.

### Setup

```yaml
auth:
  method: oidc
  oidc:
    issuer: "https://accounts.google.com"
    client_id: "your-client-id"
    client_secret: "secret:hobnob/auth/oidc_client_secret"
    redirect_url: "https://hobnob.example.com:8181/auth/callback"
    scopes: [openid, profile, email]
```

### Provider Examples

**Keycloak:**
```yaml
oidc:
  issuer: "https://keycloak.example.com/realms/hobnob"
  client_id: "hobnob"
  client_secret: "secret:hobnob/auth/oidc_client_secret"
  redirect_url: "https://hobnob.example.com:8181/auth/callback"
```

**Okta:**
```yaml
oidc:
  issuer: "https://your-org.okta.com"
  client_id: "0oaXXXXXXXXXXXXX"
  client_secret: "secret:hobnob/auth/oidc_client_secret"
  redirect_url: "https://hobnob.example.com:8181/auth/callback"
```

**Google:**
```yaml
oidc:
  issuer: "https://accounts.google.com"
  client_id: "xxxx.apps.googleusercontent.com"
  client_secret: "secret:hobnob/auth/oidc_client_secret"
  redirect_url: "https://hobnob.example.com:8181/auth/callback"
```

**Azure AD:**
```yaml
oidc:
  issuer: "https://login.microsoftonline.com/{tenant-id}/v2.0"
  client_id: "your-app-id"
  client_secret: "secret:hobnob/auth/oidc_client_secret"
  redirect_url: "https://hobnob.example.com:8181/auth/callback"
```

### First-User Bootstrap

The first user to log in via OIDC is automatically promoted to **admin**. All subsequent users are created as **viewers** by default. Admins can change roles from the Users admin page.

## Isowebauth (SSH Key-Based)

Isowebauth uses the [isowebauth](https://github.com/hobnob/isowebauth) desktop app to sign challenges with the user's SSH private key. The server verifies signatures using `ssh-keygen -Y verify`.

### Setup

1. Install the isowebauth desktop app on each admin workstation.
2. Configure isowebauth to allow your Hobnob server's origin and the `hobnob` namespace.
3. Set `auth.method: isowebauth` in `server.yaml`:

```yaml
auth:
  method: isowebauth
  isowebauth:
    namespace: "hobnob"   # default
```

### User Management

Unlike OIDC, isowebauth requires users to be pre-created with their SSH public keys:

```bash
# Create the first admin (no auth required when DB is empty):
hobnobcli users create --username alice --role admin --ssh-key-file ~/.ssh/id_ed25519.pub

# Create additional users (requires admin auth):
hobnobcli users create --username bob --role viewer --ssh-key-file /path/to/bob.pub
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
| View dashboards, thralls, runs, compliance | Yes | Yes |
| View tasks, workflows, query results | Yes | Yes |
| View docs, federation status | Yes | Yes |
| Dispatch tasks | Yes | No |
| Run workflows | Yes | No |
| Execute queries | Yes | No |
| Manage enrollment patterns | Yes | No |
| Manage webhooks | Yes | No |
| Manage users | Yes | No |
| Run health checks | Yes | No |
| Create/download backups | Yes | No |
| Create/revoke API tokens | Yes | Yes (own only) |

When `auth.method` is `none`, all users have full access.

## API Tokens

API tokens allow the CLI and scripts to authenticate without a browser session. Tokens use the `hbt_` prefix and are bcrypt-hashed at rest.

### Creating a Token

Via the CLI (requires an active session or existing token):

```bash
# Create a token (the plaintext is shown once):
hobnobcli auth set-token $(curl -s -X POST \
  -H "Authorization: Bearer $EXISTING_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "ci-deploy", "expires_in": "720h"}' \
  https://hobnob.example.com:8181/api/v1/auth/tokens | jq -r .token)
```

Or via the REST API directly:

```bash
curl -X POST https://hobnob.example.com:8181/api/v1/auth/tokens \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "my-token", "expires_in": "720h"}'
```

### CLI Authentication

Store a token for CLI use:

```bash
hobnobcli auth set-token hbt_abc123...
hobnobcli auth status    # verify
hobnobcli auth clear     # remove
```

The token is stored at `~/.config/hobnob/token` (mode 0600).

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
| `/api/v1/users` | POST | Admin* | Create a user |
| `/api/v1/users/{id}` | PUT | Admin | Update a user |
| `/api/v1/users/{id}` | DELETE | Admin | Delete a user |

*First user creation is allowed without auth when the database has no users (bootstrap).

### CLI

```bash
hobnobcli users list
hobnobcli users create --username alice --role admin [--email alice@example.com] [--ssh-key-file ~/.ssh/id.pub]
hobnobcli users set-password --username alice
hobnobcli users delete --username alice
```

## Troubleshooting

**OIDC callback fails with "invalid state parameter":**
The session may have expired between redirect and callback. Check that `redirect_url` exactly matches what's registered with your OIDC provider.

**Isowebauth "Cannot connect to isowebauth app":**
Ensure the isowebauth desktop app is running and listening on `127.0.0.1:7890`. Check that your Hobnob server's origin is in isowebauth's allowed origins.

**"ssh-keygen: command not found":**
The server requires `ssh-keygen` in the PATH for isowebauth signature verification. Install OpenSSH on the server.

**API returns 401 with valid token:**
Check that the token hasn't expired. List your tokens with `hobnobcli auth status` or the tokens API.
