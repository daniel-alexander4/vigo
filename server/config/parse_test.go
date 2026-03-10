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

func TestParseModuleFile_EmptyDocument(t *testing.T) {
	_, err := ParseModuleFile([]byte(""))
	if err == nil {
		t.Fatal("expected error for empty document")
	}
}
