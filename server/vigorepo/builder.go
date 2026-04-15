// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package vigorepo

import (
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/daniel-alexander4/vigo/server/conditionals"
	"github.com/daniel-alexander4/vigo/server/fleet"
)

// FleetQuerier is the subset of fleet.FleetIndex the builder needs.
// Defined as an interface so tests can plug in a fake and avoid
// spinning up the full fleet index.
type FleetQuerier interface {
	// AllEnvoys returns a snapshot of every envoy currently known.
	AllEnvoys() []fleet.EnvoyState
}

// Builder consumes `vigorepo` trait reports from the check-in path
// and drives [ClaimTable.Upsert] / [ClaimTable.Remove] for each
// reported source. It owns no state of its own beyond references to
// the ClaimTable and the fleet querier — all persistent state lives
// in the ClaimTable, which is rebuilt from scratch on server
// restart.
//
// Thread safety: Builder methods are safe for concurrent use from
// multiple gRPC handler goroutines. Internally the ClaimTable has
// its own mutex; the builder adds no cross-call state.
type Builder struct {
	// ct is the in-memory claim table driven by this builder.
	ct *ClaimTable
	// fleet provides the candidate pool for each upsert.
	fleet FleetQuerier
	// mu protects nothing today but is kept so callers depending
	// on Builder being a value type can safely embed it.
	mu sync.Mutex
}

// NewBuilder wires a ClaimTable to a fleet querier.
func NewBuilder(ct *ClaimTable, fq FleetQuerier) *Builder {
	return &Builder{ct: ct, fleet: fq}
}

// SourceFromTrait is one entry in a `vigorepo.sources[]` trait
// payload, cast from the raw JSON shape the trait collector emits.
// Field names mirror [state::SourceTraitEntry] in the agent.
type SourceFromTrait struct {
	Label         string
	Paused        bool
	MaxMirrors    int
	Tags          []string
	Description   string
	IncludeEnvoys []string
	ExcludeEnvoys []string
	When          string
}

// Result describes the outcome of a single `OnTraitReport` call.
type Result struct {
	// Upserted holds labels that were successfully upserted into
	// the ClaimTable.
	Upserted []string
	// Removed holds labels that were previously reported by this
	// envoy but are no longer in the current report — the builder
	// removed them from the ClaimTable.
	Removed []string
	// Errors holds per-label classification errors (invalid
	// label, seeder mismatch, etc.). An entry here does not stop
	// processing of other labels.
	Errors []error
}

