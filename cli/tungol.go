// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

var tungolOpsPuddlePairCmd = &cobra.Command{
	Use:   "pair",
	Short: "Mint a 5-minute pair code authorizing another vigosrv to join this ops-puddle",
	Long: `Generates a single-use pair code with a 5-minute TTL and writes a
pending-pair record to --ops-puddle-dir/pending-pair (0600). Prints
the joining command (with --from <this-host> + the code) for the
operator to paste on the joining vigosrv.

Run this on a vigosrv that already holds ops-puddle membership.
Running again overwrites any prior outstanding offer.`,
	RunE: runTungolOpsPuddlePair,
}

var tungolOpsPuddleJoinCmd = &cobra.Command{
	Use:   "join --from <https-url> --code <code>",
	Short: "Join an existing ops-puddle using a pair code minted on an existing member",
	Long: `Calls /api/v1/tungol/ops-puddle/pair-claim on the --from vigosrv,
unwraps the returned ops-puddle seed, re-wraps it under THIS
vigosrv's master-key-derived wrap key, and persists the ops-puddle
state under --ops-puddle-dir.

Both --from (existing-member URL) and --code (the code printed by
'pair') are required. Admin auth against --from must already be
configured (vigocli auth login --server <from> or environment).`,
	RunE: runTungolOpsPuddleJoin,
}

func init() {
	tungolCmd.PersistentFlags().String("key-file", "", "Path to master encryption key (omit to auto-detect inside --secrets-dir)")
	tungolCmd.PersistentFlags().String("secrets-dir", "/srv/vigo/secrets", "Directory holding the secrets backend's master key")
	tungolCmd.PersistentFlags().String("puddle-dir", puddle.DefaultDir, "Directory holding this vigosrv's service-account puddle")
	tungolCmd.PersistentFlags().String("ops-puddle-dir", puddle.OpsDefaultDir, "Directory holding the ops-puddle wrap (per-host)")

	tungolOpsPuddleJoinCmd.Flags().String("from", "", "HTTPS URL of an existing ops-puddle member vigosrv (e.g. https://hub.example.com:8443)")
	tungolOpsPuddleJoinCmd.Flags().String("code", "", "Pair code printed by `vigocli tungol ops-puddle pair` on --from")
	_ = tungolOpsPuddleJoinCmd.MarkFlagRequired("from")
	_ = tungolOpsPuddleJoinCmd.MarkFlagRequired("code")

	tungolCmd.AddCommand(tungolStatusCmd)
	tungolCmd.AddCommand(tungolSteorraCmd)
	tungolCmd.AddCommand(tungolOpsPuddleCmd)
	tungolOpsPuddleCmd.AddCommand(tungolOpsPuddleInitCmd)
	tungolOpsPuddleCmd.AddCommand(tungolOpsPuddleStatusCmd)
	tungolOpsPuddleCmd.AddCommand(tungolOpsPuddlePairCmd)
	tungolOpsPuddleCmd.AddCommand(tungolOpsPuddleJoinCmd)

	// Init / join write new master-key-derived state — same operator-side
	// gate as `vigocli secrets set`. requireUnlocked uses an exempt-by-verb-
	// name map scoped to `vigocli secrets`, where "init" is the bootstrap
	// verb; dispatching through that name here would silently exempt these
	// verbs. Wrap them so the gate is unconditional. Pair only writes the
	// pending-pair record (no key material), but also gates because the
	// operator must be authorized to authorize a new member. Status reads
	// public files only and stays open.
	tungolOpsPuddleInitCmd.PreRunE = requireUnlockedAlways
	tungolOpsPuddlePairCmd.PreRunE = requireUnlockedAlways
	tungolOpsPuddleJoinCmd.PreRunE = requireUnlockedAlways
}

