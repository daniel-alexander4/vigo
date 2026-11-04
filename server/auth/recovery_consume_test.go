// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.alexander4.io/vigo/server/db"
)

// TestConsumeRecoveryCode_ConcurrentReplayOnlyOneWins: many requests that
// validated the same recovery code against the same stored set race to
// consume it; exactly one may succeed.
func TestConsumeRecoveryCode_ConcurrentReplayOnlyOneWins(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	ctx := context.Background()
	u, err := store.Create(ctx, db.InsertUserParams{Username: "rc-user", Role: "viewer"})
	require.NoError(t, err)

	const before, after = `["h1","h2"]`, `["h2"]`
	require.NoError(t, store.EnableTOTP(ctx, u.ID, before, 0))

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.ConsumeRecoveryCode(ctx, u.ID, before, after)
			if err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load(), "exactly one consume of a code may succeed")

	got, err := store.GetRecoveryCodes(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, after, got)
}

// TestConsumeRecoveryCode_StaleSetRejected: a consume computed against a
// set that has since changed is refused.
func TestConsumeRecoveryCode_StaleSetRejected(t *testing.T) {
	queries := setupTestDB(t)
	store := NewUserStore(queries)
	ctx := context.Background()
	u, err := store.Create(ctx, db.InsertUserParams{Username: "rc-stale", Role: "viewer"})
	require.NoError(t, err)
	require.NoError(t, store.EnableTOTP(ctx, u.ID, `["h1"]`, 0))

	ok, err := store.ConsumeRecoveryCode(ctx, u.ID, `["h1","h2"]`, `["h2"]`)
	require.NoError(t, err)
	assert.False(t, ok)
}
