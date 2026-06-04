// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// hostPlan is the generation intent for one synthetic envoy — everything
// except the server-assigned id and the keypair, which enrollment fills
// in. Persisted (with those added) into the state manifest.
type hostPlan struct {
	Hostname    string          `json:"hostname"`
	IP          string          `json:"ip"`
	Cloud       string          `json:"cloud"`
	Region      string          `json:"region"`
	AZ          string          `json:"az"`
	Class       string          `json:"class"`
	Env         string          `json:"env"`
	Configcrate string          `json:"configcrate"`
	TraitsJSON  json.RawMessage `json:"traits_json"`
	Broken      bool            `json:"broken"`
}

type region struct {
	cloud string
	name  string
	azs   []string
}

type hostClass struct {
	name        string
	configcrate string
	awsInstance string
	gcpInstance string
	cpuCores    int
	memoryMB    int
	arch        string
}

type distroSpec struct {
	os, family, distro, version, kernel string
}

// synthEnvs is the multi-axis environment dimension. Each host belongs to
// exactly one; the hostcrate match block carries it as `environment:`,
// which surfaces as the filterable "env" axis on the envoys page, and
// the synthetic environments.vgo layers per-env overrides on top.
var synthEnvs = []string{"prod", "stage", "test"}

// The synthetic topology. 7 regions × 3 AZs × 6 classes × 3 envs = 378
// cells; the requested count is spread evenly across them, so a match
// block covers ~36 hosts — realistic, and far cheaper to publish than
// per-host files.
var synthRegions = []region{
	{"aws", "us-east-1", []string{"us-east-1a", "us-east-1b", "us-east-1c"}},
	{"aws", "us-west-2", []string{"us-west-2a", "us-west-2b", "us-west-2c"}},
	{"aws", "eu-west-1", []string{"eu-west-1a", "eu-west-1b", "eu-west-1c"}},
	{"aws", "ap-southeast-1", []string{"ap-southeast-1a", "ap-southeast-1b", "ap-southeast-1c"}},
	{"gcp", "us-central1", []string{"us-central1-a", "us-central1-b", "us-central1-c"}},
	{"gcp", "europe-west1", []string{"europe-west1-b", "europe-west1-c", "europe-west1-d"}},
	{"gcp", "asia-east1", []string{"asia-east1-a", "asia-east1-b", "asia-east1-c"}},
}

var synthClasses = []hostClass{
	{"web", "synthetic-web", "m5.large", "e2-standard-2", 2, 8192, "amd64"},
	{"app", "synthetic-web", "c5.xlarge", "n2-standard-4", 4, 16384, "amd64"},
	{"db", "synthetic-db", "r5.2xlarge", "n2-highmem-4", 8, 65536, "amd64"},
	{"cache", "synthetic-cache", "r6g.large", "e2-highmem-2", 2, 16384, "arm64"},
	{"monitoring", "synthetic-monitoring", "t3.medium", "e2-medium", 2, 4096, "amd64"},
	{"bastion", "synthetic-web", "t3.micro", "e2-micro", 1, 1024, "amd64"},
}

var synthDistros = []distroSpec{
	{"linux", "debian", "ubuntu", "22.04", "5.15.0-91-generic"},
	{"linux", "debian", "debian", "12", "6.1.0-13-amd64"},
	{"linux", "rhel", "rhel", "9.3", "5.14.0-362.el9"},
	{"linux", "rhel", "amazonlinux", "2023", "6.1.61-85.141.amzn2023"},
	{"linux", "rhel", "rocky", "9.3", "5.14.0-362.el9"},
}

