package modules

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/isogov/isogov/agent/snapshots"
)

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func newTestSnapshotManager(dir string) *snapshots.Manager {
	return &snapshots.Manager{
		BaseDir:    dir,
		MaxPerPath: 5,
	}
}

func listSnapshots(t *testing.T, snapshotDir, filePath string) []string {
	t.Helper()
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(filePath)))
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		t.Fatalf("read snapshot dir: %v", err)
	}
	var matches []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), hash) {
			matches = append(matches, e.Name())
		}
	}
	sort.Strings(matches)
	return matches
}
