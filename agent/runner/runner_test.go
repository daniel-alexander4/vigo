package runner

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/hobnob/hobnob/agent/modules"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

// mockExecutor records calls and returns canned results.
type mockExecutor struct {
	mu       sync.Mutex
	calls    []mockCall
	results  map[string]modules.ResourceResult // keyed by resource name
	changed  map[string]bool                   // keyed by resource name: whether to mark Changed
	errNames map[string]bool                   // keyed by resource name: whether to return error
}

type mockCall struct {
	Name   string
	Params map[string]string
	DryRun bool
}

func newMockExecutor() *mockExecutor {
	return &mockExecutor{
		results:  map[string]modules.ResourceResult{},
		changed:  map[string]bool{},
		errNames: map[string]bool{},
	}
}

func (m *mockExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) modules.ResourceResult {
	name := params["_resource_name"]
	m.mu.Lock()
	m.calls = append(m.calls, mockCall{Name: name, Params: params, DryRun: dryRun})
	m.mu.Unlock()

	if m.errNames[name] {
		return modules.ResourceResult{
			Action:  modules.ActionNone,
			Changed: false,
			Error:   errTestResource,
		}
	}

	action := modules.ActionUpdated
	changed := m.changed[name]
	if !changed {
		action = modules.ActionNone
	}

	return modules.ResourceResult{
		Action:  action,
		Changed: changed,
	}
}

func (m *mockExecutor) callNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, len(m.calls))
	for i, c := range m.calls {
		names[i] = c.Name
	}
	return names
}

