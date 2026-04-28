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

// SpannerRole defines the server's role in a spanner deployment.
type SpannerRole string

const (
	// RoleStandalone is the default: a single server handling everything.
	RoleStandalone SpannerRole = "standalone"
	// RoleHub is the central server in a spanner deployment.
	// Handles UI, webhooks, bootstrap, and aggregates data from spokes.
	RoleHub SpannerRole = "hub"
	// RoleSpoke is a partition server handling agent check-ins for a
	// subset of envoys. Disables webhooks, bootstrap, and secret watcher.
	RoleSpoke SpannerRole = "spoke"
)

// SpannerSection configures hub-spoke spanner. Omit entirely
// (or set role: standalone) for single-server deployments.
type SpannerSection struct {
	Role             SpannerRole        `yaml:"role" doc:"Server role in a spanner deployment." options:"standalone|hub|spoke" default:"standalone" example:"standalone"`
	SpokeID          string             `yaml:"spoke_id" doc:"Unique spoke identifier (required when role is 'spoke')." example:"us-west-spoke, eu-spoke-01"`
	HubAddr          string             `yaml:"hub_addr" doc:"Hub gRPC address (host:port) the spoke reports to." example:"vigo-hub.example.com:1530"`
	HubFallbackAddrs []string           `yaml:"hub_fallback_addrs" doc:"Spoke-only: fallback hub addresses used when the primary hub is unreachable." example:"[vigo-hub-2:1530, vigo-hub-3:1530]"`
	Spokes           []SpokeEntry       `yaml:"spokes" doc:"Hub-only: list of spoke definitions (id, address, hostname patterns)." example:"see spokes.<id> fields"`
	AutoFailover     AutoFailoverConfig `yaml:"auto_failover" doc:"Hub-only: automatic draining of spokes that fail health probes."`
}

// AutoFailoverConfig controls automatic spoke failover on the hub.
// When enabled, the hub periodically probes spoke health and drains
// spokes that fail consecutively.
type AutoFailoverConfig struct {
	Enabled   bool   `yaml:"enabled" doc:"Probe spokes and drain unhealthy ones automatically." default:"false" example:"true in multi-region setups"`
	Threshold int    `yaml:"threshold" doc:"Consecutive failed probes before the spoke is drained." default:"3" example:"3 (prod), 5 (flaky networks)"`
	Interval  string `yaml:"interval" doc:"Probe interval as a Go duration string (e.g. 30s, 1m)." default:"30s" example:"15s, 30s, 1m, 5m (accepts s, m, h)"`
}

// SpokeEntry defines a spoke server the hub aggregates from.
type SpokeEntry struct {
	ID       string   `yaml:"id" doc:"Unique spoke identifier (referenced by CLI and aggregate dashboards)." example:"us-west, eu-central, customer-acme"`
	Addr     string   `yaml:"addr" doc:"Spoke gRPC address (host:port)." example:"spoke-01.example.com:1530"`
	Patterns []string `yaml:"patterns" doc:"Hostname glob patterns routed to this spoke (first-match-wins)." example:"[*.us-west-1.internal, *.dc1.*]"`
}

// Validate checks that spanner config is internally consistent.
func (f SpannerSection) Validate() error {
	role := f.Role
	if role == "" || role == RoleStandalone {
		return nil
	}
	if role == RoleHub {
		if len(f.Spokes) == 0 {
			return fmt.Errorf("spanner.spokes is required when role is 'hub'")
		}
		for i, s := range f.Spokes {
			if s.ID == "" {
				return fmt.Errorf("spanner.spokes[%d].id is required", i)
			}
			if s.Addr == "" {
				return fmt.Errorf("spanner.spokes[%d].addr is required", i)
			}
		}
		return nil
	}
	if role == RoleSpoke {
		if f.SpokeID == "" {
			return fmt.Errorf("spanner.spoke_id is required when role is 'spoke'")
		}
		return nil
	}
	return fmt.Errorf("spanner.role must be standalone, hub, or spoke (got %q)", role)
}

