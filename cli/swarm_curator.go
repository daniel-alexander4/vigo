// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Operator-side surface onto the curator (ADR-024) — the P2P artifact
// registry, the fifth swarm content subsystem. Read-only + the admin
// moderation backstop (`block`/`unblock`).
//
// Per ADR-024 open question #1 (resolved 2026-05-13): mutating verbs
// (`push`/`tag`/`untag`/`set-recipients`/`set-target`/`rm`/`prune`/
// `gc`/`stale`/`purge`) live on the agent side as `vigo swarm curator`
// — they need an unlocked per-user puddle to sign catalog entries, so
// vigocli's root-only / peer-auth posture doesn't fit. If an
// operator-puddle on the server host ever lands, those verbs join here.
//
// Endpoints sourced from `server/api/swarm_curator.go` (slice 3a.2).
// Mirrors the shape of `vigocli swarm gitback` + `vigocli swarm lockbox`
// — duplicate response structs locally so the CLI stays decoupled from
// the server package.

// --- response shapes (mirror server/api/swarm_curator.go) ---

type curatorPlatform struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	BlobSha string `json:"blob_sha"`
	Size    uint64 `json:"size"`
}

type curatorArtifactVersion struct {
	Platforms []curatorPlatform `json:"platforms"`
}

type curatorCatalogEntryBody struct {
	SchemaVersion       uint32                            `json:"schema_version"`
	Name                string                            `json:"name"`
	Kind                string                            `json:"kind"`
	FounderPuddlePubkey string                            `json:"founder_puddle_pubkey"`
	SigningPuddlePubkey string                            `json:"signing_puddle_pubkey"`
	Versions            map[string]curatorArtifactVersion `json:"versions"`
	Tags                map[string]string                 `json:"tags"`
	Recipients          []string                          `json:"recipients"`
	Target              string                            `json:"target"`
	IssuedAt            int64                             `json:"issued_at"`
}

type curatorCatalogEntry struct {
	Body      curatorCatalogEntryBody `json:"body"`
	Signature string                  `json:"signature"`
}

type curatorCatalog struct {
	GeneratedAt  time.Time             `json:"generated_at"`
	Artifacts    []curatorCatalogEntry `json:"artifacts"`
	Participants []string              `json:"participants"`
}

type curatorBlockedArtifact struct {
	ArtifactID string    `json:"artifact_id"`
	BlockedAt  time.Time `json:"blocked_at"`
	BlockedBy  string    `json:"blocked_by"`
	Reason     string    `json:"reason"`
}

type curatorListResponse struct {
	Catalog *curatorCatalog          `json:"catalog"`
	Blocked []curatorBlockedArtifact `json:"blocked"`
}

type curatorInspectResponse struct {
	ArtifactID string               `json:"artifact_id"`
	Entry      *curatorCatalogEntry `json:"entry"`
	Blocked    bool                 `json:"blocked"`
}

type curatorVersionsResponse struct {
	ArtifactID string                            `json:"artifact_id"`
	Name       string                            `json:"name"`
	Versions   map[string]curatorArtifactVersion `json:"versions"`
	Tags       map[string]string                 `json:"tags"`
}

type curatorResolveResponse struct {
	ArtifactID  string              `json:"artifact_id"`
	Version     string              `json:"version"`
	BlobSha     string              `json:"blob_sha"`
	Size        uint64              `json:"size"`
	SignedEntry curatorCatalogEntry `json:"signed_entry"`
}

// --- top-level cobra wiring ---

var curatorCmd = &cobra.Command{
	Use:   "curator",
	Short: "Operator-side view of the curator artifact registry",
	Long: `Read-only admin view of curator — the P2P artifact registry that
publishes release binaries, tarballs, packages, and container images
across the fleet (ADR-024). Each artifact is a signed catalog entry
plus a set of content-addressed blobs gossiped over the swarm
substrate; the operator's puddle is the implicit default namespace.

Mutating verbs (push / tag / untag / set-recipients / set-target / rm
/ prune / gc / stale / purge) live on the agent side as
'vigo swarm curator', run as a real user with an unlocked puddle.
This command is read-only plus the admin moderation backstop:

  status      Fleet rollup (artifacts, participants, blocked count, hosts).
  list        One row per artifact across the fleet.
  show        Detailed view of one artifact (full signed catalog entry).
  versions    Version/tag table for one artifact.
  resolve     Show the blob coordinates a pull would fetch.
  block       Operator-block an artifact (server refuses to resolve/relay).
  unblock     Reverse a prior block.`,
}

var curatorStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Fleet-wide curator totals (artifacts, participants, blocked)",
	Args:  cobra.NoArgs,
	RunE:  runCuratorStatus,
}

var curatorListCmd = &cobra.Command{
	Use:   "list",
	Short: "One row per artifact across the fleet",
	Args:  cobra.NoArgs,
	RunE:  runCuratorList,
}

var curatorShowCmd = &cobra.Command{
	Use:   "show <artifact_id|name>",
	Short: "Detailed view of one artifact (full signed catalog entry)",
	Args:  cobra.ExactArgs(1),
	RunE:  runCuratorShow,
}

var curatorVersionsCmd = &cobra.Command{
	Use:   "versions <artifact_id|name>",
	Short: "Version + tag table for one artifact",
	Args:  cobra.ExactArgs(1),
	RunE:  runCuratorVersions,
}

var curatorResolveCmd = &cobra.Command{
	Use:   "resolve <artifact_id|name> <tag-or-version> <os> <arch>",
	Short: "Show the blob coordinates a pull would fetch (does not download)",
	Args:  cobra.ExactArgs(4),
	RunE:  runCuratorResolve,
}

var curatorBlockCmd = &cobra.Command{
	Use:   "block <artifact_id|name>",
	Short: "Operator-block an artifact — the server refuses to resolve or relay it",
	Long: `Mark an artifact as operator-blocked. The server stops resolving
it on /api/v1/swarm/curator/resolve and stops relaying its bytes on
/blob/<sha>, regardless of who published it. The signed catalog entry
itself still exists and gossips, and the DR bytes still sit on every
swarm-enabled envoy that holds the target replica — only a publisher
'vigo swarm curator rm' truly purges. Use --reason to record the why
on the audit row.`,
	Args: cobra.ExactArgs(1),
	RunE: runCuratorBlock,
}

var curatorUnblockCmd = &cobra.Command{
	Use:   "unblock <artifact_id|name>",
	Short: "Reverse a prior block",
	Args:  cobra.ExactArgs(1),
	RunE:  runCuratorUnblock,
}

// --- fetchers ---

func fetchCuratorList() (*curatorListResponse, error) {
	client := newAPIClient()
	var resp curatorListResponse
	if err := client.get("/api/v1/swarm/curator/list", &resp); err != nil {
		return nil, fmt.Errorf("fetching curator catalog: %w", err)
	}
	return &resp, nil
}

func fetchCuratorInspect(artifactID string) (*curatorInspectResponse, error) {
	client := newAPIClient()
	var resp curatorInspectResponse
	if err := client.get("/api/v1/swarm/curator/inspect/"+url.PathEscape(artifactID), &resp); err != nil {
		return nil, fmt.Errorf("fetching curator entry: %w", err)
	}
	return &resp, nil
}

func fetchCuratorVersions(artifactID string) (*curatorVersionsResponse, error) {
	client := newAPIClient()
	var resp curatorVersionsResponse
	if err := client.get("/api/v1/swarm/curator/versions/"+url.PathEscape(artifactID), &resp); err != nil {
		return nil, fmt.Errorf("fetching curator versions: %w", err)
	}
	return &resp, nil
}

// --- runners ---

func runCuratorStatus(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	list, err := fetchCuratorList()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(list)
	}

	gen := time.Time{}
	artifacts := 0
	var participants []string
	if list.Catalog != nil {
		gen = list.Catalog.GeneratedAt
		artifacts = len(list.Catalog.Artifacts)
		participants = list.Catalog.Participants
	}
	fmt.Printf("Curator — generated %s\n\n", gen.Local().Format("2006-01-02 15:04:05"))

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%d\n", bold("Artifacts"), artifacts)
	fmt.Fprintf(w, "%s\t%d\n", bold("Participants"), len(participants))
	fmt.Fprintf(w, "%s\t%s\n", bold("Blocked"), curatorBlockedCount(len(list.Blocked)))
	w.Flush()

	if len(participants) > 0 {
		fmt.Println()
		fmt.Println("Participating hosts:")
		for _, h := range participants {
			fmt.Printf("  %s\n", h)
		}
	}
	return nil
}

