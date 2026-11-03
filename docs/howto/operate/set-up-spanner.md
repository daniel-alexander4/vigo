
# Set up Spanner (peer-equal federation)

A spanner is a set of vigosrv servers — **bolts** — that share one fleet as peers. Every bolt owns a hostname-pattern partition of the envoys, holds the full admissions roster (CRDT-replicated over gossip), and answers fleet-wide admin queries by aggregating from that roster. There is no hub and no primary: every bolt runs identical code and is administered directly.

**When you'd use this:** when one server can no longer comfortably hold the fleet's check-in cadence, query load, or convergence-history retention. Practical inflection points are usually north of ~5,000 envoys per server, or when you want geographic partitioning so a regional outage doesn't take the whole control plane down.

**When you'd skip this:** smaller fleets (a single-server `standalone` deployment is the default and the right answer for almost every operator); fleets that need HA but not horizontal sharding (use `server/peer/` primary/secondary replication instead — see the `peers:` section in `server.yaml`).

> **License requirement:** Spanner needs a **commercial** license. Any non-commercial tier — the built-in free license (100 envoys), a hand-issued community/unlimited license, or any tier that does not require machine binding — does not support `spanner` mode; the server refuses to start with `spanner.mode: spanner` on such a license. Commercial licenses are bound to a specific server via [machine fingerprint](troubleshoot.md#machine-fingerprint-binding) — moving a bolt to a new machine requires a new license.

## Modes

Two modes, set per server in `server.yaml`:

- `standalone` (default) — a single vigosrv handles the whole fleet. No spanner config needed.
- `spanner` — this vigosrv is a peer-equal bolt: it owns a hostname-pattern partition and shares the admissions roster with every other bolt.

The pre-Phase-3 `founder`/`joiner` modes are retired — "founder" was only ever "whoever ran `vigocli spanner init` first", a historical fact, not a runtime role. Every bolt's `server.yaml` has the same shape.

## Configure each bolt

Each bolt's `server.yaml`:

```yaml
spanner:
  mode: spanner
  spanner_id: alexander4-prod        # the federation id — identical on every bolt
  patterns: ["*.east.example.com"]   # the hostname partition THIS bolt owns
  peers: ["10.0.0.2", "10.0.0.3"]    # config-driven hands-off peering (optional) — see below
  snapshot:
    tier1_interval: 30s
    tier2_interval: 5m
  transport:
    multicast_group: "224.0.0.45"
    multicast_port: 1534
  fallback_admin: local
```

`spanner_id` must match across every bolt. Each bolt's `patterns` must not overlap with any other bolt's — the loader refuses an overlapping config at publish time and at boot.

There are two ways to admit bolts into the spanner: **config-driven peering** (`spanner.peers`, hands-off — recommended) and the **manual invite/join ceremony** (a one-shot token). Both are founder-routed: the founder (whoever ran `vigocli spanner init`) is the sole admission authority, and once a bolt is admitted the CRDT-replicated roster is the peer list from then on. Pick one; you don't need both.

## Found the spanner

On the first server, after setting `mode: spanner` + `spanner_id` + `patterns` and restarting:

```bash
vigocli spanner init
```

This writes the founding bolt's self-admission row, signed with the bolt's Ed25519 identity (`/srv/vigo/bolt/identity.wrapped`, generated on first boot in spanner mode).

## Establish the shared fleet CA

Whichever admission path you use, every bolt must share **one fleet CA** — peer mTLS and routed-envoy check-in both verify against it. The CA private key is established **out of band; it never crosses the network.** On the founder:

```bash
vigocli spanner ca export --out fleet-ca.pem   # CA cert + private key PEM bundle, 0600
```

The bundle holds the fleet's crown-jewel private key — keep it `0600` and move it over a secure channel (scp, not email). On each joining server:

```bash
vigocli spanner ca import --from fleet-ca.pem  # installs the CA + re-signs this bolt's server cert from it
```

Restart vigosrv on the joiner afterward to load the adopted CA.

Keeping the CA off the wire is what makes the config-driven admission gate (below) safe: a rogue bolt that talks its way past the IP allowlist is bounded — it can pollute observability and play license-count games, but it cannot read fleet data, receive routed envoys, or mint trusted certs, because all of those sit behind the fleet-CA mTLS it doesn't hold.

## Admit the other bolts — config-driven peering (recommended)

Set `spanner.peers` and restart; no tokens, no ceremony. The list is **asymmetric**:

- On the **founder**, `peers` is the *who-may-join allowlist* — list each joining bolt's address. A joiner's boot-time admission request is honored only if its source address resolves to an entry here.
- On each **joiner**, `peers` points at the founder (you can also list the other bolts for gossip). After setting `mode: spanner`, the shared `spanner_id`, its own non-overlapping `patterns`, and importing the fleet CA, restart.

On boot, a joiner with no roster yet POSTs its self-signed identity to each peer until the founder admits it and returns a founder-signed roster; the joiner CRDT-merges it and starts gossiping. This runs off the check-in hot path with retry/backoff, so a briefly-unreachable founder is tolerated. A bolt that already has a roster (the founder, or a joiner that already bootstrapped) does nothing.

`spanner.peers` entries are `host` or `host:port` (HTTPS REST port, default `8443`).

## Admit the other bolts — manual invite/join ceremony

If you'd rather hand out one-shot tokens instead of listing addresses, leave `spanner.peers` empty and run the ceremony. On any **existing member**, mint a single-use invite bundle:

```bash
vigocli spanner bolt invite --ttl 24h
```

The bundle is a secret — anyone holding it can join the spanner until it expires or is consumed. Hand it to the operator of the joining server out of band.

On the **joining server** — after setting its `server.yaml` (`mode: spanner`, the shared `spanner_id`, its own non-overlapping `patterns`) and restarting:

```bash
vigocli spanner bolt join --from https://member.example.com:8443 --code "$(cat bundle.txt)"
```

The joining server presents the token to the member, receives a signed admission row plus the current roster, and starts participating in the gossiped admissions CRDT. The token is one-shot — the `consumed_tokens` G-Set guarantees it can never be consumed twice.

## Use it

`vigocli spanner status` shows this bolt's mode, its identity pubkey, and the gossip-backed observability for every peer bolt — envoy count, convergence breakdown, and how stale each peer's last snapshot is.

```bash
vigocli spanner bolt list            # the admissions roster
vigocli spanner bolt show <pubkey>   # one bolt's convergence breakdown
vigocli spanner bolt envoys <pubkey> # the envoys that bolt serves
```

Fleet-wide admin queries — `vigocli envoys list`, `vigocli runs`, force-push — work against any bolt. Each bolt answers by aggregating: `fallback_admin: local` (the default) merges the gossip-backed observability cache; `fallback_admin: fanout` queries every roster bolt's admin gRPC live. Both produce the same answer. (`vigocli runs` always fans out — runs are never gossiped.)

## Enrollment routing

When an envoy bootstraps against any bolt, that bolt resolves which roster bolt owns the envoy's hostname (first-match-wins over each bolt's `patterns`, oldest-admitted bolt wins an overlap). If a **different** bolt owns it, the contacted bolt enrolls the envoy on the owning bolt and returns its address; the agent checks in there from then on. No hub mediates this — every bolt computes the same owner from the CRDT-replicated roster.

