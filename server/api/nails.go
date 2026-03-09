package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hobnob/hobnob/server/db"
)

// NailsHandler serves the REST endpoint for node nail inspection.
type NailsHandler struct {
	queries *db.Queries
}

// NewNailsHandler creates a NailsHandler using the given database queries.
func NewNailsHandler(queries *db.Queries) *NailsHandler {
	return &NailsHandler{queries: queries}
}

// newNailsHandlerWithDB creates a NailsHandler from a raw DBTX, used in tests
// to inject mock database connections.
func newNailsHandlerWithDB(dbtx db.DBTX) *NailsHandler {
	return &NailsHandler{queries: db.New(dbtx)}
}

// RegisterRoutes registers the nails endpoint on the given mux.
func (h *NailsHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/nodes/{hostname}/nails", h.HandleGetNails)
}

// nailsResponse is the JSON envelope returned by the nails endpoint.
type nailsResponse struct {
	Hostname    string          `json:"hostname"`
	CollectedAt string          `json:"collected_at"`
	Nails       json.RawMessage `json:"nails"`
}

// HandleGetNails returns the latest collected nails for a node, looked up
// by hostname. Returns 404 if the node doesn't exist or hasn't reported
// nails yet.
func (h *NailsHandler) HandleGetNails(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	ctx := r.Context()

	node, err := h.queries.GetNodeByHostname(ctx, hostname)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("node '%s' not found", hostname))
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	nails, err := h.queries.GetLatestNodeNails(ctx, node.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound,
				fmt.Sprintf("no nails available for '%s' -- node has not reported nails yet", hostname))
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := nailsResponse{
		Hostname:    hostname,
		CollectedAt: nails.CollectedAt.Format(time.RFC3339),
		Nails:       nails.Nails,
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
