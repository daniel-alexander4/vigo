// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package config handles loading and resolving per-envoy configuration
// from YAML files on disk. It integrates with the secrets provider to
// resolve "secret:" prefixes and supports publish-triggered reload.
package config

import (
	"fmt"
	"time"

	"git.alexander4.io/vigo/server/secrets"
	"gopkg.in/yaml.v3"
)

// SpannerMode defines the server's mode in a spanner deployment. Two
// modes: standalone is the default single-vigosrv case with no spanner
// participation; spanner means this vigosrv is a peer-equal bolt in a
// federation, owning a hostname-pattern partition and sharing the
// admissions roster via CRDT gossip (ADR-026). The pre-Phase-3
// founder/joiner distinction is retired — "founder" was only ever
// "whoever ran `vigocli spanner init` first", a historical fact, not a
// runtime role; once the hub-spoke runtime was deleted every bolt runs
// identical code.
type SpannerMode string

const (
	// ModeStandalone is the default: a single server handling everything.
	ModeStandalone SpannerMode = "standalone"
	// ModeSpanner is a peer-equal bolt in a spanner federation — it owns
	// a hostname-pattern partition and shares the admissions roster with
	// every other bolt via CRDT gossip.
	ModeSpanner SpannerMode = "spanner"
)

// SpannerSection configures the spanner federation. Omit entirely (or set
// mode: standalone) for single-server deployments. ADR-026 reshaped this
// for peer-equal admissions; the legacy hub-spoke fields (role, bolts,
// hub_addr, hub_fallback_addrs, auto_failover, bolt_id,
// transport.bootstrap_peers) are retired and the loader hard-errors on
// them.
type SpannerSection struct {
	Mode          SpannerMode     `yaml:"mode" doc:"Server mode in a spanner deployment (peer-equal model, ADR-026)." options:"standalone|spanner" default:"standalone" example:"spanner"`
	SpannerID     string          `yaml:"spanner_id" doc:"Federation identifier — operator-chosen, immutable, DNS-safe (1-64 ASCII alphanumeric + - _ ., no leading dot)." example:"alexander4-prod, acme-staging"`
	Patterns      []string        `yaml:"patterns" doc:"Hostname glob patterns this bolt owns. First-match-wins across the spanner; loader refuses overlap with another bolt's patterns at publish time + boot time." example:"[*.us-west.*, *.dc1.*]"`
	Peers         []string        `yaml:"peers" doc:"Addresses (host or host:port, REST/HTTPS) of the other bolts in this spanner. When set, this bolt auto-admits with each on boot — no manual invite/join token ceremony — adopting the shared fleet CA over the signed admission channel (ADR-026 config-driven peering). Empty = gossip/ceremony discovery only." example:"[10.0.0.2, bolt-3.dc1:8443]"`
	Snapshot      SnapshotConfig  `yaml:"snapshot" doc:"Two-tier snapshot cadence — Tier-1 counters via multicast, Tier-2 roster bytes via REST."`
	Transport     TransportConfig `yaml:"transport" doc:"Multicast gossip group for Tier-1 counter gossip."`
	Freshness     FreshnessConfig `yaml:"freshness" doc:"Liveness thresholds for declaring a bolt silent (consumes server/freshness/ defaults)."`
	FallbackAdmin string          `yaml:"fallback_admin" doc:"Kill-switch for admin-query behavior on partition." options:"local|fanout" default:"local" example:"local"`
}

// SnapshotConfig governs Tier-1 (counters) + Tier-2 (roster) cadences for
// the CRDT gossip per ADR-026. Tier-1 fires every 30s on the multicast
// group; Tier-2 serves on REST every 5m or on counter-mismatch from a peer.
type SnapshotConfig struct {
	Tier1Interval string `yaml:"tier1_interval" doc:"Tier-1 counter-gossip interval as a Go duration." default:"30s" example:"30s, 1m"`
	Tier2Interval string `yaml:"tier2_interval" doc:"Tier-2 roster-snapshot interval as a Go duration." default:"5m" example:"5m, 10m"`
}

// TransportConfig configures the multicast group for Tier-1 counter
// gossip per ADR-026. The group sits alongside longdrawer
// (224.0.0.43:1532) and lockbox (224.0.0.44:1533). Peer discovery is
// otherwise gossip-driven: a joining bolt learns the roster through the
// `vigocli spanner join` admission ceremony, and the CRDT-replicated
// roster is the peer list from then on — there is no server.yaml seed.
type TransportConfig struct {
	MulticastGroup string `yaml:"multicast_group" doc:"IPv4 multicast group for Tier-1 counter gossip." default:"224.0.0.45" example:"224.0.0.45"`
	MulticastPort  int    `yaml:"multicast_port" doc:"UDP port for Tier-1 counter gossip." default:"1534" example:"1534"`
}

// FreshnessConfig is the per-spanner override for the server/freshness/
// thresholds. Empty fields fall back to the package defaults. Reserved
// for Phase 2 wiring; consumers in 2A.1b ignore it.
type FreshnessConfig struct {
	StaleAfter string `yaml:"stale_after" doc:"Override the global staleness threshold for bolt liveness — Go duration string." example:"90s, 2m"`
}

