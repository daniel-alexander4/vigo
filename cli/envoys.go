// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var envoysCmd = &cobra.Command{
	Use:   "envoys",
	Short: "Inspect and manage enrolled managed nodes (envoys)",
}

var envoysListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all managed nodes with enrollment status and last check-in time",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		var resp struct {
			Envoys []struct {
				ID         string `json:"id"`
				Hostname   string `json:"hostname"`
				IPAddress  string `json:"ipAddress"`
				EnrolledAt string `json:"enrolledAt"`
				LastSeen   string `json:"lastSeen"`
				Revoked    bool   `json:"revoked"`
			} `json:"envoys"`
		}
		endpoint := "/api/v1/envoys"
		if all, _ := cmd.Flags().GetBool("all"); all {
			endpoint += "?includeRevoked=true"
		}
		if err := client.get(endpoint, &resp); err != nil {
			return err
		}

		if len(resp.Envoys) == 0 {
			fmt.Println("No envoys found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", header("ID"), header("HOSTNAME"), header("IP"), header("ENROLLED"), header("LAST SEEN"), header("STATUS"))
		for _, c := range resp.Envoys {
			status := "active"
			if c.Revoked {
				status = "revoked"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				dim(c.ID), c.Hostname, c.IPAddress,
				dim(formatTimestamp(c.EnrolledAt)),
				dim(formatTimestamp(c.LastSeen)),
				colorStatus(status))
		}
		return w.Flush()
	},
}

var envoysShowCmd = &cobra.Command{
	Use:               "show <id-or-hostname>",
	Short:             "Show enrollment details, compliance status, and recent runs for a envoy",
	Example: `  vigocli envoys show web01.example.com
  vigocli envoys show 2e66cb50-4e75-4859-bf24-c16588dd4f3e`,
	Args:              requireArgs(1, "a hostname or UUID"),
	ValidArgsFunction: completeEnvoyIDs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		envoyID, err := resolveEnvoy(client, args[0])
		if err != nil {
			return err
		}
		var resp struct {
			Envoy struct {
				ID                     string   `json:"id"`
				Hostname               string   `json:"hostname"`
				IPAddress              string   `json:"ipAddress"`
				EnrolledAt             string   `json:"enrolledAt"`
				LastSeen               string   `json:"lastSeen"`
				Revoked                bool     `json:"revoked"`
				RevokedAt              string   `json:"revokedAt"`
				PendingRotatedSecrets  []string `json:"pendingRotatedSecrets"`
				AgentVersion           string   `json:"agentVersion"`
			} `json:"envoy"`
		}
		if err := client.get("/api/v1/envoys/"+envoyID, &resp); err != nil {
			return err
		}

		c := resp.Envoy
		fmt.Printf("ID:         %s\n", c.ID)
		fmt.Printf("Hostname:   %s\n", c.Hostname)
		fmt.Printf("IP:         %s\n", c.IPAddress)
		fmt.Printf("Enrolled:   %s\n", formatTimestamp(c.EnrolledAt))
		fmt.Printf("Last Seen:  %s\n", formatTimestamp(c.LastSeen))
		status := "active"
		if c.Revoked {
			status = fmt.Sprintf("revoked (%s)", formatTimestamp(c.RevokedAt))
		}
		fmt.Printf("Status:     %s\n", status)
		if c.AgentVersion != "" {
			fmt.Printf("Agent:      %s\n", c.AgentVersion)
		}

		// Also fetch convergence status
		var convResp struct {
			Status string `json:"status"`
		}
		if err := client.get("/api/v1/envoys/"+envoyID+"/convergence", &convResp); err == nil {
			fmt.Printf("Convergence: %s\n", convResp.Status)
		}

		if len(c.PendingRotatedSecrets) > 0 {
			fmt.Printf("Pending secrets: %s\n", strings.Join(c.PendingRotatedSecrets, ", "))
		}

		return nil
	},
}