// ServerConfig represents the top-level server configuration from server.yaml.
type ServerConfig struct {
	Server       ServerSection       `yaml:"server" docs:"getting-started/server-config" doc:"Listener addresses, TLS paths, and the server's own envoy hostname." long:"Core listener configuration — the gRPC port agents connect to, the HTTPS port for the web UI and REST API, paths to the TLS cert/key/CA chain, and the server's own envoy hostname used for trusted local operations. Edit these when moving the server to a different address, rotating TLS material, or changing the ports used by the fleet. A restart is required after any change."`
	Database     DatabaseSection     `yaml:"database" docs:"operations/database" doc:"SQLite or Postgres connection and retention settings for the run store." long:"Connection and retention settings for the run store, audit log, and run history. SQLite in WAL mode is the supported default for single-server deployments; Postgres is available for larger fleets. Retention knobs control how long convergence runs and audit events are kept before being pruned to hold disk growth in check."`
	Auth         AuthSection         `yaml:"auth" docs:"operations/authentication" doc:"Web UI and REST API authentication (basic, OIDC, or isowebauth)." long:"Who is allowed to use the web UI and REST API, and how they authenticate. Pluggable backends: basic (bcrypt username/password, default for small teams), oidc (SSO via any OpenID Connect provider like Okta or Google Workspace), and isowebauth (SSH key signing via the desktop companion app). Local CLI traffic from the Docker bridge is always trusted as admin regardless of this setting."`
	CheckIn      CheckInSection      `yaml:"checkin" docs:"concepts/check-in-lifecycle" doc:"Check-in interval, jitter, bundle TTL, and fleet-wide observe-only toggle." long:"How often each agent pulls from the server, how much random jitter to add so large fleets don't stampede at the same second, and how long cached bundles remain valid. Also hosts the fleet-wide observe-only switch that forces every envoy into dry-run mode — the cheapest way to pause all enforcement without pulling modules. Tune the interval for the trade-off between drift-detection latency and server load."`
	Watcher      WatcherSection      `yaml:"watcher" docs:"concepts/secrets" doc:"Polls the secrets provider for rotations and force-pushes affected envoys." long:"Background task that polls the configured secrets provider on a schedule and, when a secret value changes, forces a push to every envoy that consumes it. Enables automatic secret rotation without waiting for the next regular check-in — critical when rotating database passwords, TLS keys, or API tokens with revocation expectations. Poll frequency trades detection speed for provider API load."`
	Secrets      secrets.Config      `yaml:"secrets" docs:"concepts/secrets" doc:"Secrets provider backend and connection settings (local, isopass, vault, AWS, GCP, Azure)." long:"Selects which backend resolves 'secret:' references in module configs at agent check-in. Options: local (files under /srv/vigo/secrets, default for single-server), isopass (Alexander4 Isopass), vault (HashiCorp Vault), aws (AWS Secrets Manager), gcp (GCP Secret Manager), azure (Azure Key Vault). The server fails fast on startup if the backend is unreachable — it never serves a module with plaintext fallbacks."`
	Bootstrap    BootstrapConfig     `yaml:"bootstrap" docs:"getting-started/bootstrap" doc:"Agent bootstrap enrollment — trusted hostname/CIDR patterns and issued cert validity." long:"Controls how new agents are enrolled into the fleet via the initial HTTPS bootstrap call. Define trusted hostname patterns and CIDR blocks that short-circuit the one-time-token flow (useful for automated provisioning), plus the validity window on auto-issued agent certs. Tighten these in production — a permissive trusted_enrollment is the difference between friction-free onboarding and an open door."`
	Spanner      SpannerSection      `yaml:"spanner" docs:"concepts/spanner" doc:"Hub-spoke deployment — standalone, hub, or spoke role with auto-failover." long:"Deployment topology — standalone (one server runs the whole fleet), hub (MSP operator runs a rollup across many customer spokes), or spoke (customer-facing server reporting up to a hub). Set this when running an MSP deployment or a multi-region setup where one central server can't own all check-ins. Includes auto-failover tuning for when a hub's primary loses contact with its spokes."`
	Tuning       TuningSection       `yaml:"tuning" docs:"operations/tuning" doc:"Operational tuning — timing windows, gRPC transport limits, and Go runtime memory caps." long:"Low-level knobs for operators who have hit a scaling ceiling and need to hand-tune. Includes gRPC send/receive limits, concurrent stream caps, timing windows for online-vs-offline envoy classification, and Go runtime memory ceilings (GOMEMLIMIT). Leave at defaults until you see a specific symptom — most fleets never touch this section."`
	SMTP         SMTPSection         `yaml:"smtp" docs:"operations/integrations" doc:"Email relay for event notifications, digests, and scheduled evidence delivery." long:"SMTP relay used by event notifications, daily/weekly digests, and scheduled compliance evidence delivery. Most deployments should prefer the native integrations (Slack, PagerDuty, Jira) over SMTP — email is the least observable and least actionable channel. Keep this configured only when a recipient genuinely can't be reached any other way."`
	AI           AISection           `yaml:"ai" docs:"operations/ai-providers" doc:"AI assistant provider (Claude, OpenAI, Ollama) for the Ask page and compliance tooling." long:"Wires the /security chat drawer and compliance-assist tooling to an LLM provider. Supports Claude (sharpest reasoning on Vigo-specific questions), OpenAI (cost/token trade-off), and Ollama (fully self-hosted, no third-party calls). Required for automatic .retract module drafting and for the AI-powered guardrail suggestions in the publish flow."`
	Backup       BackupSection       `yaml:"backup" docs:"operations/backup" doc:"Continuous Litestream WAL replication of the SQLite database to S3/GCS/filesystem/SFTP." long:"Continuous SQLite WAL replication via Litestream, shipping every committed transaction to an off-server destination (S3, GCS, a mounted filesystem, or SFTP). This is the primary disaster-recovery mechanism — combined with the stockpile directory held in version control, a full server rebuild is a fresh install plus a restore-from-backup. Configure this for every deployment holding more than a test fleet."`
	Export       ExportSection       `yaml:"export" docs:"compliance/compliance-export" doc:"Compliance export endpoints (SIEM, CMDB, OSCAL) — each toggled independently." long:"One-way outbound feeds for compliance and audit consumers: SIEM ingestion of audit events, CMDB sync of envoy inventory, and OSCAL-format framework reports for auditors. Each channel is toggled independently — enable only the consumers the organization actually uses. Configure this when the organization needs evidence flowing into an existing GRC or IT ops stack rather than being pulled via the REST API on demand."`
	Publish      PublishSection      `yaml:"publish" docs:"operations/config-publish" doc:"Blast-radius limits on config publish — convergence threshold, approval gate, retraction drafting." long:"Safety rails around 'vigocli config publish' to stop a typo from fanning out to the whole fleet in 15 seconds. Includes a minimum-converged threshold (refuse to publish if too many envoys are already failing), an optional two-person approval gate, and automatic .retract draft generation when modules are removed. Tune these higher in production fleets and lower in dev — the default is permissive because a lab with three envoys doesn't need a change-control board."`
	RateLimit    RateLimitSection    `yaml:"rate_limit" docs:"operations/rate-limiting" doc:"Per-envoy and global rate limits on check-in RPCs." long:"Ceiling on check-in RPC rates, per-envoy and globally, to protect the server from a runaway agent or a stampeding herd. Requests over the limit are rejected with a retryable error so affected agents back off on their own. Tune this up only after confirming the server's bottleneck is genuinely check-in throughput — for most fleets the default limits sit well above normal traffic."`
	Maintenance  MaintenanceSection  `yaml:"maintenance" docs:"operations/maintenance-windows" doc:"Global change freeze — publish is blocked until the configured deadline passes." long:"Global change-freeze switch that blocks 'vigocli config publish' until a specified deadline. Use this during code freezes, customer maintenance windows, or incident response when you need to guarantee no config changes can reach the fleet. Emergency bypass requires editing the file and reloading — not a runtime override — so the freeze is real."`
	Paths        PathsSection        `yaml:"paths" docs:"getting-started/server-config" doc:"Filesystem paths for migrations, custom traits, docs, tasks, workflows, and agent binaries." long:"Override the default filesystem locations for migrations, custom trait collectors, shipped docs, task definitions, workflow definitions, and the agent-binary distribution directory. Change these only for non-standard installs (packaged distributions, per-host chroots) — the defaults under /srv/vigo and /var/lib/vigo cover every normal deployment."`
	Peers        PeersSection        `yaml:"peers" docs:"operations/peer-replication" doc:"Peer replication — primary or peer role plus the address list for server redundancy." long:"Active-passive replication that keeps one or more standby vigosrv instances file-synced with the primary, so a server outage doesn't strand the fleet. The primary streams config, secrets, and TLS files to each peer over gRPC; agents receive the peer list at enrollment and automatically fall back to one if the primary becomes unreachable. Configure this whenever the fleet is too large to tolerate an unplanned outage — promotion is then a single 'vigocli server promote' command on a surviving peer."`
	Task         TaskSection         `yaml:"task" docs:"reference/cli/task" doc:"Task dispatch — optionally require a named definition for every ad-hoc task." long:"Controls ad-hoc task dispatch safety. When require_definition is on, every task sent via 'vigocli task dispatch' must reference a pre-reviewed task definition in /srv/vigo/stockpile/tasks — direct shell commands are refused. Enable this in production to force peer review of anything an operator can fan out to a fleet; leave it off in dev where quick one-off commands are the whole point."`
	Branding     BrandingSection     `yaml:"branding" docs:"operations/branding" doc:"Custom org name, logo, favicon, and light/dark/auto UI theme." long:"White-label the web UI with the organization's name (shown in the title bar and in exported reports), a custom logo and favicon, and a forced light/dark theme or auto. The logo is inlined into cover blocks of generated PDFs so emailed attestations carry the customer's brand. Useful for MSPs per-customer and for enterprises who want their audit reports to wear their own logo."`
	License      LicenseSection      `yaml:"license" docs:"operations/licensing" doc:"License enforcement mode — local count or aggregated across spanner spokes." long:"How node-count enforcement aggregates across your topology — 'local' counts envoys registered against this single vigosrv, 'aggregated' sums counts across every spoke in a spanner deployment and enforces a single fleet-wide cap. Pick aggregated on MSP hubs or enterprise spanner setups where one license covers the full customer base. Misconfiguring this either blocks legitimate enrollments (too strict) or silently lets the fleet drift past the contract (too loose)."`
	GRC          GRCSection          `yaml:"grc" docs:"operations/grc" doc:"GRC platform integrations (Vanta, Drata, ServiceNow) for pushing compliance evidence." long:"Outbound push of compliance evidence into third-party GRC platforms (Vanta, Drata, ServiceNow GRC, Hyperproof, and similar). Each GRC target has its own endpoint, token, and control-mapping block — the server translates Vigo's internal compliance state into whatever format the target expects. Configure this when the organization has already standardized on a GRC system and you want Vigo's evidence to land there without manual uploads."`
	Integrations IntegrationsSection `yaml:"integrations" docs:"operations/integrations" doc:"Native external platform integrations — Slack, PagerDuty, Jira, ITSM, SIEM, and asset systems." long:"First-class integrations with operator-facing platforms — chat (Slack, Teams), paging (PagerDuty, Opsgenie), ticketing (Jira, Zendesk, Linear, ServiceNow, Freshservice), ITSM (ConnectWise, Autotask, NinjaRMM, Kaseya), SIEM/logging (Splunk, Elastic, Datadog, Sumo, Sentinel), and asset management (Device42, Snipe-IT). Each platform is its own subsection with its own toggle and credentials; prefer these over the legacy webhook path for anything important — native integrations carry context the webhook format can't."`
	Risk         RiskSection         `yaml:"risk" docs:"concepts/risk-scoring" doc:"Risk quantification — optional NVD CVE API enrichment." long:"Controls the per-envoy risk scoring engine. An optional NVD CVE API key enriches risk input data with CVSS metrics and vendor-attributed advisories — without it, CVE severity comes from the scanner output alone. Risk scores drive the dashboard Risk Posture column, the cyber-insurance attestation PDF, and compliance-visible drift signals, so enable the NVD key if the deployment is subject to insurance or audit scrutiny."`
	StreamEdit   StreamEditSection   `yaml:"stream_edit" docs:"concepts/stream-edit" doc:"Guardrails for file resources that pipe content through agent-local scripts." long:"Guardrails on the file resource's 'stream_edit:' attribute, which pipes content through agent-local scripts before it's written to disk. Controls whether stream-edits are allowed at all, the allowed interpreters (bash, python, etc.), time/memory limits per transform, and whether results can be cached across convergence cycles. Turn stream-edits off in hardened deployments; tune the limits when debugging long-running transforms."`
	Compliance   ComplianceSection   `yaml:"compliance" docs:"concepts/compliance" doc:"Which compliance frameworks are active in this deployment (empty = all)." long:"Scopes which compliance frameworks this deployment reports against. An empty list activates every framework Vigo knows — HIPAA, SOC 2, PCI DSS, ISO 27001, NIST 800-53, CIS, GDPR, and the rest. An explicit list restricts dashboards, reports, and waiver tooling to that subset — useful when the organization is only audited against one or two frameworks and seeing the full catalog adds noise."`
	Scrier       ScrierSection       `yaml:"scrier" docs:"concepts/scrier" doc:"Scrier browser-based remote access — SSH and RDP via guacd tunnel." long:"Browser-based remote access to any enrolled envoy — interactive SSH, Windows RDP, or console-shadow VNC, all routed through the envoy's outbound agent stream so no inbound ports are required on the target side. Every session is logged in scrier_sessions and an audit event is emitted on disconnect. Configure this when administrators need ad-hoc access to envoys behind customer firewalls without punching holes in them."`
	Swarm        SwarmSection        `yaml:"swarm" docs:"concepts/swarm" doc:"Peer-to-peer content distribution — seed directory, port, bandwidth cap." long:"Peer-to-peer content distribution between envoys over mTLS on port 1531 (agent-to-agent, not server-to-agent). Configures the seed directory the server writes into and the bandwidth cap as a percentage of detected link speed (so swarm transfers don't starve real workloads). Essential for fleets large enough that pulling every blob from the server would saturate its uplink."`
}

