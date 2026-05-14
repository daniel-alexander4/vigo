
# Users (Windows)

Enumerates local user accounts on Windows.

## Trait Path
`users`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `users.users` | array of strings | `["Administrator", "Dan", "Guest"]` | List of local user account names |

## Collection Method
Runs `net user` and parses the output. The command outputs a header line, a separator of dashes, then user names in columnar format (multiple names per line, whitespace-separated), ending with "The command completed successfully." All names between the dash separator and the completion message are extracted.

## Using in When Expressions
```yaml
- name: create-deploy-user
  type: user
  username: deploy
  when: "os_family('windows')"
```

## Using in Templates
```yaml
- name: user-report
  type: file
  target_path: C:\ProgramData\vigo\users.txt
  content: |
    Local users: {{ range .Traits.users.users }}{{ . }} {{ end }}
```

## Platform
Windows
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
