
# Users

The Users page (`/admin/users`) provides admin-only user account management for the Vigo web UI and API.

## Overview

This page is only visible when authentication is enabled (basic, OIDC, or isowebauth mode). It lists all registered user accounts and allows admins to create, edit, and delete users.

## Features

- **User list** — table of all accounts with username, role (admin/viewer/compliance), authentication method, and creation date
- **Create user** — add new user accounts with username, password, and role assignment
- **Delete user** — remove user accounts (cannot delete the last admin)
- **Set password** — reset a user's password (basic auth only)

## Roles

| Role | Permissions |
|------|-------------|
| Admin | Full access to all pages, API endpoints, and user management |
| Viewer | Read-only access to dashboards, envoy details, runs, audit log, and compliance data |
| Compliance | Compliance pages + supporting-doc upload only — no fleet, audit-log, tasks, or config access |

The first user created during initial setup automatically receives the admin role. The `compliance` role is intended for GRC staff who upload supporting documentation (BAA, IR plan, training records) and view framework reports; it is **not** for auditors — auditors should be assigned `admin` or `viewer`. See [Authentication](../../operations/authentication.md) for the full per-permission matrix.

## API Tokens

Users can create API tokens for programmatic access. Tokens use the `vgot_` prefix and are stored as bcrypt hashes. See [Authentication](../../operations/authentication.md) for details.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
