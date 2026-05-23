// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"fmt"
	"regexp"
	"strings"
)

// Usercrate-specific rules. Each fires only when opts.IsUsercrate is
// true, since usercrate files have semantics modules don't (Unix users
// with passwords, ssh keys, lockbox enrollment).

// --- password-must-be-hashed-or-secret ---

// passwordHashOrSecretRule rejects plaintext passwords in usercrates.
//
// Plaintext passwords ride in the clear through the gRPC bundle, get
// stored in run results, and end up readable by anyone with audit
// access — a violation of CLAUDE.md's "secrets never in plaintext"
// rule. The two acceptable forms:
//
//	password: "secret:vigo/dan/password"   — resolved at server load
//	password: "$6$..."                     — pre-computed crypt(3) hash
//
// The recognized hash prefixes cover MD5-crypt ($1$), bcrypt ($2a$ /
// $2y$ / $2b$), SHA-256-crypt ($5$), SHA-512-crypt ($6$), and the
// Argon2 family ($argon2i / $argon2d / $argon2id). The lock markers
// `*` and `!` are also accepted — they intentionally produce a
// password no one can authenticate against, which is fine.
type passwordHashOrSecretRule struct{}

func (passwordHashOrSecretRule) id() string { return "usercrate-password-hash-or-secret" }

var passwordHashPrefixes = []string{
	"$1$", "$2a$", "$2y$", "$2b$",
	"$5$", "$6$",
	"$argon2",
}

