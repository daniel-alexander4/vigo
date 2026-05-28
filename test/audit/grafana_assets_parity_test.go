// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package audit

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestGrafanaAssetsParity pins the byte-for-byte equality between the
// canonical Grafana dashboards + alert-rules under docs/howto/grafana/
// and the copies shipped in the example stockpile templates. The
// dashboards are the SoT; the stockpile copies exist so the example
// modules vigo-grafana-dashboards / vigo-prometheus-alerts can `source:`
// them via the normal Vigo `templates/` idiom. Either tree edited in
// isolation triggers a fail — keeping both copies in lockstep is
// cheaper than introducing a build step or a symlink that breaks on
// cross-platform clones.
func TestGrafanaAssetsParity(t *testing.T) {
	root := repoRoot(t)

	cases := []struct {
		canonical string
		template  string
	}{
		{"docs/howto/grafana/vigo-overview.json",
			"example-configs/worksite/stockpile/templates/vigo-dashboards/vigo-overview.json"},
		{"docs/howto/grafana/vigo-convergence.json",
			"example-configs/worksite/stockpile/templates/vigo-dashboards/vigo-convergence.json"},
		{"docs/howto/grafana/vigo-bursts.json",
			"example-configs/worksite/stockpile/templates/vigo-dashboards/vigo-bursts.json"},
		{"docs/howto/grafana/vigo-risk.json",
			"example-configs/worksite/stockpile/templates/vigo-dashboards/vigo-risk.json"},
		{"docs/howto/grafana/vigo-security.json",
			"example-configs/worksite/stockpile/templates/vigo-dashboards/vigo-security.json"},
		{"docs/howto/grafana/vigo-swarm.json",
			"example-configs/worksite/stockpile/templates/vigo-dashboards/vigo-swarm.json"},
		{"docs/howto/grafana/alerts.yaml",
			"example-configs/worksite/stockpile/templates/vigo-prometheus/vigo-alerts.yaml"},
	}

	for _, tc := range cases {
		canonicalPath := filepath.Join(root, tc.canonical)
		templatePath := filepath.Join(root, tc.template)

		canonical, err := os.ReadFile(canonicalPath)
		if err != nil {
			t.Errorf("read canonical %s: %v", tc.canonical, err)
			continue
		}
		template, err := os.ReadFile(templatePath)
		if err != nil {
			t.Errorf("read template %s: %v", tc.template, err)
			continue
		}
		if !bytes.Equal(canonical, template) {
			t.Errorf("drift between %s and %s — copy the canonical file over the template (or vice versa) and commit both together",
				tc.canonical, tc.template)
		}
	}
}
