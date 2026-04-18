
# kernel_module_attestation

The `kernel_module_attestation` trait collector enumerates loaded Linux kernel modules from `/proc/modules` and reports their signature status by reading `/sys/module/<name>/taint`. Pure sysfs reads — no subprocesses, no `modinfo` calls — so the collector is safe to run on every host.

## Method

1. Read `/proc/modules` for the list of loaded modules (name, size, use count, state).
2. For each module, read `/sys/module/<name>/taint` to get the taint flag string (empty if the module has not tainted the kernel).
3. Classify the signature status from the taint flags:
   - `bad_signature` — taint contains `X` (module loaded with a bad signature)
   - `unsigned` — taint contains `E` (unsigned module loaded)
   - `signed` — taint contains neither (signature verified or signing not enforced)
4. Aggregate counts into a summary block.

## Trait Structure

```json
{
  "kernel_module_attestation": {
    "modules": [
      {
        "name": "ext4",
        "size": 987136,
        "use_count": 3,
        "state": "Live",
        "taint": "",
        "signature_status": "signed"
      },
      {
        "name": "nvidia",
        "size": 55488512,
        "use_count": 47,
        "state": "Live",
        "taint": "POE",
        "signature_status": "unsigned"
      }
    ],
    "summary": {
      "total": 125,
      "signed": 120,
      "unsigned": 4,
      "bad_signature": 0,
      "proprietary": 2,
      "out_of_tree": 1
    }
  }
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `modules[].name` | string | Module name from `/proc/modules` |
| `modules[].size` | int | Module size in bytes |
| `modules[].use_count` | int | Reference count |
| `modules[].state` | string | Kernel state — typically `Live`, also `Loading`, `Unloading` |
| `modules[].taint` | string | Raw taint flag string from `/sys/module/<name>/taint` (empty for clean modules) |
| `modules[].signature_status` | string | `signed`, `unsigned`, or `bad_signature` |
| `summary.total` | int | Total loaded modules |
| `summary.signed` | int | Count with `signature_status == "signed"` |
| `summary.unsigned` | int | Count with taint `E` |
| `summary.bad_signature` | int | Count with taint `X` |
| `summary.proprietary` | int | Count with taint `P` (informational; does not affect signature status) |
| `summary.out_of_tree` | int | Count with taint `O` (informational; does not affect signature status) |

## Taint Flag Reference

The Linux kernel sets taint flags per loaded module under `/sys/module/<name>/taint`. Flags relevant to signature classification:

- `E` — module loaded unsigned (`CONFIG_MODULE_SIG_FORCE` not enforced, or signature absent)
- `X` — module loaded with a bad signature (signature present but verification failed)

Other flags (`P` proprietary, `O` out-of-tree, `F` force-loaded, `C` staging) are surfaced in the `taint` field and summary counts but do not affect `signature_status`. See `Documentation/admin-guide/tainted-kernels.rst` in the Linux source tree for the full list.

## Platform Support

| Platform | Method |
|----------|--------|
| Linux | `/proc/modules` + `/sys/module/<name>/taint` |
| macOS | Not supported (returns null) |
| FreeBSD | Not supported (returns null) |
| OpenBSD | Not supported (returns null) |
| NetBSD | Not supported (returns null) |
| illumos | Not supported (returns null) |
| Windows | Not supported (returns null) |

## Use Cases

Alert on unsigned or bad-signature modules in production:

```yaml
# Template fragment
{{if gt .Traits.kernel_module_attestation.summary.bad_signature 0}}
ALERT: {{.Traits.kernel_module_attestation.summary.bad_signature}} module(s) loaded with bad signatures
{{end}}
```

Filter envoys with unsigned modules via the inventory API:

```bash
vigocli inventory --where "kernel_module_attestation.summary.unsigned>0" \
                  --show "hostname,kernel_module_attestation.summary.unsigned"
```

## Classification

**Stable** — loaded kernel modules change infrequently after boot. Cached across convergence cycles.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