// configcrateFiles is the self-contained synthetic configcrate set,
// written under stacks/configcrates/synthetic/. Each uses real resource
// params and real compliance.provides tags from the catalog, so publish
// validates clean and the compliance surfaces light up.
var configcrateFiles = map[string]string{
	"synthetic-web.vgo": `#~ Synthetic web tier — demo fleet only (vigo-synthfleet), not a real workload.
name: synthetic-web
description: Synthetic web tier for the demo fleet

compliance:
  provides: [host-firewall, login-banner]

resources:
  - name: nginx
    type: package
    package: nginx
    state: present
    when: "os_family('linux')"

  - name: synthetic-web-conf
    type: file
    state: present
    target_path: "/etc/nginx/conf.d/synthetic.conf"
    owner: root
    group: root
    mode: "0644"
    content: |
      # synthetic demo fleet — managed by vigo-synthfleet
      server_tokens off;
    notify: [nginx-service]
    when: "os_family('linux')"

  - name: nginx-service
    type: service
    service: nginx
    enabled: true
    state: running
    depends_on: [nginx]
    when: "os_family('linux')"
`,
	"synthetic-db.vgo": `#~ Synthetic database tier — demo fleet only (vigo-synthfleet).
name: synthetic-db
description: Synthetic database tier for the demo fleet

compliance:
  provides: [host-backup, file-integrity]

resources:
  - name: postgresql
    type: package
    package: postgresql
    state: present
    when: "os_family('linux')"

  - name: postgresql-service
    type: service
    service: postgresql
    enabled: true
    state: running
    depends_on: [postgresql]
    when: "os_family('linux')"
`,
	"synthetic-cache.vgo": `#~ Synthetic cache tier — demo fleet only (vigo-synthfleet).
name: synthetic-cache
description: Synthetic cache tier for the demo fleet

compliance:
  provides: [host-firewall]

resources:
  - name: redis
    type: package
    package: redis-server
    state: present
    when: "os_family('linux')"

  - name: redis-service
    type: service
    service: redis-server
    enabled: true
    state: running
    depends_on: [redis]
    when: "os_family('linux')"
`,
	"synthetic-monitoring.vgo": `#~ Synthetic monitoring tier — demo fleet only (vigo-synthfleet).
name: synthetic-monitoring
description: Synthetic monitoring tier for the demo fleet

compliance:
  provides: [alerting, audit-trail]

resources:
  - name: node-exporter
    type: package
    package: prometheus-node-exporter
    state: present
    when: "os_family('linux')"

  - name: node-exporter-service
    type: service
    service: prometheus-node-exporter
    enabled: true
    state: running
    depends_on: [node-exporter]
    when: "os_family('linux')"
`,
}

// environmentsFile is the synthetic per-environment override layer,
// written as stacks/synthetic/environments.vgo (discovered per-directory,
// so it merges cleanly without touching any central file). It exercises
// the multi-axis env dimension: prod adds fleet-wide monitoring + a
// stricter log level, stage/test differ only in vars. Keys are limited to
// configcrates/vars (the modlint-known set).
const environmentsFile = `#~ Synthetic per-environment overrides — demo fleet only (vigo-synthfleet).
env:
  prod:
    configcrates: [synthetic-monitoring]
    vars:
      log_level: warn
      backup_schedule: "0 */4 * * *"
  stage:
    vars:
      log_level: info
  test:
    vars:
      log_level: debug
`

// synthTLD is the reserved suffix on every synthetic hostname. It is the
// marker the whole tool keys on: hostcrate patterns only ever match it,
// it can never collide with a real host, and backout targets exactly it.
const synthTLD = "synthetic"

// buildHostPlan deterministically spreads `count` hosts across the
// region/AZ/class topology and flags `broken` of them. Deterministic
// (fixed seed) so a regenerate yields identical hostnames.
func buildHostPlan(count, broken int) []hostPlan {
	type cell struct {
		r   region
		az  string
		cls hostClass
		env string
	}
	var cells []cell
	for _, r := range synthRegions {
		for _, az := range r.azs {
			for _, c := range synthClasses {
				for _, env := range synthEnvs {
					cells = append(cells, cell{r, az, c, env})
				}
			}
		}
	}
	base := count / len(cells)
	rem := count % len(cells)

	plan := make([]hostPlan, 0, count)
	rng := rand.New(rand.NewSource(1))
	gi := 0
	for ci, cl := range cells {
		n := base
		if ci < rem {
			n++
		}
		for k := 1; k <= n; k++ {
			d := synthDistros[gi%len(synthDistros)]
			inst := cl.cls.awsInstance
			if cl.r.cloud == "gcp" {
				inst = cl.cls.gcpInstance
			}
			host := fmt.Sprintf("%s-%05d.%s.%s.%s.%s", cl.cls.name, k, cl.env, cl.az, cl.r.cloud, synthTLD)
			traits, _ := json.Marshal(map[string]any{
				"os":             d.os,
				"os_family":      d.family,
				"distro":         d.distro,
				"distro_version": d.version,
				"arch":           cl.cls.arch,
				"kernel":         d.kernel,
				"hostname":       host,
				"cloud_provider": cl.r.cloud,
				"cloud_region":   cl.r.name,
				"cloud_az":       cl.az,
				"instance_type":  inst,
				"cpu_cores":      cl.cls.cpuCores,
				"memory_mb":      cl.cls.memoryMB,
				"role_class":     cl.cls.name,
				"environment":    cl.env,
				"synthetic":      true,
			})
			plan = append(plan, hostPlan{
				Hostname:    host,
				IP:          fmt.Sprintf("10.%d.%d.%d", gi>>16&0xff, gi>>8&0xff, gi&0xff),
				Cloud:       cl.r.cloud,
				Region:      cl.r.name,
				AZ:          cl.az,
				Class:       cl.cls.name,
				Env:         cl.env,
				Configcrate: cl.cls.configcrate,
				TraitsJSON:  traits,
			})
			gi++
		}
	}
	// Flag the broken few — spread across the plan, not clustered.
	if broken > len(plan) {
		broken = len(plan)
	}
	for _, idx := range rng.Perm(len(plan))[:broken] {
		plan[idx].Broken = true
	}
	return plan
}

