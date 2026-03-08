package traits

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

// mockDownloader implements Downloader for testing.
type mockDownloader struct {
	data     map[string][]byte // name -> executable bytes
	called   map[string]int    // name -> download call count
	failName string            // if set, DownloadCustomTrait returns error for this name
}

func newMockDownloader() *mockDownloader {
	return &mockDownloader{
		data:   make(map[string][]byte),
		called: make(map[string]int),
	}
}

func (m *mockDownloader) DownloadCustomTrait(ctx context.Context, name string) ([]byte, error) {
	m.called[name]++
	if name == m.failName {
		return nil, fmt.Errorf("download failed for %s", name)
	}
	data, ok := m.data[name]
	if !ok {
		return nil, fmt.Errorf("trait %q not found", name)
	}
	return data, nil
}

// makeScript creates a shell script that echoes the given string to stdout.
func makeScript(output string) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\necho '%s'\n", output))
}

// makeFailScript creates a shell script that exits with a non-zero code.
func makeFailScript() []byte {
	return []byte("#!/bin/sh\nexit 1\n")
}

// makeSleepScript creates a shell script that sleeps for the given seconds.
func makeSleepScript(seconds int) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\nsleep %d\n", seconds))
}

// checksumOf returns the SHA256 hex digest of data (for test assertions).
func checksumOf(data []byte) string {
	return computeSHA256(data)
}

func TestSyncAndRun_NewTraits_DownloadsBothAndReturnsResults(t *testing.T) {
	cacheDir := t.TempDir()
	dl := newMockDownloader()

	script1 := makeScript(`{"version":"1.2.3"}`)
	script2 := makeScript(`{"status":"ok"}`)
	dl.data["app-version"] = script1
	dl.data["health"] = script2

	exec := NewExecutor(cacheDir, dl, 10*time.Second)
	manifest := []*pb.CustomTraitEntry{
		{Name: "app-version", Sha256: checksumOf(script1)},
		{Name: "health", Sha256: checksumOf(script2)},
	}

	result, err := exec.SyncAndRun(context.Background(), manifest)
	if err != nil {
		t.Fatalf("SyncAndRun: %v", err)
	}

	// Both traits should have been downloaded.
	if dl.called["app-version"] != 1 {
		t.Errorf("app-version download count = %d, want 1", dl.called["app-version"])
	}
	if dl.called["health"] != 1 {
		t.Errorf("health download count = %d, want 1", dl.called["health"])
	}

	// Results should be nested under "custom" key.
	custom, ok := result["custom"].(map[string]any)
	if !ok {
		t.Fatalf("result[\"custom\"] not a map, got %T: %v", result["custom"], result)
	}

	av, ok := custom["app-version"].(map[string]any)
	if !ok {
		t.Fatalf("custom[\"app-version\"] not a map, got %T", custom["app-version"])
	}
	if av["version"] != "1.2.3" {
		t.Errorf("version = %v, want 1.2.3", av["version"])
	}

	h, ok := custom["health"].(map[string]any)
	if !ok {
		t.Fatalf("custom[\"health\"] not a map, got %T", custom["health"])
	}
	if h["status"] != "ok" {
		t.Errorf("status = %v, want ok", h["status"])
	}
}

func TestSyncAndRun_CachedTraits_NoRedownload(t *testing.T) {
	cacheDir := t.TempDir()
	dl := newMockDownloader()

	script := makeScript(`{"cached":"yes"}`)
	dl.data["mytrait"] = script

	// Pre-populate cache with the same content.
	cachePath := filepath.Join(cacheDir, "mytrait")
	if err := os.WriteFile(cachePath, script, 0755); err != nil {
		t.Fatal(err)
	}

	exec := NewExecutor(cacheDir, dl, 10*time.Second)
	manifest := []*pb.CustomTraitEntry{
		{Name: "mytrait", Sha256: checksumOf(script)},
	}

	result, err := exec.SyncAndRun(context.Background(), manifest)
	if err != nil {
		t.Fatalf("SyncAndRun: %v", err)
	}

	// Should NOT have downloaded (checksum matches cache).
	if dl.called["mytrait"] != 0 {
		t.Errorf("mytrait download count = %d, want 0 (cached)", dl.called["mytrait"])
	}

	// But should still have executed.
	custom, ok := result["custom"].(map[string]any)
	if !ok {
		t.Fatalf("missing custom key")
	}
	if _, ok := custom["mytrait"]; !ok {
		t.Error("mytrait not in result despite being cached")
	}
}

