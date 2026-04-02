
# vigocli doctor

Run health checks against the Vigo server to verify it is operating correctly.

## Usage

```bash
vigocli doctor [flags]
```

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--pre-start` | | `false` | Run local pre-start checks (no server needed) |
| `--config` | | `server.yaml` | Path to server.yaml (for `--pre-start`) |

## Modes

### Runtime mode (default)

Queries the running server's `/api/v1/doctor` endpoint and displays the health check results.

```bash
vigocli doctor
```

```
  database ............. OK
  grpc ................. OK
  config ............... OK
  secrets .............. OK
  tls .................. OK (expires 2027-01-15)
  license .............. OK

  Status: healthy
```

### Pre-start mode

Runs local checks without a running server. Useful for validating configuration before starting `vigosrv`.

```bash
vigocli doctor --pre-start
```

Checks performed in pre-start mode:

| Check | Description |
|-------|-------------|
| `config_file` | Parse `server.yaml` |
| `secrets_provider` | Initialize the secrets provider |
| `secret_resolution` | Resolve `secret:` references in config |
| `tls_certs` | Check cert files exist, parse, and verify expiry |
| `config_dir` | Verify `stack/stockpile/` exists with `modules/`, `roles/`, `envoys/` subdirs |
| `config_validation` | Load and validate all `.vgo` module files |
| `spanner` | Validate spanner configuration |

### Example pre-start output

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*

---

```bash
vigocli doctor --pre-start --config /srv/vigo/server.yaml
```

```
  config_file .......... OK
  secrets_provider ..... OK (local)
  secret_resolution .... OK
  tls_certs ............ WARN (expires in 28 days (2026-04-10))
  config_dir ........... OK (stack/.live)
  config_validation .... OK (12 modules)
  spanner ........... OK (standalone)

  Status: degraded
```

### Status values

| Status | Meaning |
|--------|---------|
| healthy | All checks pass |
| degraded | Some checks have warnings |
| unhealthy | One or more checks failed (exit code 1) |

### Check result indicators

| Indicator | Meaning |
|-----------|---------|
| OK | Check passed |
| WARN | Check passed with warnings |
| FAIL | Check failed |
| SKIP | Check skipped (dependency unavailable) |

## Related

- [validate](validate.md) -- validate .vgo configuration files
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