func (passwordHashOrSecretRule) apply(mod *module, opts Options) []Finding {
	if !opts.IsUsercrate {
		return nil
	}
	var out []Finding
	for _, res := range mod.Resources {
		if res.Type != "user" {
			continue
		}
		pw, ok := res.Attrs["password"].(string)
		if !ok || pw == "" {
			continue
		}
		if pw == "*" || pw == "!" {
			continue // lock markers
		}
		if strings.HasPrefix(pw, "secret:") {
			continue
		}
		if hasAnyPrefix(pw, passwordHashPrefixes) {
			continue
		}
		out = append(out, Finding{
			Severity: Error,
			Line:     res.Line,
			Column:   res.Column,
			RuleID:   "usercrate-password-hash-or-secret",
			Message: fmt.Sprintf(
				"resource %q: password is plaintext — must be a 'secret:' reference or a crypt(3)-format hash ($1$, $2[aby]$, $5$, $6$, $argon2*)",
				res.Name,
			),
		})
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func init() { register(passwordHashOrSecretRule{}) }

// --- absent-with-unused-attrs ---

// absentWithUnusedAttrsRule flags user resources where state: absent
// is set alongside attributes that only matter for present users.
// Usually means the operator forgot to clear the attrs when toggling
// state — they're harmless at runtime (the user executor ignores
// them when removing) but their presence is misleading and rots when
// the same user is later re-added with stale values.
type absentWithUnusedAttrsRule struct{}

func (absentWithUnusedAttrsRule) id() string { return "usercrate-absent-with-unused-attrs" }

// absentIgnoredAttrs are attributes the user executor doesn't act on
// when state: absent. Removing a user only needs the username; the
// other fields are residue.
var absentIgnoredAttrs = []string{
	"home", "shell", "password", "groups", "group",
	"authorized_keys", "uid", "gid", "comment",
	"sudo_passwd", "sudo_nopasswd", "system",
	"puddle", "gitback", "curator", "poolq",
}

func (absentWithUnusedAttrsRule) apply(mod *module, opts Options) []Finding {
	if !opts.IsUsercrate {
		return nil
	}
	var out []Finding
	for _, res := range mod.Resources {
		if res.Type != "user" {
			continue
		}
		state, _ := res.Attrs["state"].(string)
		if state != "absent" {
			continue
		}
		var stale []string
		for _, k := range absentIgnoredAttrs {
			if _, present := res.Attrs[k]; present {
				stale = append(stale, k)
			}
		}
		if len(stale) == 0 {
			continue
		}
		out = append(out, Finding{
			Severity: Hint,
			Line:     res.Line,
			Column:   res.Column,
			RuleID:   "usercrate-absent-with-unused-attrs",
			Message: fmt.Sprintf(
				"resource %q: state: absent but %d attribute(s) ignored on removal: %s — drop them to keep the file honest",
				res.Name, len(stale), strings.Join(stale, ", "),
			),
		})
	}
	return out
}

func init() { register(absentWithUnusedAttrsRule{}) }

// --- invalid-username ---

// invalidUsernameRule rejects user resources whose `username:` doesn't
// match the POSIX-portable Unix login pattern. Catches typos that the
// agent's user executor would silently fail on (case-mismatched
// names, embedded whitespace, trailing newlines from copy-paste).
//
// Pattern: `^[a-z_][a-z0-9_-]*\$?$`, max 32 chars. The optional
// trailing `$` is for AD-style machine accounts (`hostname$`).
type invalidUsernameRule struct{}

func (invalidUsernameRule) id() string { return "usercrate-invalid-username" }

var validUsernamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*\$?$`)

func (invalidUsernameRule) apply(mod *module, opts Options) []Finding {
	if !opts.IsUsercrate {
		return nil
	}
	var out []Finding
	for _, res := range mod.Resources {
		if res.Type != "user" {
			continue
		}
		uname, _ := res.Attrs["username"].(string)
		if uname == "" {
			continue // validate-adapter handles the missing-username error
		}
		if len(uname) > 32 {
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "usercrate-invalid-username",
				Message: fmt.Sprintf(
					"resource %q: username %q exceeds 32 characters",
					res.Name, uname,
				),
			})
			continue
		}
		if !validUsernamePattern.MatchString(uname) {
			out = append(out, Finding{
				Severity: Error,
				Line:     res.Line,
				Column:   res.Column,
				RuleID:   "usercrate-invalid-username",
				Message: fmt.Sprintf(
					"resource %q: username %q is not a valid Unix login (must match [a-z_][a-z0-9_-]*[$]? — lowercase, no spaces, no leading digit)",
					res.Name, uname,
				),
			})
		}
	}
	return out
}

func init() { register(invalidUsernameRule{}) }

// --- ssh-keys-malformed ---

// sshKeysMalformedRule flags entries in `authorized_keys` that don't
// start with a recognized OpenSSH public-key type prefix. Catches
// the common copy-paste failures: URL-encoded keys, line-broken
// keys, comment-only entries, accidentally-pasted private keys.
//
// Format-only check, not a content check — the public key itself
// isn't sensitive and we don't validate the base64 payload. We just
// confirm the entry has the shape `<type> <base64-blob> [comment]`.
type sshKeysMalformedRule struct{}

func (sshKeysMalformedRule) id() string { return "usercrate-ssh-keys-malformed" }

// recognizedSSHKeyTypes is the set of OpenSSH public-key type tokens
// in current use. Order doesn't matter — the rule does a HasPrefix
// scan over the entry's first whitespace-separated token.
var recognizedSSHKeyTypes = []string{
	"ssh-rsa",
	"ssh-ed25519",
	"ssh-dss", // legacy; allowed but server-side disabled in practice
	"ecdsa-sha2-nistp256",
	"ecdsa-sha2-nistp384",
	"ecdsa-sha2-nistp521",
	"sk-ssh-ed25519@openssh.com",
	"sk-ecdsa-sha2-nistp256@openssh.com",
}

func (sshKeysMalformedRule) apply(mod *module, opts Options) []Finding {
	if !opts.IsUsercrate {
		return nil
	}
	var out []Finding
	for _, res := range mod.Resources {
		if res.Type != "user" {
			continue
		}
		raw, ok := res.Attrs["authorized_keys"]
		if !ok {
			continue
		}
		// authorized_keys arrives as []any from yaml.v3 default decode.
		entries, ok := raw.([]any)
		if !ok {
			continue
		}
		for i, e := range entries {
			s, ok := e.(string)
			if !ok || strings.TrimSpace(s) == "" {
				out = append(out, Finding{
					Severity: Warning,
					Line:     res.Line,
					Column:   res.Column,
					RuleID:   "usercrate-ssh-keys-malformed",
					Message: fmt.Sprintf(
						"resource %q: authorized_keys[%d] is empty or not a string",
						res.Name, i,
					),
				})
				continue
			}
			if !looksLikeSSHKey(s) {
				out = append(out, Finding{
					Severity: Warning,
					Line:     res.Line,
					Column:   res.Column,
					RuleID:   "usercrate-ssh-keys-malformed",
					Message: fmt.Sprintf(
						"resource %q: authorized_keys[%d] doesn't start with a recognized SSH key type (ssh-rsa, ssh-ed25519, ecdsa-sha2-nistp{256,384,521}, sk-ssh-ed25519@openssh.com, sk-ecdsa-sha2-nistp256@openssh.com)",
						res.Name, i,
					),
				})
			}
		}
	}
	return out
}

func looksLikeSSHKey(s string) bool {
	s = strings.TrimSpace(s)
	for _, t := range recognizedSSHKeyTypes {
		if strings.HasPrefix(s, t+" ") {
			return true
		}
	}
	return false
}

func init() { register(sshKeysMalformedRule{}) }
