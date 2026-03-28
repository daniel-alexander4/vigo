
# selinux

Manage SELinux security contexts on files and directories. Linux only.

## Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `path` | Yes | File or directory path to manage |
| `context` | No | Full SELinux context (e.g., `system_u:object_r:httpd_sys_content_t:s0`) |
| `selinux_type` | No | SELinux type component only (e.g., `httpd_sys_content_t`) -- shorthand |
| `state` | No | `present` (default) or `absent` for persistent fcontext rules |

Either `context` or `selinux_type` must be specified.

## Behavior

**Check:** Runs `ls -Z <path>` to get the current SELinux context. Compares with the desired context.

**Apply (full context):** `chcon <context> <path>`

**Apply (selinux_type only, persistent):**
1. `semanage fcontext -a -t <selinux_type> <path>` (or `-m` to modify existing)
2. `restorecon -v <path>`

**Remove (state: absent):**
1. `semanage fcontext -d <path>`
2. `restorecon -v <path>`

## Examples

### Set context on a web content directory

```yaml
- name: web-content-context
  type: selinux
  path: /var/www/html
  selinux_type: httpd_sys_content_t
```

### Set a full context

```yaml
- name: custom-context
  type: selinux
  path: /opt/app/data
  context: "system_u:object_r:usr_t:s0"
```

### SELinux context on file resources

The `file` executor also supports a `secontext` parameter to set the SELinux context after writing a file:

```yaml
- name: app-config
  type: file
  target_path: /etc/app/config.yml
  content: "..."
  secontext: "system_u:object_r:etc_t:s0"
```

## Platform Support

Linux only. Requires `ls`, `chcon`, and optionally `semanage`/`restorecon` for persistent rules.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