// validateSpannerID enforces the same DNS-safe-name rules as
// puddle.ValidateFriendlyName: 1-64 ASCII alphanumeric + - _ .,
// no leading dot, and never the 64-hex-char shape that collides
// with content-addressed IDs. Logic mirrored here (rather than
// imported from puddle) to avoid a config→puddle dependency
// direction; keep in sync with puddle.ValidateFriendlyName.
func validateSpannerID(name string) error {
	const maxLen = 64
	if name == "" || len(name) > maxLen {
		return fmt.Errorf("spanner.spanner_id must be 1-%d characters", maxLen)
	}
	if name[0] == '.' {
		return fmt.Errorf("spanner.spanner_id must not start with '.'")
	}
	if len(name) == 64 && isLowerHex(name) {
		return fmt.Errorf("spanner.spanner_id must not look like a content-addressed id (64 lowercase hex characters)")
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
		if !ok {
			return fmt.Errorf(
				"spanner.spanner_id %q contains invalid character %q; use ASCII alphanumeric + '-' '_' '.'",
				name, r,
			)
		}
	}
	return nil
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// Validate checks that spanner config is internally consistent.
func (f SpannerSection) Validate() error {
	mode := f.Mode
	if mode == "" || mode == ModeStandalone {
		return nil
	}
	// spanner_id is required for every non-standalone mode — it's the
	// federation membership anchor; admissions table rows are scoped to it.
	if f.SpannerID == "" {
		return fmt.Errorf("spanner.spanner_id is required when mode is %q (DNS-safe operator-chosen identifier)", mode)
	}
	if err := validateSpannerID(f.SpannerID); err != nil {
		return err
	}
	if mode != ModeSpanner {
		return fmt.Errorf("spanner.mode must be standalone or spanner (got %q); the founder/joiner modes were retired in Phase 3 — every bolt is a peer-equal 'spanner'", mode)
	}
	// A spanner bolt owns a hostname-pattern partition; its patterns
	// must be present.
	if len(f.Patterns) == 0 {
		return fmt.Errorf("spanner.patterns is required when mode is 'spanner' (hostname glob list for this bolt's partition)")
	}
	return nil
}

// ServerConfig represents the top-level server configuration from server.yaml.
type ServerConfig struct {
	Server       ServerSection       `yaml:"server" docs:"reference/server-yaml#server" doc:"Listener addresses, TLS paths, and the server's own envoy hostname." long:"Core listener configuration — the gRPC port agents connect to, the HTTPS port for the web UI and REST API, paths to the TLS cert/key/CA chain, and the server's own envoy hostname used for trusted local operations. Edit these when moving the server to a different address, rotating TLS material, or changing the ports used by the fleet. A restart is required after any change."`
	Database     DatabaseSection     `yaml:"database" docs:"reference/server-yaml#database" doc:"SQLite or Postgres connection and retention settings for the run store." long:"Connection and retention settings for the run store, audit log, and run history. SQLite in WAL mode is the supported default for single-server deployments; Postgres is available for larger fleets. Retention knobs control how long convergence runs and audit events are kept before being pruned to hold disk growth in check."`
	Auth         AuthSection         `yaml:"auth" docs:"reference/server-yaml#auth" doc:"Web UI and REST API authentication (basic, OIDC, or isowebauth)." long:"Who is allowed to use the web UI and REST API, and how they authenticate. Pluggable backends: basic (bcrypt username/password, default for small teams), oidc (SSO via any OpenID Connect provider like Okta or Google Workspace), and isowebauth (SSH key signing via the desktop companion app). Local CLI traffic from the Docker bridge is always trusted as admin regardless of this setting."`
	CheckIn      CheckInSection      `yaml:"checkin" docs:"reference/server-yaml#checkin" doc:"Check-in interval, jitter, bundle TTL, and fleet-wide observe-only toggle." long:"How often each agent pulls from the server, how much random jitter to add so large fleets don't stampede at the same second, and how long cached bundles remain valid. Also hosts the fleet-wide observe-only switch that forces every envoy into dry-run mode — the cheapest way to pause all enforcement without pulling modules. Tune the interval for the trade-off between drift-detection latency and server load."`
	Watcher      WatcherSection      `yaml:"watcher" docs:"reference/server-yaml#watcher" doc:"Polls the secrets provider for rotations and force-pushes affected envoys." long:"Background task that polls the configured secrets provider on a schedule and, when a secret value changes, forces a push to every envoy that consumes it. Enables automatic secret rotation without waiting for the next regular check-in — critical when rotating database passwords, TLS keys, or API tokens with revocation expectations. Poll frequency trades detection speed for provider API load."`
	Secrets      secrets.Config      `yaml:"secrets" docs:"reference/server-yaml#secrets" doc:"Secrets provider backend and connection settings (local, isopass)." long:"Selects which backend resolves 'secret:' references in configcrate configs at agent check-in. Options: local (files under /srv/vigo/secrets, default for single-server), isopass (Alexander4 Isopass). The server fails fast on startup if the backend is unreachable — it never serves a configcrate with plaintext fallbacks."`
	Bootstrap    BootstrapConfig     `yaml:"bootstrap" docs:"reference/server-yaml#bootstrap" doc:"Agent bootstrap enrollment — trusted hostname/CIDR patterns and issued cert validity." long:"Controls how new agents are enrolled into the fleet via the initial HTTPS bootstrap call. Define trusted hostname patterns and CIDR blocks that short-circuit the one-time-token flow (useful for automated provisioning), plus the validity window on auto-issued agent certs. Tighten these in production — a permissive trusted_enrollment is the difference between friction-free onboarding and an open door."`
	Spanner      SpannerSection      `yaml:"spanner" docs:"reference/server-yaml#spanner" doc:"Spanner federation — peer-equal mode (standalone/spanner) per ADR-026." long:"Deployment topology — standalone (one vigosrv runs the whole fleet) or a peer-equal spanner where multiple vigosrvs share an admissions roster via CRDT gossip. In spanner mode every bolt is a write-equal peer: one operator runs 'vigocli spanner init' to found the federation, the rest join via the 'vigocli spanner join' admission ceremony, and from then on every bolt holds the same CRDT-replicated roster. Each bolt owns a hostname-pattern partition; the loader refuses overlap between bolts at publish + boot time. Set this when running an MSP deployment or a multi-region setup where one central server can't own all check-ins."`
	Tuning       TuningSection       `yaml:"tuning" docs:"reference/server-yaml#tuning" doc:"Operational tuning — timing windows, gRPC transport limits, and the Go GC target." long:"Low-level knobs for operators who have hit a scaling ceiling and need to hand-tune. Includes gRPC send/receive limits, concurrent stream caps, timing windows for online-vs-offline envoy classification, and the Go GC target (gogc). The GOMEMLIMIT soft memory limit is not here — it is auto-derived to ~80% of the cgroup-aware budget, or set via the standard GOMEMLIMIT env var. Leave at defaults until you see a specific symptom — most fleets never touch this section."`
	SMTP         SMTPSection         `yaml:"smtp" docs:"reference/server-yaml#smtp" doc:"Email relay for event notifications, digests, and scheduled evidence delivery." long:"SMTP relay used by event notifications, daily/weekly digests, and scheduled compliance evidence delivery. Most deployments should prefer the native integrations (Slack, PagerDuty, Jira) over SMTP — email is the least observable and least actionable channel. Keep this configured only when a recipient genuinely can't be reached any other way."`
	AI           AISection           `yaml:"ai" docs:"reference/server-yaml#ai" doc:"AI assistant provider (Claude, OpenAI, Ollama) for the Ask page and compliance tooling." long:"Wires the /security chat drawer and compliance-assist tooling to an LLM provider. Supports Claude (sharpest reasoning on Vigo-specific questions), OpenAI (cost/token trade-off), and Ollama (fully self-hosted, no third-party calls). Required for automatic .retract module drafting and for the AI-powered guardrail suggestions in the publish flow."`
	Backup       BackupSection       `yaml:"backup" docs:"reference/server-yaml#backup" doc:"Continuous Litestream WAL replication of the SQLite database to S3/GCS/filesystem/SFTP." long:"Continuous SQLite WAL replication via Litestream, shipping every committed transaction to an off-server destination (S3, GCS, a mounted filesystem, or SFTP). This is the primary disaster-recovery mechanism — combined with the stacks directory held in version control, a full server rebuild is a fresh install plus a restore-from-backup. Configure this for every deployment holding more than a test fleet."`
	Export       ExportSection       `yaml:"export" docs:"reference/server-yaml#export" doc:"Compliance export endpoints (SIEM, CMDB, OSCAL) — each toggled independently." long:"One-way outbound feeds for compliance and audit consumers: SIEM ingestion of audit events, CMDB sync of envoy inventory, and OSCAL-format framework reports for auditors. Each channel is toggled independently — enable only the consumers the organization actually uses. Configure this when the organization needs evidence flowing into an existing GRC or IT ops stack rather than being pulled via the REST API on demand."`
	Publish      PublishSection      `yaml:"publish" docs:"reference/server-yaml#publish" doc:"Blast-radius limits on config publish — convergence threshold, retraction drafting." long:"Safety rails around 'vigocli config publish' to stop a typo from fanning out to the whole fleet in 15 seconds. Includes a minimum-converged threshold (refuse to publish if too many envoys are already failing) and automatic .retract draft generation when modules are removed. Tune these higher in production fleets and lower in dev — the default is permissive because a lab with three envoys doesn't need a change-control board."`
	RateLimit    RateLimitSection    `yaml:"rate_limit" docs:"reference/server-yaml#rate_limit" doc:"Per-envoy and global rate limits on check-in RPCs." long:"Ceiling on check-in RPC rates, per-envoy and globally, to protect the server from a runaway agent or a stampeding herd. Requests over the limit are rejected with a retryable error so affected agents back off on their own. Tune this up only after confirming the server's bottleneck is genuinely check-in throughput — for most fleets the default limits sit well above normal traffic."`
	Maintenance  MaintenanceSection  `yaml:"maintenance" docs:"reference/server-yaml#maintenance" doc:"Global change freeze — publish is blocked until the configured deadline passes." long:"Global change-freeze switch that blocks 'vigocli config publish' until a specified deadline. Use this during code freezes, customer maintenance windows, or incident response when you need to guarantee no config changes can reach the fleet. Emergency bypass requires editing the file and reloading — not a runtime override — so the freeze is real."`
	Paths        PathsSection        `yaml:"paths" docs:"reference/server-yaml#paths" doc:"Filesystem paths for migrations, custom traits, docs, tasks, workflows, and agent binaries." long:"Override the default filesystem locations for migrations, custom trait collectors, shipped docs, task definitions, workflow definitions, and the agent-binary distribution directory. Change these only for non-standard installs (packaged distributions, per-host chroots) — the defaults under /srv/vigo and /var/lib/vigo cover every normal deployment."`
	Peers        PeersSection        `yaml:"peers" docs:"reference/server-yaml#peers" doc:"Peer replication — primary or peer role plus the address list for server redundancy." long:"Active-passive replication that keeps one or more standby vigosrv instances file-synced with the primary, so a server outage doesn't strand the fleet. The primary streams config, secrets, and TLS files to each peer over gRPC; agents receive the peer list at enrollment and automatically fall back to one if the primary becomes unreachable. Configure this whenever the fleet is too large to tolerate an unplanned outage — promotion is then a single 'vigocli server promote' command on a surviving peer."`
	Task         TaskSection         `yaml:"task" docs:"reference/server-yaml#task" doc:"Task dispatch — optionally require a named definition for every ad-hoc task." long:"Controls ad-hoc task dispatch safety. When require_definition is on, every task sent via 'vigocli task dispatch' must reference a pre-reviewed task definition in /srv/vigo/stacks/tasks — direct shell commands are refused. Enable this in production to force peer review of anything an operator can fan out to a fleet; leave it off in dev where quick one-off commands are the whole point."`
	Branding     BrandingSection     `yaml:"branding" docs:"reference/server-yaml#branding" doc:"Custom org name, logo, favicon, and light/dark/auto UI theme." long:"White-label the web UI with the organization's name (shown in the title bar and in exported reports), a custom logo and favicon, and a forced light/dark theme or auto. The logo is inlined into cover blocks of generated PDFs so emailed attestations carry the customer's brand. Useful for MSPs per-customer and for enterprises who want their audit reports to wear their own logo."`
	License      LicenseSection      `yaml:"license" docs:"reference/server-yaml#license" doc:"License enforcement mode — local count or aggregated across spanner bolts." long:"How node-count enforcement aggregates across your topology — 'local' counts envoys registered against this single vigosrv, 'aggregated' sums counts across every bolt in a spanner deployment and enforces a single fleet-wide cap. Pick aggregated on MSP hubs or enterprise spanner setups where one license covers the full customer base. Misconfiguring this either blocks legitimate enrollments (too strict) or silently lets the fleet drift past the contract (too loose)."`
	GRC          GRCSection          `yaml:"grc" docs:"reference/server-yaml#grc" doc:"GRC platform integrations (Vanta, Drata, ServiceNow) for pushing compliance evidence." long:"Outbound push of compliance evidence into third-party GRC platforms (Vanta, Drata, ServiceNow GRC, Hyperproof, and similar). Each GRC target has its own endpoint, token, and control-mapping block — the server translates Vigo's internal compliance state into whatever format the target expects. Configure this when the organization has already standardized on a GRC system and you want Vigo's evidence to land there without manual uploads."`
	Integrations IntegrationsSection `yaml:"integrations" docs:"reference/server-yaml#integrations" doc:"Native external platform integrations — Slack, PagerDuty, Jira, ITSM, SIEM, and asset systems." long:"First-class integrations with operator-facing platforms — chat (Slack, Teams), paging (PagerDuty, Opsgenie), ticketing (Jira, Zendesk, Linear, ServiceNow, Freshservice), ITSM (ConnectWise, Autotask, NinjaRMM, Kaseya), SIEM/logging (Splunk, Elastic, Datadog, Sumo, Sentinel), and asset management (Device42, Snipe-IT). Each platform is its own subsection with its own toggle and credentials; prefer these over the legacy webhook path for anything important — native integrations carry context the webhook format can't."`
	Risk         RiskSection         `yaml:"risk" docs:"reference/server-yaml#risk" doc:"Risk quantification — optional NVD CVE API enrichment." long:"Controls the per-envoy risk scoring engine. An optional NVD CVE API key enriches risk input data with CVSS metrics and vendor-attributed advisories — without it, CVE severity comes from the scanner output alone. Risk scores drive the dashboard Risk Posture column, the cyber-insurance attestation PDF, and compliance-visible drift signals, so enable the NVD key if the deployment is subject to insurance or audit scrutiny."`
	StreamEdit   StreamEditSection   `yaml:"stream_edit" docs:"reference/server-yaml#stream_edit" doc:"Guardrails for file resources that pipe content through agent-local scripts." long:"Guardrails on the file resource's 'stream_edit:' attribute, which pipes content through agent-local scripts before it's written to disk. Controls whether stream-edits are allowed at all, the allowed interpreters (bash, python, etc.), time/memory limits per transform, and whether results can be cached across convergence cycles. Turn stream-edits off in hardened deployments; tune the limits when debugging long-running transforms."`
	Compliance   ComplianceSection   `yaml:"compliance" docs:"reference/server-yaml#compliance" doc:"Which compliance frameworks are active in this deployment (empty = all)." long:"Scopes which compliance frameworks this deployment reports against. An empty list activates every framework Vigo knows — HIPAA, SOC 2, PCI DSS, ISO 27001, NIST 800-53, CIS, GDPR, and the rest. An explicit list restricts dashboards, reports, and waiver tooling to that subset — useful when the organization is only audited against one or two frameworks and seeing the full catalog adds noise."`
	Scrier       ScrierSection       `yaml:"scrier" docs:"reference/server-yaml#scrier" doc:"Scrier browser-based remote access — SSH and RDP via guacd tunnel." long:"Browser-based remote access to any enrolled envoy — interactive SSH, Windows RDP, or console-shadow VNC, all routed through the envoy's outbound agent stream so no inbound ports are required on the target side. Every session is logged in scrier_sessions and an audit event is emitted on disconnect. Configure this when administrators need ad-hoc access to envoys behind customer firewalls without punching holes in them."`
	Swarm        SwarmSection        `yaml:"swarm" docs:"reference/server-yaml#swarm" doc:"Peer-to-peer content distribution — seed directory, port, bandwidth cap." long:"Peer-to-peer content distribution between envoys over mTLS on port 1531 (agent-to-agent, not server-to-agent). Configures the seed directory the server writes into and the bandwidth cap as a percentage of detected link speed (so swarm transfers don't starve real workloads). Essential for fleets large enough that pulling every blob from the server would saturate its uplink."`
	Grafana      GrafanaSection      `yaml:"grafana" docs:"reference/server-yaml#grafana" doc:"Optional Grafana URL — surfaces dashboard links on the /health page." long:"Points the web UI at an external Grafana instance so the /health page can link operators straight to the bundled Vigo dashboards. Vigo does not run, manage, or authenticate against Grafana — the only field is a base URL, and it is purely a link target. Leave unset if you don't run Grafana or prefer not to advertise it in the UI."`
	Sandgorgon   SandgorgonSection   `yaml:"sandgorgon" docs:"reference/server-yaml#sandgorgon" doc:"Bare-metal lifecycle (Redfish BMC, decommission, NIST 800-88 wipe). Deferred post-v1 — default off." long:"Gates the sandgorgon bare-metal lifecycle REST surface (Redfish BMC power/disk control, the NIST 800-88 decommission workflow, CSV/NetBox asset import). The subsystem is deferred until after Vigo 1.0 and exposes destructive BMC operations against plaintext-stored BMC credentials, so the routes are NOT mounted unless this is explicitly enabled. Leave off."`
	I18n         I18nSection         `yaml:"i18n" docs:"reference/server-yaml#i18n" doc:"Web UI localization — enable non-English locales and the topbar language picker." long:"Opt-in localization of the web UI chrome (nav, buttons, labels) and the in-app docs. Off by default: with i18n disabled the UI renders in English with zero overhead and no language picker. When enabled, each request's locale is chosen by the operator's saved choice (a persistent cookie set from the topbar picker) first, then the browser's Accept-Language header, then the configured default. Translations are key-based with English fallback, so a missing string degrades to English rather than a blank. Operator-authored data (envoy names, run results, audit entries) and all commands/flags stay English regardless of locale."`
}

// SwarmSection configures peer-to-peer content distribution. The four
// content subsystems (puddle/lockbox, gitback, longdrawer, filecast)
// are gated per-envoy by hostname pattern lists. The top-level Enabled
// list controls whether the swarm substrate runs on a host at all;
// each sub-section's Enabled list further restricts that subsystem.
// Both layers must allow a host for the subsystem to be active there
// — default-deny across the board.
//
// See server/config/host_match.go for pattern semantics: empty list
// denies all, "*" allows all, "-rasp1" denies a specific host, first
// match wins, default-deny on no match.
type SwarmSection struct {
	Enabled             []string            `yaml:"enabled" doc:"Hostname pattern list (first-match-wins; \"-\" prefix denies; empty list disables) controlling which envoys run the swarm substrate. Both this and the per-subsystem list must allow a host for any subsystem to be active." default:"[]" example:"[\"*\"], [\"-rasp1\", \"*\"], [\"annlap\", \"plex\"]"`
	SeedDir             string              `yaml:"seed_dir" doc:"Directory where server-distributed seeds are written (bind-mounted to agents)." default:"/var/lib/vigo/swarm/seed" example:"/var/lib/vigo/swarm/seed"`
	Port                int                 `yaml:"port" doc:"TCP port on which agents serve blobs to each other over mTLS." default:"1531" example:"1-65535; must be open between envoys"`
	MaxBandwidthPercent int                 `yaml:"max_bandwidth_percent" doc:"Maximum percent of detected link speed agents will use for swarm transfers." default:"50" example:"1-100; 25 for shared links, 80 for dedicated"`
	ManifestLivenessTTL string              `yaml:"manifest_liveness_ttl" doc:"Active manifest entries unrefreshed for this duration are revoked by the retention pruner. Guards against orphaned entries when a publisher disappears. Empty / zero disables the sweep." default:"24h" example:"24h, 12h, 0 (disable)"`
	Filecast            FilecastSection     `yaml:"filecast" doc:"Filecast (admin-pushed fleet file distribution) authorization." long:"Hostname pattern list controlling which envoys receive admin-pushed files. Distribution requests are refused at the API layer when the list is empty."`
	Longdrawer          LongdrawerSection   `yaml:"longdrawer" doc:"Longdrawer (LAN plaintext directory mirror) authorization + per-file size cap." long:"Hostname pattern list controlling which envoys run the longdrawer daemon. Per-user opt-in still requires the user creating ~/longdrawer/. Plus a per-file size cap on LAN sync."`
	Puddle              SwarmPuddleSection  `yaml:"puddle" doc:"Puddle (per-user identity) authorization. Lockbox derives ITS AUTHORIZATION from this section per ADR-020 — only operational tuning lives in lockbox below." long:"Hostname pattern list controlling which envoys can host puddle identity material. Lockbox's authorization (allow/deny) follows this list — a host allowed here can hold lockbox ciphertext for its locally-enrolled puddle members; a host denied here cannot use lockbox at all."`
	Lockbox             SwarmLockboxSection `yaml:"lockbox" doc:"Lockbox operational tuning (size + envelope retention). Authorization derives from puddle above; this section is size/retention only." long:"Per-file ciphertext size cap and envelope (LEAVE/RESURRECT/TOMBSTONE) retention window. Authorization gating still derives from swarm.puddle.enabled per ADR-020 — there is no enabled field here."`
	Gitback             GitbackSection      `yaml:"gitback" doc:"Gitback (git-over-swarm) authorization + bundle size + envelope retention." long:"Hostname pattern list controlling which envoys participate in gitback — both as publishers and as bundle holders. Plus per-bundle fan-out size cap and revoke/leave envelope retention."`
	Curator             CuratorSection      `yaml:"curator" doc:"Curator (P2P artifact registry, ADR-024) authorization + per-blob size cap." long:"Hostname pattern list controlling which envoys participate in curator — both as publishers and as artifact-blob holders. Plus a per-blob (one {name,version,os,arch} artifact) size cap on the publish path. Publishing also requires a usercrate 'curator: true' flag and an unlocked puddle (a heavier grant than gitback)."`
	Poolq               PoolqSection        `yaml:"poolq" doc:"Poolq (P2P ordered-log queue, ADR-029) authorization + retention + per-message size cap." long:"Hostname pattern list controlling which envoys participate in poolq — publishers (founders) and log-holders. Plus a per-message size cap and a retention window (messages older than the window are pruned, and stale-on-ingest messages are refused). Publishing additionally requires a usercrate 'poolq: true' flag and an unlocked puddle (same heavier grant as curator)."`
	Hostdox             bool                `yaml:"hostdox" doc:"When true, each swarm-enabled agent maintains a delimited block in /etc/hosts mapping every announced peer hostname to its LAN IP — additive name resolution for fleets whose DNS doesn't own the local domain. Default false (opt-in)." default:"false" example:"true to let agents edit /etc/hosts; false (or omit) to leave it alone"`
}

// SwarmPuddleSection authorizes the puddle (per-user identity) subsystem
// per envoy. Lockbox derives its authorization from this section —
// there is no separate lockbox section. A host listed here can hold
// puddle identity material and lockbox ciphertext for its locally-
// enrolled members; a host denied here cannot do either.
type SwarmPuddleSection struct {
	Enabled []string `yaml:"enabled" doc:"Hostname pattern list (first-match-wins; \"-\" prefix denies; empty list disables) of envoys allowed to run puddle. Lockbox follows the same gate." default:"[]" example:"[\"*\"], [\"-rasp1\", \"*\"], [\"annlap\", \"plex\", \"girlslaptop\"]"`
}

// GitbackSection authorizes the gitback subsystem per envoy
// and tunes its operational caps. `Enabled` gates which envoys
// participate; `MaxBundleBytes` caps a project's `git bundle --all`
// size on every transfer path (publisher fan-out, push-receive,
// DR catch-up); `EnvelopeRetention` controls the retention window for
// revoke/leave gossip envelopes before they are reaped.
type GitbackSection struct {
	Enabled           []string `yaml:"enabled" doc:"Hostname pattern list (first-match-wins; \"-\" prefix denies; empty list disables) of envoys allowed to run gitback — publisher and receiver." default:"[]" example:"[\"*\"], [\"-rasp1\", \"*\"], [\"annlap\", \"plex\", \"girlslaptop\"]"`
	MaxBundleBytes    int64    `yaml:"max_bundle_bytes" doc:"Per-bundle (git bundle --all) size cap, in bytes, enforced on every gitback transfer path. 0 / unset = the 2 GiB compile-time default. There is no uncapped mode." default:"2147483648" example:"2147483648 (2 GiB), 5368709120 (5 GiB), 536870912 (512 MiB)"`
	EnvelopeRetention string   `yaml:"envelope_retention" doc:"Retention window for revoke/leave gossip envelopes before they are reaped. Empty = compile-time default (90d)." default:"90d" example:"90d, 30d, 365d"`
}

// CuratorSection authorizes the curator subsystem per envoy and caps
// per-blob artifact size (ADR-024). `Enabled` gates which envoys
// participate — as publishers and as artifact-blob holders;
// `MaxBytes` caps an individual artifact blob (one {name,version,os,
// arch} build) on the publish path. Curator is a heavier grant than
// gitback: publishing also requires a usercrate `curator: true` flag
// and an unlocked puddle.
type CuratorSection struct {
	Enabled   []string `yaml:"enabled" doc:"Hostname pattern list (first-match-wins; \"-\" prefix denies; empty list disables) of envoys allowed to run curator — publishers and artifact-blob holders." default:"[]" example:"[\"*\"], [\"-rasp1\", \"*\"], [\"annlap\", \"plex\", \"girlslaptop\"]"`
	MaxBytes  int64    `yaml:"max_bytes" doc:"Per-blob (one {name,version,os,arch} artifact) size cap, in bytes, enforced on the publish path. 0 / unset = the 2 GiB compile-time default. There is no uncapped mode." default:"2147483648" example:"2147483648 (2 GiB), 5368709120 (5 GiB), 21474836480 (20 GiB, large container images)"`
	LocalPort uint32   `yaml:"local_port" doc:"Localhost-only HTTP port for curator's agent-local facades — the OCI Distribution v2 registry shim ('docker pull 127.0.0.1:<port>/<puddle-name>/<artifact>:<tag>', docker-compose 'image:') and the apt/dnf/apk package-repository server ('http://127.0.0.1:<port>/repo/<name>/...', consumed by a 'repository' resource). 0 (or unset) disables both facades; set to 5000 or any free port to enable. The agent binds 127.0.0.1 only — never an external interface." default:"0" example:"5000, 5050, 0 (disable)"`
	S3Port    uint32   `yaml:"s3_port" doc:"HTTPS port for the read-only S3-compatible object API (ADR-027) — exposes curator artifacts to non-envoy S3 clients (CI runners, dev laptops, any S3 SDK). 0 (or unset) disables the listener. Authenticated with AWS SigV4 against credentials minted by 'vigocli swarm curator s3-credentials'; reuses the server TLS material. Requires the 'local' secrets backend (the SigV4 secret derivation roots in the master key)." default:"0" example:"9000, 9443, 0 (disable)"`
}

// PoolqSection authorizes the poolq subsystem per envoy and caps
// per-message size + retention window (ADR-029). `Enabled` gates which
// envoys participate — as publishers (founders) and as log-holders;
// `MaxMsgBytes` caps an individual message body on the publish path;
// `Retention` is the per-topic age window — messages older than this
// are pruned by the mesh aggregator, and stale-on-ingest messages are
// refused (flap-stable pruning, no tombstones). Poolq is a heavier
// grant than gitback: publishing also requires a usercrate `poolq: true`
// flag and an unlocked puddle — same posture as curator.
type PoolqSection struct {
	Enabled     []string `yaml:"enabled" doc:"Hostname pattern list (first-match-wins; \"-\" prefix denies; empty list disables) of envoys allowed to run poolq — publishers (founders) and log-holders." default:"[]" example:"[\"*\"], [\"-rasp1\", \"*\"], [\"annlap\", \"plex\"]"`
	Retention   string   `yaml:"retention" doc:"Per-topic message-age retention window — messages older than this are pruned, and stale-on-ingest messages are refused. Empty = compile-time default (7d)." default:"7d" example:"7d, 24h, 30d"`
	MaxMsgBytes int64    `yaml:"max_msg_bytes" doc:"Per-message body size cap on the publish path, in bytes. 0 / unset = the 16 KiB compile-time default. The hard ceiling is 64 KiB — bigger payloads belong in a curator artifact." default:"16384" example:"16384 (16 KiB), 4096 (4 KiB), 65536 (64 KiB ceiling)"`
}

// LongdrawerSection authorizes the longdrawer subsystem per envoy
// and caps per-file LAN sync size. Per-user opt-in is still gated by
// the user creating ~/longdrawer/ — this list only controls whether
// the envoy's longdrawer daemon runs at all.
type LongdrawerSection struct {
	Enabled      []string `yaml:"enabled" doc:"Hostname pattern list (first-match-wins; \"-\" prefix denies; empty list disables) of envoys allowed to run longdrawer. Per-user participation still requires the user creating ~/longdrawer/." default:"[]" example:"[\"*\"], [\"-rasp1\", \"*\"]"`
	MaxFileBytes int64    `yaml:"max_file_bytes" doc:"Per-file size cap on LAN longdrawer. 0 = no cap." default:"1073741824" example:"1073741824 (1 GiB), 0 (uncapped)"`
}

// FilecastSection authorizes the filecast (admin-pushed fleet file
// distribution) subsystem per envoy and caps admin-side distribute
// behavior. The admin distribute endpoint refuses when the `Enabled`
// list is empty (no envoy could possibly receive); `MaxSizeBytes`
// caps individual blob size at the API entry; `DefaultRetention` is
// used when `vigocli swarm filecast distribute` omits `--retention`.
type FilecastSection struct {
	Enabled          []string `yaml:"enabled" doc:"Hostname pattern list (first-match-wins; \"-\" prefix denies; empty list disables) of envoys allowed to receive admin-pushed files." default:"[]" example:"[\"*\"], [\"-rasp1\", \"*\"]"`
	MaxSizeBytes     int64    `yaml:"max_size_bytes" doc:"Per-distribute blob size cap, enforced at the admin API. 0 = no cap." default:"0" example:"5368709120 (5 GiB), 0 (uncapped)"`
	DefaultRetention string   `yaml:"default_retention" doc:"Default retention applied when 'vigocli swarm filecast distribute' omits --retention. Empty = compile-time default (7d)." default:"7d" example:"7d, 30d, 24h"`
}

// SwarmLockboxSection tunes lockbox operational caps. Authorization
// is NOT specified here — lockbox derives its enable/deny gate from
// `SwarmPuddleSection` per ADR-020 (lockbox is bound to puddle
// membership). This section exists so admins can cap per-file
// ciphertext size and envelope retention without re-routing the
// authorization model.
type SwarmLockboxSection struct {
	MaxFileBytes      int64  `yaml:"max_file_bytes" doc:"Per-file cap on lockbox ciphertext (auto-encrypt + push). 0 = no cap. Authorization derives from puddle.enabled per ADR-020." default:"1073741824" example:"1073741824 (1 GiB), 0 (uncapped)"`
	EnvelopeRetention string `yaml:"envelope_retention" doc:"Retention window for LEAVE/RESURRECT/TOMBSTONE envelopes before they are reaped. Empty = compile-time default (90d)." default:"90d" example:"90d, 30d, 365d"`
}

// SandgorgonSection gates the sandgorgon bare-metal lifecycle subsystem.
// Deferred post-v1: the REST routes (Redfish BMC power/disk-wipe, the
// NIST 800-88 decommission workflow, asset import) are mounted only when
// Enabled is true. Default false. BMC passwords are currently stored
// plaintext (see server/sandgorgon/import.go) — another reason the surface
// stays off until that's wrapped via the secrets provider.
type SandgorgonSection struct {
	Enabled bool `yaml:"enabled" doc:"Mount the sandgorgon bare-metal lifecycle REST routes (Redfish BMC, decommission, asset import). Deferred post-v1; exposes destructive BMC operations against plaintext-stored credentials." default:"false" example:"true"`
}

// ScrierSection configures the Scrier remote access feature.
type ScrierSection struct {
	Enabled          bool     `yaml:"enabled" doc:"Enable browser-based remote sessions (SSH, RDP, VNC)." default:"false" example:"true"`
	GuacdAddr        string   `yaml:"guacd_addr" doc:"Address vigosrv uses to reach guacd (host:port). The bundled compose host-networks both, so guacd is on host loopback." default:"127.0.0.1:4822" example:"127.0.0.1:4822, 10.0.0.5:4822"`
	ProxyHostname    string   `yaml:"proxy_hostname" doc:"Hostname guacd uses to reach back to this server. Host loopback under the bundled host-networked compose." default:"127.0.0.1" example:"127.0.0.1, or a routable host for a remote guacd"`
	AllowedPorts     []uint32 `yaml:"allowed_ports" doc:"TCP ports that may be tunneled to managed envoys." default:"[22,3389,5900]" example:"[22, 3389, 5900]  # ssh, rdp, vnc"`
	MaxSessions      int      `yaml:"max_sessions" doc:"Maximum concurrent Scrier sessions on this server." default:"10" example:"5-20 typical; bound by guacd capacity"`
	RecordingEnabled bool     `yaml:"recording_enabled" doc:"Persist every session as a Guacamole .recording file for later replay." default:"false" example:"true (compliance/forensics)"`
	RecordingDir     string   `yaml:"recording_dir" doc:"Directory where session recordings are written." default:"/srv/vigo/recordings" example:"/srv/vigo/recordings"`
}

// ScrierGuacdAddr returns the guacd address, defaulting to "127.0.0.1:4822"
// (the bundled compose host-networks guacd alongside vigosrv).
func (s ScrierSection) ScrierGuacdAddr() string {
	if s.GuacdAddr == "" {
		return "127.0.0.1:4822"
	}
	return s.GuacdAddr
}

// ScrierAllowedPorts returns allowed tunnel ports, defaulting to SSH + RDP + VNC.
func (s ScrierSection) ScrierAllowedPorts() []uint32 {
	if len(s.AllowedPorts) == 0 {
		return []uint32{22, 3389, 5900}
	}
	return s.AllowedPorts
}

// ScrierProxyHostname returns the hostname guacd uses to reach the proxy,
// defaulting to "127.0.0.1" (guacd is host-networked alongside vigosrv).
func (s ScrierSection) ScrierProxyHostname() string {
	if s.ProxyHostname == "" {
		return "127.0.0.1"
	}
	return s.ProxyHostname
}

// ScrierMaxSessions returns the max concurrent sessions, defaulting to 10.
func (s ScrierSection) ScrierMaxSessions() int {
	if s.MaxSessions <= 0 {
		return 10
	}
	return s.MaxSessions
}

// ComplianceSection configures which compliance frameworks are active,
// which package-manager sources count as approved for the
// `package_source_audit` trait, and where operator-uploaded
// documentation artifacts (BAA, IR plan, risk assessment, training
// records, etc.) live.
type ComplianceSection struct {
	Standards       []string              `yaml:"standards" doc:"Enabled framework slugs (e.g. hipaa, soc2, pci). Empty means all frameworks are enabled." example:"[hipaa, soc2, pci_dss, nist_800_53]"`
	ApprovedSources []string              `yaml:"approved_sources" doc:"URL prefixes allowed as package-manager sources. Each package_source_audit.sources.N row is annotated with approved=true if its URL starts with any listed prefix, approved=false otherwise. Empty disables the annotation entirely." example:"[http://archive.ubuntu.com/, https://download.docker.com/, https://download.fedoraproject.org/]"`
	Docs            ComplianceDocsSection `yaml:"docs" doc:"Operator-uploaded documentation store for documented-scope controls." long:"Configures where Vigo writes envelope-encrypted compliance artifacts (BAA, IR plan, risk assessment, training records, signed contracts) and how aggressively to cap upload sizes. Files land at <docs_dir>/<id>, encrypted with the same AES-256-GCM master key used by the secrets provider; metadata lives in compliance_docs / compliance_doc_links."`
}

// ComplianceDocsSection configures the on-disk store for operator-
// uploaded compliance documentation.
type ComplianceDocsSection struct {
	DocsDir              string   `yaml:"docs_dir" doc:"Directory where envelope-encrypted compliance docs are written." default:"/srv/vigo/compliance-docs" example:"/srv/vigo/compliance-docs"`
	MaxDocBytes          int64    `yaml:"max_doc_bytes" doc:"Reject any single uploaded doc larger than this. 50 MiB default." default:"52428800" example:"52428800 (50 MiB), 104857600 (100 MiB)"`
	MaxPerFrameworkBytes int64    `yaml:"max_per_framework_bytes" doc:"Cap on the total ciphertext size attached to any single framework. 5 GiB default." default:"5368709120" example:"5368709120 (5 GiB)"`
	MaxTotalBytes        int64    `yaml:"max_total_bytes" doc:"Hard cap on total compliance-doc storage across the deployment. 50 GiB default." default:"53687091200" example:"53687091200 (50 GiB)"`
	MimeAllowlist        []string `yaml:"mime_allowlist" doc:"MIME types accepted for upload (validated against detected magic bytes, not just the declared header). Empty falls back to the built-in list (PDF, DOCX, XLSX, PPTX, TXT, MD, PNG, JPG, SVG)." example:"[application/pdf, image/png]"`
}

// ComplianceDocsDir returns the configured docs directory, defaulting
// to /srv/vigo/compliance-docs.
func (c ComplianceDocsSection) ComplianceDocsDir() string {
	if c.DocsDir == "" {
		return "/srv/vigo/compliance-docs"
	}
	return c.DocsDir
}

// ComplianceMaxDocBytes returns the per-doc upload cap in bytes,
// defaulting to 50 MiB.
func (c ComplianceDocsSection) ComplianceMaxDocBytes() int64 {
	if c.MaxDocBytes <= 0 {
		return 50 << 20
	}
	return c.MaxDocBytes
}

// ComplianceMaxPerFrameworkBytes returns the per-framework total cap,
// defaulting to 5 GiB.
func (c ComplianceDocsSection) ComplianceMaxPerFrameworkBytes() int64 {
	if c.MaxPerFrameworkBytes <= 0 {
		return 5 << 30
	}
	return c.MaxPerFrameworkBytes
}

// ComplianceMaxTotalBytes returns the deployment-wide total cap,
// defaulting to 50 GiB.
func (c ComplianceDocsSection) ComplianceMaxTotalBytes() int64 {
	if c.MaxTotalBytes <= 0 {
		return 50 << 30
	}
	return c.MaxTotalBytes
}

// ComplianceMimeAllowlist returns the configured MIME allowlist or the
// built-in default list when unset.
func (c ComplianceDocsSection) ComplianceMimeAllowlist() []string {
	if len(c.MimeAllowlist) == 0 {
		return []string{
			"application/pdf",
			"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"application/vnd.openxmlformats-officedocument.presentationml.presentation",
			"text/plain",
			"text/markdown",
			"image/png",
			"image/jpeg",
			"image/svg+xml",
		}
	}
	return c.MimeAllowlist
}

// StreamEditSection configures guardrails for stream_edit pipelines.
type StreamEditSection struct {
	Enabled        *bool    `yaml:"enabled" doc:"Allow file resources to pipe content through stream_edit scripts." default:"true" example:"false to fully disable"`
	AllowedPaths   []string `yaml:"allowed_paths" doc:"Directories that stream_edit scripts must reside within (absolute paths)." default:"[/srv/vigo/scripts]" example:"[/srv/vigo/scripts, /opt/vigo/bin]"`
	DefaultTimeout string   `yaml:"default_timeout" doc:"Per-script wall-clock timeout as a Go duration string." default:"10s" example:"5s, 10s, 30s (accepts s, m)"`
}

// StreamEditEnabled returns whether stream_edit is enabled (default true).
func (s StreamEditSection) StreamEditEnabled() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// StreamEditAllowedPaths returns the allowed directories (default ["/srv/vigo/scripts"]).
func (s StreamEditSection) StreamEditAllowedPaths() []string {
	if len(s.AllowedPaths) == 0 {
		return []string{"/srv/vigo/scripts"}
	}
	return s.AllowedPaths
}

// StreamEditTimeoutSeconds returns the per-script timeout in seconds (default 10).
func (s StreamEditSection) StreamEditTimeoutSeconds() uint32 {
	if s.DefaultTimeout == "" {
		return 10
	}
	// Parse duration string like "10s", "30s", etc.
	d, err := time.ParseDuration(s.DefaultTimeout)
	if err != nil {
		return 10
	}
	return uint32(d.Seconds())
}

// RiskSection configures risk quantification features.
type RiskSection struct {
	NVD NVDConfig `yaml:"nvd" doc:"Optional NIST NVD CVE API enrichment for CVE risk scoring."`
}

// NVDConfig configures optional NVD CVE API enrichment.
type NVDConfig struct {
	Enabled bool   `yaml:"enabled" doc:"Fetch CVE detail from the NVD API (CVSS, description, CWE, references) — feeds risk scoring and the security-drawer Ask-AI prompt context. Disable for air-gapped deployments." default:"true" example:"false"`
	APIKey  string `yaml:"api_key" doc:"NVD API key (supports the secret: prefix for indirect lookup)." example:"secret:vigo/nvd/api_key  (request at nvd.nist.gov/developers/request-an-api-key)"`
}

// IntegrationsSection configures external platform integrations.
type IntegrationsSection struct {
	Slack        *SlackIntegration        `yaml:"slack,omitempty" doc:"Slack — incoming webhook for formatted event messages."`
	PagerDuty    *PagerDutyIntegration    `yaml:"pagerduty,omitempty" doc:"PagerDuty — trigger incidents via the Events API v2."`
	Opsgenie     *OpsgenieIntegration     `yaml:"opsgenie,omitempty" doc:"Opsgenie — create alerts via the Opsgenie API."`
	Teams        *TeamsIntegration        `yaml:"teams,omitempty" doc:"Microsoft Teams — adaptive-card messages via incoming webhook."`
	ConnectWise  *ConnectWiseIntegration  `yaml:"connectwise,omitempty" doc:"ConnectWise Manage — open service-board tickets on drift or failure."`
	Autotask     *AutotaskIntegration     `yaml:"autotask,omitempty" doc:"Autotask / Datto PSA — open tickets on drift or failure."`
	Splunk       *SplunkIntegration       `yaml:"splunk,omitempty" doc:"Splunk — forward events to an HTTP Event Collector."`
	Elastic      *ElasticIntegration      `yaml:"elastic,omitempty" doc:"Elasticsearch — forward events to an index."`
	Datadog      *DatadogIntegration      `yaml:"datadog,omitempty" doc:"Datadog — forward events via the Datadog Events API."`
	ServiceNow   *ServiceNowIntegration   `yaml:"servicenow,omitempty" doc:"ServiceNow — sync envoy inventory as CIs and open incidents."`
	Jira         *JiraIntegration         `yaml:"jira,omitempty" doc:"Jira — create issues on drift or failure events."`
	Zendesk      *ZendeskIntegration      `yaml:"zendesk,omitempty" doc:"Zendesk — create tickets on drift or failure events."`
	Freshservice *FreshserviceIntegration `yaml:"freshservice,omitempty" doc:"Freshservice — create tickets on drift or failure events."`
	Linear       *LinearIntegration       `yaml:"linear,omitempty" doc:"Linear — create issues on drift or failure events."`
	GitHubIssues *GitHubIssuesIntegration `yaml:"github_issues,omitempty" doc:"GitHub Issues — create issues in a specified repository."`
	NinjaRMM     *NinjaRMMIntegration     `yaml:"ninjarmm,omitempty" doc:"NinjaRMM — create tickets via the NinjaRMM API."`
	Kaseya       *KaseyaIntegration       `yaml:"kaseya,omitempty" doc:"Kaseya VSA — create tickets via the VSA API."`
	SumoLogic    *SumoLogicIntegration    `yaml:"sumologic,omitempty" doc:"Sumo Logic — forward events to an HTTP source."`
	Sentinel     *SentinelIntegration     `yaml:"sentinel,omitempty" doc:"Microsoft Sentinel — forward events to Log Analytics."`
	Loki         *LokiIntegration         `yaml:"loki,omitempty" doc:"Grafana Loki — forward events as labeled log streams."`
	Device42     *Device42Integration     `yaml:"device42,omitempty" doc:"Device42 — sync envoy inventory to the Device42 CMDB."`
	SnipeIT      *SnipeITIntegration      `yaml:"snipeit,omitempty" doc:"Snipe-IT — sync envoy inventory to Snipe-IT asset management."`
}

// SlackIntegration sends formatted messages to a Slack channel.
type SlackIntegration struct {
	Enabled     bool     `yaml:"enabled" doc:"Send event notifications to Slack." default:"false" example:"true"`
	WebhookURL  string   `yaml:"webhook_url" doc:"Slack incoming webhook URL (supports the secret: prefix)." example:"secret:vigo/slack/webhook  (raw: https://hooks.slack.com/services/T00/B00/XXX)"`
	Events      []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[envoy.offline, module.failed, convergence.threshold]"`
	MinSeverity string   `yaml:"min_severity" doc:"Minimum severity to forward." options:"info|warning|critical" example:"warning"`
}

// PagerDutyIntegration triggers incidents via PagerDuty Events API v2.
type PagerDutyIntegration struct {
	Enabled     bool     `yaml:"enabled" doc:"Trigger PagerDuty incidents on events." default:"false" example:"true"`
	RoutingKey  string   `yaml:"routing_key" doc:"PagerDuty Events API v2 routing key (supports the secret: prefix)." example:"secret:vigo/pagerduty/routing_key  (32-char hex from a Vigo-type service)"`
	Events      []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[envoy.offline, module.failed]"`
	MinSeverity string   `yaml:"min_severity" doc:"Minimum severity to forward." options:"info|warning|critical" example:"critical (page on-call for critical only)"`
	AutoResolve bool     `yaml:"auto_resolve" doc:"Send resolve events to PagerDuty when the underlying issue clears." default:"false" example:"true to auto-close incidents"`
}

// OpsgenieIntegration triggers alerts via Opsgenie API.
type OpsgenieIntegration struct {
	Enabled     bool     `yaml:"enabled" doc:"Create Opsgenie alerts on events." default:"false" example:"true"`
	APIKey      string   `yaml:"api_key" doc:"Opsgenie API key (supports the secret: prefix)." example:"secret:vigo/opsgenie/api_key"`
	Events      []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[envoy.offline, convergence.threshold]"`
	MinSeverity string   `yaml:"min_severity" doc:"Minimum severity to forward." options:"info|warning|critical" example:"warning"`
}

// TeamsIntegration sends adaptive cards to Microsoft Teams.
type TeamsIntegration struct {
	Enabled     bool     `yaml:"enabled" doc:"Send adaptive cards to a Microsoft Teams channel." default:"false" example:"true"`
	WebhookURL  string   `yaml:"webhook_url" doc:"Teams incoming webhook URL (supports the secret: prefix)." example:"secret:vigo/teams/webhook  (raw: https://acme.webhook.office.com/webhookb2/...)"`
	Events      []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[module.failed, envoy.offline]"`
	MinSeverity string   `yaml:"min_severity" doc:"Minimum severity to forward." options:"info|warning|critical" example:"warning"`
}

// ConnectWiseIntegration creates tickets on drift/failure events.
type ConnectWiseIntegration struct {
	Enabled    bool     `yaml:"enabled" doc:"Open ConnectWise Manage tickets on events." default:"false" example:"true"`
	URL        string   `yaml:"url" doc:"ConnectWise API base URL (e.g. https://api-na.myconnectwise.net/v4_6_release/apis/3.0)." example:"https://api-na.myconnectwise.net/v4_6_release/apis/3.0"`
	CompanyID  string   `yaml:"company_id" doc:"ConnectWise company identifier." example:"acme_corp"`
	PublicKey  string   `yaml:"public_key" doc:"ConnectWise integrator public key." example:"abc123XYZ"`
	PrivateKey string   `yaml:"private_key" doc:"ConnectWise integrator private key (supports the secret: prefix)." example:"secret:vigo/connectwise/private_key"`
	BoardID    int      `yaml:"board_id" doc:"Service-board ID new tickets are created on." example:"42  (find under System → Setup Tables → Service Board)"`
	Events     []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[module.failed, envoy.offline]"`
}

// AutotaskIntegration creates tickets in Autotask/Datto PSA.
type AutotaskIntegration struct {
	Enabled  bool     `yaml:"enabled" doc:"Open Autotask (Datto PSA) tickets on events." default:"false" example:"true"`
	URL      string   `yaml:"url" doc:"Autotask API base URL." example:"https://webservices14.autotask.net/atservicesrest/v1.0/"`
	Username string   `yaml:"username" doc:"Autotask API user." example:"vigo-api@acme.com"`
	Password string   `yaml:"password" doc:"Autotask API secret (supports the secret: prefix)." example:"secret:vigo/autotask/api_secret"`
	QueueID  int      `yaml:"queue_id" doc:"Queue ID new tickets are placed in." example:"29683874  (find under Admin → Queues)"`
	Events   []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[module.failed, drift.detected]"`
}

// SplunkIntegration forwards events to Splunk HTTP Event Collector.
type SplunkIntegration struct {
	Enabled bool     `yaml:"enabled" doc:"Forward events to Splunk via HEC." default:"false" example:"true"`
	URL     string   `yaml:"url" doc:"Splunk HTTP Event Collector endpoint URL." example:"https://splunk.example.com:8088/services/collector"`
	Token   string   `yaml:"token" doc:"HEC token (supports the secret: prefix)." example:"secret:vigo/splunk/hec_token  (UUID from Splunk HEC config)"`
	Index   string   `yaml:"index" doc:"Splunk index name (optional — falls back to the token default)." example:"vigo, security, main"`
	Source  string   `yaml:"source" doc:"Source identifier attached to every event." default:"vigo" example:"vigo, vigo-prod, vigo-us-west"`
	Events  []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[all events — recommended for SIEM ingest]"`
}

// ElasticIntegration forwards events to Elasticsearch.
type ElasticIntegration struct {
	Enabled bool     `yaml:"enabled" doc:"Forward events to Elasticsearch." default:"false" example:"true"`
	URL     string   `yaml:"url" doc:"Elasticsearch base URL." example:"https://elastic.example.com:9200, https://es.cloud:9243"`
	Index   string   `yaml:"index" doc:"Target index name." default:"vigo-events" example:"vigo-events, security-vigo-%{+yyyy.MM}"`
	APIKey  string   `yaml:"api_key" doc:"Elasticsearch API key (supports the secret: prefix)." example:"secret:vigo/elastic/api_key  (base64 id:api_key)"`
	Events  []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[] (forward all)"`
}

// DatadogIntegration forwards events to Datadog.
type DatadogIntegration struct {
	Enabled bool     `yaml:"enabled" doc:"Forward events to Datadog." default:"false" example:"true"`
	APIKey  string   `yaml:"api_key" doc:"Datadog API key (supports the secret: prefix)." example:"secret:vigo/datadog/api_key  (32-char hex from Integrations → APIs)"`
	Site    string   `yaml:"site" doc:"Datadog site." options:"datadoghq.com|datadoghq.eu|us3.datadoghq.com|us5.datadoghq.com|ap1.datadoghq.com|ddog-gov.com" default:"datadoghq.com" example:"datadoghq.com (US1), datadoghq.eu (EU1)"`
	Events  []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[envoy.offline, module.failed]"`
}

// ServiceNowIntegration syncs envoy inventory as CIs and creates incidents.
type ServiceNowIntegration struct {
	Enabled  bool     `yaml:"enabled" doc:"Sync envoys as CIs and open incidents in ServiceNow." default:"false" example:"true"`
	Instance string   `yaml:"instance" doc:"ServiceNow instance hostname (e.g. mycompany.service-now.com)." example:"acme.service-now.com"`
	Username string   `yaml:"username" doc:"ServiceNow user." example:"vigo.integration"`
	Password string   `yaml:"password" doc:"ServiceNow password (supports the secret: prefix)." example:"secret:vigo/servicenow/password"`
	Events   []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[envoy.enrolled, envoy.offline, module.failed]"`
}

// JiraIntegration creates issues in Jira on drift/failure events.
type JiraIntegration struct {
	Enabled   bool     `yaml:"enabled" doc:"Open Jira issues on events." default:"false" example:"true"`
	URL       string   `yaml:"url" doc:"Jira Cloud base URL (e.g. https://mycompany.atlassian.net)." example:"https://acme.atlassian.net"`
	Email     string   `yaml:"email" doc:"Jira account email used for API auth." example:"vigo@acme.com"`
	APIToken  string   `yaml:"api_token" doc:"Jira API token (supports the secret: prefix)." example:"secret:vigo/jira/api_token  (create at id.atlassian.com/manage-profile/security/api-tokens)"`
	Project   string   `yaml:"project" doc:"Jira project key new issues are created under (e.g. OPS)." example:"OPS, INFRA, SRE"`
	IssueType string   `yaml:"issue_type" doc:"Jira issue type for new issues." default:"Task" example:"Task, Bug, Incident"`
	Events    []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[module.failed, drift.detected]"`
}

// ZendeskIntegration creates tickets in Zendesk.
type ZendeskIntegration struct {
	Enabled   bool     `yaml:"enabled" doc:"Open Zendesk tickets on events." default:"false" example:"true"`
	Subdomain string   `yaml:"subdomain" doc:"Zendesk subdomain (e.g. \"mycompany\" for mycompany.zendesk.com)." example:"acme  (for acme.zendesk.com)"`
	Email     string   `yaml:"email" doc:"Zendesk user email." example:"vigo@acme.com"`
	APIToken  string   `yaml:"api_token" doc:"Zendesk API token (supports the secret: prefix)." example:"secret:vigo/zendesk/api_token"`
	Events    []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[envoy.offline, module.failed]"`
}

// FreshserviceIntegration creates tickets in Freshservice.
type FreshserviceIntegration struct {
	Enabled bool     `yaml:"enabled" doc:"Open Freshservice tickets on events." default:"false" example:"true"`
	Domain  string   `yaml:"domain" doc:"Freshservice domain (e.g. mycompany.freshservice.com)." example:"acme.freshservice.com"`
	APIKey  string   `yaml:"api_key" doc:"Freshservice API key (supports the secret: prefix)." example:"secret:vigo/freshservice/api_key  (Profile → API Key)"`
	Events  []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[module.failed, envoy.offline]"`
}

// LinearIntegration creates issues in Linear.
type LinearIntegration struct {
	Enabled bool     `yaml:"enabled" doc:"Create Linear issues on events." default:"false" example:"true"`
	APIKey  string   `yaml:"api_key" doc:"Linear API key (supports the secret: prefix)." example:"secret:vigo/linear/api_key  (starts with lin_api_)"`
	TeamID  string   `yaml:"team_id" doc:"Linear team ID new issues are created under." example:"5f7a9c3e-2d14-4b2e-bb81-abc123def456"`
	Events  []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[module.failed]"`
}

// GitHubIssuesIntegration creates issues in a GitHub repository.
type GitHubIssuesIntegration struct {
	Enabled bool     `yaml:"enabled" doc:"Create GitHub issues on events." default:"false" example:"true"`
	Owner   string   `yaml:"owner" doc:"GitHub org or user that owns the target repository." example:"acme-corp"`
	Repo    string   `yaml:"repo" doc:"Repository name new issues are created under." example:"ops-issues, infra"`
	Token   string   `yaml:"token" doc:"GitHub personal access token (supports the secret: prefix)." example:"secret:vigo/github/token  (scope: repo; create at github.com/settings/tokens)"`
	Labels  []string `yaml:"labels" doc:"Labels applied to every created issue (e.g. [\"vigo\", \"drift\"])." example:"[vigo, drift, infrastructure]"`
	Events  []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[module.failed, drift.detected]"`
}

// NinjaRMMIntegration creates tickets in NinjaRMM.
type NinjaRMMIntegration struct {
	Enabled      bool     `yaml:"enabled" doc:"Open NinjaRMM tickets on events." default:"false" example:"true"`
	URL          string   `yaml:"url" doc:"NinjaRMM API base URL (e.g. https://app.ninjarmm.com)." example:"https://app.ninjarmm.com, https://eu.ninjarmm.com"`
	ClientID     string   `yaml:"client_id" doc:"NinjaRMM OAuth client ID." example:"abc123-def456"`
	ClientSecret string   `yaml:"client_secret" doc:"NinjaRMM OAuth client secret (supports the secret: prefix)." example:"secret:vigo/ninjarmm/client_secret"`
	Events       []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[module.failed, envoy.offline]"`
}

// KaseyaIntegration creates tickets in Kaseya VSA.
type KaseyaIntegration struct {
	Enabled  bool     `yaml:"enabled" doc:"Open Kaseya VSA tickets on events." default:"false" example:"true"`
	URL      string   `yaml:"url" doc:"Kaseya VSA API base URL." example:"https://vsa.acme.com/api/v1.0"`
	Username string   `yaml:"username" doc:"Kaseya API user." example:"vigo-api"`
	Password string   `yaml:"password" doc:"Kaseya API password (supports the secret: prefix)." example:"secret:vigo/kaseya/password"`
	Events   []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[module.failed]"`
}

// SumoLogicIntegration forwards events to Sumo Logic HTTP source.
type SumoLogicIntegration struct {
	Enabled bool     `yaml:"enabled" doc:"Forward events to a Sumo Logic HTTP source." default:"false" example:"true"`
	URL     string   `yaml:"url" doc:"Sumo Logic HTTP source endpoint URL." example:"https://endpoint.collection.sumologic.com/receiver/v1/http/XXX"`
	Events  []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[] (forward all for SIEM)"`
}

// SentinelIntegration forwards events to Microsoft Sentinel (Log Analytics).
type SentinelIntegration struct {
	Enabled     bool     `yaml:"enabled" doc:"Forward events to Microsoft Sentinel / Log Analytics." default:"false" example:"true"`
	WorkspaceID string   `yaml:"workspace_id" doc:"Log Analytics workspace GUID." example:"12345678-1234-1234-1234-123456789abc"`
	SharedKey   string   `yaml:"shared_key" doc:"Log Analytics primary or secondary shared key (supports the secret: prefix)." example:"secret:vigo/sentinel/shared_key"`
	LogType     string   `yaml:"log_type" doc:"Custom log table name events are written to." default:"VigoEvents" example:"VigoEvents, VigoDrift"`
	Events      []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[] (forward all)"`
}

// LokiIntegration forwards events to a Grafana Loki push endpoint as labeled log
// streams. Labels stay low-cardinality (service, severity, event_name) per Loki
// best practice — high-cardinality fields (hostname, envoy_id) ride in the log
// line body. Auth is optional: leave Username/Password/BearerToken empty for
// unauthenticated Loki; set BasicAuth (Username + Password) for nginx/htpasswd
// fronted Loki; set BearerToken for Grafana Cloud Logs or token-gated deployments.
type LokiIntegration struct {
	Enabled     bool     `yaml:"enabled" doc:"Forward events to Grafana Loki." default:"false" example:"true"`
	URL         string   `yaml:"url" doc:"Loki base URL (without /loki/api/v1/push — appended automatically)." example:"https://logs.example.com, https://logs-prod-us-central1.grafana.net"`
	TenantID    string   `yaml:"tenant_id" doc:"Loki tenant for multi-tenant deployments (sent as X-Scope-OrgID). Leave empty for single-tenant." example:"acme, 123456"`
	Username    string   `yaml:"username" doc:"Basic-auth username (Grafana Cloud users: your instance ID). Mutually exclusive with bearer_token." example:"123456"`
	Password    string   `yaml:"password" doc:"Basic-auth password (supports the secret: prefix). Mutually exclusive with bearer_token." example:"secret:vigo/loki/password"`
	BearerToken string   `yaml:"bearer_token" doc:"Bearer token (supports the secret: prefix). Mutually exclusive with basic-auth." example:"secret:vigo/loki/token"`
	Events      []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[] (forward all)"`
}

// Device42Integration syncs envoy inventory to Device42.
type Device42Integration struct {
	Enabled  bool     `yaml:"enabled" doc:"Sync envoy inventory to Device42." default:"false" example:"true"`
	URL      string   `yaml:"url" doc:"Device42 base URL (e.g. https://device42.example.com)." example:"https://device42.acme.com"`
	Username string   `yaml:"username" doc:"Device42 user." example:"vigo-api"`
	Password string   `yaml:"password" doc:"Device42 password (supports the secret: prefix)." example:"secret:vigo/device42/password"`
	Events   []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[envoy.enrolled, envoy.decommissioned]"`
}

// SnipeITIntegration syncs envoy inventory to Snipe-IT.
type SnipeITIntegration struct {
	Enabled bool     `yaml:"enabled" doc:"Sync envoy inventory to Snipe-IT asset management." default:"false" example:"true"`
	URL     string   `yaml:"url" doc:"Snipe-IT API base URL (e.g. https://snipeit.example.com/api/v1)." example:"https://assets.acme.com/api/v1"`
	Token   string   `yaml:"token" doc:"Snipe-IT API token (supports the secret: prefix)." example:"secret:vigo/snipeit/token  (JWT from Settings → API)"`
	Events  []string `yaml:"events" doc:"Event types to forward. Empty means all events." example:"[envoy.enrolled, envoy.decommissioned]"`
}

// LicenseSection configures license enforcement behavior.
type LicenseSection struct {
	// Mode controls how nodes are counted for license enforcement.
	// "local" (default): count enrolled nodes on this server only.
	// "aggregate": every spanner bolt sums the fleet-wide node count from
	// the gossip-replicated roster and enforces against it independently —
	// no designated enforcer (peer-equal, post-ADR-026). Spanner mode only;
	// rejected at load outside spanner mode.
	Mode string `yaml:"mode" doc:"Counting mode for license enforcement." options:"local|aggregate" default:"local" example:"aggregate (spanner deployment)"`
}

// BrandingSection configures custom branding for the web UI.
// When set, the UI displays the custom org name and serves logos
// from the specified paths instead of the embedded Vigo defaults.
type BrandingSection struct {
	OrgName     string `yaml:"org_name" doc:"Organization name shown in the UI header and footer." example:"Acme Corp, Example Inc."`
	LogoPath    string `yaml:"logo_path" doc:"Absolute path to a custom sidebar logo image. Unset = embedded Vigo logo." example:"/srv/vigo/branding/logo.svg  (SVG or PNG; ~120×40 works well)"`
	FaviconPath string `yaml:"favicon_path" doc:"Absolute path to a custom favicon. Unset = embedded Vigo favicon." example:"/srv/vigo/branding/favicon.ico"`
	Theme       string `yaml:"theme" doc:"Default UI theme." options:"light|dark|auto" default:"light" example:"auto (follows system), dark (force Mocha)"`
}

// GrafanaSection points the web UI at an external Grafana instance.
// When URL is set, the /health page shows links to the bundled Vigo
// dashboards. Vigo neither runs nor manages Grafana — this is a link
// target only.
type GrafanaSection struct {
	URL string `yaml:"url" doc:"Base URL of a Grafana instance. When set, the /health page links to the bundled Vigo dashboards at <url>/d/<uid>. Unset = no links shown." example:"https://grafana.example.com:3000"`
}

// I18nSection configures web UI localization. Off by default — the UI
// renders English with no overhead and no language picker. When enabled,
// per-request locale is resolved cookie > Accept-Language > default, and
// translations fall back to English (the source language) per missing key.
type I18nSection struct {
	Enabled          bool     `yaml:"enabled" doc:"Enable non-English locales and the topbar language picker. When false the UI is English-only with zero overhead." default:"false" example:"true"`
	DefaultLocale    string   `yaml:"default_locale" doc:"Locale used when no cookie or Accept-Language match applies, and the fallback for any untranslated string." default:"en" example:"en, fr"`
	SupportedLocales []string `yaml:"supported_locales" doc:"Locales offered in the picker and matched against Accept-Language. Empty = every locale with a bundled catalog. Always include default_locale." example:"[en, fr]"`
}

// GRCSection configures GRC platform integrations for pushing compliance
// evidence to external governance, risk, and compliance platforms.
type GRCSection struct {
	Integrations []GRCIntegration `yaml:"integrations" doc:"List of configured GRC platform connections (Vanta, Drata, ServiceNow)." example:"see integrations[].* fields"`
}

// GRCIntegration defines a single GRC platform connection.
type GRCIntegration struct {
	Name         string            `yaml:"name" doc:"GRC platform identifier." options:"vanta|drata|servicenow" example:"vanta, drata"`
	Endpoint     string            `yaml:"endpoint" doc:"API endpoint URL for the GRC platform." example:"https://api.vanta.com/v1, https://public-api.drata.com"`
	APIKey       string            `yaml:"api_key" doc:"API key / token for authentication (supports the secret: prefix)." example:"secret:vigo/grc/vanta_api_key"`
	PushInterval string            `yaml:"push_interval" doc:"How often to push evidence as a Go duration string (e.g. 1h, 6h)." example:"1h, 6h, 24h (accepts s, m, h)"`
	Enabled      bool              `yaml:"enabled" doc:"Whether this GRC connection is active." default:"false" example:"true"`
	Frameworks   map[string]string `yaml:"frameworks" doc:"Map of Vigo framework key to the matching framework ID on the GRC side." example:"{soc2: soc2-type2, iso27001: iso_27001}"`
}

// PathsSection configures directory paths for server subsystems.
// All paths default to subdirectories of the working directory.
type PathsSection struct {
	CustomTraits string `yaml:"custom_traits" doc:"Custom trait collector scripts directory." default:"custom-traits" example:"/srv/vigo/custom-traits"`
	Docs         string `yaml:"docs" doc:"Documentation directory served by the in-app docs browser." default:"docs" example:"docs, /srv/vigo/docs"`
	Tasks        string `yaml:"tasks" doc:"Reusable task definitions directory." default:".live/tasks" example:"/srv/vigo/stacks/.live/tasks"`
	Workflows    string `yaml:"workflows" doc:"Workflow definitions directory." default:".live/workflows" example:"/srv/vigo/stacks/.live/workflows"`
	AgentDist    string `yaml:"agent_dist" doc:"Agent binary distribution directory served to bootstrappers." default:"dist/agent" example:"/srv/vigo/dist/agent"`
	LicenseDir   string `yaml:"license_dir" doc:"License file directory." default:"/srv/vigo/license" example:"/srv/vigo/license"`
	Snapshots    string `yaml:"snapshots" doc:"Backup snapshot directory." default:"/srv/vigo/snapshots" example:"/srv/vigo/snapshots  (ideally on a separate volume)"`
}

// AuthSection configures web UI and REST API authentication.
// Default method is "basic" — username/password with bcrypt hashes in the
// local secrets store at vigo/web/auth/<username>.
type AuthSection struct {
	Method             string               `yaml:"method" doc:"Authentication method." options:"basic|oidc|isowebauth" default:"basic" example:"oidc (SSO), basic (small teams)"`
	SessionIdleTimeout string               `yaml:"session_idle_timeout" doc:"Session idle timeout as a Go duration string (e.g. 15m, 1h)." default:"15m" example:"15m, 1h, 8h, 24h (accepts s, m, h)"`
	OIDC               OIDCAuthConfig       `yaml:"oidc" doc:"OIDC provider settings (used when method is 'oidc')."`
	Isowebauth         IsowebauthAuthConfig `yaml:"isowebauth" doc:"Isowebauth settings (used when method is 'isowebauth')."`
}

// OIDCAuthConfig holds OIDC provider settings.
type OIDCAuthConfig struct {
	Issuer       string   `yaml:"issuer" doc:"OIDC issuer URL (discovery document is fetched from /.well-known/openid-configuration)." example:"https://login.microsoftonline.com/{tenant}/v2.0, https://accounts.google.com"`
	ClientID     string   `yaml:"client_id" doc:"OIDC client identifier." example:"abc123.apps.googleusercontent.com"`
	ClientSecret string   `yaml:"client_secret" doc:"OIDC client secret (supports the secret: prefix)." example:"secret:vigo/oidc/client_secret"`
	RedirectURL  string   `yaml:"redirect_url" doc:"Redirect URL registered with the provider (must point at this server's /auth/callback)." example:"https://vigo.example.com/auth/callback"`
	Scopes       []string `yaml:"scopes" doc:"Scopes requested during the auth code flow." default:"[openid,profile,email]" example:"[openid, profile, email, groups]"`

	BootstrapAdminEmail   string `yaml:"bootstrap_admin_email" doc:"Email of the OIDC identity to provision as admin on first login (empty = no auto-admin; grant roles via 'vigocli webusers set-role'). Matched only when the IdP marks the email verified (email_verified: true); prefer bootstrap_admin_subject on IdPs allowing unverified email." example:"admin@example.com"`
	BootstrapAdminSubject string `yaml:"bootstrap_admin_subject" doc:"OIDC subject (sub claim) of the identity to provision as admin on first login. Use instead of email when the IdP's email is not stable." example:"auth0|abc123"`
}

// IsowebauthAuthConfig holds isowebauth settings.
type IsowebauthAuthConfig struct {
	Namespace string `yaml:"namespace" doc:"Isowebauth namespace the Vigo server claims." default:"vigo" example:"vigo, vigo-prod, vigo-us-west"`
}

// ExportSection configures compliance framework export endpoints.
// All types are disabled by default and must be individually enabled.
type ExportSection struct {
	SIEM  bool `yaml:"siem" doc:"Expose the SIEM export endpoint." default:"false" example:"true  (serves at /api/v1/export/siem)"`
	CMDB  bool `yaml:"cmdb" doc:"Expose the CMDB export endpoint." default:"false" example:"true  (serves at /api/v1/export/cmdb)"`
	OSCAL bool `yaml:"oscal" doc:"Expose the OSCAL export endpoint." default:"false" example:"true  (serves at /api/v1/export/oscal)"`
}

// PublishSection configures blast radius limits for config publish.
// When compliance_threshold is set (1-100), the server monitors fleet compliance
// after each config reload. If compliance drops below the threshold within
// the rollback window, the previous config is automatically restored.
type PublishSection struct {
	ConvergenceThreshold int               `yaml:"compliance_threshold" doc:"Minimum fleet compliance % required after publish. 0 disables the gate." default:"0" example:"0-100; 90 is a common prod value"`
	RollbackWindow       string            `yaml:"rollback_window" doc:"Window during which a failed publish is automatically rolled back." default:"5m" example:"5m, 10m, 30m (accepts s, m, h)"`
	Retraction           RetractionSection `yaml:"retraction" doc:"Automatic drafting of .retract modules when modules are removed at publish."`
}

// RetractionSection configures automatic retract module generation when
// modules are removed during config publish. Requires both this and AI to
// be enabled. When a module is removed, a .retract draft is generated
// in the stacks for operator review.
type RetractionSection struct {
	Enabled bool `yaml:"enabled" doc:"Draft .retract modules when modules are removed at publish. Requires AI to be enabled." default:"false" example:"true (requires ai.enabled: true)"`
}

// PeersSection configures peer replication for server redundancy.
type PeersSection struct {
	Role    string      `yaml:"role" doc:"Server role in the peer-replication group." options:"primary|peer" default:"primary" example:"primary (write leader), peer (read replica)"`
	Servers []PeerEntry `yaml:"servers" doc:"Peer server addresses and push intervals." example:"see servers[].* fields"`
}

// PeerEntry defines a single peer server.
type PeerEntry struct {
	Addr         string `yaml:"addr" doc:"Peer gRPC address (host:port)." example:"vigo-replica-1.example.com:1530"`
	SyncInterval string `yaml:"sync_interval" doc:"Push interval to this peer as a Go duration string." default:"10s" example:"5s, 10s, 30s (accepts s, m)"`
	Pubkey       string `yaml:"pubkey" doc:"Peer's service-account (puddle) Ed25519 public key, from its startup 'puddle service-account identity ready' log line. Required to authorize HA peer RPCs (SyncStream/Promote/Status); peer replication fails closed without it. Local secrets backend only." example:"ed25519:1a2b3c…"`
}

// TaskSection configures task dispatch behavior.
type TaskSection struct {
	RequireDefinition bool `yaml:"require_definition" doc:"Only allow named task definitions to be dispatched (reject ad-hoc commands)." default:"false" example:"true (prod), false (dev)"`
}

// BackupSection configures continuous SQLite WAL replication via Litestream.
// Disabled when URL is empty. Only applies when database.driver is "sqlite".
type BackupSection struct {
	URL             string `yaml:"url" doc:"Replica URL (s3://bucket/path, gcs://bucket/path, file:///path, sftp://host/path). Empty disables backup." example:"s3://vigo-backups/prod, gcs://acme-vigo/backups, sftp://backup.internal/vigo"`
	AccessKeyID     string `yaml:"access_key_id" doc:"S3/GCS access key (supports the secret: prefix)." example:"secret:vigo/s3/access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key" doc:"S3/GCS secret key (supports the secret: prefix)." example:"secret:vigo/s3/secret_access_key"`
	Region          string `yaml:"region" doc:"AWS region (optional — falls back to the provider default)." example:"us-west-2, eu-central-1"`
	Endpoint        string `yaml:"endpoint" doc:"S3-compatible endpoint for MinIO, Backblaze, etc." example:"https://s3.us-east-005.backblazeb2.com, https://minio.internal:9000"`
	ForcePathStyle  bool   `yaml:"force_path_style" doc:"Use path-style S3 URLs (required for MinIO)." default:"false" example:"true (MinIO, some Backblaze setups)"`
	Retention       string `yaml:"retention" doc:"Snapshot retention window as a Go duration string." default:"720h" example:"168h (1w), 720h (30d), 8760h (1y) — no d unit; use hours"`
	SyncInterval    string `yaml:"sync_interval" doc:"WAL sync frequency as a Go duration string." default:"1s" example:"1s, 10s, 1m (accepts s, m)"`
}

// AISection is the "ai:" block in server.yaml. Parsed here so it participates
// in secret resolution (api_key can use "secret:" prefix). Validation and
// defaults live in server/ai/config.go.
type AISection struct {
	Enabled       bool   `yaml:"enabled" doc:"Enable the AI assistant." default:"false" example:"true"`
	Provider      string `yaml:"provider" doc:"AI backend." options:"claude|openai|ollama" example:"claude (recommended), ollama (air-gapped)"`
	Model         string `yaml:"model" doc:"Model identifier passed to the provider (e.g. claude-opus-4-7, gpt-4o, llama3.1:70b)." example:"claude-opus-4-7, claude-sonnet-4-6, gpt-4o, llama3.1:70b"`
	MaxTokens     int    `yaml:"max_tokens" doc:"Maximum output tokens per response." default:"4096" example:"2048 (tight), 4096 (default), 8192 (long-form)"`
	BaseURL       string `yaml:"base_url" doc:"Provider base URL override (used for Ollama or Claude-compatible proxies)." example:"http://ollama.internal:11434, https://api.anthropic.com"`
	APIKey        string `yaml:"api_key" doc:"Provider API key (supports the secret: prefix)." example:"secret:vigo/ai/api_key  (raw: sk-ant-api03-...)"`
	RateLimit     int    `yaml:"rate_limit" doc:"Requests-per-minute cap on AI calls. 0 disables the cap." example:"60 (1/sec), 600 (10/sec), 0 (unlimited)"`
	MaxToolRounds int    `yaml:"max_tool_rounds" doc:"Maximum number of tool-use turns per conversation." default:"10" example:"5 (conservative), 10 (default), 20 (complex tasks)"`
	ContextMode   string `yaml:"context_mode" doc:"How much fleet context the assistant gets per call." options:"full|summary|off" example:"summary (recommended), full (small fleets), off"`
	DocsMode      string `yaml:"docs_mode" doc:"How much of the Vigo docs corpus the assistant is fed." options:"full|condensed|off" default:"full" example:"full (best answers), condensed (smaller context)"`
}

// SMTPSection configures email notifications via an SMTP relay.
// Notifications are disabled when Host is empty.
type SMTPSection struct {
	Host       string                     `yaml:"host" doc:"SMTP relay hostname. Empty disables email notifications." example:"smtp.gmail.com, smtp.sendgrid.net, mail.internal"`
	Port       int                        `yaml:"port" doc:"SMTP server port." default:"587" example:"587 (STARTTLS), 465 (implicit TLS), 25 (plaintext)"`
	From       string                     `yaml:"from" doc:"Sender address (e.g. vigo@example.com)." example:"vigo@acme.com, alerts@acme.com"`
	Username   string                     `yaml:"username" doc:"SMTP auth username (optional)." example:"vigo@acme.com, apikey (SendGrid)"`
	Password   string                     `yaml:"password" doc:"SMTP auth password (supports the secret: prefix)." example:"secret:vigo/smtp/password"`
	TLS        bool                       `yaml:"tls" doc:"Use STARTTLS to upgrade the SMTP connection." default:"true" example:"false only on trusted internal relays"`
	Recipients []string                   `yaml:"recipients" doc:"Default recipient list for event notifications." example:"[ops@acme.com, oncall@acme.com]"`
	Events     map[string]SMTPEventConfig `yaml:"events" doc:"Per-event toggles and recipient overrides. Nil means every event is delivered." example:"{envoy.offline: {recipients: [sre@acme.com]}}"`
	Digest     SMTPDigestConfig           `yaml:"digest" doc:"Batching of notifications into periodic summary emails."`
	Evidence   EvidenceEmailConfig        `yaml:"evidence" doc:"Scheduled compliance-evidence email delivery."`
}

// EvidenceEmailConfig controls automatic evidence email delivery.
type EvidenceEmailConfig struct {
	Schedule   string   `yaml:"schedule" doc:"Cadence for automated evidence emails. Empty disables." options:"weekly|monthly" example:"weekly, monthly"`
	Recipients []string `yaml:"recipients" doc:"Override recipients for evidence emails. Empty uses smtp.recipients." example:"[compliance@acme.com, auditor@acme.com]"`
	Frameworks []string `yaml:"frameworks" doc:"Which compliance frameworks to include. Empty means all." example:"[soc2, hipaa]"`
}

// SMTPEventConfig holds per-event notification settings.
// An empty value enables the event with default recipients.
type SMTPEventConfig struct {
	Recipients []string `yaml:"recipients,omitempty" doc:"Override recipients for this event. Empty uses smtp.recipients." example:"[sre@acme.com, oncall@acme.com]"`
	Threshold  int      `yaml:"threshold,omitempty" doc:"Fire the convergence.threshold event when fleet convergence drops below this %." example:"95 (fire when <95%), 80 (looser)"`
}

// SMTPDigestConfig controls batching notifications into periodic summaries.
type SMTPDigestConfig struct {
	Interval string   `yaml:"interval" doc:"Digest window as a Go duration string (e.g. 1h, 30m). Empty disables digesting." example:"15m, 1h, 6h (accepts s, m, h)"`
	Events   []string `yaml:"events" doc:"Events routed to the digest instead of being delivered immediately." example:"[module.failed, drift.detected]  (keeps envoy.offline real-time)"`
}

// TuningSection holds operational tuning parameters. All fields are optional
// and default to sensible values when omitted from server.yaml.
type TuningSection struct {
	SignatureWindow      string        `yaml:"signature_window" doc:"Clock-skew tolerance for request signatures." default:"5m" example:"5m (default), 15m (loose NTP)"`
	LastSeenFlush        string        `yaml:"last_seen_flush" doc:"How often the last-seen timestamp is flushed to the database; also drives the batched trait insert+prune cadence." default:"10s" example:"5s, 10s, 30s (accepts s, m)"`
	MetricsInterval      string        `yaml:"metrics_interval" doc:"Prometheus gauge refresh interval." default:"30s" example:"15s, 30s, 1m (match scrape_interval)"`
	TraitsScannerTTL     string        `yaml:"traits_scanner_ttl" doc:"How long agent-side trait scanner caches remain valid." default:"30s" example:"30s (default), 5m (lighten disk)"`
	RunStore             string        `yaml:"run_store" doc:"Where convergence runs are persisted." options:"database|memory" default:"database" example:"database (prod), memory (ephemeral test)"`
	RunStoreCapacity     int           `yaml:"run_store_capacity" doc:"Per-envoy run history depth when run_store is 'memory'." default:"20" example:"20 (default), 100 (debug retention)"`
	MaxConcurrentStreams uint32        `yaml:"max_concurrent_streams" doc:"Maximum concurrent gRPC bidirectional streams." default:"5000" example:"500 (small), 5000 (default), 25000 (10k-node fleet)"`
	Webhook              WebhookTuning `yaml:"webhook" doc:"Dispatch tuning for outbound webhooks."`

	// Go GC tuning. The GOMEMLIMIT soft memory limit has no key here — it is
	// auto-derived to ~80% of the cgroup-aware budget, or set via the standard
	// GOMEMLIMIT env var (deployment/runtime tuning, alongside the container's own
	// memory limit). A retired tuning.memory_limit key hard-errors at load.
	GOGC *int `yaml:"gogc" doc:"Go GC target percentage (equivalent to the GOGC env var). Unset uses the Go default." example:"50 (more GC, less RAM), 100 (default), 200 (less GC, more RAM)"`

	// gRPC transport tuning.
	GRPCReadBuffer    int    `yaml:"grpc_read_buffer" doc:"Per-connection gRPC read buffer bytes. 0 uses the gRPC default (32 KiB)." example:"32768 (32 KiB), 65536 (64 KiB)"`
	GRPCWriteBuffer   int    `yaml:"grpc_write_buffer" doc:"Per-connection gRPC write buffer bytes. 0 uses the gRPC default (32 KiB)." example:"32768 (32 KiB), 65536 (64 KiB)"`
	MaxConnectionAge  string `yaml:"max_connection_age" doc:"Force reconnect after this duration. Empty means no age limit." example:"1h, 6h, 24h (forces LB rebalancing)"`
	MaxConnectionIdle string `yaml:"max_connection_idle" doc:"Close idle connections after this duration." default:"15m" example:"5m, 15m, 1h (accepts s, m, h)"`
	KeepaliveTime     string `yaml:"keepalive_time" doc:"Interval between server→client keepalive pings." default:"30s" example:"15s, 30s, 1m (accepts s, m)"`
	KeepaliveTimeout  string `yaml:"keepalive_timeout" doc:"Deadline for a keepalive PING ACK before the conn is closed with GOAWAY (next RPC returns Unavailable). gRPC also pushes this to TCP_USER_TIMEOUT at the kernel. Tighter values detect dead clients faster; looser values tolerate transient Go scheduler queueing delays at high conn counts. See docs/howto/sizing.md for the trade-off." default:"30s" example:"20s (aggressive), 30s (default), 60s (large fleets)"`
	StreamSendBuffer  int    `yaml:"stream_send_buffer" doc:"Per-agent send-channel capacity on the bidirectional stream." default:"8" example:"8 (default), 32 (noisy fleets), 128 (debug)"`
}

// WebhookTuning holds webhook dispatch tuning parameters.
type WebhookTuning struct {
	Timeout       string `yaml:"timeout" doc:"Per-webhook HTTP timeout as a Go duration string." default:"10s" example:"5s, 10s, 30s (accepts s, m)"`
	MaxRetries    int    `yaml:"max_retries" doc:"Retry attempts before giving up on a webhook." default:"3" example:"3 (default), 5 (flaky endpoints), 0 (no retry)"`
	MaxConcurrent int    `yaml:"max_concurrent" doc:"Global cap on concurrent in-flight webhook dispatches." default:"20" example:"20 (default), 100 (large fleet)"`
	RetryDelay    string `yaml:"retry_delay" doc:"Base delay between retry attempts as a Go duration string." default:"5s" example:"1s, 5s, 30s (exponential from this base)"`
}

// RateLimitSection configures rate limiting for agent RPCs.
type RateLimitSection struct {
	Enabled         bool `yaml:"enabled" doc:"Enforce rate limits on agent RPCs." default:"false" example:"true (shared/public-facing servers)"`
	CheckinPerEnvoy int  `yaml:"checkin_per_envoy" doc:"Per-envoy check-in rate cap in requests per minute. Sized above the 1s default cadence (60/min) so enabling rate limiting doesn't throttle the default." default:"120" example:"120 (default, ~1s cadence + headroom), 240 (aggressive), 12 (slow cadences)"`
	CheckinGlobal   int  `yaml:"checkin_global" doc:"Global check-in rate cap in requests per second." default:"500" example:"500 (default), 5000 (large fleet)"`
}

// MaintenanceSection configures global change freeze.
type MaintenanceSection struct {
	FreezeUntil string `yaml:"freeze_until" doc:"RFC3339 timestamp — config publish is blocked until this moment passes." example:"2026-12-24T00:00:00Z  (end of holiday freeze)"`
}

// WatcherSection holds secret rotation watcher configuration.
type WatcherSection struct {
	Enabled      bool   `yaml:"enabled" doc:"Poll the secrets provider for rotations and force-push affected envoys." default:"false" example:"true (prod with scheduled rotations)"`
	PollInterval string `yaml:"poll_interval" doc:"How often to poll the provider as a Go duration string." example:"30s, 5m, 1h (accepts s, m, h)"`
}

// ServerSection holds listener and TLS config.
type ServerSection struct {
	GRPCListen string    `yaml:"grpc_listen" doc:"gRPC listener address (host:port)." default:":1530" example:":1530 (all interfaces), 0.0.0.0:1530, 10.0.0.5:1530"`
	APIListen  string    `yaml:"api_listen" doc:"REST/UI listener address (host:port)." default:":8443" example:":8443, 0.0.0.0:8443, 10.0.0.5:8443"`
	Hostname   string    `yaml:"hostname" doc:"The server's own envoy hostname (used for compliance display and implicit swarm seeding)." example:"vigo.example.com  (must match the server's envoy enrollment)"`
	TLS        TLSConfig `yaml:"tls" doc:"TLS certificate, private key, and CA material paths."`
	TLSSANs    []string  `yaml:"tls_sans" doc:"Extra Subject Alternative Names (IPs or DNS names) baked into auto-generated server certs." example:"[10.0.0.5, vigo.internal, vigo-lb.acme.com]"`
}

// TLSConfig holds TLS file paths.
type TLSConfig struct {
	CertFile  string `yaml:"cert_file" doc:"Path to the server TLS certificate (PEM)." example:"/etc/vigo/certs/tls.crt"`
	KeyFile   string `yaml:"key_file" doc:"Path to the server TLS private key (PEM)." example:"/etc/vigo/certs/tls.key  (chmod 0600)"`
	CAFile    string `yaml:"ca_file" doc:"Path to the CA certificate used to verify agent client certs (PEM)." example:"/etc/vigo/certs/ca.crt"`
	CAKeyFile string `yaml:"ca_key_file" doc:"Path to the CA private key (used to sign agent CSRs during bootstrap)." example:"/etc/vigo/certs/ca.key  (chmod 0600)"`
}

// TrustedEnrollment defines a hostname/CIDR pattern for token-free enrollment.
// Envoys matching the pattern and originating from an allowed CIDR are enrolled
// without a one-time token (trusted autosigning).
type TrustedEnrollment struct {
	Pattern string   `yaml:"pattern" doc:"Hostname glob pattern (filepath.Match) that enrolls without a one-time token." example:"*.prod.example.com, build-*.ci.internal"`
	CIDRs   []string `yaml:"cidrs,omitempty" doc:"Optional CIDRs the enrolling agent must originate from." example:"[10.0.0.0/8, 192.168.1.0/24]"`
}

// BootstrapConfig holds bootstrap enrollment settings.
type BootstrapConfig struct {
	TrustedPatterns []TrustedEnrollment `yaml:"trusted_enrollment" doc:"Hostname/CIDR patterns that skip the one-time-token requirement (trusted autosigning)." example:"see trusted_enrollment[].* fields"`
	CertValidity    string              `yaml:"cert_validity" doc:"Validity period for agent certificates issued at bootstrap (e.g. 8760h for one year)." example:"720h (30d), 8760h (1y), 26280h (3y) — no d unit; use hours"`
}

// DatabaseSection holds database config.
type DatabaseSection struct {
	DSN          string `yaml:"dsn" doc:"Database DSN — sqlite3 file path or postgres connection URL (supports the secret: prefix)." example:"file:/srv/vigo/vigo.db, postgres://user@host:5432/vigo?sslmode=require, secret:vigo/db/dsn"`
	Driver       string `yaml:"driver" doc:"Database driver." options:"sqlite|postgres" default:"sqlite" example:"sqlite (single-node), postgres (HA)"`
	MaxOpenConns int    `yaml:"max_open_conns" doc:"Maximum concurrent database connections. SQLite: sizes the concurrent read pool — writes always serialize on a dedicated single-writer connection, so this caps reader concurrency and bounds CGO worker threads (an unbounded pool is what lets a write storm OS-thread-exhaust the server). Postgres: caps the single pool below the server's max_connections. 0 = auto (SQLite: CPU count, min 4; Postgres: 25)." default:"0" example:"0 (auto), 8, 16, 25"`
	Retention    string `yaml:"retention" doc:"Age-based pruning for runs, tasks, and workflows (cascades to run_results via ON DELETE CASCADE). Empty disables pruning — leaving it unset lets run history grow without bound." example:"30d, 90d, 2160h (the d-suffix shorthand and Go duration strings both parse)"`
}

// StreamSignatureMode selects how aggressively the held AgentStream verifies
// per-message ed25519 signatures. The handshake check-in is always verified
// (it binds the stream to the envoy's key); this knob only governs the
// re-verification of subsequent messages on an already-bound stream.
type StreamSignatureMode string

const (
	// StreamSigAll is the default: every per-message check-in/report on a held
	// stream is ed25519-verified, exactly as a unary request would be.
	StreamSigAll StreamSignatureMode = "all"
	// StreamSigHandshakeOnly relies on the verified handshake + mTLS session
	// integrity for the stream owner's own subsequent messages, skipping their
	// (redundant) re-verification. Messages carrying a *different* envoy_id
	// (proxied device results) are still fully verified. A deliberate security
	// relaxation — only safe when agents connect directly over mTLS with no
	// TLS-terminating intermediary.
	StreamSigHandshakeOnly StreamSignatureMode = "handshake-only"
)

// CheckInSection holds check-in interval settings.
type CheckInSection struct {
	Interval            string              `yaml:"interval" doc:"Nominal agent check-in interval as a Go duration string. The server pushes this to agents, so it sets the effective fleet poll cadence." default:"1s" example:"1s (default), 15s (lighter), 5m (conservative)"`
	JitterPercent       int                 `yaml:"jitter_percent" doc:"Random ± jitter applied to the check-in interval (percent)." example:"0-50; 10 (default) spreads load across a ±10% window"`
	BundleMaxAge        string              `yaml:"bundle_max_age" doc:"Signed-bundle TTL on agents (e.g. 24h). \"0\" means bundles never expire." default:"24h" example:"1h, 24h, 168h (accepts s, m, h); 0 to disable"`
	OfflineThreshold    string              `yaml:"offline_threshold" doc:"Override the staleness threshold used by the Offline pill, risk scorer, and stale-envoy notifier. Empty preserves the default 2.5× check-in interval." example:"empty (use 2.5× interval), 5m, 15m (accepts s, m, h)"`
	FleetLookupTTL      string              `yaml:"fleet_lookup_ttl" doc:"How long a resolved fleet: cross-node config lookup is reused before recomputing from the live fleet. Higher = cheaper, staler." default:"30s" example:"30s (default), 1m, 10s"`
	ObserveOnly         bool                `yaml:"observe_only" doc:"Fleet-wide observe-only mode — all envoys report drift but never apply changes." default:"false" example:"true during Salt/Puppet migration"`
	ComplianceThreshold int                 `yaml:"compliance_threshold" doc:"Convergence percentage below which the convergence.threshold event fires. 0 disables." example:"0-100; 90 for prod alerting, 95 for tight SLOs"`
	MaxConcurrent       int                 `yaml:"max_concurrent" doc:"Maximum concurrent CheckIn handlers. The cap gates the policy-bundle build (CPU-heavy) inside the handler, so a fleet-wide stale-checkin wave (post-publish, mass re-enroll) sheds via ResourceExhausted → agent backoff instead of CPU-cascading. Always on — 0 (default) = a CPU-scaled built-in (max(128, GOMAXPROCS*16); GOMAXPROCS tracks the container's cgroup CPU limit), not disabled. Higher admits more before shedding (deeper concurrency, more CPU thrash); lower sheds sooner (tighter latency for admits, more backoff during waves). Watch vigo_checkins_total{status=\"shed\"}." default:"0" example:"0 (=CPU-scaled default), 256, 1024"`
	ReportMaxConcurrent int                 `yaml:"report_max_concurrent" doc:"Maximum concurrent ReportResult handlers before excess is shed with ResourceExhausted (the agent backs off). A queue-depth bound in front of the single SQLite writer: higher admits more before shedding (deeper queue, higher tail latency under sustained overload), lower sheds sooner (tighter tail, more backoff). Unlike checkin.max_concurrent this is always on — 0 (default) means the built-in 2048, not disabled." default:"0" example:"0 (=2048 default), 512, 4096"`
	MaxConnections      int                 `yaml:"max_connections" doc:"Maximum concurrently-held agent streams. Each connected envoy holds one persistent AgentStream costing ~220 KB RSS, so connection count — not request rate — is what drives the server toward OOM. Past this cap, new streams are refused with ResourceExhausted (the agent backs off and retries) so the server sheds gracefully instead of being OOM-killed. 0 (default) = auto-derive a safe ceiling from host RAM (≈ 40% of RAM ÷ 220 KB — the sizing-guide table; sheds, not OOMs, out of the box); a positive value sets an explicit cap; -1 disables the cap entirely (legacy uncapped — accepts streams until the kernel OOM-kills). Watch vigo_streams_active against the effective value." default:"0" example:"0 (auto from RAM), 60000 (explicit, ≈31 GiB host), -1 (uncapped)"`
	StreamSignature     StreamSignatureMode `yaml:"stream_signature" doc:"Per-message ed25519 verification on held agent streams. \"all\" (default) verifies every check-in/report, exactly as a unary request. \"handshake-only\" verifies the handshake plus any proxied (foreign-envoy) message, but skips re-verifying the stream owner's own subsequent check-ins, relying on the verified handshake + mTLS session integrity. A deliberate SECURITY RELAXATION that trades a defense-in-depth layer for ~40% less CPU at very large fleets on aggressive cadence — only safe when agents connect directly over mTLS (no TLS-terminating proxy/LB in front of the gRPC port). Prefer raising checkin.interval or sharding via spanner before reaching for this." options:"all|handshake-only" default:"all" example:"all (default), handshake-only"`
}

// Validate checks the check-in config is internally consistent.
func (c CheckInSection) Validate() error {
	switch c.StreamSignature {
	case "", StreamSigAll, StreamSigHandshakeOnly:
		return nil
	default:
		return fmt.Errorf("checkin.stream_signature must be \"all\" or \"handshake-only\" (got %q)", c.StreamSignature)
	}
}

// ConditionalBlock groups resources under a shared when expression.
// When flattened, block-level when is AND-composed with each child resource's when.
type ConditionalBlock struct {
	When      string           `yaml:"when,omitempty"`
	Resources []ResourceConfig `yaml:"resources"`
}

// EnvoyConfig represents the resolved per-envoy configuration.
// This is the output type returned by ConfigStore.Get().
type EnvoyConfig struct {
	Envoy           string         `yaml:"envoy"`
	MatchPattern    string         `yaml:"-"` // original match pattern from envoys.vgo
	Environment     string         `yaml:"environment"`
	Roles           []string       `yaml:"-"` // role names that were expanded
	Configcrates    []Configcrate  `yaml:"configcrates"`
	Vars            map[string]any `yaml:"vars"`
	VarOrder        []string       `yaml:"-"`
	Tags            []string       `yaml:"-"` // static tags from the match block, propagated to FleetIndex on check-in
	HasLookups      bool           `yaml:"-"` // true if any resource attribute or top-level var is a tag/host/trait lookup table
	HasFleetLookups bool           `yaml:"-"` // true if any top-level var is a fleet: lookup (cross-node, resolved at check-in)
	ObserveOnly     bool           `yaml:"-"` // observe-only mode: report drift only, don't enforce
	MerkleRoot      string         `yaml:"-"` // SHA256 Merkle root over modules + vars (computed at load)

	// Network device fields — populated for virtual envoys managed via gateway proxy.
	Gateway    string `yaml:"-"` // hostname of managing gateway envoy
	DeviceType string `yaml:"-"` // cisco_ios, cisco_nxos, juniper_junos
	Credential string `yaml:"-"` // resolved secret value for SSH auth
	Username   string `yaml:"-"` // SSH username
	Port       int    `yaml:"-"` // SSH port
	IPAddress  string `yaml:"-"` // management IP
}

// Clone returns a deep-enough copy of the EnvoyConfig. Safe to mutate
// modules and vars without affecting the original.
func (c *EnvoyConfig) Clone() *EnvoyConfig {
	clone := *c
	clone.Configcrates = make([]Configcrate, len(c.Configcrates))
	for i, mod := range c.Configcrates {
		clone.Configcrates[i] = mod
		clone.Configcrates[i].Resources = cloneResources(mod.Resources)
		if mod.DependsOn != nil {
			clone.Configcrates[i].DependsOn = append([]string(nil), mod.DependsOn...)
		}
	}
	clone.Vars = make(map[string]any, len(c.Vars))
	for k, v := range c.Vars {
		clone.Vars[k] = v
	}
	clone.VarOrder = append([]string(nil), c.VarOrder...)
	clone.Tags = append([]string(nil), c.Tags...)
	return &clone
}

// Configcrate describes a module assigned to a envoy.
type Configcrate struct {
	Name        string              `yaml:"name"`
	DependsOn   []string            `yaml:"depends_on"`
	When        string              `yaml:"when,omitempty"`
	Resources   []ResourceConfig    `yaml:"resources"`
	Compliance  map[string][]string `yaml:"-"` // framework → control IDs, including ancestor compliance.vgo claims
	ContentHash string              `yaml:"-"` // pre-computed at config load for delta transfer
	BaseName    string              `yaml:"-"` // for a foreach: instance "vhost[www]", the source module name "vhost"; "" otherwise
}

// ResourceConfig describes a single resource within a module.
type ResourceConfig struct {
	Name        string                    `yaml:"name"`
	Type        string                    `yaml:"type"`
	When        string                    `yaml:"when,omitempty"`
	Attributes  map[string]any            `yaml:",inline"`
	Notify      []string                  `yaml:"notify"`
	Subscribes  []string                  `yaml:"subscribes"`
	WatchSecret []string                  `yaml:"watch_secret"`
	DependsOn   []string                  `yaml:"depends_on"`
	Before      []string                  `yaml:"before"`
	ForEach     string                    `yaml:"-"` // var name to iterate (config-load-time expansion)
	Case        string                    `yaml:"-"` // template expression for case switching (check-in-time)
	CaseMatch   map[string]map[string]any `yaml:"-"` // case branch → attribute overrides
}

// ConfigcrateKind distinguishes a regular module definition from a usercrate.
// Both share one namespace and one map (configcrateDefs); Kind is how the
// resolver enforces that `modules:` only takes Module-kind defs and
// `usercrates:` only takes Usercrate-kind defs.
type ConfigcrateKind string

const (
	ConfigcrateKindConfigcrate ConfigcrateKind = ""          // zero value — regular module from stacks/modules/
	ConfigcrateKindUsercrate   ConfigcrateKind = "usercrate" // loaded from a stacks/**/usercrates/ dir
)

// ComplianceClaims is a module's per-module compliance attribution block.
// Two parallel catalog-backed lists:
//
//   - Provides — functional capabilities the module delivers across
//     frameworks (catalog: server/compliance/provisions.go).
//   - Bundle — framework-scope-cuts the module implements (catalog:
//     server/compliance/bundles.go).
//
// Both are tag lists; the loader expands each tag via the catalog into
// the framework → control IDs it satisfies. Adding a framework touches
// one catalog file instead of every module.
type ComplianceClaims struct {
	Provides []string `yaml:"provides"`
	Bundle   []string `yaml:"bundle"`
}

// ConfigcrateDefinition is a reusable module loaded from config/modules/*.yaml.
// It has a custom UnmarshalYAML that flattens conditional_block entries
// in the resources list into plain ResourceConfig items.
type ConfigcrateDefinition struct {
	Name       string           `yaml:"name"`
	Kind       ConfigcrateKind  `yaml:"-"` // set by loader: regular module vs usercrate
	SourceFile string           `yaml:"-"` // stacks-relative path, set by loader
	DependsOn  []string         `yaml:"depends_on"`
	Before     []string         `yaml:"before"`
	Defaults   map[string]any   `yaml:"defaults"`
	Resources  []ResourceConfig `yaml:"resources"`
	Vars       map[string]any   `yaml:"vars"`
	// Compliance is the per-module compliance attribution block (0.66.57+).
	// Two sub-keys, both routed through Vigo-curated catalogs:
	//
	//   compliance:
	//     provides: [time-sync, login-banner]     # functional capabilities
	//     bundle:   [cis-ubuntu-access]           # framework scope-cuts
	//
	// Re-parented under `compliance:` to restore the at-a-glance signal
	// stripped when 0.66.48 promoted `provides:` / `bundle:` to top
	// level — module YAML now declares its compliance intent under one
	// recognisable key, but the cross-walks still live in the catalogs
	// (server/compliance/provisions.go + bundles.go), not inline.
	//
	// modlint enforces the shape:
	//   - rule_configcrate_compliance_key_retired errors when `compliance:`
	//     carries the pre-0.66.48 framework→control list shape.
	//   - rule_configcrate_top_level_provides_bundle errors when the
	//     0.66.48-style top-level `provides:` / `bundle:` keys appear
	//     on a module.
	Compliance ComplianceClaims `yaml:"compliance"`

	// InheritedCompliance is the framework → control IDs map populated
	// by the loader from directory-level `compliance.vgo` inheritance
	// files (`loadComplianceConfigs` + `applyConfigcrateComplianceInheritance`).
	// Never written by operator YAML — the inheritance file is a
	// different file kind with its own decoder. Internal to the loader;
	// buildComplianceIndex reads it alongside Compliance.Provides /
	// Compliance.Bundle to build the per-envoy coverage trace.
	InheritedCompliance map[string][]string `yaml:"-"`

	// TemplateSources are stacks-relative paths the module references
	// via `source:` on any of its resources, captured by resolveSourceFiles
	// before the source attribute is rewritten into content. Used by the
	// reachability analysis to know which template files are live.
	TemplateSources []string `yaml:"-"`
}

// UnmarshalYAML implements yaml.Unmarshaler for ConfigcrateDefinition.
// It handles the "resources" key specially by passing its child nodes
// through FlattenResourceNodes to support conditional_block entries.
// All other keys are decoded via the default YAML decoder.
func (m *ConfigcrateDefinition) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("ConfigcrateDefinition: expected mapping, got %v", value.Kind)
	}

	// Walk key-value pairs in the mapping.
	for i := 0; i < len(value.Content)-1; i += 2 {
		key := value.Content[i].Value
		val := value.Content[i+1]

		switch key {
		case "name":
			if err := val.Decode(&m.Name); err != nil {
				return fmt.Errorf("decoding name: %w", err)
			}
		case "depends_on":
			if err := val.Decode(&m.DependsOn); err != nil {
				return fmt.Errorf("decoding depends_on: %w", err)
			}
		case "before":
			if err := val.Decode(&m.Before); err != nil {
				return fmt.Errorf("decoding before: %w", err)
			}
		case "defaults":
			if err := val.Decode(&m.Defaults); err != nil {
				return fmt.Errorf("decoding defaults: %w", err)
			}
		case "vars":
			if err := val.Decode(&m.Vars); err != nil {
				return fmt.Errorf("decoding vars: %w", err)
			}
		case "resources":
			if val.Kind != yaml.SequenceNode {
				return fmt.Errorf("resources: expected sequence, got %v", val.Kind)
			}
			resources, err := FlattenResourceNodes(val.Content)
			if err != nil {
				return fmt.Errorf("flattening resources: %w", err)
			}
			m.Resources = resources
		case "compliance":
			// 0.66.57+ shape: { provides: [...], bundle: [...] }.
			// ComplianceClaims silently ignores any other sub-key,
			// so pre-0.66.48 framework→control lists decode as
			// empty here; modlint (rule_configcrate_compliance_key_retired)
			// is the gate that errors on the old shape.
			if err := val.Decode(&m.Compliance); err != nil {
				return fmt.Errorf("decoding compliance: %w", err)
			}
		case "provides", "bundle":
			// Defense-in-depth against the 0.66.48-through-0.66.56
			// authoring shape. modlint catches this at publish-time
			// via rule_configcrate_top_level_provides_bundle, but the
			// loader also has direct-decode paths (test fixtures,
			// hand-rolled callers of parseConfigcrateYAML) that don't
			// pass through modlint — Go's YAML decoder would silently
			// drop the unknown keys without this arm.
			return errRetiredTopLevelComplianceKey(key)
		}
	}

	applyDefaults(m.Resources, m.Defaults)
	return nil
}