// SwarmSection configures peer-to-peer content distribution. When enabled,
// the server stores content-addressed blobs and coordinates distribution
// to agents via the swarm protocol on port 1531.
type SwarmSection struct {
	Enabled             bool                   `yaml:"enabled" doc:"Enable peer-to-peer content distribution." default:"false" example:"true"`
	SeedDir             string                 `yaml:"seed_dir" doc:"Directory where server-distributed seeds are written (bind-mounted to agents)." default:"/var/lib/vigo/swarm/seed" example:"/var/lib/vigo/swarm/seed"`
	Port                int                    `yaml:"port" doc:"TCP port on which agents serve blobs to each other over mTLS." default:"1531" example:"1-65535; must be open between envoys"`
	MaxBandwidthPercent int                    `yaml:"max_bandwidth_percent" doc:"Maximum percent of detected link speed agents will use for swarm transfers." default:"50" example:"1-100; 25 for shared links, 80 for dedicated"`
	Gitmirror           SwarmGitmirrorSection  `yaml:"gitmirror" doc:"Gitmirror subsystem (personal-DR repo backup) operability gates." long:"Gitmirror runs by default on every enrolled envoy. Use blacklist to deny specific envoys."`
}

// SwarmGitmirrorSection configures per-subsystem gates for gitmirror.
// Gitmirror is default-on across the fleet — every enrolled envoy
// receives bundles by default. The blacklist denies specific hostnames
// (filepath.Match globs), and a blacklist add triggers automatic
// fleet-wide eviction of the matching envoys' held mirrors.
type SwarmGitmirrorSection struct {
	Blacklist []string `yaml:"blacklist" doc:"Hostname patterns (filepath.Match globs) of envoys denied from receiving gitmirror bundles. Adding a host evicts its existing mirrors fleet-wide; removing a host re-allows it but does not re-replicate previous bundles." example:"[raspberrypi, *.test.example.com]"`
}

