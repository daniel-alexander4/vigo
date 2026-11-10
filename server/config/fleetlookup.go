// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"git.alexander4.io/vigo/server/conditionals"
)

// FleetLookup is the cross-node config construct: a top-level var value of the
// form
//
//	myvar:
//	  fleet:
//	    where: "tag('web') && os_family('linux')"   # selector over each envoy's traits/tags
//	    select: "server {{ .IP }}:80;"               # rendered once per matched envoy
//	    join: "\n"                                   # glue the rendered strings (omit → a list)
//	    include_stale: false                         # default: drop offline envoys (server/freshness)
//
// It resolves at check-in to the select:-rendered, freshness-filtered, sorted,
// join:-ed result for every envoy matching where:. The where: language is the
// same as when:, plus tag('<glob>') and host('<glob>') builtins; a fleet
// selector must be fully server-evaluable, so agent-only builtins (file_exists,
// has_command, …) are rejected at config load.
type FleetLookup struct {
	Where        string `yaml:"where"`
	Select       string `yaml:"select"`
	Join         string `yaml:"join,omitempty"`
	IncludeStale bool   `yaml:"include_stale,omitempty"`

	whereExpr  conditionals.WhenExpr `yaml:"-" json:"-"` // parsed Where, set at config load
	selectTmpl *template.Template    `yaml:"-" json:"-"` // parsed Select, set at config load
}

// MarshalJSON renders a FleetLookup back as {"fleet": {...}} so config trace
// shows it the way the operator wrote it.
func (f *FleetLookup) MarshalJSON() ([]byte, error) {
	inner := map[string]any{"where": f.Where, "select": f.Select}
	if f.Join != "" {
		inner["join"] = f.Join
	}
	if f.IncludeStale {
		inner["include_stale"] = true
	}
	return json.Marshal(map[string]any{"fleet": inner})
}

// FleetMember is the per-envoy view a fleet: lookup resolves against. The
// caller (the gRPC layer) builds these from server/fleet.FleetIndex —
// server/config can't import server/fleet because fleet imports config.
type FleetMember struct {
	Hostname string
	IP       string
	Tags     []string
	Traits   map[string]string // flat dotted keys, e.g. "os.distro"
	LastSeen time.Time         // last check-in (for the isStale callback)
}

// fleetSelectContext is the data passed to a fleet: select: template.
type fleetSelectContext struct {
	Hostname string
	IP       string
	Tags     []string
	Traits   map[string]string // flat keys — use {{ index .Traits "os.distro" }} for dotted keys
}

var fleetSelectorFuncs = map[string]bool{
	"os_family": true, "distro": true, "os": true, "arch": true,
	"version_ge": true, "has_display": true,
	"tag": true, "host": true, "trait": true,
}

// --- TTL-memoized resolution -------------------------------------------------

const defaultFleetLookupTTL = 30 * time.Second

type fleetMemoEntry struct {
	value      any // string (joined) or []any (list)
	computedAt time.Time
}

// SetFleetLookupTTL configures how long a resolved fleet: lookup is reused
// before recomputing (server.yaml: checkin.fleet_lookup_ttl). Non-positive
// values reset to the default (30s). Called once at startup, per ConfigStore.
func (cs *ConfigStore) SetFleetLookupTTL(d time.Duration) {
	if d <= 0 {
		d = defaultFleetLookupTTL
	}
	cs.fleetLookupTTL.Store(int64(d))
}

func (cs *ConfigStore) fleetLookupTTLValue() time.Duration {
	if v := cs.fleetLookupTTL.Load(); v > 0 {
		return time.Duration(v)
	}
	return defaultFleetLookupTTL
}

// --- detection & parsing (config load) --------------------------------------

// asFleetLookupConstruct reports whether v is a {fleet: {where:, select:, …}}
// var value, returning the inner map. The where:/select: requirement keeps a
// var that merely happens to have a "fleet" key from being mistaken for one.
func asFleetLookupConstruct(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return nil, false
	}
	inner, ok := m["fleet"].(map[string]any)
	if !ok {
		return nil, false
	}
	if _, hasWhere := inner["where"]; !hasWhere {
		return nil, false
	}
	if _, hasSelect := inner["select"]; !hasSelect {
		return nil, false
	}
	return inner, true
}

