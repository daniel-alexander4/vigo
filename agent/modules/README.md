# Resource Executors

## Summary

Resource executors implement idempotent state management. Each executor checks current state, determines if changes are needed, and only acts on drift. All 23 built-in executors follow the same interface, and custom executors can be added via a JSON script protocol.

- **Interface:** `Execute(ctx, params map[string]string, dryRun bool) ResourceResult`
- **Idempotent:** check current state, compare to desired, act only if different
- **Dry-run:** check and report without making changes
- **Fallback naming:** the runner injects `params["name"]` from the resource name for all executors

## Executor Interface

```go
type Executor interface {
    Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult
}

type ResourceResult struct {
    ModuleName   string
    ResourceType string
    ResourceName string
    Action       ActionTaken  // "already in desired state", "created", "updated", "deleted", "skipped"
    Changed      bool
    Error        error
    DurationMs   int32
}
```

## Built-in Executors (23 types)

| Type | Primary Param | Key Params | Description |
|------|---------------|------------|-------------|
| file | targetpath | content, mode (0644), owner, group, state | Manage file content, permissions, ownership |
| package | package | state (present/absent), version | Install/remove packages via system package manager |
| service | service | state (running/stopped/restarted/reloaded), enabled | Manage systemd services |
| user | username | state, uid, shell, home, group, comment, password, groups, authorized_keys | Manage system users |
| exec | command | creates, onlyif, unless, cwd, user | Run commands with idempotency guards |
| archive | url | targetpath, binary, sha256, unless, mode (0755), state | Download and extract archives |
| repository | repo_name | key_url, repo, state | Manage package repositories |
| cron | name | job, schedule, minute/hour/day/month/weekday, user (root), state | Manage cron jobs |
| process | process | state (running/stopped), signal (TERM), count/min/max, restart, kill_over | Process monitoring and control |
| file_line | path | line, match (regex), state, position (end/begin/after:pattern) | Ensure line present/absent in file |
| group | group | state, gid | Manage system groups |
| host | hostname | ip, aliases, state | Manage /etc/hosts entries |
| sysctl | key | value, state, persist (true) | Manage kernel parameters |
| mount | path | device, fstype, options (defaults), state (mounted/unmounted/present/absent) | Manage filesystem mounts |
| hostname | hostname | -- | Set system hostname |
| timezone | timezone | -- | Set system timezone |
| sshkey | hostname | type, key, aliases, file (/etc/ssh/ssh_known_hosts), state | Manage SSH known hosts |
| firewall | port | proto (any), action (allow/deny/reject), from, to, direction (in), state, comment | Manage UFW firewall rules |
| kernel_module | module | state (present/absent/loaded/unloaded), persist (true), params | Manage kernel modules |
| swap | path | size (MB), state, priority | Manage swap files |
| tmpfile | name | entry_type (d), path, mode (0755), user, group, age, state | Manage tmpfiles.d entries |
| ini | path | section, key, value, state, separator (=) | Edit INI file key-value pairs |
| json_file | path | key (dot-notation), value (JSON literal), state, indent ("  ") | Edit JSON file values |

## Examples

### Package + service with subscribes

```yaml
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present

  - name: nginx-service
    type: service
    service: nginx
    state: running
    enabled: true
    subscribes: [nginx-config]
```

### File with template

```yaml
  - name: nginx-config
    type: file
    targetpath: /etc/nginx/nginx.conf
    content: |
      worker_processes {{ .Vars.worker_count }};
      server {
        listen {{ .Vars.port }};
        server_name {{ .Traits.network.hostname }};
      }
    mode: "0644"
    owner: root
    notify: [nginx-service]
```

### User with authorized keys

```yaml
  - name: deploy-user
    type: user
    username: deploy
    shell: /bin/bash
    groups: docker,sudo
    authorized_keys: |
      ssh-ed25519 AAAA... admin@example.com
      ssh-rsa AAAA... ci@example.com
```

### Exec with guards

```yaml
  - name: init-db
    type: exec
    command: /usr/bin/initdb --pgdata=/var/lib/pgsql/data
    creates: /var/lib/pgsql/data/PG_VERSION
    user: postgres
```

## Package Manager Backends

Seven backends implement the `PackageManager` interface:

| Backend | OS Family | Tool |
|---------|-----------|------|
| AptManager | Debian/Ubuntu | apt-get / dpkg |
| DnfManager | Fedora/RHEL/Rocky | dnf / rpm |
| YumManager | CentOS | yum / rpm |
| ApkManager | Alpine | apk |
| PacmanManager | Arch | pacman |
| BrewManager | macOS | brew |
| ZypperManager | openSUSE/SLES | zypper |

Repository management is handled by `AptRepoManager` (Debian) and `DnfRepoManager` (RHEL).

## Custom Executors

Scripts placed in `--executors-dir` (default: `/etc/hobnob-node/executors/`) extend the agent with new resource types. They communicate via a JSON protocol over stdin/stdout.

**Input (stdin):**
```json
{"params": {"key": "value"}, "dry_run": false}
```

**Output (stdout):**
```json
{"action": "created", "changed": true, "error": ""}
```

Valid actions: `"created"`, `"updated"`, `"deleted"`, `"skipped"`, `"already in desired state"`

## Dry-Run

All executors follow the same pattern:

1. Check current state
2. Determine needed action
3. If `dryRun` is true: return the result without acting
4. Otherwise: execute the action and return the result
