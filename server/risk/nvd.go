// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package risk

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/daemon"
)

// NVDDetail is the authoritative record for a single CVE pulled from the
// NVD 2.0 API. Stored in the DB-backed cache so two consumers share it:
// the background risk-scoring enricher (CVSS metadata) and the on-demand
// AI prefetcher (description / CWE / references for prompt enrichment).
type NVDDetail struct {
	CVEID       string   `json:"cve_id"`
	CVSSScore   float64  `json:"cvss_score"`
	CVSSVector  string   `json:"cvss_vector"`
	Severity    string   `json:"severity"`
	Description string   `json:"description"`
	CWE         []string `json:"cwe"`
	References  []string `json:"references"`
	PublishedAt string   `json:"published_at"`
}

// NVDCache is the interface for reading/writing the persistent NVD
// detail cache. Implemented in main.go by an adapter over db.Queries.
type NVDCache interface {
	GetNVD(ctx context.Context, cveID string) (*NVDDetail, error)
	UpsertNVD(ctx context.Context, r NVDDetail) error
}

// NVDEnricher fetches CVE detail from the NVD 2.0 API and caches it.
type NVDEnricher struct {
	apiKey     string
	cache      NVDCache
	httpClient *http.Client
	rateDelay  time.Duration
}

// NewNVDEnricher creates an NVD enricher. apiKey is optional (empty = lower rate limit).
func NewNVDEnricher(apiKey string, cache NVDCache) *NVDEnricher {
	delay := 6 * time.Second // ~5 req/30s without key
	if apiKey != "" {
		delay = 600 * time.Millisecond // ~10 req/s with key
	}
	return &NVDEnricher{
		apiKey: apiKey,
		cache:  cache,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		rateDelay: delay,
	}
}

// Run starts the NVD enrichment loop. It runs immediately once, then every 6 hours.
func (e *NVDEnricher) Run(ctx context.Context, cveCollector func() []string) {
	daemon.Guard("nvd-enricher", func() { e.enrich(ctx, cveCollector()) })
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			daemon.Guard("nvd-enricher", func() { e.enrich(ctx, cveCollector()) })
		}
	}
}

func (e *NVDEnricher) enrich(ctx context.Context, cveIDs []string) {
	if len(cveIDs) == 0 {
		return
	}

	fetched, skipped, errors := 0, 0, 0
	for _, cveID := range cveIDs {
		if ctx.Err() != nil {
			break
		}

		// Skip if already cached and fresh (< 7 days). The cache adapter
		// returns nil on staleness so this also re-fetches stale records.
		if existing, err := e.cache.GetNVD(ctx, cveID); err == nil && existing != nil {
			skipped++
			continue
		}

		result, err := e.fetchCVE(ctx, cveID)
		if err != nil {
			errors++
			if errors <= 3 {
				slog.Warn("nvd lookup failed", "cve", cveID, "error", err)
			}
			continue
		}

		if err := e.cache.UpsertNVD(ctx, *result); err != nil {
			slog.Warn("nvd cache write failed", "cve", cveID, "error", err)
		}
		fetched++

		// Rate limit.
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.rateDelay):
		}
	}

	slog.Info("nvd enrichment complete", "fetched", fetched, "skipped", skipped, "errors", errors)
}

// Lookup returns the NVD record for cveID. Cache-first; on miss it does
// a synchronous fetch bounded by the http client's timeout. Returns nil
// detail + nil error when the API returns an empty record (unknown CVE)
// so callers can treat that case the same as "no enrichment available".
// Used by the AI prefetcher for on-demand prompt context — distinct
// from the background enrich loop in Run, but shares the cache.
func (e *NVDEnricher) Lookup(ctx context.Context, cveID string) (*NVDDetail, error) {
	if cveID == "" {
		return nil, nil
	}
	if existing, err := e.cache.GetNVD(ctx, cveID); err == nil && existing != nil {
		return existing, nil
	}
	result, err := e.fetchCVE(ctx, cveID)
	if err != nil {
		return nil, err
	}
	if result.CVSSScore == 0 && result.Description == "" {
		// Empty record — cache it so we don't refetch immediately, but
		// don't pretend it's useful enrichment.
		_ = e.cache.UpsertNVD(ctx, *result)
		return nil, nil
	}
	if err := e.cache.UpsertNVD(ctx, *result); err != nil {
		slog.Warn("nvd cache write failed", "cve", cveID, "error", err)
	}
	return result, nil
}

