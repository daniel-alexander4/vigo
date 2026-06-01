// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// Admin-pushed file distribution on the swarm substrate. "Filecast" is the
// content label for these payloads — swarm is the transport layer. The
// CLI verb tree under `vigocli swarm filecast` wraps the server's
// `/api/v1/swarm/filecast/*` endpoints.

var filecastCmd = &cobra.Command{
	Use:   "filecast",
	Short: "Administrator-pushed file distribution on the swarm substrate",
}

var filecastDistributeCmd = &cobra.Command{
	Use:   "distribute <file>",
	Short: "Seed a file for P2P distribution to envoys",
	Long: `Places a file in the server's seed directory for P2P distribution.
The server's agent discovers the seed on the next check-in and auto-distributes
to envoys matching --target. Envoys download from each other over mTLS HTTPS on
port 1531 (the swarm substrate).

--target is required — name the recipient set explicitly so a push never goes
fleet-wide by accident. Patterns are comma-separated; "-" prefix excludes;
exclusions win over inclusions; "host*" globs; "trait=value" matches a trait.

Examples:
  vigocli swarm filecast distribute agent-0.8.0-linux-amd64.tar.gz --target '*'
  vigocli swarm filecast distribute image.iso --target '*,-rasp*' --label lmde-3
  vigocli swarm filecast distribute app-v2.tar.gz --target '*.web.*' --label app-v2
  vigocli swarm filecast distribute firmware.bin --target 'dmi.product_name=PowerEdge R750'`,
	Args: cobra.ExactArgs(1),
	RunE: runFilecastDistribute,
}

var filecastStatusCmd = &cobra.Command{
	Use:   "status <sha256>",
	Short: "Show distribution progress for a filecast",
	Args:  cobra.ExactArgs(1),
	RunE:  runFilecastStatus,
}

var filecastListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all filecast files and their distribution status",
	Args:  cobra.NoArgs,
	RunE:  runFilecastList,
}

var filecastRevokeCmd = &cobra.Command{
	Use:   "revoke <sha256>",
	Short: "Revoke a filecast: stop distribution and evict from envoys",
	Args:  cobra.ExactArgs(1),
	RunE:  runFilecastRevoke,
}

var filecastPurgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Purge revoked filecasts — drop the revoked tombstones from the manifest",
	Long: `Garbage-collect revoked filecast records from the server manifest.

revoke is a soft-delete: the entry stays as a revoked tombstone (so the
blob can't silently resurrect) and the retention pruner only GCs it after
the retention window. purge drops those records now. With no flag it purges
every revoked entry; --older-than limits it to tombstones past that age.
Active distributions are never touched.`,
	Args: cobra.NoArgs,
	RunE: runFilecastPurge,
}

var filecastBrokenCmd = &cobra.Command{
	Use:   "broken",
	Short: "List broken seed manifests across envoys",
	Args:  cobra.NoArgs,
	RunE:  runFilecastBroken,
}