// ScrierSection configures the Scrier remote access feature.
type ScrierSection struct {
	Enabled          bool     `yaml:"enabled" doc:"Enable browser-based remote sessions (SSH, RDP, VNC)." default:"false" example:"true"`
	GuacdAddr        string   `yaml:"guacd_addr" doc:"Address of the guacd container (host:port)." default:"guacd:4822" example:"guacd:4822, 10.0.0.5:4822"`
	ProxyHostname    string   `yaml:"proxy_hostname" doc:"Hostname guacd uses to reach back to this server (Docker service name)." default:"vigo" example:"vigo (compose service name)"`
	AllowedPorts     []uint32 `yaml:"allowed_ports" doc:"TCP ports that may be tunneled to managed envoys." default:"[22,3389,5900]" example:"[22, 3389, 5900]  # ssh, rdp, vnc"`
	MaxSessions      int      `yaml:"max_sessions" doc:"Maximum concurrent Scrier sessions on this server." default:"10" example:"5-20 typical; bound by guacd capacity"`
	RecordingEnabled bool     `yaml:"recording_enabled" doc:"Persist every session as a Guacamole .recording file for later replay." default:"false" example:"true (compliance/forensics)"`
	RecordingDir     string   `yaml:"recording_dir" doc:"Directory where session recordings are written." default:"/srv/vigo/recordings" example:"/srv/vigo/recordings"`
}

