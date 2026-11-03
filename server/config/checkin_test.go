// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

func TestCheckInSection_Validate_StreamSignature(t *testing.T) {
	tests := []struct {
		name    string
		mode    StreamSignatureMode
		wantErr bool
	}{
		{name: "empty defaults to all", mode: ""},
		{name: "all", mode: StreamSigAll},
		{name: "handshake-only", mode: StreamSigHandshakeOnly},
		{name: "typo rejected", mode: "handshakeonly", wantErr: true},
		{name: "bogus rejected", mode: "off", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckInSection{StreamSignature: tt.mode}.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
