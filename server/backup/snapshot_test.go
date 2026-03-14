// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package backup

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func createTestDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("PRAGMA journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO test (id, name) VALUES (1, 'alice'), (2, 'bob')")
	if err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotSQLite_CreatesValidCopy(t *testing.T) {
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "source.db")
	createTestDB(t, srcPath)

	dstDir := t.TempDir()
	dstPath := filepath.Join(dstDir, "backup.db")

	if err := SnapshotSQLite(srcPath, dstPath); err != nil {
		t.Fatal(err)
	}

	// Verify the backup has the same data.
	db, err := sql.Open("sqlite3", dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM test").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}

	var name string
	if err := db.QueryRow("SELECT name FROM test WHERE id = 1").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "alice" {
		t.Errorf("name = %q, want alice", name)
	}
}

func TestSnapshotSQLite_SafeWithConcurrentWriters(t *testing.T) {
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "source.db")
	createTestDB(t, srcPath)

	// Keep the source DB open with an active writer.
	srcDB, err := sql.Open("sqlite3", srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer srcDB.Close()

	// Start concurrent writes.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 100; ; i++ {
			select {
			case <-stop:
				return
			default:
				srcDB.Exec("INSERT INTO test (id, name) VALUES (?, ?)", i, "concurrent")
			}
		}
	}()

	dstDir := t.TempDir()
	dstPath := filepath.Join(dstDir, "backup.db")

	if err := SnapshotSQLite(srcPath, dstPath); err != nil {
		close(stop)
		wg.Wait()
		t.Fatal(err)
	}

	close(stop)
	wg.Wait()

	// Verify the backup is readable and consistent.
	db, err := sql.Open("sqlite3", dstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM test").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 2 {
		t.Errorf("backup should have at least 2 rows, got %d", count)
	}
}

