// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var tokensCmd = &cobra.Command{
	Use:   "tokens",
	Short: "Generate one-time tokens used by agents during bootstrap enrollment",
}

var tokensGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate a cryptographic token that authorizes a single agent bootstrap",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Token string `json:"token"`
		}
		if err := client.post("/api/v1/tokens", map[string]any{}, &resp); err != nil {
			return err
		}

		fmt.Printf("%s %s\n", bold("Token:"), resp.Token)
		command, note := bootstrapCommand(serverURL, resp.Token)
		// The token is not stored until it is bound to a hostname pattern,
		// so the bootstrap command alone would be refused.
		fmt.Printf("\n%s\n  vigocli approved add '<hostname-glob>' --token %s\n", bold("1. Bind it to the hostnames allowed to use it:"), resp.Token)
		fmt.Printf("\n%s\n  %s\n", bold("2. Run on the new envoy:"), command)
		if note != "" {
			fmt.Printf("\n%s\n", note)
		}
		return nil
	},
}

// bootstrapCommand renders the one-liner an operator pastes on a new envoy.
// The token travels as the ?token= query parameter — the served script embeds
// it from there (server/bootstrap); nothing reads a VIGO_TOKEN variable. -k
// because the server certificate chains to Vigo's own CA, which the new host
// does not have yet. A loopback --server is useless on another machine, so it
// becomes a <server-address> placeholder plus a note saying what to put there.
func bootstrapCommand(base, token string) (command, note string) {
	base = strings.TrimRight(base, "/")
	if isLocalhost(base) {
		port := "8443"
		if u, err := url.Parse(base); err == nil && u.Port() != "" {
			port = u.Port()
		}
		base = "https://<server-address>:" + port
		note = "Replace <server-address> with an address the new envoy can reach this server on."
	}
	return fmt.Sprintf(`curl -sSfk "%s/bootstrap?token=%s" | sudo sh`, base, url.QueryEscape(token)), note
}

func init() {
	tokensCmd.AddCommand(tokensGenerateCmd)
}