// OnTraitReport processes a fresh vigorepo trait snapshot from an
// envoy. `reportingHostname` is the envoy's own hostname (from its
// fleet index entry, not from the trait JSON — we never trust the
// reporting envoy to self-identify). `sources` is the list of
// validated sources extracted from `vigorepo.sources[]`; if the
// trait is absent or the vigorepo section is missing, pass an empty
// slice — the builder will still remove any labels the envoy
// previously reported.
//
// Security boundary: every label's `<seeder>` segment must equal
// `reportingHostname`. A label whose seeder segment disagrees is
// rejected with an error and **not** stored. This prevents an
// envoy from claiming to seed a vigorepo source owned by another
// host in the fleet.
//
// The candidate pool for each source is built from the fleet
// querier, filtered to envoys whose `users.human.*` traits include
// the source's target user, excluding the seeder itself, revoked
// envoys, and envoys flagged offline by the fleet index.
func (b *Builder) OnTraitReport(envoyID, reportingHostname string, sources []SourceFromTrait) Result {
	var result Result
	reportingHostname = strings.ToLower(strings.TrimSpace(reportingHostname))

	// Build the set of labels in the new report. Used to remove
	// labels the envoy previously reported but no longer has.
	newLabels := make(map[string]bool, len(sources))
	for _, s := range sources {
		if s.Label != "" {
			newLabels[s.Label] = true
		}
	}

	// Remove any labels whose seeder is this envoy but which are
	// no longer present in the new report. This is how vanished
	// sources (operator deleted the `.vigorepo.yaml`) drain out of
	// the ClaimTable.
	//
	// Only labels whose SeederHost equals `reportingHostname` are
	// candidates for removal here — we must never touch labels
	// owned by other envoys.
	snap := b.ct.Snapshot()
	for label, state := range snap {
		if state.SeederHost == reportingHostname && !newLabels[label] {
			b.ct.Remove(label)
			result.Removed = append(result.Removed, label)
		}
	}

	if reportingHostname == "" {
		// Without a trusted hostname we can't enforce the seeder
		// match — accept the removes above (they're keyed on an
		// empty-string hostname, which the ClaimTable should never
		// contain anyway) but reject all upserts.
		for _, s := range sources {
			result.Errors = append(result.Errors, fmt.Errorf(
				"vigorepo: cannot upsert %q: reporting envoy has no hostname in fleet index",
				s.Label))
		}
		return result
	}

	// Upsert each reported source.
	for _, src := range sources {
		seeder, user, _, err := parseContentLabel(src.Label)
		if err != nil {
			result.Errors = append(result.Errors, err)
			continue
		}

		// Seeder-label security check.
		if seeder != reportingHostname {
			result.Errors = append(result.Errors, fmt.Errorf(
				"vigorepo: label %q claims seeder %q but reporting envoy is %q; rejected",
				src.Label, seeder, reportingHostname))
			continue
		}

		// Synthesize a Sidecar from the trait fields. The agent
		// has already validated the sidecar via the Rust strict
		// parser, so we trust the values here — but LoadSidecar's
		// clamp range and pattern/when caps are still re-applied
		// as defense in depth.
		if err := validatePatternList("include_envoys", src.IncludeEnvoys); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("vigorepo: %q: %w", src.Label, err))
			continue
		}
		if err := validatePatternList("exclude_envoys", src.ExcludeEnvoys); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("vigorepo: %q: %w", src.Label, err))
			continue
		}
		if len(src.When) > MaxWhenExprBytes {
			result.Errors = append(result.Errors, fmt.Errorf(
				"vigorepo: %q: when expression exceeds %d byte limit", src.Label, MaxWhenExprBytes))
			continue
		}
		sc := &Sidecar{
			Owner:         seeder,
			MaxMirrors:    clampMirrors(src.MaxMirrors),
			Tags:          src.Tags,
			Paused:        src.Paused,
			Description:   src.Description,
			IncludeEnvoys: src.IncludeEnvoys,
			ExcludeEnvoys: src.ExcludeEnvoys,
			When:          src.When,
		}

		candidates, err := b.buildCandidatePool(sc, seeder, user)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf(
				"vigorepo: %q candidate filter: %w", src.Label, err))
			continue
		}
		if _, err := b.ct.Upsert(src.Label, sc, candidates); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf(
				"vigorepo: upsert %q: %w", src.Label, err))
			continue
		}
		result.Upserted = append(result.Upserted, src.Label)
	}

	_ = envoyID // reserved for future use (per-envoy telemetry, rate limiting, etc.)
	return result
}

// Rebuild triggers a full re-run of selection for every source
// currently in the ClaimTable using the current fleet state. Call
// this from the reaper / fleet-change hook when an envoy is added,
// removed, or changes its `users` trait — those events may change
// the candidate pool for sources the envoy didn't itself seed.
//
// Implementation is a simple walk-and-upsert; the ClaimTable's own
// lock handles concurrent access. Runs O(sources * fleet) work, so
// callers should coalesce fleet-change events before invoking.
func (b *Builder) Rebuild() {
	snap := b.ct.Snapshot()
	for label, state := range snap {
		if state.Sidecar == nil {
			continue
		}
		candidates, err := b.buildCandidatePool(state.Sidecar, state.SeederHost, state.TargetUser)
		if err != nil {
			// Rebuild must not fail the loop on a single bad
			// when-expression — log the skip and continue so
			// other sources still pick up fleet changes.
			continue
		}
		_, _ = b.ct.Upsert(label, state.Sidecar, candidates)
	}
}

