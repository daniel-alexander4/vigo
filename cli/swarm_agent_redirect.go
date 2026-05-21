// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// vigocli's `swarm` content-subsystem subtrees are operator-side and
// read-only (per ADR-024 open Q#1 for curator; same pattern for gitback /
// lockbox / longdrawer). The mutating + per-user verbs live on the agent
// side as `vigo swarm <subsystem> <verb>` — they sign with the user's
// puddle identity, which vigocli (root-only / peer-auth) can't produce.
//
// An operator who types e.g. `vigocli swarm curator push foo` gets
// cobra's bare "unknown command" otherwise. agentRedirectCmd registers a
// hidden subcommand under each subtree for the known agent-only verbs;
// invoking one prints a tailored pointer at `vigo swarm <subsystem> <verb>`
// on the right envoy.
//
// Hidden so they don't clutter `--help` (that page already enumerates
// the real read-only verbs); cobra still finds them on dispatch.

// addAgentRedirects attaches hidden redirect subcommands under `parent`,
// one per name in `verbs`. Each redirect, when invoked, returns an error
// pointing at `vigo swarm <subsystem> <verb>` and naming the usercrate
// flag the publishing user needs.
func addAgentRedirects(parent *cobra.Command, subsystem, usercrateFlag string, verbs []string) {
	for _, v := range verbs {
		v := v // capture
		parent.AddCommand(&cobra.Command{
			Use:                v,
			Hidden:             true,
			DisableFlagParsing: true, // forward whatever args the operator typed
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SilenceUsage = true
				return fmt.Errorf(
					"`vigocli swarm %s %s` is not an operator verb — this lives on the agent side.\n"+
						"  Run `vigo swarm %s %s` on the publishing/holding envoy, as the Unix user\n"+
						"  whose usercrate carries `%s: true` (NOT as root). `vigocli swarm %s` is\n"+
						"  read-only + admin moderation; mutating verbs sign with the user's puddle\n"+
						"  identity, which vigocli can't produce",
					subsystem, v, subsystem, v, usercrateFlag, subsystem,
				)
			},
		})
	}
}
