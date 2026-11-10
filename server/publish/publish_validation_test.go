// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package publish

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/config"
)

// writeSelfProtectViolation writes a crate the loader rejects with a
// self-protection finding (an *config.IdempotencyErrors) that modlint does
// not catch — the loader-only failure path that used to reach .live.
func writeSelfProtectViolation(t *testing.T, stacks string) {
	t.Helper()
	writeFile(t, filepath.Join(stacks, "configcrates", "risky.vgo"), `
name: risky
resources:
  - name: stop-agent
    type: service
    service: vigo-envoy
    state: stopped
`)
	writeFile(t, filepath.Join(stacks, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [risky]
`)
}

func seedLive(t *testing.T, live string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(live, "configcrates"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(live, "configcrates", "existing.vgo")
	writeFile(t, p, "keep me")
	return p
}

// TestPublish_RefusesLoaderValidationErrors: a tree whose loader
// validation fails must not reach .live — the next server restart would
// otherwise start with an empty config for the whole fleet.
func TestPublish_RefusesLoaderValidationErrors(t *testing.T) {
	env := withTestDirs(t)
	writeSelfProtectViolation(t, env.stacks)
	existing := seedLive(t, env.live)

	res, err := Publish(noURLOpts(env))
	if err == nil {
		t.Fatal("Publish synced a tree with loader validation errors")
	}
	var vf *ValidationFailedError
	if !errors.As(err, &vf) {
		t.Fatalf("want *ValidationFailedError, got %T: %v", err, err)
	}
	var ie *config.IdempotencyErrors
	if !errors.As(err, &ie) {
		t.Errorf("error does not wrap *config.IdempotencyErrors (suggestFixes needs it): %v", err)
	}
	if res == nil || len(res.ValidationErrors) == 0 {
		t.Fatal("result with the failing files was not returned")
	}
	if _, statErr := os.Stat(filepath.Join(env.live, "configcrates", "risky.vgo")); !os.IsNotExist(statErr) {
		t.Error("risky.vgo reached .live despite validation errors")
	}
	if data, _ := os.ReadFile(existing); string(data) != "keep me" {
		t.Error("existing .live file was modified or removed")
	}
}

// TestValidateSource_ReportsLoaderValidationError: dry-run's path must
// see the failure (it used to print "Validation passed").
func TestValidateSource_ReportsLoaderValidationError(t *testing.T) {
	env := withTestDirs(t)
	writeSelfProtectViolation(t, env.stacks)

	res, err := ValidateSource(noURLOpts(env))
	if err != nil {
		t.Fatalf("ValidateSource: %v", err)
	}
	defer res.Cleanup()
	if res.ValidationError() == nil {
		t.Fatal("ValidationError() is nil for a tree with loader validation errors")
	}
	if len(res.Removed) != 0 {
		t.Errorf("prune ran against the empty failed store and removed %v", res.Removed)
	}

	ok := withTestDirs(t)
	writeValidConfig(t, ok.stacks)
	okRes, err := ValidateSource(noURLOpts(ok))
	if err != nil {
		t.Fatal(err)
	}
	defer okRes.Cleanup()
	if verr := okRes.ValidationError(); verr != nil {
		t.Fatalf("valid tree reported a validation error: %v", verr)
	}
}

func TestPublishFile_RefusesLoaderValidationErrors(t *testing.T) {
	env := withTestDirs(t)
	writeSelfProtectViolation(t, env.stacks)
	seedLive(t, env.live)

	_, err := PublishFile("configcrates/risky.vgo", noURLOpts(env))
	var vf *ValidationFailedError
	if !errors.As(err, &vf) {
		t.Fatalf("want *ValidationFailedError, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(env.live, "configcrates", "risky.vgo")); !os.IsNotExist(statErr) {
		t.Error("risky.vgo reached .live despite validation errors")
	}
}

func TestPublishFile_RejectsPathOutsideStacks(t *testing.T) {
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)
	// A file beside stacks/ that a `../` path would read.
	outside := filepath.Join(filepath.Dir(env.stacks), "outside.vgo")
	writeFile(t, outside, "name: outside\nresources: []\n")

	for _, p := range []string{"../outside.vgo", "configcrates/../../outside.vgo", outside, ".."} {
		_, err := PublishFile(p, noURLOpts(env))
		if err == nil || !strings.Contains(err.Error(), "not inside") {
			t.Errorf("PublishFile(%q) = %v, want containment refusal", p, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(env.live), "outside.vgo")); !os.IsNotExist(statErr) {
		t.Error("a file was written outside .live")
	}
}

func TestPublishFile_WritesCanonicalFile(t *testing.T) {
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)

	res, err := PublishFile("configcrates/nginx.vgo", noURLOpts(env))
	if err != nil {
		t.Fatalf("PublishFile: %v", err)
	}
	if res.Files != 1 || res.Configcrates != 1 {
		t.Errorf("result = %+v", res)
	}
	data, err := os.ReadFile(filepath.Join(env.live, "configcrates", "nginx.vgo"))
	if err != nil {
		t.Fatal(err)
	}
	// The bytes must match what a full Publish writes (the linted canonical
	// form), not the raw stacks bytes.
	full := withTestDirs(t)
	writeValidConfig(t, full.stacks)
	if _, err := Publish(noURLOpts(full)); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(full.live, "configcrates", "nginx.vgo"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(want) {
		t.Errorf("single-file publish bytes differ from full publish:\n got: %q\nwant: %q", data, want)
	}
}
