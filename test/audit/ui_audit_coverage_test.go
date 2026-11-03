// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestUIHandlerWriteRoutesAudit pins CLAUDE.md's non-negotiable
// "tamper-evident audit log over every privileged operation" against
// ui/handler.go's mux registrations: every non-GET route must either
// invoke `recordAudit` in its handler body, or appear in
// uiWriteRoutesAuditExempt with a rationale explaining why the POST/
// PUT/PATCH/DELETE verb does not mutate state (e.g., POST carrying a
// query body to a strictly-read-only AI proxy).
//
// Forward-looking enforcement only — the current surface (3 routes,
// 2 audited, 1 exempt) is verified clean as of v0.67.16. A future PR
// that adds a 4th write route without audit and without an exemption
// rationale fails this test.
//
// The broader server/api/* audit-coverage test lives in
// `server_api_audit_coverage_test.go`; shared AST helpers live in
// `audit_coverage_helpers_test.go`.

// uiWriteRoutesAuditExempt lists ui/handler.go non-GET routes whose
// handlers legitimately do not call `recordAudit`. Every entry MUST
// carry a one-line rationale — the rationale IS the value of the
// allowlist, and an unjustified entry is itself a finding.
var uiWriteRoutesAuditExempt = map[string]string{
	"POST /ui/ask": "AskStream proxies the strictly read-only AI assistant " +
		"(CLAUDE.md: ~14 tools, all list/get/query — no mutation surface). " +
		"The POST verb is required only because the question text rides in " +
		"the request body; the handler never writes to envoy / config / DB state.",
}

func TestUIHandlerWriteRoutesAudit(t *testing.T) {
	root := repoRoot(t)
	srcPath := filepath.Join(root, "ui", "handler.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", srcPath, err)
	}

	handlers := buildHandlersInFile(f)
	if len(handlers) == 0 {
		t.Fatal("found no methods on pointer-receiver types in ui/handler.go — parser broken or file shape changed")
	}

	routes := findWriteRoutes(f)
	if len(routes) == 0 {
		t.Fatal("found no write-method routes in ui/handler.go — parser broken or all writes were removed (the latter would be its own surprise)")
	}

	sort.Slice(routes, func(i, j int) bool { return routes[i].route < routes[j].route })

	var missing []string
	for _, r := range routes {
		if _, ok := uiWriteRoutesAuditExempt[r.route]; ok {
			continue
		}
		fd, ok := handlers[r.handler]
		if !ok {
			t.Errorf("route %q references handler h.%s which has no pointer-receiver method declaration in ui/handler.go", r.route, r.handler)
			continue
		}
		if !bodyCallsRecordAudit(fd.Body) {
			missing = append(missing, r.route+" → h."+r.handler)
		}
	}

	if len(missing) > 0 {
		t.Errorf(
			"write-method routes in ui/handler.go that neither call recordAudit nor appear in uiWriteRoutesAuditExempt:\n  %s\n\n"+
				"Fix one of: (a) add a `recordAudit(r.Context(), h.auditWriter, …)` call to the handler body, or "+
				"(b) add the route to uiWriteRoutesAuditExempt with a rationale explaining why it does not mutate state.",
			strings.Join(missing, "\n  "),
		)
	}
}