// errRetiredTopLevelComplianceKey is the single source of truth for the
// error returned when a module YAML declares `provides:` or `bundle:`
// at the top level — the authoring shape introduced in 0.66.48 and
// retired in 0.66.57 in favor of a single `compliance:` parent block.
// Both YAML decode paths (ConfigcrateDefinition.UnmarshalYAML in this file,
// parseConfigcrateYAML in parse.go) call this so the wording can't drift.
// Message mirrors modlint/rule_configcrate_top_level_provides_bundle.go so
// operators see the same diagnostic from either gate.
func errRetiredTopLevelComplianceKey(key string) error {
	return fmt.Errorf(
		"module YAML: top-level `%s:` is retired (0.66.57) — fold under a `compliance:` "+
			"parent block (e.g. `compliance: { provides: [<tag>], bundle: [<id>] }`). "+
			"Restores the at-a-glance compliance signal in module YAML without changing the catalog-backed semantics",
		key,
	)
}

// ConfigcrateRef is a reference to a module in a role or envoy entry. It supports
// two YAML forms:
//
//	modules: [nginx]               # plain string -> ConfigcrateRef{Name: "nginx"}
//	modules:
//	  - name: nginx                # object form with optional when expression
//	    when: "is_debian"
//	  - name: vhost                # foreach: instantiate the module once per
//	    foreach: vhosts            # item in the var list, with each item's
//	    key: server_name           # fields exposed as {{ .Each.<field> }}.
//	                               # key: names the item field that supplies the
//	                               # instance suffix → modules vhost[<server_name>].
//	                               # (scalar lists: omit key:, the value is the key)
type ConfigcrateRef struct {
	Name    string `yaml:"name"`
	When    string `yaml:"when,omitempty"`
	ForEach string `yaml:"foreach,omitempty"` // var name (a list) — instantiate once per item
	Key     string `yaml:"key,omitempty"`     // item field providing the instance suffix (map lists)

	// State retracts the whole configcrate when "absent": every resource it
	// manages resolves to its retracted form (stateful types → absent/stopped,
	// command types → revert:). "" (the default) and "present" enforce it.
	// Precedence across carriers is most-specific-wins (see Tier).
	State string `yaml:"state,omitempty"`

	// Tier is the carrier that produced this ref, set by tagCarrierTiers at
	// load. State precedence compares Tier (most-specific wins) rather than
	// list position, so it survives any reordering of the resolution pipeline.
	// Not serialized; a ref reaching resolution with TierUnset is a bug and is
	// rejected loudly.
	Tier Tier `yaml:"-"`

	// Set by expandForeachConfigcrateRefs when a foreach: ref is expanded into
	// per-item instances. BaseName is the original module name (for configcrateDefs
	// lookup); EachItem is this instance's list item; InstanceKey is the
	// resolved suffix. Empty/nil for an ordinary ref.
	BaseName    string `yaml:"-"`
	EachItem    any    `yaml:"-"`
	InstanceKey string `yaml:"-"`
}

