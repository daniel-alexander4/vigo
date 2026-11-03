// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import "testing"

func TestValidProjectIDHex(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"a", false},
		{"deadbeef", false},
		{"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff", true},
		{"0011223344556677" + "8899aabbccddeeff" + "0011223344556677" + "8899aabbccddeefg", false}, // bad hex
		{"00112233445566778899AABBCCDDEEFF00112233445566778899AABBCCDDEEFF", false},                // uppercase
		{"00112233445566778899aabbccddeeff00112233445566778899aabbccddeeffXX", false},              // too long
		{"00112233445566778899aabbccddeeff00112233445566778899aabbccddee", false},                  // too short
	}
	for _, c := range cases {
		if got := validProjectIDHex(c.in); got != c.want {
			t.Errorf("validProjectIDHex(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
