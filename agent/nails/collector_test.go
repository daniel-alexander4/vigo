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

func TestTimeCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*TimeCollector)(nil)
}

func TestIdentityCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*IdentityCollector)(nil)
}

func TestVirtualCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*VirtualCollector)(nil)
}

func TestUptimeCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*UptimeCollector)(nil)
}

func TestLoadAvgCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*LoadAvgCollector)(nil)
}

func TestMountpointsCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*MountpointsCollector)(nil)
}

func TestProcessorsCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*ProcessorsCollector)(nil)
}

func TestDMICollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*DMICollector)(nil)
}

func TestCloudCollector_ImplementsCollector(t *testing.T) {
	var _ Collector = (*CloudCollector)(nil)
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

func TestIdentityCollector_ReturnsExpectedKeys(t *testing.T) {
	c := &IdentityCollector{}
	if c.Name() != "identity" {
		t.Errorf("Name() = %q, want %q", c.Name(), "identity")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, key := range []string{"uid", "gid", "user", "group", "privileged"} {
		if _, ok := result[key]; !ok {
			t.Errorf("missing key %q in identity nails", key)
		}
	}

	if _, ok := result["user"].(string); !ok {
		t.Error("user should be a string")
	}
	if _, ok := result["privileged"].(bool); !ok {
		t.Error("privileged should be a bool")
	}
}

func TestVirtualCollector_ReturnsExpectedKeys(t *testing.T) {
	c := &VirtualCollector{}
	if c.Name() != "virtual" {
		t.Errorf("Name() = %q, want %q", c.Name(), "virtual")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, key := range []string{"is_virtual", "system", "role"} {
		if _, ok := result[key]; !ok {
			t.Errorf("missing key %q in virtual nails", key)
		}
	}

	if _, ok := result["is_virtual"].(bool); !ok {
		t.Error("is_virtual should be a bool")
	}
}

func TestUptimeCollector_ReturnsPositiveUptime(t *testing.T) {
	c := &UptimeCollector{}
	if c.Name() != "uptime" {
		t.Errorf("Name() = %q, want %q", c.Name(), "uptime")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sec, ok := result["seconds"].(uint64)
	if !ok {
		t.Fatal("seconds missing or wrong type")
	}
	if sec == 0 {
		t.Error("uptime seconds should be > 0")
	}

	if _, ok := result["boot_time"].(string); !ok {
		t.Error("boot_time should be a string")
	}
}

func TestLoadAvgCollector_ReturnsLoadValues(t *testing.T) {
	c := &LoadAvgCollector{}
	if c.Name() != "loadavg" {
		t.Errorf("Name() = %q, want %q", c.Name(), "loadavg")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, key := range []string{"load1", "load5", "load15"} {
		if _, ok := result[key].(float64); !ok {
			t.Errorf("%s missing or not float64", key)
		}
	}
}

func TestMountpointsCollector_ReturnsAtLeastOneMount(t *testing.T) {
	c := &MountpointsCollector{}
	if c.Name() != "mountpoints" {
		t.Errorf("Name() = %q, want %q", c.Name(), "mountpoints")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mounts, ok := result["mounts"].(map[string]any)
	if !ok {
		t.Fatal("mounts missing or wrong type")
	}
	if len(mounts) == 0 {
		t.Error("should have at least one mount point")
	}
}

func TestProcessorsCollector_ReturnsPositiveCores(t *testing.T) {
	c := &ProcessorsCollector{}
	if c.Name() != "processors" {
		t.Errorf("Name() = %q, want %q", c.Name(), "processors")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logical, ok := result["logical_cores"].(int)
	if !ok {
		t.Fatal("logical_cores missing or wrong type")
	}
	if logical <= 0 {
		t.Errorf("logical_cores = %d, want > 0", logical)
	}
}

func TestDMICollector_ReturnsMap(t *testing.T) {
	c := &DMICollector{}
	if c.Name() != "dmi" {
		t.Errorf("Name() = %q, want %q", c.Name(), "dmi")
	}

	// Should not error even if DMI files are unreadable.
	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Error("result should not be nil")
	}
}

func TestCloudCollector_ReturnsProvider(t *testing.T) {
	c := &CloudCollector{}
	if c.Name() != "cloud" {
		t.Errorf("Name() = %q, want %q", c.Name(), "cloud")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	provider, ok := result["provider"].(string)
	if !ok {
		t.Fatal("provider missing or wrong type")
	}
	if provider == "" {
		t.Error("provider should not be empty")
	}
}

func TestDefaultCollectors_ReturnsAllCollectors(t *testing.T) {
	collectors := DefaultCollectors()
	if len(collectors) != 18 {
		t.Fatalf("DefaultCollectors() returned %d collectors, want 18", len(collectors))
	}

	names := make(map[string]bool)
	for _, c := range collectors {
		names[c.Name()] = true
	}

	expected := []string{
		"os", "hardware", "network", "packages", "time",
		"identity", "virtual", "uptime", "loadavg", "mountpoints",
		"processors", "dmi", "cloud", "filesystem", "kernel",
		"users", "memory", "blockdevices",
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing collector %q", name)
		}
	}
}