// Configcrate retraction states for ConfigcrateRef.State.
const (
	ConfigcrateStatePresent = "present"
	ConfigcrateStateAbsent  = "absent"
)

// Tier ranks the carriers that can declare a configcrate ref's State, from
// least to most specific. State precedence is most-specific-wins: a finer
// carrier's explicit state overrides a coarser one (e.g. a match-block
// `present` un-retracts a `common.vgo` `absent`). Resolution compares Tier, not
// list order, so the precedence cannot silently invert if the pipeline is
// reordered. The zero value TierUnset is invalid — every ref is tagged at load
// by tagCarrierTiers, and an untagged ref reaching resolution fails loudly.
type Tier int

const (
	TierUnset  Tier = iota // zero value — untagged ref (a tagging bug; caught at resolve)
	TierRole               // role-provided (least specific)
	TierCommon             // common.vgo directory inheritance
	TierMatch              // hostcrate match block
	TierEnv                // environments.vgo per-env override (most specific)
)

// UnmarshalYAML implements yaml.Unmarshaler for ConfigcrateRef. It handles two forms:
//   - Scalar string: "nginx" -> ConfigcrateRef{Name: "nginx"}
//   - Mapping: {name: nginx, when: "is_debian"} -> decoded normally
func (m *ConfigcrateRef) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		m.Name = value.Value
		return nil
	case yaml.MappingNode:
		// Use an alias to avoid infinite recursion in UnmarshalYAML.
		type configcrateRefAlias ConfigcrateRef
		var alias configcrateRefAlias
		if err := value.Decode(&alias); err != nil {
			return err
		}
		*m = ConfigcrateRef(alias)
		return nil
	default:
		return fmt.Errorf("ConfigcrateRef: expected string or mapping, got %v", value.Kind)
	}
}

