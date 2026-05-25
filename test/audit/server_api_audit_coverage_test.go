// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package audit

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestServerAPIWriteRoutesAudit pins CLAUDE.md's non-negotiable
// "tamper-evident audit log over every privileged operation" against
// every Go file under `server/api/` and `server/bootstrap/`. Every
// non-GET `mux.Handle*` registration must either invoke `recordAudit`
// in its handler body, appear in `serverAPIWriteRoutesAuditExempt`
// (with rationale why it doesn't mutate state), or appear in
// `serverAPIWriteRoutesAuditGapPending` (with a tracking pointer to
// the pending entry that will close it).
//
// Two-map structure rather than one map with a tag field — the exempt
// category captures legitimate "this POST does not mutate state"
// rationales (audit not needed); the gap-pending category captures
// "audit IS needed, this is a known TODO" so the gap doesn't quietly
// hide. Burn-down enforcement: each gap-closure scrutinize removes
// entries from the pending map and adds `recordAudit` calls; the test
// stays green throughout but the gap-pending count monotonically
// decreases.
//
// ui/handler.go is covered by `ui_audit_coverage_test.go`; shared AST
// helpers live in `audit_coverage_helpers_test.go`.

// serverAPIWriteRoutesAuditExempt: routes whose POST/PUT/PATCH/DELETE
// verb is legitimately not a state-mutating operator action. Rationale
// is mandatory; an empty rationale is impossible by map structure.
var serverAPIWriteRoutesAuditExempt = map[string]string{
	"POST /api/v1/ask": "ai.handleAsk proxies the strictly read-only AI assistant " +
		"(CLAUDE.md: ~14 tools, all list/get/query — no mutation surface). " +
		"POST is required only because the question text rides in the request " +
		"body; the handler never writes to envoy / config / DB state. Mirrors " +
		"the `POST /ui/ask` exemption on the UI side.",
	"POST /api/v1/query": "query.HandleQuery dispatches a live read-only query " +
		"to managed envoys (inventory / traits lookups). POST is used because " +
		"the query expression rides in the body; the handler never mutates " +
		"server-side or envoy-side state.",
	"POST /api/v1/bootstrap/register": "bootstrap.BootstrapRegister is the " +
		"pre-auth enrollment endpoint; the privileged action it triggers " +
		"(envoy admission to the fleet) is audited in the registry layer " +
		"and is visible via the license counter — the bootstrap handler " +
		"itself is the protocol entry point, not the policy decision.",
	"POST /api/v1/db/query": "database.handleQuery is admin-gated and runs " +
		"SELECT-only queries against the SQLite metadata (per its package " +
		"contract enforced by the rejectMutatingSQL guard in the handler). " +
		"No mutation surface to audit.",
	"POST /api/v1/peer/promote": "peer.handlePromote delegates to " +
		"peer.Service.Promote (server/peer/service.go:230) which records " +
		"the audit event via its own auditWriter — the API layer is a " +
		"thin pass-through that would double-record if it audited too.",
}

