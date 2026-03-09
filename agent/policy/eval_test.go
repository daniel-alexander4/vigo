package policy

import (
	"encoding/json"
	"testing"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

func mustMarshalVars(t *testing.T, vars map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(vars)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEvalBundle_BasicModule(t *testing.T) {
	bundle := &pb.PolicyBundle{
		PolicyVersion: 1,
		Modules: []*pb.PolicyModule{
			{
				Name: "nginx",
				Resources: []*pb.PolicyResource{
					{
						Name: "nginx-pkg",
						Type: "package",
						Attributes: map[string]string{
							"package": "nginx",
							"state":   "present",
						},
					},
				},
			},
		},
	}

	modules, err := EvalBundle(bundle, nil)
	if err != nil {
		t.Fatalf("EvalBundle: %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("expected 1 module, got %d", len(modules))
	}
	if modules[0].Name != "nginx" {
		t.Errorf("module name = %q, want %q", modules[0].Name, "nginx")
	}
	if len(modules[0].Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(modules[0].Resources))
	}
	if modules[0].Resources[0].Params["package"] != "nginx" {
		t.Errorf("package param = %q, want %q", modules[0].Resources[0].Params["package"], "nginx")
	}
}

func TestEvalBundle_TemplateRendering(t *testing.T) {
	bundle := &pb.PolicyBundle{
		PolicyVersion: 1,
		VarsJson:      mustMarshalVars(t, map[string]any{"app_name": "myapp"}),
		VarOrder:      []string{"app_name"},
		Modules: []*pb.PolicyModule{
			{
				Name: "app-config",
				Resources: []*pb.PolicyResource{
					{
						Name: "config-file",
						Type: "file",
						Attributes: map[string]string{
							"targetpath": "/etc/{{ .Vars.app_name }}/config.yaml",
							"content":    "hostname={{ .Traits.hostname }}",
						},
					},
				},
			},
		},
	}

	nails := map[string]any{"hostname": "web-01.example.com"}
	modules, err := EvalBundle(bundle, nails)
	if err != nil {
		t.Fatalf("EvalBundle: %v", err)
	}
	if len(modules) != 1 || len(modules[0].Resources) != 1 {
		t.Fatal("expected 1 module with 1 resource")
	}

	params := modules[0].Resources[0].Params
	if params["targetpath"] != "/etc/myapp/config.yaml" {
		t.Errorf("targetpath = %q, want %q", params["targetpath"], "/etc/myapp/config.yaml")
	}
	if params["content"] != "hostname=web-01.example.com" {
		t.Errorf("content = %q, want %q", params["content"], "hostname=web-01.example.com")
	}
}

func TestEvalBundle_ConditionalVars(t *testing.T) {
	bundle := &pb.PolicyBundle{
		PolicyVersion: 1,
		ConditionalVars: []*pb.ConditionalVarDef{
			{
				Name:        "is_debian",
				Nail:       "os.distro",
				Operator:    "in",
				OperandJson: `["debian", "ubuntu"]`,
			},
		},
		Modules: []*pb.PolicyModule{
			{
				Name: "apt-config",
				When: "is_debian",
				Resources: []*pb.PolicyResource{
					{
						Name:       "apt-conf",
						Type:       "file",
						Attributes: map[string]string{"targetpath": "/etc/apt/apt.conf"},
					},
				},
			},
		},
	}

	// Debian nails → module included.
	nails := map[string]any{"os": map[string]any{"distro": "debian"}}
	modules, err := EvalBundle(bundle, nails)
	if err != nil {
		t.Fatalf("EvalBundle (debian): %v", err)
	}
	if len(modules) != 1 {
		t.Fatalf("expected 1 module for debian, got %d", len(modules))
	}

	// CentOS nails → module excluded.
	nails = map[string]any{"os": map[string]any{"distro": "centos"}}
	modules, err = EvalBundle(bundle, nails)
	if err != nil {
		t.Fatalf("EvalBundle (centos): %v", err)
	}
	if len(modules) != 0 {
		t.Errorf("expected 0 modules for centos, got %d", len(modules))
	}
}

func TestEvalBundle_ResourceWhenFilter(t *testing.T) {
	bundle := &pb.PolicyBundle{
		PolicyVersion: 1,
		VarsJson:      mustMarshalVars(t, map[string]any{"enable_ssl": "true"}),
		VarOrder:      []string{"enable_ssl"},
		Modules: []*pb.PolicyModule{
			{
				Name: "web",
				Resources: []*pb.PolicyResource{
					{
						Name:       "base-config",
						Type:       "file",
						Attributes: map[string]string{"targetpath": "/etc/web.conf"},
					},
					{
						Name:       "ssl-config",
						Type:       "file",
						When:       "enable_ssl",
						Attributes: map[string]string{"targetpath": "/etc/ssl.conf"},
					},
				},
			},
		},
	}

	modules, err := EvalBundle(bundle, nil)
	if err != nil {
		t.Fatalf("EvalBundle: %v", err)
	}
	if len(modules) != 1 {
		t.Fatal("expected 1 module")
	}
	if len(modules[0].Resources) != 2 {
		t.Fatalf("expected 2 resources (ssl enabled), got %d", len(modules[0].Resources))
	}

	// Disable SSL.
	bundle.VarsJson = mustMarshalVars(t, map[string]any{"enable_ssl": "false"})
	modules, err = EvalBundle(bundle, nil)
	if err != nil {
		t.Fatalf("EvalBundle: %v", err)
	}
	if len(modules) != 1 {
		t.Fatal("expected 1 module")
	}
	if len(modules[0].Resources) != 1 {
		t.Fatalf("expected 1 resource (ssl disabled), got %d", len(modules[0].Resources))
	}
	if modules[0].Resources[0].Name != "base-config" {
		t.Errorf("expected base-config, got %s", modules[0].Resources[0].Name)
	}
}

func TestEvalBundle_DependsOnAndNotify(t *testing.T) {
	bundle := &pb.PolicyBundle{
		PolicyVersion: 1,
		Modules: []*pb.PolicyModule{
			{
				Name: "config",
				Resources: []*pb.PolicyResource{
					{
						Name:       "app-config",
						Type:       "file",
						Attributes: map[string]string{"targetpath": "/etc/app.conf"},
						Notify:     []string{"svc"},
					},
				},
			},
			{
				Name:      "svc",
				DependsOn: []string{"config"},
				Resources: []*pb.PolicyResource{
					{
						Name:       "app-svc",
						Type:       "service",
						Attributes: map[string]string{"name": "app"},
					},
				},
			},
		},
	}

	modules, err := EvalBundle(bundle, nil)
	if err != nil {
		t.Fatalf("EvalBundle: %v", err)
	}
	if len(modules) != 2 {
		t.Fatalf("expected 2 modules, got %d", len(modules))
	}

	// Verify depends_on and notify propagated.
	if len(modules[1].DependsOn) != 1 || modules[1].DependsOn[0] != "config" {
		t.Errorf("svc depends_on = %v, want [config]", modules[1].DependsOn)
	}
	if len(modules[0].Notify) != 1 || modules[0].Notify[0] != "svc" {
		t.Errorf("config notify = %v, want [svc]", modules[0].Notify)
	}
}

func TestEvalBundle_VarTemplateChaining(t *testing.T) {
	// Later vars can reference earlier vars.
	bundle := &pb.PolicyBundle{
		PolicyVersion: 1,
		VarsJson:      mustMarshalVars(t, map[string]any{"host": "web-01", "fqdn": "{{ .Vars.host }}.example.com"}),
		VarOrder:      []string{"host", "fqdn"},
		Modules: []*pb.PolicyModule{
			{
				Name: "dns",
				Resources: []*pb.PolicyResource{
					{
						Name:       "hosts-entry",
						Type:       "file",
						Attributes: map[string]string{"content": "{{ .Vars.fqdn }}"},
					},
				},
			},
		},
	}

	modules, err := EvalBundle(bundle, nil)
	if err != nil {
		t.Fatalf("EvalBundle: %v", err)
	}
	if modules[0].Resources[0].Params["content"] != "web-01.example.com" {
		t.Errorf("content = %q, want %q", modules[0].Resources[0].Params["content"], "web-01.example.com")
	}
}

func TestEvalBundle_EmptyBundle(t *testing.T) {
	modules, err := EvalBundle(nil, nil)
	if err != nil {
		t.Fatalf("EvalBundle nil: %v", err)
	}
	if len(modules) != 0 {
		t.Errorf("expected 0 modules, got %d", len(modules))
	}
}

func TestEvalBundle_AgentSideWhen(t *testing.T) {
	// When expression with builtins should pass through to agent.
	bundle := &pb.PolicyBundle{
		PolicyVersion: 1,
		VarsJson:      mustMarshalVars(t, map[string]any{"enabled": "true"}),
		VarOrder:      []string{"enabled"},
		Modules: []*pb.PolicyModule{
			{
				Name: "app",
				Resources: []*pb.PolicyResource{
					{
						Name:       "guarded-file",
						Type:       "file",
						When:       "enabled and file_exists('/etc/app')",
						Attributes: map[string]string{"targetpath": "/etc/app/config"},
					},
				},
			},
		},
	}

	modules, err := EvalBundle(bundle, nil)
	if err != nil {
		t.Fatalf("EvalBundle: %v", err)
	}
	if len(modules) != 1 || len(modules[0].Resources) != 1 {
		t.Fatal("expected 1 module with 1 resource")
	}
	// The file_exists builtin should be passed to agent-side when.
	if modules[0].Resources[0].When == "" {
		t.Error("expected agent-side when expression for builtin")
	}
}
