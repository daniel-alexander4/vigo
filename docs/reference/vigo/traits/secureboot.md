
# Secure Boot

Detects UEFI Secure Boot status by reading EFI variables from `/sys/firmware/efi/efivars/`.

## Trait Path
`secureboot`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `secureboot.enabled` | boolean | `true` | Whether Secure Boot is enabled |
| `secureboot.mode` | string | `"uefi"` | Boot mode: `uefi` or `bios` (legacy). Secure Boot is only possible in UEFI mode. |

## Platforms
Linux only. Reads the `SecureBoot-*` EFI variable from `/sys/firmware/efi/efivars/`.

## Notes

- On legacy BIOS systems, `mode` is `bios` and `enabled` is always `false`.
- Compliance frameworks that reference Secure Boot: CIS Benchmarks (1.4.1 UEFI), DISA STIG, NIST 800-53 SI-7 (Software, Firmware, and Information Integrity).
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
