// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var swarmCmd = &cobra.Command{
	Use:   "swarm",
	Short: "Peer-to-peer content distribution",
}

var swarmDistributeCmd = &cobra.Command{
	Use:   "distribute <file>",
	Short: "Upload a file and distribute it to envoys via the swarm",
	Long: `Uploads a file to the server's blob store and assigns it to envoys
matching the target spec. Envoys download the blob from peers (preferred)
or the server (fallback) over mTLS HTTPS on port 1531.

Examples:
  vigocli swarm distribute agent-0.8.0-linux-amd64.tar.gz --target '*'
  vigocli swarm distribute app-v2.tar.gz --target '*.web.*' --label app-v2
  vigocli swarm distribute firmware.bin --target 'dmi.product_name=PowerEdge R750'`,
	Args: cobra.ExactArgs(1),
	RunE: runSwarmDistribute,
}

var swarmStatusCmd = &cobra.Command{
	Use:   "status <sha256>",
	Short: "Show distribution progress for a blob",
	Args:  cobra.ExactArgs(1),
	RunE:  runSwarmStatus,
}

var swarmListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all blobs in the server's blob store",
	Args:  cobra.NoArgs,
	RunE:  runSwarmList,
}

var swarmDeleteCmd = &cobra.Command{
	Use:   "delete <sha256>",
	Short: "Remove a blob from the server's blob store",
	Args:  cobra.ExactArgs(1),
	RunE:  runSwarmDelete,
}

var swarmBrokenCmd = &cobra.Command{
	Use:   "broken",
	Short: "List broken seed manifests across envoys",
	Args:  cobra.NoArgs,
	RunE:  runSwarmBroken,
}

func runSwarmDistribute(cmd *cobra.Command, args []string) error {
	filePath := args[0]
	target, _ := cmd.Flags().GetString("target")
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

	fmt.Printf("Hashing %s (%s)...\n", filePath, formatBytes(info.Size()))
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hashing: %w", err)
	}
	localHash := hex.EncodeToString(h.Sum(nil))

	// Reset file for upload.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seeking: %w", err)
	}

	// Single call: upload + seed in one request.
	fmt.Printf("Uploading to server...\n")
	client := newAPIClient()
	var resp struct {
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
		Label  string `json:"label"`
	}
	headers := map[string]string{
		"X-Swarm-Label":     label,
		"X-Swarm-Target":    target,
		"X-Swarm-Retention": retention,
	}
	if err := client.postRaw("/api/v1/swarm/distribute", f, headers, &resp); err != nil {
		return fmt.Errorf("distributing: %w", err)
	}

	if resp.SHA256 != localHash {
		return fmt.Errorf("hash mismatch: local=%s server=%s", localHash, resp.SHA256)
	}

	fmt.Printf("  SHA256: %s\n", resp.SHA256)
	fmt.Printf("  Size:   %s\n", formatBytes(resp.Size))
	fmt.Printf("\n  Seeded — distribution begins on next check-in cycle.\n")
	fmt.Printf("  Track progress: vigocli swarm status %s\n", resp.SHA256)
	return nil
}

