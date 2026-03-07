package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/isogov/isogov/server/db"
)

// TraitsHandler serves the REST endpoint for client trait inspection.
type TraitsHandler struct {
	queries *db.Queries
}

// NewTraitsHandler creates a TraitsHandler using the given database queries.
func NewTraitsHandler(queries *db.Queries) *TraitsHandler {
	return &TraitsHandler{queries: queries}
}

// newTraitsHandlerWithDB creates a TraitsHandler from a raw DBTX, used in tests
// to inject mock database connections.
func newTraitsHandlerWithDB(dbtx db.DBTX) *TraitsHandler {
	return &TraitsHandler{queries: db.New(dbtx)}
}

// RegisterRoutes registers the traits endpoint on the given mux.
func (h *TraitsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/clients/{hostname}/traits", h.HandleGetTraits)
}

// traitsResponse is the JSON envelope returned by the traits endpoint.
type traitsResponse struct {
	Hostname    string          `json:"hostname"`
	CollectedAt string          `json:"collected_at"`
	Traits      json.RawMessage `json:"traits"`
}

// HandleGetTraits returns the latest collected traits for a client, looked up
// by hostname. Returns 404 if the client doesn't exist or hasn't reported
// traits yet.
func (h *TraitsHandler) HandleGetTraits(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	ctx := r.Context()

	client, err := h.queries.GetClientByHostname(ctx, hostname)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("client '%s' not found", hostname))
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	facts, err := h.queries.GetLatestClientFacts(ctx, client.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound,
				fmt.Sprintf("no traits available for '%s' -- client has not reported traits yet", hostname))
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := traitsResponse{
		Hostname:    hostname,
		CollectedAt: facts.CollectedAt.Format(time.RFC3339),
		Traits:      facts.Facts,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// writeJSONError writes a JSON error response with the given status code.
func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
