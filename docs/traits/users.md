# Users

Enumerates system users classified as human, OS, or third-party accounts, and lists currently logged-in users.

## Trait Path
`users`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `users.logged_in` | array of strings | `["dan", "alice"]` | Currently logged-in usernames |
| `users.human` | array of strings | `["dan", "alice", "bob"]` | Human user accounts (UID >= 1000, interactive shell) |
| `users.os` | array of strings | `["root", "daemon", "nobody"]` | Well-known OS accounts |
| `users.thirdparty` | array of strings | `["postgres", "nginx", "redis"]` | Third-party service accounts |

## Collection Method
Logged-in users are collected by parsing the output of the `who` command. Account classification is based on `/etc/passwd`:
- **Human**: UID >= 1000, UID != 65534, and shell is not a nologin shell (`/usr/sbin/nologin`, `/sbin/nologin`, `/bin/false`)
- **OS**: UID < 1000 (or 65534) and name matches a well-known OS account list (root, bin, daemon, sys, etc.) or starts with `systemd-`
- **Third-party**: UID < 1000 (or 65534) but not in the OS account list (e.g., postgres, nginx, redis)

## Using in When Expressions
```yaml
- name: create-deploy-user
  type: user
  username: deploy
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: user-report
  type: file
  targetpath: /var/lib/hobnob/users.txt
  content: |
    Human users: {{ range .Traits.users.human }}{{ . }} {{ end }}
    Logged in: {{ range .Traits.users.logged_in }}{{ . }} {{ end }}
```

## Platform
Linux
