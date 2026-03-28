
# vigogo vi

Edit a `.vgo` file in vim with Vigo-aware syntax highlighting and automatic validation on save.

## Usage

```bash
vigogo vi <file.vgo>
```

Files must be within `/srv/vigo/stack/`. Relative paths are resolved against `/srv/vigo/stack/stage/`, so you can use short names:

```bash
vigogo vi modules/nginx.vgo           # opens /srv/vigo/stack/stage/modules/nginx.vgo
vigogo vi envoys/web-servers.vgo     # opens /srv/vigo/stack/stage/envoys/web-servers.vgo
```

## Features

### Syntax highlighting

The editor provides YAML-based syntax highlighting with Vigo-specific enhancements:

- **Top-level keys** (`name`, `vars`, `resources`, `modules`, `roles`, `envoys`, `depends_on`, `when`, `environment`, `environment_overrides`, `match`, `conditional_vars`) -- bold blue
- **Resource attributes** (`type`, `state`, `targetpath`, `content`, `package`, `service`, `command`, etc.) -- blue
- **Resource type values** (`file`, `package`, `service`, `user`, `exec`, `repository`, `archive`, `cron`) -- bold orange
- **State values** (`present`, `absent`, `running`, `stopped`, `restarted`, `reloaded`, `directory`) -- green
- **Secret references** (`secret:...`) -- bold red
- **Go template expressions** (`{{ }}`) -- purple
- **Idempotency guards** (`creates`, `onlyif`, `unless`) -- bold orange
- **Environment names** (`production`, `staging`, `development`, `testing`) -- light blue
- **Match patterns** -- bold white

### Validation on save

When saving a file, the editor automatically runs `vigogo validate` against the buffer. If validation fails:

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
vigogo vi modules/nginx.vgo
```

### Edit with full path

```bash
vigogo vi /srv/vigo/stack/stage/modules/nginx.vgo
```

## Related

- [validate](validate.md) -- validate .vgo files from the command line
- [preview](preview.md) -- preview planned changes after editing
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
