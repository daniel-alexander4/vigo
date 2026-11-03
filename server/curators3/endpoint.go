// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package curators3

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

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
	byPubkey, byName = map[string]string{}, map[string]string{}
	if s.puddle == nil {
		return
	}
	fn, err := s.puddle.NamesSnapshot(ctx, s.envoyInputs())
	if err != nil || fn == nil {
		return
	}
	byPubkey = puddlemesh.ReverseNameMap(fn)
	for pk, nm := range byPubkey {
		byName[nm] = pk
	}
	return
}

func (s *Server) envoyInputs() []puddlemesh.EnvoyInput {
	if s.fleetIndex == nil {
		return nil
	}
	envoys := s.fleetIndex.AllEnvoys()
	out := make([]puddlemesh.EnvoyInput, 0, len(envoys))
	for _, e := range envoys {
		out = append(out, puddlemesh.EnvoyInput{ID: e.ID, Hostname: e.Hostname, Revoked: e.Revoked})
	}
	return out
}
