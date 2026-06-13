// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestConfigPublishHonorsServerRoot guards the 0.75.9 `config publish
// --server-root` relocation: every executable path in cli/configpublish.go
// must operate on the --server-root-resolved directories (threaded via
// publish.Options / publish.PathsUnder), NOT the hardcoded publish.DefaultStacksDir.
//
// The relocation re-pointed four sites that previously read the const directly
// — the spanner-mode gitback mirror (publishViaGitback), the two .retract
// writes, and the --ask-ai file-confinement check — at the resolved stacks dir.
// The core Options threading is already covered behaviorally by
// test/clismoke's TestCLIConfigPublish (it publishes to a throwaway root and
// asserts .live + history land there). But those four secondary sites have no
// behavioral coverage: the spanner mirror only runs in spanner mode (clismoke
// is standalone), retraction only on removals, and --ask-ai needs an AI backend
// — and a full spanner-mode test is infeasible without the agent + gitback
// substrate (ops-puddle, the puddle helper daemon, a bare repo at a hardcoded
// /var/lib/vigo path, and stackswatch). So this structural guard stands in for
// them: a re-hardcoding of any of those sites back to publish.DefaultStacksDir
// — the realistic regression (a revert or a copy-paste from configstacks.go) —
// turns this red.
//
// DefaultStacksDir is legitimately referenced once, in the configPublishCmd
// command var's Long help string (a var declaration, not a function body), so
// scoping the check to FuncDecl bodies exempts it automatically. Full
// behavioral coverage of spanner-mode --server-root remains manual-substrate
// (make test-substrate / the home-lab once-over).
func TestConfigPublishHonorsServerRoot(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "cli", "configpublish.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	// Collect the position range of every function body — top-level FuncDecls
	// AND func literals (the cobra RunE, where opts is built, is a FuncLit
	// inside the configPublishCmd var, not a FuncDecl). A DefaultStacksDir
	// reference inside any of these is executable code; one outside them all
	// (the Long help string in the var declaration) is the one legitimate use.
	type rng struct{ lo, hi token.Pos }
	var bodies []rng
	ast.Inspect(file, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body != nil {
				bodies = append(bodies, rng{fn.Body.Pos(), fn.Body.End()})
			}
		case *ast.FuncLit:
			bodies = append(bodies, rng{fn.Body.Pos(), fn.Body.End()})
		}
		return true
	})
	inFuncBody := func(p token.Pos) bool {
		for _, r := range bodies {
			if p >= r.lo && p < r.hi {
				return true
			}
		}
		return false
	}

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "publish" || sel.Sel.Name != "DefaultStacksDir" {
			return true
		}
		if inFuncBody(sel.Pos()) {
			pos := fset.Position(sel.Pos())
			t.Errorf("publish.DefaultStacksDir referenced in executable code at %s:%d — the publish path must use the --server-root-resolved stacks dir (publish.PathsUnder / opts.StacksDir), not the /srv/vigo default. Re-hardcoding here silently breaks `config publish --server-root`; thread the resolved dir through instead.",
				filepath.Base(pos.Filename), pos.Line)
		}
		return true
	})
}
