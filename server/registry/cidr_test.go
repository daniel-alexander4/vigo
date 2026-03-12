// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package registry

import "testing"

func TestMatchesCIDR(t *testing.T) {
	tests := []struct {
		name  string
		ip    string
		cidrs []string
		want  bool
	}{
		{"match single CIDR", "10.0.1.5", []string{"10.0.1.0/24"}, true},
		{"no match", "10.0.2.5", []string{"10.0.1.0/24"}, false},
		{"match second CIDR", "172.16.5.1", []string{"10.0.0.0/8", "172.16.0.0/12"}, true},
		{"empty CIDRs", "10.0.1.5", []string{}, false},
		{"invalid IP", "not-an-ip", []string{"10.0.0.0/8"}, false},
		{"invalid CIDR", "10.0.1.5", []string{"bad-cidr"}, false},
		{"ipv6 match", "::1", []string{"::1/128"}, true},
		{"broad match", "192.168.50.1", []string{"0.0.0.0/0"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesCIDR(tt.ip, tt.cidrs)
			if got != tt.want {
				t.Errorf("matchesCIDR(%q, %v) = %v, want %v", tt.ip, tt.cidrs, got, tt.want)
			}
		})
	}
}
