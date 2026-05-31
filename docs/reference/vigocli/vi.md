
# vigocli vi

Edit a `.vgo` file in vim with Vigo-aware syntax highlighting and automatic validation on save.

## Usage

```bash
vigocli vi <file.vgo>
```

Files must be within `/srv/vigo/`. Relative paths are resolved against `/srv/vigo/stacks/stockpile/`, so you can use short names:

```bash
vigocli vi configcrates/nginx.vgo           # opens /srv/vigo/stacks/stockpile/configcrates/nginx.vgo
vigocli vi envoys/web-servers.vgo     # opens /srv/vigo/stacks/stockpile/envoys/web-servers.vgo
```

## Features

### Syntax highlighting

The editor provides YAML-based syntax highlighting with Vigo-specific enhancements:

- **Top-level keys** (`name`, `vars`, `resources`, `configcrates`, `usercrates`, `roles`, `envoys`, `depends_on`, `when`, `environment`, `match`, `exclude_configcrates`) -- bold blue
- **Resource attributes** (`type`, `state`, `targetpath`, `content`, `package`, `service`, `command`, etc.) -- blue
- **Resource type values** (`file`, `package`, `nonrepo_package`, `service`, `user`, `exec`, `repository`, `source_package`, `cron`) -- bold orange
- **State values** (`present`, `absent`, `running`, `stopped`, `restarted`, `reloaded`, `directory`) -- green
- **Secret references** (`secret:...`) -- bold red
- **Go template expressions** (`{{ }}`) -- purple
- **Idempotency guards** (`onlyif`, `unless`) -- bold orange
- **Environment names** (`production`, `staging`, `development`, `testing`) -- light blue
- **Match patterns** -- bold white

### Validation on save

When saving a file, the editor automatically runs `vigocli validate` against the buffer. If validation fails:

- Error messages are displayed in the editor
- The write is aborted
- Use `:w!` to force-save past validation

After exiting vim, a post-save validation check runs as well.

### Editor configuration

The editor is pre-configured with:

- 2-space indentation (spaces, not tabs)
- Auto-indent enabled
- Line numbers and status bar hidden for a clean editing experience

## Examples

### Edit a configcrate file

```bash
vigocli vi configcrates/nginx.vgo
```

### Edit with full path

```bash
vigocli vi /srv/vigo/stacks/stockpile/configcrates/nginx.vgo
```

## Related

- [validate](validate.md) -- validate .vgo files from the command line
- [preview](preview.md) -- preview planned changes after editing
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
