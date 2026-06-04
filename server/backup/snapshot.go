// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/argon2"
)

// Manifest describes a backup archive.
type Manifest struct {
	Version       int       `json:"version"`
	CreatedAt     time.Time `json:"created_at"`
	VigoVersion string   `json:"vigo_version"`
	DBDriver      string   `json:"db_driver"`
	DBDSN         string   `json:"db_dsn"`
	Files         []string `json:"files"`
	SHA256        string   `json:"sha256,omitempty"` // hex-encoded hash of the archive
	Encrypted     bool     `json:"encrypted,omitempty"`
}

// CreateOptions configures what goes into a backup archive.
type CreateOptions struct {
	// ServerYAMLPath is the path to server.yaml.
	ServerYAMLPath string
	// ServerRoot is the base directory (default /srv/vigo).
	ServerRoot string
	// DBDriver is "sqlite" or "postgres".
	DBDriver string
	// DBDSN is the database connection string or file path.
	DBDSN string
	// VigoVersion is the server version string.
	VigoVersion string
}

// RestoreOptions configures how a backup archive is restored.
type RestoreOptions struct {
	// TargetDir is the root directory to restore into (default /srv/vigo).
	TargetDir string
	// DBOnly skips config/secrets/tls, restoring only the database.
	DBOnly bool
	// DryRun prints what would be restored without writing.
	DryRun bool
}

// SnapshotSQLite creates a safe point-in-time copy of a SQLite database
// using VACUUM INTO (safe with concurrent WAL writers, atomic).
func SnapshotSQLite(srcDSN, dstPath string) error {
	db, err := sql.Open("sqlite3", srcDSN)
	if err != nil {
		return fmt.Errorf("opening source database: %w", err)
	}
	defer db.Close()

	// VACUUM INTO creates an atomic, consistent snapshot even with active
	// WAL writers. Available since SQLite 3.27.0 (2019).
	if _, err := db.Exec("VACUUM INTO ?", dstPath); err != nil {
		return fmt.Errorf("VACUUM INTO: %w", err)
	}
	return nil
}

// SnapshotPostgres runs pg_dump and writes the output to dstPath.
// The DSN is passed via --dbname flag and PGPASSWORD env var to avoid
// leaking credentials in the process listing.
func SnapshotPostgres(dsn, dstPath string) error {
	sanitized, password := sanitizePgDSN(dsn)
	cmd := exec.Command("pg_dump", "--format=plain", "--dbname", sanitized)
	if password != "" {
		cmd.Env = append(os.Environ(), "PGPASSWORD="+password)
	}
	out, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("creating dump file: %w", err)
	}
	defer out.Close()

	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Remove(dstPath)
		return fmt.Errorf("pg_dump: %w", err)
	}
	return nil
}

// sanitizePgDSN extracts the password from a PostgreSQL DSN and returns
// the DSN without the password plus the extracted password separately.
// Supports both URI format (postgres://user:pass@host/db) and keyword/value
// format (host=h user=u password=p dbname=d).
func sanitizePgDSN(dsn string) (sanitized, password string) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return dsn, ""
		}
		pw, _ := u.User.Password()
		if pw != "" {
			u.User = url.UserPassword(u.User.Username(), "")
			// Rebuild without password — url.UserPassword("user","") still
			// includes a colon, so strip it manually.
			sanitized = strings.Replace(u.String(), ":@", "@", 1)
			return sanitized, pw
		}
		return dsn, ""
	}

	// Keyword/value format: extract password=... field.
	var parts []string
	for _, field := range strings.Fields(dsn) {
		if strings.HasPrefix(field, "password=") {
			password = strings.TrimPrefix(field, "password=")
		} else {
			parts = append(parts, field)
		}
	}
	if password != "" {
		return strings.Join(parts, " "), password
	}
	return dsn, ""
}

