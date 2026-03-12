// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package checkin

import (
	"time"

	"github.com/cenkalti/backoff/v4"
)

// NewBackoff returns an exponential backoff configured for server unavailability.
// It retries indefinitely with initial 5s, max 5m intervals, and a 2x multiplier.
func NewBackoff() backoff.BackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 5 * time.Second
	b.MaxInterval = 5 * time.Minute
	b.MaxElapsedTime = 0 // never stop retrying
	b.Multiplier = 2.0
	return b
}
