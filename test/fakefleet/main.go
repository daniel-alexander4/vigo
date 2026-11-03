// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// vigo stands up a realistic, multi-cloud envoy
// fleet against a live vigosrv — believable AWS/GCP host hierarchy,
// per-host traits, and mostly-converged-with-a-few-broken convergence —
// for demos, screenshots, and exercising the fleet/UI surfaces at scale
// without real hardware.
//
// It is deliberately a test/ harness, not a vigocli verb: enrolling
// thousands of demo envoys has no place in the production admin CLI.
//
//	go run ./test/fakefleet create   --admin-token "$(sudo cat /srv/vigo/cli-admin-token)"
//	go run ./test/fakefleet run      # daemon: check every envoy in every 1s (systemd service)
//	go run ./test/fakefleet backout  --admin-token "$(sudo cat /srv/vigo/cli-admin-token)"
//
// Hostnames are bare and realistic (web-prod-use1a-007, or the worker
// pool worker-redis-stage-usw2b-01337) so screenshots read as a real
// fleet. Backout deletes by the persisted manifest; if that's lost it
// falls back to deleting every server-side envoy whose name has the
// generated shape (looksSynthetic) — never a real host (guarded by the
// realHosts allowlist) or the vigosrv-self envoy. See README.md for the
// durability model (a `run` daemon under a systemd service) and the
// license precondition.
package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"git.alexander4.io/vigo/server/publish"
)

const toolVersion = "0.2"

// agentVersion is what every synthetic envoy reports as its agent build on
// check-in. Plain "vigo" so the fleet reads as real agents in the UI's
// version column rather than advertising the harness.
const agentVersion = "vigo"

// config carries the resolved settings shared by every subcommand.
type config struct {
	grpcAddr          string
	apiAddr           string
	adminToken        string
	serverRoot        string // server root holding stacks/, .live/, config-history/
	stacksDir         string // derived: <serverRoot>/stacks (where the fleet tree is written)
	statePath         string
	count             int
	inventoryKB       int
	namedCount        int
	brokenCount       int
	concurrency       int
	changedCount      int
	cadence           time.Duration
	reportCadence     time.Duration
	reportConcurrency int
	ramp              time.Duration
	limit             int
	slice             string
	stream            bool
	skipPublish       bool
	vigocliBin        string
	fleetDir          string
	hostnameSuffix    string
	namePrefix        string
	ipPrefix          string
	httpClient        *http.Client
}