var errTestResource = &testError{msg: "test resource error"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// Helper to build proto modules for testing.
func makeModule(name, resType string, resources []string, opts ...moduleOpt) *pb.Module {
	mod := &pb.Module{
		Name:         name,
		ResourceType: resType,
	}
	for _, resName := range resources {
		mod.Resources = append(mod.Resources, &pb.Resource{
			Name:   resName,
			Type:   resType,
			Params: map[string]string{"_resource_name": resName},
		})
	}
	for _, opt := range opts {
		opt(mod)
	}
	return mod
}

type moduleOpt func(*pb.Module)

func withDependsOn(deps ...string) moduleOpt {
	return func(m *pb.Module) { m.DependsOn = deps }
}

func withNotify(targets ...string) moduleOpt {
	return func(m *pb.Module) { m.Notify = targets }
}

func withSubscribes(targets ...string) moduleOpt {
	return func(m *pb.Module) { m.Subscribes = targets }
}

func TestRunnerTopologicalOrder(t *testing.T) {
	mock := newMockExecutor()
	mock.changed["base"] = true
	mock.changed["app"] = true
	mock.changed["svc"] = true

	mods := []*pb.Module{
		makeModule("svc-mod", "service", []string{"svc"}, withDependsOn("app-mod")),
		makeModule("base-mod", "file", []string{"base"}),
		makeModule("app-mod", "file", []string{"app"}, withDependsOn("base-mod")),
	}

	r := NewRunner(map[string]modules.Executor{"file": mock, "service": mock}, nil, false, "")
	results, err := r.Run(context.Background(), mods)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have 3 results from main pass
	if len(results) < 3 {
		t.Fatalf("expected at least 3 results, got %d", len(results))
	}

	// Verify execution order: base -> app -> svc
	names := mock.callNames()
	baseIdx, appIdx, svcIdx := -1, -1, -1
	for i, n := range names {
		switch n {
		case "base":
			if baseIdx == -1 {
				baseIdx = i
			}
		case "app":
			if appIdx == -1 {
				appIdx = i
			}
		case "svc":
			if svcIdx == -1 {
				svcIdx = i
			}
		}
	}
	if baseIdx >= appIdx {
		t.Errorf("base (%d) should execute before app (%d)", baseIdx, appIdx)
	}
	if appIdx >= svcIdx {
		t.Errorf("app (%d) should execute before svc (%d)", appIdx, svcIdx)
	}
}

func TestRunnerNotifyTrigger(t *testing.T) {
	mock := newMockExecutor()
	mock.changed["config-file"] = true

	mods := []*pb.Module{
		makeModule("config", "file", []string{"config-file"}, withNotify("svc")),
		makeModule("svc", "service", []string{"app-service"}, withDependsOn("config")),
	}

	r := NewRunner(map[string]modules.Executor{"file": mock, "service": mock}, nil, false, "")
	results, err := r.Run(context.Background(), mods)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have: config-file (main), app-service (main), app-service (notify re-execute)
	if len(results) != 3 {
		t.Fatalf("expected 3 results (main pass + notify), got %d", len(results))
	}

	// Mock should have been called 3 times: config-file, app-service, app-service again
	calls := mock.callNames()
	if len(calls) != 3 {
		t.Fatalf("expected 3 executor calls, got %d: %v", len(calls), calls)
	}
	if calls[2] != "app-service" {
		t.Errorf("expected notify re-execution of app-service, got %s", calls[2])
	}
}

func TestRunnerErrorNoNotify(t *testing.T) {
	mock := newMockExecutor()
	mock.errNames["config-file"] = true

	mods := []*pb.Module{
		makeModule("config", "file", []string{"config-file"}, withNotify("svc")),
		makeModule("svc", "service", []string{"app-service"}, withDependsOn("config")),
	}

	r := NewRunner(map[string]modules.Executor{"file": mock, "service": mock}, nil, false, "")
	results, err := r.Run(context.Background(), mods)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have 2 results from main pass only (no notify since config-file errored)
	if len(results) != 2 {
		t.Fatalf("expected 2 results (no notify for errored resource), got %d", len(results))
	}

	// Verify config-file has an error
	for _, res := range results {
		if res.ResourceName == "config-file" {
			if res.Error == nil {
				t.Error("expected error on config-file result")
			}
		}
	}

	// Mock called twice: config-file (errored), app-service (main pass only)
	calls := mock.callNames()
	if len(calls) != 2 {
		t.Errorf("expected 2 calls (no notify), got %d: %v", len(calls), calls)
	}
}

func TestRunnerDryRun(t *testing.T) {
	mock := newMockExecutor()
	mock.changed["config-file"] = true

	mods := []*pb.Module{
		makeModule("config", "file", []string{"config-file"}, withNotify("svc")),
		makeModule("svc", "service", []string{"app-service"}, withDependsOn("config")),
	}

	r := NewRunner(map[string]modules.Executor{"file": mock, "service": mock}, nil, true, "")
	results, err := r.Run(context.Background(), mods)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Dry-run: main pass only, no notify pass
	if len(results) != 2 {
		t.Fatalf("expected 2 results (dry-run, no notify), got %d", len(results))
	}

	// All calls should have dryRun=true
	for _, call := range mock.calls {
		if !call.DryRun {
			t.Errorf("expected dryRun=true for call %s", call.Name)
		}
	}
}

func TestRunnerModuleFilter(t *testing.T) {
	mock := newMockExecutor()

	mods := []*pb.Module{
		makeModule("base", "file", []string{"base-res"}),
		makeModule("app", "file", []string{"app-res"}, withDependsOn("base")),
		makeModule("svc", "service", []string{"svc-res"}, withDependsOn("app")),
	}

	r := NewRunner(map[string]modules.Executor{"file": mock, "service": mock}, nil, false, "app")
	results, err := r.Run(context.Background(), mods)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Only app module's resources should be executed
	if len(results) != 1 {
		t.Fatalf("expected 1 result for module filter, got %d", len(results))
	}
	if results[0].ResourceName != "app-res" {
		t.Errorf("expected app-res, got %s", results[0].ResourceName)
	}
}

func TestRunnerUnknownResourceType(t *testing.T) {
	mock := newMockExecutor()

	mods := []*pb.Module{
		makeModule("base", "file", []string{"base-res"}),
		makeModule("mystery", "unknown_type", []string{"mystery-res"}),
	}

	r := NewRunner(map[string]modules.Executor{"file": mock}, nil, false, "")
	results, err := r.Run(context.Background(), mods)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Find the mystery resource result
	for _, res := range results {
		if res.ResourceName == "mystery-res" {
			if res.Error == nil {
				t.Error("expected error for unknown resource type")
			}
			if !strings.Contains(res.Error.Error(), "unknown_type") {
				t.Errorf("error should mention the unknown type, got: %v", res.Error)
			}
		}
	}
}

func TestRunnerSubscribesEndToEnd(t *testing.T) {
	mock := newMockExecutor()
	mock.changed["config-file"] = true

	mods := []*pb.Module{
		makeModule("config", "file", []string{"config-file"}),
		makeModule("svc", "service", []string{"app-service"}, withDependsOn("config"), withSubscribes("config")),
	}

	r := NewRunner(map[string]modules.Executor{"file": mock, "service": mock}, nil, false, "")
	results, err := r.Run(context.Background(), mods)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// config-file changed, svc subscribes to config -> svc should be notified
	// Main pass: config-file, app-service
	// Notify pass: app-service re-executed
	if len(results) != 3 {
		t.Fatalf("expected 3 results (main + notify via subscribes), got %d", len(results))
	}

	calls := mock.callNames()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d: %v", len(calls), calls)
	}
	if calls[2] != "app-service" {
		t.Errorf("expected notify re-execution of app-service, got %s", calls[2])
	}
}

