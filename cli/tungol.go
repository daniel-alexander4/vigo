// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.alexander4.io/vigo/server/puddle"
	"git.alexander4.io/vigo/server/secrets"
	"github.com/spf13/cobra"
)

// tungolCmd is the top-level namespace for the federated control-plane
// surface. Hosts tungol-wide verbs (status) and the per-noun groups
// (steorra, ops-puddle).
var tungolCmd = &cobra.Command{
	Use:   "tungol",
	Short: "Federated control-plane operations",
	Long: `Operations for the tungol (federation) and steorra (one vigosrv
in the tungol). The current wire implementation is hub + steorras;
Phase 1+ will replace it with peer-equal gossip.`,
}

var tungolStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show this server's tungol role (standalone / hub / spoke) and steorra availability",
	RunE:  runTungolStatus,
}

var tungolOpsPuddleCmd = &cobra.Command{
	Use:   "ops-puddle",
	Short: "Manage the ops-puddle — the puddle that owns the stockpile gitback project",
	Long: `The ops-puddle is a shared puddle owned by every vigosrv that holds
authority to push stockpile updates. The first vigosrv founds it; later
vigosrvs join via the pair flow. See ADR-025.`,
}

var tungolOpsPuddleInitCmd = &cobra.Command{
	Use:   "init <friendly-name>",
	Short: "Found the ops-puddle on this vigosrv",
	Long: `Generate the ops-puddle Ed25519 keypair, wrap it under this vigosrv's
master-key-derived wrap key, and persist it under --ops-puddle-dir. This
vigosrv's service-account puddle (per ADR-025 Slice 1) becomes the first
member. Other vigosrvs add themselves via the pair flow (Slice 2b).

The friendly name participates in gitback URLs (gitback://<name>/stockpile),
so pick something stable — renaming is a destructive operation that requires
re-founding the ops-puddle from scratch.`,
	Args: cobra.ExactArgs(1),
	RunE: runTungolOpsPuddleInit,
}

var tungolOpsPuddleStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the ops-puddle's friendly name, pubkey, and current member set",
	RunE:  runTungolOpsPuddleStatus,
}

func init() {
	tungolCmd.PersistentFlags().String("key-file", "", "Path to master encryption key (omit to auto-detect inside --secrets-dir)")
	tungolCmd.PersistentFlags().String("secrets-dir", "/srv/vigo/secrets", "Directory holding the secrets backend's master key")
	tungolCmd.PersistentFlags().String("puddle-dir", puddle.DefaultDir, "Directory holding this vigosrv's service-account puddle")
	tungolCmd.PersistentFlags().String("ops-puddle-dir", puddle.OpsDefaultDir, "Directory holding the ops-puddle wrap (per-host)")

	tungolCmd.AddCommand(tungolStatusCmd)
	tungolCmd.AddCommand(tungolSteorraCmd)
	tungolCmd.AddCommand(tungolOpsPuddleCmd)
	tungolOpsPuddleCmd.AddCommand(tungolOpsPuddleInitCmd)
	tungolOpsPuddleCmd.AddCommand(tungolOpsPuddleStatusCmd)

	// Init writes new master-key-derived state — same operator-side gate as
	// `vigocli secrets set`. requireUnlocked uses an exempt-by-verb-name map
	// scoped to `vigocli secrets`, where "init" is the bootstrap verb;
	// dispatching through that name here would silently exempt this verb.
	// Wrap it so the gate is unconditional for tungol verbs that need it.
	// Status reads public files only and stays open.
	tungolOpsPuddleInitCmd.PreRunE = requireUnlockedAlways
}

// requireUnlockedAlways calls the underlying gate primitives without the
// `vigocli secrets`-scoped exemption list. Wired onto tungol verbs that
// write master-key-derived state (e.g. ops-puddle init).
func requireUnlockedAlways(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	if !verifierExists() {
		return fmt.Errorf("secrets vault not initialized — run `vigocli secrets init` to set the master key and unlock passphrase")
	}
	if !isUnlocked() {
		return fmt.Errorf("secrets vault is locked — run `vigocli secrets unlock`")
	}
	bumpSentinel()
	return nil
}

func runTungolOpsPuddleInit(cmd *cobra.Command, args []string) error {
	name := args[0]
	if err := puddle.ValidateFriendlyName(name); err != nil {
		return err
	}

	keyFile, _ := cmd.Flags().GetString("key-file")
	secretsDir, _ := cmd.Flags().GetString("secrets-dir")
	puddleDir, _ := cmd.Flags().GetString("puddle-dir")
	opsDir, _ := cmd.Flags().GetString("ops-puddle-dir")

	provider, err := secrets.NewLocalProvider(keyFile, secretsDir)
	if err != nil {
		return fmt.Errorf("opening secrets backend: %w", err)
	}
	defer provider.Close()

	svc, err := puddle.LoadIdentity(provider, puddleDir)
	if err != nil {
		return fmt.Errorf(
			"loading service-account puddle from %s: %w (vigosrv must have run at least once to generate the service-account puddle)",
			puddleDir, err,
		)
	}

	ops, err := puddle.EnsureOpsPuddle(provider, opsDir, name, svc.PublicKeyText)
	if err != nil {
		return err
	}

	fmt.Printf("Ops-puddle founded\n")
	fmt.Printf("  Friendly name: %s\n", ops.FriendlyName)
	fmt.Printf("  Pubkey:        %s\n", ops.PublicKeyText)
	fmt.Printf("  Dir:           %s\n", opsDir)
	fmt.Printf("  Members:       1 (this vigosrv: %s)\n", svc.PublicKeyText)
	return nil
}

