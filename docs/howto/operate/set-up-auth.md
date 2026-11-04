
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

Basic authentication stores user accounts in the database and a bcrypt hash of the password in the unlock-gated secrets vault (the hash itself is held under the master key; the plaintext is never written). The login handler resolves the stored hash on every attempt and verifies the submitted password against it. This separation keeps credentials out of the database; the vault's unlock gate keeps casual access off the operator surface.

### Initial Setup

There is no auto-seeded `admin` user; the server starts up with no web users at all. Create your per-operator admin from the server host after enrolling the local agent:

```bash
# First enroll the local agent so the server can verify the OS-user mapping:
curl -sSfk https://localhost:8443/bootstrap | sudo sh

# Then create yourself as the first admin. <yourname> must be a real human
# OS account on this host (uid≥1000, real shell). The CLI auto-reads your
# pubkey from ~<yourname>/.ssh/id_ed25519.pub (fallback id_rsa.pub).
sudo vigocli webusers create --username <yourname> --role admin

# Set your login password (prompts; stores a bcrypt hash):
vigocli webusers set-password --username <yourname>
```

Sign in at `https://localhost:8443/login`. Passwords are stored as a **bcrypt hash** at `vigo/web/auth/<username>` inside the master-key AES-256-GCM envelope — the plaintext is never written — and the login handler verifies each attempt against that hash. Set passwords only with `vigocli webusers set-password`; writing the key directly with `vigocli secrets set` stores the value verbatim and will not authenticate.

**Admin webusers map 1:1 with OS users.** The username on the web side must equal a human OS account on the server host — this gates audit attribution to a real person and seeds the pubkey for browser scrier sessions. Viewer and compliance users are unconstrained; they're one-offs (auditors, partners, read-only access) without OS counterparts.

### Adding Users

```bash
# Another per-operator admin (real OS user required):
sudo vigocli webusers create --username alice --role admin

# Viewer / compliance — no OS-user constraint, --ssh-key-file optional:
sudo vigocli webusers create --username auditor --role viewer --email auditor@example.com
sudo vigocli webusers create --username carol --role compliance --email carol@example.com

# Set passwords (prompts; stores a bcrypt hash):
vigocli webusers set-password --username alice
```

### Resetting Passwords

```bash
vigocli webusers set-password --username <username>
```

After writing the new password, this also revokes the user's active web
sessions, so any session opened with the old password stops working
immediately (important after a compromise-driven reset).

### Login Rate Limiting

The HTTP login and TOTP endpoints are rate-limited per client IP (10
attempts/minute, burst 10) to bound password and TOTP-code brute force. It is
on by default and needs no configuration — a human mistyping a password a few
times never notices it, while an automated attacker gets a `429 Too Many
Requests`. Each throttled attempt is recorded as an `auth.rate_limited` audit
event. (This is the HTTP counterpart to the gRPC check-in rate limiter.)

### CLI Access (Local-Admin Token)

When `vigocli` runs on the server host it authenticates with the **local-admin token** — a credential the server mints on first boot and persists at `/srv/vigo/cli-admin-token` (mode `0600`, `root`-owned). `vigocli` reads it automatically when no per-user token is configured, so `sudo vigocli ...` works on the server host with no setup. Remote CLI access still requires a per-user API token via `vigocli auth set-token`.

The token gates on the *filesystem*, not the connecting IP: only a caller that can read the `0600` root-owned file is admin. A non-root user with a shell on the server host — who cannot read the file — gets `401`. This is the same gate that already protects the secrets master key and the `vigocli` binary itself. The server checks that gate at boot: if the token file is readable by group or other, is not owned by the server's user, is a symlink, or does not hold a `vgot_` token, vigosrv refuses to start and says why. Fix the mode (`chmod 600`) or delete the file; a fresh token is minted on the next boot.

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

Every login uses PKCE (`S256`) and sends a nonce that the returned ID token must echo, so the identity provider must accept a `code_challenge` on the authorization request. All the providers below do. On each login Vigo refreshes the user's display name and email from the ID token; role, permissions and SSH public key stay as stored.

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

### First-Admin Bootstrap

OIDC users are created as **viewers** by default — there is no automatic
"first user becomes admin" promotion (that would hand admin to whoever logs in
first on an IdP that allows self-registration). Grant the first admin one of
two ways:

