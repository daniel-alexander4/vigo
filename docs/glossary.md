
# Glossary

Canonical definitions for Vigo-specific terms. Alphabetical. Each entry is one paragraph with cross-links to the page that covers the concept in depth.

If you hit a term in the docs or CLI that isn't here, that's a doc bug — please flag it.

## blob

An opaque byte sequence distributed by the [swarm](howto/set-up-swarm.md) subsystem, addressed by its SHA-256 hash. A blob is immutable: re-distributing updated content means seeding a new blob and revoking the old one. Agents store blobs in `/var/lib/vigo/swarm/.blobs/` and materialize symlinks to them under `/var/lib/vigo/swarm/files/` per the manifest's target paths.

## bundle

A self-contained git repository archive produced by `git bundle create`, used as [gitback](howto/set-up-gitback.md)'s unit of distribution. Bundles can be full (complete repository history) or incremental (deltas since a previous bundle). Receivers materialize bundles into bare repositories at `/var/lib/vigo/swarm/gitback/mirrors/<seeder>/<user>/<project>.git`.

## compliance

Standards-framework coverage. Splits into two surfaces, kept separate as of 0.32.0:

- **Envoy compliance** — whether a managed node's modules satisfy controls in regulatory frameworks (HIPAA, SOC 2, PCI DSS, NIST 800-53, ISO 27001, etc.). Measured as coverage percentage per framework. A control counts as covered iff (a) the envoy has a module tagged with that control via `compliance:`, (b) a [Connwaer](#connwaer) trait check on the envoy escalates it, or (c) a [waiver](#waiver) applies. This is the surface framework reports under `/api/v1/report/<slug>` measure.
- **Vigo product compliance** — what the Vigo server and agent satisfy by construction (mTLS, RBAC, audit hash chain, secret encryption, etc.). Lives in `server/compliance/AppControls` and is surfaced separately at `/platform/compliance` (Platform → Data). Useful for vendor questionnaires; never folded into framework scoring.

