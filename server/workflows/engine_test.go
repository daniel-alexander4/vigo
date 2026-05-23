// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package workflows

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkflow(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestEngine_Load(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "deploy.vgo", `
name: deploy
description: Deploy application
steps:
  - name: setup
    command: longdrawer setup
    target: "*.web.*"
  - name: restart
    task: restart-app
    target: "*.web.*"
    params:
      env: staging
    on_failure: rollback
  - name: rollback
    command: longdrawer rollback
    target: "*.web.*"
`)

	engine := NewEngine(nil, nil, nil)
	if err := engine.Load(dir); err != nil {
		t.Fatal(err)
	}

	names := engine.List()
	if len(names) != 1 || names[0] != "deploy" {
		t.Errorf("unexpected names: %v", names)
	}

	def, ok := engine.Get("deploy")
	if !ok {
		t.Fatal("expected to find 'deploy' workflow")
	}
	if len(def.Steps) != 3 {
		t.Errorf("expected 3 steps, got %d", len(def.Steps))
	}
	if def.Steps[0].Name != "setup" {
		t.Errorf("expected step 0 name 'setup', got %s", def.Steps[0].Name)
	}
	if def.Steps[1].Task != "restart-app" {
		t.Errorf("expected step 1 task 'restart-app', got %s", def.Steps[1].Task)
	}
	if def.Steps[1].OnFailure != "rollback" {
		t.Errorf("expected step 1 on_failure 'rollback', got %s", def.Steps[1].OnFailure)
	}
	if def.Steps[1].Params["env"] != "staging" {
		t.Errorf("expected step 1 param env=staging")
	}
}

func TestEngine_LoadDefaultName(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "myflow.vgo", `
steps:
  - name: step1
    command: longdrawer ok
    target: "*"
`)

	engine := NewEngine(nil, nil, nil)
	if err := engine.Load(dir); err != nil {
		t.Fatal(err)
	}

	_, ok := engine.Get("myflow")
	if !ok {
		t.Error("expected workflow to use filename as default name")
	}
}

func TestEngine_LoadNoSteps(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "empty.vgo", `
name: empty
description: no steps
`)

	engine := NewEngine(nil, nil, nil)
	err := engine.Load(dir)
	if err == nil {
		t.Error("expected error for workflow without steps")
	}
}

func TestEngine_LoadMissingDir(t *testing.T) {
	engine := NewEngine(nil, nil, nil)
	if err := engine.Load("/nonexistent/path"); err != nil {
		t.Errorf("expected nil error for missing dir, got: %v", err)
	}
}

func TestEngine_RollingParams(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "rolling.vgo", `
name: rolling
steps:
  - name: deploy
    command: longdrawer deploy
    target: "*.web.*"
    batch_size: "2"
    max_failures: "1"
    health_check: "curl -s http://localhost/health"
`)

	engine := NewEngine(nil, nil, nil)
	if err := engine.Load(dir); err != nil {
		t.Fatal(err)
	}

	def, _ := engine.Get("rolling")
	step := def.Steps[0]
	if step.BatchSize != "2" {
		t.Errorf("expected batch_size 2, got %s", step.BatchSize)
	}
	if step.MaxFailures != "1" {
		t.Errorf("expected max_failures 1, got %s", step.MaxFailures)
	}
	if step.HealthCheck != "curl -s http://localhost/health" {
		t.Errorf("unexpected health_check: %s", step.HealthCheck)
	}
}

func TestEngine_AbortFlag(t *testing.T) {
	engine := NewEngine(nil, nil, nil)

	if engine.isAborted("run-1") {
		t.Error("should not be aborted initially")
	}

	engine.Abort("run-1")

	if !engine.isAborted("run-1") {
		t.Error("should be aborted after Abort()")
	}

	// Simulate cleanup.
	engine.abortFlags.Delete("run-1")
	if engine.isAborted("run-1") {
		t.Error("should not be aborted after cleanup")
	}
}

func TestEngine_OnSuccessOnFailure(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "branch.vgo", `
name: branch
steps:
  - name: check
    command: longdrawer check
    target: "*"
    on_success: deploy
    on_failure: alert
  - name: deploy
    command: longdrawer deploy
    target: "*"
  - name: alert
    command: longdrawer alert
    target: "*"
`)

	engine := NewEngine(nil, nil, nil)
	if err := engine.Load(dir); err != nil {
		t.Fatal(err)
	}

	def, _ := engine.Get("branch")
	if def.Steps[0].OnSuccess != "deploy" {
		t.Errorf("expected on_success=deploy, got %s", def.Steps[0].OnSuccess)
	}
	if def.Steps[0].OnFailure != "alert" {
		t.Errorf("expected on_failure=alert, got %s", def.Steps[0].OnFailure)
	}
}

// TestEngine_LoadRejectsDangerousStepCommand pins workflow-load
// guardrails: an ad-hoc command step that matches a dangerous pattern
// (one the agent's hardcoded guardrails would refuse anyway) fails
// the entire Load with a pointed error naming the workflow + step.
// Surfacing the block at publish time avoids the alternative — the
// workflow loads, runs partway, then a single step trips the agent
// guardrail with operators left to discover it at runtime.
func TestEngine_LoadRejectsDangerousStepCommand(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "bad.vgo", `
name: bad
steps:
  - name: stop-vigo
    command: systemctl stop vigo-envoy
    target: "*"
`)

	engine := NewEngine(nil, nil, nil)
	err := engine.Load(dir)
	if err == nil {
		t.Fatal("expected Load to fail on dangerous step command")
	}
	if !strings.Contains(err.Error(), "bad") || !strings.Contains(err.Error(), "stop-vigo") {
		t.Errorf("expected error to name workflow + step, got: %v", err)
	}
}

// TestEngine_LoadAllowsNamedTaskSteps pins the boundary: workflow load
// validates only step.Command. step.Task references stay opaque here —
// task-store-load enforces the same rule on the referenced script's
// content, so the chain (task store rejects dangerous → workflow can't
// dispatch unknown task at runtime) is the same operator-visible
// outcome without the engine needing a task-store reference.
func TestEngine_LoadAllowsNamedTaskSteps(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ok.vgo", `
name: ok
steps:
  - name: maybe-dangerous-named-task
    task: restart-vigo
    target: "*"
`)

	engine := NewEngine(nil, nil, nil)
	if err := engine.Load(dir); err != nil {
		t.Fatalf("expected Load to succeed for named-task step, got: %v", err)
	}
}