// writeConfigTree renders the synthetic configcrates and the per-region
// glob hostcrates under stacksDir. Returns the two synthetic directories
// it owns (for backout to remove).
func writeConfigTree(stacksDir string, plan []hostPlan) (configcratesDir, hostcratesDir string, err error) {
	configcratesDir = filepath.Join(stacksDir, "configcrates", synthTLD)
	hostcratesDir = filepath.Join(stacksDir, synthTLD)

	if err = os.MkdirAll(configcratesDir, 0o755); err != nil {
		return "", "", err
	}
	for name, content := range configcrateFiles {
		if err = os.WriteFile(filepath.Join(configcratesDir, name), []byte(content), 0o644); err != nil {
			return "", "", err
		}
	}
	if err = os.MkdirAll(hostcratesDir, 0o755); err != nil {
		return "", "", err
	}
	if err = os.WriteFile(filepath.Join(hostcratesDir, "environments.vgo"), []byte(environmentsFile), 0o644); err != nil {
		return "", "", err
	}
	for _, r := range synthRegions {
		dir := filepath.Join(hostcratesDir, r.cloud)
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return "", "", err
		}
		body := renderHostcrate(r)
		if err = os.WriteFile(filepath.Join(dir, r.name+".vgo"), []byte(body), 0o644); err != nil {
			return "", "", err
		}
	}
	return configcratesDir, hostcratesDir, nil
}

// renderHostcrate emits one region's hostcrate: a glob match block per
// (az, class), mapping host-class patterns straight to configcrates (no
// roles — that keeps the central roles.vgo untouched). Every pattern is
// suffixed `.synthetic`, so it can only ever match synthetic hosts.
func renderHostcrate(r region) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#~ Synthetic hostcrate for %s/%s — generated by vigo-synthfleet.\n", r.cloud, r.name)
	fmt.Fprintf(&b, "#~ Remove via `vigo-synthfleet backout`; never hand-edit.\n")
	b.WriteString("envoys:\n")
	azs := append([]string(nil), r.azs...)
	sort.Strings(azs)
	for _, az := range azs {
		for _, c := range synthClasses {
			for _, env := range synthEnvs {
				// One block per (az, class, env). The env literal in the
				// pattern partitions hosts by environment; `environment:`
				// makes it the multi-axis env dimension the synthetic
				// environments.vgo then layers overrides onto.
				fmt.Fprintf(&b, "  - match: \"%s-*.%s.%s.%s.%s\"\n", c.name, env, az, r.cloud, synthTLD)
				fmt.Fprintf(&b, "    environment: %s\n", env)
				fmt.Fprintf(&b, "    configcrates: [%s]\n", c.configcrate)
				fmt.Fprintf(&b, "    tags: [%s, %s, %s, %s]\n", synthTLD, r.cloud, c.name, env)
				b.WriteString("    vars:\n")
				fmt.Fprintf(&b, "      cloud: %s\n", r.cloud)
				fmt.Fprintf(&b, "      region: %s\n", r.name)
				fmt.Fprintf(&b, "      az: %s\n", az)
			}
		}
	}
	return b.String()
}
