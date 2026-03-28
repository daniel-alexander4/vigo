
# HSM Key Lifecycle

| | |
|---|---|
| **Machine name** | `hsm_lifecycle` |
| **Controls** | CCSS-9.1 |
| **Platforms** | All |
| **Timeout** | default (5m) |

## What it verifies

Audits a Hardware Security Module (HSM) via its PKCS#11 interface to verify that key lifecycle practices are sound. This means no expired keys are present, no keys have the extractable attribute set (which would allow them to be exported from the HSM), and keys approaching expiration are flagged. Used for CryptoCurrency Security Standard (CCSS) compliance and any environment where HSM-backed key management is required.

## Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `pkcs11_library` | string | *(required)* | Path to the PKCS#11 shared library (`.so` on Linux, `.dll` on Windows). The capability is skipped if this is not set or the library file does not exist. |
| `slot_id` | integer | `0` | PKCS#11 token slot index to audit. If the index exceeds the number of available slots, the first slot is used. |

## Thresholds

| Threshold | Type | Default | Description |
|-----------|------|---------|-------------|
| `expiry_warning_days` | integer | `30` | Number of days before a key's `CKA_END_DATE` at which it is flagged as expiring soon. Keys inside this window produce a warning. |

## Verification logic

1. **Library existence** -- Check that the PKCS#11 shared library exists at the configured path. If not found, the capability is skipped with a note that the HSM may not be installed.

2. **Initialize PKCS#11** -- Load the library via the `cryptoki` crate and call `C_Initialize` with OS thread locking.

3. **Enumerate slots** -- Call `C_GetSlotList` to find all slots with tokens present. Record the count. If no slots have tokens, record a warning and return -- the HSM may not be initialized.

4. **Select slot** -- Use the slot at index `slot_id`, or fall back to the first available slot if the index is out of range.

5. **Read token info** -- Retrieve and record the token label from the selected slot.

6. **Open session** -- Open a read-only session on the slot. No PIN is required -- only public attributes are inspected.

7. **Enumerate objects** -- Call `C_FindObjects` with no filter to list all objects (keys, certificates, etc.) in the token. Record the total count.

8. **Inspect key attributes** -- For each object, request `CKA_END_DATE` and `CKA_EXTRACTABLE` attributes:
   - **Expired keys** -- If `CKA_END_DATE` is in the past, increment the expired counter.
   - **Expiring soon** -- If `CKA_END_DATE` is within `expiry_warning_days` of today, increment the expiring-soon counter.
   - **Extractable keys** -- If `CKA_EXTRACTABLE` is `true`, increment the extractable counter. Keys that should never leave the HSM must have this attribute set to `false`.

### Result

- **Pass** -- No expired keys, no extractable keys, and no keys expiring within the warning window.
- **Fail** -- One or more expired keys found.
- **Warn** -- No expired keys, but extractable keys or keys expiring soon were found.
- **Skipped** -- `pkcs11_library` is not configured or the library file does not exist.

## Example config

```yaml
capabilities:
  hsm_lifecycle:
    schedule: "daily"
    params:
      pkcs11_library: "/usr/lib/softhsm/libsofthsm2.so"
      slot_id: 0
    thresholds:
      expiry_warning_days: 30
```

Using a Thales Luna HSM with a wider expiry window:

```yaml
capabilities:
  hsm_lifecycle:
    schedule: "daily"
    params:
      pkcs11_library: "/usr/lib/libCryptoki2_64.so"
      slot_id: 1
    thresholds:
      expiry_warning_days: 90
```

AWS CloudHSM:

```yaml
capabilities:
  hsm_lifecycle:
    schedule: "weekly"
    params:
      pkcs11_library: "/opt/cloudhsm/lib/libcloudhsm_pkcs11.so"
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
