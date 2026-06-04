// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package daemon holds the panic-safety helpers shared by every long-lived
// background worker the server runs. Two primitives compose into the model:
//
//   - SafeGo launches a daemon goroutine with a recover backstop. An
//     unrecovered panic in any detached daemon would otherwise crash the whole
//     vigosrv process — gRPC streams, REST, metrics, and every other daemon
//     with it. SafeGo contains a panic to the one goroutine: it logs the panic
//     + stack and lets that goroutine exit. It does NOT restart — a daemon that
//     panics in its one-time setup (resolving a multicast group, creating an
//     fsnotify watcher) is usually facing a deterministic fault that a restart
//     would only hit again, so it stays down, loudly logged.
//
//   - Guard wraps the per-iteration work inside a daemon's loop. A transient
//     panic in one tick (a malformed DB row, a momentary nil) is recovered and
//     the loop continues to the next tick — the daemon self-heals without
//     losing its accumulated in-memory state, re-running setup, or leaving a
//     restart-sized gap where it's fully down. The loop's own ticker interval
//     is the natural backoff.
//
// The division of labour: SafeGo guards the goroutine boundary (setup-phase
// panics → recover-and-die), Guard guards each iteration (loop-body panics →
// recover-and-continue). A worker uses SafeGo to launch and Guard around the
// work it repeats.
package daemon

import (
	"log/slog"
	"runtime/debug"
)

// SafeGo runs fn in a new goroutine with a panic-recovery backstop. A panic
// that escapes fn (typically from one-time setup, since repeating work should
// be wrapped in Guard) is logged with its stack and the goroutine exits — the
// rest of the process keeps running. The daemon is not restarted.
func SafeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("background daemon panicked — subsystem stopped until restart",
					"daemon", name,
					"panic", r,
					"stack", string(debug.Stack()),
				)
			}
		}()
		fn()
	}()
}

// Guard runs fn synchronously, recovering any panic so the caller's loop can
// proceed to the next iteration. A panicking tick is logged with its stack and
// treated as a no-op for that cycle; the daemon stays alive and retries on its
// next tick. Call it around the per-iteration work inside a daemon loop:
//
//	for {
//		select {
//		case <-ctx.Done():
//			return
//		case <-ticker.C:
//			daemon.Guard("metrics-collector", func() { c.UpdateGauges(ctx) })
//		}
//	}
func Guard(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("background daemon tick panicked — recovered, continuing",
				"daemon", name,
				"panic", r,
				"stack", string(debug.Stack()),
			)
		}
	}()
	fn()
}
