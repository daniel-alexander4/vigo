// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package swarm

import (
	"sync"
	"time"
)

// MeshCache is the TTL cache the read-only mesh aggregators share. A rebuild
// (one trait query and a JSON parse per envoy) runs under a build lock, not
// the cache lock, so a cache hit, InvalidateCache or SetTTL never waits behind
// a rebuild of the whole fleet. Rebuilds are serialized: callers that find the
// cache stale while another rebuild is running wait for it and take its result
// instead of each scanning the fleet. A rebuild that was overtaken by an
// Invalidate is returned to its caller but not cached.
//
// Version increments on every stored rebuild, so consumers that derive their
// own data from the cached value (e.g. a reverse lookup map) can key their
// derivation on it. The zero value is not usable; use NewMeshCache.
type MeshCache[T any] struct {
	mu      sync.Mutex
	val     T
	has     bool
	exp     time.Time
	ttl     time.Duration
	gen     uint64 // bumped by Invalidate/SetTTL
	version uint64 // bumped by each stored rebuild

	buildMu sync.Mutex
}

// NewMeshCache returns an empty cache with the given TTL.
func NewMeshCache[T any](ttl time.Duration) *MeshCache[T] {
	return &MeshCache[T]{ttl: ttl}
}

func (c *MeshCache[T]) fresh(now time.Time) (T, uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.has && now.Before(c.exp) {
		return c.val, c.version, true
	}
	var zero T
	return zero, 0, false
}

// Get returns the cached value, or rebuilds it with build. build runs with no
// cache lock held. An error from build is returned and nothing is cached.
func (c *MeshCache[T]) Get(build func() (T, error)) (T, error) {
	v, _, err := c.GetVersioned(build)
	return v, err
}

// GetVersioned is Get plus the version of the returned value.
func (c *MeshCache[T]) GetVersioned(build func() (T, error)) (T, uint64, error) {
	if v, ver, ok := c.fresh(time.Now()); ok {
		return v, ver, nil
	}
	c.buildMu.Lock()
	defer c.buildMu.Unlock()
	// Another caller may have rebuilt while we waited.
	if v, ver, ok := c.fresh(time.Now()); ok {
		return v, ver, nil
	}

	c.mu.Lock()
	startGen := c.gen
	c.mu.Unlock()

	v, err := build()
	if err != nil {
		var zero T
		return zero, 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != startGen {
		// Invalidated mid-build: the result may predate the change.
		return v, 0, nil
	}
	c.version++
	c.val, c.has, c.exp = v, true, time.Now().Add(c.ttl)
	return v, c.version, nil
}

// Invalidate forces the next Get to rebuild, and keeps a rebuild already in
// flight from being cached.
func (c *MeshCache[T]) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero T
	c.val, c.has, c.exp = zero, false, time.Time{}
	c.gen++
}

// SetTTL changes the TTL and invalidates the cached value.
func (c *MeshCache[T]) SetTTL(d time.Duration) {
	c.mu.Lock()
	c.ttl = d
	c.mu.Unlock()
	c.Invalidate()
}
