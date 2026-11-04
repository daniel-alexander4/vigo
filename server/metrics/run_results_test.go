// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
)

// TestRecordRunResults_BoundsLabels: agent-supplied strings never become
// label values unless they are in a known set. A hostile envoy sending a
// fresh configcrate name, resource type and action per report must land in
// "other" and add no series.
func TestRecordRunResults_BoundsLabels(t *testing.T) {
	m := NewTestMetrics()
	m.SetKnownConfigcrates([]string{"base"})

	m.RecordRunResults([]*pb.ResourceRunResult{
		{ConfigcrateName: "base", ResourceType: "file", ActionTaken: "updated", Changed: true},
		{ConfigcrateName: "evil-1", ResourceType: "evil-type", ActionTaken: "evil-action", Changed: true},
		{ConfigcrateName: "evil-2", ResourceType: "file", ActionTaken: "settled"},
	})

	if got := testutil.ToFloat64(m.DriftDetected.WithLabelValues("base")); got != 1 {
		t.Errorf("drift{base} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.DriftDetected.WithLabelValues(labelOther)); got != 1 {
		t.Errorf("drift{other} = %v, want 1", got)
	}
	if n := testutil.CollectAndCount(m.DriftDetected); n != 2 {
		t.Errorf("drift series = %d, want 2 (base, other)", n)
	}
	if got := testutil.ToFloat64(m.ConfigcrateActions.WithLabelValues("file", "updated")); got != 1 {
		t.Errorf("actions{file,updated} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.ConfigcrateActions.WithLabelValues(labelOther, labelOther)); got != 1 {
		t.Errorf("actions{other,other} = %v, want 1", got)
	}
	if n := testutil.CollectAndCount(m.ConfigcrateActions); n != 3 {
		t.Errorf("action series = %d, want 3", n)
	}
}

// TestCollector_RefreshesKnownConfigcrates: the collector tick loads the
// configcrate set the drift label is bounded to.
func TestCollector_RefreshesKnownConfigcrates(t *testing.T) {
	m := NewTestMetrics()
	c := NewCollector(nil, nil, nil, staticConfigcrates{"web"}, m, 0, 0)
	if err := c.UpdateGauges(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.RecordRunResults([]*pb.ResourceRunResult{{ConfigcrateName: "web", ResourceType: "file", ActionTaken: "created", Changed: true}})
	if got := testutil.ToFloat64(m.DriftDetected.WithLabelValues("web")); got != 1 {
		t.Errorf("drift{web} = %v, want 1 after collector refresh", got)
	}
}

type staticConfigcrates []string

func (s staticConfigcrates) ListConfigcrateNames() []string { return s }
