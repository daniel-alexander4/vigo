// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.alexander4.io/vigo/server/puddle"
	"git.alexander4.io/vigo/server/secrets"
	"github.com/spf13/cobra"
)

// tungolCmd is the new top-level namespace for the spanner-redesign
// surface (federated control-plane peer-equal model). For now it only
// hosts ops-puddle lifecycle (ADR-025 Slice 2); the steorra verbs land
// during the spanner→tungol rename slice of Phase 0.
var tungolCmd = &cobra.Command{
	Use:   "tungol",
	Short: "Federated control-plane operations (peer-equal redesign of spanner)",
	Long: `Operations for the tungol (federation) and steorra (single vigosrv) primitives
of the spanner-redesign architecture. Today this only hosts ops-puddle
lifecycle — the broader tungol/steorra surface lands as the redesign progresses.`,
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
