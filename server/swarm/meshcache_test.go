// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package swarm

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Invalidate must not wait for a rebuild in flight (the cache lock is not
// held across build), and the overtaken rebuild must not be cached.
func TestMeshCacheInvalidateDuringBuild(t *testing.T) {
	c := NewMeshCache[int](time.Hour)
	inBuild := make(chan struct{})
	release := make(chan struct{})
	var builds atomic.Int32

	go func() {
		_, _ = c.Get(func() (int, error) {
			builds.Add(1)
			close(inBuild)
			<-release
			return 1, nil
		})
	}()
	<-inBuild

	done := make(chan struct{})
	go func() {
		c.Invalidate()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Invalidate blocked behind an in-flight rebuild")
	}
	close(release)

	// The overtaken result was not cached: the next Get rebuilds.
	time.Sleep(20 * time.Millisecond)
	v, err := c.Get(func() (int, error) { builds.Add(1); return 2, nil })
	if err != nil || v != 2 {
		t.Fatalf("Get after invalidate = %d, %v; want a fresh rebuild (2)", v, err)
	}
	if builds.Load() != 2 {
		t.Fatalf("builds = %d, want 2", builds.Load())
	}
}

// Concurrent callers on a cold cache share one rebuild.
func TestMeshCacheSerializesRebuilds(t *testing.T) {
	c := NewMeshCache[int](time.Hour)
	var builds atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Get(func() (int, error) {
				builds.Add(1)
				time.Sleep(20 * time.Millisecond)
				return 7, nil
			})
		}()
	}
	wg.Wait()
	if n := builds.Load(); n != 1 {
		t.Fatalf("builds = %d, want 1 shared rebuild", n)
	}
}
