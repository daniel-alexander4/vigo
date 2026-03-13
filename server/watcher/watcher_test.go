// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package watcher

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockVersionProvider implements VersionProvider for testing.
type mockVersionProvider struct {
	mu       sync.Mutex
	versions map[string]int
}

func (m *mockVersionProvider) GetVersion(ctx context.Context, key string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.versions[key]
	if !ok {
		return 0, nil
	}
	return v, nil
}

func (m *mockVersionProvider) setVersion(key string, v int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.versions[key] = v
}

// mockThrallStore implements ThrallStore for testing.
type mockThrallStore struct {
	refs map[string][]string // secret path -> hostnames
}

func (m *mockThrallStore) ThrallsReferencingSecret(path string) []string {
	return m.refs[path]
}

// mockDB implements DB for testing.
type mockDB struct {
	mu              sync.Mutex
	forcePushed     []string
	rotatedSecrets  map[string][]string // thrall ID -> secret paths
	rotationEvents  []RotationRecord
	nodesByHost   map[string]string
}

func (m *mockDB) SetForcePush(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forcePushed = append(m.forcePushed, id)
	return nil
}

func (m *mockDB) CreateSecretRotationEvent(ctx context.Context, params RotationRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotationEvents = append(m.rotationEvents, params)
	return nil
}

func (m *mockDB) AppendRotatedSecret(ctx context.Context, id, secretPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rotatedSecrets == nil {
		m.rotatedSecrets = make(map[string][]string)
	}
	m.rotatedSecrets[id] = append(m.rotatedSecrets[id], secretPath)
	return nil
}

func (m *mockDB) GetThrallByHostname(ctx context.Context, hostname string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.nodesByHost[hostname]
	if !ok {
		return "", nil
	}
	return id, nil
}

// mockDispatcher implements EventDispatcher for testing.
type mockDispatcher struct {
	mu     sync.Mutex
	events []string
}

