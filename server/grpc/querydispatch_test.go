// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
)

func TestQueryRouterRegisterAndRoute(t *testing.T) {
	router := newQueryRouter()

	resultCh := make(chan queryResult, 2)
	pq := &pendingQuery{
		resultCh:  resultCh,
		hostnames: map[string]string{"envoy-a": "web01.prod", "envoy-b": "web02.prod"},
	}
	router.register("query-1", pq)

	// Route a response.
	router.handleQueryResponse("envoy-a", &pb.QueryResponse{
		QueryId:   "query-1",
		TraitPath: "os.distro",
		Value:     "ubuntu",
		Found:     true,
	})

	select {
	case r := <-resultCh:
		if r.envoyID != "envoy-a" {
			t.Errorf("expected envoy-a, got %s", r.envoyID)
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
	router.handleQueryResponse("envoy-a", &pb.QueryResponse{
		QueryId: "nonexistent",
	})
}

func TestQueryRouterRemove(t *testing.T) {
	router := newQueryRouter()

	resultCh := make(chan queryResult, 1)
	pq := &pendingQuery{
		resultCh:  resultCh,
		hostnames: map[string]string{"envoy-a": "host"},
	}
	router.register("query-1", pq)
	router.remove("query-1")

	// After removal, response should be dropped silently.
	router.handleQueryResponse("envoy-a", &pb.QueryResponse{
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
		hostnames: map[string]string{"envoy-a": "web01"},
	}
	router.register("query-1", pq)

	router.handleQueryResponse("envoy-a", &pb.QueryResponse{
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
