package config

import (
	"strings"
	"testing"
)

func TestParseModuleFile_BasicModule(t *testing.T) {
	data := []byte(`
name: nginx
vars:
  port: "80"
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
  - name: nginx-service
    type: service
    service: nginx
    state: running
    subscribes: [nginx-conf]
`)

	def, err := ParseModuleFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if def.Name != "nginx" {
		t.Errorf("name = %q, want nginx", def.Name)
	}
	if len(def.Resources) != 2 {
		t.Fatalf("len(resources) = %d, want 2", len(def.Resources))
	}

	r0 := def.Resources[0]
	if r0.Name != "install-nginx" || r0.Type != "package" {
		t.Errorf("resource 0: name=%q type=%q", r0.Name, r0.Type)
	}
	if r0.Attributes["package"] != "nginx" {
		t.Errorf("resource 0: package=%v, want nginx", r0.Attributes["package"])
	}

	r1 := def.Resources[1]
	if r1.Name != "nginx-service" || r1.Type != "service" {
		t.Errorf("resource 1: name=%q type=%q", r1.Name, r1.Type)
	}
	if len(r1.Subscribes) != 1 || r1.Subscribes[0] != "nginx-conf" {
		t.Errorf("resource 1: subscribes=%v, want [nginx-conf]", r1.Subscribes)
	}
}

func TestParseModuleFile_DuplicateKeyRejected(t *testing.T) {
	data := []byte(`
name: docker
resources:
  - name: docker-repo
    type: repository
    name: docker
    key_url: https://example.com/key.gpg
    state: present
`)

	_, err := ParseModuleFile(data)
	if err == nil {
		t.Fatal("expected error for duplicate key, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("error should mention duplicate key, got: %v", err)
	}
}

func TestParseModuleFile_ExecWithGuards(t *testing.T) {
	data := []byte(`
name: setup
resources:
  - name: download-key
    type: exec
    command: "curl -fsSL https://example.com/key | gpg --dearmor -o /etc/key.gpg"
    unless: "test -f /etc/key.gpg"
    depends_on: [some-dep]
    notify: [some-service]
`)

	def, err := ParseModuleFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := def.Resources[0]
	if r.Name != "download-key" {
		t.Errorf("name = %q", r.Name)
	}
	if r.Attributes["unless"] != "test -f /etc/key.gpg" {
		t.Errorf("unless = %v", r.Attributes["unless"])
	}
	if len(r.DependsOn) != 1 || r.DependsOn[0] != "some-dep" {
		t.Errorf("depends_on = %v", r.DependsOn)
	}
	if len(r.Notify) != 1 || r.Notify[0] != "some-service" {
		t.Errorf("notify = %v", r.Notify)
	}
}

func TestParseModuleFile_BeforeAtModuleLevel(t *testing.T) {
	data := []byte(`
name: base-packages
before: [app-config]
resources:
  - name: install-curl
    type: package
    package: curl
    state: present
`)

	def, err := ParseModuleFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if def.Name != "base-packages" {
		t.Errorf("name = %q, want base-packages", def.Name)
	}
	if len(def.Before) != 1 || def.Before[0] != "app-config" {
		t.Errorf("before = %v, want [app-config]", def.Before)
	}
}

func TestParseModuleFile_BeforeAtResourceLevel(t *testing.T) {
	data := []byte(`
name: setup
resources:
  - name: create-dir
    type: exec
    command: "mkdir -p /opt/app"
    creates: /opt/app
    before: [install-app]
  - name: install-app
    type: package
    package: myapp
    state: present
`)

	def, err := ParseModuleFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(def.Resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(def.Resources))
	}
	r0 := def.Resources[0]
	if len(r0.Before) != 1 || r0.Before[0] != "install-app" {
		t.Errorf("resource before = %v, want [install-app]", r0.Before)
	}
}

func TestParseModuleFile_RequireAlias(t *testing.T) {
	data := []byte(`
name: app
require: [base]
resources:
  - name: app-config
    type: file
    path: /etc/app.conf
    content: "config"
    state: present
`)

	def, err := ParseModuleFile(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(def.DependsOn) != 1 || def.DependsOn[0] != "base" {
		t.Errorf("depends_on = %v, want [base]", def.DependsOn)
	}
}

func TestParseModuleFile_EmptyDocument(t *testing.T) {
	_, err := ParseModuleFile([]byte(""))
	if err == nil {
		t.Fatal("expected error for empty document")
	}
}
