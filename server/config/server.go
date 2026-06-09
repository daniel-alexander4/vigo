// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"git.alexander4.io/vigo/server/secrets"
	"gopkg.in/yaml.v3"
)

// LoadSecretsConfig reads just the secrets section from server.yaml.
// This is called before the secrets provider exists, so no secret: resolution
// is performed. Returns the secrets.Config needed to create a provider.
func LoadSecretsConfig(path string) (secrets.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return secrets.Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}

	var raw struct {
		Secrets secrets.Config `yaml:"secrets"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return secrets.Config{}, fmt.Errorf("parsing config: %w", err)
	}

	return raw.Secrets, nil
}

// LoadServerCertSANs reads the Subject Alternative Names the auto-generated
// server cert must carry from server.yaml: the configured server.hostname
// (the address agents are told to dial — GET /bootstrap advertises it) plus
// any server.tls_sans extras. Called before the full config load so
// EnsureTLSCerts can apply them. Returns nil on any read/parse error — cert
// generation falls back to its built-in localhost + interface-IP SANs.
func LoadServerCertSANs(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw struct {
		Server struct {
			Hostname string   `yaml:"hostname"`
			TLSSANs  []string `yaml:"tls_sans"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	sans := raw.Server.TLSSANs
	// localhost is already a built-in SAN; os.Hostname() is added by
	// buildExpectedSANs. Only the explicitly configured hostname needs
	// supplying here.
	if h := raw.Server.Hostname; h != "" && h != "localhost" {
		sans = append(sans, h)
	}
	return sans
}

// LoadServerConfig reads server.yaml from the given path and resolves any
// secret: prefixed values using the provided secrets provider.
// The backend name is used for diagnostic hints on failure.
func LoadServerConfig(path string, provider secrets.Provider, backend string) (*ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading server config %s: %w", path, err)
	}

	if err := detectRetiredSwarmShape(data); err != nil {
		return nil, err
	}

	if err := detectRetiredTungolKey(data); err != nil {
		return nil, err
	}

	if err := detectRetiredSpannerKeys(data); err != nil {
		return nil, err
	}

	if err := detectUnsupportedDriver(data); err != nil {
		return nil, err
	}

	// Defaults that diverge from Go zero-values land here before unmarshal.
	// YAML keys that explicitly appear overwrite these; absent keys keep the
	// pre-set value. (The `default:` struct tags in types.go are doc-only —
	// the schema generator reads them, the loader does not.)
	cfg := ServerConfig{
		Risk: RiskSection{NVD: NVDConfig{Enabled: true}},
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing server config: %w", err)
	}

	// license.mode: aggregate sums the fleet-wide roster across spanner bolts;
	// it is meaningless on a standalone server (no peers to aggregate) and
	// would silently fall back to local counting. Fail fast rather than mislead.
	if cfg.License.Mode == "aggregate" && cfg.Spanner.Mode != ModeSpanner {
		return nil, fmt.Errorf("license.mode: aggregate requires spanner.mode: spanner — aggregate counting sums the fleet-wide node count across spanner bolts and has no effect on a standalone server; set spanner.mode: spanner, or use license.mode: local")
	}

	// Resolve secret: prefixes in config values.
	if err := resolveServerSecrets(&cfg, provider, backend); err != nil {
		return nil, fmt.Errorf("resolving server config secrets: %w", err)
	}

	// Validate swarm pattern-list ordering and warn (never error) on
	// shadowed deny entries — the silent-fail case `["*", "-rasp1"]`
	// plus glob-shadow `["a*", "-annlap"]`. Findings are also surfaced
	// via the doctor endpoint; logging here catches them at startup
	// and on every reload.
	for _, f := range ValidateSwarmPatternOrder(cfg.Swarm) {
		slog.Warn("swarm: shadowed deny pattern in server.yaml", "finding", f.String())
	}

	return &cfg, nil
}

