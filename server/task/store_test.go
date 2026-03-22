// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package push

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTask(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestStore_Load(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "hello.yaml", `
name: hello
description: Say hello
timeout: 30
script: echo hello
parameters:
  name:
    type: string
    required: true
    description: Who to greet
  loud:
    type: bool
    default: "false"
`)
	writeTask(t, dir, "check.yml", `
name: check
script: uptime
`)

	store := NewStore()
	if err := store.Load(dir); err != nil {
		t.Fatal(err)
	}

	names := store.List()
	if len(names) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(names))
	}
	if names[0] != "check" || names[1] != "hello" {
		t.Errorf("unexpected names: %v", names)
	}

	def, ok := store.Get("hello")
	if !ok {
		t.Fatal("expected to find 'hello' task")
	}
	if def.Script != "echo hello" {
		t.Errorf("unexpected script: %s", def.Script)
	}
	if def.Timeout != 30 {
		t.Errorf("expected timeout 30, got %d", def.Timeout)
	}
	if len(def.Parameters) != 2 {
		t.Errorf("expected 2 params, got %d", len(def.Parameters))
	}
}

func TestStore_LoadDefaultName(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "mycheck.yaml", `
script: uptime
`)

	store := NewStore()
	if err := store.Load(dir); err != nil {
		t.Fatal(err)
	}

	_, ok := store.Get("mycheck")
	if !ok {
		t.Error("expected task to use filename as default name")
	}
}

func TestStore_LoadDefaultTimeout(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "fast.yaml", `
name: fast
script: echo fast
`)

	store := NewStore()
	if err := store.Load(dir); err != nil {
		t.Fatal(err)
	}

	def, _ := store.Get("fast")
	if def.Timeout != 60 {
		t.Errorf("expected default timeout 60, got %d", def.Timeout)
	}
}

func TestStore_LoadNoScript(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "bad.yaml", `
name: bad
description: no script
`)

	store := NewStore()
	err := store.Load(dir)
	if err == nil {
		t.Error("expected error for task without script")
	}
}

func TestStore_LoadMissingDir(t *testing.T) {
	store := NewStore()
	if err := store.Load("/nonexistent/path"); err != nil {
		t.Errorf("expected nil error for missing dir, got: %v", err)
	}
}

func TestValidateParams_Valid(t *testing.T) {
	def := &PushDef{
		Parameters: map[string]ParamDef{
			"env":    {Type: "string", Required: true},
			"force":  {Type: "bool", Default: "false"},
		},
	}

	err := ValidateParams(def, map[string]string{"env": "staging"})
	if err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestValidateParams_MissingRequired(t *testing.T) {
	def := &PushDef{
		Parameters: map[string]ParamDef{
			"env": {Type: "string", Required: true},
		},
	}

	err := ValidateParams(def, map[string]string{})
	if err == nil {
		t.Error("expected error for missing required param")
	}
	if err.Error() != "missing required parameters: env" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateParams_Unknown(t *testing.T) {
	def := &PushDef{
		Parameters: map[string]ParamDef{
			"env": {Type: "string"},
		},
	}

	err := ValidateParams(def, map[string]string{"env": "staging", "bogus": "val"})
	if err == nil {
		t.Error("expected error for unknown param")
	}
	if err.Error() != "unknown parameters: bogus" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateParams_RequiredWithDefault(t *testing.T) {
	def := &PushDef{
		Parameters: map[string]ParamDef{
			"env": {Type: "string", Required: true, Default: "production"},
		},
	}

	err := ValidateParams(def, map[string]string{})
	if err != nil {
		t.Errorf("expected valid (has default), got: %v", err)
	}
}

func TestBuildEnv(t *testing.T) {
	def := &PushDef{
		Parameters: map[string]ParamDef{
			"env":       {Type: "string"},
			"force-run": {Type: "bool", Default: "false"},
		},
	}

	env := BuildEnv(def, map[string]string{"env": "staging"})
	if env["PARAM_ENV"] != "staging" {
		t.Errorf("expected PARAM_ENV=staging, got %s", env["PARAM_ENV"])
	}
	if env["PARAM_FORCE_RUN"] != "false" {
		t.Errorf("expected PARAM_FORCE_RUN=false, got %s", env["PARAM_FORCE_RUN"])
	}
	if len(env) != 2 {
		t.Errorf("expected 2 env vars, got %d", len(env))
	}
}

func TestBuildEnv_EmptyDefault(t *testing.T) {
	def := &PushDef{
		Parameters: map[string]ParamDef{
			"opt": {Type: "string"},
		},
	}

	env := BuildEnv(def, map[string]string{})
	if _, ok := env["PARAM_OPT"]; ok {
		t.Error("expected empty optional param to be omitted from env")
	}
}

func TestStore_All(t *testing.T) {
	dir := t.TempDir()
	writeTask(t, dir, "b.yaml", "name: b\nscript: echo b")
	writeTask(t, dir, "a.yaml", "name: a\nscript: echo a")

	store := NewStore()
	if err := store.Load(dir); err != nil {
		t.Fatal(err)
	}

	all := store.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(all))
	}
	if all[0].Name != "a" || all[1].Name != "b" {
		t.Errorf("expected sorted order [a, b], got [%s, %s]", all[0].Name, all[1].Name)
	}
}
