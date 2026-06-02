// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestNoInlineStylesInRenderedHTML guards the CSP style-src 'self' lockdown.
//
// Every HTML surface Vigo serves routes through server/httpsec, whose
// Content-Security-Policy forbids 'unsafe-inline' for styles. HTML that the ui
// package builds as Go string literals (render.go / handler.go / sparkline.go)
// is just as subject to that policy as the .html templates are — an inline
// style= attribute in such a string is silently blocked by the browser at
// runtime (style-src-attr) and the styling never applies.
//
// The 0.71.1/0.71.2 de-inline arc only swept the templates; this test exists so
// a style= attribute can't sneak back into the package's Go-rendered HTML. Move
// the declaration to a class in ui/static/css/style.css instead (u-* utility,
// .tgt-N gradient bucket, or a component class). It scans string literals only,
// so doc comments that mention style= for explanation are fine.
func TestNoInlineStylesInRenderedHTML(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read ui dir: %v", err)
	}

	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if strings.Contains(lit.Value, `style="`) {
				pos := fset.Position(lit.Pos())
				t.Errorf("%s:%d: inline style= in rendered HTML — move it to a CSS class (CSP style-src 'self' blocks inline styles)", name, pos.Line)
			}
			return true
		})
	}
}
