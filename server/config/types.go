// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package config handles loading and resolving per-envoy configuration
// from YAML files on disk. It integrates with the secrets provider to
// resolve "secret:" prefixes and supports publish-triggered reload.
package config

import (
	"fmt"
	"time"

	"github.com/daniel-alexander4/vigo/server/conditionals"
	"github.com/daniel-alexander4/vigo/server/secrets"
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
	Role             SpannerRole        `yaml:"role"`               // standalone (default), hub, spoke
	SpokeID          string             `yaml:"spoke_id"`           // required for spoke mode
	HubAddr          string             `yaml:"hub_addr"`           // hub gRPC address (for spoke→hub status reporting)
	HubFallbackAddrs []string           `yaml:"hub_fallback_addrs"` // spoke-only: fallback hub addresses for failover
	Spokes           []SpokeEntry       `yaml:"spokes"`             // hub-only: spoke definitions
	AutoFailover     AutoFailoverConfig `yaml:"auto_failover"`      // hub-only: automatic spoke failover
}

// AutoFailoverConfig controls automatic spoke failover on the hub.
// When enabled, the hub periodically probes spoke health and drains
// spokes that fail consecutively.
type AutoFailoverConfig struct {
	Enabled   bool   `yaml:"enabled"`   // default false
	Threshold int    `yaml:"threshold"` // consecutive failures before drain (default 3)
	Interval  string `yaml:"interval"`  // check interval (default "30s")
}

// SpokeEntry defines a spoke server the hub aggregates from.
type SpokeEntry struct {
	ID       string   `yaml:"id"`       // unique spoke identifier
	Addr     string   `yaml:"addr"`     // gRPC address (host:port)
	Patterns []string `yaml:"patterns"` // hostname glob patterns routed to this spoke
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
	Server     ServerSection    `yaml:"server"`
	Database   DatabaseSection  `yaml:"database"`
	Auth       AuthSection      `yaml:"auth"`
	CheckIn    CheckInSection   `yaml:"checkin"`
	Watcher    WatcherSection   `yaml:"watcher"`
	Secrets    secrets.Config   `yaml:"secrets"`
	Bootstrap  BootstrapConfig  `yaml:"bootstrap"`
	Spanner    SpannerSection   `yaml:"spanner"`
	Tuning     TuningSection    `yaml:"tuning"`
	SMTP       SMTPSection      `yaml:"smtp"`
	AI         AISection        `yaml:"ai"`
	Backup     BackupSection    `yaml:"backup"`
	Export     ExportSection    `yaml:"export"`
	Publish    PublishSection   `yaml:"publish"`
	RateLimit  RateLimitSection `yaml:"rate_limit"`
	Maintenance MaintenanceSection `yaml:"maintenance"`
	Paths      PathsSection     `yaml:"paths"`
	Peers      PeersSection     `yaml:"peers"`
	Task       TaskSection       `yaml:"task"`
	Branding   BrandingSection  `yaml:"branding"`
	License      LicenseSection      `yaml:"license"`
	GRC          GRCSection          `yaml:"grc"`
	Integrations IntegrationsSection `yaml:"integrations"`
	Risk         RiskSection         `yaml:"risk"`
	StreamEdit   StreamEditSection   `yaml:"stream_edit"`
	Compliance   ComplianceSection   `yaml:"compliance"`
	Scrier       ScrierSection       `yaml:"scrier"`
	Swarm        SwarmSection        `yaml:"swarm"`
}

// SwarmSection configures peer-to-peer content distribution. When enabled,
// the server stores content-addressed blobs and coordinates distribution
// to agents via the swarm protocol on port 1531.
type SwarmSection struct {
	Enabled             bool   `yaml:"enabled"`               // default false
	SeedDir             string `yaml:"seed_dir"`              // default "/var/lib/vigo/swarm/seed"
	Port                int    `yaml:"port"`                  // agent swarm listen port (default 1531)
	MaxBandwidthPercent int    `yaml:"max_bandwidth_percent"` // default 50 — max % of link speed agents use for swarm
	EnvoySeeding        string `yaml:"envoy_seeding"`         // "disabled" (default) | "approval" | "open"
	MinEnvoyAge         string `yaml:"min_envoy_age"`         // minimum enrollment age to seed; default "30d"
}

