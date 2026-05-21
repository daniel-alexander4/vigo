// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Failover monitors bolt health on the hub and automatically drains bolts
// that fail consecutive health probes. It runs as a background goroutine and
// does not interfere with the check-in hot path.
type Failover struct {
	hub       *Hub
	threshold int           // consecutive failures before drain
	interval  time.Duration // check interval
	mu        sync.Mutex
	failures  map[string]int  // bolt_id -> consecutive failure count
	drained   map[string]bool // bolt_id -> already drained (prevent repeated drains)
}

// NewFailover creates a Failover manager. threshold is the number of
// consecutive probe failures before a bolt is drained. interval is how
// often bolt health is checked.
func NewFailover(hub *Hub, threshold int, interval time.Duration) *Failover {
	if threshold <= 0 {
		threshold = 3
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Failover{
		hub:       hub,
		threshold: threshold,
		interval:  interval,
		failures:  make(map[string]int),
		drained:   make(map[string]bool),
	}
}

// Run blocks until ctx is cancelled, periodically probing bolt health and
// triggering automatic drain when the failure threshold is reached. Call as
// a goroutine: go failover.Run(ctx).
func (f *Failover) Run(ctx context.Context) {
	slog.Info("auto-failover started",
		"threshold", f.threshold,
		"interval", f.interval,
	)

	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("auto-failover stopped")
			return
		case <-ticker.C:
			f.check(ctx)
		}
	}
}

// check probes all bolts and updates failure counters.
func (f *Failover) check(ctx context.Context) {
	statuses := f.hub.BoltStatuses(ctx)

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, s := range statuses {
		if s.Healthy {
			// Bolt is healthy: reset failure counter.
			prev := f.failures[s.ID]
			f.failures[s.ID] = 0

			if f.drained[s.ID] {
				// Previously drained bolt has recovered.
				delete(f.drained, s.ID)
				slog.Info("bolt recovered after failover drain",
					"bolt_id", s.ID,
					"addr", s.Addr,
				)
			} else if prev > 0 {
				slog.Debug("bolt health restored",
					"bolt_id", s.ID,
					"previous_failures", prev,
				)
			}
			continue
		}

		// Bolt is unhealthy: increment failure counter.
		f.failures[s.ID]++
		count := f.failures[s.ID]

		slog.Warn("bolt health probe failed",
			"bolt_id", s.ID,
			"addr", s.Addr,
			"consecutive_failures", count,
			"threshold", f.threshold,
			"error", s.Error,
		)

		if count >= f.threshold && !f.drained[s.ID] {
			slog.Warn("bolt reached failure threshold, triggering auto-drain",
				"bolt_id", s.ID,
				"addr", s.Addr,
				"consecutive_failures", count,
			)

			// DrainBolt with empty target routes by hostname patterns.
			// Run outside the lock to avoid holding it during gRPC calls.
			boltID := s.ID
			f.drained[boltID] = true
			go func() {
				result, err := f.hub.DrainBolt(ctx, boltID, "")
				if err != nil {
					slog.Error("auto-failover drain failed",
						"bolt_id", boltID,
						"error", err,
					)
					return
				}
				slog.Warn("auto-failover drain complete",
					"bolt_id", boltID,
					"moved", result.Moved,
					"failed", result.Failed,
				)
			}()
		}
	}
}
