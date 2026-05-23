// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Operator-side surface onto poolq (ADR-029) — the P2P ordered-log queue,
// the sixth swarm content subsystem. Read-only in Slice 4; per-topic
// block/unblock moderation lands in Slice 5.
//
// Posting (`post`) lives on the agent side as `vigo swarm poolq post`,
// run as a real user with an unlocked puddle — the same posture curator
// takes for its mutating verbs.
//
// Endpoints sourced from `server/api/swarm_poolq.go`.

// --- response shapes (mirror server/api/swarm_poolq.go) ---

type poolqMessageBody struct {
	SchemaVersion      uint32 `json:"schema_version"`
	TopicName          string `json:"topic_name"`
	Seq                uint64 `json:"seq"`
	Body               string `json:"body"`
	PostedAt           int64  `json:"posted_at"`
	AuthorPuddlePubkey string `json:"author_puddle_pubkey"`
}

type poolqMessage struct {
	Body      poolqMessageBody `json:"body"`
	Signature string           `json:"signature"`
}

type poolqTopicSummary struct {
	TopicIDHex          string `json:"topic_id_hex"`
	TopicName           string `json:"topic_name"`
	FounderPuddlePubkey string `json:"founder_puddle_pubkey"`
	HeadSeq             uint64 `json:"head_seq"`
	Count               int    `json:"count"`
	FirstPostedAt       int64  `json:"first_posted_at"`
	LastPostedAt        int64  `json:"last_posted_at"`
}

type poolqTopicsResponse struct {
	GeneratedAt  time.Time           `json:"generated_at"`
	Topics       []poolqTopicSummary `json:"topics"`
	Participants []string            `json:"participants"`
}

type poolqRangeResponse struct {
	TopicIDHex string         `json:"topic_id_hex"`
	From       uint64         `json:"from"`
	To         uint64         `json:"to"`
	Messages   []poolqMessage `json:"messages"`
}

type poolqTopicFull struct {
	TopicIDHex          string         `json:"topic_id_hex"`
	TopicName           string         `json:"topic_name"`
	FounderPuddlePubkey string         `json:"founder_puddle_pubkey"`
	HeadSeq             uint64         `json:"head_seq"`
	Count               int            `json:"count"`
	FirstPostedAt       int64          `json:"first_posted_at"`
	LastPostedAt        int64          `json:"last_posted_at"`
	Blocked             bool           `json:"blocked"`
	Messages            []poolqMessage `json:"messages"`
}

// --- top-level cobra wiring ---

var poolqCmd = &cobra.Command{
	Use:   "poolq",
	Short: "Operator-side view of the poolq ordered-log queue (ADR-029)",
	Long: `Read-only admin view of poolq — the P2P ordered-log queue that
carries founder-signed, founder-monotonic, retained messages across
the fleet (ADR-029). Topics are keyed
'sha256(founder_puddle_pubkey || topic_name)'; the operator's puddle
is the implicit default namespace.

Posting (post) lives on the agent side as 'vigo swarm poolq post',
run as a real user with an unlocked puddle. This command is read-only
plus the admin moderation backstop (Slice 5):

  status      Fleet rollup (topics, participants, blocked count).
  list        One row per topic across the fleet.
  show        Detailed view of one topic — full signed message log.
  read        Print messages from a topic in a seq range.`,
}

var poolqStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Fleet-wide poolq totals (topics, participants)",
	Args:  cobra.NoArgs,
	RunE:  runPoolqStatus,
}

var poolqListCmd = &cobra.Command{
	Use:   "list",
	Short: "One row per topic across the fleet",
	Args:  cobra.NoArgs,
	RunE:  runPoolqList,
}

var poolqShowCmd = &cobra.Command{
	Use:   "show <topic_id>",
	Short: "Detailed view of one topic (full signed log)",
	Args:  cobra.ExactArgs(1),
	RunE:  runPoolqShow,
}

var poolqReadCmd = &cobra.Command{
	Use:   "read <topic_id>",
	Short: "Print messages from a topic in a seq range",
	Long: `Read messages from a topic in a [from, to] seq range. The default
range is [1, head]; --from and --to bound either end. The operator
view does not maintain a server-side offset — consumers on agents do.
This verb is for inspection; for stateful consumption, use 'vigo swarm
poolq read' on the agent side.`,
	Args: cobra.ExactArgs(1),
	RunE: runPoolqRead,
}

