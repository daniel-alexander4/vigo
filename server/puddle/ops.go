// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package puddle

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OpsDefaultDir is the canonical filesystem location for the ops-puddle's
// on-disk state on this vigosrv. Per ADR-025, each vigosrv that holds
// ops-puddle membership has its own wrapped copy of the ops-puddle's
// private key under here — wrapped under THIS vigosrv's master key. The
// ops-puddle pubkey + friendly name + member list are shared across all
// ops-puddle members; only the wrap is per-host.
const OpsDefaultDir = "/srv/vigo/ops-puddle"

// OpsHKDFPurpose is the HKDF info string for the ops-puddle wrap-key
// derivation. Distinct from HKDFPurpose so a master-key compromise that
// somehow only reveals one derivation (theoretical) can't cross over.
const OpsHKDFPurpose = "vigo-ops-puddle-wrap-v1"

// FriendlyNameFile holds the operator-chosen ops-puddle friendly name,
// one UTF-8 line, no trailing whitespace.
const FriendlyNameFile = "friendly-name"

// MembersFile holds the signed-pubkey set of vigosrvs currently
// holding ops-puddle membership. JSON array of OpsMember records.
const MembersFile = "members.json"

// MaxFriendlyNameLen mirrors the agent's puddle-name limit
// (agent/src/swarm/puddle/name.rs::MAX_NAME_LEN) so `<name>/<handle>`
// gitback URLs stay bounded — the ops-puddle name participates in the
// same URL space.
const MaxFriendlyNameLen = 64

// OpsMember is one member of the ops-puddle: a vigosrv (identified by
// its service-account-puddle pubkey) authorized to sign + push as the
// ops-puddle. ADR-025 Slice 2; Slice 2b's pair flow appends new
// records.
type OpsMember struct {
	// Pubkey is the member vigosrv's service-account puddle pubkey,
	// "ed25519:<hex>" form (see EncodePubkey).
	Pubkey string `json:"pubkey"`
	// JoinedAt is the UTC timestamp when this member was added.
	JoinedAt time.Time `json:"joined_at"`
}

// OpsPuddle is an unlocked ops-puddle identity plus its shared metadata
// (friendly name, member set). The signing key is the SAME 32-byte
// Ed25519 seed every member holds — what differs between members is the
// wrap. As with [Identity], wipe by dropping the reference.
type OpsPuddle struct {
	SigningKey    ed25519.PrivateKey
	PublicKey     ed25519.PublicKey
	PublicKeyText string
	FriendlyName  string
	Members       []OpsMember
}

// EnsureOpsPuddle founds the ops-puddle if one is not already present
// at dir, or loads the existing one. Founding requires a friendly name
// (operator-chosen, see ValidateFriendlyName) and the founder's
// service-account-puddle pubkey (which becomes the first member).
//
// A non-empty dir with no identity file is treated as fresh — the
// directory is populated. A populated dir is loaded as-is; the
// friendlyName argument MUST match what's persisted (refuses silent
// renaming, since the friendly name participates in gitback URLs and
// renaming would break in-flight references).
//
// founderPubkey is required on first founding (forms members[0]) and
// ignored on load.
func EnsureOpsPuddle(deriver KeyDeriver, dir, friendlyName, founderPubkey string) (*OpsPuddle, error) {
	if deriver == nil {
		return nil, errors.New("ops-puddle: nil key deriver")
	}
	if dir == "" {
		return nil, errors.New("ops-puddle: empty dir")
	}
	if err := ValidateFriendlyName(friendlyName); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ops-puddle: creating %s: %w", dir, err)
	}

	wrappedPath := WrappedPath(dir)
	if _, err := os.Stat(wrappedPath); errors.Is(err, os.ErrNotExist) {
		if founderPubkey == "" {
			return nil, errors.New("ops-puddle: founderPubkey required for first founding")
		}
		return initOpsPuddle(deriver, dir, friendlyName, founderPubkey)
	} else if err != nil {
		return nil, fmt.Errorf("ops-puddle: stat %s: %w", wrappedPath, err)
	}

	existing, err := LoadOpsPuddle(deriver, dir)
	if err != nil {
		return nil, err
	}
	if existing.FriendlyName != friendlyName {
		return nil, fmt.Errorf(
			"ops-puddle: friendly name mismatch — persisted %q, requested %q "+
				"(renaming an existing ops-puddle would break gitback URLs; either use the persisted name or migrate via re-found)",
			existing.FriendlyName, friendlyName,
		)
	}
	return existing, nil
}

