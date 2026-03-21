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
)

// CVSSResult holds a cached CVSS lookup result.
type CVSSResult struct {
	CVEID      string  `json:"cve_id"`
	CVSSScore  float64 `json:"cvss_score"`
	CVSSVector string  `json:"cvss_vector"`
	Severity   string  `json:"severity"`
}

// CVSSCache is the interface for reading/writing the CVSS cache.
type CVSSCache interface {
	GetCVSS(ctx context.Context, cveID string) (*CVSSResult, error)
	UpsertCVSS(ctx context.Context, r CVSSResult) error
}

// NVDEnricher fetches CVSS scores from the NVD 2.0 API and caches them.
type NVDEnricher struct {
	apiKey     string
	cache      CVSSCache
	httpClient *http.Client
	rateDelay  time.Duration
}

// NewNVDEnricher creates an NVD enricher. apiKey is optional (empty = lower rate limit).
func NewNVDEnricher(apiKey string, cache CVSSCache) *NVDEnricher {
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
	e.enrich(ctx, cveCollector())
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.enrich(ctx, cveCollector())
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

		// Skip if already cached and fresh (< 7 days).
		if existing, err := e.cache.GetCVSS(ctx, cveID); err == nil && existing != nil {
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

		if err := e.cache.UpsertCVSS(ctx, *result); err != nil {
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

func (e *NVDEnricher) fetchCVE(ctx context.Context, cveID string) (*CVSSResult, error) {
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

	var nvdResp struct {
		Vulnerabilities []struct {
			CVE struct {
				ID      string `json:"id"`
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
			} `json:"cve"`
		} `json:"vulnerabilities"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&nvdResp); err != nil {
		return nil, fmt.Errorf("decoding NVD response: %w", err)
	}

	if len(nvdResp.Vulnerabilities) == 0 {
		return &CVSSResult{CVEID: cveID}, nil
	}

	cve := nvdResp.Vulnerabilities[0].CVE

	// Prefer CVSS v3.1, fall back to v3.0.
	if len(cve.Metrics.CVSSV31) > 0 {
		m := cve.Metrics.CVSSV31[0].CVSSData
		return &CVSSResult{
			CVEID:      cve.ID,
			CVSSScore:  m.BaseScore,
			CVSSVector: m.VectorString,
			Severity:   strings.ToLower(m.BaseSeverity),
		}, nil
	}
	if len(cve.Metrics.CVSSV30) > 0 {
		m := cve.Metrics.CVSSV30[0].CVSSData
		return &CVSSResult{
			CVEID:      cve.ID,
			CVSSScore:  m.BaseScore,
			CVSSVector: m.VectorString,
			Severity:   strings.ToLower(m.BaseSeverity),
		}, nil
	}

	return &CVSSResult{CVEID: cve.ID}, nil
}