func runCuratorList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	list, err := fetchCuratorList()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(list)
	}
	return renderCuratorList(list, os.Stdout)
}

func renderCuratorList(list *curatorListResponse, out io.Writer) error {
	if list.Catalog == nil || len(list.Catalog.Artifacts) == 0 {
		fmt.Fprintln(out, "No curator artifacts reported by the fleet.")
		if len(list.Blocked) > 0 {
			fmt.Fprintf(out, "(%d artifact_id(s) are operator-blocked but have no current entry.)\n", len(list.Blocked))
		}
		return nil
	}

	// Index blocked status by artifact_id for the BLOCK column.
	blocked := make(map[string]bool, len(list.Blocked))
	for _, b := range list.Blocked {
		blocked[strings.ToLower(b.ArtifactID)] = true
	}

	sorted := append([]curatorCatalogEntry(nil), list.Catalog.Artifacts...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Body.Name != sorted[j].Body.Name {
			return sorted[i].Body.Name < sorted[j].Body.Name
		}
		return sorted[i].Body.FounderPuddlePubkey < sorted[j].Body.FounderPuddlePubkey
	})

	w := newAlignedTable(out)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		bold("NAME"), bold("KIND"), bold("VERSIONS"), bold("TAGS"), bold("PLATFORMS"),
		bold("TARGET"), bold("STATUS"), bold("ARTIFACT_ID"))
	for _, e := range sorted {
		aid, err := curatorArtifactID(e)
		if err != nil {
			aid = strings.Repeat("?", 12)
		}
		platforms := platformSummary(e)
		status := green("ok")
		if blocked[strings.ToLower(aid)] {
			status = red("blocked")
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n",
			e.Body.Name,
			e.Body.Kind,
			len(e.Body.Versions),
			len(e.Body.Tags),
			platforms,
			truncate(e.Body.Target, 20),
			status,
			shortSha(aid))
	}
	w.Flush()

	// Footer: any blocked rows that aren't in the live catalog (publisher rm'd
	// after the block landed). Operator should see these or they'll wonder.
	var orphans []curatorBlockedArtifact
	for _, b := range list.Blocked {
		live := false
		for _, e := range sorted {
			if aid, err := curatorArtifactID(e); err == nil && strings.EqualFold(aid, b.ArtifactID) {
				live = true
				break
			}
		}
		if !live {
			orphans = append(orphans, b)
		}
	}
	if len(orphans) > 0 {
		fmt.Fprintf(out, "\n%s %d block(s) without a current catalog entry:\n",
			yellow("Note:"), len(orphans))
		for _, b := range orphans {
			when := ""
			if !b.BlockedAt.IsZero() {
				when = "  blocked " + b.BlockedAt.Local().Format("2006-01-02")
			}
			by := ""
			if b.BlockedBy != "" {
				by = " by " + b.BlockedBy
			}
			reason := ""
			if b.Reason != "" {
				reason = " — " + b.Reason
			}
			fmt.Fprintf(out, "  %s%s%s%s\n", shortSha(b.ArtifactID), when, by, reason)
		}
	}
	return nil
}

func runCuratorShow(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	target := args[0]
	resp, err := resolveAndShow(target)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(resp)
	}
	return renderCuratorShow(resp, os.Stdout)
}

