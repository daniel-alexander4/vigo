// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewBlobStore_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blobs")
	_, err := NewBlobStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory not created: %v", err)
	}
}

func TestStore_And_Open(t *testing.T) {
	store, _ := NewBlobStore(t.TempDir())

	data := []byte("hello swarm blob content")
	hash, size, err := store.Store(bytes.NewReader(data), "test.bin")
	if err != nil {
		t.Fatal(err)
	}

	expectedHash := sha256.Sum256(data)
	expectedHex := hex.EncodeToString(expectedHash[:])
	if hash != expectedHex {
		t.Errorf("hash = %s, want %s", hash, expectedHex)
	}
	if size != int64(len(data)) {
		t.Errorf("size = %d, want %d", size, len(data))
	}

	// Open and read back.
	f, openSize, err := store.Open(hash)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if openSize != size {
		t.Errorf("open size = %d, want %d", openSize, size)
	}
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, data) {
		t.Error("read data mismatch")
	}
}

func TestStore_Dedup(t *testing.T) {
	store, _ := NewBlobStore(t.TempDir())

	data := []byte("same content")
	h1, _, _ := store.Store(bytes.NewReader(data), "first")
	h2, _, _ := store.Store(bytes.NewReader(data), "second")

	if h1 != h2 {
		t.Errorf("expected same hash, got %s and %s", h1, h2)
	}

	// Label should be updated.
	if store.Label(h1) != "second" {
		t.Errorf("label = %q, want %q", store.Label(h1), "second")
	}
}

func TestHas(t *testing.T) {
	store, _ := NewBlobStore(t.TempDir())

	data := []byte("exists")
	hash, _, _ := store.Store(bytes.NewReader(data), "")

	if !store.Has(hash) {
		t.Error("expected Has() = true")
	}
	if store.Has("0000000000000000000000000000000000000000000000000000000000000000") {
		t.Error("expected Has() = false for missing blob")
	}
}

func TestDelete(t *testing.T) {
	store, _ := NewBlobStore(t.TempDir())

	data := []byte("to delete")
	hash, _, _ := store.Store(bytes.NewReader(data), "bye")

	if err := store.Delete(hash); err != nil {
		t.Fatal(err)
	}
	if store.Has(hash) {
		t.Error("blob still exists after delete")
	}
}

func TestList(t *testing.T) {
	store, _ := NewBlobStore(t.TempDir())

	store.Store(bytes.NewReader([]byte("blob1")), "first")
	store.Store(bytes.NewReader([]byte("blob2")), "second")

	blobs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 2 {
		t.Fatalf("expected 2 blobs, got %d", len(blobs))
	}
	for _, b := range blobs {
		if b.ChunkSize != ChunkSize {
			t.Errorf("chunk_size = %d, want %d", b.ChunkSize, ChunkSize)
		}
		if b.Chunks != 1 {
			t.Errorf("chunks = %d, want 1", b.Chunks)
		}
	}
}

func TestOpen_InvalidHash(t *testing.T) {
	store, _ := NewBlobStore(t.TempDir())
	_, _, err := store.Open("not-a-hash")
	if err == nil {
		t.Error("expected error for invalid hash")
	}
}

func TestOpen_PathTraversal(t *testing.T) {
	store, _ := NewBlobStore(t.TempDir())
	_, _, err := store.Open("../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestIsValidHash(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"a" + strings.Repeat("0", 63), true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("0", 64), true},
		{strings.Repeat("0", 63), false},
		{strings.Repeat("0", 65), false},
		{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", false}, // uppercase
		{"", false},
	}
	for _, tt := range tests {
		if got := IsValidHash(tt.input); got != tt.want {
			t.Errorf("IsValidHash(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestChunksForSize(t *testing.T) {
	tests := []struct {
		size int64
		want int
	}{
		{0, 0},
		{1, 1},
		{ChunkSize, 1},
		{ChunkSize + 1, 2},
		{ChunkSize * 10, 10},
		{ChunkSize*10 + 500, 11},
	}
	for _, tt := range tests {
		if got := chunksForSize(tt.size); got != tt.want {
			t.Errorf("chunksForSize(%d) = %d, want %d", tt.size, got, tt.want)
		}
	}
}
