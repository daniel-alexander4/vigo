package nails

import (
	"context"
	"errors"
	"testing"
)

// --- Mock collectors for unit tests ---

type mockCollector struct {
	name   string
	result map[string]any
	err    error
}

func (m *mockCollector) Name() string { return m.name }
func (m *mockCollector) Collect(_ context.Context) (map[string]any, error) {
	return m.result, m.err
}

// --- CollectAll tests ---

func TestCollectAll_AggregatesMultipleCollectors(t *testing.T) {
	collectors := []Collector{
		&mockCollector{name: "alpha", result: map[string]any{"key1": "val1"}},
		&mockCollector{name: "beta", result: map[string]any{"key2": 42}},
	}

	result, err := CollectAll(context.Background(), collectors)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Result should be nested: {"alpha": {"key1": "val1"}, "beta": {"key2": 42}}
	if len(result) != 2 {
		t.Fatalf("expected 2 top-level keys, got %d", len(result))
	}

	alpha, ok := result["alpha"].(map[string]any)
	if !ok {
		t.Fatal("alpha key missing or wrong type")
	}
	if alpha["key1"] != "val1" {
		t.Errorf("alpha.key1 = %v, want val1", alpha["key1"])
	}

	beta, ok := result["beta"].(map[string]any)
	if !ok {
		t.Fatal("beta key missing or wrong type")
	}
	if beta["key2"] != 42 {
		t.Errorf("beta.key2 = %v, want 42", beta["key2"])
	}
}

func TestCollectAll_ContinuesOnCollectorError(t *testing.T) {
	collectors := []Collector{
		&mockCollector{name: "good", result: map[string]any{"ok": true}},
		&mockCollector{name: "bad", err: errors.New("collection failed")},
		&mockCollector{name: "also_good", result: map[string]any{"fine": true}},
	}

	result, err := CollectAll(context.Background(), collectors)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "good" and "also_good" should be present, "bad" should be skipped
	if len(result) != 2 {
		t.Fatalf("expected 2 top-level keys, got %d: %v", len(result), result)
	}
	if _, ok := result["good"]; !ok {
		t.Error("expected 'good' key in result")
	}
	if _, ok := result["also_good"]; !ok {
		t.Error("expected 'also_good' key in result")
	}
	if _, ok := result["bad"]; ok {
		t.Error("'bad' collector should not be in result")
	}
}

func TestCollectAll_EmptyCollectors(t *testing.T) {
	result, err := CollectAll(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected empty map, got %v", result)
	}
}

// --- Interface compliance tests ---

func TestOSCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*OSCollector)(nil)
}

func TestHardwareCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*HardwareCollector)(nil)
}

func TestNetworkCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*NetworkCollector)(nil)
}

func TestPackageCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*PackageCollector)(nil)
}

// --- Live collector tests (run on actual host) ---

func TestOSCollector_ReturnsExpectedKeys(t *testing.T) {
	c := &OSCollector{}
	if c.Name() != "os" {
		t.Errorf("Name() = %q, want %q", c.Name(), "os")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedKeys := []string{"family", "distro", "version", "kernel", "arch"}
	for _, key := range expectedKeys {
		if _, ok := result[key]; !ok {
			t.Errorf("missing key %q in OS nails", key)
		}
	}

	// arch should be non-empty
	arch, _ := result["arch"].(string)
	if arch == "" {
		t.Error("arch should not be empty")
	}
}

func TestHardwareCollector_ReturnsPositiveValues(t *testing.T) {
	c := &HardwareCollector{}
	if c.Name() != "hardware" {
		t.Errorf("Name() = %q, want %q", c.Name(), "hardware")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cpuCount, ok := result["cpu_count"].(int)
	if !ok {
		t.Fatal("cpu_count missing or wrong type")
	}
	if cpuCount <= 0 {
		t.Errorf("cpu_count = %d, want > 0", cpuCount)
	}

	memMB, ok := result["memory_mb"].(uint64)
	if !ok {
		t.Fatal("memory_mb missing or wrong type")
	}
	if memMB <= 0 {
		t.Errorf("memory_mb = %d, want > 0", memMB)
	}

	diskGB, ok := result["disk_gb"].(map[string]uint64)
	if !ok {
		t.Fatal("disk_gb missing or wrong type")
	}
	if len(diskGB) == 0 {
		t.Error("disk_gb should have at least one mount point")
	}
}

func TestNetworkCollector_ReturnsNonEmptyHostname(t *testing.T) {
	c := &NetworkCollector{}
	if c.Name() != "network" {
		t.Errorf("Name() = %q, want %q", c.Name(), "network")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hostname, ok := result["hostname"].(string)
	if !ok {
		t.Fatal("hostname missing or wrong type")
	}
	if hostname == "" {
		t.Error("hostname should not be empty")
	}

	// ip_addresses should be a slice
	if _, ok := result["ip_addresses"].([]string); !ok {
		t.Error("ip_addresses missing or wrong type (should be []string)")
	}

	// mac_addresses should be a slice
	if _, ok := result["mac_addresses"].([]string); !ok {
		t.Error("mac_addresses missing or wrong type (should be []string)")
	}
}

func TestPackageCollector_ReturnsInstalledMap(t *testing.T) {
	c := &PackageCollector{}
	if c.Name() != "packages" {
		t.Errorf("Name() = %q, want %q", c.Name(), "packages")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have an "installed" key with a map
	installed, ok := result["installed"].(map[string]string)
	if !ok {
		t.Fatal("installed key missing or wrong type (should be map[string]string)")
	}
	// installed may be empty on unsupported platforms -- that's fine
	_ = installed
}

func TestDefaultCollectors_ReturnsFiveCollectors(t *testing.T) {
	collectors := DefaultCollectors()
	if len(collectors) != 5 {
		t.Fatalf("DefaultCollectors() returned %d collectors, want 5", len(collectors))
	}

	names := make(map[string]bool)
	for _, c := range collectors {
		names[c.Name()] = true
	}

	for _, expected := range []string{"os", "hardware", "network", "packages", "time"} {
		if !names[expected] {
			t.Errorf("missing collector %q", expected)
		}
	}
}
