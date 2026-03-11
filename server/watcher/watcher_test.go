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