func renderCuratorShow(resp *curatorInspectResponse, out io.Writer) error {
	if resp.Entry == nil && !resp.Blocked {
		fmt.Fprintln(out, "No such artifact.")
		return nil
	}
	if resp.Entry == nil {
		fmt.Fprintf(out, "Artifact %s is %s but has no current catalog entry (publisher rm'd).\n",
			shortSha(resp.ArtifactID), red("blocked"))
		return nil
	}

	e := resp.Entry
	fmt.Fprintf(out, "Name:           %s\n", bold(e.Body.Name))
	fmt.Fprintf(out, "Artifact ID:    %s\n", resp.ArtifactID)
	fmt.Fprintf(out, "Kind:           %s\n", e.Body.Kind)
	fmt.Fprintf(out, "Founder puddle: ed25519:%s\n", e.Body.FounderPuddlePubkey)
	if !strings.EqualFold(e.Body.SigningPuddlePubkey, e.Body.FounderPuddlePubkey) {
		fmt.Fprintf(out, "Signing puddle: ed25519:%s  %s\n",
			e.Body.SigningPuddlePubkey, dim("(rekeyed — delegation chain bridges)"))
	}
	fmt.Fprintf(out, "Target:         %s\n", e.Body.Target)
	fmt.Fprintf(out, "Recipients:     %s\n", strings.Join(e.Body.Recipients, ", "))
	fmt.Fprintf(out, "Issued at:      %s\n",
		time.UnixMilli(e.Body.IssuedAt).Local().Format("2006-01-02 15:04:05"))
	status := green("ok")
	if resp.Blocked {
		status = red("blocked")
	}
	fmt.Fprintf(out, "Status:         %s\n\n", status)

	// Versions + per-platform breakdown.
	verKeys := make([]string, 0, len(e.Body.Versions))
	for v := range e.Body.Versions {
		verKeys = append(verKeys, v)
	}
	sort.Strings(verKeys)
	if len(verKeys) > 0 {
		// Reverse tag map for the row's TAGS column.
		tagsByVersion := make(map[string][]string)
		for tag, v := range e.Body.Tags {
			tagsByVersion[v] = append(tagsByVersion[v], tag)
		}
		for _, ts := range tagsByVersion {
			sort.Strings(ts)
		}

		fmt.Fprintln(out, "Versions:")
		w := newAlignedTable(out)
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n",
			bold("VERSION"), bold("TAGS"), bold("OS/ARCH"), bold("SIZE"), bold("SHA"))
		for _, v := range verKeys {
			av := e.Body.Versions[v]
			tagCol := "—"
			if ts := tagsByVersion[v]; len(ts) > 0 {
				tagCol = strings.Join(ts, ",")
			}
			if len(av.Platforms) == 0 {
				fmt.Fprintf(w, "  %s\t%s\t—\t—\t—\n", v, tagCol)
				continue
			}
			for i, p := range av.Platforms {
				vCol := ""
				tagsCol := ""
				if i == 0 {
					vCol = v
					tagsCol = tagCol
				}
				fmt.Fprintf(w, "  %s\t%s\t%s/%s\t%s\t%s\n",
					vCol, tagsCol, p.OS, p.Arch,
					formatBytes(int64(p.Size)), shortSha(p.BlobSha))
			}
		}
		w.Flush()
	}

	// Surface dangling tags (point at a version that doesn't exist).
	var dangling []string
	for tag, v := range e.Body.Tags {
		if _, ok := e.Body.Versions[v]; !ok {
			dangling = append(dangling, fmt.Sprintf("%s → %s", tag, v))
		}
	}
	if len(dangling) > 0 {
		sort.Strings(dangling)
		fmt.Fprintf(out, "\n%s %d dangling tag(s): %s\n",
			yellow("Note:"), len(dangling), strings.Join(dangling, ", "))
	}
	return nil
}

func runCuratorVersions(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	target := args[0]
	// versions/<aid> requires a hex artifact_id; let the server normalize.
	resp, err := fetchCuratorVersions(target)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(resp)
	}
	if len(resp.Versions) == 0 {
		fmt.Printf("No versions reported for %s.\n", resp.Name)
		return nil
	}
	fmt.Printf("Artifact: %s  (id %s)\n\n", bold(resp.Name), shortSha(resp.ArtifactID))

	tagsByVersion := make(map[string][]string)
	for tag, v := range resp.Tags {
		tagsByVersion[v] = append(tagsByVersion[v], tag)
	}
	for _, ts := range tagsByVersion {
		sort.Strings(ts)
	}

	verKeys := make([]string, 0, len(resp.Versions))
	for v := range resp.Versions {
		verKeys = append(verKeys, v)
	}
	sort.Strings(verKeys)

	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%s\t%s\n", bold("VERSION"), bold("TAGS"), bold("PLATFORMS"))
	for _, v := range verKeys {
		av := resp.Versions[v]
		tagCol := "—"
		if ts := tagsByVersion[v]; len(ts) > 0 {
			tagCol = strings.Join(ts, ",")
		}
		plats := make([]string, 0, len(av.Platforms))
		for _, p := range av.Platforms {
			plats = append(plats, fmt.Sprintf("%s/%s", p.OS, p.Arch))
		}
		sort.Strings(plats)
		fmt.Fprintf(w, "%s\t%s\t%s\n", v, tagCol, strings.Join(plats, ", "))
	}
	w.Flush()
	return nil
}