var envoysRevokeCmd = &cobra.Command{
	Use:               "revoke <id>",
	Short:             "Block a envoy from checking in and receiving policy updates",
	Long: `Revoke an enrolled envoy. Cert is marked revoked, the envoy can no
longer check in, and re-enrollment under the same UUID is blocked.

Cross-envoy cleanup runs by default: the revoked host's pubkey is
dropped from every other envoy's per-user lockbox recipients list
so future re-encrypts don't target a host that can't receive. Use
--skip-swarm-cleanup if you plan to re-bootstrap the same host
shortly (e.g. investigating a suspected compromise) and want the
recipient state preserved.`,
	Example:           "  vigocli envoys revoke 2e66cb50-4e75-4859-bf24-c16588dd4f3e",
	Args:              requireArgs(1, "a envoy ID"),
	ValidArgsFunction: completeEnvoyIDs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !confirmAction("Revoke this envoy? It will be blocked from checking in.") {
			fmt.Println("Cancelled.")
			return nil
		}
		skipCleanup, _ := cmd.Flags().GetBool("skip-swarm-cleanup")
		body := map[string]any{"id": args[0], "skip_swarm_cleanup": skipCleanup}
		client := newAPIClient()
		if err := client.post("/api/v1/envoys/"+args[0]+"/revoke", body, nil); err != nil {
			return err
		}
		fmt.Printf("Revoked envoy %s\n", args[0])
		return nil
	},
}

var envoysDeleteCmd = &cobra.Command{
	Use:               "delete <id>",
	Short:             "Permanently remove a envoy, its run history, and traits from the server",
	Long: `Permanently delete an envoy and CASCADE its traits, runs, and
snapshots from the database.

Cross-envoy lockbox cleanup runs by default — same semantic as
'envoys revoke'. Pass --skip-swarm-cleanup to leave peer recipient
state intact (rare; only useful if you intend to re-create the same
hostname shortly under a different UUID).`,
	Example:           "  vigocli envoys delete 2e66cb50-4e75-4859-bf24-c16588dd4f3e",
	Args:              requireArgs(1, "a envoy ID"),
	ValidArgsFunction: completeEnvoyIDs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !confirmAction("Delete this envoy and all its history? This cannot be undone.") {
			fmt.Println("Cancelled.")
			return nil
		}
		skipCleanup, _ := cmd.Flags().GetBool("skip-swarm-cleanup")
		client := newAPIClient()
		path := "/api/v1/envoys/" + args[0]
		if skipCleanup {
			path += "?skip_swarm_cleanup=true"
		}
		if err := client.delete(path, nil); err != nil {
			return err
		}
		fmt.Printf("Deleted envoy %s\n", args[0])
		return nil
	},
}

var envoysTraitsCmd = &cobra.Command{
	Use:               "traits <hostname> [dot.path]",
	Short:             "View auto-discovered system facts (OS, hardware, network, packages) for a envoy",
	ValidArgsFunction: completeHostnames,
	Long: `View the traits (auto-discovered system facts) for a envoy. Returns the full
trait tree as JSON, or a specific value when a dot-path is provided.`,
	Example: `  vigocli envoys traits web01.example.com
  vigocli envoys traits web01.example.com os.distro
  vigocli envoys traits web01.example.com hardware.memory_mb`,
	Args: requireArgs(1, "a hostname"),
	RunE: func(cmd *cobra.Command, args []string) error {
		hostname := args[0]
		client := newAPIClient()

		var resp struct {
			Hostname    string         `json:"hostname"`
			CollectedAt string         `json:"collected_at"`
			Traits       map[string]any `json:"traits"`
		}
		if err := client.get("/api/v1/envoys/"+hostname+"/traits", &resp); err != nil {
			return err
		}

		// If a dot-path is provided, extract just that value.
		if len(args) == 2 {
			val, ok := traverseDotPath(resp.Traits, args[1])
			if !ok {
				return fmt.Errorf("trait path '%s' not found", args[1])
			}
			fmt.Fprintln(os.Stdout, formatTraitValue(val))
			return nil
		}

		// No dot-path: print the full envelope as pretty JSON.
		data, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to format response: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	},
}

