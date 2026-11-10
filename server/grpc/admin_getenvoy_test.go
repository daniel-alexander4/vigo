// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"context"
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/testutil"
)

// TestGetEnvoy_ReturnsPubkeyAndPin: spanner ReassignEnvoy re-enrolls the envoy
// on the target bolt from this response, and EnrollEnvoy rejects an empty
// pubkey — so GetEnvoy must carry the stored pubkey and cert pin.
func TestGetEnvoy_ReturnsPubkeyAndPin(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testutil.OpenMigratedTestDB(t))
	e, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{Hostname: "n.example.com", IpAddress: "10.0.0.2", PubkeyPem: "PEM-DATA"})
	if err != nil {
		t.Fatal(err)
	}
	pin := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := queries.UpdateEnvoyCertPin(ctx, db.UpdateEnvoyCertPinParams{ID: e.ID, CertSpkiSha256: pin}); err != nil {
		t.Fatal(err)
	}
	admin := &AdminService{queries: queries}
	resp, err := admin.GetEnvoy(ctx, &pb.GetEnvoyRequest{Id: e.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetPubkeyPem() != "PEM-DATA" || resp.GetCertSpkiSha256() != pin {
		t.Fatalf("GetEnvoy pubkey/pin = %q/%q, want stored values", resp.GetPubkeyPem(), resp.GetCertSpkiSha256())
	}
}