func runCuratorResolve(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	aid, tagOrVer, os_, arch := args[0], args[1], args[2], args[3]
	client := newAPIClient()
	q := url.Values{}
	q.Set("artifact_id", aid)
	// The /resolve endpoint takes either tag= or version=. Send as tag= and
	// rely on the server's fallthrough to version= if the tag doesn't exist.
	q.Set("tag", tagOrVer)
	q.Set("os", os_)
	q.Set("arch", arch)
	var resp curatorResolveResponse
	if err := client.get("/api/v1/swarm/curator/resolve?"+q.Encode(), &resp); err != nil {
		// Fall through to version= for a literal version that the server
		// didn't accept as a tag (server-side handler treats them as
		// alternatives, not fallbacks).
		q2 := url.Values{}
		q2.Set("artifact_id", aid)
		q2.Set("version", tagOrVer)
		q2.Set("os", os_)
		q2.Set("arch", arch)
		if err2 := client.get("/api/v1/swarm/curator/resolve?"+q2.Encode(), &resp); err2 != nil {
			return fmt.Errorf("resolve: %w", err)
		}
	}
	if asJSON {
		return writeJSONPretty(resp)
	}
	fmt.Printf("Resolved %s/%s @ %s on %s/%s:\n\n",
		shortSha(resp.ArtifactID), resp.SignedEntry.Body.Name, resp.Version, os_, arch)
	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%s\n", bold("Blob sha"), resp.BlobSha)
	fmt.Fprintf(w, "%s\t%s\n", bold("Size"), formatBytes(int64(resp.Size)))
	fmt.Fprintf(w, "%s\t%s\n", bold("Version"), resp.Version)
	w.Flush()
	return nil
}

func runCuratorBlock(cmd *cobra.Command, args []string) error {
	reason, _ := cmd.Flags().GetString("reason")
	aid, err := resolveCuratorTarget(args[0])
	if err != nil {
		return err
	}
	client := newAPIClient()
	path := "/api/v1/swarm/curator/block/" + url.PathEscape(aid)
	if reason != "" {
		path += "?reason=" + url.QueryEscape(reason)
	}
	var resp map[string]any
	if err := client.post(path, nil, &resp); err != nil {
		return fmt.Errorf("block: %w", err)
	}
	fmt.Printf("Blocked %s.\n", shortSha(aid))
	if reason != "" {
		fmt.Printf("Reason: %s\n", reason)
	}
	return nil
}

func runCuratorUnblock(cmd *cobra.Command, args []string) error {
	aid, err := resolveCuratorTarget(args[0])
	if err != nil {
		return err
	}
	client := newAPIClient()
	var resp map[string]any
	if err := client.post("/api/v1/swarm/curator/unblock/"+url.PathEscape(aid), nil, &resp); err != nil {
		return fmt.Errorf("unblock: %w", err)
	}
	fmt.Printf("Unblocked %s.\n", shortSha(aid))
	return nil
}

// --- helpers ---

// resolveAndShow resolves `target` (artifact_id, name, or name prefix) to
// the inspectResponse for that artifact. The server's `/inspect/{aid}` route
// only accepts a hex artifact_id, so a name lookup falls back to /list.
// (REST endpoint name kept as `/inspect/{aid}` — that's an internal URL;
// the operator-facing CLI verb is `show` to match the gitback / lockbox /
// longdrawer sibling convention.)
func resolveAndShow(target string) (*curatorInspectResponse, error) {
	if isHex64CLI(target) {
		return fetchCuratorInspect(strings.ToLower(target))
	}
	list, err := fetchCuratorList()
	if err != nil {
		return nil, err
	}
	aid, err := resolveCuratorTargetIn(list, target)
	if err != nil {
		return nil, err
	}
	return fetchCuratorInspect(aid)
}

// resolveCuratorTarget turns an artifact_id, name, or name prefix into a hex
// artifact_id by consulting the fleet catalog. Used by block/unblock/show.
func resolveCuratorTarget(target string) (string, error) {
	if isHex64CLI(target) {
		return strings.ToLower(target), nil
	}
	list, err := fetchCuratorList()
	if err != nil {
		return "", err
	}
	return resolveCuratorTargetIn(list, target)
}