// traverseDotPath walks a nested map using a dot-separated path and returns
// the value at that path. Returns (nil, false) if any segment is missing.
func traverseDotPath(data map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var current any = data
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// formatTraitValue is defined in inventory.go — it handles all types
// including nested maps and arrays with human-readable summaries.

var envoysMaintenanceCmd = &cobra.Command{
	Use:               "maintenance <id>",
	Short:             "Suppress convergence on a envoy until a specified time",
	ValidArgsFunction: completeEnvoyIDs,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := newAPIClient()
		clear, _ := cmd.Flags().GetBool("clear")

		if clear {
			if err := client.delete("/api/v1/envoys/"+args[0]+"/maintenance", nil); err != nil {
				return err
			}
			fmt.Println("Maintenance mode cleared.")
			return nil
		}

		until, _ := cmd.Flags().GetString("until")
		if until == "" {
			return fmt.Errorf("--until or --clear is required")
		}

		req := map[string]string{"until": until}
		if err := client.put("/api/v1/envoys/"+args[0]+"/maintenance", req, nil); err != nil {
			return err
		}
		fmt.Printf("Maintenance mode set until %s\n", until)
		return nil
	},
}

var envoysPurgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Delete stale duplicate envoys, keeping only the most recently seen entry per hostname",
	Long: `Finds hostnames with multiple enrolled envoys (e.g., machines rebuilt with
the same name) and deletes all but the most recently seen entry for each.

Use --dry-run to preview what would be deleted without making changes.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		client := newAPIClient()
		var resp struct {
			Envoys []struct {
				ID         string `json:"id"`
				Hostname   string `json:"hostname"`
				IPAddress  string `json:"ipAddress"`
				EnrolledAt string `json:"enrolledAt"`
				LastSeen   string `json:"lastSeen"`
				Revoked    bool   `json:"revoked"`
			} `json:"envoys"`
		}
		if err := client.get("/api/v1/envoys?includeRevoked=true", &resp); err != nil {
			return err
		}

		// Group by hostname.
		type entry struct {
			ID, Hostname, IPAddress, EnrolledAt, LastSeen string
			Revoked                                       bool
		}
		byHostname := make(map[string][]entry)
		for _, t := range resp.Envoys {
			byHostname[t.Hostname] = append(byHostname[t.Hostname], entry{
				t.ID, t.Hostname, t.IPAddress, t.EnrolledAt, t.LastSeen, t.Revoked,
			})
		}

		// For each hostname with >1 entry, keep the most recently seen.
		// Revoked entries always lose to active ones.
		var toDelete []entry
		for _, entries := range byHostname {
			if len(entries) < 2 {
				continue
			}
			best := 0
			for i := 1; i < len(entries); i++ {
				if entries[best].Revoked && !entries[i].Revoked {
					best = i
				} else if !entries[best].Revoked && entries[i].Revoked {
					continue
				} else if entries[i].LastSeen > entries[best].LastSeen {
					best = i
				} else if entries[i].LastSeen == entries[best].LastSeen && entries[i].EnrolledAt > entries[best].EnrolledAt {
					best = i
				}
			}
			for i, e := range entries {
				if i != best {
					toDelete = append(toDelete, e)
				}
			}
		}

		if len(toDelete) == 0 {
			fmt.Println("No duplicate envoys found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", header("ID"), header("HOSTNAME"), header("IP"), header("LAST SEEN"), header("STATUS"))
		for _, d := range toDelete {
			status := "active"
			if d.Revoked {
				status = "revoked"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				dim(d.ID), d.Hostname, d.IPAddress, dim(formatTimestamp(d.LastSeen)), colorStatus(status))
		}
		w.Flush()
		fmt.Printf("\n%d duplicate envoy(s) will be deleted.\n", len(toDelete))

		if dryRun {
			fmt.Println("Dry run — no changes made.")
			return nil
		}

		if !confirmAction("Purge these envoys?") {
			fmt.Println("Cancelled.")
			return nil
		}

		var failures int
		for _, d := range toDelete {
			if err := client.delete("/api/v1/envoys/"+d.ID, nil); err != nil {
				fmt.Fprintf(os.Stderr, "  failed to delete %s (%s): %v\n", d.ID, d.Hostname, err)
				failures++
			} else {
				fmt.Printf("  deleted %s (%s)\n", d.ID, d.Hostname)
			}
		}
		if failures > 0 {
			return fmt.Errorf("%d deletion(s) failed", failures)
		}
		fmt.Printf("Purged %d stale envoy(s).\n", len(toDelete))
		return nil
	},
}

// resolveEnvoy resolves an ID-or-hostname string to a envoy UUID.
// If the argument looks like a UUID (contains a hyphen and is 36+ chars),
// it is returned as-is. Otherwise, it searches the envoy list by hostname.
func resolveEnvoy(client *apiClient, idOrHostname string) (string, error) {
	// Simple heuristic: UUIDs are 36 chars with hyphens.
	if len(idOrHostname) >= 36 && strings.Contains(idOrHostname, "-") {
		return idOrHostname, nil
	}
	// Also accept short UUIDs (8-char prefix from truncUUID).
	// But hostnames can also be short, so try hostname lookup first.
	var resp struct {
		Envoys []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		} `json:"envoys"`
	}
	if err := client.get("/api/v1/envoys", &resp); err != nil {
		return "", fmt.Errorf("listing envoys for hostname lookup: %w", err)
	}
	for _, t := range resp.Envoys {
		if t.Hostname == idOrHostname {
			return t.ID, nil
		}
	}
	// Maybe it's a short UUID prefix.
	for _, t := range resp.Envoys {
		if strings.HasPrefix(t.ID, idOrHostname) {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("no envoy found matching %q", idOrHostname)
}

var envoysRebootstrapCmd = &cobra.Command{
	Use:   "rebootstrap [hostname...]",
	Short: "Signal envoys to download the latest agent binary and restart",
	Long: `Signal one or more envoys to update their agent binary on the next check-in.
