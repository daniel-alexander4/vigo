---

# vigocli tungol

Operations for the **tungol** (federation) and **steorra** (one vigosrv in the tungol) primitives. The current wire implementation is hub + steorras in `server/tungol/`; Phase 1+ will replace it with peer-equal gossip.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `status` | Show this server's tungol role (standalone / hub / spoke) and steorra availability |
| `steorra list` | List all registered steorras with status, latency, and envoy counts |
| `steorra show <id>` | Show a steorra's compliance breakdown, envoy count, and recent activity |
| `steorra envoys <id>` | List all envoys managed by a specific steorra |
| `steorra runs <id>` | List recent convergence runs on a specific steorra |
| `steorra push <id>` | Trigger immediate check-in for all envoys on a steorra |
| `steorra reassign <envoy-id>` | Move an envoy to a different steorra without re-enrollment |
| `steorra drain <id>` | Migrate all envoys off a steorra for maintenance or decommission |
| `steorra add <id>` | Register a new steorra with the tungol and generate a sample server.yaml |
| `steorra remove <id>` | Drain and remove a steorra from the tungol configuration |
| `ops-puddle init <friendly-name>` | Found the ops-puddle on this vigosrv |
| `ops-puddle status` | Show the ops-puddle's friendly name, pubkey, and member set |

## Persistent flags

| Flag | Default | Description |
|------|---------|-------------|
| `--key-file` | (auto-detected inside `--secrets-dir`) | Path to the master encryption key. |
| `--secrets-dir` | `/srv/vigo/secrets` | Directory holding the secrets backend's master key. |
| `--puddle-dir` | `/srv/vigo/puddle` | Directory holding this vigosrv's service-account puddle (ADR-025). |
| `--ops-puddle-dir` | `/srv/vigo/ops-puddle` | Directory holding the per-host ops-puddle wrap (ADR-025). |

## tungol status

Shows the deployment's tungol role and, when run against a hub, summary statistics for each registered steorra (envoy counts, compliance percentage, health, latency, hostname patterns). Standalone deployments report `No tungol configured.` and exit.

## tungol steorra

The `steorra` group nests every per-steorra verb. The bare `vigocli tungol steorra` (no verb) prints help — it does NOT default to `list`; the explicit `list` verb is required.

### steorra list

Compact one-line-per-steorra table. Calls `GET /api/v1/tungol/status` and presents only the steorra summary.

### steorra show &lt;steorra-id&gt;

Full compliance breakdown for one steorra: envoy count, converged / relapsed / diverged / failed / offline / no-data counts, compliance percentage, latency, hostname patterns, and any last-error string. Calls `GET /api/v1/tungol/steorras/{id}`.

### steorra envoys &lt;steorra-id&gt;

Envoy roster for a specific steorra (hostname, IP, revoked flag, last-seen timestamp). Calls `GET /api/v1/tungol/steorras/{id}/envoys`.

### steorra runs &lt;steorra-id&gt;

Recent convergence runs from envoys on a steorra (status, module counts, changed / failed counts, started-at). Calls `GET /api/v1/tungol/steorras/{id}/runs`.

### steorra push &lt;steorra-id&gt; \[--envoy-id ID\]

Force an immediate check-in for every envoy on the named steorra. With `--envoy-id`, narrows the push to one envoy. Calls `POST /api/v1/tungol/steorras/{id}/push`.

### steorra reassign &lt;envoy-id&gt; --from &lt;steorra-id&gt; --to &lt;steorra-id&gt;

Move an envoy between steorras without re-enrolling it. Required flags `--from` and `--to`; both are steorra IDs. The agent reconnects on its next check-in. Calls `POST /api/v1/tungol/reassign`.

### steorra drain &lt;steorra-id&gt; \[--to &lt;steorra-id&gt;\]

Migrate every envoy off a steorra, either to a specified `--to` target or auto-routed by hostname pattern. Prompts for confirmation. Calls `POST /api/v1/tungol/steorras/{id}/drain`.

### steorra add &lt;steorra-id&gt; --addr HOST:PORT --patterns GLOB,... \[--output PATH\]