func TestRunnerNoDepsDeclarationOrder(t *testing.T) {
	mock := newMockExecutor()

	// No depends_on, notify, or subscribes — should execute in declaration order
	// without topological sort overhead.
	mods := []*pb.Module{
		makeModule("alpha", "file", []string{"alpha-res"}),
		makeModule("beta", "file", []string{"beta-res"}),
		makeModule("gamma", "file", []string{"gamma-res"}),
	}

	r := NewRunner(map[string]modules.Executor{"file": mock}, nil, false, "")
	results, err := r.Run(context.Background(), mods)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Verify declaration order preserved
	calls := mock.callNames()
	if calls[0] != "alpha-res" || calls[1] != "beta-res" || calls[2] != "gamma-res" {
		t.Errorf("expected declaration order [alpha-res, beta-res, gamma-res], got %v", calls)
	}
}

func TestRunnerContinuesOnError(t *testing.T) {
	mock := newMockExecutor()
	mock.errNames["first-res"] = true

	mods := []*pb.Module{
		makeModule("first", "file", []string{"first-res"}),
		makeModule("second", "file", []string{"second-res"}),
	}

	r := NewRunner(map[string]modules.Executor{"file": mock}, nil, false, "")
	results, err := r.Run(context.Background(), mods)
	if err != nil {
		t.Fatalf("Run should not return error for individual resource failure: %v", err)
	}

	// Both resources should have results
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// First errored, second should still have executed
	calls := mock.callNames()
	if len(calls) != 2 {
		t.Errorf("expected 2 calls (continues past error), got %d: %v", len(calls), calls)
	}
}

// --- When builtin tests ---