// ParseFleetLookup builds a *FleetLookup from the inner {where:, select:, …}
// map, parsing and trial-running the selector and the select: template so a
// malformed fleet: construct fails at config publish, not at the first check-in.
func ParseFleetLookup(varName string, inner map[string]any) (*FleetLookup, error) {
	fl := &FleetLookup{}
	for k, v := range inner {
		switch k {
		case "where":
			s, ok := v.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("var %q: fleet: where: must be a non-empty string", varName)
			}
			fl.Where = s
		case "select":
			s, ok := v.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("var %q: fleet: select: must be a non-empty string", varName)
			}
			fl.Select = s
		case "join":
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("var %q: fleet: join: must be a string", varName)
			}
			fl.Join = s
		case "include_stale":
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("var %q: fleet: include_stale: must be true or false", varName)
			}
			fl.IncludeStale = b
		default:
			return nil, fmt.Errorf("var %q: fleet: unknown key %q (allowed: where, select, join, include_stale)", varName, k)
		}
	}

	expr, err := conditionals.ParseWhenExpr(fl.Where)
	if err != nil {
		return nil, fmt.Errorf("var %q: fleet: where: %w", varName, err)
	}
	for _, fn := range fleetSelectorFuncNames(expr) {
		if !fleetSelectorFuncs[fn] {
			return nil, fmt.Errorf("var %q: fleet: where: %q is not a valid selector function — use os_family, distro, os, arch, version_ge, has_display, tag, host, or trait", varName, fn)
		}
	}
	fl.whereExpr = expr

	tmpl, err := template.New("fleet-select").Parse(fl.Select)
	if err != nil {
		return nil, fmt.Errorf("var %q: fleet: select: %w", varName, err)
	}
	if err := tmpl.Execute(io.Discard, fleetSelectContext{Traits: map[string]string{}}); err != nil {
		return nil, fmt.Errorf("var %q: fleet: select: %w", varName, err)
	}
	fl.selectTmpl = tmpl
	return fl, nil
}

func fleetSelectorFuncNames(expr conditionals.WhenExpr) []string {
	var out []string
	var walk func(conditionals.WhenExpr)
	walk = func(e conditionals.WhenExpr) {
		switch n := e.(type) {
		case *conditionals.FuncCallExpr:
			out = append(out, n.Name)
		case *conditionals.NotWhenExpr:
			walk(n.Operand)
		case *conditionals.AndWhenExpr:
			walk(n.Left)
			walk(n.Right)
		case *conditionals.OrWhenExpr:
			walk(n.Left)
			walk(n.Right)
		case *conditionals.EqExpr:
			walk(n.Left)
			walk(n.Right)
		case *conditionals.NeqExpr:
			walk(n.Left)
			walk(n.Right)
		}
	}
	walk(expr)
	return out
}

// --- resolution (check-in) ---------------------------------------------------

