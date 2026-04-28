
# Glossary

Canonical definitions for Vigo-specific terms. Alphabetical. Each entry is one paragraph with cross-links to the page that covers the concept in depth.

If you hit a term in the docs or CLI that isn't here, that's a doc bug — please flag it.

## blob

An opaque byte sequence distributed by the [swarm](../engineering/subsystems/swarm.md) subsystem, addressed by its SHA-256 hash. A blob is immutable: re-distributing updated content means seeding a new blob and revoking the old one. Agents store blobs in `/var/lib/vigo/swarm/.blobs/` and materialize symlinks to them under `/var/lib/vigo/swarm/files/` per the manifest's target paths.

## bundle

A self-contained git repository archive produced by `git bundle create`, used as [gitmirror](../engineering/subsystems/gitmirror.md)'s unit of distribution. Bundles can be full (complete repository history) or incremental (deltas since a previous bundle). Receivers materialize bundles into bare repositories at `/var/lib/vigo/swarm/gitmirror/mirrors/<seeder>/<user>/<project>.git`.

## compliance

Standards-framework coverage. Splits into two surfaces, kept separate as of 0.32.0:

- **Envoy compliance** — whether a managed node's modules satisfy controls in regulatory frameworks (HIPAA, SOC 2, PCI DSS, NIST 800-53, ISO 27001, etc.). Measured as coverage percentage per framework. A control counts as covered iff (a) the envoy has a module tagged with that control via `compliance:`, (b) a [Connwaer](#connwaer) trait check on the envoy escalates it, or (c) a [waiver](#waiver) applies. This is the surface framework reports under `/api/v1/report/<slug>` measure.
- **Vigo product compliance** — what the Vigo server and agent satisfy by construction (mTLS, RBAC, audit hash chain, secret encryption, etc.). Lives in `server/compliance/AppControls` and is surfaced separately at `/platform/compliance` (Platform → Data). Useful for vendor questionnaires; never folded into framework scoring.

