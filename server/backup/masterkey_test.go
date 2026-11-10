// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.alexander4.io/vigo/server/secrets"
)

func masterKeyFixture(t *testing.T) (serverRoot, srcDB string) {
	t.Helper()
	serverRoot = t.TempDir()
	os.MkdirAll(filepath.Join(serverRoot, "db"), 0o700)
	srcDB = filepath.Join(serverRoot, "db", "source.db")
	createTestDB(t, srcDB)
	secretsDir := filepath.Join(serverRoot, "secrets")
	os.MkdirAll(secretsDir, 0o700)
	os.WriteFile(filepath.Join(secretsDir, "db_pass"), []byte("ciphertext"), 0o600)
	os.WriteFile(filepath.Join(secretsDir, secrets.MasterKeyFile), []byte("KEY"), 0o600)
	return serverRoot, srcDB
}

func archiveFiles(t *testing.T, opts CreateOptions) *Manifest {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := CreateArchive(archivePath, opts); err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func hasFile(m *Manifest, f string) bool {
	for _, x := range m.Files {
		if x == f {
			return true
		}
	}
	return false
}

// An unencrypted archive must not carry the master key next to the
// ciphertext it decrypts; an encrypted one does.
func TestCreateArchive_MasterKeyOnlyWhenEncrypted(t *testing.T) {
	root, db := masterKeyFixture(t)
	key := "secrets/" + secrets.MasterKeyFile

	m := archiveFiles(t, CreateOptions{ServerRoot: root, DBDriver: "sqlite", DBDSN: db})
	if hasFile(m, key) || m.MasterKeyIncluded {
		t.Fatalf("plain archive includes the master key: %v", m.Files)
	}
	if !hasFile(m, "secrets/db_pass") {
		t.Fatalf("secret ciphertext missing: %v", m.Files)
	}

	m = archiveFiles(t, CreateOptions{ServerRoot: root, DBDriver: "sqlite", DBDSN: db, IncludeMasterKey: true})
	if !hasFile(m, key) || !m.MasterKeyIncluded {
		t.Fatalf("archive for --encrypt should include the master key: %v", m.Files)
	}
}

// The manifest records the DSN without its password.
func TestManifestDSNRedacted(t *testing.T) {
	for _, dsn := range []string{
		"postgres://vigo:hunter2@db:5432/vigo",
		"host=db user=vigo password=hunter2 dbname=vigo",
	} {
		m := newManifest(CreateOptions{DBDSN: dsn}, "postgres", nil)
		if strings.Contains(m.DBDSN, "hunter2") {
			t.Fatalf("manifest DSN holds the password: %q", m.DBDSN)
		}
	}
}

// failAfterFirstWrite accepts the first Write (gzip's header) and fails every
// later one, so the failure surfaces only when gzip flushes on Close.
type failAfterFirstWrite struct{ writes int }

func (f *failAfterFirstWrite) Write(p []byte) (int, error) {
	f.writes++
	if f.writes > 1 {
		return 0, errors.New("disk full")
	}
	return len(p), nil
}
func (f *failAfterFirstWrite) Close() error { return nil }

// A failure while the gzip stream is flushed on Close means a truncated
// archive and must be reported, not swallowed by a deferred Close.
func TestWriteTarGz_ReportsCloseError(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "f"), []byte("small"), 0o600)
	if err := writeTarGzTo(&failAfterFirstWrite{}, src); err == nil {
		t.Fatal("writeTarGzTo returned nil although the final flush failed")
	}
}
