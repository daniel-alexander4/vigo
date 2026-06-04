// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package daemon is the single source of truth for recovered-panic handling
// across the server. An unrecovered panic in any goroutine crashes the whole
// vigosrv process — gRPC streams, REST, metrics, and every other goroutine with
// it — so every long-lived or detached goroutine, and every request-handler
// path, routes its panic recovery through here.
//
// One primitive does the work:
//
//   - Recover is the deferred backstop. `defer daemon.Recover(site, onPanic)`
//     recovers a panic, logs it with its stack in one canonical shape, then runs
//     onPanic (may be nil) — the boundary-specific teardown: set a named return
//     error, push to a coordination channel, mark a run failed. recover() works
//     inside it because `defer daemon.Recover(...)` makes it the deferred
//     function calling recover() directly, which is Go's one requirement.
//
// Two conveniences wrap Recover for the common goroutine shapes:
//
//   - SafeGo launches fn in a new goroutine with a recover backstop (onPanic
//     nil). A panic that escapes fn — typically from one-time setup — is logged
//     and the goroutine exits; the daemon is NOT restarted (a deterministic
//     setup fault would only recur). Use to launch a detached daemon.
//
//   - Guard runs fn synchronously, recovering so the caller's loop continues to
//     the next iteration. A transient tick panic (a malformed DB row, a momentary
//     nil) is logged and the loop ticks on — no state loss, no restart gap, the
//     ticker interval is the natural backoff. Use around per-iteration work.
//
// Anything that needs a teardown action on panic (a gRPC interceptor returning
// codes.Internal, a stream reader signalling its error channel, a workflow run
// marked failed) defers Recover directly with a non-nil onPanic, rather than
// hand-rolling a fourth recover. The recover, the log level, the stack capture,
// and the log keys live in exactly one place.
package daemon

import (
	"log/slog"
	"runtime/debug"
)

// Recover is the deferred panic backstop every site routes through. Call it as
// the deferred function itself:
//
//	defer daemon.Recover(site, onPanic)
//
// On a panic it logs site + value + stack at Error, then runs onPanic if
// non-nil, handing it the recovered value. onPanic carries the boundary-specific
// teardown — set a named return error, push to a coordination channel, mark a
// run failed (with the panic value in the failure reason) — and may set a named
// return value of the enclosing function (deferred funcs can mutate named
// returns). With a nil onPanic it simply contains and logs the panic.
func Recover(site string, onPanic func(r any)) {
	if r := recover(); r != nil {
		slog.Error("recovered panic",
			"site", site,
			"panic", r,
			"stack", string(debug.Stack()),
		)
		if onPanic != nil {
			onPanic(r)
		}
	}
}

// SafeGo runs fn in a new goroutine with a recover backstop. A panic that
// escapes fn is logged with its stack and the goroutine exits — the rest of the
// process keeps running. The daemon is not restarted; self-healing of repeating
// work is Guard's job.
func SafeGo(name string, fn func()) {
	go func() {
		defer Recover(name, nil)
		fn()
	}()
}

// Guard runs fn synchronously, recovering any panic so the caller's loop can
// proceed to the next iteration. A panicking tick is logged and treated as a
// no-op for that cycle; the daemon stays alive and retries on its next tick:
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
	defer Recover(name, nil)
	fn()
}