func runFilecastDistribute(cmd *cobra.Command, args []string) error {
	filePath := args[0]
	target, _ := cmd.Flags().GetString("target")
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf(`--target is required: name the envoys this file goes to so a push never goes fleet-wide by accident.

  --target '*'                  every envoy
  --target '*,-raspberrypi'     every envoy except raspberrypi
  --target '-rasp*'             every envoy except hostnames matching rasp*
  --target 'danlap,annlap'      just those two
  --target 'os.distro=ubuntu'   envoys whose os.distro trait is "ubuntu"

Patterns: comma-separated; "-" prefix excludes; exclusions win; "host*" globs; "trait=value" matches`)
	}
	label, _ := cmd.Flags().GetString("label")
	retention, _ := cmd.Flags().GetString("retention")

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}

	// Default label to filename.
	if label == "" {
		label = info.Name()
	}

	// Stream-hash: compute SHA256 while uploading so the file is read only once.
	// Wrap in a 4 MiB buffered reader — TeeReader prevents sendfile optimization,
	// so large reads reduce syscall overhead on multi-GiB files.
	fmt.Printf("Seeding %s (%s)...\n", filePath, formatBytes(info.Size()))
	h := sha256.New()
	body := bufio.NewReaderSize(io.TeeReader(f, h), 4<<20)

	client := newAPIClient()
	var resp struct {
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
		Label  string `json:"label"`
	}
	headers := map[string]string{
		"X-Filecast-Label":     label,
		"X-Filecast-Target":    target,
		"X-Filecast-Retention": retention,
	}
	if err := client.postRaw("/api/v1/swarm/filecast/distribute", body, headers, &resp); err != nil {
		return fmt.Errorf("distributing: %w", err)
	}

	localHash := hex.EncodeToString(h.Sum(nil))
	if resp.SHA256 != localHash {
		return fmt.Errorf("hash mismatch: local=%s server=%s", localHash, resp.SHA256)
	}

	fmt.Printf("  SHA256: %s\n", resp.SHA256)
	fmt.Printf("  Size:   %s\n", formatBytes(resp.Size))

	// Check how many envoys match the target spec.
	var matchResp struct {
		Count int `json:"count"`
	}
	if err := client.get(fmt.Sprintf("/api/v1/swarm/filecast/match?target=%s", url.QueryEscape(target)), &matchResp); err == nil {
		if matchResp.Count == 0 {
			fmt.Printf("\n  %s target %q matched 0 envoys — no distribution will occur.\n", yellow("Warning:"), target)
		} else {
			fmt.Printf("\n  Seeded — distribution begins on next check-in cycle (%d envoys matched).\n", matchResp.Count)
		}
	} else {
		fmt.Printf("\n  Seeded — distribution begins on next check-in cycle.\n")
	}
	fmt.Printf("  Track progress: vigocli swarm filecast status %s\n", resp.SHA256)
	return nil
}

func runFilecastStatus(cmd *cobra.Command, args []string) error {
	hash := args[0]
	client := newAPIClient()

	var resp struct {
		SHA256     string `json:"sha256"`
		Label      string `json:"label"`
		Status     string `json:"status"`
		ChunkCount int    `json:"chunk_count"`
		Summary    struct {
			Source      int `json:"source"`
			Complete    int `json:"complete"`
			Cached      int `json:"cached"`
			Downloading int `json:"downloading"`
			Pending     int `json:"pending"`
			Skipped     int `json:"skipped"`
			Revoking    int `json:"revoking"`
			Revoked     int `json:"revoked"`
			Total       int `json:"total"`
		} `json:"summary"`
		Envoys []struct {
			Hostname   string `json:"hostname"`
			Status     string `json:"status"`
			ChunksHave int    `json:"chunks_have"`
			ChunksNeed int    `json:"chunks_need"`
			SkipReason string `json:"skip_reason,omitempty"`
		} `json:"envoys"`
	}
	if err := client.get("/api/v1/swarm/filecast/status/"+hash, &resp); err != nil {
		return fmt.Errorf("fetching status: %w", err)
	}

	label := resp.Label
	if label == "" {
		label = resp.SHA256[:12] + "..."
	}

	revoked := resp.Status == "revoked"

	fmt.Printf("Filecast: %s\n", bold(label))
	if resp.SHA256 != "" {
		fmt.Printf("  SHA256: %s\n", resp.SHA256)
	}
	fmt.Printf("  Chunks: %d\n", resp.ChunkCount)
	if revoked {
		fmt.Printf("  Status: %s\n", red("revoked (pending eviction from envoys)"))
	}
	fmt.Println()

	s := resp.Summary
	if revoked {
		// Post-revoke summary: complete/downloading/pending/skipped counts
		// are no longer meaningful — every envoy that had the payload is
		// either revoking (still holding it) or revoked (evicted).
		fmt.Printf("  %s revoking  %s revoked  (%d total)\n\n",
			yellow(fmt.Sprintf("%d", s.Revoking)),
			yellow(fmt.Sprintf("%d", s.Revoked)),
			s.Total,
		)
	} else {
		fmt.Printf("  %s source  %s complete  %s cached  %d downloading  %d pending  %s skipped  (%d total)\n\n",
			green(fmt.Sprintf("%d", s.Source)),
			green(fmt.Sprintf("%d", s.Complete)),
			cyan(fmt.Sprintf("%d", s.Cached)),
			s.Downloading,
			s.Pending,
			yellow(fmt.Sprintf("%d", s.Skipped)),
			s.Total,
		)
	}

	if len(resp.Envoys) == 0 {
		fmt.Println("  No envoys assigned.")
		return nil
	}

	for _, e := range resp.Envoys {
		var icon, progress string
		switch e.Status {
		case "source":
			// Distinct from "complete": this envoy originated the payload,
			// didn't download it. Black-star glyph reads unambiguously
			// as "origin" against the green checkmark used for complete.
			icon = green("★")
			progress = green("source")
		case "complete":
			icon = green("✓")
			progress = green("complete")
		case "cached":
			icon = cyan("✓")
			progress = cyan("cached")
		case "downloading":
			icon = "▶"
			progress = lodeProgressBar(e.ChunksHave, e.ChunksNeed) +
				fmt.Sprintf(" %d%%", percent(e.ChunksHave, e.ChunksNeed))
		case "pending":
			icon = " "
			progress = dim("pending")
		case "skipped":
			icon = yellow("⚠")
			progress = yellow("skipped")
			if e.SkipReason != "" {
				progress += dim(" (" + e.SkipReason + ")")
			}
		case "revoking":
			icon = yellow("⟳")
			progress = yellow("revoking")
		case "revoked":
			icon = yellow("✗")
			progress = yellow("revoked")
		}
		fmt.Printf("  %s %-30s %s\n", icon, e.Hostname, progress)
	}

	return nil
}

