package runner

import (
	"strings"
	"testing"

	pb "github.com/isogov/isogov/gen/proto/isogov"
)

func TestTopoSortNoDeps(t *testing.T) {
	nodes := []ResourceNode{
		{Name: "a", Type: "file"},
		{Name: "b", Type: "package"},
		{Name: "c", Type: "service"},
	}

	sorted, err := TopoSort(nodes)
	if err != nil {
		t.Fatalf("TopoSort no deps: %v", err)
	}
	if len(sorted) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(sorted))
	}
	// With no deps, Kahn's algorithm processes zero-in-degree nodes in order
	if sorted[0].Name != "a" || sorted[1].Name != "b" || sorted[2].Name != "c" {
		t.Errorf("expected [a, b, c], got [%s, %s, %s]", sorted[0].Name, sorted[1].Name, sorted[2].Name)
	}
}

func TestTopoSortLinearChain(t *testing.T) {
	// A -> B -> C (C depends on B, B depends on A)
	nodes := []ResourceNode{
		{Name: "c", Type: "file", DependsOn: []string{"b"}},
		{Name: "a", Type: "file"},
		{Name: "b", Type: "file", DependsOn: []string{"a"}},
	}

	sorted, err := TopoSort(nodes)
	if err != nil {
		t.Fatalf("TopoSort linear: %v", err)
	}
	if len(sorted) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(sorted))
	}
	// Must be a -> b -> c
	names := make([]string, len(sorted))
	for i, n := range sorted {
		names[i] = n.Name
	}
	if names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Errorf("expected [a, b, c], got %v", names)
	}
}

func TestTopoSortDiamond(t *testing.T) {
	// A -> B, A -> C, B -> D, C -> D
	nodes := []ResourceNode{
		{Name: "d", Type: "file", DependsOn: []string{"b", "c"}},
		{Name: "b", Type: "file", DependsOn: []string{"a"}},
		{Name: "c", Type: "file", DependsOn: []string{"a"}},
		{Name: "a", Type: "file"},
	}

	sorted, err := TopoSort(nodes)
	if err != nil {
		t.Fatalf("TopoSort diamond: %v", err)
	}
	if len(sorted) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(sorted))
	}

	// Verify topological ordering constraints
	indexOf := map[string]int{}
	for i, n := range sorted {
		indexOf[n.Name] = i
	}
	if indexOf["a"] >= indexOf["b"] {
		t.Error("a must come before b")
	}
	if indexOf["a"] >= indexOf["c"] {
		t.Error("a must come before c")
	}
	if indexOf["b"] >= indexOf["d"] {
		t.Error("b must come before d")
	}
	if indexOf["c"] >= indexOf["d"] {
		t.Error("c must come before d")
	}
}

func TestTopoSortCircularDependency(t *testing.T) {
	// A -> B -> C -> A
	nodes := []ResourceNode{
		{Name: "a", Type: "file", DependsOn: []string{"c"}},
		{Name: "b", Type: "file", DependsOn: []string{"a"}},
		{Name: "c", Type: "file", DependsOn: []string{"b"}},
	}

	_, err := TopoSort(nodes)
	if err == nil {
		t.Fatal("expected error for circular dependency")
	}
	if !strings.Contains(err.Error(), "circular dependency") {
		t.Errorf("error should mention 'circular dependency', got: %v", err)
	}
	// Should name the participating resources
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") || !strings.Contains(err.Error(), "c") {
		t.Errorf("error should name cycle participants (a, b, c), got: %v", err)
	}
}

func TestTopoSortUnknownDependency(t *testing.T) {
	nodes := []ResourceNode{
		{Name: "a", Type: "file", DependsOn: []string{"x"}},
	}

	_, err := TopoSort(nodes)
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
	if !strings.Contains(err.Error(), "unknown resource") {
		t.Errorf("error should mention 'unknown resource', got: %v", err)
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("error should name the missing dependency 'x', got: %v", err)
	}
}