func TestCreateArchive_IncludesAllFiles(t *testing.T) {
	serverRoot := t.TempDir()

	// Set up test filesystem.
	dbDir := filepath.Join(serverRoot, "db")
	os.MkdirAll(dbDir, 0700)
	srcDB := filepath.Join(dbDir, "source.db")
	createTestDB(t, srcDB)

	proposedDir := filepath.Join(serverRoot, "hnb", "proposed", "modules")
	os.MkdirAll(proposedDir, 0700)
	os.WriteFile(filepath.Join(proposedDir, "nginx.hnb"), []byte("name: nginx\n"), 0644)

	secretsDir := filepath.Join(serverRoot, "secrets")
	os.MkdirAll(secretsDir, 0700)
	os.WriteFile(filepath.Join(secretsDir, "db_pass"), []byte("encrypted"), 0600)

	tlsDir := filepath.Join(serverRoot, "tls")
	os.MkdirAll(tlsDir, 0700)
	os.WriteFile(filepath.Join(tlsDir, "cert"), []byte("CERT"), 0644)

	os.WriteFile(filepath.Join(serverRoot, "server.yaml"), []byte("server:\n  grpc_listen: :1530\n"), 0644)

	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")

	err := CreateArchive(archivePath, CreateOptions{
		ServerYAMLPath: filepath.Join(serverRoot, "server.yaml"),
		ServerRoot:     serverRoot,
		DBDriver:       "sqlite",
		DBDSN:          srcDB,
		HobnobVersion:  "test-v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read manifest from archive.
	m, err := ReadManifest(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	expectedFiles := map[string]bool{
		"db/hobnob.db":                  false,
		"hnb/proposed/modules/nginx.hnb": false,
		"secrets/db_pass":               false,
		"tls/cert":                      false,
		"server.yaml":                   false,
	}

	for _, f := range m.Files {
		if _, ok := expectedFiles[f]; ok {
			expectedFiles[f] = true
		}
	}

	for f, found := range expectedFiles {
		if !found {
			t.Errorf("expected file %q not in manifest", f)
		}
	}
}

func TestCreateArchive_ManifestCorrect(t *testing.T) {
	serverRoot := t.TempDir()
	dbDir := filepath.Join(serverRoot, "db")
	os.MkdirAll(dbDir, 0700)
	srcDB := filepath.Join(dbDir, "source.db")
	createTestDB(t, srcDB)

	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")

	err := CreateArchive(archivePath, CreateOptions{
		ServerRoot:    serverRoot,
		DBDriver:      "sqlite",
		DBDSN:         srcDB,
		HobnobVersion: "v2.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}

	m, err := ReadManifest(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	if m.Version != 1 {
		t.Errorf("version = %d, want 1", m.Version)
	}
	if m.HobnobVersion != "v2.0.0" {
		t.Errorf("hobnob_version = %q, want v2.0.0", m.HobnobVersion)
	}
	if m.DBDriver != "sqlite" {
		t.Errorf("db_driver = %q, want sqlite", m.DBDriver)
	}
	if m.DBDSN != srcDB {
		t.Errorf("db_dsn = %q, want %q", m.DBDSN, srcDB)
	}
	if m.CreatedAt.IsZero() {
		t.Error("created_at is zero")
	}
}

func TestRestoreArchive_ExtractsToCorrectPaths(t *testing.T) {
	serverRoot := t.TempDir()

	// Create a backup with all components.
	dbDir := filepath.Join(serverRoot, "db")
	os.MkdirAll(dbDir, 0700)
	srcDB := filepath.Join(dbDir, "source.db")
	createTestDB(t, srcDB)

	proposedDir := filepath.Join(serverRoot, "hnb", "proposed", "modules")
	os.MkdirAll(proposedDir, 0700)
	os.WriteFile(filepath.Join(proposedDir, "nginx.hnb"), []byte("name: nginx\n"), 0644)

	secretsDir := filepath.Join(serverRoot, "secrets")
	os.MkdirAll(secretsDir, 0700)
	os.WriteFile(filepath.Join(secretsDir, "db_pass"), []byte("encrypted"), 0600)

	tlsDir := filepath.Join(serverRoot, "tls")
	os.MkdirAll(tlsDir, 0700)
	os.WriteFile(filepath.Join(tlsDir, "cert"), []byte("CERT"), 0644)

	os.WriteFile(filepath.Join(serverRoot, "server.yaml"), []byte("server:\n  grpc_listen: :1530\n"), 0644)

	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	err := CreateArchive(archivePath, CreateOptions{
		ServerYAMLPath: filepath.Join(serverRoot, "server.yaml"),
		ServerRoot:     serverRoot,
		DBDriver:       "sqlite",
		DBDSN:          srcDB,
		HobnobVersion:  "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Restore to a different directory.
	restoreDir := t.TempDir()
	restoreDBPath := filepath.Join(restoreDir, "db", "hobnob.db")

	m, err := RestoreArchive(archivePath, RestoreOptions{
		TargetDir: restoreDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("manifest is nil")
	}

	// Verify database was restored.
	db, err := sql.Open("sqlite3", restoreDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM test").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("restored db row count = %d, want 2", count)
	}

	// Verify config was restored.
	data, err := os.ReadFile(filepath.Join(restoreDir, "hnb", "proposed", "modules", "nginx.hnb"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "name: nginx\n" {
		t.Errorf("restored nginx.hnb = %q", string(data))
	}

	// Verify secrets.
	data, err = os.ReadFile(filepath.Join(restoreDir, "secrets", "db_pass"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "encrypted" {
		t.Errorf("restored secret = %q", string(data))
	}

	// Verify TLS.
	data, err = os.ReadFile(filepath.Join(restoreDir, "tls", "cert"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "CERT" {
		t.Errorf("restored cert = %q", string(data))
	}

	// Verify server.yaml.
	data, err = os.ReadFile(filepath.Join(restoreDir, "server.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "server:\n  grpc_listen: :1530\n" {
		t.Errorf("restored server.yaml = %q", string(data))
	}
}

func TestRestoreArchive_DBOnly(t *testing.T) {
	serverRoot := t.TempDir()

	dbDir := filepath.Join(serverRoot, "db")
	os.MkdirAll(dbDir, 0700)
	srcDB := filepath.Join(dbDir, "source.db")
	createTestDB(t, srcDB)

	proposedDir := filepath.Join(serverRoot, "hnb", "proposed")
	os.MkdirAll(proposedDir, 0700)
	os.WriteFile(filepath.Join(proposedDir, "test.hnb"), []byte("test"), 0644)

	os.WriteFile(filepath.Join(serverRoot, "server.yaml"), []byte("test"), 0644)

	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	err := CreateArchive(archivePath, CreateOptions{
		ServerYAMLPath: filepath.Join(serverRoot, "server.yaml"),
		ServerRoot:     serverRoot,
		DBDriver:       "sqlite",
		DBDSN:          srcDB,
		HobnobVersion:  "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	restoreDir := t.TempDir()
	_, err = RestoreArchive(archivePath, RestoreOptions{
		TargetDir: restoreDir,
		DBOnly:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// DB should exist.
	restoreDBPath := filepath.Join(restoreDir, "db", "hobnob.db")
	if _, err := os.Stat(restoreDBPath); err != nil {
		t.Errorf("database not restored: %v", err)
	}

	// Config should NOT exist.
	if _, err := os.Stat(filepath.Join(restoreDir, "hnb", "proposed", "test.hnb")); !os.IsNotExist(err) {
		t.Error("config should not be restored in db-only mode")
	}

	// server.yaml should NOT exist.
	if _, err := os.Stat(filepath.Join(restoreDir, "server.yaml")); !os.IsNotExist(err) {
		t.Error("server.yaml should not be restored in db-only mode")
	}
}

func TestReadManifest_WithoutFullExtraction(t *testing.T) {
	serverRoot := t.TempDir()
	dbDir := filepath.Join(serverRoot, "db")
	os.MkdirAll(dbDir, 0700)
	srcDB := filepath.Join(dbDir, "source.db")
	createTestDB(t, srcDB)

	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	err := CreateArchive(archivePath, CreateOptions{
		ServerRoot:    serverRoot,
		DBDriver:      "sqlite",
		DBDSN:         srcDB,
		HobnobVersion: "v1.0",
	})
	if err != nil {
		t.Fatal(err)
	}

	m, err := ReadManifest(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	if m.Version != 1 {
		t.Errorf("version = %d, want 1", m.Version)
	}
	if m.HobnobVersion != "v1.0" {
		t.Errorf("version = %q, want v1.0", m.HobnobVersion)
	}
	if m.DBDriver != "sqlite" {
		t.Errorf("driver = %q, want sqlite", m.DBDriver)
	}
}

func TestRoundTrip_CreateRestoreVerify(t *testing.T) {
	serverRoot := t.TempDir()

	dbDir := filepath.Join(serverRoot, "db")
	os.MkdirAll(dbDir, 0700)
	srcDB := filepath.Join(dbDir, "source.db")
	createTestDB(t, srcDB)

	// Add more data to verify fidelity.
	db, err := sql.Open("sqlite3", srcDB)
	if err != nil {
		t.Fatal(err)
	}
	for i := 3; i <= 100; i++ {
		db.Exec("INSERT INTO test (id, name) VALUES (?, ?)", i, "user")
	}
	db.Close()

	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	err = CreateArchive(archivePath, CreateOptions{
		ServerRoot:    serverRoot,
		DBDriver:      "sqlite",
		DBDSN:         srcDB,
		HobnobVersion: "round-trip",
	})
	if err != nil {
		t.Fatal(err)
	}

	restoreDir := t.TempDir()
	_, err = RestoreArchive(archivePath, RestoreOptions{TargetDir: restoreDir})
	if err != nil {
		t.Fatal(err)
	}

	restoredDB, err := sql.Open("sqlite3", filepath.Join(restoreDir, "db", "hobnob.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()

	var count int
	if err := restoredDB.QueryRow("SELECT COUNT(*) FROM test").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 100 {
		t.Errorf("round-trip count = %d, want 100", count)
	}
}

func TestCreateArchive_SkipsMissingOptionalDirs(t *testing.T) {
	serverRoot := t.TempDir()

	// Only create the DB — no secrets, tls, hnb, or server.yaml.
	dbDir := filepath.Join(serverRoot, "db")
	os.MkdirAll(dbDir, 0700)
	srcDB := filepath.Join(dbDir, "source.db")
	createTestDB(t, srcDB)

	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	err := CreateArchive(archivePath, CreateOptions{
		ServerRoot:    serverRoot,
		DBDriver:      "sqlite",
		DBDSN:         srcDB,
		HobnobVersion: "minimal",
	})
	if err != nil {
		t.Fatal(err)
	}

	m, err := ReadManifest(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	// Should only have the DB file.
	if len(m.Files) != 1 {
		t.Errorf("expected 1 file (db only), got %d: %v", len(m.Files), m.Files)
	}
	if m.Files[0] != "db/hobnob.db" {
		t.Errorf("expected db/hobnob.db, got %q", m.Files[0])
	}
}

func TestRestoreArchive_DryRun(t *testing.T) {
	serverRoot := t.TempDir()
	dbDir := filepath.Join(serverRoot, "db")
	os.MkdirAll(dbDir, 0700)
	srcDB := filepath.Join(dbDir, "source.db")
	createTestDB(t, srcDB)

	os.WriteFile(filepath.Join(serverRoot, "server.yaml"), []byte("test"), 0644)

	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	err := CreateArchive(archivePath, CreateOptions{
		ServerYAMLPath: filepath.Join(serverRoot, "server.yaml"),
		ServerRoot:     serverRoot,
		DBDriver:       "sqlite",
		DBDSN:          srcDB,
		HobnobVersion:  "dry",
	})
	if err != nil {
		t.Fatal(err)
	}

	restoreDir := t.TempDir()
	m, err := RestoreArchive(archivePath, RestoreOptions{
		TargetDir: restoreDir,
		DryRun:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if m == nil {
		t.Fatal("manifest should be returned on dry run")
	}
	if m.HobnobVersion != "dry" {
		t.Errorf("version = %q, want dry", m.HobnobVersion)
	}

	// Nothing should be written to the restore dir.
	entries, _ := os.ReadDir(restoreDir)
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("dry run wrote files: %v", names)
	}
}

func TestReadManifest_InvalidArchive(t *testing.T) {
	badPath := filepath.Join(t.TempDir(), "bad.tar.gz")
	os.WriteFile(badPath, []byte("not a valid archive"), 0644)

	_, err := ReadManifest(badPath)
	if err == nil {
		t.Fatal("expected error for invalid archive")
	}
}

func TestSanitizePgDSN_URI(t *testing.T) {
	dsn := "postgres://admin:s3cret@db.example.com:5432/hobnob?sslmode=require"
	sanitized, pw := sanitizePgDSN(dsn)
	if pw != "s3cret" {
		t.Errorf("password = %q, want s3cret", pw)
	}
	if strings.Contains(sanitized, "s3cret") {
		t.Errorf("sanitized DSN still contains password: %s", sanitized)
	}
	if !strings.Contains(sanitized, "admin@db.example.com") {
		t.Errorf("sanitized DSN missing user/host: %s", sanitized)
	}
}

func TestSanitizePgDSN_KeyValue(t *testing.T) {
	dsn := "host=db.example.com port=5432 user=admin password=s3cret dbname=hobnob"
	sanitized, pw := sanitizePgDSN(dsn)
	if pw != "s3cret" {
		t.Errorf("password = %q, want s3cret", pw)
	}
	if strings.Contains(sanitized, "s3cret") {
		t.Errorf("sanitized DSN still contains password: %s", sanitized)
	}
	if !strings.Contains(sanitized, "host=db.example.com") {
		t.Errorf("sanitized DSN missing host: %s", sanitized)
	}
}

func TestSanitizePgDSN_NoPassword(t *testing.T) {
	dsn := "postgres://admin@db.example.com:5432/hobnob"
	sanitized, pw := sanitizePgDSN(dsn)
	if pw != "" {
		t.Errorf("password = %q, want empty", pw)
	}
	if sanitized != dsn {
		t.Errorf("sanitized = %q, want original %q", sanitized, dsn)
	}
}

func TestManifestJSON_RoundTrip(t *testing.T) {
	m := Manifest{
		Version:       1,
		HobnobVersion: "v1.0",
		DBDriver:      "sqlite",
		DBDSN:         "/data/hobnob.db",
		Files:         []string{"db/hobnob.db", "server.yaml"},
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	var m2 Manifest
	if err := json.Unmarshal(data, &m2); err != nil {
		t.Fatal(err)
	}

	if m2.Version != m.Version || m2.DBDriver != m.DBDriver || len(m2.Files) != len(m.Files) {
		t.Errorf("manifest round-trip mismatch: %+v vs %+v", m, m2)
	}
}
