# hobnobcli vi

Edit a `.hnb` file in vim with Hobnob-aware syntax highlighting and automatic validation on save.

Can also be invoked directly as `hnbvi` for convenience.

## Usage

```bash
hobnobcli vi <file.hnb>
```

or

```bash
hnbvi <file.hnb>
```

Files must be within `/srv/hobnob/policy/`. Relative paths are resolved against `/srv/hobnob/policy/proposed/`, so you can use short names:

```bash
hnbvi modules/nginx.hnb           # opens /srv/hobnob/policy/proposed/modules/nginx.hnb
hnbvi thralls/web-servers.hnb     # opens /srv/hobnob/policy/proposed/thralls/web-servers.hnb
```

## Features

### Syntax highlighting

The editor provides YAML-based syntax highlighting with Hobnob-specific enhancements:

- **Top-level keys** (`name`, `vars`, `resources`, `modules`, `roles`, `thralls`, `depends_on`, `when`, `environment`, `environment_overrides`, `match`, `conditional_vars`) -- bold blue
- **Resource attributes** (`type`, `state`, `targetpath`, `content`, `package`, `service`, `command`, etc.) -- blue
- **Resource type values** (`file`, `package`, `service`, `user`, `exec`, `repository`, `archive`, `cron`) -- bold orange
- **State values** (`present`, `absent`, `running`, `stopped`, `restarted`, `reloaded`, `directory`) -- green
- **Secret references** (`secret:...`) -- bold red
- **Go template expressions** (`{{ }}`) -- purple
- **Idempotency guards** (`creates`, `onlyif`, `unless`) -- bold orange
- **Environment names** (`production`, `staging`, `development`, `testing`) -- light blue
- **Match patterns** -- bold white

### Validation on save

When saving a file, the editor automatically runs `hobnobcli validate` against the buffer. If validation fails:

- Error messages are displayed in the editor
- The write is aborted
- Use `:w!` to force-save past validation

After exiting vim, a post-save validation check runs as well.

### Editor configuration

The editor is pre-configured with:

- Line numbers enabled
- 2-space indentation (spaces, not tabs)
- Auto-indent enabled
- Status line showing file type and position

## Examples

### Edit a module file

```bash
hnbvi modules/nginx.hnb
```

### Edit with full path

```bash
hobnobcli vi /srv/hobnob/policy/proposed/modules/nginx.hnb
```

## Related

- [validate](validate.md) -- validate .hnb files from the command line
- [preview](preview.md) -- preview planned changes after editing
