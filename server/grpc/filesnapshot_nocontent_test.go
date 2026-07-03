// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"git.alexander4.io/vigo/server/db"
	"github.com/google/uuid"
)

// TestFileSnapshot_NoContentColumn locks the 000027 change: a file snapshot
// records the rollback existence marker (path + content_hash) with no content
// payload at all. Shipping/storing the prior file content was a plaintext
// secret leak that nothing read back; InsertFileSnapshot no longer takes a
// content argument, and the column is gone from the schema.
func TestFileSnapshot_NoContentColumn(t *testing.T) {
	ctx := context.Background()
	queries := db.New(testDB)

	envoy, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{
		Hostname:  fmt.Sprintf("snap-%s.example.com", uuid.NewString()[:8]),
		IpAddress: "10.99.0.9",
		PubkeyPem: "test-pubkey",
	})
	if err != nil {
		t.Fatalf("CreateEnvoy: %v", err)
	}

	if err := queries.InsertFileSnapshot(ctx, db.InsertFileSnapshotParams{
		EnvoyID:     envoy.ID,
		FilePath:    "/etc/app/secret.key",
		ContentHash: "abc123",
		RunID:       sql.NullString{Valid: false},
	}); err != nil {
		t.Fatalf("InsertFileSnapshot: %v", err)
	}

	snaps, err := queries.ListFileSnapshots(ctx, db.ListFileSnapshotsParams{
		EnvoyID:  envoy.ID,
		FilePath: "/etc/app/secret.key",
	})
	if err != nil {
		t.Fatalf("ListFileSnapshots: %v", err)
	}
	// The existence marker Rollback relies on is present…
	if len(snaps) != 1 || snaps[0].ContentHash != "abc123" {
		t.Fatalf("expected 1 snapshot with hash abc123, got %+v", snaps)
	}
	// …and there is no content field to leak plaintext into (compile-time: the
	// struct no longer has a Content member — referencing snaps[0].Content here
	// would fail to build, which is the guarantee).
}