// RoleRef is a reference to a role, optionally conditional on a when expression.
// Supports two YAML forms: scalar string "webserver" or mapping {name: webserver, when: "..."}.
type RoleRef struct {
	Name string `yaml:"name"`
	When string `yaml:"when,omitempty"`
}

// UnmarshalYAML implements yaml.Unmarshaler for RoleRef. It handles two forms:
//   - Scalar string: "webserver" -> RoleRef{Name: "webserver"}
//   - Mapping: {name: webserver, when: "distro('ubuntu')"} -> decoded normally
func (r *RoleRef) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		r.Name = value.Value
		return nil
	case yaml.MappingNode:
		type roleRefAlias RoleRef
		var alias roleRefAlias
		if err := value.Decode(&alias); err != nil {
			return err
		}
		*r = RoleRef(alias)
		return nil
	default:
		return fmt.Errorf("RoleRef: expected string or mapping, got %v", value.Kind)
	}
}

// RoleDefinition is a named list of modules loaded from config/roles/*.yaml.
// Roles may include other roles (single level only — included roles cannot
// themselves include roles). An optional When expression applies to all
// modules in the role that don't have their own when: condition.
// Case provides conditional module sets — each case entry has a when: and
// its own modules list. All matching cases contribute modules (not short-circuit).
type RoleDefinition struct {
	Name         string           `yaml:"name"`
	When         string           `yaml:"when,omitempty"`
	Includes     []string         `yaml:"includes"`       // other role names to include (single level)
	Configcrates []ConfigcrateRef `yaml:"configcrates"`   // unconditional modules
	Usercrates   []ConfigcrateRef `yaml:"usercrates"`     // positive include of usercrates (Kind == Usercrate); cross-kind enforced at resolve time
	Case         []RoleCase       `yaml:"case,omitempty"` // conditional module groups
}

