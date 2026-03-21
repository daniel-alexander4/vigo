// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/secrets"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var testCmd = &cobra.Command{
	Use:   "test [file-or-dir]",
	Short: "Run config tests that validate resolved policy against expected outcomes",
	Long: `Validate .vgo config files against expected outcomes using test definitions.

Test files (*.test.vgo) declare a hostname, optional mock traits, and
expected resources/modules. The test runner loads the full config tree,
resolves config as if the hostname checked in, and verifies the result
matches expectations.

If no argument is given, tests are discovered from policy/stage/tests/.
Pass a file to run a single test or a directory to run all tests in it.

Runs entirely offline — no server connection needed.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runTest,
}

func init() {
	testCmd.Flags().String("config-dir", "/srv/vigo/policy/stage", "Config directory to load")
	testCmd.Flags().BoolP("verbose", "v", false, "Show resolved resources on pass")
	testCmd.Flags().Bool("cli", false, "Run the CLI integration test suite against a live server")
}

// testDef is a parsed test definition file.
type testDef struct {
	Name   string            `yaml:"name"`
	Envoy string            `yaml:"envoy"`
	Traits map[string]any    `yaml:"traits"`
	Expect testExpectations  `yaml:"expect"`
}

type testExpectations struct {
	ModuleCount   *int              `yaml:"module_count"`
	ResourceCount *int              `yaml:"resource_count"`
	Modules       []string          `yaml:"modules"`
	Resources     []expectResource  `yaml:"resources"`
	Vars          map[string]string `yaml:"vars"`
}

type expectResource struct {
	Resource        string `yaml:"resource"`
	Type            string `yaml:"type"`
	Module          string `yaml:"module"`
	ContentContains string `yaml:"content_contains"`
	Targetpath      string `yaml:"targetpath"`
	Package         string `yaml:"package"`
	Service         string `yaml:"service"`
	State           string `yaml:"state"`
	When            string `yaml:"when"`
}

func runTest(cmd *cobra.Command, args []string) error {
	cliMode, _ := cmd.Flags().GetBool("cli")
	if cliMode {
		return runCLITests(cmd)
	}

	configDir, _ := cmd.Flags().GetString("config-dir")
	verbose, _ := cmd.Flags().GetBool("verbose")

	// Discover test files.
	var testFiles []string
	if len(args) > 0 {
		info, err := os.Stat(args[0])
		if err != nil {
			return fmt.Errorf("opening %s: %w", args[0], err)
		}
		if info.IsDir() {
			testFiles = discoverTests(args[0])
		} else {
			testFiles = []string{args[0]}
		}
	} else {
		testDir := filepath.Join(configDir, "tests")
		testFiles = discoverTests(testDir)
	}

	if len(testFiles) == 0 {
		fmt.Println("No test files found.")
		fmt.Println("Create test files with .test.vgo extension in policy/stage/tests/")
		return nil
	}

	// Load config tree with a null secrets provider (tests don't resolve secrets).
	nullProvider := &secrets.NullProvider{}
	store, err := config.NewConfigStore(configDir, nullProvider, true)
	if err != nil {
		return fmt.Errorf("loading config from %s: %w", configDir, err)
	}

	// Run tests.
	passed, failed, errors := 0, 0, 0
	for _, tf := range testFiles {
		data, err := os.ReadFile(tf)
		if err != nil {
			fmt.Printf("  ERROR  %s: %v\n", filepath.Base(tf), err)
			errors++
			continue
		}

		var td testDef
		if err := yaml.Unmarshal(data, &td); err != nil {
			fmt.Printf("  ERROR  %s: %v\n", filepath.Base(tf), err)
			errors++
			continue
		}
		if td.Envoy == "" {
			fmt.Printf("  ERROR  %s: 'envoy' field is required\n", filepath.Base(tf))
			errors++
			continue
		}
		if td.Name == "" {
			td.Name = filepath.Base(tf)
		}

		failures := runOneTest(store, &td, verbose)
		if len(failures) == 0 {
			fmt.Printf("  PASS   %s\n", td.Name)
			passed++
		} else {
			fmt.Printf("  FAIL   %s\n", td.Name)
			for _, f := range failures {
				fmt.Printf("           %s\n", f)
			}
			failed++
		}
	}

	fmt.Printf("\n%d passed, %d failed, %d errors (%d total)\n", passed, failed, errors, len(testFiles))
	if failed > 0 || errors > 0 {
		os.Exit(1)
	}
	return nil
}

// runCLITests executes the CLI integration test suite (scripts/test-cli.sh)
// against a live server. Falls back to common script locations.
func runCLITests(cmd *cobra.Command) error {
	server, _ := cmd.Flags().GetString("server")
	if server == "" {
		server = "https://localhost:8443"
	}

	// Find the test script.
	candidates := []string{
		"scripts/test-cli.sh",
		"/srv/vigo/scripts/test-cli.sh",
		filepath.Join(filepath.Dir(os.Args[0]), "..", "scripts", "test-cli.sh"),
	}
	var scriptPath string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			scriptPath = c
			break
		}
	}
	if scriptPath == "" {
		return fmt.Errorf("test-cli.sh not found; looked in: %s", strings.Join(candidates, ", "))
	}

	c := exec.Command("bash", scriptPath, "--server", server)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(), "VIGOGO="+os.Args[0])
	return c.Run()
}

func runOneTest(store *config.ConfigStore, td *testDef, verbose bool) []string {
	var failures []string

	// Resolve config for the given hostname.
	cfg := store.Get(td.Envoy)
	if cfg == nil {
		return []string{"no config matched for hostname " + td.Envoy}
	}

	// Render templates in content fields using mock traits + resolved vars.
	renderCtx := map[string]any{
		"Traits": td.Traits,
		"Vars":   cfg.Vars,
	}

	// Check module count.
	if td.Expect.ModuleCount != nil {
		if len(cfg.Modules) != *td.Expect.ModuleCount {
			failures = append(failures, fmt.Sprintf("module_count: got %d, want %d", len(cfg.Modules), *td.Expect.ModuleCount))
		}
	}

	// Check resource count.
	totalResources := 0
	for _, m := range cfg.Modules {
		totalResources += len(m.Resources)
	}
	if td.Expect.ResourceCount != nil {
		if totalResources != *td.Expect.ResourceCount {
			failures = append(failures, fmt.Sprintf("resource_count: got %d, want %d", totalResources, *td.Expect.ResourceCount))
		}
	}

	// Check expected modules.
	moduleNames := make(map[string]bool)
	for _, m := range cfg.Modules {
		moduleNames[m.Name] = true
	}
	for _, want := range td.Expect.Modules {
		if !moduleNames[want] {
			failures = append(failures, fmt.Sprintf("module %q not found (have: %s)", want, joinKeys(moduleNames)))
		}
	}

	// Check expected vars.
	for k, want := range td.Expect.Vars {
		got := fmt.Sprintf("%v", cfg.Vars[k])
		if got != want {
			failures = append(failures, fmt.Sprintf("var %q: got %q, want %q", k, got, want))
		}
	}

	// Build resource index for assertions.
	type resolvedResource struct {
		moduleName string
		res        config.ResourceConfig
	}
	resourceIndex := make(map[string]resolvedResource)
	for _, m := range cfg.Modules {
		for _, r := range m.Resources {
			resourceIndex[r.Name] = resolvedResource{moduleName: m.Name, res: r}
		}
	}

	// Check expected resources.
	for _, er := range td.Expect.Resources {
		rr, ok := resourceIndex[er.Resource]
		if !ok {
			failures = append(failures, fmt.Sprintf("resource %q not found", er.Resource))
			continue
		}
		if er.Type != "" && rr.res.Type != er.Type {
			failures = append(failures, fmt.Sprintf("resource %q type: got %q, want %q", er.Resource, rr.res.Type, er.Type))
		}
		if er.Module != "" && rr.moduleName != er.Module {
			failures = append(failures, fmt.Sprintf("resource %q module: got %q, want %q", er.Resource, rr.moduleName, er.Module))
		}
		if er.When != "" && rr.res.When != er.When {
			failures = append(failures, fmt.Sprintf("resource %q when: got %q, want %q", er.Resource, rr.res.When, er.When))
		}
		if er.State != "" {
			got := attrStr(rr.res.Attributes, "state")
			if got == "" {
				got = "present"
			}
			if got != er.State {
				failures = append(failures, fmt.Sprintf("resource %q state: got %q, want %q", er.Resource, got, er.State))
			}
		}
		if er.Targetpath != "" {
			got := attrStr(rr.res.Attributes, "targetpath")
			if got != er.Targetpath {
				failures = append(failures, fmt.Sprintf("resource %q targetpath: got %q, want %q", er.Resource, got, er.Targetpath))
			}
		}
		if er.Package != "" {
			got := attrStr(rr.res.Attributes, "package")
			if got != er.Package {
				failures = append(failures, fmt.Sprintf("resource %q package: got %q, want %q", er.Resource, got, er.Package))
			}
		}
		if er.Service != "" {
			got := attrStr(rr.res.Attributes, "service")
			if got != er.Service {
				failures = append(failures, fmt.Sprintf("resource %q service: got %q, want %q", er.Resource, got, er.Service))
			}
		}
		if er.ContentContains != "" {
			content := attrStr(rr.res.Attributes, "content")
			// Try rendering templates in content.
			rendered := renderTemplate(content, renderCtx)
			if !strings.Contains(rendered, er.ContentContains) {
				failures = append(failures, fmt.Sprintf("resource %q content does not contain %q", er.Resource, er.ContentContains))
			}
		}
	}

	if verbose && len(failures) == 0 {
		fmt.Printf("           envoy=%s modules=%d resources=%d vars=%d\n",
			td.Envoy, len(cfg.Modules), totalResources, len(cfg.Vars))
	}

	return failures
}

func discoverTests(dir string) []string {
	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".test.vgo") || strings.HasSuffix(path, ".test.yaml") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func attrStr(attrs map[string]any, key string) string {
	v, ok := attrs[key]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func joinKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

func renderTemplate(s string, ctx map[string]any) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	tmpl, err := template.New("").Parse(s)
	if err != nil {
		return s
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return s
	}
	return buf.String()
}
