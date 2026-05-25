// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package audit

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// Shared AST-walking helpers for the audit-coverage tests in this
// package (`ui_audit_coverage_test.go`, `server_api_audit_coverage_test.go`).
// The shape: parse one or more Go source files, find every non-GET
// `mux.Handle*` registration, pair each route's handler reference
// (`h.Name`) with a `func (h *X) Name` declaration in the same file,
// and walk the body for `recordAudit(...)` calls. Routes without a
// recordAudit call must appear in the test's per-surface allowlist.

// writeRoute is one non-GET mux registration paired with the handler
// method name it dispatches to.
type writeRoute struct {
	route   string // "POST /api/v1/path"
	handler string // "HandlerMethodName"
}

// findWriteRoutes walks the file's AST for `mux.Handle("METHOD …", …)`
// and `mux.HandleFunc("METHOD …", h.Name)` registrations whose method
// is not GET. Returns each route plus the handler method name dug out
// of the second arg (through any middleware wrappers like
// `complianceDocsWrite(http.HandlerFunc(h.CreateWaiver))`).
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

// extractHandlerName digs into the second arg of a mux.Handle call
// and returns the method name from the first `h.Name` SelectorExpr
// it finds. Recurses through CallExpr args so wrapper shapes like
// `middleware(http.HandlerFunc(h.Foo))` work.
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

// buildHandlersInFile returns a name → FuncDecl map for every method
// on any `*X` pointer-receiver type declared in this file. Per-file
// scoping matches the mux-registration pattern (each file's
// RegisterRoutes references methods on the file's own handler type).
// If two types in the same file share a method name (unusual), the
// last declaration wins; that's a code-organization smell that
// callers can catch separately by asserting len equals the route
// count.
func buildHandlersInFile(f *ast.File) map[string]*ast.FuncDecl {
	out := make(map[string]*ast.FuncDecl)
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 {
			continue
		}
		// Receiver must be `*X` (any pointer type); plain-value
		// receivers don't carry the audit-recording pattern.
		if _, ok := fd.Recv.List[0].Type.(*ast.StarExpr); !ok {
			continue
		}
		out[fd.Name.Name] = fd
	}
	return out
}

// bodyCallsRecordAudit returns true iff the function body contains
// any call to the package-level `recordAudit` helper (defined in
// `server/api/config.go` and `ui/audit.go`). Matches on bare Ident
// to avoid false positives on `something.Record(…)` from unrelated
// types — the deliberate SoT for "this handler emitted an audit
// event" is the `recordAudit` helper, not a direct `.Record` call.
// Handlers that bypass the helper (e.g., a deny-path that wants
// custom error handling) should be migrated to the helper before
// being credited.
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