func TestSyncAndRun_ChangedChecksum_DownloadsOnlyChanged(t *testing.T) {
	cacheDir := t.TempDir()
	dl := newMockDownloader()

	oldScript := makeScript(`{"v":"old"}`)
	newScript := makeScript(`{"v":"new"}`)
	unchangedScript := makeScript(`{"v":"same"}`)

	dl.data["changed"] = newScript
	dl.data["unchanged"] = unchangedScript

	// Pre-populate cache with old version of "changed" and current of "unchanged".
	os.WriteFile(filepath.Join(cacheDir, "changed"), oldScript, 0755)
	os.WriteFile(filepath.Join(cacheDir, "unchanged"), unchangedScript, 0755)

	exec := NewExecutor(cacheDir, dl, 10*time.Second)
	manifest := []*pb.CustomTraitEntry{
		{Name: "changed", Sha256: checksumOf(newScript)},
		{Name: "unchanged", Sha256: checksumOf(unchangedScript)},
	}

	_, err := exec.SyncAndRun(context.Background(), manifest)
	if err != nil {
		t.Fatalf("SyncAndRun: %v", err)
	}

	if dl.called["changed"] != 1 {
		t.Errorf("changed download count = %d, want 1", dl.called["changed"])
	}
	if dl.called["unchanged"] != 0 {
		t.Errorf("unchanged download count = %d, want 0", dl.called["unchanged"])
	}
}

func TestSyncAndRun_ValidJSON(t *testing.T) {
	cacheDir := t.TempDir()
	dl := newMockDownloader()

	script := makeScript(`{"version":"1.2.3"}`)
	dl.data["app-version"] = script

	exec := NewExecutor(cacheDir, dl, 10*time.Second)
	manifest := []*pb.CustomTraitEntry{
		{Name: "app-version", Sha256: checksumOf(script)},
	}

	result, err := exec.SyncAndRun(context.Background(), manifest)
	if err != nil {
		t.Fatalf("SyncAndRun: %v", err)
	}

	custom := result["custom"].(map[string]any)
	av := custom["app-version"].(map[string]any)
	if av["version"] != "1.2.3" {
		t.Errorf("version = %v, want 1.2.3", av["version"])
	}
}

func TestSyncAndRun_NestedJSON(t *testing.T) {
	cacheDir := t.TempDir()
	dl := newMockDownloader()

	script := makeScript(`{"config":{"version":"2.0","debug":true}}`)
	dl.data["myapp"] = script

	exec := NewExecutor(cacheDir, dl, 10*time.Second)
	manifest := []*pb.CustomTraitEntry{
		{Name: "myapp", Sha256: checksumOf(script)},
	}

	result, err := exec.SyncAndRun(context.Background(), manifest)
	if err != nil {
		t.Fatalf("SyncAndRun: %v", err)
	}

	custom := result["custom"].(map[string]any)
	myapp := custom["myapp"].(map[string]any)
	config, ok := myapp["config"].(map[string]any)
	if !ok {
		t.Fatalf("config is not a map, got %T", myapp["config"])
	}
	if config["version"] != "2.0" {
		t.Errorf("config.version = %v, want 2.0", config["version"])
	}
	if config["debug"] != true {
		t.Errorf("config.debug = %v, want true", config["debug"])
	}
}