## Envoy reassignment and drain

Move an envoy between bolts (bolt ids are pubkey-hex from `vigocli spanner bolt list`):

```bash
vigocli spanner bolt reassign <envoy-id> --from <bolt-pubkey> --to <bolt-pubkey>
```

Drain every envoy off a bolt — for maintenance or decommission:

```bash
vigocli spanner bolt drain <bolt-pubkey>            # auto-route each envoy by hostname
vigocli spanner bolt drain <bolt-pubkey> --to <bolt-pubkey>
```

The agent handles reconnection automatically — on its next check-in it is redirected to the new bolt.

## Failure handling

There is no hub-side health probe and no auto-drain. A silent bolt is detected by the gossip freshness thresholds (`spanner.freshness.stale_after`) and surfaces in `vigocli spanner status` as a stale snapshot age. Recovery is automatic when the bolt resumes gossiping. To take a bolt out of service deliberately, drain it (above) before stopping it.

## License enforcement

With `license.mode: aggregate`, every bolt independently sums the fleet-wide node count from the gossip-replicated roster and enforces against it — there is no designated enforcer. With `license.mode: local` (the default), each bolt counts only its own envoys.

## Web UI

The `/spanner` page lists every bolt in the spanner — one row each, with the bolt serving the page tagged **this bolt**. Peer rows come from the gossip-backed observability cache (with a snapshot age); the local row is live from this bolt's own fleet index, so the view is identical no matter which bolt's UI you open. `/spanner/bolts/{pubkey}` drills into a bolt's convergence breakdown and envoy roster.

## Related

- [Server Configuration](../../reference/server-yaml.md) — the `spanner:` section
- ADR-026: peer-equal admissions — the design

## What's next

- **HA for an individual bolt** → `server/peer/` primary/secondary replication, configured per bolt independently of the spanner.
- **A bolt is wedged** → `vigocli spanner status` shows a stale snapshot age; drain it with `vigocli spanner bolt drain <pubkey>`.
- **License is too small / wrong tier** → spanner needs a commercial license; a free or community-tier license is refused regardless of node count. See [troubleshoot.md#machine-fingerprint-binding](troubleshoot.md#machine-fingerprint-binding) for fingerprint rebinds.
- **Cross-bolt convergence lags** → the Tier-1 counter-gossip cadence is `30s` by default; reduce `spanner.snapshot.tier1_interval` for tighter cross-bolt convergence.

---
*Verified on Vigo 0.66.12 · 2026-05-21.*