// ScrierSection configures the Scrier remote access feature.
type ScrierSection struct {
	Enabled          bool     `yaml:"enabled"`            // default false
	GuacdAddr        string   `yaml:"guacd_addr"`         // default "guacd:4822"
	ProxyHostname    string   `yaml:"proxy_hostname"`     // default "vigo" — hostname guacd uses to reach this server
	AllowedPorts     []uint32 `yaml:"allowed_ports"`      // default [3389, 22]
	MaxSessions      int      `yaml:"max_sessions"`       // default 10
	RecordingEnabled bool     `yaml:"recording_enabled"`  // default false
	RecordingDir     string   `yaml:"recording_dir"`      // default "/srv/vigo/recordings"
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

// ComplianceSection configures which compliance frameworks are active.
// When Standards is empty or the section is omitted, all frameworks are enabled.
type ComplianceSection struct {
	Standards []string `yaml:"standards"`
}

// StreamEditSection configures guardrails for stream_edit pipelines.
type StreamEditSection struct {
	Enabled        *bool    `yaml:"enabled"`         // default true; false strips all stream_edit attributes
	AllowedPaths   []string `yaml:"allowed_paths"`   // directories where scripts must reside (default ["/srv/vigo/scripts"])
	DefaultTimeout string   `yaml:"default_timeout"` // per-script timeout (default "10s")
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
	NVD NVDConfig `yaml:"nvd"`
}

// NVDConfig configures optional NVD CVE API enrichment.
type NVDConfig struct {
	Enabled bool   `yaml:"enabled"`
	APIKey  string `yaml:"api_key"` // supports secret: prefix
}

// IntegrationsSection configures external platform integrations.
type IntegrationsSection struct {
	Slack       *SlackIntegration       `yaml:"slack,omitempty"`
	PagerDuty   *PagerDutyIntegration   `yaml:"pagerduty,omitempty"`
	Opsgenie    *OpsgenieIntegration    `yaml:"opsgenie,omitempty"`
	Teams       *TeamsIntegration       `yaml:"teams,omitempty"`
	ConnectWise *ConnectWiseIntegration `yaml:"connectwise,omitempty"`
	Autotask    *AutotaskIntegration    `yaml:"autotask,omitempty"`
	Splunk      *SplunkIntegration      `yaml:"splunk,omitempty"`
	Elastic     *ElasticIntegration     `yaml:"elastic,omitempty"`
	Datadog      *DatadogIntegration      `yaml:"datadog,omitempty"`
	ServiceNow   *ServiceNowIntegration   `yaml:"servicenow,omitempty"`
	Jira         *JiraIntegration         `yaml:"jira,omitempty"`
	Zendesk      *ZendeskIntegration      `yaml:"zendesk,omitempty"`
	Freshservice *FreshserviceIntegration `yaml:"freshservice,omitempty"`
	Linear       *LinearIntegration       `yaml:"linear,omitempty"`
	GitHubIssues *GitHubIssuesIntegration `yaml:"github_issues,omitempty"`
	NinjaRMM     *NinjaRMMIntegration     `yaml:"ninjarmm,omitempty"`
	Kaseya       *KaseyaIntegration       `yaml:"kaseya,omitempty"`
	SumoLogic    *SumoLogicIntegration    `yaml:"sumologic,omitempty"`
	Sentinel     *SentinelIntegration     `yaml:"sentinel,omitempty"`
	Device42     *Device42Integration     `yaml:"device42,omitempty"`
	SnipeIT      *SnipeITIntegration      `yaml:"snipeit,omitempty"`
}

// SlackIntegration sends formatted messages to a Slack channel.
type SlackIntegration struct {
	Enabled     bool     `yaml:"enabled"`
	WebhookURL  string   `yaml:"webhook_url"`  // Slack incoming webhook URL
	Events      []string `yaml:"events"`       // event types to forward (empty = all)
	MinSeverity string   `yaml:"min_severity"` // minimum severity: "info", "warning", "critical" (default: all)
}

// PagerDutyIntegration triggers incidents via PagerDuty Events API v2.
type PagerDutyIntegration struct {
	Enabled     bool     `yaml:"enabled"`
	RoutingKey  string   `yaml:"routing_key"`  // supports secret: prefix
	Events      []string `yaml:"events"`       // event types to forward
	MinSeverity string   `yaml:"min_severity"` // minimum severity filter
	AutoResolve bool     `yaml:"auto_resolve"` // send resolve events when issues clear
}

// OpsgenieIntegration triggers alerts via Opsgenie API.
type OpsgenieIntegration struct {
	Enabled     bool     `yaml:"enabled"`
	APIKey      string   `yaml:"api_key"`      // supports secret: prefix
	Events      []string `yaml:"events"`
	MinSeverity string   `yaml:"min_severity"`
}

// TeamsIntegration sends adaptive cards to Microsoft Teams.
type TeamsIntegration struct {
	Enabled     bool     `yaml:"enabled"`
	WebhookURL  string   `yaml:"webhook_url"`  // Teams incoming webhook URL
	Events      []string `yaml:"events"`
	MinSeverity string   `yaml:"min_severity"`
}

// ConnectWiseIntegration creates tickets on drift/failure events.
type ConnectWiseIntegration struct {
	Enabled   bool     `yaml:"enabled"`
	URL       string   `yaml:"url"`        // e.g. "https://api-na.myconnectwise.net/v4_6_release/apis/3.0"
	CompanyID string   `yaml:"company_id"`
	PublicKey string   `yaml:"public_key"`
	PrivateKey string  `yaml:"private_key"` // supports secret: prefix
	BoardID   int      `yaml:"board_id"`    // service board ID for new tickets
	Events    []string `yaml:"events"`
}

// AutotaskIntegration creates tickets in Autotask/Datto PSA.
type AutotaskIntegration struct {
	Enabled  bool     `yaml:"enabled"`
	URL      string   `yaml:"url"`
	Username string   `yaml:"username"`
	Password string   `yaml:"password"` // supports secret: prefix
	QueueID  int      `yaml:"queue_id"`
	Events   []string `yaml:"events"`
}

// SplunkIntegration forwards events to Splunk HTTP Event Collector.
type SplunkIntegration struct {
	Enabled  bool     `yaml:"enabled"`
	URL      string   `yaml:"url"`       // HEC endpoint URL
	Token    string   `yaml:"token"`     // HEC token, supports secret: prefix
	Index    string   `yaml:"index"`     // Splunk index (optional)
	Source   string   `yaml:"source"`    // source identifier (default: "vigo")
	Events   []string `yaml:"events"`
}

// ElasticIntegration forwards events to Elasticsearch.
type ElasticIntegration struct {
	Enabled  bool     `yaml:"enabled"`
	URL      string   `yaml:"url"`      // Elasticsearch URL
	Index    string   `yaml:"index"`    // index name (default: "vigo-events")
	APIKey   string   `yaml:"api_key"`  // supports secret: prefix
	Events   []string `yaml:"events"`
}

// DatadogIntegration forwards events to Datadog.
type DatadogIntegration struct {
	Enabled bool     `yaml:"enabled"`
	APIKey  string   `yaml:"api_key"` // supports secret: prefix
	Site    string   `yaml:"site"`    // "datadoghq.com" (default) or "datadoghq.eu"
	Events  []string `yaml:"events"`
}

// ServiceNowIntegration syncs envoy inventory as CIs and creates incidents.
type ServiceNowIntegration struct {
	Enabled  bool     `yaml:"enabled"`
	Instance string   `yaml:"instance"` // e.g. "mycompany.service-now.com"
	Username string   `yaml:"username"`
	Password string   `yaml:"password"` // supports secret: prefix
	Events   []string `yaml:"events"`
}

// JiraIntegration creates issues in Jira on drift/failure events.
type JiraIntegration struct {
	Enabled  bool     `yaml:"enabled"`
	URL      string   `yaml:"url"`       // e.g. "https://mycompany.atlassian.net"
	Email    string   `yaml:"email"`     // Jira user email
	APIToken string   `yaml:"api_token"` // supports secret: prefix
	Project  string   `yaml:"project"`   // project key (e.g. "OPS")
	IssueType string  `yaml:"issue_type"` // default "Task"
	Events   []string `yaml:"events"`
}

// ZendeskIntegration creates tickets in Zendesk.
type ZendeskIntegration struct {
	Enabled   bool     `yaml:"enabled"`
	Subdomain string   `yaml:"subdomain"` // e.g. "mycompany" (.zendesk.com)
	Email     string   `yaml:"email"`
	APIToken  string   `yaml:"api_token"` // supports secret: prefix
	Events    []string `yaml:"events"`
}

// FreshserviceIntegration creates tickets in Freshservice.
type FreshserviceIntegration struct {
	Enabled  bool     `yaml:"enabled"`
	Domain   string   `yaml:"domain"` // e.g. "mycompany.freshservice.com"
	APIKey   string   `yaml:"api_key"` // supports secret: prefix
	Events   []string `yaml:"events"`
}

// LinearIntegration creates issues in Linear.
type LinearIntegration struct {
	Enabled bool     `yaml:"enabled"`
	APIKey  string   `yaml:"api_key"` // supports secret: prefix
	TeamID  string   `yaml:"team_id"` // Linear team ID
	Events  []string `yaml:"events"`
}

// GitHubIssuesIntegration creates issues in a GitHub repository.
type GitHubIssuesIntegration struct {
	Enabled bool     `yaml:"enabled"`
	Owner   string   `yaml:"owner"` // GitHub org or user
	Repo    string   `yaml:"repo"`  // repository name
	Token   string   `yaml:"token"` // personal access token, supports secret: prefix
	Labels  []string `yaml:"labels"` // labels to apply (e.g. ["vigo", "drift"])
	Events  []string `yaml:"events"`
}

// NinjaRMMIntegration creates tickets in NinjaRMM.
type NinjaRMMIntegration struct {
	Enabled      bool     `yaml:"enabled"`
	URL          string   `yaml:"url"`           // e.g. "https://app.ninjarmm.com"
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"` // supports secret: prefix
	Events       []string `yaml:"events"`
}

// KaseyaIntegration creates tickets in Kaseya VSA.
type KaseyaIntegration struct {
	Enabled  bool     `yaml:"enabled"`
	URL      string   `yaml:"url"`       // Kaseya VSA API URL
	Username string   `yaml:"username"`
	Password string   `yaml:"password"` // supports secret: prefix
	Events   []string `yaml:"events"`
}

// SumoLogicIntegration forwards events to Sumo Logic HTTP source.
type SumoLogicIntegration struct {
	Enabled bool     `yaml:"enabled"`
	URL     string   `yaml:"url"` // HTTP source endpoint URL
	Events  []string `yaml:"events"`
}

// SentinelIntegration forwards events to Microsoft Sentinel (Log Analytics).
type SentinelIntegration struct {
	Enabled     bool     `yaml:"enabled"`
	WorkspaceID string   `yaml:"workspace_id"`
	SharedKey   string   `yaml:"shared_key"` // supports secret: prefix
	LogType     string   `yaml:"log_type"`   // custom log table name (default: "VigoEvents")
	Events      []string `yaml:"events"`
}

// Device42Integration syncs envoy inventory to Device42.
type Device42Integration struct {
	Enabled  bool     `yaml:"enabled"`
	URL      string   `yaml:"url"`      // e.g. "https://device42.example.com"
	Username string   `yaml:"username"`
	Password string   `yaml:"password"` // supports secret: prefix
	Events   []string `yaml:"events"`
}

// SnipeITIntegration syncs envoy inventory to Snipe-IT.
type SnipeITIntegration struct {
	Enabled bool     `yaml:"enabled"`
	URL     string   `yaml:"url"`     // e.g. "https://snipeit.example.com/api/v1"
	Token   string   `yaml:"token"`   // API token, supports secret: prefix
	Events  []string `yaml:"events"`
}

// LicenseSection configures license enforcement behavior.
type LicenseSection struct {
	// Mode controls how nodes are counted for license enforcement.
	// "local" (default): count enrolled nodes on this server only.
	// "aggregate": count nodes across all spanner spokes (hub only).
	// In aggregate mode, spokes skip license enforcement entirely.
	Mode string `yaml:"mode"`
}

// BrandingSection configures custom branding for the web UI.
// When set, the UI displays the custom org name and serves logos
// from the specified paths instead of the embedded Vigo defaults.
type BrandingSection struct {
	OrgName     string `yaml:"org_name"`      // organization name shown in UI header/footer
	LogoPath    string `yaml:"logo_path"`     // path to custom sidebar logo (default: embedded logo-dark.svg)
	FaviconPath string `yaml:"favicon_path"`  // path to custom favicon (default: embedded icon.svg)
	Theme       string `yaml:"theme"`         // "light" (default), "dark", or "auto" (follows OS preference)
}

// GRCSection configures GRC platform integrations for pushing compliance
// evidence to external governance, risk, and compliance platforms.
type GRCSection struct {
	Integrations []GRCIntegration `yaml:"integrations"`
}

// GRCIntegration defines a single GRC platform connection.
type GRCIntegration struct {
	Name         string            `yaml:"name"`          // "vanta", "drata", "servicenow"
	Endpoint     string            `yaml:"endpoint"`      // API URL
	APIKey       string            `yaml:"api_key"`       // supports secret: prefix
	PushInterval string            `yaml:"push_interval"` // e.g. "1h", "6h"
	Enabled      bool              `yaml:"enabled"`
	Frameworks   map[string]string `yaml:"frameworks"`    // vigo standard key → GRC framework ID
}

// PathsSection configures directory paths for server subsystems.
// All paths default to subdirectories of the working directory.
type PathsSection struct {
	Migrations   string `yaml:"migrations"`    // database migration files (default: "migrations")
	CustomTraits string `yaml:"custom_traits"` // custom trait collector scripts (default: "custom-traits")
	Docs         string `yaml:"docs"`          // documentation directory (default: "docs")
	Tasks        string `yaml:"tasks"`         // reusable task definitions (default: ".live/tasks")
	Workflows    string `yaml:"workflows"`     // workflow definitions (default: ".live/workflows")
	AgentDist    string `yaml:"agent_dist"`    // agent binary distribution (default: "dist/agent")
	LicenseDir   string `yaml:"license_dir"`   // license file directory (default: "/srv/vigo/license")
	Snapshots    string `yaml:"snapshots"`     // backup snapshot directory (default: "/srv/vigo/snapshots")
}

// AuthSection configures web UI and REST API authentication.
// Default method is "basic" — username/password with bcrypt hashes in the
// local secrets store at vigo/web/auth/<username>.
type AuthSection struct {
	Method             string           `yaml:"method"`               // "basic" (default), "oidc", "isowebauth"
	SessionIdleTimeout string           `yaml:"session_idle_timeout"` // duration, e.g. "15m" (default)
	OIDC               OIDCAuthConfig   `yaml:"oidc"`
	Isowebauth         IsowebauthAuthConfig `yaml:"isowebauth"`
}

// OIDCAuthConfig holds OIDC provider settings.
type OIDCAuthConfig struct {
	Issuer       string   `yaml:"issuer"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	RedirectURL  string   `yaml:"redirect_url"`
	Scopes       []string `yaml:"scopes"`
}

// IsowebauthAuthConfig holds isowebauth settings.
type IsowebauthAuthConfig struct {
	Namespace string `yaml:"namespace"` // default: "vigo"
}

// ExportSection configures compliance framework export endpoints.
// All types are disabled by default and must be individually enabled.
type ExportSection struct {
	SIEM  bool `yaml:"siem"`  // enable SIEM export endpoint
	CMDB  bool `yaml:"cmdb"`  // enable CMDB export endpoint
	OSCAL bool `yaml:"oscal"` // enable OSCAL export endpoint
}

// PublishSection configures blast radius limits for config publish.
// When compliance_threshold is set (1-100), the server monitors fleet compliance
// after each config reload. If compliance drops below the threshold within
// the rollback window, the previous config is automatically restored.
type PublishSection struct {
	ConvergenceThreshold int                `yaml:"compliance_threshold"` // 0 = disabled, 1-100 = minimum fleet compliance %
	RollbackWindow       string             `yaml:"rollback_window"`     // duration to watch after reload (default "5m")
	ApprovalRequired     bool               `yaml:"approval_required"`   // require two-person approval before config reload (default false)
	ApprovalTimeout      string             `yaml:"approval_timeout"`    // how long pending changes stay valid (default "24h")
	Retraction           RetractionSection  `yaml:"retraction"`          // auto-generate retract modules for removed modules
}

// RetractionSection configures automatic retract module generation when
// modules are removed during config publish. Requires both this and AI to
// be enabled. When a module is removed, a .retract draft is generated
// in the stockpile for operator review.
type RetractionSection struct {
	Enabled bool `yaml:"enabled"` // default false — generate .retract modules on publish
}

// PeersSection configures peer replication for server redundancy.
type PeersSection struct {
	Role    string      `yaml:"role"`    // "primary" or "peer" (default "primary")
	Servers []PeerEntry `yaml:"servers"` // list of peer server addresses
}

// PeerEntry defines a single peer server.
type PeerEntry struct {
	Addr         string `yaml:"addr"`          // gRPC address (host:port)
	SyncInterval string `yaml:"sync_interval"` // push interval (default "10s")
}

// TaskSection configures task dispatch behavior.
type TaskSection struct {
	RequireDefinition bool `yaml:"require_definition"` // when true, only named task definitions can be dispatched (default false)
}

// BackupSection configures continuous SQLite WAL replication via Litestream.
// Disabled when URL is empty. Only applies when database.driver is "sqlite".
type BackupSection struct {
	URL             string `yaml:"url"`               // replica URL: s3://bucket/path, gcs://bucket/path, file:///path, sftp://host/path
	AccessKeyID     string `yaml:"access_key_id"`     // S3/GCS access key (supports secret: prefix)
	SecretAccessKey string `yaml:"secret_access_key"` // S3/GCS secret key (supports secret: prefix)
	Region          string `yaml:"region"`             // AWS region (optional, defaults to provider default)
	Endpoint        string `yaml:"endpoint"`           // S3-compatible endpoint for MinIO, Backblaze, etc.
	ForcePathStyle  bool   `yaml:"force_path_style"`   // use path-style URLs (required for MinIO)
	Retention       string `yaml:"retention"`           // snapshot retention period (default "720h" = 30 days)
	SyncInterval    string `yaml:"sync_interval"`       // WAL sync frequency (default "1s")
}

// AISection is the "ai:" block in server.yaml. Parsed here so it participates
// in secret resolution (api_key can use "secret:" prefix). Validation and
// defaults live in server/ai/config.go.
type AISection struct {
	Enabled       bool   `yaml:"enabled"`
	Provider      string `yaml:"provider"`
	Model         string `yaml:"model"`
	MaxTokens     int    `yaml:"max_tokens"`
	BaseURL       string `yaml:"base_url"`
	APIKey        string `yaml:"api_key"`
	RateLimit     int    `yaml:"rate_limit"`
	MaxToolRounds int    `yaml:"max_tool_rounds"`
	ContextMode   string `yaml:"context_mode"`
	DocsMode      string `yaml:"docs_mode"` // full | condensed | off (default: full)
}

// SMTPSection configures email notifications via an SMTP relay.
// Notifications are disabled when Host is empty.
type SMTPSection struct {
	Host       string                      `yaml:"host"`       // SMTP relay hostname (e.g. "smtp.example.com")
	Port       int                         `yaml:"port"`       // SMTP port (default 587)
	From       string                      `yaml:"from"`       // sender address (e.g. "vigo@example.com")
	Username   string                      `yaml:"username"`   // SMTP auth username (optional)
	Password   string                      `yaml:"password"`   // SMTP auth password (supports secret: prefix)
	TLS        bool                        `yaml:"tls"`        // use STARTTLS (default true)
	Recipients []string                    `yaml:"recipients"` // default recipient list
	Events     map[string]SMTPEventConfig  `yaml:"events"`     // event toggles + per-event overrides (nil = all)
	Digest     SMTPDigestConfig            `yaml:"digest"`     // periodic summary batching
	Evidence   EvidenceEmailConfig         `yaml:"evidence"`   // scheduled evidence delivery
}

// EvidenceEmailConfig controls automatic evidence email delivery.
type EvidenceEmailConfig struct {
	Schedule   string   `yaml:"schedule"`   // "weekly" or "monthly" (empty = disabled)
	Recipients []string `yaml:"recipients"` // override default recipients
	Frameworks []string `yaml:"frameworks"` // which frameworks to include (empty = all)
}

// SMTPEventConfig holds per-event notification settings.
// An empty value enables the event with default recipients.
type SMTPEventConfig struct {
	Recipients []string `yaml:"recipients,omitempty"` // override default recipients
	Threshold  int      `yaml:"threshold,omitempty"`  // convergence.threshold: fire below this %
}

// SMTPDigestConfig controls batching notifications into periodic summaries.
type SMTPDigestConfig struct {
	Interval string   `yaml:"interval"` // batch window (e.g. "1h", "30m"); empty = disabled
	Events   []string `yaml:"events"`   // events routed to digest instead of immediate
}

// TuningSection holds operational tuning parameters. All fields are optional
// and default to sensible values when omitted from server.yaml.
type TuningSection struct {
	SignatureWindow     string        `yaml:"signature_window"`      // default "5m"
	LastSeenFlush       string        `yaml:"last_seen_flush"`       // default "10s"
	TraitsPruneInterval string        `yaml:"traits_prune_interval"` // default "5m"
	MetricsInterval     string        `yaml:"metrics_interval"`      // default "30s"
	TraitsScannerTTL    string        `yaml:"traits_scanner_ttl"`    // default "30s"
	RunStore            string        `yaml:"run_store"`             // "database" (default) or "memory"
	RunStoreCapacity    int           `yaml:"run_store_capacity"`    // per-envoy run history depth for memory mode (default 20)
	MaxConcurrentStreams uint32       `yaml:"max_concurrent_streams"` // gRPC bidirectional stream limit (default 5000)
	Webhook             WebhookTuning `yaml:"webhook"`

	// Go runtime tuning.
	GOGC        *int   `yaml:"gogc"`         // GOGC percentage (nil → Go default 100)
	MemoryLimit string `yaml:"memory_limit"` // GOMEMLIMIT (e.g. "4GiB", "512MiB"); empty → no limit

	// gRPC transport tuning.
	GRPCReadBuffer     int    `yaml:"grpc_read_buffer"`       // per-connection read buffer bytes (0 → gRPC default 32 KiB)
	GRPCWriteBuffer    int    `yaml:"grpc_write_buffer"`      // per-connection write buffer bytes (0 → gRPC default 32 KiB)
	MaxConnectionAge   string `yaml:"max_connection_age"`     // force reconnect after this duration (empty → no limit)
	MaxConnectionIdle  string `yaml:"max_connection_idle"`    // close idle connections after (default "15m")
	KeepaliveTime      string `yaml:"keepalive_time"`         // server→client ping interval (default "30s")
	KeepaliveTimeout   string `yaml:"keepalive_timeout"`      // ping response deadline (default "10s")
	StreamSendBuffer   int    `yaml:"stream_send_buffer"`     // per-agent send channel capacity (default 8)
}

// WebhookTuning holds webhook dispatch tuning parameters.
type WebhookTuning struct {
	Timeout       string `yaml:"timeout"`        // default "10s"
	MaxRetries    int    `yaml:"max_retries"`     // default 3
	MaxConcurrent int    `yaml:"max_concurrent"`  // default 20
	RetryDelay    string `yaml:"retry_delay"`     // default "5s"
}

// RateLimitSection configures rate limiting for agent RPCs.
type RateLimitSection struct {
	Enabled         bool `yaml:"enabled"`           // enable rate limiting (default false)
	CheckinPerEnvoy int  `yaml:"checkin_per_envoy"` // per-envoy check-in rate (requests/minute, default 6)
	CheckinGlobal    int  `yaml:"checkin_global"`     // global check-in rate (requests/second, default 500)
}

// MaintenanceSection configures global change freeze.
type MaintenanceSection struct {
	FreezeUntil string `yaml:"freeze_until"` // RFC3339 timestamp; publish blocked while active
}

// WatcherSection holds secret rotation watcher configuration.
type WatcherSection struct {
	Enabled      bool   `yaml:"enabled"`
	PollInterval string `yaml:"poll_interval"`
}

// ServerSection holds listener and TLS config.
type ServerSection struct {
	GRPCListen string    `yaml:"grpc_listen"`
	APIListen  string    `yaml:"api_listen"`
	Hostname   string    `yaml:"hostname"`  // server's envoy hostname for compliance display
	TLS        TLSConfig `yaml:"tls"`
	TLSSANs    []string  `yaml:"tls_sans"` // extra Subject Alternative Names for auto-generated certs (IPs or DNS names)
}

// TLSConfig holds TLS file paths.
type TLSConfig struct {
	CertFile  string `yaml:"cert_file"`
	KeyFile   string `yaml:"key_file"`
	CAFile    string `yaml:"ca_file"`
	CAKeyFile string `yaml:"ca_key_file"`
}

// TrustedEnrollment defines a hostname/CIDR pattern for token-free enrollment.
// Envoys matching the pattern and originating from an allowed CIDR are enrolled
// without a one-time token (trusted autosigning).
type TrustedEnrollment struct {
	Pattern string   `yaml:"pattern"`
	CIDRs   []string `yaml:"cidrs,omitempty"`
}

// BootstrapConfig holds bootstrap enrollment settings.
type BootstrapConfig struct {
	TrustedPatterns []TrustedEnrollment `yaml:"trusted_enrollment"`
	CertValidity    string              `yaml:"cert_validity"` // e.g. "8760h" (1 year)
}

// DatabaseSection holds database config.
type DatabaseSection struct {
	DSN       string `yaml:"dsn"`
	Driver    string `yaml:"driver"`    // "sqlite" (default) or "postgres"
	Retention string `yaml:"retention"` // age-based pruning for runs/tasks/workflows (e.g. "90d", "2160h"); empty = no pruning
}

// CheckInSection holds check-in interval settings.
type CheckInSection struct {
	Interval             string `yaml:"interval"`
	JitterPercent        int    `yaml:"jitter_percent"`
	BundleMaxAge         string `yaml:"bundle_max_age"`         // how long signed bundles remain valid on agents (e.g. "24h"); default "24h", "0" = forever
	ObserveOnly          bool   `yaml:"observe_only"`           // global observe-only: all envoys report drift only, no changes applied
	ComplianceThreshold  int    `yaml:"compliance_threshold"`   // convergence % below which convergence.threshold fires (0 = disabled)
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
	EnvironmentOverrides map[string]map[string]any `yaml:"environment_overrides"`
	ConditionalVars      []conditionals.ConditionalVar `yaml:"-"`
	MatchVars            map[string][]conditionals.MatchBranch `yaml:"-"` // string-valued conditional vars
	MatchVarDefaults     map[string]string                     `yaml:"-"` // default values for match vars
	Tags        []string `yaml:"-"` // static tags from the match block, propagated to FleetIndex on check-in
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
	clone.ConditionalVars = append([]conditionals.ConditionalVar(nil), c.ConditionalVars...)
	clone.Tags = append([]string(nil), c.Tags...)
	return &clone
}

// ModuleConfig describes a module assigned to a envoy.
type ModuleConfig struct {
	Name        string           `yaml:"name"`
	DependsOn   []string         `yaml:"depends_on"`
	When        string           `yaml:"when,omitempty"`
	Resources   []ResourceConfig `yaml:"resources"`
	ContentHash string           `yaml:"-"` // pre-computed at config load for delta transfer
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
	ConditionalVars  []conditionals.ConditionalVar              `yaml:"-"`
	MatchVars        map[string][]conditionals.MatchBranch      `yaml:"-"`
	MatchVarDefaults map[string]string                          `yaml:"-"`
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
	VarsFrom    []string                  `yaml:"vars_from"`  // external var files (relative to example-configs/)
	Vars        map[string]any            `yaml:"vars"`
	EnvironmentOverrides map[string]map[string]any `yaml:"environment_overrides"`
	Tags                 []string `yaml:"tags"` // static tags for targeting
	ExcludeModules       []string `yaml:"exclude_modules"`   // opt out of inherited modules
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

// WaiverConfig holds compliance waivers loaded from waivers.vgo files.
// Keyed by compliance tag key (e.g., "cis-ubuntu", "pci-dss").
type WaiverConfig struct {
	Waivers map[string][]WaiverEntry `yaml:"waivers"`
	DirPath string                   `yaml:"-"`
}

// CommonConfig defines modules, roles, and vars inherited by all envoy
// entries in subdirectories. Loaded from common.vgo files at each directory
// level. Files without match: entries are treated as common configs.
type CommonConfig struct {
	Modules  []ModuleRef    `yaml:"modules"`
	Roles    []RoleRef      `yaml:"roles"`
	Vars     map[string]any `yaml:"vars"`
	VarsFrom []string       `yaml:"vars_from"`
	DirPath  string         `yaml:"-"` // relative dir path (e.g., "production/web")
}

// PerEnvOverrideBlock holds modules, vars, and exclusions for a single
// environment within a per_env_overrides.vgo file.
type PerEnvOverrideBlock struct {
	Modules        []ModuleRef    `yaml:"modules"`
	ExcludeModules []string       `yaml:"exclude_modules"`
	Vars           map[string]any `yaml:"vars"`
}

// PerEnvOverridesConfig holds the parsed content of a per_env_overrides.vgo file.
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
		case "vars_from":
			// Accept both scalar string and list of strings.
			if val.Kind == yaml.ScalarNode {
				n.VarsFrom = append(n.VarsFrom, val.Value)
			} else {
				var paths []string
				if err := val.Decode(&paths); err != nil {
					return fmt.Errorf("decoding vars_from: %w", err)
				}
				n.VarsFrom = append(n.VarsFrom, paths...)
			}
		case "vars":
			if err := val.Decode(&n.Vars); err != nil {
				return fmt.Errorf("decoding vars: %w", err)
			}
		case "environment_overrides":
			if err := val.Decode(&n.EnvironmentOverrides); err != nil {
				return fmt.Errorf("decoding environment_overrides: %w", err)
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
