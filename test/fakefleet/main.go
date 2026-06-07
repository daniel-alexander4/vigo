// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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
	"time"
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
	stacksDir         string
	statePath         string
	count             int
	namedCount        int
	brokenCount       int
	concurrency       int
	changedCount      int
	cadence           time.Duration
	reportCadence     time.Duration
	reportConcurrency int
	ramp              time.Duration
	limit             int
	vigocliBin        string
	fleetDir          string
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
func parseFlags(cmd string, args []string) config {
	fs := newFlagSet(cmd)
	grpcAddr := fs.String("server", "127.0.0.1:1530", "gRPC server address (host:port)")
	apiAddr := fs.String("api", "https://127.0.0.1:8443", "REST API base (enroll, publish, delete)")
	adminToken := fs.String("admin-token", "", "admin bearer token; defaults to the contents of --admin-token-file")
	tokenFile := fs.String("admin-token-file", "/srv/vigo/cli-admin-token", "root-only local-admin token file, read when --admin-token is empty")
	stacksDir := fs.String("stacks-dir", "/srv/vigo/stacks", "operator stacks tree; fleet config is written under it")
	statePath := fs.String("state", "/srv/vigo/fleet/state.json", "fleet identity manifest (0600); resume reads it")
	count := fs.Int("count", 13647, "number of envoys (create only)")
	named := fs.Int("named", 100, "how many keep a realistic service hostname; the rest are worker-NNNNN (create only)")
	broken := fs.Int("broken", 20, "how many envoys report a failing resource → Failed (create only)")
	changed := fs.Int("changed", 500, "how many envoys report drift → Changed/Diverged (create only)")
	concurrency := fs.Int("concurrency", 64, "max in-flight enroll/checkin operations")
	cadence := fs.Duration("cadence", time.Second, "check-in interval, also reported to the server as the check-in cycle (run/tick)")
	reportCadence := fs.Duration("report-cadence", 5*time.Minute, "how often the run daemon reports a convergence result per envoy (kept rare — a run is an SQLite write; freshness rides on --cadence check-ins)")
	reportConcurrency := fs.Int("report-concurrency", 256, "max in-flight ReportResult writes across the fleet (fire-and-forget; a backlog skips a report round rather than stalling check-ins)")
	ramp := fs.Duration("ramp", time.Minute, "startup window the run daemon staggers first check-ins over (spreads the initial full-bundle builds so the server isn't stormed)")
	limit := fs.Int("limit", 0, "if >0, the run daemon drives only the first N envoys (testing)")
	vigocliBin := fs.String("vigocli", "vigocli", "vigocli binary used to publish the fleet config (stacks → .live)")
	fleetDir := fs.String("fleet-dir", "alexander4", "stacks subdir the demo clouds nest under (e.g. alexander4); the harness owns <fleet-dir>/{aws,gcp} and removes only those on backout")
	fs.Parse(args)

	tok := *adminToken
	if tok == "" {
		if b, err := os.ReadFile(*tokenFile); err == nil {
			tok = trimToken(string(b))
		}
	}

	return config{
		grpcAddr:          *grpcAddr,
		apiAddr:           *apiAddr,
		adminToken:        tok,
		stacksDir:         *stacksDir,
		statePath:         *statePath,
		count:             *count,
		namedCount:        *named,
		brokenCount:       *broken,
		changedCount:      *changed,
		concurrency:       *concurrency,
		cadence:           *cadence,
		reportCadence:     *reportCadence,
		reportConcurrency: *reportConcurrency,
		ramp:              *ramp,
		limit:             *limit,
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