func TestSyncAndRun_NonZeroExit_SkippedWithWarning(t *testing.T) {
	cacheDir := t.TempDir()
	dl := newMockDownloader()

	failScript := makeFailScript()
	goodScript := makeScript(`{"ok":true}`)
	dl.data["bad-trait"] = failScript
	dl.data["good-trait"] = goodScript

	exec := NewExecutor(cacheDir, dl, 10*time.Second)
	manifest := []*pb.CustomTraitEntry{
		{Name: "bad-trait", Sha256: checksumOf(failScript)},
		{Name: "good-trait", Sha256: checksumOf(goodScript)},
	}

	result, err := exec.SyncAndRun(context.Background(), manifest)
	if err != nil {
		t.Fatalf("SyncAndRun: %v", err)
	}

	custom := result["custom"].(map[string]any)

	// bad-trait should be absent.
	if _, ok := custom["bad-trait"]; ok {
		t.Error("bad-trait should be absent from results")
	}

	// good-trait should still be present.
	if _, ok := custom["good-trait"]; !ok {
		t.Error("good-trait should be present despite bad-trait failure")
	}
}

func TestSyncAndRun_InvalidJSON_SkippedWithWarning(t *testing.T) {
	cacheDir := t.TempDir()
	dl := newMockDownloader()

	badScript := makeScript("not valid json")
	dl.data["bad-json"] = badScript

	exec := NewExecutor(cacheDir, dl, 10*time.Second)
	manifest := []*pb.CustomTraitEntry{
		{Name: "bad-json", Sha256: checksumOf(badScript)},
	}

	result, err := exec.SyncAndRun(context.Background(), manifest)
	if err != nil {
		t.Fatalf("SyncAndRun: %v", err)
	}

	custom, ok := result["custom"].(map[string]any)
	if ok {
		if _, found := custom["bad-json"]; found {
			t.Error("bad-json should be absent from results due to invalid JSON")
		}
	}
}

func TestSyncAndRun_Timeout_KilledAndSkipped(t *testing.T) {
	cacheDir := t.TempDir()
	dl := newMockDownloader()

	slowScript := makeSleepScript(15)
	dl.data["slow-trait"] = slowScript

	// Use a very short timeout for test speed.
	exec := NewExecutor(cacheDir, dl, 500*time.Millisecond)
	manifest := []*pb.CustomTraitEntry{
		{Name: "slow-trait", Sha256: checksumOf(slowScript)},
	}

	result, err := exec.SyncAndRun(context.Background(), manifest)
	if err != nil {
		t.Fatalf("SyncAndRun: %v", err)
	}

	custom, ok := result["custom"].(map[string]any)
	if ok {
		if _, found := custom["slow-trait"]; found {
			t.Error("slow-trait should be absent from results due to timeout")
		}
	}
}

func TestSyncAndRun_EmptyManifest(t *testing.T) {
	cacheDir := t.TempDir()
	dl := newMockDownloader()

	exec := NewExecutor(cacheDir, dl, 10*time.Second)
	result, err := exec.SyncAndRun(context.Background(), nil)
	if err != nil {
		t.Fatalf("SyncAndRun: %v", err)
	}

	// Result should have no custom key or an empty custom map.
	if custom, ok := result["custom"]; ok {
		if m, isMap := custom.(map[string]any); isMap && len(m) > 0 {
			t.Errorf("expected no custom traits, got %v", m)
		}
	}
}

func TestSyncAndRun_StaleFilesCleanedUp(t *testing.T) {
	cacheDir := t.TempDir()
	dl := newMockDownloader()

	// Create a stale file that is NOT in the manifest.
	stalePath := filepath.Join(cacheDir, "old-trait")
	os.WriteFile(stalePath, makeScript(`{"stale":true}`), 0755)

	// Manifest has only "active-trait".
	activeScript := makeScript(`{"active":true}`)
	dl.data["active-trait"] = activeScript

	exec := NewExecutor(cacheDir, dl, 10*time.Second)
	manifest := []*pb.CustomTraitEntry{
		{Name: "active-trait", Sha256: checksumOf(activeScript)},
	}

	_, err := exec.SyncAndRun(context.Background(), manifest)
	if err != nil {
		t.Fatalf("SyncAndRun: %v", err)
	}

	// Stale file should be removed.
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("stale file 'old-trait' should have been deleted")
	}

	// Active trait file should exist.
	activePath := filepath.Join(cacheDir, "active-trait")
	if _, err := os.Stat(activePath); os.IsNotExist(err) {
		t.Error("active-trait should exist in cache")
	}
}
