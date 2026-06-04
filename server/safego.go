// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"log/slog"
	"runtime/debug"
)

// safeGo runs fn in a new goroutine with panic recovery. Every long-lived
// background daemon the server starts (server_run.go) runs detached; an
// unrecovered panic in any one would otherwise crash the entire vigosrv
// process — gRPC streams, REST, metrics, and every other daemon with it.
// safeGo contains the panic to the one daemon: it logs the panic + stack and
// lets that goroutine exit. Mirrors the per-goroutine recover used by
// executeWorkflow (server/workflows) and runRollingExecution (server/grpc).
//
// The panicked daemon stays down until the next server restart — deliberately
// NOT auto-restarted. Several of these workers are not re-entrant: re-invoking
// enforcer.Run / watcher.Run would deadlock on a mutex they still hold, and
// grcPusher.Start / spannerGossip.Run / stacksWatcher.Run would spawn duplicate
// internal goroutines or fsnotify watchers. Restart-with-backoff would require
// making those idempotent first (tracked as a follow-on). Until then, the death
// is loud (slog.Error + stack) rather than a silent process crash.
func safeGo(name string, fn func()) {
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