var poolqBlockCmd = &cobra.Command{
	Use:   "block <topic_id>",
	Short: "Operator-block a topic — server refuses to serve its message log",
	Long: `Mark a topic as operator-blocked. The server stops serving
/api/v1/swarm/poolq/topic/<id>/range and /log for that topic, and
the agent-side 'vigo swarm poolq read'/'tail' both 404. The signed
messages still exist in posting users' ~/.vigo-poolq/ directories
and the trait collector keeps emitting them — only a 'rm -rf' on
the founder's state dir truly purges. Use --reason to record the
why on the audit row.`,
	Args: cobra.ExactArgs(1),
	RunE: runPoolqBlock,
}

var poolqUnblockCmd = &cobra.Command{
	Use:   "unblock <topic_id>",
	Short: "Reverse a prior block",
	Args:  cobra.ExactArgs(1),
	RunE:  runPoolqUnblock,
}

// --- fetchers ---

func fetchPoolqTopics() (*poolqTopicsResponse, error) {
	client := newAPIClient()
	var resp poolqTopicsResponse
	if err := client.get("/api/v1/swarm/poolq/topics", &resp); err != nil {
		return nil, fmt.Errorf("fetching poolq topics: %w", err)
	}
	return &resp, nil
}

func fetchPoolqTopic(topicID string) (*poolqTopicFull, error) {
	client := newAPIClient()
	var resp poolqTopicFull
	if err := client.get("/api/v1/swarm/poolq/topic/"+url.PathEscape(topicID)+"/log", &resp); err != nil {
		return nil, fmt.Errorf("fetching poolq topic: %w", err)
	}
	return &resp, nil
}

func fetchPoolqRange(topicID string, from, to uint64) (*poolqRangeResponse, error) {
	client := newAPIClient()
	q := fmt.Sprintf("?from=%d", from)
	if to > 0 {
		q += fmt.Sprintf("&to=%d", to)
	}
	var resp poolqRangeResponse
	if err := client.get("/api/v1/swarm/poolq/topic/"+url.PathEscape(topicID)+"/range"+q, &resp); err != nil {
		return nil, fmt.Errorf("fetching poolq range: %w", err)
	}
	return &resp, nil
}

// --- runners ---

func runPoolqStatus(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	resp, err := fetchPoolqTopics()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(resp)
	}
	fmt.Printf("Poolq — generated %s\n\n", resp.GeneratedAt.Local().Format("2006-01-02 15:04:05"))
	w := newAlignedTable(os.Stdout)
	fmt.Fprintf(w, "%s\t%d\n", bold("Topics"), len(resp.Topics))
	fmt.Fprintf(w, "%s\t%d\n", bold("Participants"), len(resp.Participants))
	totalMsgs := 0
	for _, t := range resp.Topics {
		totalMsgs += t.Count
	}
	fmt.Fprintf(w, "%s\t%d\n", bold("Messages"), totalMsgs)
	w.Flush()
	if len(resp.Participants) > 0 {
		fmt.Println()
		fmt.Println("Participating hosts:")
		for _, h := range resp.Participants {
			fmt.Printf("  %s\n", h)
		}
	}
	return nil
}

func runPoolqList(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	resp, err := fetchPoolqTopics()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(resp)
	}
	return renderPoolqList(resp, os.Stdout)
}

func renderPoolqList(resp *poolqTopicsResponse, out io.Writer) error {
	if len(resp.Topics) == 0 {
		fmt.Fprintln(out, "No poolq topics reported by the fleet.")
		return nil
	}
	w := newAlignedTable(out)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
		bold("NAME"), bold("HEAD_SEQ"), bold("COUNT"), bold("FIRST"), bold("LAST"), bold("TOPIC_ID"))
	for _, t := range resp.Topics {
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\t%s\n",
			truncate(t.TopicName, 28),
			t.HeadSeq,
			t.Count,
			formatPoolqTime(t.FirstPostedAt),
			formatPoolqTime(t.LastPostedAt),
			shortSha(t.TopicIDHex),
		)
	}
	return w.Flush()
}

