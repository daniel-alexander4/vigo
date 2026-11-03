
# vigocli license

View, install, and inspect the server license that governs envoy limits.

## Subcommands

### license status

Show current license tier, envoy usage, enforcement stage, and expiry.

```bash
vigocli license status
```

**Output:**

```
Enforcement Stage: Compliant

License:
  License ID:  lic-20260313-acme
  Customer:    Acme Corp (acme)
  Tier:        enterprise
  Version:     1
  Issued:      2026-03-13
  Expires:     2027-03-13
  Days left:   365

Envoys: 12 / 500
```

### license install

Copy a `.lic` file to the server's license directory and trigger a reload via SIGHUP. First-time install path; for rotation of an already-installed license, use `update` instead — it handles the [loader's alphabetical-first quirk](#multiple-lic-files) atomically.

```bash
vigocli license install --file /path/to/license.lic
```

**Flags:**

| Flag | Required | Description |
|------|----------|-------------|
| `--file` | Yes | Path to the `.lic` file |

The command validates the license file (signature + expiry) before installing and sends SIGHUP to the running `vigosrv` process for live reload.

### license update

Atomically rotate the active `.lic`. Validates the new file (signature + machine fingerprint + expiry) before touching anything on disk, archives the existing `.lic` to `/srv/vigo/license/.archive/` with a UTC timestamp suffix, installs the new file, sends SIGHUP, and polls the local REST API to confirm the new `license_id` is active.

```bash
vigocli license update --file /path/to/license.lic
```

**Flags:**

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--file` | Yes | — | Path to the new `.lic` file |
| `--ca-file` | No | `/srv/vigo/tls/ca` | Path to TLS CA certificate (for fingerprint check) |

Refuses if the license directory has more than one `.lic` file — the loader picks first-alphabetically, so rotation against a multi-`.lic` directory would be ambiguous. Operator must consolidate first.

If the new license fails validation, no changes are made on disk and the old license stays active. If SIGHUP succeeds but the reload confirmation poll times out (5s), the file is installed but the operator should verify with `vigocli license status`.

### license verify

Dry-run validate a `.lic` file (signature + machine fingerprint + expiry) without installing it. Useful before a maintenance window to confirm tessera sent the right file.

```bash
vigocli license verify --file /path/to/license.lic
```

**Flags:**

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--file` | Yes | — | Path to the `.lic` file to validate |
| `--ca-file` | No | `/srv/vigo/tls/ca` | Path to TLS CA certificate (for fingerprint check) |

### license rollback

Restore the most recently archived `.lic` from `/srv/vigo/license/.archive/` and reload. Use as the "undo" button if a license installed via `update` has a problem detected post-reload. Archives the currently-active license before restoring (so re-rolling-back is symmetric).

```bash
vigocli license rollback
```

Requires at least one entry in `/srv/vigo/license/.archive/`. Iterative — running `rollback` again restores the second-most-recent archive entry, and so on.

### Multiple .lic files

The license loader globs `*.lic` in `/srv/vigo/license/`, sorts alphabetically, and uses the first one that decodes successfully. With multiple files present, alphabetical order — not modification time or cap value — decides which is active. `acme-2026.lic` beats `acme-2027.lic` lexicographically; just dropping a new file alongside the old one does **not** rotate the license.

The safe pattern is single-file-at-a-time:

- For first install: `vigocli license install --file <path>`
- For rotation: `vigocli license update --file <path>` (handles the archive + atomic swap automatically)
- For rollback: `vigocli license rollback`

### license inspect

Decode and display a `.lic` file without contacting the server. If no file is specified, inspects the installed license.

```bash
vigocli license inspect /path/to/license.lic
vigocli license inspect    # inspect installed license
```

### license fingerprint

Display this server's machine fingerprint for license binding. Send this fingerprint when requesting a paid license.

```bash
vigocli license fingerprint
vigocli license fingerprint --ca-file /srv/vigo/tls/ca
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--ca-file` | `/srv/vigo/tls/ca` | Path to TLS CA certificate |

The fingerprint is `SHA256(machine-id + primary-MAC + CA-cert-fingerprint)`. It changes on OS reinstall, primary NIC replacement, or CA cert regeneration.

## See Also

- [Licensing](../../howto/operate/troubleshoot.md) -- tiers, enforcement stages, and machine fingerprint
- [REST API: License](../api.md#license)
