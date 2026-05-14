---
title: vigo swarm (diagnostic verbs)
weight: 25
---

# `vigo swarm` (diagnostic verbs)

Read-only envoy-side diagnostic verbs for the swarm substrate (0.41.1+). Surface the agent's view of the substrate without contacting the server: gate state, manifest version, active blob inventory, peer table.

## Verbs

| Verb | Purpose |
|------|---------|
| `vigo swarm status` | Resolved per-subsystem gates, manifest version, active-blob count, peer count, last check-in, blob cache size. |
| `vigo swarm seeds list` | One line per active blob this envoy holds (sha-prefix, label, target, age, retention). Sorted newest-first. |
| `vigo swarm seeds show <key>` | Full detail for one blob. `<key>` is a sha256 prefix (≥4 chars, must be unambiguous) or an exact label match. Includes peers known to hold the blob. |
| `vigo swarm peers list` | Known peers from the gossip / multicast table — address, blob count, last-seen age. |
| `vigo swarm hostdox status` | Whether hostdox is enabled for this envoy (from the server-resolved gate in `state.json`) plus the current contents of the agent-managed `# BEGIN/END vigo swarm peers` block in `/etc/hosts` (one line per peer name → IP). |

## Permissions

No sudo required. The verbs read three daemon-canonical mirror files at `/var/lib/vigo/swarm/`:

| File | Written by | Mode | Contents |
|------|------------|------|----------|
| `state.json` | agent on every accepted bundle | 0644 | resolved subsystem gates, last check-in, agent + policy versions |
| `manifest.json` | manifest store | 0644 | LWW-Map of active blob entries |
| `peers.json` | peer map | 0644 | gossip-table snapshot — addresses + blob coverage |

The blob cache directory itself (`/var/lib/vigo/swarm/.blobs/`) stays root-only — `status` reports `(root-only)` for cache size when it can't stat the directory. Knowing the existence + sizes of cached payloads is mildly information-leaky (which version of which payload an envoy holds), so the choice is to surface size *only* with sudo.

## Output stability

The output format is **not** a stable contract — it's optimized for human reading and may change between releases. If you need machine-readable output, parse the underlying JSON files directly (their schemas are documented in the agent source: `agent/src/swarm/{state_mirror,manifest,peermap}.rs`). A `--json` flag on these verbs is on the backlog if real demand surfaces.

## Examples

```text
$ vigo swarm status
Swarm posture for this envoy

  agent version:    0.41.1
  policy version:   42
  last checkin:     12s ago (unix:1746659584)

  Gates (resolved from server pattern lists):
    swarm:        on
    payload:      on
    longdrawer:     on
    puddle:       on  (lockbox follows)
    gitback:    on
    hostdox: off  (managed /etc/hosts block)

  Manifest:
    version:      19
    active blobs: 3
    revoked:      1

  Peers:
    known addresses: 4
    known (addr × blob) pairs: 11

  Blob cache:       127.4 MiB (/var/lib/vigo/swarm/.blobs)
```

```text
$ vigo swarm seeds list
SHA            LABEL                TARGET                   AGE      RETENTION
a3f2c1d4e5b6   agent-0.41.1         *                        45m      7d
8e1f4c7b2a3d   firmware-r750        dmi.product=PowerEdge*   3h       30d
```

```text
$ vigo swarm hostdox status
hostdox status

  enabled:          yes  (server.yaml swarm.hostdox: true, and this host matches swarm.enabled)
  /etc/hosts block: present — 3 peer name(s):
    192.168.1.2     danlap
    192.168.1.74    annlap
    192.168.1.219   plex
```

```text
$ vigo swarm seeds show agent-0.41.1
sha256:        a3f2c1d4e5b6789abc012def3456789012345678901234567890abcdef123456
label:         agent-0.41.1
target:        *
retention:     7d
size:          12.4 MiB (12968448)
status:        Active
seeder:        server
updated_at:    45m ago (unix:1746656800)
updated_by:    server

Peers holding this blob:
  ADDRESS                    CHUNKS  SOURCE      AGE
  10.0.0.4:1531                  47  multicast   12s
  10.0.0.7:1531                  47  multicast   45s
```

## Related

- [Swarm Engineering Reference](../../howto/set-up-swarm.md) — substrate architecture and gate semantics
- [Swarm Operations](../../howto/set-up-swarm.md) — operator-side configuration and troubleshooting
- [`vigocli swarm` reference](../../howto/set-up-swarm.md) — server-side inspection (works against any reachable envoy)
