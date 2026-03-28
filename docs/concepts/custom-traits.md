
# Custom Traits

Custom trait collectors extend the agent's fact-gathering with site-specific data. They are scripts that output JSON and are run alongside the built-in collectors.

## How It Works

The server loads custom trait scripts from the traits directory and includes their output in the trait data sent to the agent. Scripts are cached with a configurable TTL.

## Script Requirements

Custom trait scripts:
- Must be executable (`chmod +x`)
- Must output valid JSON to stdout
- Must exit 0 on success
- Should complete quickly (cached with TTL to avoid repeated execution)

## Example

`custom-traits/app-version.sh`:

```bash
#!/bin/bash
version=$(/opt/app/bin/app --version 2>/dev/null || echo "unknown")
echo "{\"app\": {\"version\": \"$version\"}}"
```

This produces a trait at `custom.app.version` (custom traits are namespaced under `custom.`).

## Directory

Configure in `server.yaml`:

```yaml
paths:
  custom_traits: "custom-traits"
```

Default: `custom-traits` (relative to working directory). Place scripts in this directory. The server discovers them automatically.

## Using Custom Traits

### In When Expressions

Custom traits aren't directly available in builtin when functions, but they are available as conditional vars:

```yaml
vars:
  is_app_v2:
    trait: custom.app.version
    regex: "^2\\."

resources:
  - name: v2-config
    type: file
    target_path: /etc/app/v2.conf
    content: "..."
    when: "is_app_v2"
```


---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
### In Templates

```yaml
- name: app-info
  type: file
  target_path: /etc/app-info
  content: |
    App Version: {{ index .Traits "custom" "app" "version" }}
```

## Caching

Custom trait collectors are cached with a configurable TTL:

```yaml
tuning:
  traits_scanner_ttl: "30s"  # default
```

Traits are re-collected when the cache expires or when the agent sends new traits during check-in.

## Related

- [Custom Executors](custom-executors.md) — Custom resource types
- [Trait Collectors](../reference/traits/_index.md) — Built-in trait reference
- [When Expressions](when-expressions.md) — Using traits in conditionals
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