func runSwarmStatus(cmd *cobra.Command, args []string) error {
	hash := args[0]
	client := newAPIClient()

	var resp struct {
		SHA256     string `json:"sha256"`
		Label      string `json:"label"`
		ChunkCount int    `json:"chunk_count"`
		Summary    struct {
			Complete    int `json:"complete"`
			Downloading int `json:"downloading"`
			Pending     int `json:"pending"`
			Skipped     int `json:"skipped"`
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
	if err := client.get("/api/v1/swarm/status/"+hash, &resp); err != nil {
		return fmt.Errorf("fetching status: %w", err)
	}

	label := resp.Label
	if label == "" {
		label = resp.SHA256[:12] + "..."
	}

	fmt.Printf("Blob: %s\n", bold(label))
	if resp.SHA256 != "" {
		fmt.Printf("  SHA256: %s\n", resp.SHA256)
	}
	fmt.Printf("  Chunks: %d\n\n", resp.ChunkCount)

	s := resp.Summary
	fmt.Printf("  %s complete  %d downloading  %s pending  %s skipped  (%d total)\n\n",
		green(fmt.Sprintf("%d", s.Complete)),
		s.Downloading,
		dim(fmt.Sprintf("%d", s.Pending)),
		yellow(fmt.Sprintf("%d", s.Skipped)),
		s.Total,
	)

	if len(resp.Envoys) == 0 {
		fmt.Println("  No envoys assigned.")
		return nil
	}

	for _, e := range resp.Envoys {
		var icon, progress string
		switch e.Status {
		case "complete":
			icon = green("\u2713")
			progress = green("complete")
		case "downloading":
			icon = "\u25b6"
			progress = fmt.Sprintf("%d/%d chunks", e.ChunksHave, e.ChunksNeed)
		case "pending":
			icon = dim("\u2013")
			progress = dim("pending")
		case "skipped":
			icon = yellow("\u26a0")
			progress = yellow("skipped")
			if e.SkipReason != "" {
				progress += dim(" (" + e.SkipReason + ")")
			}
		}
		fmt.Printf("  %s %-30s %s\n", icon, e.Hostname, progress)
	}

	return nil
}

func runSwarmList(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	type blobEntry struct {
		SHA256      string `json:"sha256"`
		Label       string `json:"label"`
		Size        int64  `json:"size"`
		Source      string `json:"source"`
		Target      string `json:"target"`
		Retention   string `json:"retention"`
		Complete    int    `json:"complete"`
		Downloading int    `json:"downloading"`
		Pending     int    `json:"pending"`
		Skipped     int    `json:"skipped"`
		Total       int    `json:"total"`
		Retired     bool   `json:"retired"`
		RetiredAt   string `json:"retired_at"`
	}

	var resp struct {
		Blobs []blobEntry `json:"blobs"`
	}

	path := "/api/v1/swarm/list"
	retired, _ := cmd.Flags().GetBool("retired")
	if retired {
		path += "?retired=true"
	}
	if err := client.get(path, &resp); err != nil {
		return fmt.Errorf("listing blobs: %w", err)
	}

	// Client-side filtering.
	sourceFilter, _ := cmd.Flags().GetString("source")
	statusFilter, _ := cmd.Flags().GetString("status")

	var filtered []blobEntry
	for _, b := range resp.Blobs {
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

	if len(filtered) == 0 {
		fmt.Println("No swarm files.")
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
		if b.Retired {
			source = "retired"
		}

		ret := b.Retention
		if ret == "" {
			ret = "-"
		}
		progress := fmt.Sprintf("%d/%d", b.Complete, b.Total)
		if b.Downloading > 0 {
			progress += fmt.Sprintf(" (%d dl)", b.Downloading)
		}

		fmt.Printf("%-28s %-14s %8s  %-8s %-10s %s\n",
			label, sha, formatBytes(b.Size), source, ret, progress)
	}
	return nil
}

func runSwarmBroken(cmd *cobra.Command, args []string) error {
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
	if err := client.get("/api/v1/swarm/broken", &resp); err != nil {
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

func runSwarmDelete(cmd *cobra.Command, args []string) error {
	hash := args[0]
	client := newAPIClient()

	var resp struct {
		Deleted string `json:"deleted"`
	}
	if err := client.delete("/api/v1/swarm/"+hash, &resp); err != nil {
		return fmt.Errorf("deleting blob: %w", err)
	}

	fmt.Printf("Deleted: %s\n", resp.Deleted)
	return nil
}

func init() {
	swarmDistributeCmd.Flags().String("target", "*", "Target spec: hostname glob, trait filter (key=value), or '*' for all")
	swarmDistributeCmd.Flags().String("label", "", "Human-readable label (defaults to filename)")
	swarmDistributeCmd.Flags().String("retention", "7d", "Auto-evict from agents after this duration (e.g. 7d, 168h); 0 to keep forever")

	swarmListCmd.Flags().String("source", "", "Filter by source: server or seed")
	swarmListCmd.Flags().String("status", "", "Filter by status: active or complete")
	swarmListCmd.Flags().Bool("retired", false, "Include retired (expired) seeds")

	swarmCmd.AddCommand(swarmDistributeCmd, swarmStatusCmd, swarmListCmd, swarmDeleteCmd, swarmBrokenCmd)
}
