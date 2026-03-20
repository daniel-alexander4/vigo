
# Locale

Reports the system's locale settings.

## Trait Path
`locale`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `locale.lang` | string | `"en_US.UTF-8"` | System language setting |
| `locale.lc_ctype` | string | `"en_US.UTF-8"` | Character classification locale |
| `locale.lc_numeric` | string | `"en_US.UTF-8"` | Numeric formatting locale |
| `locale.lc_time` | string | `"en_US.UTF-8"` | Date/time formatting locale |
| `locale.lc_collate` | string | `"en_US.UTF-8"` | String collation locale |
| `locale.lc_monetary` | string | `"en_US.UTF-8"` | Monetary formatting locale |
| `locale.lc_messages` | string | `"en_US.UTF-8"` | Message display locale |

The exact fields depend on the system's `locale` output. All key names are lowercased.

## Collection Method
Runs the `locale` command and parses its `KEY=VALUE` output. Keys are converted to lowercase and surrounding quotes are stripped from values. Returns an empty object if the `locale` command is unavailable or fails.

## Using in When Expressions
```yaml
- name: install-locales
  type: package
  package: locales
  when: "os_family('debian')"
```

## Using in Templates
```yaml
- name: locale-info
  type: file
  target_path: /var/lib/vigo/locale.txt
  content: |
    Language: {{ .Traits.locale.lang }}
```

## Platform
Linux
