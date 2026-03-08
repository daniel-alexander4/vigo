package db

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// cachedNode pairs a DB node with the time it was fetched.
type cachedNode struct {
	node      Node
	fetchedAt time.Time
}

// NodeCache provides a TTL-based in-memory cache for node lookups,
// eliminating repeated GetNodeByID queries during a single check-in cycle.
// Thread-safe for concurrent access from interceptors and handlers.
type NodeCache struct {
	mu      sync.RWMutex
	entries map[uuid.UUID]*cachedNode
	queries *Queries
	ttl     time.Duration
}

// NewNodeCache creates a node cache backed by the given Queries with the
// specified TTL. Entries are lazily evicted on access after TTL expires.
func NewNodeCache(queries *Queries, ttl time.Duration) *NodeCache {
	return &NodeCache{
		entries: make(map[uuid.UUID]*cachedNode),
		queries: queries,
		ttl:     ttl,
	}
}

// GetByID returns the node for the given UUID, serving from cache if the
// entry is fresh (within TTL). On cache miss or stale entry, fetches from DB
// and updates the cache. Returns pgx.ErrNoRows if the node does not exist.
func (nc *NodeCache) GetByID(ctx context.Context, id uuid.UUID) (Node, error) {
	nc.mu.RLock()
	if entry, ok := nc.entries[id]; ok && time.Since(entry.fetchedAt) < nc.ttl {
		n := entry.node
		nc.mu.RUnlock()
		return n, nil
	}
	nc.mu.RUnlock()

	// Cache miss or stale — fetch from DB.
	node, err := nc.queries.GetNodeByID(ctx, id)
	if err != nil {
		return Node{}, err
	}

	nc.mu.Lock()
	nc.entries[id] = &cachedNode{node: node, fetchedAt: time.Now()}
	nc.mu.Unlock()

	return node, nil
}

// Invalidate removes a node from the cache, forcing the next lookup to
// hit the database. Used after revocation to ensure immediate effect.
func (nc *NodeCache) Invalidate(id uuid.UUID) {
	nc.mu.Lock()
	delete(nc.entries, id)
	nc.mu.Unlock()
}

// nodeCtxKey is the context key for passing a cached node from the
// interceptor to the handler within a single request.
type nodeCtxKey struct{}

// ContextWithNode stores a Node in the context for reuse by downstream
// handlers, avoiding duplicate DB lookups within the same gRPC request.
func ContextWithNode(ctx context.Context, n Node) context.Context {
	return context.WithValue(ctx, nodeCtxKey{}, &n)
}

// NodeFromContext retrieves a Node previously stored by the interceptor.
// Returns nil if no node is in the context.
func NodeFromContext(ctx context.Context) *Node {
	n, _ := ctx.Value(nodeCtxKey{}).(*Node)
	return n
}

// IsNotFound returns true if the error is pgx.ErrNoRows.
func IsNotFound(err error) bool {
	return err == pgx.ErrNoRows
}