// requireUnlockedAlways calls the underlying gate primitives without the
// `vigocli secrets`-scoped exemption list. Wired onto tungol verbs that
// write master-key-derived state (e.g. ops-puddle init).
func requireUnlockedAlways(cmd *cobra.Command, _ []string) error {
	cmd.SilenceUsage = true
	if !verifierExists() {
		return vaultNotInitializedError()
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

// runTungolOpsPuddlePair mints a fresh pair code, persists the
// pending-pair record under --ops-puddle-dir, and prints the
// command the operator should paste on the joining vigosrv.
func runTungolOpsPuddlePair(cmd *cobra.Command, _ []string) error {
	opsDir, _ := cmd.Flags().GetString("ops-puddle-dir")

	// Sanity: pair only makes sense if this vigosrv already holds
	// ops-puddle membership. Check by stat — no need to unwrap.
	if _, err := os.Stat(puddle.WrappedPath(opsDir)); err != nil {
		return fmt.Errorf(
			"no ops-puddle at %s: %w (run `vigocli tungol ops-puddle init <name>` to found one, or `join` to claim one)",
			opsDir, err,
		)
	}

	code, err := puddle.GeneratePairCode()
	if err != nil {
		return fmt.Errorf("generating pair code: %w", err)
	}
	pending := &puddle.PendingPair{
		CodeHash:  puddle.HashPairCode(code),
		ExpiresAt: time.Now().UTC().Add(puddle.PairCodeTTL),
		Used:      false,
	}
	if err := puddle.WritePendingPair(opsDir, pending); err != nil {
		return fmt.Errorf("writing pending pair: %w", err)
	}

	thisHost := localHostnameForPair()
	expires := pending.ExpiresAt.Local().Format("15:04:05 MST")

	fmt.Printf("Pair offer minted (expires %s, single-use)\n\n", expires)
	fmt.Printf("  On the joining vigosrv, run:\n\n")
	fmt.Printf("    vigocli tungol ops-puddle join \\\n")
	fmt.Printf("      --from https://%s:8443 \\\n", thisHost)
	fmt.Printf("      --code %s\n\n", code)
	fmt.Printf("  (The operator on the joining vigosrv must already have admin\n")
	fmt.Printf("   credentials for this vigosrv — run `vigocli auth login --server\n")
	fmt.Printf("   https://%s:8443` first if not.)\n", thisHost)
	return nil
}

// localHostnameForPair returns a best-effort identifier for this
// vigosrv to show in the join command. Falls back to "<this-host>"
// if hostname lookup fails — the operator can replace it manually.
func localHostnameForPair() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "<this-host>"
	}
	return h
}

// runTungolOpsPuddleJoin claims a pair offer from --from, unwraps
// the returned seed, re-wraps it under THIS vigosrv's local wrap
// key, and persists the ops-puddle state.
func runTungolOpsPuddleJoin(cmd *cobra.Command, _ []string) error {
	from, _ := cmd.Flags().GetString("from")
	code, _ := cmd.Flags().GetString("code")
	keyFile, _ := cmd.Flags().GetString("key-file")
	secretsDir, _ := cmd.Flags().GetString("secrets-dir")
	puddleDir, _ := cmd.Flags().GetString("puddle-dir")
	opsDir, _ := cmd.Flags().GetString("ops-puddle-dir")

	// Local secrets backend + service-account puddle (to identify
	// ourselves as the joining member).
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

	// Cross-vigosrv call: build a one-off client against --from.
	client := newAPIClientFor(from)
	body := map[string]string{
		"code":           code,
		"joining_pubkey": svc.PublicKeyText,
	}
	var resp struct {
		SeedB64      string             `json:"seed_b64"`
		FriendlyName string             `json:"friendly_name"`
		Members      []puddle.OpsMember `json:"members"`
	}
	if err := client.post("/api/v1/tungol/ops-puddle/pair-claim", body, &resp); err != nil {
		return fmt.Errorf("pair-claim against %s: %w", from, err)
	}

	seed, err := base64.StdEncoding.DecodeString(resp.SeedB64)
	if err != nil {
		return fmt.Errorf("decoding seed from %s: %w", from, err)
	}

	ops, err := puddle.IngestOpsPuddle(provider, opsDir, resp.FriendlyName, seed, resp.Members)
	if err != nil {
		return fmt.Errorf("ingesting ops-puddle locally: %w", err)
	}

	fmt.Printf("Ops-puddle joined\n")
	fmt.Printf("  Friendly name: %s\n", ops.FriendlyName)
	fmt.Printf("  Pubkey:        %s\n", ops.PublicKeyText)
	fmt.Printf("  Dir:           %s\n", opsDir)
	fmt.Printf("  Members (%d):\n", len(ops.Members))
	for _, m := range ops.Members {
		marker := ""
		if m.Pubkey == svc.PublicKeyText {
			marker = " (this vigosrv)"
		}
		fmt.Printf("    - %s%s\n", m.Pubkey, marker)
	}
	return nil
}