func usage() {
	fmt.Fprintf(os.Stderr, `vigo %s — demo fleet harness

Usage:
  vigo <command> [flags]

Commands:
  create    Generate the fleet config tree, enroll, publish, first report.
  run       Daemon: hold a persistent conn per envoy and check in every --cadence (default 1s).
  tick      One freshness pass: check in + report every envoy once, then exit.
  backout   Delete every fleet envoy, wipe the fleet config, remove state.

Run 'vigo <command> -h' for command flags.
`, toolVersion)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "create":
		runCreate(parseFlags(cmd, args))
	case "run":
		runRun(parseFlags(cmd, args))
	case "tick":
		runTick(parseFlags(cmd, args))
	case "backout":
		runBackout(parseFlags(cmd, args))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

// parseFlags builds the per-command flag set. The commands share one
// shape, so one parser with command-appropriate defaults keeps it simple.
// The admin token falls back to the root-only local-admin token file,
// matching how vigocli authenticates on the server host — that keeps the
// systemd service unit free of any secret.
// validIPPrefix reports whether s is exactly two dotted octets, each
// 0-255 — the shape synthIP requires ("A.B" → "A.B.x.x").
func validIPPrefix(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

func parseFlags(cmd string, args []string) config {
	fs := newFlagSet(cmd)
	grpcAddr := fs.String("server", "127.0.0.1:1530", "gRPC server address (host:port)")
	apiAddr := fs.String("api", "https://127.0.0.1:8443", "REST API base (enroll, publish, delete)")
	adminToken := fs.String("admin-token", "", "admin bearer token; defaults to the contents of --admin-token-file")
	tokenFile := fs.String("admin-token-file", "/srv/vigo/cli-admin-token", "root-only local-admin token file, read when --admin-token is empty")
	serverRoot := fs.String("server-root", publish.DefaultServerRoot, "server root holding stacks/, .live/, config-history/; the fleet tree is written under <root>/stacks and published with `vigocli config publish --server-root <root>`")
	statePath := fs.String("state", "/srv/vigo/fleet/state.json", "fleet identity manifest (0600); resume reads it")
	count := fs.Int("count", 13647, "number of envoys (create only)")
	inventoryKB := fs.Int("inventory-kb", 0, "ADR-033 split-traits full-inventory size target in KB. create: 0 = server-realistic default (150). run/tick: 0 = use the size baked into the manifest; >0 = regenerate the bulk at this size + recompute per-host hashes in memory (a sweep without re-enrolling)")
	named := fs.Int("named", 100, "how many keep a realistic service hostname; the rest are worker-NNNNN (create only)")
	broken := fs.Int("broken", 20, "how many envoys report a failing resource → Failed (create only)")
	changed := fs.Int("changed", 500, "how many envoys report drift → Changed/Diverged (create only)")
	concurrency := fs.Int("concurrency", 64, "max in-flight enroll/checkin operations")
	cadence := fs.Duration("cadence", time.Second, "check-in interval, also reported to the server as the check-in cycle (run/tick)")
	reportCadence := fs.Duration("report-cadence", 5*time.Minute, "how often the run daemon reports a convergence result per envoy (kept rare — a run is an SQLite write; freshness rides on --cadence check-ins)")
	reportConcurrency := fs.Int("report-concurrency", 256, "max in-flight ReportResult writes across the fleet (fire-and-forget; a backlog skips a report round rather than stalling check-ins)")
	ramp := fs.Duration("ramp", time.Minute, "startup window the run daemon staggers first check-ins over (spreads the initial full-bundle builds so the server isn't stormed)")
	limit := fs.Int("limit", 0, "if >0, the run daemon drives only the first N envoys (testing)")
	slice := fs.String("slice", "", "drive only this VM's shard of the fleet as \"i/N\" (1-based), e.g. 2/5 = the 2nd of 5 harness VMs. The N shards tile the fleet disjointly — each VM runs the same create'd manifest with a different --slice and together they drive every envoy once. Empty = whole fleet. For multi-VM fan-out past the ~64k connections one box can hold")
	stream := fs.Bool("stream", false, "run daemon holds a real bidi AgentStream per envoy (exercises vigo_streams_active / the per-stream connection-RSS ceiling, checkin.max_connections) instead of unary check-ins")
	vigocliBin := fs.String("vigocli", "vigocli", "vigocli binary used to publish the fleet config (stacks → .live)")
	skipPublish := fs.Bool("skip-publish", false, "create only: enroll + first-report without writing or publishing a config tree — envoys converge against whatever .live the target server already serves")
	fleetDir := fs.String("fleet-dir", "alexander4", "stacks subdir the demo clouds nest under (e.g. alexander4); the harness owns <fleet-dir>/{aws,gcp} and removes only those on backout")
	hostnameSuffix := fs.String("hostname-suffix", "", "append this suffix to every generated hostname (create only) — e.g. .a.test, so envoys match a spanner bolt's hostname pattern")
	namePrefix := fs.String("name-prefix", "", "prepend \"<tag>-\" to every generated hostname (create only) — e.g. bolt1, so a spanner bolt's envoys are recognizable by name")
	ipPrefix := fs.String("ip-prefix", "", "first two octets of synthetic IPs as \"A.B\" (create only) — e.g. 10.100 → 10.100.x.x, so a spanner bolt's envoys share a recognizable /16. Empty = the default 10.x.x.x spread")
	noVitalsHash := fs.Bool("no-vitals-hash", false, "omit vitals_state_hash (ADR-033 Option C) from check-ins, so the server re-persists every churny tick — the 'before' leg of the Option C CPU A/B against one server image")
	_ = fs.Parse(args)

	// synthIP splices --ip-prefix in verbatim as the leading two octets;
	// a malformed value silently produces invalid addresses (e.g. "10" →
	// "10.5.3", "10.100.5" → "10.100.5.5.3"). Validate the one raw string
	// here, the single choke point before it fans out to gen.
	if *ipPrefix != "" && !validIPPrefix(*ipPrefix) {
		fmt.Fprintf(os.Stderr,
			"invalid --ip-prefix %q: want exactly two octets \"A.B\", each 0-255 (e.g. 10.100)\n",
			*ipPrefix)
		os.Exit(2)
	}

	// Apply the Option C A/B toggle as a process-wide setting (buildCheckInReq
	// reads it like agentVersion). Default keeps the faithful split-agent behavior.
	emitVitalsStateHash = !*noVitalsHash

	tok := *adminToken
	if tok == "" {
		if b, err := os.ReadFile(*tokenFile); err == nil {
			tok = trimToken(string(b))
		}
	}

	// The fleet tree is written under <server-root>/stacks — the same dir
	// `vigocli config publish --server-root <root>` reads from, so a single
	// knob keeps the write and the publish in lockstep. PathsUnder is the SoT.
	stacksDir, _, _ := publish.PathsUnder(*serverRoot)

	return config{
		grpcAddr:          *grpcAddr,
		apiAddr:           *apiAddr,
		adminToken:        tok,
		serverRoot:        *serverRoot,
		stacksDir:         stacksDir,
		statePath:         *statePath,
		count:             *count,
		inventoryKB:       *inventoryKB,
		namedCount:        *named,
		hostnameSuffix:    *hostnameSuffix,
		namePrefix:        *namePrefix,
		ipPrefix:          *ipPrefix,
		brokenCount:       *broken,
		changedCount:      *changed,
		concurrency:       *concurrency,
		cadence:           *cadence,
		reportCadence:     *reportCadence,
		reportConcurrency: *reportConcurrency,
		ramp:              *ramp,
		limit:             *limit,
		slice:             *slice,
		stream:            *stream,
		skipPublish:       *skipPublish,
		vigocliBin:        *vigocliBin,
		fleetDir:          *fleetDir,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-signed / IP-based server cert
				MaxIdleConns:    256,
			},
		},
	}
}
