// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// TestVaultNotInitializedErrorMessage pins the operator-facing message so
// future edits don't quietly drop the "sudo vigocli secrets init"
// guidance. Both call sites (cli/spanner.go, cli/secrets_unlock.go) route
// through this helper, so one assertion covers them.
func TestVaultNotInitializedErrorMessage(t *testing.T) {
	got := vaultNotInitializedError().Error()
	if !strings.Contains(got, `secrets vault not initialized`) {
		t.Errorf("error missing summary: %q", got)
	}
	if !strings.Contains(got, `sudo vigocli secrets init`) {
		t.Errorf("error missing actionable command: %q", got)
	}
}

// TestSecretsSetUseString catches regressions on the cobra Use string —
// the closed type set must be visible in the usage banner cobra prints on
// arg-count errors, not hidden in --help only.
func TestSecretsSetUseString(t *testing.T) {
	want := "set <raw|password> <key>"
	if secretsSetCmd.Use != want {
		t.Errorf("secretsSetCmd.Use = %q, want %q", secretsSetCmd.Use, want)
	}
}

// TestSecretsInitDefaultKeyFile guards against a regression where --key-file
// becomes required again — a stock install must `sudo vigocli secrets init`
// without flags.
func TestSecretsInitDefaultKeyFile(t *testing.T) {
	flag := secretsInitCmd.Flags().Lookup("key-file")
	if flag == nil {
		t.Fatal("--key-file flag missing on secrets init")
	}
	if flag.DefValue == "" {
		t.Errorf("--key-file has no default (empty string) — operators must pass it explicitly, regression of the install foot-gun")
	}
	want := "/srv/vigo/secrets/.master.key"
	if flag.DefValue != want {
		t.Errorf("--key-file default = %q, want %q (the server's auto-generated path)", flag.DefValue, want)
	}
	// Cobra exposes "required" status via the annotation map.
	if annotations := flag.Annotations["cobra_annotation_bash_completion_one_required_flag"]; len(annotations) > 0 && annotations[0] == "true" {
		t.Error("--key-file should not be MarkFlagRequired — defeats the point of the default")
	}
}

// TestRequireVaultUnlockedForWebusers_NotInitialized confirms the webuser
// password gate routes through the shared vault-not-initialized helper.
func TestRequireVaultUnlockedForWebusers_NotInitialized(t *testing.T) {
	withTempPaths(t)

	err := requireVaultUnlockedForWebusers()
	if err == nil {
		t.Fatal("expected error when vault not initialized")
	}
	if !strings.Contains(err.Error(), "secrets vault not initialized") {
		t.Errorf("error not from the shared helper: %q", err)
	}
}

// TestRequireVaultUnlockedForWebusers_Locked confirms the gate's second
// check fires with the unlock command in the message.
func TestRequireVaultUnlockedForWebusers_Locked(t *testing.T) {
	withTempPaths(t)
	if err := writeVerifier("CorrectHorseBattery$1!"); err != nil {
		t.Fatalf("writeVerifier: %v", err)
	}
	// Verifier exists but no sentinel → locked.
	err := requireVaultUnlockedForWebusers()
	if err == nil {
		t.Fatal("expected error when vault locked")
	}
	if !strings.Contains(err.Error(), "vigocli secrets unlock") {
		t.Errorf("error missing unlock command: %q", err)
	}
}

// TestWebuserPasswordKey pins the secrets-vault key shape against
// server/auth/basic.go:188. If basic.go's key format changes without this
// helper updating, the test catches it (string-literal mismatch).
func TestWebuserPasswordKey(t *testing.T) {
	got := webuserPasswordKey("dan")
	want := "vigo/web/auth/dan"
	if got != want {
		t.Errorf("webuserPasswordKey(\"dan\") = %q, want %q (must match server/auth/basic.go:188)", got, want)
	}
}
