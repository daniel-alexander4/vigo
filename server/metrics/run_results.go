// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/validate"
)

// labelOther replaces any label value outside its known set. Every value
// recorded from a run result is agent-supplied, so an unbounded label would
// let one envoy mint a new series per report.
const labelOther = "other"

// runActions is the action_taken vocabulary the agent sends: the Display
// form of its executors::Action enum (agent/src/executors/mod.rs).
var runActions = map[string]bool{
	"settled": true,
	"created": true,
	"updated": true,
	"deleted": true,
	"skipped": true,
	"blocked": true,
}

// SetKnownConfigcrates replaces the configcrate names DriftDetected may use
// as a label value. Called off the hot path by the Collector.
func (m *Metrics) SetKnownConfigcrates(names []string) {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	m.knownConfigcrates.Store(&set)
}

// RecordRunResults counts one run's per-resource results. Every label value
// is bounded: resource type to validate.KnownTypes, action to the proto
// vocabulary, configcrate to the loaded config; anything else is "other".
// No per-envoy label: hostnames would grow the series set with the fleet.
func (m *Metrics) RecordRunResults(results []*pb.ResourceRunResult) {
	var known map[string]struct{}
	if p := m.knownConfigcrates.Load(); p != nil {
		known = *p
	}
	for _, r := range results {
		typ := r.GetResourceType()
		if !validate.KnownTypes[typ] {
			typ = labelOther
		}
		action := r.GetActionTaken()
		if !runActions[action] {
			action = labelOther
		}
		m.ConfigcrateActions.WithLabelValues(typ, action).Inc()
		if r.GetChanged() {
			cc := r.GetConfigcrateName()
			if _, ok := known[cc]; !ok {
				cc = labelOther
			}
			m.DriftDetected.WithLabelValues(cc).Inc()
		}
	}
}
