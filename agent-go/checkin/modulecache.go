// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package checkin

import (
	"sync"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

// ModuleCache stores PolicyModules by name, keyed by their content hash.
// When the server sends a stub (content_hash only, no resources), the agent
// fills in the module from this cache. This enables delta policy transfer —
// only changed modules are sent in full.
type ModuleCache struct {
	mu      sync.RWMutex
	modules map[string]*cachedModule // keyed by module name
}

type cachedModule struct {
	hash   string
	module *pb.PolicyModule
}

// NewModuleCache creates an empty module cache.
func NewModuleCache() *ModuleCache {
	return &ModuleCache{
		modules: make(map[string]*cachedModule),
	}
}

// Hashes returns a map of module name → content hash for all cached modules.
// This is sent to the server in CheckInRequest.cached_module_hashes.
func (c *ModuleCache) Hashes() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.modules) == 0 {
		return nil
	}

	hashes := make(map[string]string, len(c.modules))
	for name, cm := range c.modules {
		hashes[name] = cm.hash
	}
	return hashes
}

// Get returns the cached module for the given name and hash, or nil if not
// found or hash doesn't match.
func (c *ModuleCache) Get(name, hash string) *pb.PolicyModule {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cm, ok := c.modules[name]
	if !ok || cm.hash != hash {
		return nil
	}
	return cm.module
}

// Update stores a full (non-stub) module in the cache and removes any modules
// that are no longer in the bundle. Call this after merging stubs.
func (c *ModuleCache) Update(modules []*pb.PolicyModule) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Track which names are still active.
	active := make(map[string]bool, len(modules))
	for _, mod := range modules {
		active[mod.Name] = true
		if mod.ContentHash == "" {
			continue
		}
		c.modules[mod.Name] = &cachedModule{
			hash:   mod.ContentHash,
			module: mod,
		}
	}

	// Evict modules no longer in the bundle (thrall's role changed, etc.).
	for name := range c.modules {
		if !active[name] {
			delete(c.modules, name)
		}
	}
}

// MergeStubs fills in stub modules from the cache. Returns the merged list.
// Stubs whose hash doesn't match the cache are returned as-is (empty resources).
func MergeStubs(modules []*pb.PolicyModule, cache *ModuleCache) []*pb.PolicyModule {
	if cache == nil {
		return modules
	}

	result := make([]*pb.PolicyModule, 0, len(modules))
	for _, mod := range modules {
		if mod.Stub {
			cached := cache.Get(mod.Name, mod.ContentHash)
			if cached != nil {
				result = append(result, cached)
				continue
			}
			// Cache miss — shouldn't happen, but pass the stub through.
			// The module will have zero resources and be harmless.
		}
		result = append(result, mod)
	}
	return result
}
