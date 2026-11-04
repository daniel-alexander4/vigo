// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package curators3

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/swarm/curatormesh"
	"git.alexander4.io/vigo/server/swarm/puddlemesh"
)

// CatalogSource is the curator fleet-catalog accessor the S3 API reads — the
// canonical `(catalog, operator-block table)` snapshot. Satisfied by
// `*api.CuratorHandler`; declared as an interface because `server/api` imports
// this package, so this package cannot import `server/api` back.
type CatalogSource interface {
	FleetSnapshot(ctx context.Context) (*curatormesh.Catalog, []db.CuratorBlockedArtifact, error)
}

// Server is the read-only S3-compatible HTTP surface over curator artifacts
// (ADR-027). Every request is SigV4-verified, then dispatched to an S3 read
// operation over curator's catalog + the server's blob cache.
type Server struct {
	verifier   *Verifier
	catalog    CatalogSource
	puddle     *puddlemesh.Aggregator
	fleetIndex *fleet.FleetIndex
	blobsDir   string
	metrics    *metrics.Metrics
	names      nameCache
}

// NewServer wires the S3 endpoint. `puddleAgg` resolves the puddle-pubkey →
// friendly-name map (ADR-022) for `<publisher>` key rendering; `blobsDir` is
// the server's co-resident envoy blob cache.
func NewServer(v *Verifier, catalog CatalogSource, puddleAgg *puddlemesh.Aggregator, fleetIndex *fleet.FleetIndex, blobsDir string, m *metrics.Metrics) *Server {
	return &Server{verifier: v, catalog: catalog, puddle: puddleAgg, fleetIndex: fleetIndex, blobsDir: blobsDir, metrics: m}
}

// Handler returns the http.Handler the S3 listener serves.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.route) }

// route SigV4-verifies, then dispatches path-style S3 requests. Curator's
// publish surface stays `vigo swarm curator push`; every mutating method here
// is refused.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	resolved, err := s.verifier.Verify(r.Context(), r)
	if err != nil {
		var se *Error
		if errors.As(err, &se) {
			writeS3Error(w, r, se.HTTPStatus, se.Code, se.Message)
		} else {
			slog.Error("curator s3: verification fault", "error", err)
			writeS3Error(w, r, http.StatusInternalServerError, "InternalError", "internal error")
		}
		s.count("error")
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeS3Error(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed", "the curator S3 API is read-only")
		s.count("error")
		return
	}

	bucket, key, hasKey := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	switch {
	case bucket == "":
		s.listBuckets(w, r)
		s.count("list_buckets")
	case bucket != bucketName:
		writeS3Error(w, r, http.StatusNotFound, "NoSuchBucket", "the specified bucket does not exist")
		s.count("error")
	case !hasKey || key == "":
		if _, ok := r.URL.Query()["location"]; ok {
			s.getBucketLocation(w, r)
			s.count("bucket_location")
			return
		}
		if r.Method == http.MethodHead { // HeadBucket
			w.WriteHeader(http.StatusOK)
			s.count("head_bucket")
			return
		}
		s.listObjectsV2(w, r, resolved)
		s.count("list_objects")
	default:
		s.serveObject(w, r, resolved, key)
		s.count("get_object")
	}
}

func (s *Server) count(op string) {
	if s.metrics != nil && s.metrics.CuratorS3Requests != nil {
		s.metrics.CuratorS3Requests.WithLabelValues(op).Inc()
	}
}

// nameMaps returns the founder-pubkey-hex ⇄ friendly-name maps for
// unambiguously-claimed puddle names (ADR-022). Both empty on error — the
// facade then falls back to hex publishers, which always resolve.
func (s *Server) nameMaps(ctx context.Context) (byPubkey, byName map[string]string) {
	if s.puddle == nil {
		return map[string]string{}, map[string]string{}
	}
	return s.names.get(ctx, s.puddle, s.envoyInputs)
}

// NameResolver returns a friendly-name → founder-pubkey-hex lookup over the
// puddle aggregator, for pinning a credential's scope at mint time.
func NameResolver(agg *puddlemesh.Aggregator, fleetIndex *fleet.FleetIndex) func(ctx context.Context) map[string]string {
	var nc nameCache
	envoys := func() []puddlemesh.EnvoyInput { return envoyInputsOf(fleetIndex) }
	return func(ctx context.Context) map[string]string {
		_, byName := nc.get(ctx, agg, envoys)
		return byName
	}
}

// nameCache keeps the reverse maps derived from one FleetNames snapshot. The
// aggregator returns the same *FleetNames until its TTL rebuild, so the maps
// are recomputed only when the snapshot pointer changes, not per request.
type nameCache struct {
	mu       sync.Mutex
	src      *puddlemesh.FleetNames
	byPubkey map[string]string
	byName   map[string]string
}

func (c *nameCache) get(ctx context.Context, agg *puddlemesh.Aggregator, envoys func() []puddlemesh.EnvoyInput) (byPubkey, byName map[string]string) {
	fn, err := agg.NamesSnapshot(ctx, envoys())
	if err != nil || fn == nil {
		return map[string]string{}, map[string]string{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.src != fn {
		c.byPubkey = puddlemesh.ReverseNameMap(fn)
		c.byName = make(map[string]string, len(c.byPubkey))
		for pk, nm := range c.byPubkey {
			c.byName[nm] = pk
		}
		c.src = fn
	}
	return c.byPubkey, c.byName
}

func (s *Server) envoyInputs() []puddlemesh.EnvoyInput { return envoyInputsOf(s.fleetIndex) }

func envoyInputsOf(fleetIndex *fleet.FleetIndex) []puddlemesh.EnvoyInput {
	if fleetIndex == nil {
		return nil
	}
	envoys := fleetIndex.AllEnvoys()
	out := make([]puddlemesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, puddlemesh.EnvoyInput{ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked})
	}
	return out
}
