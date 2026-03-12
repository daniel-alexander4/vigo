// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package modules

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyBaseIndent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		indent  int
		want    string
	}{
		{
			name:    "zero indent is no-op",
			content: "line1\nline2\n",
			indent:  0,
			want:    "line1\nline2\n",
		},
		{
			name:    "indent 2 spaces",
			content: "line1\nline2\n",
			indent:  2,
			want:    "  line1\n  line2\n",
		},
		{
			name:    "preserves relative indentation",
			content: "top\n  nested\ntop again\n",
			indent:  4,
			want:    "    top\n      nested\n    top again\n",
		},
		{
			name:    "empty lines stay empty",
			content: "line1\n\nline3\n",
			indent:  2,
			want:    "  line1\n\n  line3\n",
		},
		{
			name:    "trailing newline line stays empty",
			content: "a\nb\n",
			indent:  1,
			want:    " a\n b\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyBaseIndent(tt.content, tt.indent)
			if got != tt.want {
				t.Errorf("applyBaseIndent(%q, %d) =\n%q\nwant:\n%q", tt.content, tt.indent, got, tt.want)
			}
		})
	}
}

func TestFileExecutor_BaseIndent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.conf")

	f := &FileExecutor{}
	result := f.Execute(context.Background(), map[string]string{
		"targetpath": target,
		"content":    "key = val\n  sub = nested\n",
		"baseindent": "2",
	}, false)

	if result.Error != nil {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if result.Action != ActionCreated {
		t.Errorf("action = %q, want %q", result.Action, ActionCreated)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	want := "  key = val\n    sub = nested\n"
	if string(data) != want {
		t.Errorf("file content =\n%q\nwant:\n%q", string(data), want)
	}
}

func TestFileExecutor_BaseIndentInvalid(t *testing.T) {
	f := &FileExecutor{}
	result := f.Execute(context.Background(), map[string]string{
		"targetpath": "/tmp/test-baseindent-invalid",
		"content":    "hello",
		"baseindent": "abc",
	}, false)

	if result.Error == nil {
		t.Fatal("expected error for invalid baseindent, got nil")
	}
}

func TestFileExecutor_BaseIndentIdempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "idem.conf")

	f := &FileExecutor{}
	params := map[string]string{
		"targetpath": target,
		"content":    "line1\nline2\n",
		"baseindent": "3",
	}

	// First run: creates
	r1 := f.Execute(context.Background(), params, false)
	if r1.Error != nil {
		t.Fatalf("run 1 error: %v", r1.Error)
	}
	if r1.Action != ActionCreated {
		t.Errorf("run 1 action = %q, want created", r1.Action)
	}

	// Second run: no change
	r2 := f.Execute(context.Background(), params, false)
	if r2.Error != nil {
		t.Fatalf("run 2 error: %v", r2.Error)
	}
	if r2.Action != ActionNone {
		t.Errorf("run 2 action = %q, want none", r2.Action)
	}
	if r2.Changed {
		t.Error("run 2 should not report changed")
	}
}
