
# Licensing

Connwaer is a separately licensed product from Vigo. It requires a valid license file for production use but runs in evaluation mode without one.

## License File

Connwaer looks for a license in the configured `license_dir` (default: `/srv/vigo/license`). The license is a JSON file with an Ed25519 signature, using the same format as Vigo server licenses.

Required fields:

| Field | Description |
|-------|-------------|
| `customer_name` | Licensed customer name |
| `customer_id` | Customer identifier |
| `license_id` | Unique license identifier |
| `tier` | License tier (Community, Starter, Growth, Business, Enterprise, Scale) |
| `max_envoys` | Maximum licensed node count |
| `issued_at` | License issue date |
| `expires_at` | License expiration date |
| `features` | Array of enabled features -- must contain `"connwaer"` |

## Feature Check

On startup, Connwaer:

1. Loads the license file from `license_dir`
2. Verifies the Ed25519 signature
3. Checks that `"connwaer"` is in the `features` array
4. Checks the expiration date

If any check fails, Connwaer logs a warning and continues in evaluation mode.

## Evaluation Mode

Connwaer never blocks on license issues. All 18 capabilities run regardless of license status. Evaluation mode is indicated by startup warnings:

- `"license expired"` -- license file past its expiration date
- `"does not include the 'connwaer' feature"` -- valid license without Connwaer authorization
- `"no valid license found"` -- missing or invalid license file

## Pricing

Connwaer is priced per-node per-year with tiered pricing:

| Tier | Nodes | Price/Node/Year |
|------|-------|-----------------|
| Community | 1-25 | Free |
| Starter | 26-100 | $96 |
| Growth | 101-500 | $84 |
| Business | 501-2,000 | $72 |
| Enterprise | 2,001-10,000 | $60 |
| Scale | 10,001+ | $48 |

Connwaer requires an active Vigo installation (any tier including Community) for the trait pipeline integration. It can also run standalone without Vigo, but compliance scoring on the Vigo server requires both.
