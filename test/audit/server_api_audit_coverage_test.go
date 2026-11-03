// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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
// Empty as of v0.67.20 — every previously-pending gap was closed in
// the same scrutinize that landed this test (including the 7
// sandgorgon handlers; per Dan's instruction, code-level audit gaps
// get fixed even when the subsystem functionality is post-v1-deferred,
// because the audit-on-state-mutation invariant is a code-level
// property not a functional one). Future regressions appear here as
// new entries via /scrutinize discipline; an empty map is the steady
// state.
var serverAPIWriteRoutesAuditGapPending = map[string]string{}

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
