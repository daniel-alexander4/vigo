// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// findRedirect walks a parent's subcommands looking for a hidden one with
// `Use == verb`. Reaches into both the verb name and the help text so we
// catch refactors that change the shape (e.g. someone adds Use: "verb <arg>").
func findRedirect(t *testing.T, parent *cobra.Command, verb string) *cobra.Command {
	t.Helper()
	for _, c := range parent.Commands() {
		if c.Name() == verb {
			return c
		}
	}
	return nil
}

// agent-side verbs we redirect from on each subsystem. If a real verb is
// later added to vigocli for one of these names, this test will fail
// because the hidden redirect would shadow it — a deliberate trip-wire.
var redirectMatrix = []struct {
	parent     *cobra.Command
	subsystem  string
	verbs      []string
	flagPhrase string // substring expected in the error message
}{
	{curatorCmd, "curator", []string{
		"push", "tag", "untag", "set-recipients", "set-target", "rm",
		"prune", "gc", "stale", "purge", "pull",
	}, "`curator: true`"},
	{gitbackCmd, "gitback", []string{"project", "purge"}, "`gitback: true`"},
	{lockboxCmd, "lockbox", []string{
		"unlock", "lock", "encrypt", "decrypt", "delete", "resurrect",
		"reencrypt", "purge",
	}, "`puddle: true`"},
	{puddleCmd, "puddle", []string{
		"init", "pubkey", "unlock", "lock", "pair", "join", "leave",
		"rekey-pair", "rekey", "members", "rotate-passphrase", "status", "name",
	}, "`puddle: true`"},
}

func TestAgentRedirects_AllRegisteredHiddenAndReturnGuidance(t *testing.T) {
	for _, tc := range redirectMatrix {
		for _, verb := range tc.verbs {
			redirect := findRedirect(t, tc.parent, verb)
			if redirect == nil {
				t.Errorf("no redirect for vigocli swarm %s %s", tc.subsystem, verb)
				continue
			}
			if !redirect.Hidden {
				t.Errorf("redirect for %s %s must be Hidden=true to keep --help clean", tc.subsystem, verb)
			}
			if !redirect.DisableFlagParsing {
				t.Errorf("redirect for %s %s must DisableFlagParsing so operator-typed flags forward through cobra without error", tc.subsystem, verb)
			}

			err := redirect.RunE(redirect, []string{"some", "arg"})
			if err == nil {
				t.Errorf("redirect for %s %s should return an error (non-zero exit)", tc.subsystem, verb)
				continue
			}
			msg := err.Error()
			// Tailored body: names both the wrong invocation and the right one,
			// names the usercrate flag, and points away from root.
			wants := []string{
				"vigocli swarm " + tc.subsystem + " " + verb,
				"vigo swarm " + tc.subsystem + " " + verb,
				tc.flagPhrase,
				"NOT as root",
			}
			for _, w := range wants {
				if !strings.Contains(msg, w) {
					t.Errorf("%s %s redirect message missing %q\nfull message: %s", tc.subsystem, verb, w, msg)
				}
			}
		}
	}
}

func TestAgentRedirects_DoNotShadowRealVigocliVerbs(t *testing.T) {
	// Each redirect must be Hidden, but we also want to be sure no real
	// (non-hidden) vigocli verb shares a name with a redirect — that'd
	// mean we either shadowed a real command or registered the redirect
	// twice. cobra resolves on Name; visibility of the matched command
	// is what determines whether it appears in --help.
	for _, tc := range redirectMatrix {
		realVerbs := map[string]*cobra.Command{}
		for _, c := range tc.parent.Commands() {
			if !c.Hidden {
				realVerbs[c.Name()] = c
			}
		}
		for _, verb := range tc.verbs {
			if real, ok := realVerbs[verb]; ok {
				t.Errorf("vigocli swarm %s %s is registered as both a real verb (%q) and a hidden redirect — pick one",
					tc.subsystem, verb, real.Short)
			}
		}
	}
}