func runPoolqShow(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	topicID := strings.ToLower(strings.TrimSpace(args[0]))
	if !isHex64Cli(topicID) {
		return errors.New("topic_id must be 64 hex characters")
	}
	t, err := fetchPoolqTopic(topicID)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(t)
	}
	fmt.Printf("Topic %s (%s)\n", t.TopicIDHex, t.TopicName)
	fmt.Printf("  Founder:    %s\n", t.FounderPuddlePubkey)
	fmt.Printf("  Head seq:   %d\n", t.HeadSeq)
	fmt.Printf("  Count:      %d\n", t.Count)
	fmt.Printf("  First:      %s\n", formatPoolqTime(t.FirstPostedAt))
	fmt.Printf("  Last:       %s\n", formatPoolqTime(t.LastPostedAt))
	if t.Blocked {
		fmt.Printf("  Status:     %s\n", red("blocked"))
	}
	if len(t.Messages) == 0 {
		fmt.Println("\nNo messages.")
		return nil
	}
	fmt.Println()
	renderPoolqMessages(t.Messages, os.Stdout)
	return nil
}

func runPoolqRead(cmd *cobra.Command, args []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")
	from, _ := cmd.Flags().GetUint64("from")
	to, _ := cmd.Flags().GetUint64("to")
	if from == 0 {
		from = 1
	}
	topicID := strings.ToLower(strings.TrimSpace(args[0]))
	if !isHex64Cli(topicID) {
		return errors.New("topic_id must be 64 hex characters")
	}
	r, err := fetchPoolqRange(topicID, from, to)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSONPretty(r)
	}
	if len(r.Messages) == 0 {
		fmt.Printf("No messages in [%d, %s].\n", from, toSuffix(to))
		return nil
	}
	renderPoolqMessages(r.Messages, os.Stdout)
	return nil
}

// --- helpers ---

func renderPoolqMessages(msgs []poolqMessage, out io.Writer) {
	w := newAlignedTable(out)
	fmt.Fprintf(w, "%s\t%s\t%s\n", bold("SEQ"), bold("POSTED_AT"), bold("BODY"))
	for _, m := range msgs {
		fmt.Fprintf(w, "%d\t%s\t%s\n",
			m.Body.Seq,
			formatPoolqTime(m.Body.PostedAt),
			truncate(singleLine(m.Body.Body), 80),
		)
	}
	_ = w.Flush()
}

func formatPoolqTime(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
}

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	return s
}

func toSuffix(to uint64) string {
	if to == 0 {
		return "head"
	}
	return strconv.FormatUint(to, 10)
}

func runPoolqBlock(cmd *cobra.Command, args []string) error {
	topicID := strings.ToLower(strings.TrimSpace(args[0]))
	if !isHex64Cli(topicID) {
		return errors.New("topic_id must be 64 hex characters")
	}
	reason, _ := cmd.Flags().GetString("reason")
	client := newAPIClient()
	path := "/api/v1/swarm/poolq/block/" + url.PathEscape(topicID)
	if reason != "" {
		path += "?reason=" + url.QueryEscape(reason)
	}
	var resp map[string]any
	if err := client.post(path, nil, &resp); err != nil {
		return fmt.Errorf("blocking topic: %w", err)
	}
	fmt.Printf("Blocked %s\n", topicID)
	return nil
}

func runPoolqUnblock(cmd *cobra.Command, args []string) error {
	topicID := strings.ToLower(strings.TrimSpace(args[0]))
	if !isHex64Cli(topicID) {
		return errors.New("topic_id must be 64 hex characters")
	}
	client := newAPIClient()
	path := "/api/v1/swarm/poolq/unblock/" + url.PathEscape(topicID)
	var resp map[string]any
	if err := client.post(path, nil, &resp); err != nil {
		return fmt.Errorf("unblocking topic: %w", err)
	}
	fmt.Printf("Unblocked %s\n", topicID)
	return nil
}

func isHex64Cli(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func init() {
	poolqStatusCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	poolqListCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	poolqShowCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	poolqReadCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of a table")
	poolqReadCmd.Flags().Uint64("from", 1, "Lower seq bound (inclusive)")
	poolqReadCmd.Flags().Uint64("to", 0, "Upper seq bound (inclusive); 0 = head")
	poolqBlockCmd.Flags().String("reason", "", "Reason recorded on the audit row")
	poolqCmd.AddCommand(poolqStatusCmd, poolqListCmd, poolqShowCmd, poolqReadCmd, poolqBlockCmd, poolqUnblockCmd)
}
