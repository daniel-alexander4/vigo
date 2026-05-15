// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package risk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// sampleNVDPayload mirrors the NVD 2.0 cve.id=… response — trimmed to
// the fields the extractor reads. Pinning the shape here surfaces an
// upstream API drift the day it happens rather than at next operator
// click.
const sampleNVDPayload = `{
  "vulnerabilities": [{
    "cve": {
      "id": "CVE-2024-1234",
      "published": "2024-03-15T10:00:00.000",
      "descriptions": [
        {"lang": "es", "value": "fuga de memoria"},
        {"lang": "en", "value": "Memory leak in TLS handshake parsing."}
      ],
      "metrics": {
        "cvssMetricV31": [{
          "cvssData": {
            "baseScore": 7.5,
            "vectorString": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H",
            "baseSeverity": "HIGH"
          }
        }],
        "cvssMetricV30": [{
          "cvssData": {
            "baseScore": 5.0,
            "vectorString": "older",
            "baseSeverity": "MEDIUM"
          }
        }]
      },
      "weaknesses": [
        {"description": [{"lang": "en", "value": "CWE-400"}]},
        {"description": [{"lang": "en", "value": "CWE-770"}]}
      ],
      "references": [
        {"url": "https://example.com/advisory", "tags": ["Vendor Advisory"]},
        {"url": "https://example.com/patch", "tags": ["Patch"]},
        {"url": "https://example.com/3", "tags": []},
        {"url": "https://example.com/4", "tags": []},
        {"url": "https://example.com/5", "tags": []},
        {"url": "https://example.com/6-DROPPED", "tags": []}
      ]
    }
  }]
}`

func TestExtractNVDDetail_FullPayload(t *testing.T) {
	var parsed nvdResponse
	if err := json.Unmarshal([]byte(sampleNVDPayload), &parsed); err != nil {
		t.Fatalf("unmarshal sample: %v", err)
	}
	if len(parsed.Vulnerabilities) == 0 {
		t.Fatal("sample payload has no vulnerabilities")
	}
	got := extractNVDDetail(parsed.Vulnerabilities[0].CVE)

	if got.CVEID != "CVE-2024-1234" {
		t.Errorf("CVEID = %q, want CVE-2024-1234", got.CVEID)
	}
	// CVSS v3.1 preferred over v3.0.
	if got.CVSSScore != 7.5 || got.Severity != "high" {
		t.Errorf("CVSS = %v/%q, want 7.5/high (v3.1 wins over v3.0)", got.CVSSScore, got.Severity)
	}
	if !strings.HasPrefix(got.CVSSVector, "CVSS:3.1/") {
		t.Errorf("CVSS vector = %q, want v3.1 vector", got.CVSSVector)
	}
	// English description wins.
	if got.Description != "Memory leak in TLS handshake parsing." {
		t.Errorf("Description = %q, want English text", got.Description)
	}
	// Both CWE entries surface in order.
	if !reflect.DeepEqual(got.CWE, []string{"CWE-400", "CWE-770"}) {
		t.Errorf("CWE = %v, want [CWE-400 CWE-770]", got.CWE)
	}
	// References capped at 5.
	if len(got.References) != 5 {
		t.Errorf("References len = %d, want 5", len(got.References))
	}
	for _, r := range got.References {
		if strings.Contains(r, "DROPPED") {
			t.Errorf("reference %q should be beyond the cap", r)
		}
	}
	if got.PublishedAt != "2024-03-15T10:00:00.000" {
		t.Errorf("PublishedAt = %q, want raw NVD published timestamp", got.PublishedAt)
	}
}

func TestExtractNVDDetail_NoCVSS(t *testing.T) {
	const raw = `{"vulnerabilities":[{"cve":{"id":"CVE-2024-NOSCORE","descriptions":[{"lang":"en","value":"reserved"}]}}]}`
	var parsed nvdResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	got := extractNVDDetail(parsed.Vulnerabilities[0].CVE)
	if got.CVSSScore != 0 || got.Severity != "" || got.CVSSVector != "" {
		t.Errorf("expected zero CVSS fields, got %+v", got)
	}
	if got.Description != "reserved" {
		t.Errorf("Description = %q, want 'reserved'", got.Description)
	}
}

// stubNVDCache is an in-memory NVDCache for testing — no DB roundtrip.
type stubNVDCache struct {
	store      map[string]NVDDetail
	getCalls   int
	upsertHits int
}

func newStubNVDCache() *stubNVDCache { return &stubNVDCache{store: map[string]NVDDetail{}} }

func (s *stubNVDCache) GetNVD(_ context.Context, id string) (*NVDDetail, error) {
	s.getCalls++
	d, ok := s.store[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return &d, nil
}

func (s *stubNVDCache) UpsertNVD(_ context.Context, r NVDDetail) error {
	s.upsertHits++
	s.store[r.CVEID] = r
	return nil
}

func TestNVDEnricher_Lookup_CacheHit(t *testing.T) {
	cache := newStubNVDCache()
	cache.store["CVE-2024-1"] = NVDDetail{CVEID: "CVE-2024-1", CVSSScore: 9.8, Severity: "critical"}

	// httpClient is nil-but-Lookup-shouldn't-hit-it on a cache hit; use a
	// server that fails if accessed so a regression surfaces loudly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("cache hit should not trigger an HTTP fetch")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := &NVDEnricher{cache: cache, httpClient: srv.Client()}
	got, err := e.Lookup(context.Background(), "CVE-2024-1")
	if err != nil {
		t.Fatalf("Lookup err: %v", err)
	}
	if got == nil || got.CVSSScore != 9.8 {
		t.Errorf("Lookup miss on cache hit: got %+v", got)
	}
}

func TestNVDEnricher_Lookup_EmptyCVEID(t *testing.T) {
	e := &NVDEnricher{cache: newStubNVDCache()}
	got, err := e.Lookup(context.Background(), "")
	if got != nil || err != nil {
		t.Errorf("Lookup(\"\") = (%+v, %v), want (nil, nil)", got, err)
	}
}
