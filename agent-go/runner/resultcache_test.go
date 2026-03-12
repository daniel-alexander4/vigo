// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package runner

import (
	"errors"
	"testing"

	"github.com/hobnob/hobnob/agent/modules"
)

func TestResultCache_HitOnNoOp(t *testing.T) {
	c := NewResultCache(5)
	params := map[string]string{"path": "/etc/app.conf", "content": "hello"}
	result := modules.ResourceResult{Action: modules.ActionNone, Changed: false}

	c.Store("config-file", "file", params, result)

	cached, ok := c.Lookup("config-file", "file", params)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if cached.Action != modules.ActionNone {
		t.Errorf("action = %q, want %q", cached.Action, modules.ActionNone)
	}
}

func TestResultCache_MissOnParamsChange(t *testing.T) {
	c := NewResultCache(5)
	params1 := map[string]string{"path": "/etc/app.conf", "content": "hello"}
	params2 := map[string]string{"path": "/etc/app.conf", "content": "world"}
	result := modules.ResourceResult{Action: modules.ActionNone, Changed: false}

	c.Store("config-file", "file", params1, result)

	_, ok := c.Lookup("config-file", "file", params2)
	if ok {
		t.Error("expected cache miss on different params")
	}
}

func TestResultCache_MissOnChanged(t *testing.T) {
	c := NewResultCache(5)
	params := map[string]string{"path": "/etc/app.conf"}
	result := modules.ResourceResult{Action: modules.ActionUpdated, Changed: true}

	c.Store("config-file", "file", params, result)

	_, ok := c.Lookup("config-file", "file", params)
	if ok {
		t.Error("expected cache miss for changed result")
	}
}

func TestResultCache_MissOnError(t *testing.T) {
	c := NewResultCache(5)
	params := map[string]string{"path": "/etc/app.conf"}
	result := modules.ResourceResult{Action: modules.ActionNone, Error: errors.New("fail")}

	c.Store("config-file", "file", params, result)

	_, ok := c.Lookup("config-file", "file", params)
	if ok {
		t.Error("expected cache miss for errored result")
	}
}

func TestResultCache_ExpiresAfterMaxAge(t *testing.T) {
	c := NewResultCache(3)
	params := map[string]string{"path": "/etc/app.conf"}
	result := modules.ResourceResult{Action: modules.ActionNone, Changed: false}

	c.Store("config-file", "file", params, result)

	// Hits for 3 lookups (age 0→1, 1→2, 2→3).
	for i := 0; i < 3; i++ {
		_, ok := c.Lookup("config-file", "file", params)
		if !ok {
			t.Fatalf("expected cache hit on lookup %d", i+1)
		}
	}

	// 4th lookup: age=3 == maxAge, should miss.
	_, ok := c.Lookup("config-file", "file", params)
	if ok {
		t.Error("expected cache miss after max age exceeded")
	}
}

func TestResultCache_NonCacheableType(t *testing.T) {
	c := NewResultCache(5)
	params := map[string]string{"command": "echo hello"}
	result := modules.ResourceResult{Action: modules.ActionNone, Changed: false}

	c.Store("run-cmd", "exec", params, result)

	_, ok := c.Lookup("run-cmd", "exec", params)
	if ok {
		t.Error("exec type should not be cacheable")
	}
}

func TestResultCache_EvictsOnDrift(t *testing.T) {
	c := NewResultCache(5)
	params := map[string]string{"path": "/etc/app.conf"}

	// Store a no-op result.
	c.Store("config-file", "file", params, modules.ResourceResult{Action: modules.ActionNone, Changed: false})

	// Verify it's cached.
	_, ok := c.Lookup("config-file", "file", params)
	if !ok {
		t.Fatal("expected cache hit before eviction")
	}

	// Store a changed result — should evict.
	c.Store("config-file", "file", params, modules.ResourceResult{Action: modules.ActionUpdated, Changed: true})

	_, ok = c.Lookup("config-file", "file", params)
	if ok {
		t.Error("expected cache miss after drift eviction")
	}
}

func TestHashParams_Deterministic(t *testing.T) {
	p1 := map[string]string{"b": "2", "a": "1"}
	p2 := map[string]string{"a": "1", "b": "2"}

	if hashParams(p1) != hashParams(p2) {
		t.Error("hash should be deterministic regardless of map iteration order")
	}
}
