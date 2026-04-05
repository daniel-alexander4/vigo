// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

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

func runSwarmDistribute(cmd *cobra.Command, args []string) error {
	filePath := args[0]
	target, _ := cmd.Flags().GetString("target")
	label, _ := cmd.Flags().GetString("label")

	// Open the file and compute SHA256 locally first.
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
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

	// Default label to filename.
	if label == "" {
		label = info.Name()
	}

	// Upload to server.
	fmt.Printf("Uploading to server...\n")
	client := newAPIClient()
	var uploadResp struct {
		SHA256     string `json:"sha256"`
		Size       int64  `json:"size"`
		Label      string `json:"label"`
		ChunkCount int    `json:"chunk_count"`
	}
	if err := client.postRaw("/api/v1/blob?label="+label, f, &uploadResp); err != nil {
		return fmt.Errorf("uploading blob: %w", err)
	}

	if uploadResp.SHA256 != localHash {
		return fmt.Errorf("hash mismatch: local=%s server=%s", localHash, uploadResp.SHA256)
	}

	fmt.Printf("  SHA256: %s\n", uploadResp.SHA256)
	fmt.Printf("  Size:   %s (%d chunks)\n", formatBytes(uploadResp.Size), uploadResp.ChunkCount)

	// Distribute to targets.
	if target == "" {
		target = "*"
	}
	var distResp struct {
		Targets int `json:"targets"`
	}
	if err := client.post("/api/v1/swarm/distribute", map[string]string{
		"sha256": uploadResp.SHA256,
		"target": target,
		"label":  label,
	}, &distResp); err != nil {
		return fmt.Errorf("distributing: %w", err)
	}

	fmt.Printf("\n  %s Distributed to %d envoy(s)\n", green("OK"), distResp.Targets)
	fmt.Printf("  Track progress: vigocli swarm status %s\n", uploadResp.SHA256)
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

	var resp struct {
		Blobs []struct {
			SHA256    string `json:"sha256"`
			Size      int64  `json:"size"`
			Label     string `json:"label"`
			Chunks    int    `json:"chunk_count"`
			CreatedAt string `json:"created_at"`
		} `json:"blobs"`
	}
	if err := client.get("/api/v1/blob", &resp); err != nil {
		return fmt.Errorf("listing blobs: %w", err)
	}

	if len(resp.Blobs) == 0 {
		fmt.Println("No blobs in the store.")
		return nil
	}

	fmt.Printf("Blobs (%d):\n\n", len(resp.Blobs))
	for _, b := range resp.Blobs {
		label := b.Label
		if label == "" {
			label = b.SHA256[:12] + "..."
		}
		fmt.Printf("  %-30s %8s  %3d chunks  %s\n",
			bold(label),
			formatBytes(b.Size),
			b.Chunks,
			dim(b.SHA256[:16]+"..."),
		)
	}
	return nil
}

func runSwarmDelete(cmd *cobra.Command, args []string) error {
	hash := args[0]
	client := newAPIClient()

	var resp struct {
		Deleted string `json:"deleted"`
	}
	if err := client.delete("/api/v1/blob/"+hash, &resp); err != nil {
		return fmt.Errorf("deleting blob: %w", err)
	}

	fmt.Printf("Deleted: %s\n", resp.Deleted)
	return nil
}

func init() {
	swarmDistributeCmd.Flags().String("target", "*", "Target spec: hostname glob, trait filter (key=value), or '*' for all")
	swarmDistributeCmd.Flags().String("label", "", "Human-readable label (defaults to filename)")

	swarmCmd.AddCommand(swarmDistributeCmd, swarmStatusCmd, swarmListCmd, swarmDeleteCmd)
}
