package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIClientGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/nodes" {
			t.Errorf("expected /api/v1/nodes, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"nodes": []map[string]any{
				{"id": "abc-123", "hostname": "web1.example.com"},
			},
		})
	}))
	defer ts.Close()

	serverURL = ts.URL
	c := newAPIClient()

	var resp struct {
		Nodes []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		} `json:"nodes"`
	}
	if err := c.get("/api/v1/nodes", &resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(resp.Nodes))
	}
	if resp.Nodes[0].ID != "abc-123" {
		t.Errorf("expected id abc-123, got %s", resp.Nodes[0].ID)
	}
}

func TestAPIClientPost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json content type, got %s", r.Header.Get("Content-Type"))
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["pattern"] != "*.web.example.com" {
			t.Errorf("expected pattern *.web.example.com, got %s", body["pattern"])
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": "pat-1"})
	}))
	defer ts.Close()

	serverURL = ts.URL
	c := newAPIClient()

	var resp struct {
		ID string `json:"id"`
	}
	err := c.post("/api/v1/approved", map[string]string{"pattern": "*.web.example.com"}, &resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "pat-1" {
		t.Errorf("expected id pat-1, got %s", resp.ID)
	}
}

func TestAPIClientDelete(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/approved/pat-1" {
			t.Errorf("expected /api/v1/approved/pat-1, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}))
	defer ts.Close()

	serverURL = ts.URL
	c := newAPIClient()

	if err := c.delete("/api/v1/approved/pat-1", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAPIClientErrorResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message":"not found"}`))
	}))
	defer ts.Close()

	serverURL = ts.URL
	c := newAPIClient()

	err := c.get("/api/v1/nodes/missing", nil)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	expected := `server returned 404: not found`
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestAPIClientServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer ts.Close()

	serverURL = ts.URL
	c := newAPIClient()

	err := c.post("/api/v1/push", map[string]string{}, nil)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	expected := "server returned 500: internal error"
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}
