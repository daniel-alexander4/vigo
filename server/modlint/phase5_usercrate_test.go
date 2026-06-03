// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"strings"
	"testing"
)

// --- usercrate-password-hash-or-secret ---

func TestLint_UsercratePasswordHash_AcceptsValidForms(t *testing.T) {
	cases := []string{
		`secret:vigo/dan/password`,
		`$1$abcdef$xyz`,                    // MD5 crypt
		`$2y$10$abcdefghij1234567890abcde`, // bcrypt 2y
		`$2a$10$abcdefghij1234567890abcde`, // bcrypt 2a
		`$2b$10$abcdefghij1234567890abcde`, // bcrypt 2b
		`$5$abc$xyz`,                       // SHA-256-crypt
		`$6$abc$xyz`,                       // SHA-512-crypt
		`$argon2id$v=19$m=65536,t=3,p=4$abc$xyz`,
		`*`, // lock marker
		`!`, // lock marker
	}
	for _, pw := range cases {
		input := `resources:
  - name: dan
    type: user
    state: present
    username: dan
    password: ` + quoteForYAML(pw) + `
`
		r := Lint([]byte(input), Options{IsUsercrate: true})
		if findFinding(r, "usercrate-password-hash-or-secret") != nil {
			t.Errorf("password %q: should be accepted; got %v", pw, r.Errors)
		}
	}
}

func TestLint_UsercratePasswordHash_RejectsPlaintext(t *testing.T) {
	cases := []string{
		`hunter2`,
		`correct horse battery staple`,
		`password123`,
		`a-real-password`,
	}
	for _, pw := range cases {
		input := `resources:
  - name: dan
    type: user
    state: present
    username: dan
    password: ` + quoteForYAML(pw) + `
`
		r := Lint([]byte(input), Options{IsUsercrate: true})
		if findFinding(r, "usercrate-password-hash-or-secret") == nil {
			t.Errorf("password %q: should be rejected as plaintext", pw)
		}
	}
}

func TestLint_UsercratePasswordHash_OnlyFiresWhenIsUsercrate(t *testing.T) {
	// Same plaintext password in a non-usercrate context: rule should
	// not fire (in-fleet modules can do whatever the operator wants).
	input := `name: m
resources:
  - name: dan
    type: user
    state: present
    username: dan
    password: "hunter2"
`
	r := Lint([]byte(input), Options{IsUsercrate: false})
	if findFinding(r, "usercrate-password-hash-or-secret") != nil {
		t.Error("rule must only fire on usercrates")
	}
}

// --- usercrate-absent-with-unused-attrs ---

func TestLint_UsercrateAbsentWithUnusedAttrs(t *testing.T) {
	input := `resources:
  - name: dan
    type: user
    username: dan
    state: absent
    home: /home/dan
    shell: /bin/bash
    groups: [docker, sudo]
`
	r := Lint([]byte(input), Options{IsUsercrate: true})
	f := findFinding(r, "usercrate-absent-with-unused-attrs")
	if f == nil {
		t.Fatalf("expected absent-with-unused-attrs hint, got %v", r.Findings)
	}
	if f.Severity != Hint {
		t.Errorf("severity=%q, want hint", f.Severity)
	}
	for _, attr := range []string{"home", "shell", "groups"} {
		if !strings.Contains(f.Message, attr) {
			t.Errorf("message should list %q; got %q", attr, f.Message)
		}
	}
}

func TestLint_UsercrateAbsentMinimal_OK(t *testing.T) {
	input := `resources:
  - name: dan
    type: user
    username: dan
    state: absent
`
	r := Lint([]byte(input), Options{IsUsercrate: true})
	if findFinding(r, "usercrate-absent-with-unused-attrs") != nil {
		t.Error("minimal absent user; rule must not fire")
	}
}

// --- usercrate-invalid-username ---

func TestLint_UsercrateInvalidUsername(t *testing.T) {
	cases := []string{
		"Dan",                   // uppercase
		"dan!",                  // punctuation
		"dan smith",             // whitespace
		"1dan",                  // leading digit
		strings.Repeat("a", 33), // > 32 chars
		"-dan",                  // leading hyphen
	}
	for _, uname := range cases {
		input := `resources:
  - name: u
    type: user
    state: present
    username: ` + quoteForYAML(uname) + `
`
		r := Lint([]byte(input), Options{IsUsercrate: true})
		if findFinding(r, "usercrate-invalid-username") == nil {
			t.Errorf("username %q: expected invalid-username error", uname)
		}
	}
}

func TestLint_UsercrateValidUsername_OK(t *testing.T) {
	cases := []string{"dan", "dan_smith", "dan-smith", "_special", "machine$"}
	for _, uname := range cases {
		input := `resources:
  - name: u
    type: user
    state: present
    username: ` + quoteForYAML(uname) + `
`
		r := Lint([]byte(input), Options{IsUsercrate: true})
		if findFinding(r, "usercrate-invalid-username") != nil {
			t.Errorf("username %q should be accepted", uname)
		}
	}
}

// --- usercrate-ssh-keys-malformed ---

func TestLint_UsercrateSSHKeysValid(t *testing.T) {
	input := `resources:
  - name: dan
    type: user
    state: present
    username: dan
    authorized_keys:
      - "ssh-rsa AAAAB3NzaC1yc2E... dan@laptop"
      - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5... dan@workstation"
      - "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTY... dan@phone"
`
	r := Lint([]byte(input), Options{IsUsercrate: true})
	if findFinding(r, "usercrate-ssh-keys-malformed") != nil {
		t.Errorf("valid keys; rule must not fire — got %v", r.Findings)
	}
}

func TestLint_UsercrateSSHKeysMalformed(t *testing.T) {
	input := `resources:
  - name: dan
    type: user
    state: present
    username: dan
    authorized_keys:
      - "ssh-rsa AAAAB... dan@valid"
      - "definitelynotsshkey blob"
      - ""
      - "ssh-rsa%20URL%20encoded%20mess"
`
	r := Lint([]byte(input), Options{IsUsercrate: true})
	// 3 invalid entries → 3 findings (entries 1, 2, 3 are bad).
	count := 0
	for _, f := range r.Findings {
		if f.RuleID == "usercrate-ssh-keys-malformed" {
			count++
		}
	}
	if count != 3 {
		t.Errorf("expected 3 ssh-keys-malformed findings, got %d (%v)", count, r.Findings)
	}
}

// --- helpers ---

// quoteForYAML wraps a string in YAML double quotes, escaping
// backslashes and double-quotes so the resulting fixture parses
// regardless of the embedded characters.
func quoteForYAML(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
