// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"testing"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

func TestQueryRouterRegisterAndRoute(t *testing.T) {
	router := newQueryRouter()

	resultCh := make(chan queryResult, 2)
	pq := &pendingQuery{
		resultCh:  resultCh,
		hostnames: map[string]string{"thrall-a": "web01.prod", "thrall-b": "web02.prod"},
	}
	router.register("query-1", pq)

	// Route a response.
	router.handleQueryResponse("thrall-a", &pb.QueryResponse{
		QueryId:   "query-1",
		TraitPath: "os.distro",
		Value:     "ubuntu",
		Found:     true,
	})

	select {
	case r := <-resultCh:
		if r.thrallID != "thrall-a" {
			t.Errorf("expected thrall-a, got %s", r.thrallID)
		}
		if r.hostname != "web01.prod" {
			t.Errorf("expected web01.prod, got %s", r.hostname)
		}
		if r.value != "ubuntu" {
			t.Errorf("expected ubuntu, got %s", r.value)
		}
		if !r.found {
			t.Error("expected found=true")
		}
	default:
		t.Error("expected result on channel")
	}
}

func TestQueryRouterUnknownQuery(t *testing.T) {
	router := newQueryRouter()

	// Should not panic for unknown query.
	router.handleQueryResponse("thrall-a", &pb.QueryResponse{
		QueryId: "nonexistent",
	})
}

func TestQueryRouterRemove(t *testing.T) {
	router := newQueryRouter()

	resultCh := make(chan queryResult, 1)
	pq := &pendingQuery{
		resultCh:  resultCh,
		hostnames: map[string]string{"thrall-a": "host"},
	}
	router.register("query-1", pq)
	router.remove("query-1")

	// After removal, response should be dropped silently.
	router.handleQueryResponse("thrall-a", &pb.QueryResponse{
		QueryId: "query-1",
		Value:   "test",
		Found:   true,
	})

	select {
	case <-resultCh:
		t.Error("expected no result after removal")
	default:
	}
}

func TestQueryRouterNotFoundResponse(t *testing.T) {
	router := newQueryRouter()

	resultCh := make(chan queryResult, 1)
	pq := &pendingQuery{
		resultCh:  resultCh,
		hostnames: map[string]string{"thrall-a": "web01"},
	}
	router.register("query-1", pq)

	router.handleQueryResponse("thrall-a", &pb.QueryResponse{
		QueryId:   "query-1",
		TraitPath: "nonexistent.path",
		Found:     false,
	})

	select {
	case r := <-resultCh:
		if r.found {
			t.Error("expected found=false")
		}
		if r.value != "" {
			t.Errorf("expected empty value, got %s", r.value)
		}
	default:
		t.Error("expected result on channel")
	}
}
