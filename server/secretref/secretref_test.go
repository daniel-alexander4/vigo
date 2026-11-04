// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package secretref

import "testing"

func TestValidateKey(t *testing.T) {
	good := []string{"vigo/db/dsn", "a", "x/y.z", "a_b-c/d.e"}
	bad := []string{"", "..", "vigo/../x", ".master.key", "vigo/.hidden", "a//b", "/abs", "trailing/", "Upper/x", "sp ace"}
	for _, k := range good {
		if err := ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q) = %v, want nil", k, err)
		}
	}
	for _, k := range bad {
		if err := ValidateKey(k); err == nil {
			t.Errorf("ValidateKey(%q) = nil, want an error", k)
		}
	}
}

func TestScanEmbedded(t *testing.T) {
	spans := Scan("PW=secret:vigo/db/pw,DB=secret:x `secret:`")
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3: %#v", len(spans), spans)
	}
	if spans[0].Key != "vigo/db/pw" || spans[1].Key != "x" || spans[2].Key != "" {
		t.Fatalf("keys = %q %q %q", spans[0].Key, spans[1].Key, spans[2].Key)
	}
}