Distinct from [convergence](#convergence): convergence is "did my modules apply successfully," compliance is "do my modules satisfy framework X." See [scope](#scope) for how each framework control is classified, and [Compliance](../compliance/overview.md) for the full overview.

## connwaer

A standalone Rust agent for active compliance verification — checking things Vigo can't prove through enforcement alone (WORM storage, RNG validation, encryption at rest, network segmentation). Runs alongside the vigo agent on enrolled envoys. Separate product with its own license file and independent node counting. See the [Connwaer docs](../connwaer/_index.md).

## convergence

Module-convergence status — whether an envoy's assigned modules have been applied successfully. Statuses: `converged`, `relapsed`, `diverged`, `degraded`, `failed`, `no_data`. "Degraded" means some modules failed but not all. Reachability (online/offline) is tracked separately. See the [Convergence Walkthrough](../engineering/architecture/convergence-walkthrough.md).

## envoy

A managed node running the `vigo` Rust agent. Each envoy enrolls once, receives a client UUID, and checks in periodically to pull desired state and report results. The central server addresses individual envoys by UUID; operators reference them by hostname.

## executor

The Rust implementation of a resource type in the agent (`file`, `package`, `service`, `user`, `cron`, etc.). Executors check current state, act only on drift (idempotent), and return one of `None`/`Created`/`Updated`/`Deleted`/`Skipped`/`Blocked`. See the [Executor Reference](executors/_index.md).

## payload

Administrator-pushed file distribution on top of the [swarm](#swarm) substrate. An operator runs `vigocli swarm distribute <file>` (or drops a payload + `.swarm` sidecar on a seeder) and the blob replicates across the fleet via swarm's peer channel. Payload is one of four content subsystems that ride swarm; the other three ([filesync](#filesync), [gitmirror](#gitmirror), [cryptsync](#cryptsync)) are user-scoped, while payload is operator-scoped and fleet-wide. Surfaced in the web UI at [`/payload`](ui/swarm-payload.md). The CLI verb still reads `vigocli swarm …` because it predates the name split.

## manifest

The LWW-CRDT metadata store for swarm blobs, gossiped between envoys. Tracks which blobs exist, their targets, retention policies, and revocation tombstones. Each envoy holds a local manifest; version mismatches during multicast trigger manifest sync over the peer HTTPS channel. See [Swarm Manifest](../engineering/subsystems/swarm.md#manifest).

## matchfile

Any `.vgo` file containing an `envoys:` block with `- match:` entries. Maps hostname patterns/globs to roles, modules, and vars. First-match-wins. Conventionally named `envoys.vgo` but the loader walks all `.vgo` files, so per-host filenames (`plex.vgo`, `web01.vgo`) also work. See [Multi-Axis Config](../engineering/writing-modules/multi-axis-config.md).

## module

A reusable resource set in `stockpile/modules/*.vgo`. Declares resources, default vars, and optional compliance claims. Composed into [roles](#role), applied to [envoys](#envoy) via [matchfiles](#matchfile) or directory-level `common.vgo` inheritance. Modules are the primary composition unit for envoy-wide config. See [Config Format](../engineering/writing-modules/config-format.md) and [Resource Language](../engineering/writing-modules/resource-types.md).

## resource

A single idempotent state declaration inside a module — one file, one package, one service, one user account, etc. Resources declare a `type:` (mapping to an [executor](#executor)), parameters, and optional `depends_on` / `notify` / `when:` attributes. See [Resource Language](../engineering/writing-modules/resource-types.md).

## retract

An auto-generated inverse of a module — file resources flipped to `state: absent`, services to `stopped`+`enabled: false`, packages to `absent`, etc. Used for clean module removal. Generated by `vigocli config publish` when retraction is enabled; the resulting `.retract` file is inert until renamed and applied. See [Module Retraction](../engineering/writing-modules/retraction.md).

## role

A named grouping of modules and vars defined in `roles.vgo` (or the legacy `roles/` directory). Supports `when:` (role-wide conditional), `case:` (per-platform module groups), and `includes:` (single-level role composition). Assigned to envoys via [matchfiles](#matchfile). See [Config Format](../engineering/writing-modules/config-format.md).

## sandgorgon

Vigo's bare metal lifecycle subsystem: PXE provisioning, BMC (Redfish) integration, decommission workflow with NIST 800-88 certificate generation, CSV/NetBox inventory import. Exposed at `/sandgorgon/` in the web UI and via `vigocli sandgorgon`. See [Sandgorgon](../sandgorgon/_index.md).

## scrier

Vigo's admin remote-access subsystem: interactive SSH, RDP, and VNC sessions to any enrolled envoy, routed through the envoy's outbound `AgentStream` + a dedicated `TunnelStream`. No inbound ports on the envoy side — the core MSP/customer-firewall value prop. Browser access at `/scrier/{envoy_id}`; CLI access via `vigocli scrier ssh`. See [Scrier](../engineering/subsystems/scrier.md).

## seed

The originating instance of a swarm blob on an envoy — the source of truth for that blob. Today seeds are created exclusively by `vigocli swarm payload distribute`, which writes into the server's bind-mounted seed directory. The envoy-originated seed verb (`vigo swarm seed add`) is deferred — see `pending.md` for status. Peers pull chunks from seeds until they have full local copies.

## spanner

Vigo's hub-spoke architecture for multi-server deployments. One `vigosrv` hub aggregates state from multiple spoke `vigosrv` instances via dedicated gRPC. Used for MSP deployments where each customer runs its own spoke and the MSP operator gets an aggregate dashboard at the hub. See [Spanner](../engineering/subsystems/spanner.md).

## scaffolding

The envoy-assignment tree under [worksite](#worksite), at `/srv/vigo/worksite/scaffolding/`. Holds everything that connects envoys to modules: matchfiles (`envoys.vgo`), [roles](#role) definitions (`roles.vgo`), `common.vgo` inheritance, [waivers](#waiver), `environments.vgo` per-environment overrides, and customer/site subdirs. Nothing in scaffolding is a module definition; all module definitions live in [stockpile](#stockpile). Shipped 0.29.0.

## stockpile

The module library tree under [worksite](#worksite), at `/srv/vigo/worksite/stockpile/`. Holds reusable module content: [modules](#stone) (module definitions), [usercrates](#usercrate), `templates/` (Go template files referenced by `source:`), and `compliance.vgo` claim-inheritance files. Nothing in stockpile talks about specific envoys; all envoy assignment lives in [scaffolding](#scaffolding). The narrowed-meaning stockpile shipped in 0.29.0 — earlier versions conflated module library and envoy rules in a single flat tree.

## stone

A module definition file in `stockpile/modules/**/*.vgo`. The directory name `modules/` is the filesystem-level construction metaphor for what the YAML + code call a [module](#module) — the YAML key (`modules:`) and Go/Rust identifiers (`ModuleDefinition`) still use "module" for industry-standard recognition. Operators writing `modules: [nginx]` in a role are referencing the stone at `stockpile/modules/nginx.vgo`. Filesystem rename shipped 0.29.0.

## structure

The validated published output tree under [worksite](#worksite), at `/srv/vigo/worksite/structure/`. `vigocli config publish` lints [stockpile](#stockpile) and [scaffolding](#scaffolding) and atomically syncs them here; the server reads `structure/` on every reload. Locked read-only between publishes. Never hand-edit — the loader's filename-per-tree validator will refuse reload on a misplaced file. Previously named `.live/`; renamed in 0.28.0.

## swarm

The envoy-only P2P network substrate. Swarm is the transport layer that all P2P content subsystems ride on: peer discovery (multicast on `224.0.0.42:1531` + registry-backed hints), mTLS HTTPS between envoys on port 1531, manifest CRDT gossip, and content-addressed chunking. Swarm does not itself define *what* gets distributed — the four content subsystems do: [payload](#payload) (admin-pushed blobs), [filesync](#filesync) (plaintext home-dir sync), [gitmirror](#gitmirror) (source-control repo mirrors), [cryptsync](#cryptsync) (encrypted home-dir sync). Each subsystem has its own content rules but reuses swarm's peer channel, port, and mTLS trust. See [Swarm](../engineering/subsystems/swarm.md).

## trait

A collected fact about an envoy, reported at check-in — OS, hardware, installed packages, listening ports, filesystem mounts, kernel modules, etc. Traits feed into `when:` expressions, compliance evaluation, inventory views, and trend analysis. Each trait has a dedicated collector in `agent/src/traits/`. See the [Trait Reference](traits/_index.md).

## usercrate

A config module scoped to one or more users' environments (account creation, home-dir files, dotfiles, personal cron jobs), analogous to a regular [module](#module) but organized around human and service-account identity. Usercrates live in `stockpile/**/usercrates/*.vgo` — any directory level — and auto-apply to every envoy matched by that directory's scope, without needing to list them in `common.vgo`. Per-envoy opt-out via `exclude_usercrates:` on match blocks. The module-level `name:` field is optional and defaults to the filename stem (`dan.vgo` → `dan`). Distinct from the `user:` [resource](#resource) type, which is one primitive inside a usercrate; a usercrate typically holds one user resource but may hold several when grouping by team, role, or project makes sense. See [User Management](../engineering/writing-modules/usercrates.md).

**Usage:** drop a usercrate file into `stockpile/usercrates/alice.vgo` to apply alice fleet-wide, or into `stockpile/<site>/usercrates/alice.vgo` to scope her to a specific branch. Per-envoy opt-out via `exclude_usercrates: [alice]` on a match block. Name collisions with regular modules are fatal at load time — usercrates and modules share one namespace.

## cryptsync

Per-user always-encrypted home-directory sync on the [swarm](#swarm) substrate. Files dropped into `~/cryptsync/` are age-encrypted to every enrolled envoy in the user's mesh and synced as ciphertext — every envoy holds the same encrypted bytes, only mesh members can decrypt. Requires the user's cryptsync password for decryption. The encryption-first sibling of [filesync](#filesync): same transport, different content rules. See [Cryptsync](../engineering/subsystems/cryptsync.md).

## gitmirror

Personal disaster-recovery source-control distribution on the [swarm](#swarm) substrate. The agent scans `~/gitmirror/<project>/` for repositories with a `.gitmirror.yaml` sidecar, packs them into git [bundles](#bundle), and distributes them as root-owned bare repositories on peer envoys. Pure P2P on top of swarm — no server involvement in selection or delivery. Default-on per-envoy; operators deny specific hosts via hostname globs in `swarm.gitmirror.blacklist` in server.yaml. See [Gitmirror](../engineering/subsystems/gitmirror.md).

## filesync

Per-user home-directory sync on the [swarm](#swarm) substrate. Files in `~/filesync/` replicate to peer envoys over swarm's mTLS peer HTTPS channel, with filesync's own LAN multicast group on `224.0.0.43:1532` for discovery. Plaintext at rest (encryption moved to [cryptsync](#cryptsync) as of 0.26.1). Cross-subnet peers via `cross_subnet_peers:` in `~/.filesync/config.yaml`. See [Filesync](../engineering/subsystems/filesync.md).

## scope

A per-control classification on every framework catalog entry that captures who is responsible for satisfying it. Shipped 0.32.3 alongside the catalog completion in 0.32.2.

- **enforced** — Vigo modules apply the technical control (firewall rules, package state, file mode, sysctl, user management, SSH config, AppArmor profiles, etc.). Coverage flips when an envoy's modules carry the matching `compliance:` tag.
- **attested** — Vigo collects evidence but doesn't enforce. Drift detection, [security_scan](traits/security_scan.md) trait, audit-chain integrity, [Connwaer](#connwaer) fleet checks. Coverage requires the relevant signal to be present and healthy.
- **documented** — operator satisfies via uploaded artifact (BAA, IR plan, risk assessment, training records, signed contracts). Coverage flips when at least one active [compliance doc](#compliance-docs) is linked to the (framework, control_id). Without an attached doc, documented-scope controls render as "missing — requires uploaded documentation".
- **not_applicable** — control is out of deployment scope (e.g., HIPAA "Isolating Health Care Clearinghouse Functions" for a non-clearinghouse). Excluded from the framework denominator.

Surfaced as the Scope column on per-envoy compliance detail pages so a "missing" control tells the operator whether to assign a module or upload a document. See [Compliance](../compliance/_index.md).

## compliance docs

The operator-uploaded artifact store that backs `documented`-scope framework controls — BAAs, incident response plans, risk assessments, training records, signed third-party contracts, etc. Shipped 0.32.4. Files land envelope-encrypted at rest under `compliance.docs.docs_dir` (default `/srv/vigo/compliance-docs/`), wrapped with the same AES-256-GCM master key the secrets provider uses. Metadata lives in the `compliance_docs` table; per-control attachments live in `compliance_doc_links` so a single uploaded BAA can satisfy HIPAA 164.308(b), HITRUST 09.f, SOC 2 CC9.2 with one upload.

Magic-byte MIME validation rejects renamed file types (e.g., a JPEG masquerading as `.pdf`). Default allowlist: PDF, DOCX, XLSX, PPTX, TXT, MD, PNG, JPG, SVG. Configurable size caps: 50 MiB per doc, 5 GiB per framework, 50 GiB total — overridable in `compliance.docs.{max_doc_bytes, max_per_framework_bytes, max_total_bytes}`.

Soft-delete is the default — the doc disappears from listings but the ciphertext stays on disk so historical [audit bundles](../reference/api/rest.md#compliance-documentation) can still verify it. Physical purge requires the dedicated `compliance.docs.purge` permission. Every upload, replace, soft-delete, purge, link, unlink, and download writes a tamper-evident audit-chain entry with the doc's SHA-256.

Operators interact via the "Supporting Documentation" panel in the per-control drilldown on the Compliance Evidence page, or via [`POST /api/v1/compliance/docs`](../reference/api/rest.md#compliance-documentation) for tooling.

Available only on the `local` secrets backend today; under any other backend the routes return `503` and documented-scope controls stay missing.

## waiver

A per-envoy compliance exception — "accept this control as satisfied despite the non-enforcement." Waivers live in `scaffolding/**/waivers.vgo` (phase 3, 0.29.1) and inherit through the scaffolding directory chain: root applies fleet-wide, subdirectory waivers apply only to envoys under that subtree. Each waiver carries a `control` ID, `reason`, `approved_by`, and optional `expires:` date. Waived controls count as "accepted" in compliance scoring and render with a distinct Lavender "waived" badge on dashboards — not hidden. Distinct from [compliance claim](#compliance), which declares what a module satisfies; a waiver declares what an envoy is excused from. See [Compliance Waivers + Claims](../engineering/writing-modules/config-format.md#compliance-waivers--claims).

## worksite

The parent directory for the whole Vigo config operation, at `/srv/vigo/worksite/`. Contains three sibling subtrees: [stockpile](#stockpile) (module library), [scaffolding](#scaffolding) (envoy-assignment rules), and [structure](#structure) (validated published output). Operators edit stockpile + scaffolding; the server reads structure. The construction metaphor: stockpile holds the [modules](#stone) (parts), scaffolding is the framework that determines what gets built where, structure is the finished building. Shipped 0.28.0; tree split into stockpile + scaffolding in 0.29.0.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
