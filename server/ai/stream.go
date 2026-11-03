// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// SSEWriter writes server-sent events to an HTTP response.
type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSEWriter creates an SSEWriter from an http.ResponseWriter.
// Returns nil if the response writer does not support flushing.
func NewSSEWriter(w http.ResponseWriter) *SSEWriter {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	return &SSEWriter{w: w, flusher: flusher}
}

// Send writes an SSE event.
func (s *SSEWriter) Send(event SSEEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event.Type, data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}