// CreateArchive builds a tar.gz backup at archivePath.
func CreateArchive(archivePath string, opts CreateOptions) error {
	serverRoot := opts.ServerRoot
	if serverRoot == "" {
		serverRoot = "/srv/vigo"
	}

	// Create a temp dir for staging.
	tmpDir, err := os.MkdirTemp("", "vigo-backup-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	var files []string

	// 1. Database snapshot.
	dbDriver := opts.DBDriver
	if dbDriver == "" {
		dbDriver = "sqlite"
	}
	dbDir := filepath.Join(tmpDir, "db")
	if err := os.MkdirAll(dbDir, 0700); err != nil {
		return fmt.Errorf("creating db staging dir: %w", err)
	}

	if dbDriver == "sqlite" {
		dstDB := filepath.Join(dbDir, "vigo.db")
		if err := SnapshotSQLite(opts.DBDSN, dstDB); err != nil {
			return fmt.Errorf("sqlite snapshot: %w", err)
		}
		files = append(files, "db/vigo.db")
	} else {
		dstDump := filepath.Join(dbDir, "dump.sql")
		if err := SnapshotPostgres(opts.DBDSN, dstDump); err != nil {
			return fmt.Errorf("postgres dump: %w", err)
		}
		files = append(files, "db/dump.sql")
	}

	// 2. Copy directories that exist.
	dirMappings := []struct {
		src     string
		archive string
	}{
		{filepath.Join(serverRoot, "stacks"), "stacks"},
		{filepath.Join(serverRoot, "secrets"), "secrets"},
		{filepath.Join(serverRoot, "tls"), "tls"},
	}

	for _, dm := range dirMappings {
		if _, err := os.Stat(dm.src); os.IsNotExist(err) {
			continue // optional directory
		}
		copied, err := copyTree(dm.src, filepath.Join(tmpDir, dm.archive))
		if err != nil {
			return fmt.Errorf("copying %s: %w", dm.src, err)
		}
		for _, f := range copied {
			files = append(files, filepath.Join(dm.archive, f))
		}
	}

	// 3. Copy server.yaml.
	serverYAML := opts.ServerYAMLPath
	if serverYAML == "" {
		serverYAML = filepath.Join(serverRoot, "server.yaml")
	}
	if _, err := os.Stat(serverYAML); err == nil {
		if err := copyFile(serverYAML, filepath.Join(tmpDir, "server.yaml")); err != nil {
			return fmt.Errorf("copying server.yaml: %w", err)
		}
		files = append(files, "server.yaml")
	}

	// 4. Write manifest.
	manifest := Manifest{
		Version:       1,
		CreatedAt:     time.Now().UTC(),
		VigoVersion: opts.VigoVersion,
		DBDriver:      dbDriver,
		DBDSN:         opts.DBDSN,
		Files:         files,
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "manifest.json"), manifestData, 0644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}

	// 5. Create tar.gz.
	return writeTarGz(archivePath, tmpDir)
}

// RestoreArchive extracts a backup tar.gz and restores all components.
func RestoreArchive(archivePath string, opts RestoreOptions) (*Manifest, error) {
	targetDir := opts.TargetDir
	if targetDir == "" {
		targetDir = "/srv/vigo"
	}

	// Extract to a temp dir first.
	tmpDir, err := os.MkdirTemp("", "vigo-restore-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := extractTarGz(archivePath, tmpDir); err != nil {
		return nil, fmt.Errorf("extracting archive: %w", err)
	}

	// Read manifest.
	manifestPath := filepath.Join(tmpDir, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	if opts.DryRun {
		return &manifest, nil
	}

	// Restore database.
	if manifest.DBDriver == "sqlite" {
		srcDB := filepath.Join(tmpDir, "db", "vigo.db")
		if _, err := os.Stat(srcDB); err == nil {
			// Always restore to target dir; the original DSN path may not exist.
			dstDB := filepath.Join(targetDir, "db", "vigo.db")
			if err := os.MkdirAll(filepath.Dir(dstDB), 0700); err != nil {
				return nil, fmt.Errorf("creating db directory: %w", err)
			}
			if err := copyFile(srcDB, dstDB); err != nil {
				return nil, fmt.Errorf("restoring database: %w", err)
			}
			// Remove any WAL/SHM files from a previous instance.
			os.Remove(dstDB + "-wal")
			os.Remove(dstDB + "-shm")
		}
	} else {
		srcDump := filepath.Join(tmpDir, "db", "dump.sql")
		if _, err := os.Stat(srcDump); err == nil {
			sanitized, pw := sanitizePgDSN(manifest.DBDSN)
			cmd := exec.Command("psql", "--dbname", sanitized, "-f", srcDump)
			if pw != "" {
				cmd.Env = append(os.Environ(), "PGPASSWORD="+pw)
			}
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return nil, fmt.Errorf("restoring postgres dump: %w", err)
			}
		}
	}

	if opts.DBOnly {
		return &manifest, nil
	}

	// Restore config, secrets, tls, server.yaml.
	restoreMappings := []struct {
		src string
		dst string
	}{
		{"stacks", filepath.Join(targetDir, "stacks")},
		{"secrets", filepath.Join(targetDir, "secrets")},
		{"tls", filepath.Join(targetDir, "tls")},
	}

	for _, rm := range restoreMappings {
		srcDir := filepath.Join(tmpDir, rm.src)
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			continue
		}
		if err := os.MkdirAll(rm.dst, 0700); err != nil {
			return nil, fmt.Errorf("creating %s: %w", rm.dst, err)
		}
		if _, err := copyTree(srcDir, rm.dst); err != nil {
			return nil, fmt.Errorf("restoring %s: %w", rm.src, err)
		}
	}

	// server.yaml
	srcYAML := filepath.Join(tmpDir, "server.yaml")
	if _, err := os.Stat(srcYAML); err == nil {
		if err := copyFile(srcYAML, filepath.Join(targetDir, "server.yaml")); err != nil {
			return nil, fmt.Errorf("restoring server.yaml: %w", err)
		}
	}

	return &manifest, nil
}

