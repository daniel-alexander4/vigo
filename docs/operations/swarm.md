
# Swarm Operations

How to configure, monitor, and troubleshoot the envoy P2P substrate and the four content subsystems that ride it ([payload](../reference/glossary.md#payload), [longdrawer](../engineering/subsystems/longdrawer.md), [gitmirror](../engineering/subsystems/gitmirror.md), [cryptsync](../engineering/subsystems/cryptsync.md)).

## Enabling Swarm

Add the `swarm:` section to `server.yaml`. Each subsystem is gated per-envoy by a hostname pattern list (0.41.0+):

```yaml
swarm:
  enabled: ["*"]                     # which envoys run the substrate
  port: 1531                         # agent peer server port (default)
  max_bandwidth_percent: 50          # agent download throttle (default)
  puddle:    { enabled: ["*"] }      # cryptsync derives from this
  gitmirror: { enabled: ["*"] }
  longdrawer:  { enabled: ["*"] }
  payload:   { enabled: ["*"] }
```

Pattern semantics: first-match-wins, `-` prefix denies, default-deny on no match. Empty list disables a subsystem fleet-wide. See [Server Configuration](../getting-started/server-config.md#swarm) for the full table.

Restart the server. Agents automatically start their HTTPS peer server on port 1531 when they receive swarm assignments.

## Distributing a File

Distribute in one command:

```bash
# Distribute to all envoys
vigocli swarm distribute agent-0.8.4-linux-amd64.tar.gz --target '*'

# Distribute to web servers only
vigocli swarm distribute app-v2.tar.gz --target '*.web.*' --label app-v2

# Distribute by trait
vigocli swarm distribute firmware.bin --target 'dmi.product_name=PowerEdge R750'

# Custom retention (default is 7 days)
vigocli swarm distribute logs.tar.gz --target '*' --retention 30d
```

The CLI streams the file to the server's seed directory (hashing in a single pass) and writes a `.swarm` manifest. The host agent discovers the seed on the next check-in cycle, creates a local manifest entry with its own address, and gossips it to peers via multicast. Agents evaluate the target spec locally and download from each other via P2P on port 1531. The server is not in the distribution path — swarm is entirely peer-to-peer.

## Monitoring Progress

Check per-envoy distribution status:

```bash
vigocli swarm status <sha256>
```

```
Blob: a1b2c3d4e5f6...  (app-v2)

Summary: 8 complete, 2 downloading, 1 skipped  (11 total)

HOSTNAME                 STATUS        CHUNKS     SKIP REASON
web-01.example.com       complete      47/47
web-02.example.com       complete      47/47
web-03.example.com       downloading   31/47
db-01.example.com        skipped       0/47       insufficient disk space
```

List all swarm seeds:

```bash
vigocli swarm list
```

The web UI at [`/payload`](../reference/ui/swarm-swarmcast.md) shows all active and revoked seeds with distribution progress. Detail pages auto-refresh every 5 seconds.

## Envoy-Seeded Distribution

Any envoy can originate a distribution — not just the server. Drop a file and a `.swarm` manifest into the seed directory:

```bash
# On any envoy:
cp firmware.bin /var/lib/vigo/swarm/seed/
cat > /var/lib/vigo/swarm/seed/firmware.bin.swarm <<'EOF'
{
  "target": "*",
  "retention": "7d"
}
EOF
```

The manifest fields:

| Field | Required | Description |
|-------|----------|-------------|
| `target` | Yes | Target spec: `*` (all), hostname glob (`*.web.*`), or trait filter (`os.distro=ubuntu`) |
| `retention` | Yes | How long to keep the blob (e.g. `"7d"`, `"168h"`) |

On the next check-in, the agent hashes the file and reports it to the server. The server records the seed in the manifest CRDT and gossips it to all envoys. Each envoy evaluates the target spec and self-assigns if it matches.

`vigocli swarm distribute` uses the same mechanism — it places the file in the server's seed directory. The server is just another envoy.

To change retention or target after seeding, edit the `.swarm` file — changes take effect on the next check-in cycle.

## Network Requirements

| Port | Protocol | Direction | Purpose |
|------|----------|-----------|---------|
| 1531 | TCP (mTLS) | Inbound + Outbound | Peer chunk exchange |
| 1531 | UDP | Inbound + Outbound | Multicast peer discovery (`224.0.0.42`, LAN only) |

**Both TCP and UDP must be allowed on port 1531.** A common misconfiguration is allowing only TCP — the peer server works but multicast discovery silently fails, leaving agents unable to find peers. Use the `swarm-firewall` example module to enforce both rules.

Multicast discovery only works within a LAN segment. Across subnets, agents discover peers via active probing (querying known addresses), peer exchange headers, and server hints.

If your network blocks multicast, swarm still works — agents probe known peer addresses directly and receive server hints via check-in. Peer state persists across restarts in `peers.json`.

## Agent Traits

The agent reports swarm-related traits:

| Trait | Description |
|-------|-------------|
| `swarm.blob_count` | Number of cached blobs |
| `swarm.blobs` | SHA256 hashes of cached blobs |
| `swarm.chunk_sources` | Per-blob download source breakdown (includes `"cached"` for chunks already on disk at run start) |
| `swarm.chunk_details` | Per-chunk source and timing |
| `swarm.active_download_ms` | Per-blob active scheduler wall-clock time (excludes pre-cached chunks) |
| `swarm.downloading` | In-progress downloads (sha → chunks_have) |

These traits are visible in the envoy detail page and can be queried with `vigocli query`.

## Agent Storage

Swarm files are stored under `/var/lib/vigo/swarm/` on Linux (`/Library/Vigo/swarm/` on macOS):

```
/var/lib/vigo/swarm/
├── state.json      # Resolved subsystem gates + last check-in (mode 0644, 0.41.1+)
├── manifest.json   # LWW-CRDT manifest — blob lifecycle state (mode 0644)
├── peers.json      # Persistent peer registry — survives restarts (mode 0644)
├── .blobs/         # Content-addressed storage (SHA256 filenames, root-only)
│   └── 9faa44b9...
├── files/          # Symlinks with original filenames
│   └── firmware.bin → ../.blobs/9faa44b9...
├── seed/           # Drop files here with .swarm manifests to seed distribution
│   ├── firmware.bin
│   └── firmware.bin.swarm
└── graveyard/      # Revoked blobs awaiting hard-delete (auto-cleaned after 7 days)
    └── a1b2c3d4...
```

The three `.json` files at the root (`state.json`, `manifest.json`, `peers.json`) are mode 0644 since 0.41.1 so user-mode CLI verbs can read them. Inspect via the diagnostic CLI rather than parsing the JSON directly:

```bash
vigo swarm status              # gates, manifest version, peer count, last checkin
vigo swarm seeds list          # active blobs this envoy holds
vigo swarm seeds show <key>    # full detail (sha-prefix or label)
vigo swarm peers list          # gossip-table snapshot
```

See [`vigo swarm` (diagnostic verbs)](../reference/cli/agent-swarm.md) for the full reference.

The `.blobs/` directory holds the actual data, named by SHA256 hash. The `files/` directory contains symlinks using the blob's label (original filename):

```bash
ls -la /var/lib/vigo/swarm/files/
```

If a new blob is distributed with the same label, the symlink is updated to point to the new blob.

## Retention

All swarm blobs have a default retention of **7 days**.

**Receivers:** `vigocli swarm distribute` defaults to `--retention 7d`. The agent records the download completion timestamp and evicts the blob when the retention period expires. Override per distribution with `--retention 30d` or `--retention 0` to keep forever.

**Seeders:** Once retention elapses, the server's retention pruner records a revocation in the manifest CRDT. The revocation gossips to all envoys (consumers and the original seeder alike). On the seeder, the blob moves from `.blobs/` to `graveyard/` and the source file + `.swarm` manifest are removed from `seed/` so the trait collector doesn't re-report it. On consumers, the cached blob is evicted. Graveyard files are hard-deleted after another 7 days; within that grace window, a same-SHA reseed resurrects the content without re-downloading.

**Manifest tombstone GC:** The same retention pruner also drops revoked entries from the in-memory manifest store once they've aged past the retention window — the revoke marker has done its job (every reachable agent has observed it and evicted the blob). Without this pass, revoked entries would accumulate in the server's manifest forever; agents prune their persisted manifest on the same window via the check-in loop.

**Manual revoke:** To immediately revoke a blob fleet-wide:

```bash
vigocli swarm revoke <sha256>
```

This marks the seed as revoked in the tracker, records a revocation in the manifest CRDT, and propagates the revocation to all envoys via gossip. Each envoy evicts the blob from its cache upon receiving the revocation. On a LAN, all copies are removed within seconds. Cross-subnet envoys receive the revocation on their next check-in. Operator-initiated revocation and retention-driven revocation flow through identical code — the only filesystem `unlink` happens 7 days later in `cleanup_graveyard`.

**Re-seeding:** To re-distribute revoked content, place the file and `.swarm` manifest back in `seed/`. On the next check-in, the trait collector re-hashes the file, the server records a fresh active entry in the manifest CRDT (the new timestamp beats the old revocation), and distribution resumes. The SHA256 hash is content-based — same file = same hash — so partial downloads from previous rounds are automatically resumed. Peer map entries for the revoked SHA are flushed during eviction, so the re-seeded download starts with fresh peer discovery instead of trusting stale claims from the previous round.

## Manifest

Each envoy maintains a local manifest file at `/var/lib/vigo/swarm/manifest.json` that tracks blob lifecycle state. The manifest is a CRDT (Conflict-free Replicated Data Type) that gossips between envoys automatically.

You don't need to manage the manifest directly — it's maintained by the agent. The manifest ensures:

- Revocations propagate fleet-wide in seconds (via multicast + peer sync)
- Revoked blobs can't be re-registered by stale trait reports
- Offline envoys catch up when they reconnect
- The server going down doesn't prevent revocations between envoys on the same LAN

Revoked manifest entries are pruned after 7 days.

## Download Jitter

When all envoys receive a swarm assignment on the same check-in cycle, they stagger download starts to avoid overwhelming the seeder. Each envoy computes a deterministic delay (0–1 second) by hashing its hostname + the blob SHA256. This ensures:

- Each envoy gets a consistent delay for the same blob (restarts don't change it)
- Different envoys start at different times, spreading the initial load
- The jitter is short enough to not noticeably delay distribution

No configuration is needed — jitter is automatic.

## Disk Space

Every peer-to-peer delivery path in the fleet — swarmcast (admin-distributed swarm blobs), vigocrypt (cryptsync ciphertext), gitmirror (gitmirror bundles), and vigosync (longdrawer announcements) — refuses to commit bytes to a filesystem that has **10% or less free**. The check applies symmetrically on both sides of each transfer:

- **Sender side** — before a push, the sender asks the recipient's peer server `GET /health/storage?for=<subsystem>[&user=…]` over the existing mTLS on port 1531 and compares the returned `free_pct` to the threshold. A peer at or below 10% free is skipped quietly; fan-outs across many peers probe each one once per minute (60s cache) so a burst doesn't hammer the health endpoint.
- **Receiver side** — the push handler (`/cryptsync/push`, `/gitmirror/push`) re-checks its own disk on entry and returns **507 Insufficient Storage** before reading any body bytes. This closes the race between a sender's cached probe and the push arriving when the disk has since filled up.
- **Swarm (pull-based)** — the downloader gates itself in `BlobCache::check_disk_space`, which now enforces both the classic absolute-reserve check (`available >= blob_size + 512 MiB`) AND the 10% free-pct gate. A receiver with 9% free aborts the download before committing chunks.

Skipped pushes are no-ops, not failures: the next push cadence re-probes, and as soon as the peer crosses above 10% free the pushes resume on their own. No backoff state accumulates. Senders log skips at `warn` (or `debug` for announcement-only paths like longdrawer) so operators can see which peers are in disk-pressure state.

The 10% threshold is the default, overridable with `VIGO_MIN_FREE_PCT=<f64>` in the agent environment (primarily for tests). Fleet-wide policy override via `server.yaml` is a follow-up.

Each subsystem measures the filesystem where its data actually lands — `/var/lib/vigo/swarm/.blobs` for payload, `/var/lib/vigo/swarm/gitmirror` for gitmirror mirrors, `~<user>/cryptsync` for cryptsync ciphertext, `~<user>/longdrawer` for longdrawer plaintext. These may sit on different mounts; the check uses the correct one per purpose.

## Troubleshooting

### Envoy stuck on "pending"

"Pending" means the server hasn't received any swarm trait data for this envoy for this blob — the envoy hasn't started downloading yet. Envoys that are actively downloading show as "downloading" with a progress bar. Check in order:

1. **Was the server restarted?** The in-memory PeerTracker is lost on restart. Status goes blank until envoys re-report their traits. Envoys that already have the blob will re-appear as "complete" within 1-2 check-in cycles (~15s). No action needed — wait.

2. **Is the envoy online?** `vigocli envoys` — look for last-seen time.

3. **Is swarm enabled on the server?** `swarm.enabled: true` in `server.yaml`.

4. **Agent version mismatch?** Check `vigo version` on the envoy vs `cat VERSION` on the server. After `force_update` exec(), the new binary may not take effect until the next restart. Check `journalctl -u vigo-envoy | grep "swarm manager started"`.

5. **Disk space critically low?** If `BlobCache::check_disk_space` trips — either absolute reserve (size + 512 MiB) or the 10% free-pct gate — the download is skipped and the envoy shows "skipped." Free up space or rotate old blobs; the next check-in re-attempts.

### Downloads fail with "no available peers"

The scheduler finds 0 peers and bails immediately. This is the most common swarm issue. Diagnose in order:

1. **Firewall: both TCP and UDP 1531 must be open.** A common misconfiguration is allowing only TCP — the peer server works but multicast discovery silently fails. Check with `ufw status | grep 1531`. Use the `swarm-firewall` module to enforce both rules.

2. **Multicast announcer dead.** The seeder must announce blobs via multicast. Check if the seeder is sending announcements:
   ```bash
   sudo timeout 10 tcpdump -i <iface> udp port 1531 -A -c 5 2>&1 | grep "HAVE\|ADDR"
   ```
   If the seeder sends `ADDR` but no `HAVE` for the blob, or sends nothing at all, the announcer task may have exited. Check for `multicast announcer exited` in the agent logs (0.19.32+). Restart the agent.

3. **PeerMap empty.** On the receiving envoy, check if it has discovered any peers (no sudo required, 0.41.1+):
   ```bash
   vigo swarm peers list
   ```
   If empty or missing the seeder's address for the target blob, multicast packets aren't reaching the agent (firewall, different subnet, or announcer dead). For the per-blob view, use `vigo swarm seeds show <sha-or-label>` — it lists the peers known to hold that specific blob.

4. **Multicast leaving but not arriving.** If tcpdump on the seeder shows packets leaving but tcpdump on the receiver shows nothing from the seeder's IP, the issue is network-level:
   ```bash
   # On the seeder:
   sudo timeout 10 tcpdump -i any 'udp port 1531 and src host <seeder-ip>' -A -c 2
   # On the receiver:
   sudo timeout 10 tcpdump -i any 'udp port 1531 and src host <seeder-ip>' -c 1
   ```
   If the seeder sends but the receiver gets nothing, check: WiFi AP IGMP snooping settings, router multicast forwarding, VPN/tunnel interfaces stealing the default route (Tailscale, WireGuard — fixed in 0.19.37 for the announcer address, but the network must still forward the packets).

5. **Tailscale/VPN installed on the seeder.** Prior to 0.19.37, `local_ipv4()` picked the VPN tunnel IP instead of the LAN IP. Upgrade to 0.19.37+ or set `swarm.announce_address` in the agent config to override.

6. **Agent version mismatch.** The scheduler deadlock fix (0.19.29) and peer refresh (0.19.30) are required for multi-chunk downloads. Check `vigo version` on the envoy.

### Envoy stuck on "downloading"

The envoy started downloading but hasn't completed. Check:

- Network connectivity between peers (port 1531 must be reachable)
- Disk space on the envoy (`df -h`)
- Agent logs for download errors (`journalctl -u vigo-envoy --since "5 minutes ago" | grep -i swarm`)
- Stale connections: `ss -tnp | grep 1531` — CLOSE-WAIT connections indicate the peer server closed the connection. Upgrade to 0.19.28+ (header_read_timeout fix)

### "skipped — insufficient disk space"

The envoy doesn't have enough free disk for the blob. Free up space or increase the disk, then re-distribute.

### Downloading from only one peer

If an envoy downloads all chunks from the seeder despite other peers being available:

1. **Agent version < 0.23.2?** Two bugs caused seeder dominance: stale PeerMap entries after revoke/reseed caused 404 floods from peers (falling back to seeder for every chunk), and the orchestrator skipped peer discovery when only the seeder was known. Both fixed in 0.23.2.

2. **Agent version < 0.19.30?** The scheduler didn't refresh the peer map mid-download. Fixed in 0.19.30: the scheduler refreshes every 100 chunks (~10 seconds), picking up new peers announced via multicast during the transfer.

3. **Multicast working?** Check that other peers are announcing HAVE lines for the blob — see the "Downloads fail with no available peers" section above.

Verify multi-peer distribution in the status output — the Sources column shows per-peer chunk counts:

```
danlap: 2000  girlslaptop: 3000  plex: 2378
```

### Peer downloads timing out

If agents log `timeout: connect` errors when downloading from peers, the peer address may be wrong. The server uses the IP the agent reported at enrollment. If the agent was enrolled from a machine running Docker, it may have captured the Docker bridge IP instead of the real LAN IP.

Check the stored IP:

```bash
vigocli envoys | grep <hostname>   # look at the IP column
```

If the IP is a Docker/virtual interface address, re-enroll the agent. The agent filters out virtual interfaces (docker, br-, veth, virbr) when detecting its outbound IP, and refreshes the IP on every check-in cycle.

### Chunk downloads fail with "unexpected end of file"

All chunk download attempts to a peer fail immediately with `io: unexpected end of file`. This indicates the peer's HTTPS server has exhausted its 100-connection semaphore. Two causes were fixed in 0.19.25:

1. **Duplicate concurrent downloads** — the agent spawned a new download task on every check-in cycle for the same blob, each with its own 8-connection semaphore. Five concurrent tasks = 40 connections from one envoy. Fixed by an in-progress guard that allows only one download per blob at a time.
2. **Idle connection accumulation** — HTTP keep-alive connections with no idle timeout held semaphore permits indefinitely. Fixed by a 15-second idle header read timeout on the peer server (connections are only closed when idle between requests, not during active transfers).

Upgrade all agents to 0.19.28+.

### Peer TLS handshake failure

If agents log `certificate does not allow extended key usage for server authentication`, the envoy's enrollment certificate was issued before the CA included ServerAuth EKU. Re-enroll the affected envoys to get new certificates with both ClientAuth and ServerAuth.

### No peers discovered (cross-subnet)

If envoys are on different subnets, multicast won't reach them. Peers are discovered via:

- **Active probing** — the agent queries known peer addresses (from `peers.json`) for the target blob
- **Peer exchange** — the `X-Peers` header on chunk responses shares peer addresses transitively

Peer state persists across restarts in `peers.json`, so an agent that has downloaded from a peer before will try it again for new blobs.

### Multicast announcer not starting

If `journalctl | grep "multicast announcer"` shows `multicast announcer exited` with an error, the UDP socket creation failed. Common causes:

- File descriptor limit reached — check `ulimit -n`
- Another process holding the socket (unlikely — the announcer uses an ephemeral port)
- OS-level multicast restrictions

If neither "multicast announcer started" nor "multicast announcer exited" appears in the logs, the agent is running a version before 0.19.32 that logged announcer errors at debug level (invisible). Upgrade and check again.

## Seed Lifecycle Scenarios

What happens for every action an operator can take on the seeder or downloader.

### Seeder actions

| Action | What happens | Result |
|--------|-------------|--------|
| **Place file + .swarm** | Trait collector hashes file, creates `.blobs/` symlink, reports seed. Server registers in manifest CRDT, gossips to fleet. | Distribution starts. |
| **Remove file from seed/** | Trait collector stops reporting seed. Server auto-revokes (tracker + manifest CRDT). All envoys evict the blob via gossip. | Seeding is intentional — removing the file is an authoritative revocation regardless of how many receivers have the blob. |
| **Replace file (same filename, different content)** | Trait collector computes new hash → label collision detected → old hash revoked, new hash registered. | Old content revoked, new content distributed. |
| **Edit .swarm (change target/retention)** | PeerTracker picks up updated fields on next check-in. Note: ManifestStore does not re-gossip target changes — existing envoy assignments are unaffected. | Changes take effect for new assignments only. |
| **Reseed after revoke (same content)** | Same SHA256 hash. Server creates a fresh active manifest entry (new timestamp beats old revocation). Envoys merge the active entry, clear their completed set, and re-download. | Works. Partial chunks from previous rounds are reused. Within 7 days, the graveyard copy is also reused. |
| **Interrupted seed (partial file, then complete)** | Partial file gets a different SHA256. When the full file replaces it, the partial hash is auto-revoked via label collision and the correct hash is registered. The `evict_revoked_blobs` function checks for shared symlinks before deleting seed source files. | Works. |
| **Kill/restart agent while seeding** | Manifest and peer state persist to disk. Seed file still in `seed/`. Trait collector re-discovers on next cycle. | Seamless resume. |

### Downloader actions

| Action | What happens | Result |
|--------|-------------|--------|
| **Partial download interrupted (agent restart)** | Chunks survive in `.blobs/<sha>.partial/`. On restart, SwarmManager is fresh (empty `completed` set), manifest loaded from disk, scheduler resumes from existing chunks. | Automatic resume. |
| **Delete partial blob** (`rm -rf .blobs/<sha>.partial/`) | Scheduler recreates the partial directory and restarts from chunk 0. | Download restarts from scratch. |
| **Delete complete blob** (`rm .blobs/<sha>`) | `completed` set still has the hash, but on the next ManifestUpdated evaluation, the manager detects the blob is missing from cache and clears the stale entry. Download restarts. | Self-healing within one check-in cycle. |
| **Revocation arrives mid-download** | Scheduler polls `manifest.is_revoked` between retry rounds, in the chunk producer loop, and in the consumer before writing each chunk. On revocation it bails with `REVOKED_ERR`, and the outer pipeline wipes the partial directory immediately so the trait collector stops reporting the sha as `downloading`. | Clean. Partial gone on the same gossip cycle; server transitions the envoy from `revoking` → `revoked` on the next check-in. |
| **Disk full or under pressure** | `check_disk_space` fails either the absolute-reserve gate (size + 512 MiB) or the 10% free-pct gate. Download skipped with "insufficient disk space" or "disk pressure." Status shows "skipped." Retries automatically on every check-in. | Self-healing when space is freed. |
| **No peers available** | Scheduler bails with "no progress" after multicast wait + probing. Retries on every check-in cycle. | Resolves when peers come online. |
| **Seeder goes offline** | Chunk downloads fail → exponential backoff → "no progress" bail. Resumes when seeder (or any peer with the blob) comes back. | Retries automatically. |

## Related

- [Swarm Concepts](../engineering/subsystems/swarm.md) — How swarm works
- [vigocli payload](../reference/cli/swarm-swarmcast.md) — CLI reference for admin-pushed swarmcasts on this substrate
- [Server Configuration](../getting-started/server-config.md) — `swarm:` section
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
