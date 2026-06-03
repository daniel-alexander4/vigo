// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

import (
	"strings"
	"testing"
)

// findFinding returns the first Finding with the given RuleID, or nil
// if none. Tests use this so they can assert on a specific rule's
// behavior without being thrown off by other rules' findings against
// the same fixture.
func findFinding(r *Result, ruleID string) *Finding {
	for i := range r.Findings {
		if r.Findings[i].RuleID == ruleID {
			return &r.Findings[i]
		}
	}
	return nil
}

// --- mode-octal ---

func TestLint_ModeOctal_BareIntegerAutoFixed(t *testing.T) {
	input := `name: m
resources:
  - name: nginx-conf
    type: file
    state: present
    target_path: /etc/nginx/nginx.conf
    content: "..."
    owner: root
    group: root
    mode: 644
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	f := findFinding(r, "mode-octal")
	if f == nil {
		t.Fatalf("expected mode-octal finding, got %v", r.Findings)
	}
	if f.Severity != Warning || !f.FixApplied {
		t.Errorf("severity=%q fix=%v, want warning + fix", f.Severity, f.FixApplied)
	}
	if !strings.Contains(r.Output, `mode: '0644'`) && !strings.Contains(r.Output, `mode: "0644"`) {
		t.Errorf("output should contain quoted '0644'; got:\n%s", r.Output)
	}
}

func TestLint_ModeOctal_NonOctalDigitsRejected(t *testing.T) {
	input := `name: m
resources:
  - name: bad
    type: file
    state: present
    target_path: /tmp/x
    content: "x"
    owner: root
    group: root
    mode: 888
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for mode with non-octal digits")
	}
	if !containsError(r, "non-octal digits") {
		t.Errorf("expected non-octal-digits error; got %v", r.Errors)
	}
}

func TestLint_ModeOctal_StringPassThrough(t *testing.T) {
	input := `name: m
resources:
  - name: ok
    type: file
    state: present
    target_path: /etc/x
    content: "x"
    owner: root
    group: root
    mode: '0644'
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if findFinding(r, "mode-octal") != nil {
		t.Error("mode-octal must not fire on a properly-quoted string")
	}
}

// `mode: 0644` is the most common operator typo: yaml.v3 honors YAML 1.1's
// leading-zero-octal carryover and parses it as int 420. The rule must
// recover the original octal intent from the raw YAML text and emit
// "0644" — NOT "0420" (which would silently ship --w-r-----).
func TestLint_ModeOctal_LeadingZeroOctal(t *testing.T) {
	input := `name: m
resources:
  - name: nginx-conf
    type: file
    state: present
    target_path: /etc/nginx/nginx.conf
    content: "..."
    owner: root
    group: root
    mode: 0644
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	f := findFinding(r, "mode-octal")
	if f == nil {
		t.Fatalf("expected mode-octal finding, got %v", r.Findings)
	}
	if f.Severity != Warning || !f.FixApplied {
		t.Errorf("severity=%q fix=%v, want warning + fix", f.Severity, f.FixApplied)
	}
	if !strings.Contains(r.Output, `mode: '0644'`) && !strings.Contains(r.Output, `mode: "0644"`) {
		t.Errorf("output should canonicalize to quoted '0644'; got:\n%s", r.Output)
	}
	if strings.Contains(r.Output, `mode: '0420'`) || strings.Contains(r.Output, `mode: "0420"`) {
		t.Errorf("output must NOT contain '0420' (the buggy 0.33.6→0.33.10 behavior); got:\n%s", r.Output)
	}
}

// YAML 1.2 explicit octal `0o644` should also canonicalize to "0644".
func TestLint_ModeOctal_ExplicitOctalPrefix(t *testing.T) {
	input := `name: m
resources:
  - name: cfg
    type: file
    state: present
    target_path: /etc/x
    content: "x"
    owner: root
    group: root
    mode: 0o644
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if !strings.Contains(r.Output, `mode: '0644'`) && !strings.Contains(r.Output, `mode: "0644"`) {
		t.Errorf("output should canonicalize to quoted '0644'; got:\n%s", r.Output)
	}
}

// 4-digit setuid mode written with a leading zero (`04755`) must round-trip
// to "4755" (setuid + rwxr-xr-x), not to whatever %o of (octal 04755 =
// decimal 2541) reformats to.
func TestLint_ModeOctal_LeadingZeroSetuid(t *testing.T) {
	input := `name: m
resources:
  - name: bin
    type: file
    state: present
    target_path: /usr/local/bin/x
    content: "x"
    owner: root
    group: root
    mode: 04755
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if !strings.Contains(r.Output, `mode: '4755'`) && !strings.Contains(r.Output, `mode: "4755"`) {
		t.Errorf("output should canonicalize to quoted '4755'; got:\n%s", r.Output)
	}
}

// --- container-image-source ---

func TestLint_ContainerImageSource_BothSet(t *testing.T) {
	input := `name: m
resources:
  - name: confused
    type: container
    container: confused
    image: "nginx:latest"
    artifact: "alexander4/nginx:latest"
    state: running
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for container with both image and artifact")
	}
	if !containsError(r, "both 'image' and 'artifact'") {
		t.Errorf("expected both-set error; got %v", r.Errors)
	}
}

func TestLint_ContainerImageSource_NeitherSet_Running(t *testing.T) {
	input := `name: m
resources:
  - name: empty
    type: container
    container: empty
    state: running
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for state=running with no image source")
	}
	if !containsError(r, "needs 'image' or 'artifact'") {
		t.Errorf("expected missing-source error; got %v", r.Errors)
	}
}

