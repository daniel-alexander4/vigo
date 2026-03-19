// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package db

import "context"

// AppendRotatedSecret adds a secret path to a envoy's pending rotation list.
// Uses json_each to avoid duplicates in the JSON array.
func (q *Queries) AppendRotatedSecret(ctx context.Context, id, secretPath string) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE envoys
		SET pending_rotated_secrets = json_insert(pending_rotated_secrets, '$[#]', ?)
		WHERE id = ?
		AND NOT EXISTS (SELECT 1 FROM json_each(envoys.pending_rotated_secrets) WHERE value = ?)`,
		secretPath, id, secretPath)
	return err
}

// ClearRotatedSecrets resets a envoy's pending rotation list after delivery.
func (q *Queries) ClearRotatedSecrets(ctx context.Context, id string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE envoys SET pending_rotated_secrets = '[]' WHERE id = ?`, id)
	return err
}