// buildCandidatePool returns the set of envoys eligible to mirror a
// source. Filters applied, in order:
//
//  1. Exclude the seeder itself (vigorepo mirrors != self-seeding).
//  2. Exclude revoked envoys.
//  3. Exclude envoys currently flagged Offline by the fleet index
//     (if the fleet considers an envoy dead, we shouldn't hand it
//     new mirror work).
//  4. Keep only envoys whose flattened `users.human.*` traits
//     include the target user.
//  5. Sidecar `include_envoys` whitelist — when non-empty, keep
//     only envoys whose hostname matches at least one glob pattern.
//  6. Sidecar `exclude_envoys` blacklist — drop envoys whose
//     hostname matches any glob pattern.
//  7. Sidecar `when` expression — parsed once per call, evaluated
//     per candidate with the vigorepo builtin set (`tag`, `trait_eq`).
//
// Returns an error only when the sidecar's when expression fails to
// parse. Individual per-candidate evaluation errors are treated as
// "candidate fails the filter" so a single bad trait value can't
// bring down the whole source's selection.
func (b *Builder) buildCandidatePool(sc *Sidecar, seederHost, targetUser string) ([]Candidate, error) {
	if b.fleet == nil {
		return nil, nil
	}
	seederHost = strings.ToLower(seederHost)

	// Pre-parse the when expression once for this source. An empty
	// expression short-circuits the evaluator entirely.
	var whenExpr conditionals.WhenExpr
	if sc != nil && sc.When != "" {
		parsed, err := conditionals.ParseWhenExpr(sc.When)
		if err != nil {
			return nil, fmt.Errorf("when: %w", err)
		}
		whenExpr = parsed
	}

	all := b.fleet.AllEnvoys()
	out := make([]Candidate, 0, len(all))
	for _, env := range all {
		if strings.ToLower(env.Hostname) == seederHost {
			continue
		}
		if env.Revoked || env.Offline {
			continue
		}
		if !envoyHasHumanUser(env.Traits, targetUser) {
			continue
		}
		if sc != nil {
			if !matchInclude(sc.IncludeEnvoys, env.Hostname) {
				continue
			}
			if matchExclude(sc.ExcludeEnvoys, env.Hostname) {
				continue
			}
			if whenExpr != nil && !evalEnvoyWhen(whenExpr, env) {
				continue
			}
		}
		out = append(out, Candidate{EnvoyID: env.ID, Hostname: env.Hostname})
	}
	return out, nil
}

// matchInclude returns true if an envoy with the given hostname passes
// the sidecar's include_envoys whitelist. An empty list is a pass-all.
// Patterns are shell-style globs matched via path.Match; a malformed
// pattern is treated as "no match" so an invalid entry can't
// accidentally whitelist everything.
func matchInclude(patterns []string, hostname string) bool {
	if len(patterns) == 0 {
		return true
	}
	hn := strings.ToLower(hostname)
	for _, pat := range patterns {
		if matchPattern(pat, hn) {
			return true
		}
	}
	return false
}

// matchExclude returns true if the hostname matches any blacklist
// pattern. An empty list always returns false (no exclusions).
func matchExclude(patterns []string, hostname string) bool {
	if len(patterns) == 0 {
		return false
	}
	hn := strings.ToLower(hostname)
	for _, pat := range patterns {
		if matchPattern(pat, hn) {
			return true
		}
	}
	return false
}

// matchPattern runs a single glob match with case-insensitive
// semantics. The pattern is lowercased by the caller before calling
// (via matchInclude/matchExclude). Malformed globs return false so
// they can't accidentally match everything — this matches the
// defensive posture of the rest of the vigorepo validation pipeline.
func matchPattern(pattern, hostname string) bool {
	ok, err := path.Match(strings.ToLower(pattern), hostname)
	if err != nil {
		return false
	}
	return ok
}