// nvdResponse mirrors the NVD 2.0 fields the enricher reads. Anything
// not declared here is ignored by encoding/json.
type nvdResponse struct {
	Vulnerabilities []struct {
		CVE struct {
			ID           string `json:"id"`
			Published    string `json:"published"`
			Descriptions []struct {
				Lang  string `json:"lang"`
				Value string `json:"value"`
			} `json:"descriptions"`
			Metrics struct {
				CVSSV31 []struct {
					CVSSData struct {
						BaseScore    float64 `json:"baseScore"`
						VectorString string  `json:"vectorString"`
						BaseSeverity string  `json:"baseSeverity"`
					} `json:"cvssData"`
				} `json:"cvssMetricV31"`
				CVSSV30 []struct {
					CVSSData struct {
						BaseScore    float64 `json:"baseScore"`
						VectorString string  `json:"vectorString"`
						BaseSeverity string  `json:"baseSeverity"`
					} `json:"cvssData"`
				} `json:"cvssMetricV30"`
			} `json:"metrics"`
			Weaknesses []struct {
				Description []struct {
					Lang  string `json:"lang"`
					Value string `json:"value"`
				} `json:"description"`
			} `json:"weaknesses"`
			References []struct {
				URL  string   `json:"url"`
				Tags []string `json:"tags"`
			} `json:"references"`
		} `json:"cve"`
	} `json:"vulnerabilities"`
}

func (e *NVDEnricher) fetchCVE(ctx context.Context, cveID string) (*NVDDetail, error) {
	url := fmt.Sprintf("https://services.nvd.nist.gov/rest/json/cves/2.0?cveId=%s", cveID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if e.apiKey != "" {
		req.Header.Set("apiKey", e.apiKey)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("NVD API returned %d", resp.StatusCode)
	}

	var parsed nvdResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decoding NVD response: %w", err)
	}

	if len(parsed.Vulnerabilities) == 0 {
		return &NVDDetail{CVEID: cveID}, nil
	}
	return extractNVDDetail(parsed.Vulnerabilities[0].CVE), nil
}

// extractNVDDetail projects the NVD wire shape into NVDDetail, choosing
// English-language descriptions/CWEs and preferring CVSS v3.1 over v3.0.
// Top 5 references (URL only) are kept; the rest are dropped.
func extractNVDDetail(cve struct {
	ID           string `json:"id"`
	Published    string `json:"published"`
	Descriptions []struct {
		Lang  string `json:"lang"`
		Value string `json:"value"`
	} `json:"descriptions"`
	Metrics struct {
		CVSSV31 []struct {
			CVSSData struct {
				BaseScore    float64 `json:"baseScore"`
				VectorString string  `json:"vectorString"`
				BaseSeverity string  `json:"baseSeverity"`
			} `json:"cvssData"`
		} `json:"cvssMetricV31"`
		CVSSV30 []struct {
			CVSSData struct {
				BaseScore    float64 `json:"baseScore"`
				VectorString string  `json:"vectorString"`
				BaseSeverity string  `json:"baseSeverity"`
			} `json:"cvssData"`
		} `json:"cvssMetricV30"`
	} `json:"metrics"`
	Weaknesses []struct {
		Description []struct {
			Lang  string `json:"lang"`
			Value string `json:"value"`
		} `json:"description"`
	} `json:"weaknesses"`
	References []struct {
		URL  string   `json:"url"`
		Tags []string `json:"tags"`
	} `json:"references"`
}) *NVDDetail {
	out := &NVDDetail{CVEID: cve.ID, PublishedAt: cve.Published}

	// Prefer CVSS v3.1, fall back to v3.0.
	if len(cve.Metrics.CVSSV31) > 0 {
		m := cve.Metrics.CVSSV31[0].CVSSData
		out.CVSSScore = m.BaseScore
		out.CVSSVector = m.VectorString
		out.Severity = strings.ToLower(m.BaseSeverity)
	} else if len(cve.Metrics.CVSSV30) > 0 {
		m := cve.Metrics.CVSSV30[0].CVSSData
		out.CVSSScore = m.BaseScore
		out.CVSSVector = m.VectorString
		out.Severity = strings.ToLower(m.BaseSeverity)
	}

	for _, d := range cve.Descriptions {
		if d.Lang == "en" {
			out.Description = d.Value
			break
		}
	}

	for _, w := range cve.Weaknesses {
		for _, d := range w.Description {
			if d.Lang == "en" && d.Value != "" {
				out.CWE = append(out.CWE, d.Value)
				break
			}
		}
	}

	const maxRefs = 5
	for i, r := range cve.References {
		if i >= maxRefs {
			break
		}
		out.References = append(out.References, r.URL)
	}

	return out
}