func runFilecastList(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	type payloadEntry struct {
		SHA256      string `json:"sha256"`
		Label       string `json:"label"`
		Size        int64  `json:"size"`
		Source      string `json:"source"`
		Target      string `json:"target"`
		Retention   string `json:"retention"`
		Complete    int    `json:"complete"`
		Cached      int    `json:"cached"`
		Downloading int    `json:"downloading"`
		Pending     int    `json:"pending"`
		Skipped     int    `json:"skipped"`
		Total       int    `json:"total"`
		Revoked     bool   `json:"revoked"`
		RevokedAt   string `json:"revoked_at"`
	}

	var resp struct {
		Payloads []payloadEntry `json:"payloads"`
	}

	path := "/api/v1/swarm/filecast/list"
	revoked, _ := cmd.Flags().GetBool("revoked")
	if revoked {
		path += "?revoked=true"
	}
	if err := client.get(path, &resp); err != nil {
		return fmt.Errorf("listing payloads: %w", err)
	}

	// Client-side filtering.
	sourceFilter, _ := cmd.Flags().GetString("source")
	statusFilter, _ := cmd.Flags().GetString("status")

	var filtered []payloadEntry
	for _, b := range resp.Payloads {
		if sourceFilter != "" && b.Source != sourceFilter {
			continue
		}
		switch statusFilter {
		case "active":
			if b.Downloading == 0 && b.Pending == 0 {
				continue
			}
		case "complete":
			if b.Total == 0 || b.Downloading > 0 || b.Pending > 0 {
				continue
			}
		}
		filtered = append(filtered, b)
	}

	if asJSON, _ := cmd.Flags().GetBool("json"); asJSON {
		out, err := json.MarshalIndent(filtered, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling filecasts: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	if len(filtered) == 0 {
		fmt.Println("No filecasts.")
		return nil
	}

	fmt.Printf("%-28s %-14s %8s  %-8s %-10s %s\n",
		"NAME", "SHA256", "SIZE", "SOURCE", "RETENTION", "PROGRESS")
	fmt.Println(strings.Repeat("-", 95))

	for _, b := range filtered {
		label := b.Label
		if label == "" {
			label = b.SHA256[:12]
		}
		if len(label) > 27 {
			label = label[:24] + "..."
		}
		sha := b.SHA256[:12]

		source := b.Source
		if b.Revoked {
			source = "revoked"
		}

		ret := b.Retention
		if ret == "" {
			ret = "-"
		}
		done := b.Complete + b.Cached
		progress := fmt.Sprintf("%d/%d", done, b.Total)
		if b.Cached > 0 {
			progress += fmt.Sprintf(" (%d cached)", b.Cached)
		}
		if b.Downloading > 0 {
			progress += fmt.Sprintf(" (%d dl)", b.Downloading)
		}

		fmt.Printf("%-28s %-14s %8s  %-8s %-10s %s\n",
			label, sha, formatBytes(b.Size), source, ret, progress)
	}
	return nil
}

func runFilecastBroken(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var resp struct {
		Envoys []struct {
			Hostname    string `json:"hostname"`
			BrokenSeeds []struct {
				Filename string `json:"filename"`
				Error    string `json:"error"`
			} `json:"broken_seeds"`
		} `json:"envoys"`
	}
	if err := client.get("/api/v1/swarm/filecast/broken", &resp); err != nil {
		return fmt.Errorf("listing broken seeds: %w", err)
	}

	if len(resp.Envoys) == 0 {
		fmt.Println("No broken seeds.")
		return nil
	}

	fmt.Printf("%-30s %-30s %s\n", "ENVOY", "FILENAME", "ERROR")
	fmt.Println(strings.Repeat("-", 90))

	for _, e := range resp.Envoys {
		for _, b := range e.BrokenSeeds {
			fmt.Printf("%-30s %-30s %s\n", e.Hostname, b.Filename, b.Error)
		}
	}
	return nil
}

func runFilecastRevoke(cmd *cobra.Command, args []string) error {
	hash := args[0]
	client := newAPIClient()

	var resp struct {
		Revoked        string `json:"revoked"`
		AlreadyRevoked bool   `json:"already_revoked"`
	}
	if err := client.delete("/api/v1/swarm/filecast/"+hash, &resp); err != nil {
		return fmt.Errorf("revoking payload: %w", err)
	}

	if resp.AlreadyRevoked {
		fmt.Printf("Already revoked: %s\n", resp.Revoked)
	} else {
		fmt.Printf("Revoked: %s\n", resp.Revoked)
	}
	return nil
}

func runFilecastPurge(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	path := "/api/v1/swarm/filecast/purge"
	if olderThan, _ := cmd.Flags().GetString("older-than"); olderThan != "" {
		path += "?older_than=" + url.QueryEscape(olderThan)
	}

	var resp struct {
		Purged int `json:"purged"`
	}
	if err := client.post(path, nil, &resp); err != nil {
		return fmt.Errorf("purging filecasts: %w", err)
	}

	fmt.Printf("Purged %d revoked filecast(s).\n", resp.Purged)
	return nil
}

// lodeProgressBar renders a 20-character block progress bar.
// Each block represents 5% completion. Uses Catppuccin Green for filled
// blocks and Surface1 for empty.
func lodeProgressBar(have, need int) string {
	const width = 20
	if need == 0 {
		return strings.Repeat("░", width)
	}
	filled := have * width / need
	if filled > width {
		filled = width
	}
	bar := green(strings.Repeat("█", filled)) + dim(strings.Repeat("░", width-filled))
	return bar
}

func percent(have, need int) int {
	if need == 0 {
		return 0
	}
	return have * 100 / need
}

func init() {
	filecastDistributeCmd.Flags().String("target", "", "Required — which envoys receive the file: hostname glob, exact hostname, trait filter (key=value), or '*' for all; comma-separated, '-' prefix excludes")
	filecastDistributeCmd.Flags().String("label", "", "Human-readable label (defaults to filename)")
	filecastDistributeCmd.Flags().String("retention", "7d", "Auto-evict from agents after this duration (e.g. 7d, 168h); 0 to keep forever")

	filecastListCmd.Flags().String("source", "", "Filter by source: server or seed")
	filecastListCmd.Flags().String("status", "", "Filter by status: active or complete")
	filecastListCmd.Flags().Bool("revoked", false, "Include revoked (expired or operator-revoked) payloads")
	filecastListCmd.Flags().Bool("json", false, "Output raw JSON (includes full sha256, for scripting)")

	filecastPurgeCmd.Flags().String("older-than", "", "Only purge revoked filecasts older than this (e.g. 24h, 7d); default: all revoked")

	filecastCmd.AddCommand(filecastDistributeCmd, filecastStatusCmd, filecastListCmd, filecastRevokeCmd, filecastPurgeCmd, filecastBrokenCmd)
}
