// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ResolveSecrets returns the plaintext values of secret references that the
// calling envoy's resolved policy bundle references. The interceptor has
// already verified the envoy's ed25519 signature over the sorted ref list +
// timestamp by the time the handler runs.
//
// Authorization: the handler walks the envoy's resolved EnvoyConfig and
// builds the set of secret refs the bundle actually carries (as
// `__VIGO_SECRET__<key>__END_VIGO_SECRET__` placeholder markers). Any
// requested ref outside that set fails the WHOLE call with
// PermissionDenied — no partial response is returned, so a caller can't
// fish for individual entitled refs by submitting larger batches.
//
// Audit: a `secret.entitlement_denied` event is recorded on rejection; a
// `secret.epoch_resolved` event is recorded on successful resolution.
// Refs are logged in both cases — values are NOT logged.
func (s *AgentService) ResolveSecrets(ctx context.Context, req *pb.ResolveSecretsRequest) (*pb.ResolveSecretsResponse, error) {
	envoyPtr := db.EnvoyFromContext(ctx)
	if envoyPtr == nil {
		return nil, status.Error(codes.Unauthenticated, "envoy identity not in context")
	}
	envoy := *envoyPtr
	if len(req.GetRefs()) == 0 {
		return &pb.ResolveSecretsResponse{
			Values:       map[string]string{},
			SecretsEpoch: s.configStore.SecretsEpoch(),
		}, nil
	}

	pattern, cfg := s.configStore.Match(envoy.Hostname)
	if cfg == nil {
		s.recordResolveSecretsAudit(ctx, envoy, pattern, req.GetRefs(), "no_matched_config", "secret.entitlement_denied")
		return nil, status.Errorf(codes.NotFound, "no matched config for envoy %q", envoy.Hostname)
	}

	entitled := referencedSecretsForConfig(cfg)
	var notEntitled []string
	for _, ref := range req.GetRefs() {
		if !entitled[ref] {
			notEntitled = append(notEntitled, ref)
		}
	}
	if len(notEntitled) > 0 {
		sort.Strings(notEntitled)
		s.recordResolveSecretsAudit(ctx, envoy, pattern, notEntitled, "not_referenced_by_bundle", "secret.entitlement_denied")
		return nil, status.Errorf(codes.PermissionDenied, "secret refs not referenced by envoy's bundle: %v", notEntitled)
	}

	values := make(map[string]string, len(req.GetRefs()))
	for _, ref := range req.GetRefs() {
		val, err := s.configStore.ResolveSecret(ctx, ref)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "resolving secret %q: %v", ref, err)
		}
		values[ref] = val
	}

	epoch := s.configStore.SecretsEpoch()
	s.recordResolveSecretsAudit(ctx, envoy, pattern, req.GetRefs(), "", "secret.epoch_resolved")
	return &pb.ResolveSecretsResponse{Values: values, SecretsEpoch: epoch}, nil
}

// referencedSecretsForConfig walks the resolved EnvoyConfig for placeholder
// markers and returns the set of secret paths the bundle references. Drives
// the entitlement check in ResolveSecrets — an envoy can only resolve refs
// whose placeholder appears somewhere in its own bundle.
func referencedSecretsForConfig(cfg *config.EnvoyConfig) map[string]bool {
	out := make(map[string]bool)
	for _, v := range cfg.Vars {
		if str, ok := v.(string); ok {
			collectSecretRefsFromString(str, out)
		}
	}
	for _, mod := range cfg.Configcrates {
		for _, res := range mod.Resources {
			for _, av := range res.Attributes {
				if str, ok := av.(string); ok {
					collectSecretRefsFromString(str, out)
				}
			}
		}
	}
	collectSecretRefsFromString(cfg.Credential, out)
	return out
}

// collectSecretRefsFromString scans s for `__VIGO_SECRET__<key>__END_VIGO_SECRET__`
// markers and adds each key to refs.
func collectSecretRefsFromString(s string, refs map[string]bool) {
	const begin = "__VIGO_SECRET__"
	const end = "__END_VIGO_SECRET__"
	i := 0
	for {
		rel := strings.Index(s[i:], begin)
		if rel < 0 {
			return
		}
		keyStart := i + rel + len(begin)
		keyEnd := strings.Index(s[keyStart:], end)
		if keyEnd < 0 {
			return
		}
		refs[s[keyStart:keyStart+keyEnd]] = true
		i = keyStart + keyEnd + len(end)
	}
}

// recordResolveSecretsAudit emits an audit event for a ResolveSecrets call.
// Values are never logged — only refs + the outcome reason.
func (s *AgentService) recordResolveSecretsAudit(ctx context.Context, envoy db.Envoy, pattern string, refs []string, reason string, eventType string) {
	if s.auditWriter == nil {
		return
	}
	details := map[string]any{
		"envoy_id":       envoy.ID,
		"hostname":       envoy.Hostname,
		"matched_pattern": pattern,
		"refs":           refs,
		"refs_count":     len(refs),
	}
	if reason != "" {
		details["reason"] = reason
	}
	payload, _ := json.Marshal(details)
	_ = s.auditWriter.Record(ctx, eventType, envoy.Hostname, "agent", eventType, string(payload))
}
