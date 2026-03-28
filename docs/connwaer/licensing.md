
# Licensing

Connwaer is a paid add-on to Vigo. It requires both a Vigo installation (any tier, including the free 25-node Community tier) and a `"connwaer"` feature flag in the Vigo license. Node count enforcement is handled by the Vigo server, which tracks how many envoys report Connwaer traits.

## License File

Connwaer reads the same license file as Vigo, from the configured `license_dir` (default: `/srv/vigo/license`). No separate license file is needed -- the `"connwaer"` feature is added to the existing Vigo license.

Generate a license with the Connwaer feature:

```bash
vigo-license-manager license create \
  --customer "Acme Corp" \
  --customer-id "acme" \
  --tier "growth" \
  --nodes 200 \
  --expires 2027-03-28 \
  --features "connwaer" \
  --out /srv/vigo/license/acme.lic
```

## Feature Check

On startup, Connwaer:

1. Loads the license file from `license_dir`
2. Verifies the Ed25519 signature
3. Checks that `"connwaer"` is in the `features` array
4. Checks the expiration date

If any check fails, Connwaer logs a warning and continues in evaluation mode.

## Node Count Enforcement

Connwaer itself does not enforce node counts -- the Vigo server does. The server tracks how many envoys report `connwaer.*` traits and enforces the `max_envoys` limit from the license. This means:

- Connwaer nodes are counted as part of the Vigo license node count
- No separate node counting infrastructure is needed
- The Vigo server is the single enforcement point for both products

## Evaluation Mode

Connwaer does not block on license issues. All 18 capabilities run regardless of license status. Evaluation mode is indicated by startup warnings:

- `"license expired"` -- license file past its expiration date
- `"does not include the 'connwaer' feature"` -- valid Vigo license without Connwaer authorization
- `"no valid license found"` -- missing or invalid license file

## Pricing

Connwaer is priced per-node per-year, added on top of the Vigo license:

| Tier | Nodes | Price/Node/Year |
|------|-------|-----------------|
| Community | 1-25 | Free |
| Starter | 26-100 | $96 |
| Growth | 101-500 | $84 |
| Business | 501-2,000 | $72 |
| Enterprise | 2,001-10,000 | $60 |
| Scale | 10,001+ | $48 |

A Vigo installation is required. Connwaer is not available as a standalone product.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