// ResolveFleetLookupsInConfig replaces each top-level *FleetLookup var in cfg
// with its resolved value — a join:-ed string, or a []any list if no join:.
// members() is invoked at most once per call, and only when at least one lookup
// is missing from the memo or past the TTL; isStale(m) reports whether an envoy
// should be dropped (nil = keep all). MUST be called on a Clone()'d config —
// it mutates cfg.Vars. The memo is per-ConfigStore so two stores in one
// process (test harness, multi-vigosrv lab) don't share resolution state.
func (cs *ConfigStore) ResolveFleetLookupsInConfig(cfg *EnvoyConfig, members func() []FleetMember, isStale func(FleetMember) bool, now time.Time) error {
	if cfg == nil || !cfg.HasFleetLookups {
		return nil
	}
	ttl := cs.fleetLookupTTLValue()
	var snap []FleetMember
	haveSnap := false

	for name, v := range cfg.Vars {
		fl, ok := v.(*FleetLookup)
		if !ok {
			continue
		}
		key := fl.Where + "\x00" + fl.Select + "\x00" + fl.Join + "\x00" + strconv.FormatBool(fl.IncludeStale)

		cs.fleetMemoMu.Lock()
		ent, hit := cs.fleetMemo[key]
		cs.fleetMemoMu.Unlock()
		if hit && now.Sub(ent.computedAt) < ttl {
			cfg.Vars[name] = ent.value
			continue
		}

		if !haveSnap {
			if members != nil {
				snap = members()
			}
			haveSnap = true
		}
		val, err := resolveFleetLookup(fl, snap, isStale)
		if err != nil {
			return fmt.Errorf("resolving fleet: var %q: %w", name, err)
		}

		cs.fleetMemoMu.Lock()
		cs.fleetMemo[key] = fleetMemoEntry{value: val, computedAt: now}
		cs.fleetMemoMu.Unlock()
		cfg.Vars[name] = val
	}
	return nil
}

func resolveFleetLookup(fl *FleetLookup, members []FleetMember, isStale func(FleetMember) bool) (any, error) {
	var rendered []string
	for _, m := range members {
		if !fl.IncludeStale && isStale != nil && isStale(m) {
			continue
		}
		ok, err := evalFleetSelector(fl.whereExpr, m)
		if err != nil {
			// A per-envoy eval error (e.g. a comparison against a trait this
			// envoy hasn't reported) excludes that envoy, not the whole lookup —
			// the selector's structural validity was checked at config load.
			continue
		}
		if !ok {
			continue
		}
		var buf bytes.Buffer
		if err := fl.selectTmpl.Execute(&buf, fleetSelectContext{Hostname: m.Hostname, IP: m.IP, Tags: m.Tags, Traits: m.Traits}); err != nil {
			return nil, fmt.Errorf("rendering select: for envoy %q: %w", m.Hostname, err)
		}
		rendered = append(rendered, buf.String())
	}
	sort.Strings(rendered) // stable output — no needless config churn from map order
	if fl.Join == "" {
		out := make([]any, len(rendered))
		for i, s := range rendered {
			out[i] = s
		}
		return out, nil
	}
	return strings.Join(rendered, fl.Join), nil
}

func evalFleetSelector(expr conditionals.WhenExpr, m FleetMember) (bool, error) {
	traitsAny := make(map[string]any, len(m.Traits))
	for k, v := range m.Traits {
		traitsAny[k] = v
	}
	sub := conditionals.SubstituteVars(expr, traitsAny)
	return conditionals.EvalWhenExprWithFuncs(sub,
		func(name string) bool { return conditionals.WhenVarIsTrue(name, traitsAny) },
		func(fn string, args []string) (bool, error) { return evalFleetBuiltin(fn, args, m, traitsAny) },
	)
}

func evalFleetBuiltin(fn string, args []string, m FleetMember, traitsAny map[string]any) (bool, error) {
	arg := ""
	if len(args) > 0 {
		arg = args[0]
	}
	switch fn {
	case "os_family", "distro", "os", "arch", "version_ge", "has_display":
		return evalTraitFunc(fn, arg, traitsAny), nil
	case "trait":
		// trait('key', 'value') — equality on an arbitrary (possibly dotted)
		// trait key, since the when: lexer won't accept dots in bare identifiers.
		val := ""
		if len(args) > 1 {
			val = args[1]
		}
		return strings.EqualFold(strings.TrimSpace(m.Traits[arg]), val), nil
	case "tag":
		for _, t := range m.Tags {
			if matched, _ := filepath.Match(arg, t); matched {
				return true, nil
			}
		}
		return false, nil
	case "host":
		matched, _ := filepath.Match(arg, m.Hostname)
		return matched, nil
	default:
		return false, fmt.Errorf("fleet selector: %q is not a valid function — use os_family, distro, os, arch, version_ge, has_display, tag, host, or trait", fn)
	}
}
