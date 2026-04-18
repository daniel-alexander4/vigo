// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"strings"
	"testing"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/fleet"
)

// authorizeTestService wires the minimum state needed to exercise
// authorizeSeed: a FleetIndex with a single envoy, plus the SwarmSection
// allowlist and the server's own envoy hostname.
func authorizeTestService(envoyHostname string, seedingEnvoys []string, serverEnvoyHost string) (*AgentService, string) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:       "e1",
		Hostname: envoyHostname,
		IP:       "10.0.0.2",
	}, nil)
	svc := &AgentService{
		fleetIndex:      fi,
		swarmCfg:        config.SwarmSection{SeedingEnvoys: seedingEnvoys},
		serverEnvoyHost: serverEnvoyHost,
	}
	return svc, "e1"
}

func TestAuthorizeSeed_ExactHostnameMatch(t *testing.T) {
	svc, id := authorizeTestService("vault.ops.example.com", []string{"vault.ops.example.com"}, "")
	reason, ok := svc.authorizeSeed(id)
	if !ok {
		t.Fatalf("expected allow, got reject: %s", reason)
	}
}

func TestAuthorizeSeed_GlobMatch(t *testing.T) {
	svc, id := authorizeTestService("build-42.ci.example.com", []string{"build-*.ci.example.com"}, "")
	reason, ok := svc.authorizeSeed(id)
	if !ok {
		t.Fatalf("expected allow for glob match, got reject: %s", reason)
	}
}

func TestAuthorizeSeed_ServerEnvoyImplicit(t *testing.T) {
	// Empty allowlist, but the envoy's hostname equals server.hostname
	// — must be implicitly allowed.
	svc, id := authorizeTestService("vigo.example.com", nil, "vigo.example.com")
	reason, ok := svc.authorizeSeed(id)
	if !ok {
		t.Fatalf("expected server envoy to be implicitly allowed, got reject: %s", reason)
	}
}

func TestAuthorizeSeed_EmptyListRejectsNonServerEnvoy(t *testing.T) {
	svc, id := authorizeTestService("web-01.prod", nil, "vigo.example.com")
	reason, ok := svc.authorizeSeed(id)
	if ok {
		t.Fatalf("expected reject with empty allowlist and non-server envoy, got allow")
	}
	if !strings.Contains(reason, "not in swarm.seeding_envoys") {
		t.Errorf("reason should name the config field, got %q", reason)
	}
	if !strings.Contains(reason, "web-01.prod") {
		t.Errorf("reason should include the offending hostname, got %q", reason)
	}
}

func TestAuthorizeSeed_NoMatchRejects(t *testing.T) {
	svc, id := authorizeTestService("web-01.prod", []string{"build-*.ci.example.com"}, "vigo.example.com")
	reason, ok := svc.authorizeSeed(id)
	if ok {
		t.Fatalf("expected reject when hostname matches no pattern, got allow")
	}
	if reason == "" {
		t.Errorf("expected rejection reason, got empty string")
	}
}

func TestAuthorizeSeed_WildcardAllowsAll(t *testing.T) {
	// A bare "*" is explicitly permitted (the config loader logs a warn
	// at startup but the runtime still honors it). Verify the semantics.
	svc, id := authorizeTestService("any-envoy.anywhere", []string{"*"}, "")
	reason, ok := svc.authorizeSeed(id)
	if !ok {
		t.Fatalf("expected allow for bare * pattern, got reject: %s", reason)
	}
}

func TestAuthorizeSeed_UnknownEnvoyRejects(t *testing.T) {
	fi := fleet.NewFleetIndex()
	svc := &AgentService{
		fleetIndex:      fi,
		swarmCfg:        config.SwarmSection{SeedingEnvoys: []string{"*"}},
		serverEnvoyHost: "vigo.example.com",
	}
	reason, ok := svc.authorizeSeed("nonexistent")
	if ok {
		t.Fatalf("expected reject for unknown envoy, got allow")
	}
	if reason != "unknown envoy" {
		t.Errorf("expected 'unknown envoy' reason, got %q", reason)
	}
}
