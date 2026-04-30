
# Users

User account management for the Vigo web UI and REST API. Vigo does not ship a dedicated `/admin/users` web page — user CRUD is CLI-only via `vigocli webusers`, which talks to the `/api/v1/users` REST endpoint. The role-aware nav in the web UI hides surfaces a user lacks permission for, but the user list itself is administered from the server host.

## Where to manage users

- **CLI** — `vigocli webusers list / create / delete` from the server host (or any host with `vigocli` and a configured server URL + admin token). See [`vigocli webusers`](../cli/webusers.md).
- **REST** — `GET / POST / PUT / DELETE /api/v1/users`. The list and mutation endpoints require admin role; the bootstrap `POST /api/v1/users` is open until the first admin exists, then admin-gated.
- **Setup page** (`/setup`) — first-run only. The web UI redirects here when the seeded admin account has no password set; the operator submits a password and the page disappears once setup is complete.

## Roles

| Role | Permissions |
|------|-------------|
| Admin | Full access to all pages, API endpoints, and user management |
| Viewer | Read-only access to dashboards, envoy details, runs, audit log, and compliance data |
| Compliance | Compliance pages + supporting-doc upload only — no fleet, audit-log, tasks, or config access |

The first user created during initial setup automatically receives the admin role. The `compliance` role is intended for GRC staff who upload supporting documentation (BAA, IR plan, training records) and view framework reports; it is **not** for auditors — auditors should be assigned `admin` or `viewer`. See [Authentication](../../operations/authentication.md) for the full per-permission matrix.

## API Tokens

Users can create API tokens for programmatic access. Tokens use the `vgot_` prefix and are stored as bcrypt hashes. Token creation, listing, and deletion ride the `tokens.manage` permission and are scoped to the calling user — every authenticated role can manage its own tokens. See [Authentication](../../operations/authentication.md) for details.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