// ReadManifest reads the manifest from a backup archive without full extraction.
func ReadManifest(archivePath string) (*Manifest, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("opening archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("manifest.json not found in archive")
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}
		if hdr.Name == "manifest.json" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("reading manifest: %w", err)
			}
			var m Manifest
			if err := json.Unmarshal(data, &m); err != nil {
				return nil, fmt.Errorf("parsing manifest: %w", err)
			}
			return &m, nil
		}
	}
}

// writeTarGz creates a tar.gz archive from a directory.
func writeTarGz(archivePath, srcDir string) error {
	f, err := os.Create(archivePath)
	if err != nil {
		return fmt.Errorf("creating archive file: %w", err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.Walk(srcDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get relative path for the archive.
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("creating tar header for %s: %w", rel, err)
		}
		header.Name = filepath.ToSlash(rel)

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("writing tar header for %s: %w", rel, err)
		}

		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("opening %s: %w", path, err)
		}
		defer file.Close()

		if _, err := io.Copy(tw, file); err != nil {
			return fmt.Errorf("writing %s to tar: %w", rel, err)
		}

		return nil
	})
}

// extractTarGz extracts a tar.gz archive into dstDir.
func extractTarGz(archivePath, dstDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("opening archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar entry: %w", err)
		}

		// Prevent path traversal.
		target := filepath.Join(dstDir, hdr.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dstDir)+string(os.PathSeparator)) {
			return fmt.Errorf("invalid tar entry path: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, fs.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("creating directory %s: %w", hdr.Name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", hdr.Name, err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("creating file %s: %w", hdr.Name, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("writing %s: %w", hdr.Name, err)
			}
			out.Close()
		}
	}

	return nil
}

// copyFile copies a single file preserving permissions.
func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// copyTree recursively copies a directory tree. Returns relative paths of all
// files copied.
func copyTree(srcDir, dstDir string) ([]string, error) {
	var copied []string

	err := filepath.Walk(srcDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		target := filepath.Join(dstDir, rel)

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}

		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}

		if err := copyFile(path, target); err != nil {
			return err
		}

		copied = append(copied, rel)
		return nil
	})

	return copied, err
}

// HashFile computes the SHA-256 hex digest of a file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// WriteHashFile writes a SHA-256 sidecar file (archive.tar.gz.sha256).
func WriteHashFile(archivePath, hash string) error {
	name := filepath.Base(archivePath)
	content := hash + "  " + name + "\n"
	return os.WriteFile(archivePath+".sha256", []byte(content), 0600)
}

// VerifyHashFile checks a SHA-256 sidecar file against the archive.
// Returns the hash if valid, or an error if the file is missing or mismatched.
func VerifyHashFile(archivePath string) (string, error) {
	actual, err := HashFile(archivePath)
	if err != nil {
		return "", fmt.Errorf("computing hash: %w", err)
	}
	sidecar := archivePath + ".sha256"
	data, err := os.ReadFile(sidecar)
	if err != nil {
		return actual, fmt.Errorf("reading %s: %w (actual hash: %s)", sidecar, err, actual)
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return actual, fmt.Errorf("empty hash file")
	}
	if fields[0] != actual {
		return actual, fmt.Errorf("hash mismatch: expected %s, got %s", fields[0], actual)
	}
	return actual, nil
}

// Argon2 parameters for password-based key derivation.
const (
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64 MB
	argon2Threads = 4
	argon2KeyLen  = 32 // AES-256
	saltLen       = 16
)

// EncryptFile encrypts a file in place using AES-256-GCM with a password-derived
// key (Argon2id). The file format is: salt (16 bytes) || nonce (12 bytes) || ciphertext.
func EncryptFile(path, password string) error {
	plaintext, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generating salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("creating GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	// Write: salt || nonce || ciphertext
	out := make([]byte, 0, saltLen+len(nonce)+len(ciphertext))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)

	return os.WriteFile(path, out, 0600)
}

// DecryptFile decrypts a file in place that was encrypted with EncryptFile.
func DecryptFile(path, password string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	if len(data) < saltLen+12 { // salt + minimum nonce
		return fmt.Errorf("file too small to be encrypted")
	}

	salt := data[:saltLen]
	key := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("creating GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < saltLen+nonceSize {
		return fmt.Errorf("file too small for nonce")
	}

	nonce := data[saltLen : saltLen+nonceSize]
	ciphertext := data[saltLen+nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return fmt.Errorf("decryption failed (wrong password?): %w", err)
	}

	return os.WriteFile(path, plaintext, 0600)
}
