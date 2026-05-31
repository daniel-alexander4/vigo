// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// --- unit tests: expandForeachConfigcrateRefs / foreachInstanceKey ---

func TestExpandForeachConfigcrateRefs_ScalarList(t *testing.T) {
	defs := map[string]*ConfigcrateDefinition{"cache": {Name: "cache"}}
	refs := []ConfigcrateRef{{Name: "cache", ForEach: "kinds"}}
	vars := map[string]any{"kinds": []any{"nginx", "redis"}}

	out, err := expandForeachConfigcrateRefs(refs, defs, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0].Name != "cache[nginx]" || out[0].BaseName != "cache" || out[0].InstanceKey != "nginx" {
		t.Errorf("out[0] = %+v", out[0])
	}
	if out[1].Name != "cache[redis]" || out[1].EachItem != "redis" {
		t.Errorf("out[1] = %+v", out[1])
	}
}

func TestExpandForeachConfigcrateRefs_MapList(t *testing.T) {
	defs := map[string]*ConfigcrateDefinition{"vhost": {Name: "vhost"}}
	refs := []ConfigcrateRef{{Name: "vhost", ForEach: "vhosts", Key: "server_name"}}
	vars := map[string]any{"vhosts": []any{
		map[string]any{"server_name": "www.example.com", "port": "443"},
		map[string]any{"server_name": "blog.example.com"},
	}}

	out, err := expandForeachConfigcrateRefs(refs, defs, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 || out[0].Name != "vhost[www.example.com]" || out[1].Name != "vhost[blog.example.com]" {
		t.Fatalf("out = %+v", out)
	}
	m, _ := out[0].EachItem.(map[string]any)
	if m["port"] != "443" {
		t.Errorf("out[0].EachItem = %+v", out[0].EachItem)
	}
}

func TestExpandForeachConfigcrateRefs_Errors(t *testing.T) {
	defs := map[string]*ConfigcrateDefinition{"vhost": {Name: "vhost"}}

	cases := []struct {
		name string
		ref  ConfigcrateRef
		vars map[string]any
		want string
	}{
		{"var not found", ConfigcrateRef{Name: "vhost", ForEach: "missing"}, map[string]any{}, "foreach var \"missing\" not found"},
		{"not a list", ConfigcrateRef{Name: "vhost", ForEach: "x"}, map[string]any{"x": "scalar"}, "expected a list"},
		{"map list needs key", ConfigcrateRef{Name: "vhost", ForEach: "vhosts"}, map[string]any{"vhosts": []any{map[string]any{"server_name": "a"}}}, "add key:"},
		{"key on scalar list", ConfigcrateRef{Name: "vhost", ForEach: "vhosts", Key: "x"}, map[string]any{"vhosts": []any{"a", "b"}}, "drop key:"},
		{"missing key field", ConfigcrateRef{Name: "vhost", ForEach: "vhosts", Key: "server_name"}, map[string]any{"vhosts": []any{map[string]any{"other": "a"}}}, "missing the \"server_name\" field"},
		{"duplicate keys", ConfigcrateRef{Name: "vhost", ForEach: "vhosts", Key: "server_name"}, map[string]any{"vhosts": []any{map[string]any{"server_name": "a"}, map[string]any{"server_name": "a"}}}, "same instance key \"a\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := expandForeachConfigcrateRefs([]ConfigcrateRef{tc.ref}, defs, tc.vars)
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestExpandForeachConfigcrateRefs_NoForeachPassthrough(t *testing.T) {
	refs := []ConfigcrateRef{{Name: "nginx"}, {Name: "firewall", When: "is_debian"}}
	out, err := expandForeachConfigcrateRefs(refs, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Name != "nginx" || out[1].When != "is_debian" {
		t.Errorf("out = %+v", out)
	}
}

// --- unit tests: expandModuleInstanceResources ---

func TestExpandModuleInstanceResources_MapItem(t *testing.T) {
	resources := []ResourceConfig{
		{Name: "vhost-docroot", Type: "directory", Attributes: map[string]any{"target_path": "/var/www/{{ .Each.server_name }}"}},
		{
			Name:       "vhost-conf",
			Type:       "file",
			Attributes: map[string]any{"target_path": "/etc/nginx/sites-enabled/{{ .Each.server_name }}.conf", "content": "server_name {{ .Each.server_name }};"},
			DependsOn:  []string{"vhost-docroot"},
			Notify:     []string{"nginx-reload"},
		},
	}
	item := map[string]any{"server_name": "www.example.com"}

	out, err := expandModuleInstanceResources(resources, item, "www.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0].Name != "vhost-docroot[www.example.com]" {
		t.Errorf("out[0].Name = %q", out[0].Name)
	}
	if out[0].Attributes["target_path"] != "/var/www/www.example.com" {
		t.Errorf("out[0].target_path = %v", out[0].Attributes["target_path"])
	}
	if out[1].Name != "vhost-conf[www.example.com]" {
		t.Errorf("out[1].Name = %q", out[1].Name)
	}
	if out[1].Attributes["content"] != "server_name www.example.com;" {
		t.Errorf("out[1].content = %v", out[1].Attributes["content"])
	}
	// intra-module reference rewritten to the suffixed name; external one left alone
	if len(out[1].DependsOn) != 1 || out[1].DependsOn[0] != "vhost-docroot[www.example.com]" {
		t.Errorf("out[1].DependsOn = %v", out[1].DependsOn)
	}
	if len(out[1].Notify) != 1 || out[1].Notify[0] != "nginx-reload" {
		t.Errorf("out[1].Notify = %v", out[1].Notify)
	}
}

func TestExpandModuleInstanceResources_ScalarItem(t *testing.T) {
	resources := []ResourceConfig{
		{Name: "pkg", Type: "package", Attributes: map[string]any{"package": "{{ .Item }}-server"}},
	}
	out, err := expandModuleInstanceResources(resources, "redis", "redis")
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Name != "pkg[redis]" || out[0].Attributes["package"] != "redis-server" {
		t.Errorf("out[0] = %+v", out[0])
	}
}

func TestExpandModuleInstanceResources_LeftoverRefIsError(t *testing.T) {
	resources := []ResourceConfig{
		{Name: "cfg", Type: "file", Attributes: map[string]any{"content": "port = {{ .Each.port }}"}},
	}
	// item has no "port" — the {{ .Each.port }} reference can't be resolved.
	_, err := expandModuleInstanceResources(resources, map[string]any{"server_name": "a"}, "a")
	if err == nil {
		t.Fatal("expected error for unresolved .Each reference")
	}
	if !strings.Contains(err.Error(), "doesn't provide") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestExpandModuleInstanceResources_NestedForeachSkipped(t *testing.T) {
	resources := []ResourceConfig{
		{Name: "sub-{{ .Item }}", Type: "file", ForEach: "subpaths", Attributes: map[string]any{"target_path": "/x/{{ .Item }}"}},
	}
	out, err := expandModuleInstanceResources(resources, map[string]any{"server_name": "a"}, "a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The resource-level foreach is left intact; only the name gets suffixed.
	if out[0].ForEach != "subpaths" {
		t.Errorf("out[0].ForEach = %q, want preserved", out[0].ForEach)
	}
	if out[0].Name != "sub-{{ .Item }}[a]" {
		t.Errorf("out[0].Name = %q", out[0].Name)
	}
}

// --- integration tests through NewConfigStore ---

func writeVhostModule(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "configcrates", "vhost.vgo"), `
name: vhost
vars:
  doc_root_base: "/var/www"
resources:
  - name: vhost-docroot
    type: directory
    target_path: "{{ .Vars.doc_root_base }}/{{ .Each.server_name }}"
    mode: "0755"
  - name: vhost-conf
    type: file
    target_path: "/etc/nginx/sites-enabled/{{ .Each.server_name }}.conf"
    content: "server_name {{ .Each.server_name }};"
    depends_on: [vhost-docroot]
    notify: [nginx-reload]
`)
}

func storeFromDir(t *testing.T, dir string) *ConfigStore {
	t.Helper()
	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	return store
}

func TestForeachConfigcrate_FromMatchBlock(t *testing.T) {
	dir := setupDir(t)
	writeVhostModule(t, dir)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web01"
    configcrates:
      - name: vhost
        foreach: vhosts
        key: server_name
    vars:
      vhosts:
        - server_name: www.example.com
        - server_name: shop.example.com
`)
	store := storeFromDir(t, dir)
	cfg := store.Get("web01")
	if cfg == nil {
		t.Fatal("no config for web01")
	}

	var names []string
	byName := map[string]Configcrate{}
	for _, m := range cfg.Configcrates {
		names = append(names, m.Name)
		byName[m.Name] = m
	}
	for _, want := range []string{"vhost[www.example.com]", "vhost[shop.example.com]"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("missing module instance %q; got %v", want, names)
		}
	}

	www := byName["vhost[www.example.com]"]
	if www.BaseName != "vhost" {
		t.Errorf("vhost[www.example.com].BaseName = %q", www.BaseName)
	}
	var docroot, conf *ResourceConfig
	for i := range www.Resources {
		switch {
		case strings.HasPrefix(www.Resources[i].Name, "vhost-docroot"):
			docroot = &www.Resources[i]
		case strings.HasPrefix(www.Resources[i].Name, "vhost-conf"):
			conf = &www.Resources[i]
		}
	}
	if docroot == nil || conf == nil {
		t.Fatalf("missing expected resources in vhost[www.example.com]: %v", www.Resources)
	}
	// .Each substituted at load; .Vars left for the agent (cfg.Vars carries doc_root_base).
	if conf.Attributes["content"] != "server_name www.example.com;" {
		t.Errorf("conf.content = %v", conf.Attributes["content"])
	}
	if got := docroot.Attributes["target_path"]; got != "{{ .Vars.doc_root_base }}/www.example.com" {
		t.Errorf("docroot.target_path = %v", got)
	}
	if got := cfg.Vars["doc_root_base"]; got != "/var/www" {
		t.Errorf("cfg.Vars[doc_root_base] = %v", got)
	}
	// Resource names suffixed; intra-module depends_on rewritten; external notify left alone.
	if conf.Name != "vhost-conf[www.example.com]" {
		t.Errorf("conf.Name = %q", conf.Name)
	}
	if len(conf.DependsOn) != 1 || conf.DependsOn[0] != "vhost-docroot[www.example.com]" {
		t.Errorf("conf.DependsOn = %v", conf.DependsOn)
	}
	if len(conf.Notify) != 1 || conf.Notify[0] != "nginx-reload" {
		t.Errorf("conf.Notify = %v", conf.Notify)
	}
}

func TestForeachConfigcrate_FromRoleAndCommon(t *testing.T) {
	dir := setupDir(t)
	writeVhostModule(t, dir)
	writeRoles(t, dir, `
- name: webserver
  configcrates:
    - name: vhost
      foreach: vhosts
      key: server_name
`)
	// vhosts var supplied via a common.vgo in the subtree.
	writeFile(t, filepath.Join(dir, "default", "common.vgo"), `
vars:
  vhosts:
    - server_name: a.example.com
    - server_name: b.example.com
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web*"
    roles: [webserver]
`)
	store := storeFromDir(t, dir)
	cfg := store.Get("web42")
	if cfg == nil {
		t.Fatal("no config")
	}
	got := map[string]bool{}
	for _, m := range cfg.Configcrates {
		got[m.Name] = true
	}
	for _, want := range []string{"vhost[a.example.com]", "vhost[b.example.com]"} {
		if !got[want] {
			t.Fatalf("missing %q; have %v", want, boolKeys(got))
		}
	}
}

func TestForeachConfigcrate_FromEnvironments(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "configcrates", "site.vgo"), `
name: site
resources:
  - name: site-file
    type: file
    target_path: "/srv/{{ .Each.name }}"
    content: "ok"
`)
	writeFile(t, filepath.Join(dir, "default", "environments.vgo"), `
env:
  production:
    configcrates:
      - name: site
        foreach: sites
        key: name
    vars:
      sites:
        - name: one
        - name: two
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "p1"
    environment: production
`)
	store := storeFromDir(t, dir)
	cfg := store.Get("p1")
	if cfg == nil {
		t.Fatal("no config")
	}
	got := map[string]bool{}
	for _, m := range cfg.Configcrates {
		got[m.Name] = true
	}
	for _, want := range []string{"site[one]", "site[two]"} {
		if !got[want] {
			t.Fatalf("missing %q; have %v", want, boolKeys(got))
		}
	}
}

func TestForeachConfigcrate_LoadErrors(t *testing.T) {
	cases := []struct {
		name      string
		moduleVgo string
		envoysVgo string
		want      string
	}{
		{
			name:      "missing var",
			moduleVgo: "name: m\nresources:\n  - name: r\n    type: file\n    target_path: /x\n    content: ok\n",
			envoysVgo: "envoys:\n  - match: h\n    configcrates:\n      - name: m\n        foreach: nope\n",
			want:      "foreach var \"nope\" not found",
		},
		{
			name:      "map list without key",
			moduleVgo: "name: m\nresources:\n  - name: r\n    type: file\n    target_path: /x\n    content: ok\n",
			envoysVgo: "envoys:\n  - match: h\n    configcrates:\n      - name: m\n        foreach: items\n    vars:\n      items:\n        - {a: 1}\n        - {a: 2}\n",
			want:      "add key:",
		},
		{
			name:      "duplicate keys",
			moduleVgo: "name: m\nresources:\n  - name: r\n    type: file\n    target_path: /x\n    content: ok\n",
			envoysVgo: "envoys:\n  - match: h\n    configcrates:\n      - name: m\n        foreach: items\n        key: id\n    vars:\n      items:\n        - {id: dup}\n        - {id: dup}\n",
			want:      "same instance key",
		},
		{
			name:      "unresolved .Each in resource",
			moduleVgo: "name: m\nresources:\n  - name: r\n    type: file\n    target_path: /x/{{ .Each.missing }}\n    content: ok\n",
			envoysVgo: "envoys:\n  - match: h\n    configcrates:\n      - name: m\n        foreach: items\n        key: id\n    vars:\n      items:\n        - {id: a}\n",
			want:      "doesn't provide",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupDir(t)
			writeFile(t, filepath.Join(dir, "configcrates", "m.vgo"), tc.moduleVgo)
			writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), tc.envoysVgo)
			_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestForeachConfigcrate_BeforeFansOut(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "configcrates", "base.vgo"), `
name: base
before: [app]
resources:
  - name: base-r
    type: file
    target_path: /etc/base
    content: ok
`)
	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
resources:
  - name: app-r
    type: file
    target_path: "/srv/{{ .Each.name }}"
    content: ok
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: h
    configcrates:
      - base
      - name: app
        foreach: apps
        key: name
    vars:
      apps:
        - {name: one}
        - {name: two}
`)
	store := storeFromDir(t, dir)
	cfg := store.Get("h")
	if cfg == nil {
		t.Fatal("no config")
	}
	// base before: [app] -> every app[*] instance gets depends_on: [base].
	for _, m := range cfg.Configcrates {
		if strings.HasPrefix(m.Name, "app[") {
			found := false
			for _, d := range m.DependsOn {
				if d == "base" {
					found = true
				}
			}
			if !found {
				t.Errorf("%s.DependsOn = %v, want to include \"base\"", m.Name, m.DependsOn)
			}
		}
	}
}

func boolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