Distinct from [convergence](#convergence): convergence is "did my modules apply successfully," compliance is "do my modules satisfy framework X." See [scope](#scope) for how each framework control is classified, and [Compliance](reference/compliance-matrix.md) for the full overview.

## connwaer

A standalone Rust agent for active compliance verification — checking things Vigo can't prove through enforcement alone (WORM storage, RNG validation, encryption at rest, network segmentation). Runs alongside the vigo agent on enrolled envoys. Separate product with its own license file and independent node counting. See the [Connwaer docs](reference/compliance-matrix.md).

## convergence

Module-convergence state for an envoy, evaluated on two orthogonal axes:

- **Failure axis** — `converged`, `degraded`, `failed`, `no_data`. Mutually exclusive; sums to fleet total. Any non-zero failure clears the threshold; zero failures is `converged` regardless of how many modules made changes.
- **Drift axis** — `none`, `changed`, `diverged`. `changed` fires when the latest run has `ChangedCount > 0`; `diverged` fires after `DivergedThreshold` consecutive runs with changes (default 5). Diverged is a strict subset of changed. Independent of the failure axis — an envoy can be `converged` AND `diverged` simultaneously (zero failures, but persistent module churn).

Reachability (online / offline) is a third orthogonal axis tracked by `server/freshness`. See the [Convergence Walkthrough](reference/writing-modules.md).

## drift

The drift axis of convergence (above). Computed by `convergence.ComputeDrift` over an envoy's recent run history, expressed as a `DriftLevel` (`none` / `changed` / `diverged`). Drift is purely about *whether modules keep mutating* — it says nothing about whether mutations succeed. A non-idempotent module that recreates the same file every run produces persistent `Created` results and lands the envoy in `diverged` even though every run technically succeeds.

## envoy

A managed node running the `vigo` Rust agent. Each envoy enrolls once, receives a client UUID, and checks in periodically to pull desired state and report results. The central server addresses individual envoys by UUID; operators reference them by hostname. The retire/replace/dispose path uses three different verbs (revoke, puddle evict, delete) with deliberately different scopes — see the [Envoy Lifecycle](howto/troubleshoot.md) reference.

## executor

The Rust implementation of a resource type in the agent (`file`, `package`, `service`, `user`, `cron`, etc.). Executors check current state, act only on drift (idempotent), and return one of `None`/`Created`/`Updated`/`Deleted`/`Skipped`/`Blocked`. See the [Executor Reference](reference/vigo/resources/).

## fleet

Every [envoy](#envoy) enrolled with a given server. "Fleet-wide" means *across all those envoys* — it is the broadest scope in Vigo. Distinguish from two narrower scopes that the UI must not conflate with it:

- The **[swarm](#swarm)** is the P2P substrate connecting envoys to each other; in practice the swarm spans the same envoys as the fleet, but it is the *transport layer*, not the set of machines. There is exactly one swarm — there is no "a user's swarm."
- A **[puddle](#puddle)** is a *per-user* trust unit: one user's collection of envoys, treated as a single Ed25519 identity. "A user's puddle" is meaningful; "the fleet's puddle" is not.

Canonical scope words, narrowest to broadest: **puddle** ⊆ envoys-running-a-given-user ⊆ **fleet**; the **swarm** is the substrate over the fleet. ("Mesh" is *not* a canonical Vigo term — where older prose said "mesh" it meant either the puddle (for [lockbox](#lockbox)) or an ad-hoc per-user sync set (for [longdrawer](#longdrawer)); use the precise term.)

See the [scope-hierarchy diagram](howto/set-up-swarm.md#scope-fleet-vs-swarm-vs-puddle) on the Swarm page for how these layers and the four content subsystems relate.

## filecast

Administrator-pushed file distribution on top of the [swarm](#swarm) substrate. An operator runs `vigocli swarm filecast distribute <file>` (or drops a file + `.swarm` sidecar on a seeder) and the blob replicates across the fleet via swarm's peer channel. Filecast is one of four content subsystems that ride swarm; the other three ([longdrawer](#longdrawer), [gitback](#gitback), [lockbox](#lockbox)) are user-scoped, while filecast is operator-scoped and fleet-wide. Surfaced in the web UI at [`/swarm/filecast`](howto/).

## manifest

The LWW-CRDT metadata store for swarm blobs, gossiped between envoys. Tracks which blobs exist, their targets, retention policies, and revocation tombstones. Each envoy holds a local manifest; version mismatches during multicast trigger manifest sync over the peer HTTPS channel. See [Swarm Manifest](howto/set-up-swarm.md#manifest).

## hostcrate

Any `.vgo` file containing an `envoys:` block with `- match:` entries. Maps hostname patterns/globs to roles, modules, and vars. First-match-wins. Conventionally named `envoys.vgo` but the loader walks all `.vgo` files, so per-host filenames (`plex.vgo`, `web01.vgo`) also work. See [Multi-Axis Config](reference/writing-modules.md).

## module

A reusable resource set in `stockpile/modules/*.vgo`. Declares resources, default vars, and optional compliance claims. Composed into [roles](#role), applied to [envoys](#envoy) via [hostcrates](#hostcrate) or directory-level `common.vgo` inheritance. Modules are the primary composition unit for envoy-wide config. See [Config Format](reference/writing-modules.md) and [Resource Language](reference/writing-modules.md).

## resource

A single idempotent state declaration inside a module — one file, one package, one service, one user account, etc. Resources declare a `type:` (mapping to an [executor](#executor)), parameters, and optional `depends_on` / `notify` / `when:` attributes. See [Resource Language](reference/writing-modules.md).

## retract

An auto-generated inverse of a module — file resources flipped to `state: absent`, services to `stopped`+`enabled: false`, packages to `absent`, etc. Used for clean module removal. Generated by `vigocli config publish` when retraction is enabled; the resulting `.retract` file is inert until renamed and applied. See [Module Retraction](reference/writing-modules.md).

## role

A named grouping of modules and vars defined in `roles.vgo` (or the legacy `roles/` directory). Supports `when:` (role-wide conditional), `case:` (per-platform module groups), and `includes:` (single-level role composition). Assigned to envoys via [hostcrates](#hostcrate). See [Config Format](reference/writing-modules.md).

## sandgorgon

Vigo's bare metal lifecycle subsystem: PXE provisioning, BMC (Redfish) integration, decommission workflow with NIST 800-88 certificate generation, CSV/NetBox inventory import. Exposed at `/sandgorgon/` in the web UI and via `vigocli sandgorgon`. **Deferred to post-v1** -- the code is present but bare-metal lifecycle is not a v1-supported feature. See [Sandgorgon](overview.md).

## scrier

Vigo's admin remote-access subsystem: interactive SSH, RDP, and VNC sessions to any enrolled envoy, routed through the envoy's outbound `AgentStream` + a dedicated `TunnelStream`. No inbound ports on the envoy side — the core MSP/customer-firewall value prop. Browser access at `/scrier/{envoy_id}`; CLI access via `vigocli scrier ssh`. See [Scrier](howto/set-up-scrier.md).

## seed

The originating instance of a swarm blob on an envoy — the source of truth for that blob. Today seeds are created exclusively by `vigocli swarm filecast distribute`, which writes into the server's bind-mounted seed directory. The envoy-originated seed verb (`vigo swarm seed add`) is deferred — see `pending.md` for status. Peers pull chunks from seeds until they have full local copies.

## spanner

The retired name for the federated control-plane subsystem. All operator-facing surfaces (YAML, CLI, REST, UI, metrics, proto, on-disk filenames) speak the [tungol](#tungol) / [steorra](#steorra) vocabulary; the loader rejects the old `spanner:` YAML key at startup. The `role:` value `spoke` is retained as the architectural marker for "this vigosrv is one steorra in the tungol" until Phase 1+ retires the hub + steorras shape entirely. See [Tungol](howto/set-up-tungol.md).

## steorra

One `vigosrv` participating in a [tungol](#tungol) federation. Owns a hostname-pattern partition of the fleet; handles agent check-ins for its envoys; participates as a peer in the gossiped admissions CRDT (ADR-026). The YAML config uses `tungol.mode: founder` (the bootstrap-discovery rendezvous in Phase 2 — Phase 3 retires the founder/joiner distinction entirely) or `tungol.mode: joiner` (a steorra that joined an existing tungol). Per-steorra identity is the Ed25519 pubkey from `server/steorra/`, not a YAML-configured ID.

## stockpile

The single operator-edited tree under [stacks](#stacks), at `/srv/vigo/stacks/stockpile/`. Holds every config primitive: [modules](#stone) (module definitions), [usercrates](#usercrate), `templates/` (Go template files referenced by `source:`), `tasks/`, `workflows/`, plus the assignment files `envoys.vgo` hostcrates, `roles.vgo`, `common.vgo`, `waivers.vgo`, `environments.vgo`, and `compliance.vgo` claim-inheritance files. The 0.33.1 collapse merged the prior `stockpile/` subtree into stockpile — there is one tree now, and `vigocli config publish` syncs only this one to [.live](#live).

## stone

A module definition file in `stockpile/modules/**/*.vgo`. The directory name `modules/` is the filesystem-level construction metaphor for what the YAML + code call a [module](#module) — the YAML key (`modules:`) and Go/Rust identifiers (`ModuleDefinition`) still use "module" for industry-standard recognition. Operators writing `modules: [nginx]` in a role are referencing the stone at `stockpile/modules/nginx.vgo`. Filesystem rename shipped 0.29.0.

## swarm

The envoy-only P2P network substrate. Swarm is the transport layer that all P2P content subsystems ride on: peer discovery (multicast on `224.0.0.42:1531` + registry-backed hints), mTLS HTTPS between envoys on port 1531, manifest CRDT gossip, and content-addressed chunking. Swarm does not itself define *what* gets distributed — the four content subsystems do: [filecast](#filecast) (admin-pushed blobs, fleet-wide), [longdrawer](#longdrawer) (plaintext home-dir sync, per-user LAN set), [gitback](#gitback) (source-control repo mirrors, per-puddle), [lockbox](#lockbox) (encrypted home-dir sync, per-puddle). Each subsystem has its own content rules but reuses swarm's peer channel, port, and mTLS trust. The swarm is singular and fleet-wide — see [fleet](#fleet) for the scope-word distinctions. See [Swarm](howto/set-up-swarm.md).

## trait

A collected fact about an envoy, reported at check-in — OS, hardware, installed packages, listening ports, filesystem mounts, kernel modules, etc. Traits feed into `when:` expressions, compliance evaluation, inventory views, and trend analysis. Each trait has a dedicated collector in `agent/src/traits/`. See the [Trait Reference](reference/vigo/traits/).

## tungol

The whole federated control plane — a constellation of [steorra](#steorra) servers cooperating to operate one fleet. CLI (`vigocli tungol`), UI route (`/tungol`), REST namespace (`/api/v1/tungol/*`), YAML key (`tungol:`), and metric prefix (`vigo_tungol_*`) all speak the tungol vocabulary. The current wire implementation is hub + steorras; Phase 1+ will replace it with peer-equal gossip. See [Set up Tungol](howto/set-up-tungol.md).

## usercrate

A config module scoped to one or more users' environments (account creation, home-dir files, dotfiles, personal cron jobs), analogous to a regular [module](#module) but organized around human and service-account identity. Usercrates live in `stockpile/**/usercrates/*.vgo` — any directory level — but the directory is a library: as of 0.54.0 a usercrate is **inert until positively included** via a carrier's `usercrates:` field (match block, common.vgo, role, or environments.vgo per-env block — the same four carriers `modules:` lives on). The module-level `name:` field is optional and defaults to the filename stem (`dan.vgo` → `dan`). Distinct from the `user:` [resource](#resource) type, which is one primitive inside a usercrate; the shape rule requires exactly one `type: user` resource, with additional non-user resources (file for authorized_keys/dotfiles, exec for post-create setup) permitted. See [User Management](reference/writing-modules.md).

**Usage:** drop `stockpile/usercrates/alice.vgo` to define alice, then add `usercrates: [alice]` to wherever she should apply — typically `stockpile/common.vgo` (fleet-wide) or a per-site `common.vgo` (scoped to that branch). Name collisions with regular modules are fatal at load time; usercrates and modules share one namespace, and `modules:` vs `usercrates:` reference disjoint sets (cross-kind misuse fails to load with the corrected field hint). Unreferenced usercrates emit a publish-time warning.

## lockbox

Per-user always-encrypted home-directory sync on the [swarm](#swarm) substrate. Membership is the user's [puddle](#puddle) (ADR-020 — lockbox is puddle-only). Files dropped into `~/lockbox/` are age-encrypted to every envoy in the user's puddle and synced as ciphertext — every envoy holds the same encrypted bytes, only puddle members can decrypt. Requires the user's lockbox password for decryption. The encryption-first sibling of [longdrawer](#longdrawer): same transport, different content rules. See [Lockbox](howto/set-up-lockbox.md).

## puddle

Per-user puddle identity primitive (ADR-014). A single Ed25519 keypair representing "this user's collection of envoys, treated as one trust unit," wrapped under the user's passphrase using lockbox's Argon2id+XChaCha20 envelope. Sibling subsystems ([gitback](#gitback) under ADR-015, lockbox membership under ADR-016) consume it as a trust anchor — "envoys in puddle X" expressed as a single pubkey rather than a per-pair invite ceremony. Onboarding via [peer-pair](#peer-pair); manual scp survives as the air-gapped fallback. State at `~/.vigo-puddle/`. CLI: `vigo swarm puddle {init,pair,join,unlock,lock,pubkey,members,rotate-passphrase,status}`. See [Puddle](howto/set-up-puddle.md).

## peer-pair

The primary onboarding flow for [puddle](#puddle) and (by ADR-015) the unified gitback substrate. The active envoy issues a 5-minute single-use 8-character pairing code (`NNNN-NNNN`); the joining envoy POSTs the code over swarm mTLS (port 1531) to receive the wrapped identity blob. The user's passphrase never leaves the destination envoy. Same human-explicit-authorization model as Signal/Bluetooth pairing; the dual gate of mTLS-trusted-CA + dictated code prevents any other fleet-CA peer from silently requesting the blob.

## gitback

Personal disaster-recovery source-control distribution on the [swarm](#swarm) substrate. The agent scans `~/gitback/<project>/` for repositories with a `.gitback.yaml` sidecar, packs them into git [bundles](#bundle), and distributes them as root-owned bare repositories on peer envoys. Pure P2P on top of swarm — no server involvement in selection or delivery. Default-deny per-envoy in 0.41.0+; operators authorize hosts via the `swarm.gitback.enabled` pattern list in server.yaml (first-match-wins, `-` prefix denies). Per-user opt-in is gated by the `gitback: true` flag on the user resource in the usercrate. See [Gitback](howto/set-up-gitback.md).

## longdrawer

Per-user home-directory sync on the [swarm](#swarm) substrate. Files in `~/longdrawer/` replicate to peer envoys over swarm's mTLS peer HTTPS channel, with longdrawer's own LAN multicast group on `224.0.0.43:1532` for discovery. Unlike [lockbox](#lockbox), longdrawer is *not* puddle-scoped — the sync set is the envoys on the user's LAN segment (plus any `cross_subnet_peers:`) that hold a `~/longdrawer/` directory for that user. Plaintext at rest (encryption moved to [lockbox](#lockbox) as of 0.26.1). Cross-subnet peers via `cross_subnet_peers:` in `~/.longdrawer/config.yaml`. See [Longdrawer](howto/set-up-longdrawer.md).

## audit bundle

A self-contained zip a Vigo operator can hand directly to an external auditor. Generated per-framework via [`GET /api/v1/report/audit-bundle/{slug}`](reference/api.md#audit-bundle-export) or `vigocli report audit-bundle <framework>`. Contains `manifest.json` (SHA-256 of every other artifact), `report.pdf` (WeasyPrint-rendered) + `report.html` (auditor-readable fallback), `evidence.json` (structured per-control: scope, status, modules, attached docs), `docs/<id>__<filename>` (decrypted copies of every operator-uploaded compliance document linked to the framework's controls), `audit-log.csv` (audit-chain entries within the lookback window), and `convergence.csv` (daily fleet convergence in the same window). Default period is 365 days; configurable per request. Verify integrity after extraction with `jq -r '.artifacts[] | "\(.sha256)  \(.path)"' manifest.json | sha256sum -c`. Shipped 0.32.5.

## scope

A per-control classification on every framework catalog entry that captures who is responsible for satisfying it. Shipped 0.32.3 alongside the catalog completion in 0.32.2.

- **enforced** — Vigo modules apply the technical control (firewall rules, package state, file mode, sysctl, user management, SSH config, AppArmor profiles, etc.). Coverage flips when an envoy's modules carry the matching `compliance:` tag.
- **attested** — Vigo collects evidence but doesn't enforce. Drift detection, [security_scan](reference/vigo/traits/) trait, audit-chain integrity, [Connwaer](#connwaer) fleet checks. Coverage requires the relevant signal to be present and healthy.
- **documented** — operator satisfies via uploaded artifact (BAA, IR plan, risk assessment, training records, signed contracts). Coverage flips when at least one active [compliance doc](#compliance-docs) is linked to the (framework, control_id). Without an attached doc, documented-scope controls render as "missing — requires uploaded documentation".
- **not_applicable** — control is out of deployment scope (e.g., HIPAA "Isolating Health Care Clearinghouse Functions" for a non-clearinghouse). Excluded from the framework denominator.

Surfaced as the Scope column on per-envoy compliance detail pages so a "missing" control tells the operator whether to assign a module or upload a document. See [Compliance](reference/compliance-matrix.md).

## compliance role

The third web-UI role added 0.34.2, sitting between admin and viewer. Default permissions: `compliance.read` + `compliance.docs.write` only — every framework page is visible, supporting documentation can be uploaded, and the per-framework audit-bundle export downloads (it's classified as `compliance.read` because the bundle is an evidence artifact, not the raw audit-log stream). The role deliberately excludes `audit.read`, `fleet.read`, and every mutation permission, so a holder can't see envoys/modules/config/secrets/scrier/payload, can't publish or dispatch tasks, and can't read the hash-chained audit log directly. Intended for GRC staff and compliance-document uploaders, **not** auditors — auditors should be assigned `admin` or `viewer` depending on the depth of evidence access required. Stored as `role: 'compliance'` in `users.role`; the DB CHECK constraint enumerates the three valid values. See [Authentication](howto/set-up-auth.md#roles-and-permissions).

## compliance docs

The operator-uploaded artifact store that backs `documented`-scope framework controls — BAAs, incident response plans, risk assessments, training records, signed third-party contracts, etc. Shipped 0.32.4. Files land envelope-encrypted at rest under `compliance.docs.docs_dir` (default `/srv/vigo/compliance-docs/`), wrapped with the same AES-256-GCM master key the secrets provider uses. Metadata lives in the `compliance_docs` table; per-control attachments live in `compliance_doc_links` so a single uploaded BAA can satisfy HIPAA 164.308(b), HITRUST 09.f, SOC 2 CC9.2 with one upload.

Magic-byte MIME validation rejects renamed file types (e.g., a JPEG masquerading as `.pdf`). Default allowlist: PDF, DOCX, XLSX, PPTX, TXT, MD, PNG, JPG, SVG. Configurable size caps: 50 MiB per doc, 5 GiB per framework, 50 GiB total — overridable in `compliance.docs.{max_doc_bytes, max_per_framework_bytes, max_total_bytes}`.

Soft-delete is the default — the doc disappears from listings but the ciphertext stays on disk so historical [audit bundles](reference/api.md#compliance-documentation) can still verify it. Physical purge requires the dedicated `compliance.docs.purge` permission. Every upload, replace, soft-delete, purge, link, unlink, and download writes a tamper-evident audit-chain entry with the doc's SHA-256.

Operators interact via the "Supporting Documentation" panel in the per-control drilldown on the Compliance Evidence page, or via [`POST /api/v1/compliance/docs`](reference/api.md#compliance-documentation) for tooling.

Available only on the `local` secrets backend today; under any other backend the routes return `503` and documented-scope controls stay missing.

## waiver

A per-envoy compliance exception — "accept this control as satisfied despite the non-enforcement." Waivers live in `stockpile/**/waivers.vgo` (single-tree as of 0.33.1) and inherit through the directory chain: root applies fleet-wide, subdirectory waivers apply only to envoys under that subtree. Each waiver carries a `control` ID, `reason`, `approved_by`, and optional `expires:` date. Waived controls count as "accepted" in compliance scoring and render with a distinct Lavender "waived" badge on dashboards — not hidden. Distinct from [compliance claim](#compliance), which declares what a module satisfies; a waiver declares what an envoy is excused from. See [Compliance Waivers + Claims](reference/writing-modules.md#compliance-waivers--claims).

## stacks

The parent directory for the whole Vigo config operation, at `/srv/vigo/stacks/`. Contains three sibling subtrees: [stockpile](#stockpile) (the operator-edited working tree), [.live](#live) (validated published output, what the server reads), and [examples](#examples) (read-only reference templates seeded by the image). Operators edit stockpile and run `vigocli config publish` to sync into .live. Shipped 0.33.1, replacing the earlier `worksite/{stockpile,scaffolding,structure}/` layout — the prior `scaffolding/` tree folded into stockpile, `structure/` renamed to `.live/`. Existing installs migrate automatically on first boot.

## examples

The read-only install-template tree under [stacks](#stacks), at `/srv/vigo/stacks/examples/`. Seeded from the image at container start with `*.vgo.example` templates the operator can adapt. Materialized into [stockpile](#stockpile) via `vigocli config examples copy <name>`, which strips the `.example` suffix and preserves the source subpath. Never read by the server, never published — it's a parts catalog. Pair with [anti-examples](#anti-examples), which holds the retract template for every module example.

## anti-examples

The read-only retract-template tree under [stacks](#stacks), at `/srv/vigo/stacks/anti-examples/`. Mirrors the directory layout of `examples/modules/` so every install template at `examples/modules/<cat>/<name>.vgo.example` has a paired retract at `anti-examples/modules/<cat>/<name>-retract.vgo.example`. Materialized into [stockpile](#stockpile) via `vigocli config anti-examples copy <name>` — the retract drops in alongside its install module without colliding because of the `-retract` suffix. `vigocli config anti-examples mirror-check` walks both trees and reports module examples missing a paired retract (or orphan retracts). Shipped 0.33.1.

## live

The validated published output tree under [stacks](#stacks), at `/srv/vigo/stacks/.live/`. `vigocli config publish` lints [stockpile](#stockpile) and atomically syncs canonical bytes here; the server reads `.live/` on every reload. Locked read-only between publishes. Never hand-edit — the loader's filename validator will refuse reload on a misplaced file. Renamed from `structure/` (0.28.0) back to `.live/` in 0.33.1.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](legal/license.md).*
