// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
)

// hostPlan is the generation intent for one synthetic envoy — everything
// except the server-assigned id and the keypair, which enrollment fills
// in. Persisted (with those added) into the state manifest.
type hostPlan struct {
	Hostname    string `json:"hostname"`
	IP          string `json:"ip"`
	Cloud       string `json:"cloud"`
	Region      string `json:"region"`
	AZ          string `json:"az"`
	Class       string `json:"class"`
	Env         string `json:"env"`
	Configcrate string `json:"configcrate"`
	// IdentityJSON is the per-host portion of the ADR-033 inventory — the
	// stable identity keys (os/distro/arch/hostname/cloud/role/env) that vary
	// per envoy. It's byte-merged with the shared inventory bulk (payload.go)
	// to form the full inventory; the demo UI's per-host os/distro/region all
	// come from these keys.
	IdentityJSON json.RawMessage `json:"identity_json"`
	// Outcome drives what this host reports each tick: "ok" (settled),
	// "changed" (a resource re-applied → drift), or "failed" (a resource
	// errored). Spread across the plan so the fleet shows a realistic mix.
	Outcome string `json:"outcome"`
}

type region struct {
	cloud string
	name  string
	code  string // short region code used in realistic hostnames (e.g. use1)
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

// The synthetic topology. 7 regions × 3 AZs × 17 classes × 3 envs = 1071
// cells; the requested count is spread evenly across them (~13 hosts per
// cell at the default count) — far cheaper to publish than per-host files.
//
// Region codes are globally unique so that bare (domain-less) hostnames
// can't collide across clouds — e.g. gcp is europe-west4 (code euw4), not
// europe-west1, so its zonecodes never clash with aws eu-west-1's euw1*.
var synthRegions = []region{
	{"aws", "us-east-1", "use1", []string{"us-east-1a", "us-east-1b", "us-east-1c"}},
	{"aws", "us-west-2", "usw2", []string{"us-west-2a", "us-west-2b", "us-west-2c"}},
	{"aws", "eu-west-1", "euw1", []string{"eu-west-1a", "eu-west-1b", "eu-west-1c"}},
	{"aws", "ap-southeast-1", "apse1", []string{"ap-southeast-1a", "ap-southeast-1b", "ap-southeast-1c"}},
	{"gcp", "us-central1", "usc1", []string{"us-central1-a", "us-central1-b", "us-central1-c"}},
	{"gcp", "europe-west4", "euw4", []string{"europe-west4-a", "europe-west4-b", "europe-west4-c"}},
	{"gcp", "asia-east1", "ase1", []string{"asia-east1-a", "asia-east1-b", "asia-east1-c"}},
}

// azCode is the short zone code used in realistic hostnames + match
// patterns — the region code plus the zone's trailing letter (us-east-1a
// → use1a, us-central1-b → usc1b). Combined with the cloud it's globally
// unique, so first-match-wins patterns stay zone-specific.
func azCode(r region, az string) string { return r.code + az[len(az)-1:] }

// synthClasses are the service roles — real software names for hostname
// variety — each mapped to a category configcrate. The name is what shows
// in the hostname (nginx-prod-use1a-007); the configcrate is the resource
// set it reports against.
var synthClasses = []hostClass{
	// web / app tier → web
	{"web", "web", "m5.large", "e2-standard-2", 2, 8192, "amd64"},
	{"app", "web", "c5.xlarge", "n2-standard-4", 4, 16384, "amd64"},
	{"nginx", "web", "m5.large", "e2-standard-2", 2, 8192, "amd64"},
	{"apache", "web", "m5.large", "e2-standard-2", 2, 8192, "amd64"},
	{"tomcat", "web", "c5.2xlarge", "n2-standard-8", 8, 32768, "amd64"},
	{"haproxy", "web", "c6g.large", "e2-standard-2", 2, 4096, "arm64"},
	// database tier → database
	{"postgres", "database", "r5.2xlarge", "n2-highmem-4", 8, 65536, "amd64"},
	{"mysql", "database", "r5.xlarge", "n2-highmem-2", 4, 32768, "amd64"},
	{"mariadb", "database", "r5.xlarge", "n2-highmem-2", 4, 32768, "amd64"},
	{"mongo", "database", "r5.2xlarge", "n2-highmem-4", 8, 65536, "amd64"},
	// cache tier → cache
	{"redis", "cache", "r6g.large", "e2-highmem-2", 2, 16384, "arm64"},
	{"memcached", "cache", "r6g.large", "e2-highmem-2", 2, 16384, "arm64"},
	// message-queue tier → message-queue
	{"kafka", "message-queue", "m5.2xlarge", "n2-standard-8", 8, 32768, "amd64"},
	{"rabbitmq", "message-queue", "m5.large", "e2-standard-2", 2, 8192, "amd64"},
	// observability + infra → monitoring
	{"prometheus", "monitoring", "t3.large", "e2-standard-2", 2, 8192, "amd64"},
	{"grafana", "monitoring", "t3.medium", "e2-medium", 2, 4096, "amd64"},
	{"bastion", "monitoring", "t3.micro", "e2-micro", 1, 1024, "amd64"},
}

var synthDistros = []distroSpec{
	{"linux", "debian", "ubuntu", "22.04", "5.15.0-91-generic"},
	{"linux", "debian", "debian", "12", "6.1.0-13-amd64"},
	{"linux", "rhel", "rhel", "9.3", "5.14.0-362.el9"},
	{"linux", "rhel", "amazonlinux", "2023", "6.1.61-85.141.amzn2023"},
	{"linux", "rhel", "rocky", "9.3", "5.14.0-362.el9"},
}

// configcrateFiles is the self-contained service-tier configcrate set,
// written under stacks/configcrates/services/. Each uses real resource
// params and real compliance.provides tags from the catalog, so publish
// validates clean and the compliance surfaces light up.
var configcrateFiles = map[string]string{
	"web.vgo": `#~ Web tier — nginx with a hardened baseline.
name: web
description: Web servers (nginx) with a hardened baseline

compliance:
  provides: [host-firewall, login-banner]

resources:
  - name: nginx
    type: package
    package: nginx
    state: present
    when: "os_family('linux')"

  - name: web-hardening
    type: file
    state: present
    target_path: "/etc/nginx/conf.d/hardening.conf"
    owner: root
    group: root
    mode: "0644"
    content: |
      # managed by vigo
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
	"database.vgo": `#~ Database tier — PostgreSQL.
name: database
description: PostgreSQL database servers

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
	"cache.vgo": `#~ Cache tier — Redis.
name: cache
description: Redis in-memory cache servers

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
	"message-queue.vgo": `#~ Message-queue tier — RabbitMQ.
name: message-queue
description: RabbitMQ message brokers

compliance:
  provides: [host-firewall, log-rotation]

resources:
  - name: queue-broker
    type: package
    package: rabbitmq-server
    state: present
    when: "os_family('linux')"

  - name: queue-service
    type: service
    service: rabbitmq-server
    enabled: true
    state: running
    depends_on: [queue-broker]
    when: "os_family('linux')"
`,
	"monitoring.vgo": `#~ Monitoring tier — Prometheus node exporter.
name: monitoring
description: Prometheus node exporter + alerting baseline

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

// environmentsFile is the per-environment override layer, written as
// <cloud>/environments.vgo (discovered per-directory, so it merges cleanly
// without touching any central file). It exercises the multi-axis env
// dimension: prod adds fleet-wide monitoring + a stricter log level,
// stage/test differ only in vars. Keys are limited to configcrates/vars
// (the modlint-known set).
const environmentsFile = `#~ Per-environment overrides.
env:
  prod:
    configcrates: [monitoring]
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

// svcSubdir is the configcrate family name and the directory the generated
// configcrates live under (configcrates/services/). Hostnames are bare and
// realistic, so the backout-safety marker is looksSynthetic (positive
// name-shape match + the realHosts allowlist) below, not a name token.
const svcSubdir = "services"

// realHosts is the operator's entire real fleet — the only non-synthetic
// envoys on the network. Backout's manifest-less fallback never deletes
// these (nor the vigosrv-self envoy, whose name can't match the synthetic
// shape), even though the demo hostnames now carry no reserved suffix.
var realHosts = map[string]bool{
	"danlap": true, "annlap": true, "girlslaptop": true,
	"plex": true, "raspberrypi": true,
}

// Sets of the valid name components, built once from the topology so
// looksSynthetic can positively identify a generated hostname.
var (
	synthClassSet = func() map[string]bool {
		m := make(map[string]bool, len(synthClasses))
		for _, c := range synthClasses {
			m[c.name] = true
		}
		return m
	}()
	synthEnvSet = func() map[string]bool {
		m := make(map[string]bool, len(synthEnvs))
		for _, e := range synthEnvs {
			m[e] = true
		}
		return m
	}()
	synthZoneSet = func() map[string]bool {
		m := map[string]bool{}
		for _, r := range synthRegions {
			for _, az := range r.azs {
				m[azCode(r, az)] = true
			}
		}
		return m
	}()
)

// baseLabel is the lowercased first DNS label of a hostname ("danlap"
// from "danlap.lan"). Synthetic names are bare, so it's a noop for them.
func baseLabel(hostname string) string {
	if i := strings.IndexByte(hostname, '.'); i >= 0 {
		hostname = hostname[:i]
	}
	return strings.ToLower(hostname)
}

// looksSynthetic reports whether a hostname has the exact shape this
// harness generates — "<class>-<env>-<zone>-NNN" or the worker form
// "worker-<class>-<env>-<zone>-NNNNN", with class/env/zone drawn from the
// known topology and a numeric tail. It's the positive identifier
// backout's fallback uses when the manifest is lost: no reserved suffix
// needed, and it can never match a real host (guarded by realHosts) or
// the vigosrv-self envoy (whose name has no such shape).
func looksSynthetic(hostname string) bool {
	if realHosts[baseLabel(hostname)] {
		return false
	}
	parts := strings.Split(hostname, "-")
	if len(parts) == 5 && parts[0] == "worker" {
		parts = parts[1:]
	}
	if len(parts) != 4 {
		return false
	}
	if !synthClassSet[parts[0]] || !synthEnvSet[parts[1]] || !synthZoneSet[parts[2]] {
		return false
	}
	if parts[3] == "" {
		return false
	}
	for _, ch := range parts[3] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// buildHostPlan deterministically spreads `count` hosts across the
// region/AZ/class/env topology. About `named` of them (evenly spread)
// keep a realistic service hostname (<class>-<env>-<zone>-NNN); the rest
// join the worker pool (worker-<class>-<env>-<zone>-NNNNN). It then
// assigns outcomes: `failed` report a resource error, `changed` report
// drift, the rest are settled. Deterministic (fixed seed) so a regenerate
// is identical.
func buildHostPlan(count, named, failed, changed int) []hostPlan {
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

	if named < 0 {
		named = 0
	}
	if named > count {
		named = count
	}
	namedStep := 0
	if named > 0 {
		namedStep = count / named // ≥1 because named ≤ count
	}

	plan := make([]hostPlan, 0, count)
	rng := rand.New(rand.NewSource(1))
	gi := 0
	namedAssigned := 0
	workerSeq := 0
	for ci, cl := range cells {
		n := base
		if ci < rem {
			n++
		}
		zc := azCode(cl.r, cl.az)
		for k := 1; k <= n; k++ {
			d := synthDistros[gi%len(synthDistros)]
			inst := cl.cls.awsInstance
			if cl.r.cloud == "gcp" {
				inst = cl.cls.gcpInstance
			}
			// ~`named` hosts (evenly spread) keep a realistic service
			// hostname (web-prod-use1a-007); the rest join the worker pool
			// (worker-web-prod-use1a-00042). Both are bare (no domain) so
			// they read as real hosts; the env + zonecode keep every host
			// matchable by this zone's hostcrate, so workers nest in the
			// tree and carry the env axis too.
			var host string
			if namedStep > 0 && namedAssigned < named && gi%namedStep == 0 {
				host = fmt.Sprintf("%s-%s-%s-%03d", cl.cls.name, cl.env, zc, k)
				namedAssigned++
			} else {
				workerSeq++
				host = fmt.Sprintf("worker-%s-%s-%s-%05d", cl.cls.name, cl.env, zc, workerSeq)
			}
			identity, _ := json.Marshal(map[string]any{
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
			})
			plan = append(plan, hostPlan{
				Hostname:     host,
				IP:           fmt.Sprintf("10.%d.%d.%d", gi>>16&0xff, gi>>8&0xff, gi&0xff),
				Cloud:        cl.r.cloud,
				Region:       cl.r.name,
				AZ:           cl.az,
				Class:        cl.cls.name,
				Env:          cl.env,
				Configcrate:  cl.cls.configcrate,
				IdentityJSON: identity,
				Outcome:      "ok",
			})
			gi++
		}
	}
	// Assign outcomes across a shuffled index: the first `failed` get
	// errors, the next `changed` report drift, the rest stay settled.
	if failed < 0 {
		failed = 0
	}
	if changed < 0 {
		changed = 0
	}
	if failed+changed > len(plan) {
		changed = len(plan) - failed
		if changed < 0 {
			failed = len(plan)
			changed = 0
		}
	}
	perm := rng.Perm(len(plan))
	for _, idx := range perm[:failed] {
		plan[idx].Outcome = "failed"
	}
	for _, idx := range perm[failed : failed+changed] {
		plan[idx].Outcome = "changed"
	}
	return plan
}

// writeConfigTree renders the synthetic configcrates and the nested
// per-zone glob hostcrates under stacksDir. The hostcrates nest under the
// operator's own fleet dir as <fleetDir>/<cloud>/<region>/<zone>/envoys.vgo
// (e.g. alexander4/aws/us-east-1/us-east-1a/envoys.vgo) — one shared file
// per zone (not per host) — so /envoys/tree groups each envoy under its
// cloud/region/zone alongside the real fleet. configcrate *definitions*
// must live under configcrates/ (loader-hardcoded), so they go in
// configcrates/synthetic/. Returns the exact directories it created, so
// backout removes precisely those — never the operator's fleet dir
// itself, which holds real config.
func writeConfigTree(stacksDir, fleetDir string, plan []hostPlan) (cleanupDirs []string, err error) {
	configcratesDir := filepath.Join(stacksDir, "configcrates", svcSubdir)
	if err = os.MkdirAll(configcratesDir, 0o755); err != nil {
		return nil, err
	}
	for name, content := range configcrateFiles {
		if err = os.WriteFile(filepath.Join(configcratesDir, name), []byte(content), 0o644); err != nil {
			return nil, err
		}
	}
	cleanupDirs = append(cleanupDirs, configcratesDir)

	seenCloud := map[string]bool{}
	for _, r := range synthRegions {
		cloudRoot := filepath.Join(stacksDir, fleetDir, r.cloud)
		if !seenCloud[r.cloud] {
			seenCloud[r.cloud] = true
			if err = os.MkdirAll(cloudRoot, 0o755); err != nil {
				return nil, err
			}
			// One environments.vgo per cloud root keeps the env overrides
			// inside the dirs the harness owns (removed wholesale by
			// backout) while still covering every zone beneath it.
			if err = os.WriteFile(filepath.Join(cloudRoot, "environments.vgo"), []byte(environmentsFile), 0o644); err != nil {
				return nil, err
			}
			cleanupDirs = append(cleanupDirs, cloudRoot)
		}
		for _, az := range r.azs {
			dir := filepath.Join(cloudRoot, r.name, az)
			if err = os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
			if err = os.WriteFile(filepath.Join(dir, "envoys.vgo"), []byte(renderZoneHostcrate(r, az)), 0o644); err != nil {
				return nil, err
			}
		}
	}
	return cleanupDirs, nil
}

// renderZoneHostcrate emits one zone's hostcrate: two glob match blocks
// per (class, env) — one for the kept service hosts
// (<class>-<env>-<zone>-NNN) and one for the worker pool
// (worker-<class>-<env>-<zone>-NNNNN) — both mapping straight to the
// class's configcrate (no roles, so the central roles.vgo stays
// untouched). The env + zonecode literals keep every pattern zone- and
// environment-specific, so workers nest under their cloud/region/zone in
// the tree and carry the env axis just like the named hosts; first-
// match-wins stays unambiguous because the `worker-` prefix is disjoint
// from every class name.
func renderZoneHostcrate(r region, az string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#~ Hostcrate for %s/%s/%s — generated by vigo.\n", r.cloud, r.name, az)
	fmt.Fprintf(&b, "#~ Remove via `vigo backout`; never hand-edit.\n")
	b.WriteString("envoys:\n")
	zc := azCode(r, az)
	for _, c := range synthClasses {
		for _, env := range synthEnvs {
			for _, prefix := range []string{"", "worker-"} {
				fmt.Fprintf(&b, "  - match: \"%s%s-%s-%s-*\"\n", prefix, c.name, env, zc)
				fmt.Fprintf(&b, "    environment: %s\n", env)
				fmt.Fprintf(&b, "    configcrates: [%s]\n", c.configcrate)
				fmt.Fprintf(&b, "    tags: [%s, %s, %s]\n", r.cloud, c.name, env)
				b.WriteString("    vars:\n")
				fmt.Fprintf(&b, "      cloud: %s\n", r.cloud)
				fmt.Fprintf(&b, "      region: %s\n", r.name)
				fmt.Fprintf(&b, "      az: %s\n", az)
			}
		}
	}
	return b.String()
}