// runTungolStatus prints this server's tungol role and (in hub mode)
// the steorras it aggregates.
func runTungolStatus(cmd *cobra.Command, args []string) error {
	client := newAPIClient()

	var resp struct {
		Role               string `json:"role"`
		TungolID           string `json:"tungol_id"`
		SteorraID          string `json:"steorra_id"`
		LocalSteorraPubkey string `json:"local_steorra_pubkey"`
		Steorras           []struct {
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
		SteorraPins []struct {
			TungolID  string `json:"tungol_id"`
			SteorraID string `json:"steorra_id"`
			Pubkey    string `json:"pubkey"`
			PinnedAt  string `json:"pinned_at"`
			LastSeen  string `json:"last_seen"`
		} `json:"steorra_pins"`
	}

	if err := client.get("/api/v1/tungol/status", &resp); err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}

	fmt.Printf("  Role: %s\n", bold(resp.Role))

	if resp.TungolID != "" {
		fmt.Printf("  Tungol ID: %s\n", resp.TungolID)
	}
	if resp.SteorraID != "" {
		fmt.Printf("  Steorra ID: %s\n", resp.SteorraID)
	}
	// Phase 1: local steorra pubkey for operator cross-check.
	// On alarm, compare this short pubkey against the asserted
	// pubkey in the hub's audit event.
	if resp.LocalSteorraPubkey != "" {
		fmt.Printf("  Local steorra pubkey: %s\n", resp.LocalSteorraPubkey)
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

	// Pinned-pubkey index for quick lookup when rendering the steorra table.
	pinByID := make(map[string]string, len(resp.SteorraPins))
	for _, p := range resp.SteorraPins {
		pinByID[p.SteorraID] = p.Pubkey
	}

	fmt.Printf("  Steorras: %d (%d healthy)\n", len(resp.Steorras), healthy)
	fmt.Printf("  Total Envoys: %d\n\n", totalEnvoys)

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		header("ID"), header("ADDRESS"), header("PUBKEY"), header("ENVOYS"),
		header("COMPLIANT"), header("COMPLIANCE"), header("LATENCY"),
		header("PATTERNS"), header("STATUS"))
	for _, s := range resp.Steorras {
		status := colorStatus("healthy")
		if !s.Healthy {
			status = colorStatus("unreachable")
		}
		patterns := strings.Join(s.Patterns, ", ")
		compliant := fmt.Sprintf("%d/%d", s.Converged, s.EnvoyCount)
		convPct := fmt.Sprintf("%.1f%%", s.ConvergencePct)
		latency := fmt.Sprintf("%dms", s.LatencyMs)
		pubkey := pinByID[s.ID]
		if pubkey == "" {
			pubkey = "(unpinned)"
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			s.ID, s.Addr, pubkey, s.EnvoyCount, compliant, convPct, latency, patterns, status)
	}
	w.Flush()

	return nil
}