// detectRetiredSwarmShape inspects raw server.yaml for swarm fields that
// were valid in earlier versions and now produce a hard error. Catches:
//
//   - swarm.cryptsync.* — retired (renamed to swarm.lockbox.* in this
//     release; authorization still derives from swarm.puddle.enabled
//     per ADR-020).
//   - swarm.gitmirror.* — retired (renamed to swarm.gitback.* in
//     this release).
//   - swarm.<sub>.blacklist — retired across all sub-subsections in
//     0.41.0 (subsumed by negative patterns in the new pattern-list shape).
//   - swarm.enabled / swarm.<sub>.enabled as a bool — retired in 0.41.0
//     (now a pattern list of hostname globs).
//   - swarm.payload.* — retired in the filecast rename; the same
//     authorization lives at swarm.filecast.* now.
//   - swarm.filesync.* — retired in the longdrawer rename; the same
//     authorization lives at swarm.longdrawer.* now.
//
// Without this, yaml.v3 would silently drop unknown fields and surface
// shape mismatches as opaque type errors. This pre-parse pass produces a
// single error pointing at the migration doc so the operator can fix
// everything in one pass.
func detectRetiredSwarmShape(data []byte) error {
	var raw struct {
		Swarm struct {
			Enabled   any `yaml:"enabled"`
			Cryptsync any `yaml:"cryptsync"`
			Puddle    struct {
				Enabled any `yaml:"enabled"`
			} `yaml:"puddle"`
			Gitmirror any `yaml:"gitmirror"`
			Filesync  any `yaml:"filesync"`
			Payload   any `yaml:"payload"`
		} `yaml:"swarm"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// Permissive parse can't fail on its own — if it does, fall through
		// to the typed parse and let that surface the underlying issue.
		return nil
	}
	var retired []string
	if isBoolScalar(raw.Swarm.Enabled) {
		retired = append(retired, "swarm.enabled is a bool but must be a pattern list (e.g. [\"*\"] for fleet-wide on, [] for off)")
	}
	if raw.Swarm.Cryptsync != nil {
		retired = append(retired, "swarm.cryptsync.* — retired (renamed to swarm.lockbox.* in this release; authorization still derives from swarm.puddle.enabled per ADR-020)")
	}
	if isBoolScalar(raw.Swarm.Puddle.Enabled) {
		retired = append(retired, "swarm.puddle.enabled is a bool but must be a pattern list")
	}
	if raw.Swarm.Gitmirror != nil {
		retired = append(retired, "swarm.gitmirror.* — retired in the gitback rename; move the entire section to swarm.gitback.* (same fields)")
	}
	if raw.Swarm.Filesync != nil {
		retired = append(retired, "swarm.filesync.* — retired in the longdrawer rename; move the entire section to swarm.longdrawer.* (same fields)")
	}
	if raw.Swarm.Payload != nil {
		retired = append(retired, "swarm.payload.* — retired in the filecast rename; move the entire section to swarm.filecast.* (same fields)")
	}
	if len(retired) == 0 {
		return nil
	}
	return fmt.Errorf("server.yaml uses retired swarm shape:\n  - %s\n\nsee docs/reference/server-yaml.md#0.41.0-migration",
		strings.Join(retired, "\n  - "))
}

// isBoolScalar reports whether v is a YAML scalar that decoded to a bool.
// Used by detectRetiredSwarmShape to distinguish the retired bool form
// from the new pattern-list form (a YAML sequence decodes to []any).
func isBoolScalar(v any) bool {
	_, ok := v.(bool)
	return ok
}

// detectRetiredTungolKey rejects server.yaml that still uses the top-level
// `tungol:` key. The federation subsystem was renamed back to `spanner:` and
// the `steorras:` / `steorra_id:` sub-keys to `bolts:` / `bolt_id:` in the
// same cutover; the operator must rename the block in one pass. No
// translation, no shim — point at the migration target and refuse to start.
func detectRetiredTungolKey(data []byte) error {
	var raw struct {
		Tungol any `yaml:"tungol"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	if raw.Tungol == nil {
		return nil
	}
	return fmt.Errorf("server.yaml uses retired `tungol:` key — rename the entire block to `spanner:` and rename the sub-keys `steorras:` → `bolts:` and `steorra_id:` → `bolt_id:` in one pass (no translation layer is provided)")
}

// detectRetiredSpannerKeys rejects server.yaml that still uses the
// pre-Phase-2 hub/spoke field shape inside spanner:. The peer-equal
// admissions model in ADR-026 retired role/bolt_id/hub_addr/
// hub_fallback_addrs/bolts/auto_failover and replaced them with
// mode/patterns/transport/snapshot/freshness/fallback_admin. No
// translation layer — point at each retired key's replacement and
// refuse to start.
func detectRetiredSpannerKeys(data []byte) error {
	var raw struct {
		Spanner struct {
			Role             any `yaml:"role"`
			BoltID           any `yaml:"bolt_id"`
			HubAddr          any `yaml:"hub_addr"`
			HubFallbackAddrs any `yaml:"hub_fallback_addrs"`
			Bolts            any `yaml:"bolts"`
			AutoFailover     any `yaml:"auto_failover"`
			Transport        struct {
				BootstrapPeers any `yaml:"bootstrap_peers"`
			} `yaml:"transport"`
		} `yaml:"spanner"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	var retired []string
	if raw.Spanner.Role != nil {
		retired = append(retired, "spanner.role — replaced by spanner.mode (values: standalone | spanner)")
	}
	if raw.Spanner.BoltID != nil {
		retired = append(retired, "spanner.bolt_id — retired; bolts are identified by their Ed25519 pubkey (server/bolt/), not a YAML-configured string")
	}
	if raw.Spanner.HubAddr != nil {
		retired = append(retired, "spanner.hub_addr — retired; the hub is gone. Join a spanner with the `vigocli spanner join` admission ceremony — the CRDT-replicated roster is the peer list")
	}
	if raw.Spanner.HubFallbackAddrs != nil {
		retired = append(retired, "spanner.hub_fallback_addrs — retired; peer discovery is gossip-driven. Join a spanner with `vigocli spanner join`")
	}
	if raw.Spanner.Bolts != nil {
		retired = append(retired, "spanner.bolts — retired; per-bolt patterns + identity live in the gossiped admissions CRDT, not in server.yaml. Each vigosrv declares its own patterns via spanner.patterns")
	}
	if raw.Spanner.AutoFailover != nil {
		retired = append(retired, "spanner.auto_failover — retired; failover is implicit in CRDT gossip + freshness thresholds (spanner.freshness.stale_after)")
	}
	if raw.Spanner.Transport.BootstrapPeers != nil {
		retired = append(retired, "spanner.transport.bootstrap_peers — retired; peer discovery is gossip-driven. A joining bolt learns the roster through `vigocli spanner join`, and the CRDT-replicated roster is the peer list from then on")
	}
	if len(retired) == 0 {
		return nil
	}
	return fmt.Errorf("server.yaml uses retired spanner shape (per ADR-026 Phase 2 peer-equal admissions):\n  - %s",
		strings.Join(retired, "\n  - "))
}

// detectUnsupportedDriver rejects server.yaml that selects the postgres
// database driver. The postgres driver path exists in the codebase, but its
// migration set is incomplete — migrations/postgres/ cannot build the current
// schema — so a postgres server would start against missing tables. Fail fast
// at config load with a pointer to the supported driver rather than ship that.
func detectUnsupportedDriver(data []byte) error {
	var raw struct {
		Database struct {
			Driver string `yaml:"driver"`
		} `yaml:"database"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	if raw.Database.Driver == "postgres" {
		return fmt.Errorf("database.driver: postgres is not supported in this release — the Postgres migration set is incomplete. Use the default SQLite driver: remove the database.driver line, or set it to \"sqlite\"")
	}
	return nil
}

// resolveServerSecrets resolves secret: prefixed values in the server config.
// Collects all failures so the operator can fix everything in one restart.
func resolveServerSecrets(cfg *ServerConfig, provider secrets.Provider, backend string) error {
	type field struct {
		ptr  *string
		name string
	}
	fields := []field{
		{&cfg.Database.DSN, "database.dsn"},
		{&cfg.Auth.OIDC.ClientSecret, "auth.oidc.client_secret"},
		{&cfg.AI.APIKey, "ai.api_key"},
		{&cfg.SMTP.Password, "smtp.password"},
		{&cfg.Backup.AccessKeyID, "backup.access_key_id"},
		{&cfg.Backup.SecretAccessKey, "backup.secret_access_key"},
	}

	var errs []*SecretError
	for _, f := range fields {
		if strings.HasPrefix(*f.ptr, secretPrefix) {
			key := strings.TrimPrefix(*f.ptr, secretPrefix)
			val, err := provider.Get(context.Background(), key)
			if err != nil {
				errs = append(errs, &SecretError{
					Match:  "server.yaml",
					Attr:   f.name,
					Secret: key,
					Err:    err,
				})
				continue
			}
			*f.ptr = val
		}
	}
	if len(errs) > 0 {
		return &SecretErrors{Errors: errs, Backend: backend}
	}

	return nil
}
