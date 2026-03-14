// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

// queryRouter tracks in-flight live queries and routes incoming
// QueryResponse messages from agents to the waiting caller.
type queryRouter struct {
	mu      sync.Mutex
	pending map[string]*pendingQuery // queryID → pending
}

type pendingQuery struct {
	resultCh  chan queryResult
	hostnames map[string]string // thrallID → hostname
}

type queryResult struct {
	thrallID string
	hostname string
	value    string
	found    bool
}

func newQueryRouter() *queryRouter {
	return &queryRouter{
		pending: make(map[string]*pendingQuery),
	}
}

func (r *queryRouter) register(queryID string, pq *pendingQuery) {
	r.mu.Lock()
	r.pending[queryID] = pq
	r.mu.Unlock()
}

func (r *queryRouter) remove(queryID string) {
	r.mu.Lock()
	delete(r.pending, queryID)
	r.mu.Unlock()
}

// handleQueryResponse routes an incoming QueryResponse to the waiting caller.
func (r *queryRouter) handleQueryResponse(thrallID string, resp *pb.QueryResponse) {
	r.mu.Lock()
	pq, ok := r.pending[resp.QueryId]
	r.mu.Unlock()
	if !ok {
		slog.Warn("received query response for unknown query",
			"query_id", resp.QueryId, "thrall_id", thrallID)
		return
	}

	hostname := pq.hostnames[thrallID]

	select {
	case pq.resultCh <- queryResult{
		thrallID: thrallID,
		hostname: hostname,
		value:    resp.Value,
		found:    resp.Found,
	}:
	default:
	}
}

// QueryEntry is a single thrall's result in a live query.
type QueryEntry struct {
	ThrallID    string `json:"thrallId"`
	Hostname    string `json:"hostname"`
	Value       string `json:"value,omitempty"`
	Found       bool   `json:"found"`
	CollectedAt string `json:"collectedAt,omitempty"`
	Stale       bool   `json:"stale,omitempty"`
	Status      string `json:"status"`
}

// QueryCounts holds the breakdown of query result categories.
type QueryCounts struct {
	Responded int `json:"responded"`
	TimedOut  int `json:"timedOut"`
	Offline   int `json:"offline"`
}

// LiveQueryResult holds the full result of a live query.
type LiveQueryResult struct {
	QueryID   string       `json:"queryId"`
	TraitPath string       `json:"traitPath"`
	Responded []QueryEntry `json:"responded"`
	TimedOut  []QueryEntry `json:"timedOut"`
	Offline   []QueryEntry `json:"offline"`
	Counts    QueryCounts  `json:"counts"`
}

// DispatchQuery resolves targets, dispatches QueryRequest to connected agents,
// fetches cached traits for offline agents, and collects responses with a timeout.
func (s *AgentService) DispatchQuery(ctx context.Context, traitPath, targetSpec string, timeout time.Duration) (*LiveQueryResult, error) {
	targets, err := resolveTargets(ctx, s.queries, targetSpec, s.fleetIndex)
	if err != nil {
		return nil, err
	}

	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	queryID := uuid.New().String()
	connected := s.streamMgr.connectedThrallIDs()

	// Separate connected, promotable, and offline targets.
	var connectedTargets, promotableTargets, offlineTargets []targetInfo
	for _, t := range targets {
		if connected[t.ThrallID] {
			connectedTargets = append(connectedTargets, t)
		} else if s.isRecentlyActive(t.ThrallID) {
			promotableTargets = append(promotableTargets, t)
		} else {
			offlineTargets = append(offlineTargets, t)
		}
	}

	// Register pending query (connected + promotable may respond).
	dispatchable := append(connectedTargets, promotableTargets...)
	hostnames := make(map[string]string, len(dispatchable))
	for _, t := range dispatchable {
		hostnames[t.ThrallID] = t.Hostname
	}
	resultCh := make(chan queryResult, len(dispatchable))
	pq := &pendingQuery{resultCh: resultCh, hostnames: hostnames}
	s.queryRouter.register(queryID, pq)
	defer s.queryRouter.remove(queryID)

	// Dispatch to connected thralls.
	dispatched := make(map[string]targetInfo)
	for _, t := range connectedTargets {
		msg := &pb.ServerStreamMessage{
			Msg: &pb.ServerStreamMessage_QueryRequest{
				QueryRequest: &pb.QueryRequest{
					QueryId:   queryID,
					TraitPath: traitPath,
				},
			},
		}
		if s.streamMgr.dispatchTask(t.ThrallID, msg) {
			dispatched[t.ThrallID] = t
		} else {
			offlineTargets = append(offlineTargets, t)
		}
	}

	// Queue promotable thralls for stream promotion.
	for _, t := range promotableTargets {
		msg := &pb.ServerStreamMessage{
			Msg: &pb.ServerStreamMessage_QueryRequest{
				QueryRequest: &pb.QueryRequest{
					QueryId:   queryID,
					TraitPath: traitPath,
				},
			},
		}
		thrallID := t.ThrallID
		s.streamMgr.queueForPromotion(t.ThrallID, msg, func() {
			// On timeout, promotable target becomes offline — query timeout
			// handles this naturally (results won't arrive).
			slog.Debug("query promotion timed out", "thrall_id", thrallID, "query_id", queryID)
		})
		dispatched[t.ThrallID] = t
	}

	// Fetch cached traits for offline thralls.
	pointer := "/" + strings.ReplaceAll(traitPath, ".", "/")
	var offline []QueryEntry
	for _, t := range offlineTargets {
		entry := QueryEntry{
			ThrallID: t.ThrallID,
			Hostname: t.Hostname,
			Status:   "offline",
		}
		traits, dbErr := s.queries.GetLatestThrallTraits(ctx, t.ThrallID)
		if dbErr == nil {
			var traitsMap map[string]any
			if json.Unmarshal([]byte(traits.Traits), &traitsMap) == nil {
				val := jsonPointerLookup(traitsMap, pointer)
				if val != nil {
					entry.Value = fmt.Sprint(val)
					entry.Found = true
				}
			}
			entry.CollectedAt = traits.CollectedAt.Format(time.RFC3339)
			entry.Stale = true
		}
		offline = append(offline, entry)
	}

	// Collect responses with timeout.
	var responded []QueryEntry
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	remaining := len(dispatched)
	for remaining > 0 {
		select {
		case r := <-resultCh:
			remaining--
			delete(dispatched, r.thrallID)
			responded = append(responded, QueryEntry{
				ThrallID: r.thrallID,
				Hostname: r.hostname,
				Value:    r.value,
				Found:    r.found,
				Status:   "responded",
			})
		case <-timer.C:
			remaining = 0
		case <-ctx.Done():
			remaining = 0
		}
	}

	// Mark remaining dispatched-but-non-responsive as timed_out.
	var timedOut []QueryEntry
	for _, t := range dispatched {
		timedOut = append(timedOut, QueryEntry{
			ThrallID: t.ThrallID,
			Hostname: t.Hostname,
			Status:   "timed_out",
		})
	}

	slog.Info("live query completed",
		"query_id", queryID,
		"trait_path", traitPath,
		"target_spec", targetSpec,
		"responded", len(responded),
		"timed_out", len(timedOut),
		"offline", len(offline),
	)

	return &LiveQueryResult{
		QueryID:   queryID,
		TraitPath: traitPath,
		Responded: responded,
		TimedOut:  timedOut,
		Offline:   offline,
		Counts: QueryCounts{
			Responded: len(responded),
			TimedOut:  len(timedOut),
			Offline:   len(offline),
		},
	}, nil
}