func runTungolOpsPuddleStatus(cmd *cobra.Command, _ []string) error {
	opsDir, _ := cmd.Flags().GetString("ops-puddle-dir")

	// Status reads public files only. Pubkey + friendly name + members
	// are all readable without unwrapping the signing key, so we skip
	// the secrets-backend dance entirely.
	pub, err := os.ReadFile(puddle.PubkeyPath(opsDir))
	if err != nil {
		return fmt.Errorf("no ops-puddle at %s: %w", opsDir, err)
	}
	name, err := os.ReadFile(filepath.Join(opsDir, puddle.FriendlyNameFile))
	if err != nil {
		return fmt.Errorf("reading friendly name: %w", err)
	}
	membersRaw, err := os.ReadFile(filepath.Join(opsDir, puddle.MembersFile))
	if err != nil {
		return fmt.Errorf("reading members: %w", err)
	}
	var members []puddle.OpsMember
	if err := json.Unmarshal(membersRaw, &members); err != nil {
		return fmt.Errorf("parsing members.json: %w", err)
	}

	fmt.Printf("Ops-puddle status\n")
	fmt.Printf("  Friendly name: %s\n", trimTrailingNewline(string(name)))
	fmt.Printf("  Pubkey:        %s\n", trimTrailingNewline(string(pub)))
	fmt.Printf("  Members (%d):\n", len(members))
	for _, m := range members {
		fmt.Printf("    - %s (joined %s)\n", m.Pubkey, m.JoinedAt.Format("2006-01-02 15:04:05 UTC"))
	}
	return nil
}

func trimTrailingNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}

// runTungolStatus prints this server's tungol role and (in hub mode)
// the steorras it aggregates.
func runTungolStatus(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var resp struct {
		Role      string `json:"role"`
		SteorraID string `json:"steorra_id"`
		Steorras  []struct {
			ID             string   `json:"id"`
			Addr           string   `json:"addr"`
			Patterns       []string `json:"patterns"`
			Healthy        bool     `json:"healthy"`
			EnvoyCount     int32    `json:"envoy_count"`
			Converged      int32    `json:"converged"`
			Relapsed       int32    `json:"relapsed"`
			Diverged       int32    `json:"diverged"`
			Failed         int32    `json:"failed"`
			Offline        int32    `json:"offline"`
			NoData         int32    `json:"no_data"`
			ConvergencePct float32  `json:"compliance_pct"`
			LatencyMs      int64    `json:"latency_ms"`
			Error          string   `json:"error"`
		} `json:"steorras"`
	}

	if err := client.get("/api/v1/tungol/status", &resp); err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}

	fmt.Printf("  Role: %s\n", bold(resp.Role))

	if resp.SteorraID != "" {
		fmt.Printf("  Steorra ID: %s\n", resp.SteorraID)
	}

	if resp.Role == "standalone" {
		fmt.Println("  No tungol configured.")
		return nil
	}

	if len(resp.Steorras) == 0 {
		return nil
	}

	healthy := 0
	var totalEnvoys int32
	for _, s := range resp.Steorras {
		if s.Healthy {
			healthy++
		}
		totalEnvoys += s.EnvoyCount
	}

	fmt.Printf("  Steorras: %d (%d healthy)\n", len(resp.Steorras), healthy)
	fmt.Printf("  Total Envoys: %d\n\n", totalEnvoys)

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", header("ID"), header("ADDRESS"), header("ENVOYS"), header("COMPLIANT"), header("COMPLIANCE"), header("LATENCY"), header("PATTERNS"), header("STATUS"))
	for _, s := range resp.Steorras {
		status := colorStatus("healthy")
		if !s.Healthy {
			status = colorStatus("unreachable")
		}
		patterns := strings.Join(s.Patterns, ", ")
		compliant := fmt.Sprintf("%d/%d", s.Converged, s.EnvoyCount)
		convPct := fmt.Sprintf("%.1f%%", s.ConvergencePct)
		latency := fmt.Sprintf("%dms", s.LatencyMs)
		fmt.Fprintf(w, "  %s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Addr, s.EnvoyCount, compliant, convPct, latency, patterns, status)
	}
	w.Flush()

	return nil
}
