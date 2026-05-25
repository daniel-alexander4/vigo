// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestUIHandlerWriteRoutesAudit pins CLAUDE.md's non-negotiable
// "tamper-evident audit log over every privileged operation" against
// ui/handler.go's mux registrations: every non-GET route must either
// invoke `recordAudit` in its handler body, or appear in
// writeRoutesAuditExempt with a rationale explaining why the POST/
// PUT/PATCH/DELETE verb does not mutate state (e.g., POST carrying a
// query body to a strictly-read-only AI proxy).
//
// Forward-looking enforcement only — the current surface (3 routes,
// 2 audited, 1 exempt) is verified clean as of v0.67.16. A future PR
// that adds a 4th write route without audit and without an exemption
// rationale fails this test.
//
// Scope: ui/handler.go only. The broader server/api/* audit-coverage
// pass is tracked separately in pending.md.

// writeRoutesAuditExempt lists ui/handler.go non-GET routes whose
// handlers legitimately do not call `recordAudit`. Every entry MUST
// carry a one-line rationale — the rationale IS the value of the
// allowlist, and an unjustified entry is itself a finding.
var writeRoutesAuditExempt = map[string]string{
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

	// Build a name → FuncDecl map for every method on *Handler. The
	// route registrations reference handlers as `h.Name`, so we look
	// them up by Name only — *Handler is the only receiver type that
	// appears in this file.
	handlers := make(map[string]*ast.FuncDecl)
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 {
			continue
		}
		star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		ident, ok := star.X.(*ast.Ident)
		if !ok || ident.Name != "Handler" {
			continue
		}
		handlers[fd.Name.Name] = fd
	}
	if len(handlers) == 0 {
		t.Fatal("found no methods on *Handler — parser broken or file shape changed")
	}

	routes := findWriteRoutes(f)
	if len(routes) == 0 {
		t.Fatal("found no write-method routes in ui/handler.go — parser broken or all writes were removed (the latter would be its own surprise)")
	}

	// Sort for deterministic test output.
	sort.Slice(routes, func(i, j int) bool { return routes[i].route < routes[j].route })

	var missing []string
	for _, r := range routes {
		if _, ok := writeRoutesAuditExempt[r.route]; ok {
			continue
		}
		fd, ok := handlers[r.handler]
		if !ok {
			t.Errorf("route %q references handler h.%s which has no *Handler method declaration in ui/handler.go", r.route, r.handler)
			continue
		}
		if !bodyCallsRecordAudit(fd.Body) {
			missing = append(missing, r.route+" → h."+r.handler)
		}
	}

	if len(missing) > 0 {
		t.Errorf(
			"write-method routes in ui/handler.go that neither call recordAudit nor appear in writeRoutesAuditExempt:\n  %s\n\n"+
				"Fix one of: (a) add a `recordAudit(r.Context(), h.auditWriter, …)` call to the handler body, or "+
				"(b) add the route to writeRoutesAuditExempt with a rationale explaining why it does not mutate state.",
			strings.Join(missing, "\n  "),
		)
	}
}

type writeRoute struct {
	route   string // e.g. "POST /api/v1/compliance/waivers"
	handler string // e.g. "CreateWaiver"
}

// findWriteRoutes walks the file's AST for `mux.Handle("METHOD /path", …)`
// and `mux.HandleFunc("METHOD /path", h.Name)` registrations whose
// method is anything other than GET. Returns each registration's
// route string + the handler method name (digging through middleware
// wrappers like `complianceDocsWrite(http.HandlerFunc(h.CreateWaiver))`).
func findWriteRoutes(f *ast.File) []writeRoute {
	var routes []writeRoute
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "mux" {
			return true
		}
		if sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		routeStr, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		parts := strings.SplitN(routeStr, " ", 2)
		if len(parts) < 2 {
			return true
		}
		if parts[0] == "GET" {
			return true
		}
		handlerName := extractHandlerName(call.Args[1])
		if handlerName == "" {
			return true
		}
		routes = append(routes, writeRoute{route: routeStr, handler: handlerName})
		return true
	})
	return routes
}

// extractHandlerName digs into a route's handler argument and returns
// the method name from the first `h.Name` SelectorExpr it finds.
// Handles bare references (`h.AskStream`), `http.HandlerFunc` wrappers,
// and middleware wrappers like `complianceDocsWrite(http.HandlerFunc(h.CreateWaiver))`
// — recurses into CallExpr args.
func extractHandlerName(arg ast.Expr) string {
	switch e := arg.(type) {
	case *ast.SelectorExpr:
		if ident, ok := e.X.(*ast.Ident); ok && ident.Name == "h" {
			return e.Sel.Name
		}
	case *ast.CallExpr:
		for _, sub := range e.Args {
			if name := extractHandlerName(sub); name != "" {
				return name
			}
		}
	}
	return ""
}

// bodyCallsRecordAudit returns true iff the function body contains
// any call to the package-level `recordAudit` helper defined in
// ui/audit.go. Walks all CallExpr nodes; matches on bare Ident name
// to avoid false positives on `something.Record(…)` from unrelated
// types.
func bodyCallsRecordAudit(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == "recordAudit" {
			found = true
			return false
		}
		return true
	})
	return found
}
