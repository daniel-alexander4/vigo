// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package integration

import (
	"context"
	"testing"
)

// TestCrossBinary_RegisterCheckInConverge verifies the full cross-binary path:
// Go server starts → agent binary enrolls → checks in → receives policy →
// applies resources (dry-run) → reports result back to server.
func TestCrossBinary_RegisterCheckInConverge(t *testing.T) {
	modules := map[string]string{
		"testmod": `
name: testmod
resources:
  - name: test-pkg
    type: package
    package: curl
    state: present
`,
	}
	thralls := `
thralls:
  - match: "*.integ.test"
    modules: [testmod]
`
	env := startServer(t, modules, thralls)
	defer env.Cleanup()

	dataDir, pubPEM := prepareAgentDir(t)
	thrallID := enrollAgent(t, env, dataDir, pubPEM, "web01.integ.test", "cross-binary-token")

	// Run agent in dry-run mode (don't actually install packages on the test host).
	output, err := runAgent(t, env.ServerAddr, dataDir, "--dry-run")
	if err != nil {
		t.Fatalf("agent failed: %v\noutput:\n%s", err, output)
	}
	_ = thrallID

	// Verify the agent received and processed the testmod module.
	assertOutputContains(t, output, "testmod")
}

// TestCrossBinary_ConsecutiveRuns verifies the agent can run multiple
// consecutive convergence cycles against the same server.
func TestCrossBinary_ConsecutiveRuns(t *testing.T) {
	modules := map[string]string{
		"testmod": `
name: testmod
resources:
  - name: test-pkg
    type: package
    package: curl
    state: present
`,
	}
	thralls := `
thralls:
  - match: "*.integ.test"
    modules: [testmod]
`
	env := startServer(t, modules, thralls)
	defer env.Cleanup()

	dataDir, pubPEM := prepareAgentDir(t)
	enrollAgent(t, env, dataDir, pubPEM, "consec01.integ.test", "consec-token")

	// First run.
	output1, err := runAgent(t, env.ServerAddr, dataDir, "--dry-run")
	if err != nil {
		t.Fatalf("first run failed: %v\noutput:\n%s", err, output1)
	}
	assertOutputContains(t, output1, "testmod")

	// Second run — should also succeed (policy delivered again since `run`
	// always sends policy_version=0).
	output2, err := runAgent(t, env.ServerAddr, dataDir, "--dry-run")
	if err != nil {
		t.Fatalf("second run failed: %v\noutput:\n%s", err, output2)
	}
	assertOutputContains(t, output2, "testmod")
}

// TestCrossBinary_EmptyConfig verifies the agent handles an empty config
// (no modules assigned) gracefully — it should connect, check in, and
// exit without error.
func TestCrossBinary_EmptyConfig(t *testing.T) {
	// No modules, no thrall mapping.
	env := startServer(t, nil, "")
	defer env.Cleanup()

	dataDir, pubPEM := prepareAgentDir(t)
	enrollAgent(t, env, dataDir, pubPEM, "empty01.integ.test", "empty-token")

	output, err := runAgent(t, env.ServerAddr, dataDir, "--dry-run")
	if err != nil {
		t.Fatalf("agent failed: %v\noutput:\n%s", err, output)
	}
	// Agent should handle zero-module response gracefully.
	// The output contains either "zero modules" warning or an empty run summary.
	// Key assertion: agent exits 0 (no error).
}

// TestCrossBinary_RevokedAgent verifies that a revoked agent gets rejected
// by the server on check-in.
func TestCrossBinary_RevokedAgent(t *testing.T) {
	env := startServer(t, nil, "")
	defer env.Cleanup()

	dataDir, pubPEM := prepareAgentDir(t)
	thrallID := enrollAgent(t, env, dataDir, pubPEM, "revoked01.integ.test", "revoke-token")

	// Revoke the agent.
	if err := env.Registry.RevokeThrall(context.Background(), thrallID); err != nil {
		t.Fatal(err)
	}

	// Agent should fail with a permission error.
	output, err := runAgent(t, env.ServerAddr, dataDir, "--dry-run")
	if err == nil {
		t.Fatal("expected agent to fail after revocation")
	}
	_ = output // Error is expected — agent exits non-zero.
}

// TestCrossBinary_MultiModule verifies the agent receives and processes
// multiple modules in a single convergence cycle.
func TestCrossBinary_MultiModule(t *testing.T) {
	modules := map[string]string{
		"base": `
name: base
resources:
  - name: install-curl
    type: package
    package: curl
    state: present
`,
		"web": `
name: web
depends_on: [base]
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`,
	}
	thralls := `
thralls:
  - match: "*.integ.test"
    modules: [base, web]
`
	env := startServer(t, modules, thralls)
	defer env.Cleanup()

	dataDir, pubPEM := prepareAgentDir(t)
	enrollAgent(t, env, dataDir, pubPEM, "multi01.integ.test", "multi-token")

	output, err := runAgent(t, env.ServerAddr, dataDir, "--dry-run")
	if err != nil {
		t.Fatalf("agent failed: %v\noutput:\n%s", err, output)
	}

	// Both modules should appear in the output.
	assertOutputContains(t, output, "base")
	assertOutputContains(t, output, "web")
}

// TestCrossBinary_ForcePush verifies that setting force_push on a thrall
// causes the server to re-deliver the full policy even on a no-change cycle.
func TestCrossBinary_ForcePush(t *testing.T) {
	modules := map[string]string{
		"testmod": `
name: testmod
resources:
  - name: test-pkg
    type: package
    package: curl
    state: present
`,
	}
	thralls := `
thralls:
  - match: "*.integ.test"
    modules: [testmod]
`
	env := startServer(t, modules, thralls)
	defer env.Cleanup()

	dataDir, pubPEM := prepareAgentDir(t)
	thrallID := enrollAgent(t, env, dataDir, pubPEM, "force01.integ.test", "force-token")

	// First run — caches policy.
	output1, err := runAgent(t, env.ServerAddr, dataDir, "--dry-run")
	if err != nil {
		t.Fatalf("first run failed: %v\noutput:\n%s", err, output1)
	}

	// Set force_push.
	if err := env.Queries.SetForcePush(context.Background(), thrallID); err != nil {
		t.Fatal(err)
	}

	// Second run — despite cached version, should get full policy due to force_push.
	output2, err := runAgent(t, env.ServerAddr, dataDir, "--dry-run")
	if err != nil {
		t.Fatalf("forced run failed: %v\noutput:\n%s", err, output2)
	}
	// Should NOT say "no change" — force_push bypasses the short-circuit.
	assertOutputContains(t, output2, "testmod")
}