func TestEvaluateAgentWhen_OsFamily(t *testing.T) {
	nails := map[string]any{
		"os": map[string]any{"family": "debian", "distro": "ubuntu", "version": "22.04", "arch": "amd64"},
	}

	tests := []struct {
		expr string
		want bool
	}{
		{"os_family('debian')", true},
		{"os_family('Debian')", true},
		{"os_family('rhel')", false},
	}
	for _, tt := range tests {
		got, err := evaluateAgentWhen(context.Background(), tt.expr, nails)
		if err != nil {
			t.Fatalf("evaluateAgentWhen(%q) error: %v", tt.expr, err)
		}
		if got != tt.want {
			t.Errorf("evaluateAgentWhen(%q) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestEvaluateAgentWhen_Distro(t *testing.T) {
	nails := map[string]any{
		"os": map[string]any{"distro": "ubuntu"},
	}

	tests := []struct {
		expr string
		want bool
	}{
		{"distro('ubuntu')", true},
		{"distro('Ubuntu')", true},
		{"distro('centos')", false},
	}
	for _, tt := range tests {
		got, err := evaluateAgentWhen(context.Background(), tt.expr, nails)
		if err != nil {
			t.Fatalf("evaluateAgentWhen(%q) error: %v", tt.expr, err)
		}
		if got != tt.want {
			t.Errorf("evaluateAgentWhen(%q) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestEvaluateAgentWhen_Arch(t *testing.T) {
	nails := map[string]any{
		"os": map[string]any{"arch": "amd64"},
	}

	tests := []struct {
		expr string
		want bool
	}{
		{"arch('amd64')", true},
		{"arch('AMD64')", true},
		{"arch('arm64')", false},
	}
	for _, tt := range tests {
		got, err := evaluateAgentWhen(context.Background(), tt.expr, nails)
		if err != nil {
			t.Fatalf("evaluateAgentWhen(%q) error: %v", tt.expr, err)
		}
		if got != tt.want {
			t.Errorf("evaluateAgentWhen(%q) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestEvaluateAgentWhen_VersionGe(t *testing.T) {
	nails := map[string]any{
		"os": map[string]any{"version": "22.04"},
	}

	tests := []struct {
		expr string
		want bool
	}{
		{"version_ge('20.04')", true},
		{"version_ge('22.04')", true},  // equal
		{"version_ge('22.10')", false}, // 04 < 10
		{"version_ge('24.04')", false},
	}
	for _, tt := range tests {
		got, err := evaluateAgentWhen(context.Background(), tt.expr, nails)
		if err != nil {
			t.Fatalf("evaluateAgentWhen(%q) error: %v", tt.expr, err)
		}
		if got != tt.want {
			t.Errorf("evaluateAgentWhen(%q) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestCompareVersionGE(t *testing.T) {
	tests := []struct {
		actual, required string
		want             bool
	}{
		{"22.04", "20.04", true},
		{"22.04", "22.04", true},
		{"22.04", "22.10", false},
		{"22.04", "24.04", false},
		{"9", "8", true},
		{"8", "9", false},
		{"1.2.3", "1.2.3", true},
		{"1.2.4", "1.2.3", true},
		{"1.2.2", "1.2.3", false},
		{"", "1.0", false},
		{"1.0", "1.0.0", true},
	}
	for _, tt := range tests {
		got := compareVersionGE(tt.actual, tt.required)
		if got != tt.want {
			t.Errorf("compareVersionGE(%q, %q) = %v, want %v", tt.actual, tt.required, got, tt.want)
		}
	}
}

func TestEvaluateAgentWhen_InGroup(t *testing.T) {
	nails := map[string]any{
		"identity": map[string]any{
			"user":  "testuser",
			"group": "staff",
			"groups": []string{"staff", "docker", "sudo"},
		},
	}

	tests := []struct {
		expr string
		want bool
	}{
		{"in_group('staff')", true},   // primary group
		{"in_group('docker')", true},  // supplementary
		{"in_group('sudo')", true},    // supplementary
		{"in_group('wheel')", false},  // not a member
	}
	for _, tt := range tests {
		got, err := evaluateAgentWhen(context.Background(), tt.expr, nails)
		if err != nil {
			t.Fatalf("evaluateAgentWhen(%q) error: %v", tt.expr, err)
		}
		if got != tt.want {
			t.Errorf("evaluateAgentWhen(%q) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestEvaluateAgentWhen_CombinedNailsBuiltins(t *testing.T) {
	nails := map[string]any{
		"os": map[string]any{"family": "debian", "arch": "amd64", "version": "22.04"},
	}

	tests := []struct {
		expr string
		want bool
	}{
		{"os_family('debian') and arch('amd64')", true},
		{"os_family('debian') and arch('arm64')", false},
		{"os_family('rhel') or arch('amd64')", true},
		{"not os_family('rhel')", true},
		{"os_family('debian') and version_ge('20.04')", true},
	}
	for _, tt := range tests {
		got, err := evaluateAgentWhen(context.Background(), tt.expr, nails)
		if err != nil {
			t.Fatalf("evaluateAgentWhen(%q) error: %v", tt.expr, err)
		}
		if got != tt.want {
			t.Errorf("evaluateAgentWhen(%q) = %v, want %v", tt.expr, got, tt.want)
		}
	}
}

func TestEvaluateAgentWhen_NilNails(t *testing.T) {
	// Should not panic with nil nails.
	got, err := evaluateAgentWhen(context.Background(), "os_family('debian')", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected false with nil nails")
	}
}

func TestNailStr(t *testing.T) {
	nails := map[string]any{
		"os": map[string]any{"family": "debian"},
	}
	if got := nailStr(nails, "os", "family"); got != "debian" {
		t.Errorf("nailStr(os, family) = %q, want debian", got)
	}
	if got := nailStr(nails, "os", "missing"); got != "" {
		t.Errorf("nailStr(os, missing) = %q, want empty", got)
	}
	if got := nailStr(nails, "missing", "family"); got != "" {
		t.Errorf("nailStr(missing, family) = %q, want empty", got)
	}
	if got := nailStr(nil, "os", "family"); got != "" {
		t.Errorf("nailStr(nil) = %q, want empty", got)
	}
}