// RoleCase is a conditional group of modules within a role definition.
// All cases whose when: expression matches contribute their modules.
type RoleCase struct {
	When         string           `yaml:"when"`
	Configcrates []ConfigcrateRef `yaml:"configcrates"`
}

// ValidDeviceTypes lists the supported network device types for gateway proxy management.
var ValidDeviceTypes = map[string]bool{
	"cisco_ios":     true,
	"cisco_nxos":    true,
	"juniper_junos": true,
	"arista_eos":    true,
	"cisco_iosxr":   true,
}

// EnvoyEntry is a single entry in envoys.vgo. It has a custom UnmarshalYAML
// that merges duplicate 'modules' and 'roles' keys (standard YAML would
// silently keep only the last value).
type EnvoyEntry struct {
	Match               string           `yaml:"match"`
	Environment         string           `yaml:"environment"`
	Roles               []RoleRef        `yaml:"roles"`
	Configcrates        []ConfigcrateRef `yaml:"configcrates"`
	Usercrates          []ConfigcrateRef `yaml:"usercrates"` // positive include of usercrates (Kind == Usercrate)
	Vars                map[string]any   `yaml:"vars"`
	Tags                []string         `yaml:"tags"`                 // static tags for targeting
	ExcludeConfigcrates []string         `yaml:"exclude_configcrates"` // opt out of inherited modules
	ExcludeResources    []string         `yaml:"exclude_resources"`    // opt out of specific resources by name
	ObserveOnly         bool             `yaml:"observe_only"`         // observe-only mode: report drift only, don't enforce

	// DirPath is the relative directory path within the config tree where
	// this entry was loaded from. Set by the loader for inheritance resolution.
	DirPath string `yaml:"-"`

	// SourceFile is the filename (without directory) of the config file
	// that defined this entry. Set by the loader for trace display.
	SourceFile string `yaml:"-"`

	// OwnConfigcrates/OwnRoles/OwnUsercrates/OwnVars hold the entry's original
	// values before inheritance merging. Set by applyInheritance for trace.
	OwnConfigcrates []ConfigcrateRef `yaml:"-"`
	OwnRoles        []RoleRef        `yaml:"-"`
	OwnUsercrates   []ConfigcrateRef `yaml:"-"`
	OwnVars         map[string]any   `yaml:"-"`

	// Network device fields — set when this entry is a virtual envoy managed
	// through a gateway. Gateway must be a real compute envoy.
	Gateway    string `yaml:"gateway"`     // hostname of managing gateway envoy
	DeviceType string `yaml:"device_type"` // cisco_ios, cisco_nxos, juniper_junos
	Credential string `yaml:"credential"`  // secret: path for SSH auth
	Username   string `yaml:"username"`    // SSH username (default: admin)
	Port       int    `yaml:"port"`        // SSH port (default: 22)
	IPAddress  string `yaml:"ip_address"`  // management IP (if different from DNS)
}

