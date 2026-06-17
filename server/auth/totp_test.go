// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package auth

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// TestValidateTOTPCodeStep verifies a valid code matches and reports the
// time-step it belongs to, and that the step is stable across the skew window
// — the property the DB-level replay guard relies on.
func TestValidateTOTPCodeStep(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("step@example.com")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}

	now := time.Unix(1_700_000_000, 0)
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}

	ok, step := ValidateTOTPCodeStep(code, secret, now)
	if !ok {
		t.Fatal("valid code rejected")
	}
	if want := now.Unix() / totpPeriod; step != want {
		t.Errorf("step = %d, want %d", step, want)
	}

	// A code generated a full period earlier still validates (skew window) and
	// reports its own, lower step — so the monotonic guard rejects it once the
	// current step has been consumed.
	prev := now.Add(-totpPeriod * time.Second)
	prevCode, _ := totp.GenerateCode(secret, prev)
	okPrev, prevStep := ValidateTOTPCodeStep(prevCode, secret, now)
	if !okPrev {
		t.Fatal("previous-step code rejected within skew window")
	}
	if prevStep >= step {
		t.Errorf("previous step %d not < current step %d", prevStep, step)
	}

	// A wrong code is rejected.
	if ok, _ := ValidateTOTPCodeStep("000000", secret, now); ok {
		// 000000 could in theory be a valid code; guard against flake by
		// only failing if it matches the real code's step.
		if real, _ := totp.GenerateCode(secret, now); real != "000000" {
			t.Error("wrong code accepted")
		}
	}
}
