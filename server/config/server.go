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

// LoadTLSSANs reads just the server.tls_sans list from server.yaml.
// Called before the full config load so EnsureTLSCerts can include extra SANs.
func LoadTLSSANs(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw struct {
		Server struct {
			TLSSANs []string `yaml:"tls_sans"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return raw.Server.TLSSANs
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

	if err := detectRetiredSpannerKey(data); err != nil {
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

// detectRetiredSpannerKey rejects server.yaml that still uses the top-level
// `spanner:` key. The federation subsystem was renamed to `tungol:` and the
// `spokes:` / `spoke_id:` sub-keys to `steorras:` / `steorra_id:` in the
// same cutover; the operator must rename the block in one pass. No
// translation, no shim — point at the migration target and refuse to start.
func detectRetiredSpannerKey(data []byte) error {
	var raw struct {
		Spanner any `yaml:"spanner"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	if raw.Spanner == nil {
		return nil
	}
	return fmt.Errorf("server.yaml uses retired `spanner:` key — rename the entire block to `tungol:` and rename the sub-keys `spokes:` → `steorras:` and `spoke_id:` → `steorra_id:` in one pass (no translation layer is provided)")
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