// WaiverEntry represents a single compliance control waiver.
type WaiverEntry struct {
	Control    string `yaml:"control"`
	Reason     string `yaml:"reason"`
	ApprovedBy string `yaml:"approved_by"`
	Expires    string `yaml:"expires,omitempty"` // YYYY-MM-DD or empty for no expiration
}

// ComplianceConfig holds compliance claims and/or waivers loaded from a
// compliance.vgo file. One file format, two behaviors by location:
//
//   - Files in the envoys directory tree (anywhere under stacks/ except
//     modules/, templates/, examples/) contribute to the waiver index.
//     The Compliance field is ignored in that location.
//   - Files in the modules/ subtree contribute to each ancestor module's
//     effective compliance claims. The Waivers field is ignored in that
//     location.
//
// modlint emits a warning when the "wrong" key appears for a file's tree.
type ComplianceConfig struct {
	Compliance map[string][]string      `yaml:"compliance"` // framework → list of control IDs
	Waivers    map[string][]WaiverEntry `yaml:"waivers"`    // framework → list of waivers
	DirPath    string                   `yaml:"-"`
}

// CommonConfig defines modules, roles, and vars inherited by all envoy
// entries in subdirectories. Loaded from common.vgo files at each directory
// level. Files without match: entries are treated as common configs.
type CommonConfig struct {
	Configcrates []ConfigcrateRef `yaml:"configcrates"`
	Usercrates   []ConfigcrateRef `yaml:"usercrates"` // positive include of usercrates for every entry at this dir and below
	Roles        []RoleRef        `yaml:"roles"`
	Vars         map[string]any   `yaml:"vars"`
	DirPath      string           `yaml:"-"` // relative dir path (e.g., "production/web")
}

