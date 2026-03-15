// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package federation

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Failover monitors spoke health on the hub and automatically drains spokes
// that fail consecutive health probes. It runs as a background goroutine and
// does not interfere with the check-in hot path.
type Failover struct {
	hub       *Hub
	threshold int           // consecutive failures before drain
	interval  time.Duration // check interval
	mu        sync.Mutex
	failures  map[string]int  // spoke_id -> consecutive failure count
	drained   map[string]bool // spoke_id -> already drained (prevent repeated drains)
}

// NewFailover creates a Failover manager. threshold is the number of
// consecutive probe failures before a spoke is drained. interval is how
// often spoke health is checked.
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

// Run blocks until ctx is cancelled, periodically probing spoke health and
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

// check probes all spokes and updates failure counters.
func (f *Failover) check(ctx context.Context) {
	statuses := f.hub.SpokeStatuses(ctx)

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, s := range statuses {
		if s.Healthy {
			// Spoke is healthy: reset failure counter.
			prev := f.failures[s.ID]
			f.failures[s.ID] = 0

			if f.drained[s.ID] {
				// Previously drained spoke has recovered.
				delete(f.drained, s.ID)
				slog.Info("spoke recovered after failover drain",
					"spoke_id", s.ID,
					"addr", s.Addr,
				)
			} else if prev > 0 {
				slog.Debug("spoke health restored",
					"spoke_id", s.ID,
					"previous_failures", prev,
				)
			}
			continue
		}

		// Spoke is unhealthy: increment failure counter.
		f.failures[s.ID]++
		count := f.failures[s.ID]

		slog.Warn("spoke health probe failed",
			"spoke_id", s.ID,
			"addr", s.Addr,
			"consecutive_failures", count,
			"threshold", f.threshold,
			"error", s.Error,
		)

		if count >= f.threshold && !f.drained[s.ID] {
			slog.Warn("spoke reached failure threshold, triggering auto-drain",
				"spoke_id", s.ID,
				"addr", s.Addr,
				"consecutive_failures", count,
			)

			// DrainSpoke with empty target routes by hostname patterns.
			// Run outside the lock to avoid holding it during gRPC calls.
			spokeID := s.ID
			f.drained[spokeID] = true
			go func() {
				result, err := f.hub.DrainSpoke(ctx, spokeID, "")
				if err != nil {
					slog.Error("auto-failover drain failed",
						"spoke_id", spokeID,
						"error", err,
					)
					return
				}
				slog.Warn("auto-failover drain complete",
					"spoke_id", spokeID,
					"moved", result.Moved,
					"failed", result.Failed,
				)
			}()
		}
	}
}
