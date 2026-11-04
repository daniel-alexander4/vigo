// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestLocalProviderKeyContainment: no operation — read, version stat or
// delete — may touch a path outside the secrets directory.
func TestLocalProviderKeyContainment(t *testing.T) {
	p, dir := setupLocalProvider(t)
	defer p.Close()
	outside := filepath.Join(dir, "outside")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete("../outside"); err == nil {
		t.Error("Delete(../outside) succeeded, want containment error")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("file outside the secrets dir was removed: %v", err)
	}
	if _, err := p.GetVersion(context.Background(), "../outside"); err == nil {
		t.Error("GetVersion(../outside) succeeded, want containment error")
	}
	if _, err := p.Get(context.Background(), "../outside"); err == nil {
		t.Error("Get(../outside) succeeded, want containment error")
	}
}