// LoadOpsPuddle unwraps an existing ops-puddle. Returns an error if
// any required file is missing.
func LoadOpsPuddle(deriver KeyDeriver, dir string) (*OpsPuddle, error) {
	if deriver == nil {
		return nil, errors.New("ops-puddle: nil key deriver")
	}
	envelope, err := os.ReadFile(WrappedPath(dir))
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: reading %s: %w", WrappedPath(dir), err)
	}
	salt, err := os.ReadFile(SaltPath(dir))
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: reading %s: %w", SaltPath(dir), err)
	}
	if len(salt) != SaltLen {
		return nil, fmt.Errorf("ops-puddle: salt at %s is %d bytes, expected %d", SaltPath(dir), len(salt), SaltLen)
	}
	wrapKey, err := deriver.DeriveKey(OpsHKDFPurpose, salt, wrapKeyLen)
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: derive wrap key: %w", err)
	}
	seed, err := unwrap(envelope, wrapKey)
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: %w", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("ops-puddle: derived public key has wrong type")
	}

	friendlyName, err := readFriendlyName(dir)
	if err != nil {
		return nil, err
	}
	members, err := readMembers(dir)
	if err != nil {
		return nil, err
	}

	return &OpsPuddle{
		SigningKey:    priv,
		PublicKey:     pub,
		PublicKeyText: EncodePubkey(pub),
		FriendlyName:  friendlyName,
		Members:       members,
	}, nil
}

// ValidateFriendlyName mirrors the agent's puddle-name rules
// (agent/src/swarm/puddle/name.rs::validate_name): 1-64 chars, ASCII
// alphanumeric + `-` `_` `.`, no leading dot, and never the 64-hex-char
// shape that would collide with a gitback project_id literal.
func ValidateFriendlyName(name string) error {
	if name == "" || len(name) > MaxFriendlyNameLen {
		return fmt.Errorf("ops-puddle name must be 1-%d characters", MaxFriendlyNameLen)
	}
	if strings.HasPrefix(name, ".") {
		return errors.New("ops-puddle name must not start with '.'")
	}
	if len(name) == 64 && isHex(name) {
		return errors.New("ops-puddle name must not look like a project_id (64 lowercase hex characters)")
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
		if !ok {
			return fmt.Errorf(
				"ops-puddle name %q contains invalid character %q; "+
					"use ASCII alphanumeric + '-' '_' '.'", name, r,
			)
		}
	}
	return nil
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func initOpsPuddle(deriver KeyDeriver, dir, friendlyName, founderPubkey string) (*OpsPuddle, error) {
	if !strings.HasPrefix(founderPubkey, "ed25519:") {
		return nil, fmt.Errorf("ops-puddle: founderPubkey %q must be in 'ed25519:<hex>' form", founderPubkey)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: generate keypair: %w", err)
	}
	seed := priv.Seed()

	salt, err := freshSalt()
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: %w", err)
	}
	wrapKey, err := deriver.DeriveKey(OpsHKDFPurpose, salt, wrapKeyLen)
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: derive wrap key: %w", err)
	}
	envelope, err := wrap(seed, wrapKey)
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: wrap: %w", err)
	}

	members := []OpsMember{{Pubkey: founderPubkey, JoinedAt: time.Now().UTC()}}
	pubText := EncodePubkey(pub)

	if err := writePrivateFile(SaltPath(dir), salt); err != nil {
		return nil, fmt.Errorf("ops-puddle: writing salt: %w", err)
	}
	if err := writePrivateFile(WrappedPath(dir), envelope); err != nil {
		return nil, fmt.Errorf("ops-puddle: writing wrapped identity: %w", err)
	}
	if err := writePublicFile(PubkeyPath(dir), []byte(pubText+"\n")); err != nil {
		return nil, fmt.Errorf("ops-puddle: writing pubkey: %w", err)
	}
	if err := writePrivateFile(friendlyNamePath(dir), []byte(friendlyName+"\n")); err != nil {
		return nil, fmt.Errorf("ops-puddle: writing friendly name: %w", err)
	}
	if err := writeMembers(dir, members); err != nil {
		return nil, err
	}

	return &OpsPuddle{
		SigningKey:    priv,
		PublicKey:     pub,
		PublicKeyText: pubText,
		FriendlyName:  friendlyName,
		Members:       members,
	}, nil
}