// PerEnvOverrideBlock holds modules, vars, and exclusions for a single
// environment within a environments.vgo file.
type PerEnvOverrideBlock struct {
	Configcrates        []ConfigcrateRef `yaml:"configcrates"`
	Usercrates          []ConfigcrateRef `yaml:"usercrates"` // positive include of usercrates in this environment
	ExcludeConfigcrates []string         `yaml:"exclude_configcrates"`
	Vars                map[string]any   `yaml:"vars"`
}

// PerEnvOverridesConfig holds the parsed content of a environments.vgo file.
type PerEnvOverridesConfig struct {
	Env     map[string]*PerEnvOverrideBlock `yaml:"env"`
	DirPath string                          `yaml:"-"`
}

// UnmarshalYAML implements yaml.Unmarshaler for EnvoyEntry. It walks the
// mapping manually so duplicate 'modules' and 'roles' keys are merged
// instead of the last one winning.
func (n *EnvoyEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("EnvoyEntry: expected mapping, got %v", value.Kind)
	}
	for i := 0; i < len(value.Content)-1; i += 2 {
		key := value.Content[i].Value
		val := value.Content[i+1]
		switch key {
		case "match":
			if err := val.Decode(&n.Match); err != nil {
				return fmt.Errorf("decoding match: %w", err)
			}
		case "environment":
			if err := val.Decode(&n.Environment); err != nil {
				return fmt.Errorf("decoding environment: %w", err)
			}
		case "roles":
			var extra []RoleRef
			if err := val.Decode(&extra); err != nil {
				return fmt.Errorf("decoding roles: %w", err)
			}
			n.Roles = append(n.Roles, extra...)
		case "configcrates":
			var extra []ConfigcrateRef
			if err := val.Decode(&extra); err != nil {
				return fmt.Errorf("decoding configcrates: %w", err)
			}
			n.Configcrates = append(n.Configcrates, extra...)
		case "modules":
			return fmt.Errorf("`modules:` was renamed to `configcrates:` — update the match block (see docs/reference/configcrate-language.md)")
		case "usercrates":
			var extra []ConfigcrateRef
			if err := val.Decode(&extra); err != nil {
				return fmt.Errorf("decoding usercrates: %w", err)
			}
			n.Usercrates = append(n.Usercrates, extra...)
		case "vars":
			if err := val.Decode(&n.Vars); err != nil {
				return fmt.Errorf("decoding vars: %w", err)
			}
		case "gateway":
			if err := val.Decode(&n.Gateway); err != nil {
				return fmt.Errorf("decoding gateway: %w", err)
			}
		case "device_type":
			if err := val.Decode(&n.DeviceType); err != nil {
				return fmt.Errorf("decoding device_type: %w", err)
			}
		case "credential":
			if err := val.Decode(&n.Credential); err != nil {
				return fmt.Errorf("decoding credential: %w", err)
			}
		case "username":
			if err := val.Decode(&n.Username); err != nil {
				return fmt.Errorf("decoding username: %w", err)
			}
		case "port":
			if err := val.Decode(&n.Port); err != nil {
				return fmt.Errorf("decoding port: %w", err)
			}
		case "ip_address":
			if err := val.Decode(&n.IPAddress); err != nil {
				return fmt.Errorf("decoding ip_address: %w", err)
			}
		case "exclude_configcrates":
			if err := val.Decode(&n.ExcludeConfigcrates); err != nil {
				return fmt.Errorf("decoding exclude_configcrates: %w", err)
			}
		case "exclude_modules":
			return fmt.Errorf("`exclude_modules:` was renamed to `exclude_configcrates:` — update the match block (see docs/reference/configcrate-language.md)")
		case "exclude_usercrates":
			return fmt.Errorf("`exclude_usercrates:` was removed in 0.54.0 — usercrates are now inert until positively included via `usercrates:` (see docs/reference/configcrate-language.md)")
		case "exclude_resources":
			if err := val.Decode(&n.ExcludeResources); err != nil {
				return fmt.Errorf("decoding exclude_resources: %w", err)
			}
		case "tags":
			if err := val.Decode(&n.Tags); err != nil {
				return fmt.Errorf("decoding tags: %w", err)
			}
		}
	}
	return nil
}

// EnvoysConfig is the top-level structure of envoys.vgo.
type EnvoysConfig struct {
	Envoys []EnvoyEntry `yaml:"envoys"`
}

// StandardCoverage describes an envoy's coverage of a compliance standard.
type StandardCoverage struct {
	Standard        string            `json:"standard"`
	TotalControls   int               `json:"total_controls"`
	CoveredControls []ControlCoverage `json:"covered_controls"`
	MissingControls []string          `json:"missing_controls"`
	CoveragePercent float64           `json:"coverage_percent"`
}

// ControlCoverage describes an envoy's coverage of a single control.
type ControlCoverage struct {
	ControlID    string   `json:"control_id"`
	Configcrates []string `json:"configcrates"`
}