// serverAPIWriteRoutesAuditGapPending: routes that SHOULD audit but
// don't yet. Each entry's value is the pending tracking pointer that
// will close it. The pointer format is "PENDING: <pending-entry-name>".
// As gap closures land, entries are removed from this map and the
// handler grows a recordAudit call in the same commit.
//
// 36 entries today (v0.67.18 baseline). Grouped by intended close-out
// batch — one pending entry per logical batch so each follow-on
// scrutinize has a concrete, bounded scope.
var serverAPIWriteRoutesAuditGapPending = map[string]string{
	// PENDING: server/api/envoys audit-add (4 handlers)
	"POST /api/v1/envoys/{id}/update":      "PENDING: server/api/envoys audit-add",
	"POST /api/v1/envoys/update":           "PENDING: server/api/envoys audit-add",
	"PUT /api/v1/envoys/{id}/maintenance":  "PENDING: server/api/envoys audit-add",
	"DELETE /api/v1/envoys/{id}/maintenance": "PENDING: server/api/envoys audit-add",

	// PENDING: server/api/database audit-add (4 handlers)
	"POST /api/v1/db/vacuum":     "PENDING: server/api/database audit-add",
	"POST /api/v1/db/checkpoint": "PENDING: server/api/database audit-add",
	"POST /api/v1/db/prune":      "PENDING: server/api/database audit-add",
	"POST /api/v1/db/analyze":    "PENDING: server/api/database audit-add",

	// PENDING: server/api/config audit-add (6 handlers — publish lifecycle + secret rotation ack)
	"POST /api/v1/config/publishes":         "PENDING: server/api/config audit-add",
	"POST /api/v1/config/rollback/{version}": "PENDING: server/api/config audit-add",
	"POST /api/v1/config/freeze":             "PENDING: server/api/config audit-add",
	"POST /api/v1/config/unfreeze":           "PENDING: server/api/config audit-add",
	"POST /api/v1/config/retract":            "PENDING: server/api/config audit-add",
	"POST /api/v1/secrets/rotated":           "PENDING: server/api/config audit-add",

	// PENDING: server/api/swarm audit-add (7 handlers across 4 files — swarm/curator/poolq/filecast block/unblock + filecast distribute/revoke + swarm cleanup)
	"POST /api/v1/swarm/cleanup":                       "PENDING: server/api/swarm audit-add",
	"POST /api/v1/swarm/curator/block/{artifact_id}":   "PENDING: server/api/swarm audit-add",
	"POST /api/v1/swarm/curator/unblock/{artifact_id}": "PENDING: server/api/swarm audit-add",
	"POST /api/v1/swarm/poolq/block/{topic_id}":        "PENDING: server/api/swarm audit-add",
	"POST /api/v1/swarm/poolq/unblock/{topic_id}":      "PENDING: server/api/swarm audit-add",
	"POST /api/v1/swarm/filecast/distribute":           "PENDING: server/api/swarm audit-add",
	"DELETE /api/v1/swarm/filecast/{sha256}":           "PENDING: server/api/swarm audit-add",

	// PENDING: server/api/spanner audit-add (4 handlers — federation peer control + invite issuance)
	"POST /api/v1/spanner/bolts/{id}/push":  "PENDING: server/api/spanner audit-add",
	"POST /api/v1/spanner/reassign":         "PENDING: server/api/spanner audit-add",
	"POST /api/v1/spanner/bolts/{id}/drain": "PENDING: server/api/spanner audit-add",
	"POST /api/v1/spanner/bolt/invite":      "PENDING: server/api/spanner audit-add",

	// PENDING: server/api/task+workflows audit-add (3 handlers — cancel verbs)
	"POST /api/v1/task/{id}/cancel":    "PENDING: server/api/task+workflows audit-add",
	"POST /api/v1/task/cancel-running": "PENDING: server/api/task+workflows audit-add",
	"POST /api/v1/workflows/{id}/abort": "PENDING: server/api/task+workflows audit-add",

	// PENDING: server/api/users.handleTOTPSetup audit-add (1 handler — TOTP secret creation, sibling to handleTOTPConfirm/Disable which already audit)
	"POST /api/v1/users/totp/setup": "PENDING: server/api/users.handleTOTPSetup audit-add",

	// PENDING: sandgorgon audit-add (post-v1, all 7 sandgorgon mutation routes)
	// CLAUDE.md explicitly defers the whole sandgorgon subsystem to post-v1;
	// these handlers don't count toward v1 scoping. Audit-add lands when the
	// subsystem activates.
	"POST /api/v1/sandgorgon/commission":                "PENDING: sandgorgon audit-add (post-v1)",
	"POST /api/v1/sandgorgon/decommission/{id}":         "PENDING: sandgorgon audit-add (post-v1)",
	"POST /api/v1/sandgorgon/return-to-available/{id}":  "PENDING: sandgorgon audit-add (post-v1)",
	"POST /api/v1/sandgorgon/provision/{id}":            "PENDING: sandgorgon audit-add (post-v1)",
	"POST /api/v1/sandgorgon/images":                    "PENDING: sandgorgon audit-add (post-v1)",
	"DELETE /api/v1/sandgorgon/images/{id}":             "PENDING: sandgorgon audit-add (post-v1)",
	"POST /api/v1/sandgorgon/import":                    "PENDING: sandgorgon audit-add (post-v1)",
}

func TestServerAPIWriteRoutesAudit(t *testing.T) {
	root := repoRoot(t)

	// Walk server/api/ + server/bootstrap/ for any *.go file that
	// registers routes via mux.Handle*. Per-file scoping matches the
	// codebase shape (each file has its own RegisterRoutes against a
	// dedicated handler type).
	var sourceFiles []string
	for _, dir := range []string{
		filepath.Join(root, "server", "api"),
		filepath.Join(root, "server", "bootstrap"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			sourceFiles = append(sourceFiles, filepath.Join(dir, name))
		}
	}
	if len(sourceFiles) == 0 {
		t.Fatal("found no .go files in server/api/ or server/bootstrap/ — walker broken")
	}
	sort.Strings(sourceFiles)

	type missingEntry struct {
		route   string
		handler string
		file    string
	}
	var allRoutes []writeRoute
	var unclassified []missingEntry

	for _, srcPath := range sourceFiles {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, srcPath, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", srcPath, err)
		}
		routes := findWriteRoutes(f)
		if len(routes) == 0 {
			continue
		}
		handlers := buildHandlersInFile(f)
		for _, r := range routes {
			allRoutes = append(allRoutes, r)
			if _, ok := serverAPIWriteRoutesAuditExempt[r.route]; ok {
				continue
			}
			if _, ok := serverAPIWriteRoutesAuditGapPending[r.route]; ok {
				continue
			}
			fd, ok := handlers[r.handler]
			if !ok {
				t.Errorf("route %q in %s references handler h.%s which has no pointer-receiver method declaration in the same file",
					r.route, filepath.Base(srcPath), r.handler)
				continue
			}
			if !bodyCallsRecordAudit(fd.Body) {
				unclassified = append(unclassified, missingEntry{
					route:   r.route,
					handler: r.handler,
					file:    filepath.Base(srcPath),
				})
			}
		}
	}

	if len(allRoutes) == 0 {
		t.Fatal("found no write-method routes anywhere under server/api/ or server/bootstrap/ — walker broken")
	}

	if len(unclassified) > 0 {
		sort.Slice(unclassified, func(i, j int) bool { return unclassified[i].route < unclassified[j].route })
		var lines []string
		for _, m := range unclassified {
			lines = append(lines, m.route+" → h."+m.handler+"  ("+m.file+")")
		}
		t.Errorf(
			"write-method routes in server/api/ + server/bootstrap/ that neither call recordAudit nor appear in any allowlist:\n  %s\n\n"+
				"Fix one of:\n"+
				"  (a) add a `recordAudit(r.Context(), h.auditWriter, …)` call to the handler body,\n"+
				"  (b) add the route to serverAPIWriteRoutesAuditExempt with a rationale explaining why it does not mutate state, or\n"+
				"  (c) add the route to serverAPIWriteRoutesAuditGapPending with a pending-entry tracking pointer for the deferred fix.",
			strings.Join(lines, "\n  "),
		)
	}
}