func friendlyNamePath(dir string) string { return filepath.Join(dir, FriendlyNameFile) }
func membersPath(dir string) string      { return filepath.Join(dir, MembersFile) }

func readFriendlyName(dir string) (string, error) {
	b, err := os.ReadFile(friendlyNamePath(dir))
	if err != nil {
		return "", fmt.Errorf("ops-puddle: reading friendly name: %w", err)
	}
	name := strings.TrimRight(string(b), "\n")
	if err := ValidateFriendlyName(name); err != nil {
		return "", fmt.Errorf("ops-puddle: persisted friendly name invalid: %w", err)
	}
	return name, nil
}

func readMembers(dir string) ([]OpsMember, error) {
	b, err := os.ReadFile(membersPath(dir))
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: reading members: %w", err)
	}
	var ms []OpsMember
	if err := json.Unmarshal(b, &ms); err != nil {
		return nil, fmt.Errorf("ops-puddle: parsing members.json: %w", err)
	}
	return ms, nil
}

func writeMembers(dir string, ms []OpsMember) error {
	b, err := json.MarshalIndent(ms, "", "  ")
	if err != nil {
		return fmt.Errorf("ops-puddle: marshaling members: %w", err)
	}
	b = append(b, '\n')
	return writePrivateFile(membersPath(dir), b)
}

// ---------- Slice 2b: pair flow ----------

// PendingPairFile holds the in-flight pair record on the existing-
// member vigosrv between `vigocli spanner ops-puddle pair` (which
// writes it) and the joining vigocli's pair-claim REST call (which
// consumes it). Single pending pair at a time — running `pair`
// again overwrites.
const PendingPairFile = "pending-pair"

// PairCodeBytes is the random-byte length of a pair code before
// base64-url-no-pad encoding. 12 bytes → 16-char code (~96 bits of
// entropy). The code is the bearer secret that gates the pair-claim
// call; mTLS protects the wire and the 5-min TTL bounds the window.
const PairCodeBytes = 12

// PairCodeTTL is how long a generated pair code is honored before
// the existing-member refuses to redeem it.
const PairCodeTTL = 5 * time.Minute

// PendingPair is the on-disk record of an outstanding pair offer.
// Stored under 0600 in the ops-puddle dir; consumed (and marked
// used) by the pair-claim handler.
type PendingPair struct {
	// CodeHash is SHA-256(code), hex-encoded. The plaintext code
	// never lands on disk so a stolen ops-puddle dir snapshot can't
	// be replayed against an existing-member.
	CodeHash string `json:"code_hash"`
	// ExpiresAt is the absolute deadline (UTC RFC 3339) past which
	// the handler refuses the code regardless of whether it has
	// been used.
	ExpiresAt time.Time `json:"expires_at"`
	// Used flips to true after a successful claim; second-use
	// attempts are refused.
	Used bool `json:"used"`
}

// GeneratePairCode mints a random PairCodeBytes-length code and
// returns its base64-url-no-pad encoding. The plaintext is what the
// operator pastes; only its SHA-256 is persisted.
func GeneratePairCode() (string, error) {
	buf := make([]byte, PairCodeBytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("ops-puddle: pair code: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashPairCode returns the canonical SHA-256-hex of a pair code,
// matching the form persisted in PendingPair.CodeHash.
func HashPairCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// WritePendingPair persists a fresh pending-pair record at
// <dir>/pending-pair. Overwrites any prior record. Called by
// `vigocli spanner ops-puddle pair` on the existing-member.
func WritePendingPair(dir string, p *PendingPair) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("ops-puddle: marshaling pending pair: %w", err)
	}
	b = append(b, '\n')
	return writePrivateFile(filepath.Join(dir, PendingPairFile), b)
}

// LoadPendingPair reads the current pending-pair record. Returns
// (nil, nil) when no pending pair is on disk — callers distinguish
// "no offer" from a load error.
func LoadPendingPair(dir string) (*PendingPair, error) {
	b, err := os.ReadFile(filepath.Join(dir, PendingPairFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: reading pending pair: %w", err)
	}
	var p PendingPair
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("ops-puddle: parsing pending pair: %w", err)
	}
	return &p, nil
}

// MarkPendingPairUsed flips the used bit on the current pending pair
// and persists. Idempotent on already-used records.
func MarkPendingPairUsed(dir string) error {
	p, err := LoadPendingPair(dir)
	if err != nil {
		return err
	}
	if p == nil {
		return errors.New("ops-puddle: no pending pair to mark used")
	}
	p.Used = true
	return WritePendingPair(dir, p)
}

// AppendMember adds joiningPubkey to the ops-puddle's member set on
// this dir and persists the updated members.json. Returns the new
// list. Refuses duplicates (caller already-a-member is a no-op
// returning the existing list) and rejects malformed pubkey
// strings. Called by the pair-claim handler after a successful
// claim — both the existing-member and the joining vigosrv end up
// with the same members list.
func AppendMember(dir, joiningPubkey string) ([]OpsMember, error) {
	if !strings.HasPrefix(joiningPubkey, "ed25519:") {
		return nil, fmt.Errorf("ops-puddle: pubkey %q must be in 'ed25519:<hex>' form", joiningPubkey)
	}
	members, err := readMembers(dir)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if m.Pubkey == joiningPubkey {
			return members, nil // idempotent
		}
	}
	members = append(members, OpsMember{
		Pubkey:   joiningPubkey,
		JoinedAt: time.Now().UTC(),
	})
	if err := writeMembers(dir, members); err != nil {
		return nil, err
	}
	return members, nil
}

