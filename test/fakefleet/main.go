// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// vigo-fakefleet stands up a realistic, multi-cloud SYNTHETIC envoy
// fleet against a live vigosrv — believable AWS/GCP host hierarchy,
// per-host traits, and mostly-converged-with-a-few-broken convergence —
// for demos, screenshots, and exercising the fleet/UI surfaces at scale
// without real hardware.
//
// It is deliberately a test/ harness, not a vigocli verb: enrolling
// thousands of fake envoys has no place in the production admin CLI.
//
//	go run ./test/fakefleet create   --admin-token "$(sudo cat /srv/vigo/cli-admin-token)"
//	go run ./test/fakefleet tick     # one freshness pass (driven by the systemd timer)
//	go run ./test/fakefleet backout  --admin-token "$(sudo cat /srv/vigo/cli-admin-token)"
//
// Every synthetic host's name ends in the reserved `.synthetic` suffix.
// That suffix is the marker: it can never collide with a real host, the
// generated hostcrate patterns only ever match it, and backout targets
// exactly it. See README.md for the durability model (systemd timer +
// stateless tick) and the license precondition.
package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"time"
)

const toolVersion = "0.1"

// config carries the resolved settings shared by every subcommand.
type config struct {
	grpcAddr    string
	apiAddr     string
	adminToken  string
	stacksDir   string
	statePath   string
	count       int
	brokenCount int
	concurrency int
	cadence     time.Duration
	vigocliBin  string
	fleetDir    string
	httpClient  *http.Client
}

func usage() {
	fmt.Fprintf(os.Stderr, `vigo-fakefleet %s — synthetic demo fleet harness

Usage:
  vigo-fakefleet <command> [flags]

Commands:
  create    Generate the stacks/synthetic tree, enroll the fleet, publish, first tick.
  tick      One freshness pass: check in + report every synthetic envoy once, then exit.
  backout   Delete every *.synthetic envoy, wipe the synthetic config, remove state.

Run 'vigo-fakefleet <command> -h' for command flags.
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

// parseFlags builds the per-command flag set. The three commands share
// one shape, so one parser with command-appropriate defaults keeps it
// simple. The admin token falls back to the root-only local-admin token
// file, matching how vigocli authenticates on the server host — that
// keeps the systemd tick unit free of any secret.
func parseFlags(cmd string, args []string) config {
	fs := newFlagSet(cmd)
	grpcAddr := fs.String("server", "127.0.0.1:1530", "gRPC server address (host:port)")
	apiAddr := fs.String("api", "https://127.0.0.1:8443", "REST API base (enroll, publish, delete)")
	adminToken := fs.String("admin-token", "", "admin bearer token; defaults to the contents of --admin-token-file")
	tokenFile := fs.String("admin-token-file", "/srv/vigo/cli-admin-token", "root-only local-admin token file, read when --admin-token is empty")
	stacksDir := fs.String("stacks-dir", "/srv/vigo/stacks", "operator stacks tree; synthetic config is written under it")
	statePath := fs.String("state", "/srv/vigo/fakefleet/state.json", "fleet identity manifest (0600); resume reads it")
	count := fs.Int("count", 13647, "number of synthetic envoys (create only)")
	broken := fs.Int("broken", 7, "how many envoys report a failing resource (create only)")
	concurrency := fs.Int("concurrency", 64, "max in-flight enroll/checkin operations")
	cadence := fs.Duration("cadence", 2*time.Minute, "tick interval reported to the server as the check-in cycle (match the systemd timer)")
	vigocliBin := fs.String("vigocli", "vigocli", "vigocli binary used to publish the synthetic config (stacks → .live)")
	fleetDir := fs.String("fleet-dir", "alexander4", "stacks subdir the fake clouds nest under (e.g. alexander4); the harness owns <fleet-dir>/{aws,gcp} and removes only those on backout")
	fs.Parse(args)

	tok := *adminToken
	if tok == "" {
		if b, err := os.ReadFile(*tokenFile); err == nil {
			tok = trimToken(string(b))
		}
	}

	return config{
		grpcAddr:    *grpcAddr,
		apiAddr:     *apiAddr,
		adminToken:  tok,
		stacksDir:   *stacksDir,
		statePath:   *statePath,
		count:       *count,
		brokenCount: *broken,
		concurrency: *concurrency,
		cadence:     *cadence,
		vigocliBin:  *vigocliBin,
		fleetDir:    *fleetDir,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-signed / IP-based server cert
				MaxIdleConns:    256,
			},
		},
	}
}