func resolveCuratorTargetIn(list *curatorListResponse, target string) (string, error) {
	if list == nil || list.Catalog == nil || len(list.Catalog.Artifacts) == 0 {
		// A blocked-but-deleted artifact can still be referenced by hex; reject
		// non-hex targets when there's nothing to match against.
		return "", fmt.Errorf("no curator artifacts in the fleet to resolve %q", target)
	}
	t := strings.ToLower(target)

	// Exact name match wins (artifact_ids carry the founder pubkey, so two
	// different founders' artifacts can share a name — that's an ambiguity).
	var exact []curatorCatalogEntry
	for _, e := range list.Catalog.Artifacts {
		if e.Body.Name == target {
			exact = append(exact, e)
		}
	}
	if len(exact) == 1 {
		return curatorArtifactID(exact[0])
	}
	if len(exact) > 1 {
		ids := make([]string, len(exact))
		for i, e := range exact {
			aid, _ := curatorArtifactID(e)
			ids[i] = shortSha(aid)
		}
		return "", fmt.Errorf("name %q is published by %d different founders (artifact_ids %s); pass the full artifact_id",
			target, len(exact), strings.Join(ids, ", "))
	}

	// Unique name prefix.
	var prefix []curatorCatalogEntry
	for _, e := range list.Catalog.Artifacts {
		if strings.HasPrefix(strings.ToLower(e.Body.Name), t) {
			prefix = append(prefix, e)
		}
	}
	if len(prefix) == 1 {
		return curatorArtifactID(prefix[0])
	}
	if len(prefix) > 1 {
		names := make([]string, len(prefix))
		for i, e := range prefix {
			names[i] = e.Body.Name
		}
		return "", fmt.Errorf("prefix %q is ambiguous; candidates: %s",
			target, strings.Join(names, ", "))
	}
	return "", fmt.Errorf("no curator artifact matches %q", target)
}

// curatorArtifactID derives sha256(founder_puddle_pubkey ‖ name) as a hex
// string. Mirrors curatormesh.ArtifactID and the agent-side
// catalog::artifact_id_for. Errors when founder_puddle_pubkey isn't 32 bytes
// of hex.
func curatorArtifactID(e curatorCatalogEntry) (string, error) {
	return computeArtifactID(e.Body.FounderPuddlePubkey, e.Body.Name)
}

func computeArtifactID(founderPubkeyHex, name string) (string, error) {
	pk, err := hex.DecodeString(founderPubkeyHex)
	if err != nil {
		return "", fmt.Errorf("founder_puddle_pubkey: %w", err)
	}
	if len(pk) != 32 {
		return "", errors.New("founder_puddle_pubkey is not 32 bytes")
	}
	h := sha256.New()
	h.Write(pk)
	h.Write([]byte(name))
	return hex.EncodeToString(h.Sum(nil)), nil
}

func platformSummary(e curatorCatalogEntry) string {
	if len(e.Body.Versions) == 0 {
		return "—"
	}
	seen := map[string]struct{}{}
	for _, av := range e.Body.Versions {
		for _, p := range av.Platforms {
			seen[fmt.Sprintf("%s/%s", p.OS, p.Arch)] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func curatorBlockedCount(n int) string {
	if n == 0 {
		return "0"
	}
	return yellow(fmt.Sprintf("%d", n))
}

func isHex64CLI(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func init() {
	curatorStatusCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	curatorListCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	curatorShowCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	curatorVersionsCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	curatorResolveCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	curatorBlockCmd.Flags().String("reason", "", "Free-text reason recorded on the block row")

	curatorCmd.AddCommand(
		curatorStatusCmd,
		curatorListCmd,
		curatorShowCmd,
		curatorVersionsCmd,
		curatorResolveCmd,
		curatorBlockCmd,
		curatorUnblockCmd,
	)
	// Hidden redirects for agent-side verbs operators commonly mis-type into
	// vigocli. Per ADR-024 open Q#1: mutating + cleanup verbs live on
	// `vigo swarm curator` because they need an unlocked per-user puddle.
	addAgentRedirects(curatorCmd, "curator", "curator", []string{
		"push", "tag", "untag", "set-recipients", "set-target", "rm",
		"prune", "gc", "stale", "purge", "pull",
	})
}
