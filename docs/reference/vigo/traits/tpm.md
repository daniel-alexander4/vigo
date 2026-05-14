
# TPM

Detects the presence and version of a Trusted Platform Module (TPM) via `/sys/class/tpm/`.

## Trait Path
`tpm`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `tpm.present` | boolean | `true` | Whether a TPM device is detected |
| `tpm.version` | string | `"2.0"` | TPM specification version (`1.2` or `2.0`). Only set when `present` is true. |

## Platforms
Linux only. Reads from `/sys/class/tpm/tpm0/tpm_version_major`.

## Notes

- TPM 2.0 is required for Windows 11, BitLocker, and many compliance frameworks.
- Compliance frameworks that reference TPM: NIST 800-53 SC-12 (Cryptographic Key Establishment and Management), DISA STIG.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
