// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package poolqmesh builds the fleet-wide poolq ordered-log catalog by
// aggregating every envoy's `poolq` trait payload — the signed messages
// posted by local founders — bucketed by
// `topic_id = sha256(author_puddle_pubkey ‖ topic_name)` (ADR-029).
//
// The server publishes, never arbitrates: messages flow through this
// package verbatim. It does NOT verify Ed25519 signatures or delegation
// chains — the agent and `vigocli swarm poolq read` do that end-to-end (so
// a forged message never gets *materialized*, even if a misbehaving envoy
// reports it). That's the same stance curatormesh + gitbackmesh take, and
// it keeps this package free of a Go-side twin of
// `agent/src/swarm/poolq/message.rs`. The only crypto here is the SHA-256
// that derives `topic_id` (a plain hash, not a signature).
//
// What this package DOES do:
//
//   - Bucket messages by computed `topic_id`. v1 founder-only enforcement
//     is implicit in the bucketing: two different signers posting under
//     the same `topic_name` produce different `topic_id`s and end up in
//     different buckets, so a non-founder simply creates a parallel topic
//     it owns — never spoofs an existing one.
//   - Per-topic G-Set merge (immutable messages, keyed by `msg_id` —
//     duplicates collapse).
//   - Age-based retention prune: drop messages with `posted_at` older
//     than the operator's `swarm.poolq.retention`. The stale-on-ingest
//     refusal lives in the same predicate (an already-aged-out message
//     reported by an envoy with a faster clock is refused at merge time
//     rather than reintroduced) — what makes pruning flap-stable without
//     tombstones.
//   - Per-topic head-`seq`, message-count, first-seen timestamps for the
//     REST endpoints in Slice 4.
//
// Lives in its own package so both `server/api` (REST) and `ui` (web) can
// consume it without an import cycle through `server/fleet` — same as
// `curatormesh` / `gitbackmesh` / `lockboxmesh`.
package poolqmesh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/swarm"
)

// EnvoyInput is the minimal envoy descriptor the aggregator needs — (ID,
// Hostname, Revoked). Accepting these instead of importing `server/fleet`
// keeps this package at the bottom of the import graph. Matches the
// curatormesh shape.
type EnvoyInput struct {
	ID       string
	Hostname string
	Revoked  bool
}

// MessageBody mirrors agent/src/swarm/poolq/message.rs's serde output
// exactly so signed envelopes round-trip through this package verbatim and
// can be verified end-to-end on the consumer side.
type MessageBody struct {
	SchemaVersion      uint32 `json:"schema_version"`
	TopicName          string `json:"topic_name"`
	Seq                uint64 `json:"seq"`
	Body               string `json:"body"`
	PostedAt           int64  `json:"posted_at"`
	AuthorPuddlePubkey string `json:"author_puddle_pubkey"`
}

// Message is one signed poolq message. `Signature` flows through verbatim;
// see the package doc for why the server doesn't verify it.
type Message struct {
	Body      MessageBody `json:"body"`
	Signature string      `json:"signature"`
}

// Topic is the merged-log view of one topic: the founder anchor + the
// in-order messages + summary stats. Messages are sorted ascending by
// `seq`; gaps are possible mid-propagation but the contiguous prefix is
// "what every consumer can replay" (the G-Set's readable window).
type Topic struct {
	TopicIDHex          string    `json:"topic_id_hex"`
	TopicName           string    `json:"topic_name"`
	FounderPuddlePubkey string    `json:"founder_puddle_pubkey"`
	HeadSeq             uint64    `json:"head_seq"`
	Count               int       `json:"count"`
	FirstPostedAt       int64     `json:"first_posted_at"`
	LastPostedAt        int64     `json:"last_posted_at"`
	Blocked             bool      `json:"blocked"`
	Messages            []Message `json:"messages"`
}

// Snapshot is the fleet-wide aggregated poolq log: every known topic with
// its merged G-Set sorted by seq, plus the hostnames that contributed.
type Snapshot struct {
	GeneratedAt  time.Time `json:"generated_at"`
	Topics       []Topic   `json:"topics"` // sorted by topic_id_hex
	Participants []string  `json:"participants"`

	byID map[string]int // topic_id_hex → index into Topics
}

