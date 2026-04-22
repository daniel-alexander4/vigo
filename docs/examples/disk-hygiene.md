
# Disk Hygiene

Prevent disk and inode exhaustion with a role that bundles five focused modules. Each module enforces one cleanup policy — assign the whole role or pick individual modules.

## The Role

```yaml
# scaffolding/roles.vgo
roles:
  - name: disk-hygiene
    modules: [logrotate, journal-cap, tmp-aging, package-cache, docker-prune]
```

Assign it to a node:

```yaml
match:
  - pattern: "*.web.example.com"
    roles: [bastion, disk-hygiene]
```

## Modules

### logrotate (already exists)

Installs logrotate and enforces rotation for `/var/log/*.log` and `/var/log/*/*.log`. Many service modules (nginx, postgresql, haproxy, etc.) ship their own logrotate resources — this module covers the system-wide catch-all.

### journal-cap

Drops a systemd-journald config into `/etc/systemd/journald.conf.d/` that caps total journal size and retention:

```yaml
vars:
  journal_max_use: "500M"      # max disk space for journal files
  journal_max_retention: "30day"  # delete entries older than this
```

Notifies `systemd-journald` to reload after changes.

### tmp-aging

Writes a `tmpfiles.d` config that removes old files from `/tmp` and `/var/tmp`:

```yaml
vars:
  tmp_max_age: "7d"       # /tmp cleanup threshold
  var_tmp_max_age: "30d"  # /var/tmp cleanup threshold
```

Enforced by `systemd-tmpfiles-clean.timer` (runs daily on most distros, no extra service needed).

### package-cache

OS-family conditional — prevents stale package downloads from accumulating:

- **Debian/Ubuntu:** Sets `APT::Periodic::AutocleanInterval` so apt removes superseded `.deb` files every N days (default 7).
- **RHEL/Fedora:** Sets `keepcache=0` in `dnf.conf` and runs `dnf clean all` if the cache exceeds 500 MB.

### docker-prune

Creates a systemd timer that runs `docker system prune -af` on a schedule. Removes stopped containers, dangling images, unused networks, and build cache older than `prune_age`:

```yaml
vars:
  prune_age: "168h"      # 7 days
  prune_schedule: "weekly"  # systemd OnCalendar value
```

All resources are gated with `when: "has_command('docker')"` — the module is a no-op on machines without Docker.

## Per-node overrides

Override vars from the match block or from `common.vgo`:

```yaml
match:
  - pattern: "build-*.ci.example.com"
    roles: [disk-hygiene]
    vars:
      journal_max_use: "200M"
      tmp_max_age: "1d"
      prune_schedule: "daily"
      prune_age: "24h"
```

## What this doesn't do

This role enforces **preventive policies** — it configures the system so disk usage stays under control. It does not:

- **Scan for and delete arbitrary large files.** Use `vigocli query` to find large files across the fleet when an alert fires.
- **Alert on disk/inode thresholds.** That's the job of the disk trait collector + server-side alerting (webhooks/SMTP).
- **Replace service-specific logrotate configs.** Service modules (nginx, postgres, etc.) manage their own log rotation. This role handles system-wide defaults.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