// evalEnvoyWhen evaluates a parsed when expression against a single
// envoy. The builtin set is intentionally minimal:
//
//   - `tag('name')` — true if the envoy's Tags slice contains `name`
//   - `trait_eq('key', 'value')` — true if `env.Traits[key] == value`
//
// Bare variable references and other functions return false. Any
// evaluation error also returns false — vigorepo prefers "exclude on
// ambiguity" over "include on ambiguity" for personal-DR safety.
func evalEnvoyWhen(expr conditionals.WhenExpr, env fleet.EnvoyState) bool {
	resolve := func(_ string) bool { return false }
	funcs := func(name string, args []string) (bool, error) {
		switch name {
		case "tag":
			if len(args) != 1 {
				return false, nil
			}
			for _, t := range env.Tags {
				if t == args[0] {
					return true, nil
				}
			}
			return false, nil
		case "trait_eq":
			if len(args) != 2 {
				return false, nil
			}
			v, ok := env.Traits[args[0]]
			if !ok {
				return false, nil
			}
			return v == args[1], nil
		default:
			return false, nil
		}
	}
	ok, err := conditionals.EvalWhenExprWithFuncs(expr, resolve, funcs)
	if err != nil {
		return false
	}
	return ok
}

// envoyHasHumanUser returns true if the flattened trait map
// contains any `users.human.<index>` entry whose value equals
// `user`. The `users` trait is emitted by
// `agent/src/traits/users.rs` as
// `{ "human": [...], "os": [...], "thirdparty": [...] }`, which
// fleet.FlattenJSON turns into `users.human.0`, `users.human.1`,
// etc.
func envoyHasHumanUser(traits map[string]string, user string) bool {
	if len(traits) == 0 || user == "" {
		return false
	}
	prefix := "users.human."
	for k, v := range traits {
		if strings.HasPrefix(k, prefix) && v == user {
			return true
		}
	}
	return false
}

// clampMirrors mirrors the Rust clamp logic as defense in depth.
// Values returned from LoadSidecar are always already in range,
// but trait-reported values are trusted data from an agent — if a
// maliciously crafted report claims `max_mirrors=9999`, clamp it
// here rather than allow an oversize selection to succeed.
func clampMirrors(n int) int {
	if n < MinMirrors {
		return MinMirrors
	}
	if n > MaxMirrors {
		return MaxMirrors
	}
	return n
}

// ExtractSourcesFromTraits walks a parsed `traits` map (as produced
// by `json.Unmarshal(traitsJSON, &parsed)` in the ReportTraits
// handler) and returns the `vigorepo.sources[]` entries as typed
// [SourceFromTrait] values. Missing / malformed shapes are
// tolerated — the function returns whatever cleanly parsed entries
// it found, and a count of entries it had to skip.
//
// This is a free function (not a method on Builder) so callers in
// `server/grpc` can extract the slice without holding a builder
// reference, and so it's independently testable.
func ExtractSourcesFromTraits(traits map[string]any) (sources []SourceFromTrait, skipped int) {
	raw, ok := traits["vigorepo"].(map[string]any)
	if !ok {
		return nil, 0
	}
	rawSources, ok := raw["sources"].([]any)
	if !ok {
		return nil, 0
	}
	for _, entry := range rawSources {
		m, ok := entry.(map[string]any)
		if !ok {
			skipped++
			continue
		}
		label, _ := m["label"].(string)
		if label == "" {
			skipped++
			continue
		}

		var src SourceFromTrait
		src.Label = label
		src.Paused, _ = m["paused"].(bool)
		src.Description, _ = m["description"].(string)
		src.When, _ = m["when"].(string)

		// max_mirrors arrives as json.Number-or-float64 depending
		// on decoder settings; the default decoder gives float64.
		if v, ok := m["max_mirrors"].(float64); ok {
			src.MaxMirrors = int(v)
		}

		if tagList, ok := m["tags"].([]any); ok {
			src.Tags = make([]string, 0, len(tagList))
			for _, t := range tagList {
				if s, ok := t.(string); ok {
					src.Tags = append(src.Tags, s)
				}
			}
		}
		if list, ok := m["include_envoys"].([]any); ok {
			src.IncludeEnvoys = make([]string, 0, len(list))
			for _, v := range list {
				if s, ok := v.(string); ok {
					src.IncludeEnvoys = append(src.IncludeEnvoys, s)
				}
			}
		}
		if list, ok := m["exclude_envoys"].([]any); ok {
			src.ExcludeEnvoys = make([]string, 0, len(list))
			for _, v := range list {
				if s, ok := v.(string); ok {
					src.ExcludeEnvoys = append(src.ExcludeEnvoys, s)
				}
			}
		}
		sources = append(sources, src)
	}
	return sources, skipped
}