func TestBuildDAGSubscribesNormalization(t *testing.T) {
	// Module B subscribes to module A -> A's notify list should get B added
	modules := []*pb.Module{
		{
			Name:         "a",
			ResourceType: "file",
			Resources: []*pb.Resource{
				{Name: "config-file", Type: "file", Params: map[string]string{"path": "/etc/app.conf"}},
			},
		},
		{
			Name:         "b",
			ResourceType: "service",
			Resources: []*pb.Resource{
				{Name: "app-service", Type: "service", Params: map[string]string{"name": "app"}},
			},
			Subscribes: []string{"a"},
			DependsOn:  []string{"a"},
		},
	}

	nodes, notifyMap, err := BuildDAG(modules)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	// Should have 2 resource nodes
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}

	// The notify map should show that A notifies B's resource
	// Find A's resource name
	var aResourceName string
	var bResourceName string
	for _, n := range nodes {
		if n.ModuleName == "a" {
			aResourceName = n.Name
		}
		if n.ModuleName == "b" {
			bResourceName = n.Name
		}
	}

	targets, ok := notifyMap[aResourceName]
	if !ok {
		t.Fatalf("expected notify map entry for %q, got map: %v", aResourceName, notifyMap)
	}
	found := false
	for _, target := range targets {
		if target == bResourceName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %q in notify targets for %q, got %v", bResourceName, aResourceName, targets)
	}
}

func TestBuildDAGModuleLevelMetadata(t *testing.T) {
	modules := []*pb.Module{
		{
			Name:         "base",
			ResourceType: "file",
			Resources: []*pb.Resource{
				{Name: "base-config", Type: "file", Params: map[string]string{"path": "/etc/base.conf"}},
			},
		},
		{
			Name:         "app",
			ResourceType: "file",
			DependsOn:    []string{"base"},
			When:         `{{ eq .Traits.os.family "linux" }}`,
			Resources: []*pb.Resource{
				{Name: "app-config", Type: "file", Params: map[string]string{"path": "/etc/app.conf"}},
			},
			Notify: []string{"svc"},
		},
		{
			Name:         "svc",
			ResourceType: "service",
			DependsOn:    []string{"app"},
			Resources: []*pb.Resource{
				{Name: "app-svc", Type: "service", Params: map[string]string{"name": "app"}},
			},
		},
	}

	nodes, notifyMap, err := BuildDAG(modules)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}

	// Find the app node and check its when condition
	for _, n := range nodes {
		if n.ModuleName == "app" {
			if n.When != `{{ eq .Traits.os.family "linux" }}` {
				t.Errorf("expected when condition on app node, got %q", n.When)
			}
			if len(n.DependsOn) == 0 {
				t.Error("expected app to depend on base")
			}
		}
	}

	// Check notify map: app's resource should notify svc's resource
	var appResource string
	var svcResource string
	for _, n := range nodes {
		if n.ModuleName == "app" {
			appResource = n.Name
		}
		if n.ModuleName == "svc" {
			svcResource = n.Name
		}
	}

	targets, ok := notifyMap[appResource]
	if !ok {
		t.Fatalf("expected notify entry for %q", appResource)
	}
	found := false
	for _, target := range targets {
		if target == svcResource {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %q in notify targets for %q, got %v", svcResource, appResource, targets)
	}
}

func TestBuildDAGMultipleResourcesPerModule(t *testing.T) {
	modules := []*pb.Module{
		{
			Name:         "configs",
			ResourceType: "file",
			Resources: []*pb.Resource{
				{Name: "nginx-conf", Type: "file", Params: map[string]string{"path": "/etc/nginx/nginx.conf"}},
				{Name: "app-conf", Type: "file", Params: map[string]string{"path": "/etc/app/config.yaml"}},
			},
		},
	}

	nodes, _, err := BuildDAG(modules)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	for _, n := range nodes {
		if n.ModuleName != "configs" {
			t.Errorf("expected module name 'configs', got %q", n.ModuleName)
		}
	}
}

func TestBuildDAGEmptyModules(t *testing.T) {
	nodes, notifyMap, err := BuildDAG(nil)
	if err != nil {
		t.Fatalf("BuildDAG nil: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(nodes))
	}
	if len(notifyMap) != 0 {
		t.Errorf("expected empty notify map, got %v", notifyMap)
	}
}