// TopicIDFor returns the hex `sha256(author_puddle_pubkey ‖ topic_name)`
// for the given signer + topic name — the same derivation as
// `topic_id_for` in the agent. Errors when the pubkey isn't 32 bytes of
// hex.
func TopicIDFor(authorPubkeyHex, topicName string) (string, error) {
	pk, err := hex.DecodeString(authorPubkeyHex)
	if err != nil {
		return "", err
	}
	if len(pk) != 32 {
		return "", errors.New("author_puddle_pubkey is not 32 bytes")
	}
	h := sha256.New()
	h.Write(pk)
	h.Write([]byte(topicName))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// LookupByID returns the topic with the given `topic_id_hex`, or nil.
// Production snapshots come from [Aggregator.Snapshot], which prepopulates
// `byID`; hand-built snapshots (tests, fixtures) may have an empty map, so
// this falls back to a linear scan — same result, slower.
func (s *Snapshot) LookupByID(topicIDHex string) *Topic {
	if s == nil {
		return nil
	}
	want := strings.ToLower(topicIDHex)
	if idx, ok := s.byID[want]; ok {
		return &s.Topics[idx]
	}
	for i := range s.Topics {
		if strings.EqualFold(s.Topics[i].TopicIDHex, want) {
			return &s.Topics[i]
		}
	}
	return nil
}

// RangeMessages returns the slice of messages in `[fromSeq, toSeq]`
// (inclusive on both ends). `toSeq == 0` is shorthand for "to the head".
// Messages are already seq-sorted in [Topic.Messages]; this is a binary-
// search slice, not a copy.
func RangeMessages(t *Topic, fromSeq, toSeq uint64) []Message {
	if t == nil || len(t.Messages) == 0 {
		return nil
	}
	start := sort.Search(len(t.Messages), func(i int) bool {
		return t.Messages[i].Body.Seq >= fromSeq
	})
	if start >= len(t.Messages) {
		return nil
	}
	end := len(t.Messages)
	if toSeq > 0 {
		end = sort.Search(len(t.Messages), func(i int) bool {
			return t.Messages[i].Body.Seq > toSeq
		})
	}
	if end <= start {
		return nil
	}
	return t.Messages[start:end]
}

// Aggregator builds the fleet snapshot from the trait pipeline. Carries a
// short-lived in-memory cache so a burst of `topics`/`head`/`range`/`log`
// calls in the same window only pays the per-envoy trait scan + JSON parse
// once. Same shape as curatormesh.Aggregator.
type Aggregator struct {
	queries *db.Queries

	mu       sync.Mutex
	cache    *Snapshot
	cacheExp time.Time
	ttl      time.Duration

	retention time.Duration // operator-configured retention from server.yaml; 0 ⇒ 7d default
	now       func() time.Time
}

// DefaultRetention is the compile-time per-topic message-age retention
// window applied when the operator hasn't configured `swarm.poolq.retention`
// (or set it to zero/negative). Matches the ADR-029 default.
const DefaultRetention = 7 * 24 * time.Hour

// NewAggregator constructs an aggregator over the given query layer with
// the default 30-second cache TTL (matching curatormesh / gitbackmesh).
// `retention` is the operator-configured `swarm.poolq.retention` — a
// zero or negative value falls back to [DefaultRetention].
func NewAggregator(queries *db.Queries, retention time.Duration) *Aggregator {
	if retention <= 0 {
		retention = DefaultRetention
	}
	return &Aggregator{
		queries:   queries,
		ttl:       swarm.MeshCacheTTL,
		retention: retention,
		now:       time.Now,
	}
}

// SetTTL overrides the cache TTL — same semantics as curatormesh.SetTTL:
// invalidates the current cached entry so the new TTL takes effect on the
// next Snapshot.
func (a *Aggregator) SetTTL(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ttl = d
	a.cache = nil
	a.cacheExp = time.Time{}
}

// SetRetention overrides the retention window. Used by the server's config
// reload path when an operator publishes a new `swarm.poolq.retention`.
// Invalidates the cache so the new prune cutoff takes effect immediately.
// A zero or negative value falls back to [DefaultRetention].
func (a *Aggregator) SetRetention(retention time.Duration) {
	if retention <= 0 {
		retention = DefaultRetention
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.retention = retention
	a.cache = nil
	a.cacheExp = time.Time{}
}

// InvalidateCache forces the next Snapshot to rebuild. Use after an
// authoritative state change (per-topic block/unblock — Slice 5) so the
// API response reflects it immediately instead of waiting up to one TTL.
func (a *Aggregator) InvalidateCache() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache = nil
	a.cacheExp = time.Time{}
}

// Snapshot returns the cached fleet snapshot or rebuilds it from the
// passed envoy list. The cache key is implicit (the aggregator is per-
// server; the envoy list comes from the same FleetIndex on every call),
// matching curatormesh's contract.
func (a *Aggregator) Snapshot(ctx context.Context, envoys []EnvoyInput) (*Snapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache != nil && a.now().Before(a.cacheExp) {
		return a.cache, nil
	}
	rows := make([]envelopeRow, 0, len(envoys))
	for _, e := range envoys {
		if e.Revoked {
			continue
		}
		env, ok := a.loadTrait(ctx, e.ID)
		if !ok {
			continue
		}
		rows = append(rows, envelopeRow{hostname: e.Hostname, envelope: env})
	}
	cutoff := a.now().UnixMilli() - a.retention.Milliseconds()
	snap := aggregate(rows, cutoff)
	snap.GeneratedAt = a.now().UTC()
	a.cache = snap
	a.cacheExp = a.now().Add(a.ttl)
	return snap, nil
}

type envelopeRow struct {
	hostname string
	envelope *poolqTraitEnvelope
}

// aggregate is the pure G-Set core: bucket every reported message by its
// derived `topic_id`, dedup on `msg_id` (immutable messages → identical
// bytes → identical id), apply the age cutoff (`posted_at < cutoffMillis`
// is both stale-on-ingest refusal AND age-prune), and emit a sorted log
// per topic.
//
// A row whose `author_puddle_pubkey` is unparseable is dropped (same as a
// missing trait). A row with a missing/malformed `msg_id`-equivalent
// (computed from the body) is dropped.
func aggregate(rows []envelopeRow, cutoffMillis int64) *Snapshot {
	type bucket struct {
		founderPubkey string
		topicName     string
		msgs          map[string]Message // msg_id_hex → Message (G-Set merge)
	}
	buckets := make(map[string]*bucket) // topic_id_hex → bucket
	participants := make(map[string]struct{})
	for _, row := range rows {
		participants[row.hostname] = struct{}{}
		for _, m := range row.envelope.Messages {
			if m.Body.PostedAt < cutoffMillis {
				// Stale-on-ingest refusal AND age-prune in one predicate —
				// what makes age-based retention flap-stable without
				// tombstones.
				continue
			}
			topicID, err := TopicIDFor(m.Body.AuthorPuddlePubkey, m.Body.TopicName)
			if err != nil {
				continue
			}
			msgID, err := msgIDFor(m.Body)
			if err != nil {
				continue
			}
			b, ok := buckets[topicID]
			if !ok {
				b = &bucket{
					founderPubkey: m.Body.AuthorPuddlePubkey,
					topicName:     m.Body.TopicName,
					msgs:          make(map[string]Message),
				}
				buckets[topicID] = b
			}
			// G-Set: union by msg_id. A second reporting envoy contributing
			// the same bytes is a no-op overwrite.
			b.msgs[msgID] = m
		}
	}

	topicIDs := make([]string, 0, len(buckets))
	for id := range buckets {
		topicIDs = append(topicIDs, id)
	}
	sort.Strings(topicIDs)

	topics := make([]Topic, 0, len(buckets))
	byID := make(map[string]int, len(buckets))
	for i, id := range topicIDs {
		b := buckets[id]
		msgs := make([]Message, 0, len(b.msgs))
		for _, m := range b.msgs {
			msgs = append(msgs, m)
		}
		sort.Slice(msgs, func(i, j int) bool { return msgs[i].Body.Seq < msgs[j].Body.Seq })

		var head uint64
		var first, last int64
		if len(msgs) > 0 {
			head = msgs[len(msgs)-1].Body.Seq
			first = msgs[0].Body.PostedAt
			last = msgs[len(msgs)-1].Body.PostedAt
		}
		topics = append(topics, Topic{
			TopicIDHex:          id,
			TopicName:           b.topicName,
			FounderPuddlePubkey: b.founderPubkey,
			HeadSeq:             head,
			Count:               len(msgs),
			FirstPostedAt:       first,
			LastPostedAt:        last,
			Messages:            msgs,
		})
		byID[id] = i
	}

	partList := make([]string, 0, len(participants))
	for h := range participants {
		partList = append(partList, h)
	}
	sort.Strings(partList)

	return &Snapshot{
		Topics:       topics,
		Participants: partList,
		byID:         byID,
	}
}

// MsgIDFor is the exported alias the UI template helper calls. The
// in-package callers (the aggregator's `aggregate` dedup) keep using the
// unexported name to avoid the import-cycle ceremony other packages
// would need for a Go-side recomputation of a single message's id.
func MsgIDFor(b MessageBody) (string, error) { return msgIDFor(b) }

// msgIDFor reproduces agent/src/swarm/poolq/message.rs's hand-rolled
// canonical-bytes encoding so the server can dedup against the same
// `msg_id` the agent computed at publish time.
//
// Layout (must stay byte-identical with the Rust side; see ADR-029 + the
// `canonical_bytes` function on the Rust side):
//
//  1. POOLQ_MSG_DOMAIN = `b"poolq-msg-v1\0"` (13 bytes)
//  2. schema_version (4 B BE u32)
//  3. author_puddle_pubkey (32 B raw, parsed from hex)
//  4. topic_name (u16 BE length + UTF-8 bytes)
//  5. seq (8 B BE u64)
//  6. posted_at (8 B BE i64)
//  7. body (u32 BE length + UTF-8 bytes)
func msgIDFor(b MessageBody) (string, error) {
	pk, err := hex.DecodeString(b.AuthorPuddlePubkey)
	if err != nil {
		return "", err
	}
	if len(pk) != 32 {
		return "", errors.New("author_puddle_pubkey is not 32 bytes")
	}
	topic := []byte(b.TopicName)
	body := []byte(b.Body)
	if len(topic) > 0xFFFF {
		return "", errors.New("topic_name too long for canonical encoding")
	}
	h := sha256.New()
	h.Write(poolqMsgDomain)
	h.Write(beU32(b.SchemaVersion))
	h.Write(pk)
	h.Write(beU16(uint16(len(topic))))
	h.Write(topic)
	h.Write(beU64(b.Seq))
	h.Write(beI64(b.PostedAt))
	h.Write(beU32(uint32(len(body))))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil)), nil
}