The server sets a force_update flag that tells the agent to download the latest
binary, verify its checksum, replace itself, and restart — no SSH required.

With --all, signals every active envoy. Otherwise, pass hostnames.

Examples:
  vigocli envoys rebootstrap plex raspberry
  vigocli envoys rebootstrap --all`,
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")

		if !all && len(args) == 0 {
			return fmt.Errorf("specify hostnames or use --all")
		}

		client := newAPIClient()

		if all {
			var resp struct {
				Count int `json:"count"`
			}
			if err := client.post("/api/v1/envoys/update", nil, &resp); err != nil {
				return err
			}
			fmt.Printf("Queued update for %d envoy(s). They will update on next check-in.\n", resp.Count)
			return nil
		}

		// Resolve hostnames to IDs and signal each one.
		var failures int
		for _, name := range args {
			id, err := resolveEnvoy(client, name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", name, err)
				failures++
				continue
			}
			if err := client.post("/api/v1/envoys/"+id+"/update", nil, nil); err != nil {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", name, err)
				failures++
			} else {
				fmt.Printf("  %s: queued\n", name)
			}
		}
		if failures > 0 {
			return fmt.Errorf("%d target(s) failed", failures)
		}
		fmt.Println("Envoys will update on next check-in.")
		return nil
	},
}

func init() {
	envoysListCmd.Flags().Bool("all", false, "include revoked envoys")
	envoysRebootstrapCmd.Flags().Bool("all", false, "update all active envoys")
	envoysMaintenanceCmd.Flags().String("until", "", "maintenance window end (RFC3339)")
	envoysMaintenanceCmd.Flags().Bool("clear", false, "clear maintenance mode")
	envoysPurgeCmd.Flags().Bool("dry-run", false, "preview duplicates without deleting")
	envoysRevokeCmd.Flags().Bool("skip-swarm-cleanup", false,
		"don't drop the host's pubkey from peers' lockbox recipients (use only when re-bootstrapping the same host shortly)")
	envoysDeleteCmd.Flags().Bool("skip-swarm-cleanup", false,
		"don't drop the host's pubkey from peers' lockbox recipients")
	envoysCmd.AddCommand(envoysListCmd, envoysShowCmd, envoysRevokeCmd, envoysDeleteCmd,
		envoysTraitsCmd, envoysMaintenanceCmd,
		envoysPurgeCmd, envoysRebootstrapCmd, envoysPushCmd, envoysRevertCmd, previewCmd)
}