func TestLint_ContainerImageSource_NeitherSet_Absent(t *testing.T) {
	input := `name: m
resources:
  - name: gone
    type: container
    container: gone
    state: absent
`
	r := Lint([]byte(input), Options{})
	if finding := findFinding(r, "container-image-source"); finding != nil {
		t.Errorf("state=absent should not require image source; got %+v", finding)
	}
}

func TestLint_ContainerImageSource_ArtifactOnly(t *testing.T) {
	input := `name: m
resources:
  - name: redis
    type: container
    container: redis
    artifact: "alexander4/redis:7.2-alpine"
    state: running
`
	r := Lint([]byte(input), Options{})
	if finding := findFinding(r, "container-image-source"); finding != nil {
		t.Errorf("artifact alone is valid; got %+v", finding)
	}
}

// --- file-content-source-conflict ---

func TestLint_FileContentSourceConflict(t *testing.T) {
	input := `name: m
resources:
  - name: confused
    type: file
    state: present
    target_path: /etc/x
    content: "inline"
    source: "templates/x.tmpl"
    owner: root
    group: root
    mode: '0644'
`
	r := Lint([]byte(input), Options{})
	if r.OK() {
		t.Fatal("expected error for file with both content and source")
	}
	if !containsError(r, "both 'content' and 'source'") {
		t.Errorf("expected both-set error; got %v", r.Errors)
	}
}

// --- secret-path ---

func TestLint_SecretPath_Valid(t *testing.T) {
	input := `name: m
resources:
  - name: db
    type: package
    package: postgresql
    state: present
  - name: dsn-file
    type: file
    state: present
    target_path: /etc/dsn
    content: "secret:vigo/db/dsn"
    owner: root
    group: root
    mode: '0600'
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	if findFinding(r, "secret-path") != nil {
		t.Error("valid secret path must not trigger secret-path")
	}
}

func TestLint_SecretPath_InvalidShape(t *testing.T) {
	cases := []string{
		"secret:",            // empty
		"secret:Foo/Bar",     // uppercase
		"secret:foo bar/baz", // whitespace
		"secret:foo//bar",    // empty path component
	}
	for _, bad := range cases {
		input := `name: m
resources:
  - name: f
    type: file
    state: present
    target_path: /etc/x
    content: "` + bad + `"
    owner: root
    group: root
    mode: '0600'
`
		r := Lint([]byte(input), Options{})
		if r.OK() {
			t.Errorf("expected error for secret %q; got OK", bad)
			continue
		}
		if !containsError(r, "valid secret reference") {
			t.Errorf("expected secret-path error for %q; got %v", bad, r.Errors)
		}
	}
}

// --- package-state-latest ---

func TestLint_PackageStateLatest_Info(t *testing.T) {
	input := `name: m
resources:
  - name: vim
    type: package
    package: vim
    state: latest
`
	r := Lint([]byte(input), Options{})
	if !r.OK() {
		t.Fatalf("expected no errors, got: %v", r.Errors)
	}
	f := findFinding(r, "package-state-latest")
	if f == nil {
		t.Fatalf("expected package-state-latest finding, got %v", r.Findings)
	}
	if f.Severity != Info {
		t.Errorf("severity = %q, want info", f.Severity)
	}
}

// --- did-you-mean ---

func TestLint_DidYouMean_TypoTypeName(t *testing.T) {
	// `pacage` is one edit from `package`.
	input := `name: m
resources:
  - name: vim
    type: pacage
    package: vim
`
	r := Lint([]byte(input), Options{})
	// validate-adapter emits the Error; did-you-mean adds a Hint.
	if !containsError(r, "unknown resource type") {
		t.Errorf("expected unknown-type error; got %v", r.Errors)
	}
	hint := findFinding(r, "did-you-mean")
	if hint == nil {
		t.Fatalf("expected did-you-mean hint, got %v", r.Findings)
	}
	if hint.Severity != Hint {
		t.Errorf("severity = %q, want hint", hint.Severity)
	}
	if !strings.Contains(hint.Message, `"package"`) {
		t.Errorf("hint should suggest 'package'; got %q", hint.Message)
	}
}

func TestLint_DidYouMean_TypoAttribute(t *testing.T) {
	// `pakcage` is one transposition from `package`.
	input := `name: m
resources:
  - name: vim
    type: package
    state: present
    pakcage: vim
`
	r := Lint([]byte(input), Options{})
	hint := findFinding(r, "did-you-mean")
	if hint == nil {
		t.Fatalf("expected did-you-mean hint, got %v", r.Findings)
	}
	if !strings.Contains(hint.Message, `"package"`) {
		t.Errorf("hint should suggest 'package'; got %q", hint.Message)
	}
}

func TestLint_DidYouMean_FarTypoNoSuggestion(t *testing.T) {
	// `foobar_widget` is too far from any known type — no suggestion.
	input := `name: m
resources:
  - name: thing
    type: foobar_widget
`
	r := Lint([]byte(input), Options{})
	if findFinding(r, "did-you-mean") != nil {
		t.Error("did-you-mean must not fire on far-distance typos")
	}
}