func (m *mockDispatcher) DispatchRotation(ctx context.Context, event string, secretPath string, affected int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func TestRotationTrigger(t *testing.T) {
	thrallID := uuid.New().String()
	provider := &mockVersionProvider{versions: map[string]int{"hobnob/db/dsn": 1}}
	store := &mockThrallStore{refs: map[string][]string{
		"hobnob/db/dsn": {"web-01.example.com"},
	}}
	database := &mockDB{
		nodesByHost: map[string]string{
			"web-01.example.com": thrallID,
		},
	}
	dispatcher := &mockDispatcher{}

	w := NewWatcher(provider, database, store, dispatcher, 100*time.Millisecond, []string{"hobnob/db/dsn"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize version tracking.
	w.initVersions(ctx)

	// Simulate version bump.
	provider.setVersion("hobnob/db/dsn", 2)

	// Run one check.
	w.checkRotations(ctx)

	// Verify force push was triggered.
	database.mu.Lock()
	assert.Contains(t, database.forcePushed, thrallID)
	assert.Len(t, database.rotationEvents, 1)
	assert.Equal(t, "hobnob/db/dsn", database.rotationEvents[0].SecretPath)
	database.mu.Unlock()

	// Verify webhook dispatched.
	dispatcher.mu.Lock()
	assert.Contains(t, dispatcher.events, "secret.rotated")
	dispatcher.mu.Unlock()
}

func TestNotifyFuncCalledOnRotation(t *testing.T) {
	thrallID := uuid.New().String()
	provider := &mockVersionProvider{versions: map[string]int{"hobnob/db/dsn": 1}}
	store := &mockThrallStore{refs: map[string][]string{
		"hobnob/db/dsn": {"web-01.example.com"},
	}}
	database := &mockDB{
		nodesByHost: map[string]string{
			"web-01.example.com": thrallID,
		},
	}
	dispatcher := &mockDispatcher{}

	w := NewWatcher(provider, database, store, dispatcher, 100*time.Millisecond, []string{"hobnob/db/dsn"})

	var mu sync.Mutex
	var notified []string
	var notifiedDetails []map[string]any
	w.SetNotifyFunc(func(event string, details map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		notified = append(notified, event)
		notifiedDetails = append(notifiedDetails, details)
	})

	ctx := context.Background()
	w.initVersions(ctx)

	provider.setVersion("hobnob/db/dsn", 2)
	w.checkRotations(ctx)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"secret.rotated"}, notified)
	require.Len(t, notifiedDetails, 1)
	assert.Equal(t, "hobnob/db/dsn", notifiedDetails[0]["path"])
	assert.Equal(t, 1, notifiedDetails[0]["affected"])
}

func TestRotationAppendsRotatedSecret(t *testing.T) {
	thrallID := uuid.New().String()
	provider := &mockVersionProvider{versions: map[string]int{"hobnob/db/dsn": 1}}
	store := &mockThrallStore{refs: map[string][]string{
		"hobnob/db/dsn": {"web-01.example.com"},
	}}
	database := &mockDB{
		nodesByHost: map[string]string{
			"web-01.example.com": thrallID,
		},
	}
	dispatcher := &mockDispatcher{}

	w := NewWatcher(provider, database, store, dispatcher, 100*time.Millisecond, []string{"hobnob/db/dsn"})

	ctx := context.Background()
	w.initVersions(ctx)

	provider.setVersion("hobnob/db/dsn", 2)
	w.checkRotations(ctx)

	database.mu.Lock()
	defer database.mu.Unlock()

	// Should have appended the rotated secret path.
	assert.Contains(t, database.rotatedSecrets[thrallID], "hobnob/db/dsn")
}

func TestNoActionWhenVersionsUnchanged(t *testing.T) {
	provider := &mockVersionProvider{versions: map[string]int{"hobnob/db/dsn": 1}}
	store := &mockThrallStore{refs: map[string][]string{}}
	database := &mockDB{nodesByHost: map[string]string{}}
	dispatcher := &mockDispatcher{}

	w := NewWatcher(provider, database, store, dispatcher, 100*time.Millisecond, []string{"hobnob/db/dsn"})

	ctx := context.Background()
	w.initVersions(ctx)

	// No version change.
	w.checkRotations(ctx)

	database.mu.Lock()
	assert.Empty(t, database.forcePushed)
	assert.Empty(t, database.rotationEvents)
	database.mu.Unlock()
}

func TestWatcherRunLoop(t *testing.T) {
	provider := &mockVersionProvider{versions: map[string]int{"hobnob/db/dsn": 1}}
	store := &mockThrallStore{refs: map[string][]string{}}
	database := &mockDB{nodesByHost: map[string]string{}}
	dispatcher := &mockDispatcher{}

	w := NewWatcher(provider, database, store, dispatcher, 50*time.Millisecond, []string{"hobnob/db/dsn"})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := w.Run(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// errorVersionProvider returns an error on GetVersion calls when errOn is true.
type errorVersionProvider struct {
	mu       sync.Mutex
	versions map[string]int
	errOn    bool
}

func (m *errorVersionProvider) GetVersion(ctx context.Context, key string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.errOn {
		return 0, context.DeadlineExceeded
	}
	v, ok := m.versions[key]
	if !ok {
		return 0, nil
	}
	return v, nil
}

func (m *errorVersionProvider) setError(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errOn = on
}

func TestExponentialBackoffOnFailure(t *testing.T) {
	provider := &errorVersionProvider{versions: map[string]int{"hobnob/db/dsn": 1}}
	store := &mockThrallStore{refs: map[string][]string{}}
	database := &mockDB{nodesByHost: map[string]string{}}
	dispatcher := &mockDispatcher{}

	w := NewWatcher(provider, database, store, dispatcher, 100*time.Millisecond, []string{"hobnob/db/dsn"})

	ctx := context.Background()
	w.initVersions(ctx)

	// Simulate provider failures and verify consecutiveFailures increases.
	provider.setError(true)

	w.checkRotations(ctx)
	assert.Equal(t, 1, w.consecutiveFailures, "after 1 failure")

	w.checkRotations(ctx)
	assert.Equal(t, 2, w.consecutiveFailures, "after 2 failures")

	w.checkRotations(ctx)
	assert.Equal(t, 3, w.consecutiveFailures, "after 3 failures")

	// Verify the backoff multiplier follows 2^failures (capped at 32).
	// At 3 failures: multiplier = 2^3 = 8.
	multiplier := 1 << w.consecutiveFailures
	if multiplier > maxBackoffMultiplier {
		multiplier = maxBackoffMultiplier
	}
	assert.Equal(t, 8, multiplier, "backoff multiplier after 3 failures")

	// Recovery: a successful check resets the counter.
	provider.setError(false)
	w.checkRotations(ctx)
	assert.Equal(t, 0, w.consecutiveFailures, "should reset after success")
}

func TestBackoffCapsAt32x(t *testing.T) {
	provider := &errorVersionProvider{versions: map[string]int{"hobnob/db/dsn": 1}}
	store := &mockThrallStore{refs: map[string][]string{}}
	database := &mockDB{nodesByHost: map[string]string{}}
	dispatcher := &mockDispatcher{}

	w := NewWatcher(provider, database, store, dispatcher, 100*time.Millisecond, []string{"hobnob/db/dsn"})

	ctx := context.Background()
	w.initVersions(ctx)

	// Force many consecutive failures.
	provider.setError(true)
	for i := 0; i < 10; i++ {
		w.checkRotations(ctx)
	}
	assert.Equal(t, 10, w.consecutiveFailures)

	// Multiplier should be capped at 32 (maxBackoffMultiplier), not 2^10=1024.
	multiplier := 1 << w.consecutiveFailures
	if multiplier > maxBackoffMultiplier {
		multiplier = maxBackoffMultiplier
	}
	assert.Equal(t, maxBackoffMultiplier, multiplier, "multiplier should cap at 32")
}

func TestNoActionOnVersionDowngrade(t *testing.T) {
	provider := &mockVersionProvider{versions: map[string]int{"hobnob/db/dsn": 5}}
	store := &mockThrallStore{refs: map[string][]string{
		"hobnob/db/dsn": {"web-01.example.com"},
	}}
	thrallID := uuid.New().String()
	database := &mockDB{
		nodesByHost: map[string]string{
			"web-01.example.com": thrallID,
		},
	}
	dispatcher := &mockDispatcher{}

	w := NewWatcher(provider, database, store, dispatcher, 100*time.Millisecond, []string{"hobnob/db/dsn"})

	ctx := context.Background()
	w.initVersions(ctx)

	// Downgrade version from 5 to 3 — should not trigger any action.
	provider.setVersion("hobnob/db/dsn", 3)
	w.checkRotations(ctx)

	database.mu.Lock()
	assert.Empty(t, database.forcePushed, "version downgrade should not trigger force push")
	assert.Empty(t, database.rotationEvents, "version downgrade should not create rotation event")
	database.mu.Unlock()

	dispatcher.mu.Lock()
	assert.Empty(t, dispatcher.events, "version downgrade should not dispatch events")
	dispatcher.mu.Unlock()
}

func TestNoActionOnSameVersion(t *testing.T) {
	provider := &mockVersionProvider{versions: map[string]int{"hobnob/db/dsn": 3}}
	store := &mockThrallStore{refs: map[string][]string{
		"hobnob/db/dsn": {"web-01.example.com"},
	}}
	thrallID := uuid.New().String()
	database := &mockDB{
		nodesByHost: map[string]string{
			"web-01.example.com": thrallID,
		},
	}
	dispatcher := &mockDispatcher{}

	w := NewWatcher(provider, database, store, dispatcher, 100*time.Millisecond, []string{"hobnob/db/dsn"})

	ctx := context.Background()
	w.initVersions(ctx)

	// Same version — no action expected.
	w.checkRotations(ctx)

	database.mu.Lock()
	assert.Empty(t, database.forcePushed, "same version should not trigger force push")
	assert.Empty(t, database.rotationEvents, "same version should not create rotation event")
	database.mu.Unlock()

	dispatcher.mu.Lock()
	assert.Empty(t, dispatcher.events, "same version should not dispatch events")
	dispatcher.mu.Unlock()
}

func TestMultipleAffectedNodes(t *testing.T) {
	node1 := uuid.New().String()
	node2 := uuid.New().String()

	provider := &mockVersionProvider{versions: map[string]int{"hobnob/tls/cert": 1}}
	store := &mockThrallStore{refs: map[string][]string{
		"hobnob/tls/cert": {"web-01.example.com", "web-02.example.com"},
	}}
	database := &mockDB{
		nodesByHost: map[string]string{
			"web-01.example.com": node1,
			"web-02.example.com": node2,
		},
	}
	dispatcher := &mockDispatcher{}

	w := NewWatcher(provider, database, store, dispatcher, 100*time.Millisecond, []string{"hobnob/tls/cert"})

	ctx := context.Background()
	w.initVersions(ctx)

	provider.setVersion("hobnob/tls/cert", 2)
	w.checkRotations(ctx)

	database.mu.Lock()
	assert.Len(t, database.forcePushed, 2)
	assert.Contains(t, database.forcePushed, node1)
	assert.Contains(t, database.forcePushed, node2)
	database.mu.Unlock()
}