// IngestOpsPuddle persists an ops-puddle on the JOINING vigosrv
// from material received over the pair-claim wire: the raw 32-byte
// seed (which becomes the same Ed25519 signing key every member
// holds), the operator-chosen friendly name, and the members list.
//
// The wrap is host-local: this function derives a FRESH per-host
// salt and uses the local secrets master key (via deriver) to
// produce a wrap key — so the existing-member's wrap is never
// reused, and a master-key rotation on either side doesn't
// invalidate the other's copy. After this call, both vigosrvs hold
// the same private key under different wraps.
//
// Refuses to overwrite an existing ops-puddle at dir. The operator
// must delete the dir first if re-pairing is the intent (rare).
func IngestOpsPuddle(deriver KeyDeriver, dir, friendlyName string, seed []byte, members []OpsMember) (*OpsPuddle, error) {
	if deriver == nil {
		return nil, errors.New("ops-puddle: nil key deriver")
	}
	if dir == "" {
		return nil, errors.New("ops-puddle: empty dir")
	}
	if len(seed) != seedLen {
		return nil, fmt.Errorf("ops-puddle: seed must be %d bytes, got %d", seedLen, len(seed))
	}
	if err := ValidateFriendlyName(friendlyName); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ops-puddle: creating %s: %w", dir, err)
	}
	if _, err := os.Stat(WrappedPath(dir)); err == nil {
		return nil, fmt.Errorf(
			"ops-puddle: refusing to overwrite existing ops-puddle at %s — "+
				"delete the directory first if re-pairing is intentional",
			dir,
		)
	}

	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("ops-puddle: derived public key has wrong type")
	}

	salt, err := freshSalt()
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: %w", err)
	}
	wrapKey, err := deriver.DeriveKey(OpsHKDFPurpose, salt, wrapKeyLen)
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: derive wrap key: %w", err)
	}
	envelope, err := wrap(seed, wrapKey)
	if err != nil {
		return nil, fmt.Errorf("ops-puddle: wrap: %w", err)
	}

	pubText := EncodePubkey(pub)
	if err := writePrivateFile(SaltPath(dir), salt); err != nil {
		return nil, fmt.Errorf("ops-puddle: writing salt: %w", err)
	}
	if err := writePrivateFile(WrappedPath(dir), envelope); err != nil {
		return nil, fmt.Errorf("ops-puddle: writing wrapped identity: %w", err)
	}
	if err := writePublicFile(PubkeyPath(dir), []byte(pubText+"\n")); err != nil {
		return nil, fmt.Errorf("ops-puddle: writing pubkey: %w", err)
	}
	if err := writePrivateFile(friendlyNamePath(dir), []byte(friendlyName+"\n")); err != nil {
		return nil, fmt.Errorf("ops-puddle: writing friendly name: %w", err)
	}
	if err := writeMembers(dir, members); err != nil {
		return nil, err
	}

	return &OpsPuddle{
		SigningKey:    priv,
		PublicKey:     pub,
		PublicKeyText: pubText,
		FriendlyName:  friendlyName,
		Members:       members,
	}, nil
}