Print (or write, with `--output`) a sample `server.yaml` for a new steorra pre-filled with its ID, the hub address, and the hostname patterns it will own. Also prints the `tungol:` block to add to the hub's server.yaml. Hub-only — refused on standalone or spoke servers.

### steorra remove &lt;steorra-id&gt;

Drains the steorra, then prints the YAML lines to remove from the hub's `tungol.steorras:` list (operator-driven edit, then hub restart).

## ops-puddle init &lt;friendly-name&gt;

Generates the ops-puddle's Ed25519 keypair, wraps it under this vigosrv's master-key-derived wrap key, and persists it under `--ops-puddle-dir`. This vigosrv's service-account puddle becomes member zero. Other vigosrvs join via the pair flow.

The friendly name participates in gitback URLs (`gitback://<name>/stockpile`) — pick something stable; renaming requires re-founding the ops-puddle. Name validation rules mirror the agent's per-user puddle rules: 1–64 ASCII alphanumeric + `-` `_` `.`, no leading dot, and no 64-hex-character `project_id` lookalike.

Requires an unlocked secrets vault (`vigocli secrets unlock`).

```
$ vigocli tungol ops-puddle init alexander4-ops
Ops-puddle founded
  Friendly name: alexander4-ops
  Pubkey:        ed25519:274438afd5550b90c720c405acf07d3194fcd75c4ba2038adb25f6e688082d48
  Dir:           /srv/vigo/ops-puddle
  Members:       1 (this vigosrv: ed25519:559812a17c032f71e0b2f842980951b95cde184b43e5522d06d41eb44c2f6b52)
```

## ops-puddle status

Reads the public files in `--ops-puddle-dir` (friendly name, pubkey, members list) without unwrapping the signing key. Safe to run on a locked vault.

## ops-puddle pair

Mints a single-use pair code (16 chars, base64url, ~96 bits of entropy) with a 5-minute TTL and writes a `pending-pair` record to `--ops-puddle-dir/pending-pair` (mode 0600, only the SHA-256 of the code lands on disk). Prints the `join` command for the operator to paste on the joining vigosrv. Running again overwrites any prior outstanding offer.

Requires this vigosrv to already hold ops-puddle membership (run `init` first if not). Requires an unlocked secrets vault.

```
$ vigocli tungol ops-puddle pair
Pair offer minted (expires 14:55:03 PDT, single-use)

  On the joining vigosrv, run:

    vigocli tungol ops-puddle join \
      --from https://hub.example.com:8443 \
      --code 8h3K_z2pYvR4qLwx

  (The operator on the joining vigosrv must already have admin
   credentials for this vigosrv — run `vigocli auth login --server
   https://hub.example.com:8443` first if not.)
```

## ops-puddle join --from &lt;https-url&gt; --code &lt;code&gt;

Claims a pair offer from the `--from` vigosrv: POSTs `/api/v1/tungol/ops-puddle/pair-claim` with the code + this vigosrv's service-account puddle pubkey, receives the ops-puddle seed + friendly name + updated members list, re-wraps the seed under THIS vigosrv's master-key-derived wrap key, and persists the ops-puddle state under `--ops-puddle-dir`.

Both vigosrvs end up with the same Ed25519 private key but wrapped under different per-host keys — so a master-key rotation on one side doesn't invalidate the other's copy.

Refuses to overwrite an existing ops-puddle at `--ops-puddle-dir`. Requires an unlocked secrets vault and admin credentials for `--from` (configure via `vigocli auth login --server <from>` first if needed).

```
$ vigocli tungol ops-puddle join \
    --from https://hub.example.com:8443 \
    --code 8h3K_z2pYvR4qLwx
Ops-puddle joined
  Friendly name: alexander4-ops
  Pubkey:        ed25519:274438afd5550b90c720c405acf07d3194fcd75c4ba2038adb25f6e688082d48
  Dir:           /srv/vigo/ops-puddle
  Members (2):
    - ed25519:559812a17c032f71e0b2f842980951b95cde184b43e5522d06d41eb44c2f6b52
    - ed25519:f81de2c4a8f1bb4d6e23b07c5b920fd1a2eb43d9a09f3e2b40c47ab9a83b66c1 (this vigosrv)
```