- **Opt-in bootstrap (self-service):** name the intended admin's identity under
  `oidc:` so their first login lands as admin deterministically:

  ```yaml
  oidc:
    # ... issuer/client_id/etc ...
    bootstrap_admin_email: "admin@example.com"   # match on the email claim
    # or, when the IdP's email isn't stable:
    # bootstrap_admin_subject: "auth0|abc123"    # match on the sub claim
  ```

  Both empty (the default) means no auto-admin. **Email matching is honored
  only when the IdP marks the address verified** (`email_verified: true` in the
  ID token) — otherwise it's ignored, so an IdP that lets a user self-set an
  unverified email can't be used to claim the admin address. On IdPs where email
  verification is uncertain, use `bootstrap_admin_subject` (the `sub` claim is
  IdP-assigned and can't be spoofed).

- **CLI (out-of-band):** have the user log in once (creating their viewer row),
  then promote them from the server host:

  ```bash
  sudo vigocli webusers set-role --username <their-email> --role admin
  ```

Admins can change roles from the Users admin page thereafter.

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

The following paths are accessible without authentication regardless of the configured auth method. This ensures agent bootstrap and metrics scraping work without credentials.

| Path prefix | Purpose |
|-------------|---------|
| `/bootstrap` | Agent bootstrap script |
| `/api/v1/agent/` | Agent binary downloads |
| `/api/v1/bootstrap/` | Agent enrollment registration |
| `/metrics` | Prometheus metrics scraping |
| `/static/` | CSS, JavaScript, and image assets |
| `/auth/` | Login and SSO callback handlers |
| `/login`, `/totp` | Login and second-factor pages |

Agent-facing swarm reads are public too: the fleet pubkey map, the puddle name map, curator `resolve` and `blob`, and the poolq `topics` and `topic/…` reads. The poolq `block`/`unblock` moderation routes are admin-only.

## Cross-Origin Request Protection

The server refuses a state-changing browser request (`POST`, `PUT`, `PATCH`, `DELETE`) that comes from another origin, with `403`. It goes by the browser's `Sec-Fetch-Site` header, or the `Origin` header on browsers that do not send it. This stops a page on another site from using a logged-in operator's session cookie to act on the server. Requests that carry neither header (vigocli, agents, `curl`) and requests authenticated with an `Authorization: Bearer` API token are not affected. If you put the UI behind a reverse proxy, the proxy must pass the original `Host` header through.

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
| Manage users | Yes | No | No |
| Run health checks | Yes | No | No |
| Create/download backups | Yes | No | No |
| Create/revoke API tokens | Yes | Yes (own only) | Yes (own only) |

The `compliance` role is for GRC staff and compliance-document uploaders. It is **not for auditors** — auditors should be assigned `admin` or `viewer` depending on the depth of access required. The role exists so compliance staff can do their job (upload BAA, IR plans, training records; view framework reports; download audit-bundle evidence) without being granted broader fleet, query, or audit-log access.

### Fine-Grained Permissions

Beyond the role labels, each user has a permission set stored as a JSON array. Admins always have all permissions regardless of the stored slice. Viewer defaults: `fleet.read`, `audit.read`, `compliance.read`. Compliance defaults: `compliance.read`, `compliance.docs.write` (no `audit.read`).

| Permission | Description | Admin | Viewer | Compliance |
|------------|-------------|-------|--------|------------|
| `fleet.read` | View envoys, runs, inventory, tasks, workflows, security, health; use the web-UI AI drawer | Yes | Yes | No |
| `fleet.write` | Modify envoy tags, maintenance, force update | Yes | No | No |
| `config.publish` | Publish / freeze / reload configuration | Yes | No | No |
| `config.rollback` | Roll back to a prior config version | Yes | No | No |
| `push.execute` | Dispatch ad-hoc tasks | Yes | No | No |
| `query.execute` | Execute live trait queries (incl. REST `POST /api/v1/ask`) | Yes | No | No |
| `workflow.execute` | Run / abort workflows | Yes | No | No |
| `audit.read` | View raw audit-log chain | Yes | Yes | **No** |
| `compliance.read` | View framework reports + audit bundle | Yes | Yes | Yes |
| `compliance.docs.write` | Upload supporting compliance documentation | Yes | No | Yes |
| `compliance.docs.purge` | Hard-delete soft-deleted compliance docs | Yes | No | No |

The `audit.read` exclusion on the compliance role is deliberate: the raw hash-chained audit log is admin-and-viewer-only, while the audit-bundle export (a per-framework compliance evidence artifact built from the same data) rides `compliance.read` so the role can download what an auditor asks for without being handed the entire audit stream.

A user's permission set is the default for their role; there is no API for assigning a custom set. Managing users, API tokens and enrollment patterns needs the admin role itself, not a permission. A role the server does not recognise gets no permissions.

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
vigocli webusers set-password --username alice
vigocli webusers delete --username alice
```

## Audit Logging

All authentication and user management events are recorded in the tamper-evident audit chain (`audit_entries` table). These events support HIPAA 164.312(b) audit controls and intrusion detection.

**Event-type convention.** Every audit event type is `<domain>.<resource>.<action>` (three-level) when the domain has multiple resources worth disambiguating, otherwise `<domain>.<action>` (two-level). Lowercase, dot-separated. Examples below.

| Event Type | Trigger | Actor |
|------------|---------|-------|
| `auth.login` | Successful login (basic, OIDC, isowebauth) | Username |
| `auth.login_failed` | Failed login (bad password, unknown user, bad signature) | Attempted username |
| `auth.logout` | User logout | Username |
| `user.create` | User account created | Admin who created |
| `user.delete` | User account deleted | Admin who deleted |
| `user.role_change` | Role updated (only when role actually changes) | Admin who changed |
| `user.totp_setup` | TOTP secret generated for user (pre-confirm) | Subject username |
| `user.totp_enable` | TOTP enabled after operator-confirmed code | Subject username |
| `user.totp_disable` | TOTP disabled | Subject username |
| `token.create` | API token created | Token owner |
| `token.revoke` | API token revoked | Token owner |
| `secret.accessed` | Secret revealed via `vigocli secrets reveal` | CLI user (via `X-Vigo-User` header) |
| `secret.rotation_acked` | Server acknowledged a secret rotation reported by `vigocli secrets set` | Session user |
| `secrets.unlocked` | Secrets vault unlocked | Session user |
| `secrets.locked` | Secrets vault locked | Session user |
| `secrets.passphrase_rotated` | Unlock passphrase rotated | Session user |
| `secrets.passphrase_reset` | Unlock passphrase reset via break-glass | Session user |
| `spanner.id_initialized` | Founding bolt initialised the spanner | Founder pubkey-hex |
| `spanner.join_offered` / `.join_accepted` / `.join_rejected` | Joining bolt's request to admit was offered / accepted / rejected | Joiner pubkey-hex |
| `spanner.bolt_admitted` / `.bolt_invite_issued` / `.bolt_push` / `.bolt_drain` / `.peer_reassign` | Spanner federation control verbs | Operator session user |

Other event domains follow the same convention: `compliance.docs.*` (upload/replace/delete/purge/link_added/link_removed/download), `compliance.bundle.download`, `curator.s3_credential.{create,revoke}`, `db.{vacuum,checkpoint,prune,analyze}`, `envoy.{enroll,delete,revoke,force_update,force_update_all,maintenance_set,maintenance_cleared}`, `config.{reload,publish_recorded,rollback,freeze,unfreeze,retract,published}`, `task.{dispatch,cancel,cancel_running,blocked}`, `workflow.{dispatch,abort}`, `sandgorgon.{commission,decommission,return_to_available,provision,import_csv,image.register,image.remove}`, `swarm.{cleanup,curator.block,curator.unblock,poolq.block,poolq.unblock,filecast.distribute,filecast.revoke,filecast.denied}`, `peer.promote`, `scrier.connect`, `security.hardening_warning`, `webusers.legacy_resigned`, `emergency.access`.

> **Rename notice (2026-05-25).** The audit-event taxonomy was unified to dotted-hierarchical in this release. Chain entries written before the rename keep their original spelling (the SHA-256 hash chain is append-only by design). Historical names you may find in older chain rows: `spanner_id_initialized` → `spanner.id_initialized`, `spanner_join_offered` → `spanner.join_offered`, `spanner_join_accepted` → `spanner.join_accepted`, `spanner_join_rejected` → `spanner.join_rejected`, `spanner_bolt_admitted` → `spanner.bolt_admitted`, `spanner_bolt_push` → `spanner.bolt_push`, `spanner_bolt_drain` → `spanner.bolt_drain`, `spanner_peer_reassign` → `spanner.peer_reassign`, `spanner_bolt_invite_issued` → `spanner.bolt_invite_issued`, `secret_accessed` → `secret.accessed`, `secrets_unlocked` → `secrets.unlocked`, `secrets_locked` → `secrets.locked`, `secrets_passphrase_rotated` → `secrets.passphrase_rotated`, `secrets_passphrase_reset` → `secrets.passphrase_reset`, `compliance.waiver_created` → `compliance.waiver.created`, `compliance.waiver_revoked` → `compliance.waiver.revoked`, `compliance.docs.link.add` → `compliance.docs.link_added`, `compliance.docs.link.remove` → `compliance.docs.link_removed`. `vigocli audit list --type` does not translate — query the original name to find historical entries, the new name for post-rename entries.

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
- **Audit who logged in when** → every auth event lands in the [hash-chained audit log](../compliance/compliance-reporting.md).
- **Auth is misbehaving** → [Troubleshoot common issues](troubleshoot.md).

---
*Verified on Vigo 0.51.6 · 2026-05-13.*