// ScrierGuacdAddr returns the guacd address, defaulting to "guacd:4822".
func (s ScrierSection) ScrierGuacdAddr() string {
	if s.GuacdAddr == "" {
		return "guacd:4822"
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
// defaulting to "vigo" (the Docker Compose service name).
func (s ScrierSection) ScrierProxyHostname() string {
	if s.ProxyHostname == "" {
		return "vigo"
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
	Standards       []string         `yaml:"standards" doc:"Enabled framework slugs (e.g. hipaa, soc2, pci). Empty means all frameworks are enabled." example:"[hipaa, soc2, pci_dss, nist_800_53]"`
	ApprovedSources []string         `yaml:"approved_sources" doc:"URL prefixes allowed as package-manager sources. Each package_source_audit.sources.N row is annotated with approved=true if its URL starts with any listed prefix, approved=false otherwise. Empty disables the annotation entirely." example:"[http://archive.ubuntu.com/, https://download.docker.com/, https://download.fedoraproject.org/]"`
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
	Enabled bool   `yaml:"enabled" doc:"Fetch CVSS scores and descriptions from the NVD API." default:"false" example:"true"`
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
	// "aggregate": count nodes across all spanner spokes (hub only).
	// In aggregate mode, spokes skip license enforcement entirely.
	Mode string `yaml:"mode" doc:"Counting mode for license enforcement." options:"local|aggregate" default:"local" example:"aggregate (hub in a spanner deployment)"`
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
	Migrations   string `yaml:"migrations" doc:"Database migration files directory." default:"migrations" example:"migrations  (rarely overridden)"`
	CustomTraits string `yaml:"custom_traits" doc:"Custom trait collector scripts directory." default:"custom-traits" example:"/srv/vigo/custom-traits"`
	Docs         string `yaml:"docs" doc:"Documentation directory served by the in-app docs browser." default:"docs" example:"docs, /srv/vigo/docs"`
	Tasks        string `yaml:"tasks" doc:"Reusable task definitions directory." default:".live/tasks" example:"/srv/vigo/stockpile/.live/tasks"`
	Workflows    string `yaml:"workflows" doc:"Workflow definitions directory." default:".live/workflows" example:"/srv/vigo/stockpile/.live/workflows"`
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
	ApprovalRequired     bool              `yaml:"approval_required" doc:"Require a second admin to approve every config publish before it takes effect." default:"false" example:"true (regulated environments)"`
	ApprovalTimeout      string            `yaml:"approval_timeout" doc:"How long a pending change remains valid before it expires." default:"24h" example:"1h, 8h, 24h, 72h (accepts s, m, h)"`
	Retraction           RetractionSection `yaml:"retraction" doc:"Automatic drafting of .retract modules when modules are removed at publish."`
}

// RetractionSection configures automatic retract module generation when
// modules are removed during config publish. Requires both this and AI to
// be enabled. When a module is removed, a .retract draft is generated
// in the stockpile for operator review.
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
	LastSeenFlush        string        `yaml:"last_seen_flush" doc:"How often the last-seen timestamp is flushed to the database." default:"10s" example:"5s, 10s, 30s (accepts s, m)"`
	TraitsPruneInterval  string        `yaml:"traits_prune_interval" doc:"How often stale trait rows are pruned." default:"5m" example:"5m, 15m, 1h (accepts s, m, h)"`
	MetricsInterval      string        `yaml:"metrics_interval" doc:"Prometheus gauge refresh interval." default:"30s" example:"15s, 30s, 1m (match scrape_interval)"`
	TraitsScannerTTL     string        `yaml:"traits_scanner_ttl" doc:"How long agent-side trait scanner caches remain valid." default:"30s" example:"30s (default), 5m (lighten disk)"`
	RunStore             string        `yaml:"run_store" doc:"Where convergence runs are persisted." options:"database|memory" default:"database" example:"database (prod), memory (ephemeral test)"`
	RunStoreCapacity     int           `yaml:"run_store_capacity" doc:"Per-envoy run history depth when run_store is 'memory'." default:"20" example:"20 (default), 100 (debug retention)"`
	MaxConcurrentStreams uint32        `yaml:"max_concurrent_streams" doc:"Maximum concurrent gRPC bidirectional streams." default:"5000" example:"500 (small), 5000 (default), 25000 (10k-node fleet)"`
	Webhook              WebhookTuning `yaml:"webhook" doc:"Dispatch tuning for outbound webhooks."`

	// Go runtime tuning.
	GOGC        *int   `yaml:"gogc" doc:"Go GC target percentage (equivalent to the GOGC env var). Unset uses the Go default." example:"50 (more GC, less RAM), 100 (default), 200 (less GC, more RAM)"`
	MemoryLimit string `yaml:"memory_limit" doc:"Go memory soft-limit (equivalent to GOMEMLIMIT; e.g. 4GiB, 512MiB). Empty = no limit." example:"512MiB, 2GiB, 8GiB  (typically 70-80% of container limit)"`

	// gRPC transport tuning.
	GRPCReadBuffer    int    `yaml:"grpc_read_buffer" doc:"Per-connection gRPC read buffer bytes. 0 uses the gRPC default (32 KiB)." example:"32768 (32 KiB), 65536 (64 KiB)"`
	GRPCWriteBuffer   int    `yaml:"grpc_write_buffer" doc:"Per-connection gRPC write buffer bytes. 0 uses the gRPC default (32 KiB)." example:"32768 (32 KiB), 65536 (64 KiB)"`
	MaxConnectionAge  string `yaml:"max_connection_age" doc:"Force reconnect after this duration. Empty means no age limit." example:"1h, 6h, 24h (forces LB rebalancing)"`
	MaxConnectionIdle string `yaml:"max_connection_idle" doc:"Close idle connections after this duration." default:"15m" example:"5m, 15m, 1h (accepts s, m, h)"`
	KeepaliveTime     string `yaml:"keepalive_time" doc:"Interval between server→client keepalive pings." default:"30s" example:"15s, 30s, 1m (accepts s, m)"`
	KeepaliveTimeout  string `yaml:"keepalive_timeout" doc:"Deadline for a keepalive ACK before the connection is considered dead." default:"10s" example:"5s, 10s, 20s (accepts s, m)"`
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
	CheckinPerEnvoy int  `yaml:"checkin_per_envoy" doc:"Per-envoy check-in rate cap in requests per minute." default:"6" example:"6 (default), 12 (aggressive intervals), 2 (conservative)"`
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
	DSN       string `yaml:"dsn" doc:"Database DSN — sqlite3 file path or postgres connection URL (supports the secret: prefix)." example:"file:/srv/vigo/vigo.db, postgres://user@host:5432/vigo?sslmode=require, secret:vigo/db/dsn"`
	Driver    string `yaml:"driver" doc:"Database driver." options:"sqlite|postgres" default:"sqlite" example:"sqlite (single-node), postgres (HA)"`
	Retention string `yaml:"retention" doc:"Age-based pruning for runs, tasks, and workflows (e.g. 90d, 2160h). Empty disables pruning." example:"2160h (90d), 720h (30d); no d unit — use hours"`
}

// CheckInSection holds check-in interval settings.
type CheckInSection struct {
	Interval            string `yaml:"interval" doc:"Nominal agent check-in interval as a Go duration string." example:"15s (responsive), 30s (default), 5m (conservative); minimum 5s"`
	JitterPercent       int    `yaml:"jitter_percent" doc:"Random ± jitter applied to the check-in interval (percent)." example:"0-50; 10 (default) spreads load across a ±10% window"`
	BundleMaxAge        string `yaml:"bundle_max_age" doc:"Signed-bundle TTL on agents (e.g. 24h). \"0\" means bundles never expire." default:"24h" example:"1h, 24h, 168h (accepts s, m, h); 0 to disable"`
	ObserveOnly         bool   `yaml:"observe_only" doc:"Fleet-wide observe-only mode — all envoys report drift but never apply changes." default:"false" example:"true during Salt/Puppet migration"`
	ComplianceThreshold int    `yaml:"compliance_threshold" doc:"Convergence percentage below which the convergence.threshold event fires. 0 disables." example:"0-100; 90 for prod alerting, 95 for tight SLOs"`
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
	Envoy          string                    `yaml:"envoy"`
	MatchPattern    string                    `yaml:"-"` // original match pattern from envoys.vgo
	Environment     string                    `yaml:"environment"`
	Roles           []string                  `yaml:"-"` // role names that were expanded
	Modules         []ModuleConfig            `yaml:"modules"`
	Vars            map[string]any            `yaml:"vars"`
	VarOrder        []string                  `yaml:"-"`
	Tags        []string `yaml:"-"` // static tags from the match block, propagated to FleetIndex on check-in
	HasLookups  bool     `yaml:"-"` // true if any resource attribute or top-level var is a tag/host/trait lookup table
	ObserveOnly bool     `yaml:"-"` // observe-only mode: report drift only, don't enforce
	MerkleRoot  string   `yaml:"-"` // SHA256 Merkle root over modules + vars (computed at load)

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
	clone.Modules = make([]ModuleConfig, len(c.Modules))
	for i, mod := range c.Modules {
		clone.Modules[i] = mod
		clone.Modules[i].Resources = cloneResources(mod.Resources)
		if mod.DependsOn != nil {
			clone.Modules[i].DependsOn = append([]string(nil), mod.DependsOn...)
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

// ModuleConfig describes a module assigned to a envoy.
type ModuleConfig struct {
	Name        string              `yaml:"name"`
	DependsOn   []string            `yaml:"depends_on"`
	When        string              `yaml:"when,omitempty"`
	Resources   []ResourceConfig    `yaml:"resources"`
	Compliance  map[string][]string `yaml:"-"` // framework → control IDs, including ancestor compliance.vgo claims
	ContentHash string              `yaml:"-"` // pre-computed at config load for delta transfer
}

// ResourceConfig describes a single resource within a module.
type ResourceConfig struct {
	Name             string                    `yaml:"name"`
	Type             string                    `yaml:"type"`
	When             string                    `yaml:"when,omitempty"`
	Attributes       map[string]any            `yaml:",inline"`
	Notify           []string                  `yaml:"notify"`
	Subscribes       []string                  `yaml:"subscribes"`
	WatchSecret []string                  `yaml:"watch_secret"`
	DependsOn        []string                  `yaml:"depends_on"`
	Before           []string                  `yaml:"before"`
	ForEach          string                    `yaml:"-"` // var name to iterate (config-load-time expansion)
	Case             string                    `yaml:"-"` // template expression for case switching (check-in-time)
	CaseMatch        map[string]map[string]any `yaml:"-"` // case branch → attribute overrides
}

// ModuleDefinition is a reusable module loaded from config/modules/*.yaml.
// It has a custom UnmarshalYAML that flattens conditional_block entries
// in the resources list into plain ResourceConfig items.
type ModuleDefinition struct {
	Name            string                        `yaml:"name"`
	SourceFile      string                        `yaml:"-"` // stockpile-relative path, set by loader
	DependsOn       []string                      `yaml:"depends_on"`
	Before          []string                      `yaml:"before"`
	Defaults        map[string]any                `yaml:"defaults"`
	Resources       []ResourceConfig              `yaml:"resources"`
	Vars            map[string]any                `yaml:"vars"`
	Compliance      map[string][]string           `yaml:"compliance"`
}

// UnmarshalYAML implements yaml.Unmarshaler for ModuleDefinition.
// It handles the "resources" key specially by passing its child nodes
// through FlattenResourceNodes to support conditional_block entries.
// All other keys are decoded via the default YAML decoder.
func (m *ModuleDefinition) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("ModuleDefinition: expected mapping, got %v", value.Kind)
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
			if err := val.Decode(&m.Compliance); err != nil {
				return fmt.Errorf("decoding compliance: %w", err)
			}
		}
	}

	applyDefaults(m.Resources, m.Defaults)
	return nil
}

// ModuleRef is a reference to a module in a role or envoy entry. It supports
// two YAML forms:
//
//	modules: [nginx]               # plain string -> ModuleRef{Name: "nginx"}
//	modules:
//	  - name: nginx                # object form with optional when expression
//	    when: "is_debian"
type ModuleRef struct {
	Name string `yaml:"name"`
	When string `yaml:"when,omitempty"`
}

// UnmarshalYAML implements yaml.Unmarshaler for ModuleRef. It handles two forms:
//   - Scalar string: "nginx" -> ModuleRef{Name: "nginx"}
//   - Mapping: {name: nginx, when: "is_debian"} -> decoded normally
func (m *ModuleRef) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		m.Name = value.Value
		return nil
	case yaml.MappingNode:
		// Use an alias to avoid infinite recursion in UnmarshalYAML.
		type moduleRefAlias ModuleRef
		var alias moduleRefAlias
		if err := value.Decode(&alias); err != nil {
			return err
		}
		*m = ModuleRef(alias)
		return nil
	default:
		return fmt.Errorf("ModuleRef: expected string or mapping, got %v", value.Kind)
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
	Name     string      `yaml:"name"`
	When     string      `yaml:"when,omitempty"`
	Includes []string    `yaml:"includes"`        // other role names to include (single level)
	Modules  []ModuleRef `yaml:"modules"`          // unconditional modules
	Case     []RoleCase  `yaml:"case,omitempty"`   // conditional module groups
}

// RoleCase is a conditional group of modules within a role definition.
// All cases whose when: expression matches contribute their modules.
type RoleCase struct {
	When    string      `yaml:"when"`
	Modules []ModuleRef `yaml:"modules"`
}

// ValidDeviceTypes lists the supported network device types for gateway proxy management.
var ValidDeviceTypes = map[string]bool{
	"cisco_ios":      true,
	"cisco_nxos":     true,
	"juniper_junos":  true,
	"arista_eos":     true,
	"cisco_iosxr":    true,
}

// EnvoyEntry is a single entry in envoys.vgo. It has a custom UnmarshalYAML
// that merges duplicate 'modules' and 'roles' keys (standard YAML would
// silently keep only the last value).
type EnvoyEntry struct {
	Match       string                    `yaml:"match"`
	Environment string                    `yaml:"environment"`
	Roles       []RoleRef                 `yaml:"roles"`
	Modules     []ModuleRef               `yaml:"modules"`
	Vars        map[string]any            `yaml:"vars"`
	Tags                 []string `yaml:"tags"` // static tags for targeting
	ExcludeModules       []string `yaml:"exclude_modules"`   // opt out of inherited modules
	ExcludeUsercrates    []string `yaml:"exclude_usercrates"` // opt out of auto-applied usercrates from usercrates/ subdirs in the ancestor chain
	ExcludeResources     []string `yaml:"exclude_resources"` // opt out of specific resources by name
	ObserveOnly          bool     `yaml:"observe_only"` // observe-only mode: report drift only, don't enforce

	// DirPath is the relative directory path within the config tree where
	// this entry was loaded from. Set by the loader for inheritance resolution.
	DirPath string `yaml:"-"`

	// SourceFile is the filename (without directory) of the config file
	// that defined this entry. Set by the loader for trace display.
	SourceFile string `yaml:"-"`

	// OwnModules/OwnRoles/OwnVars hold the entry's original values before
	// inheritance merging. Set by applyInheritance for trace display.
	OwnModules []ModuleRef    `yaml:"-"`
	OwnRoles   []RoleRef      `yaml:"-"`
	OwnVars    map[string]any `yaml:"-"`

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
//   - Files in the envoys directory tree (anywhere under stockpile/ except
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
	Modules           []ModuleRef    `yaml:"modules"`
	Roles             []RoleRef      `yaml:"roles"`
	Vars              map[string]any `yaml:"vars"`
	ExcludeUsercrates []string       `yaml:"exclude_usercrates"` // drop named usercrates for every entry at this directory level and below
	DirPath           string         `yaml:"-"` // relative dir path (e.g., "production/web")
}

// PerEnvOverrideBlock holds modules, vars, and exclusions for a single
// environment within a environments.vgo file.
type PerEnvOverrideBlock struct {
	Modules           []ModuleRef    `yaml:"modules"`
	ExcludeModules    []string       `yaml:"exclude_modules"`
	ExcludeUsercrates []string       `yaml:"exclude_usercrates"`
	Vars              map[string]any `yaml:"vars"`
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
		case "modules":
			var extra []ModuleRef
			if err := val.Decode(&extra); err != nil {
				return fmt.Errorf("decoding modules: %w", err)
			}
			n.Modules = append(n.Modules, extra...)
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
		case "exclude_modules":
			if err := val.Decode(&n.ExcludeModules); err != nil {
				return fmt.Errorf("decoding exclude_modules: %w", err)
			}
		case "exclude_usercrates":
			if err := val.Decode(&n.ExcludeUsercrates); err != nil {
				return fmt.Errorf("decoding exclude_usercrates: %w", err)
			}
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
	ControlID string   `json:"control_id"`
	Modules   []string `json:"modules"`
}