var poolqMsgDomain = []byte("poolq-msg-v1\x00")

func beU16(v uint16) []byte { return []byte{byte(v >> 8), byte(v)} }
func beU32(v uint32) []byte {
	return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}
func beU64(v uint64) []byte {
	return []byte{
		byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32),
		byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v),
	}
}
func beI64(v int64) []byte { return beU64(uint64(v)) }

// loadTrait pulls the latest traits row for an envoy and extracts the
// `poolq` namespace. Missing rows, malformed JSON, and a missing `poolq`
// namespace all collapse to `(nil, false)` — treated the same as "no
// participation reported".
func (a *Aggregator) loadTrait(ctx context.Context, envoyID string) (*poolqTraitEnvelope, bool) {
	tr, err := a.queries.GetLatestEnvoyTraits(ctx, envoyID)
	if err != nil {
		return nil, false
	}
	var env struct {
		Poolq *poolqTraitEnvelope `json:"poolq"`
	}
	if err := json.Unmarshal([]byte(tr.Traits), &env); err != nil {
		return nil, false
	}
	if env.Poolq == nil {
		return nil, false
	}
	return env.Poolq, true
}

// poolqTraitEnvelope is the top-level shape agent/src/traits/poolq.rs emits.
type poolqTraitEnvelope struct {
	UpdatedAt    int64     `json:"updated_at"`
	MessageCount int       `json:"message_count"`
	Messages     []Message `json:"messages"`
}
