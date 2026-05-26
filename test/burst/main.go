// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// vigo-burst measures the server's response to a mass config-publish
// burst: a healthy fleet of N envoys in steady-state CheckIn cycle gets
// hit with a config reload, every envoy's next CheckIn pulls a fresh
// bundle within one interval, and the tool captures the latency spike
// + recovery curve at per-second granularity.
//
// See README.md for the methodology + measurement-discipline rationale
// vs. the simpler test/loadtest/ tool that lives alongside.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	mathrand "math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func mathRandInt63n(n int64) int64 { return mathrand.Int63n(n) }

const toolVersion = "0.1"

func main() {
	grpcAddr := flag.String("server", "localhost:1530", "gRPC server address (host:port)")
	apiAddr := flag.String("api", "https://localhost:8443", "REST API base (used for enroll + cleanup + reload trigger)")
	count := flag.Int("count", 1000, "number of simulated envoys")
	interval := flag.Duration("interval", 5*time.Second, "per-envoy CheckIn interval")
	warmupSec := flag.Int("warmup", 60, "warmup phase (seconds) — discarded from baseline percentiles")
	baselineSec := flag.Int("baseline", 60, "baseline phase (seconds) — measured steady-state")
	burstSettleSec := flag.Int("burst-settle", 180, "post-burst phase (seconds) — captures the spike and recovery")
	cooldownSec := flag.Int("cooldown", 30, "cooldown phase (seconds) — confirms return to baseline")
	trials := flag.Int("trials", 1, "number of back-to-back trials; aggregate mean+stdev reported")
	skipCleanup := flag.Bool("skip-cleanup", false, "leave simulated envoys enrolled after the run")
	adminToken := flag.String("admin-token", "", "admin bearer token (e.g. $(sudo cat /srv/vigo/cli-admin-token)); required for reload + cleanup")
	hostnamePrefix := flag.String("hostname-prefix", "burst-", "hostname prefix; bootstrap pattern must accept this")
	serverContainer := flag.String("server-container", "vigo", "docker container name for server-side pprof sampling; empty disables sampling")
	serverSampleInterval := flag.Duration("server-sample-interval", 5*time.Second, "cadence for server-side resource samples")
	outputDir := flag.String("output", "", "directory for per-trial output (manifest/summary/CSV); empty prints summary only")
	slaBaselineP99 := flag.Duration("sla-baseline-p99", 0, "SLA gate: max baseline p99 (e.g. 1s); 0 disables")
	slaSettleSec := flag.Int("sla-settle-sec", 0, "SLA gate: max post-burst seconds to return within 2× baseline p99; 0 disables")
	slaFailureRate := flag.Float64("sla-failure-rate", 0, "SLA gate: max baseline failure ratio (0.001 = 0.1%); 0 disables")
	notes := flag.String("notes", "", "freeform notes embedded in manifest.json (e.g. hardware label)")
	flag.Parse()

	if *adminToken == "" {
		fmt.Fprintln(os.Stderr, "--admin-token is required (publish-reload + cleanup both need it)")
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	tlsClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   30 * time.Second,
	}

	cli := append([]string{}, os.Args[1:]...)
	man := manifest{
		ToolVersion:      toolVersion,
		StartedUTC:       time.Now().UTC(),
		Hostname:         hostnameOrUnknown(),
		OS:               runtime.GOOS + "/" + runtime.GOARCH,
		ServerAddr:       *grpcAddr,
		APIAddr:          *apiAddr,
		ServerContainer:  *serverContainer,
		EnvoyCount:       *count,
		Interval:         interval.String(),
		WarmupSec:        *warmupSec,
		BaselineSec:      *baselineSec,
		BurstSettleSec:   *burstSettleSec,
		CooldownSec:      *cooldownSec,
		Trials:           *trials,
		HostnamePrefix:   *hostnamePrefix,
		CLIArgs:          cli,
		ServerYAMLSHA256: serverYAMLHash(ctx, *serverContainer),
		VigoVersion:      vigoVersion(ctx, *serverContainer),
		SLABaselineP99Ms: slaBaselineP99.Milliseconds(),
		SLASettleSec:     *slaSettleSec,
		SLAFailureRate:   *slaFailureRate,
		Notes:            *notes,
	}

	if *outputDir != "" {
		if err := os.MkdirAll(*outputDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir output: %v\n", err)
			os.Exit(2)
		}
		if err := writeManifest(filepath.Join(*outputDir, "manifest.json"), man); err != nil {
			fmt.Fprintf(os.Stderr, "write manifest: %v\n", err)
		}
	}

	fmt.Printf("vigo-burst %s\n", toolVersion)
	fmt.Printf("  server:    %s\n", *grpcAddr)
	fmt.Printf("  envoys:    %d @ %v interval\n", *count, *interval)
	fmt.Printf("  phases:    warmup=%ds baseline=%ds burst-settle=%ds cooldown=%ds\n",
		*warmupSec, *baselineSec, *burstSettleSec, *cooldownSec)
	fmt.Printf("  trials:    %d\n", *trials)
	fmt.Println()

	// Single enrollment cohort reused across trials. Each trial restarts
	// the in-process CheckIn loop but the envoys themselves persist (and
	// keep their fingerprints across trials, which becomes interesting
	// noise to absorb in the baseline phase).
	envoys, enrollFails := enrollAll(tlsClient, *apiAddr, *hostnamePrefix, *count)
	if len(envoys) == 0 {
		fmt.Fprintln(os.Stderr, "no envoys enrolled — check trusted_enrollment pattern in server.yaml")
		os.Exit(2)
	}
	fmt.Printf("enrolled %d/%d envoys (%d failed)\n\n", len(envoys), *count, enrollFails)

	defer func() {
		if *skipCleanup {
			return
		}
		fmt.Printf("\ncleaning up %d envoys...\n", len(envoys))
		cleaned := cleanupEnvoys(tlsClient, *apiAddr, *adminToken, envoys)
		fmt.Printf("  deleted %d/%d\n", cleaned, len(envoys))
	}()

	var allTrials []trialResult
	for t := 1; t <= *trials; t++ {
		fmt.Printf("=== trial %d/%d ===\n", t, *trials)
		res, err := runTrial(ctx, trialOpts{
			envoys:               envoys,
			grpcAddr:             *grpcAddr,
			apiAddr:              *apiAddr,
			adminToken:           *adminToken,
			interval:             *interval,
			warmupSec:            *warmupSec,
			baselineSec:          *baselineSec,
			burstSettleSec:       *burstSettleSec,
			cooldownSec:          *cooldownSec,
			serverContainer:      *serverContainer,
			serverSampleInterval: *serverSampleInterval,
			trialID:              t,
			outputDir:            *outputDir,
		}, tlsClient)
		if err != nil {
			fmt.Fprintf(os.Stderr, "trial %d failed: %v\n", t, err)
			break
		}
		allTrials = append(allTrials, res)
		printTrialSummary(res)
	}

	if len(allTrials) > 1 {
		printAggregate(allTrials, *outputDir)
	}

	// SLA gating — exit non-zero on violation so a CI run trips.
	if exitCode := evalSLA(allTrials, *slaBaselineP99, *slaSettleSec, *slaFailureRate); exitCode != 0 {
		os.Exit(exitCode)
	}
}

// -- enrollment --

func enrollAll(client *http.Client, apiAddr, prefix string, count int) ([]testEnvoy, int) {
	fmt.Printf("enrolling %d envoys...\n", count)
	envoys := make([]testEnvoy, 0, count)
	var fails int
	for i := 0; i < count; i++ {
		hostname := fmt.Sprintf("%s%05d.test", prefix, i)
		e, err := enroll(client, apiAddr, hostname)
		if err != nil {
			fails++
			if fails <= 3 {
				fmt.Printf("  FAIL %s: %v\n", hostname, err)
			} else if fails == 4 {
				fmt.Printf("  ... suppressing further enrollment errors\n")
			}
			continue
		}
		envoys = append(envoys, e)
		if (i+1)%1000 == 0 {
			fmt.Printf("  enrolled %d/%d\n", i+1, count)
		}
	}
	return envoys, fails
}

func cleanupEnvoys(client *http.Client, apiAddr, adminToken string, envoys []testEnvoy) int {
	const concurrency = 16
	sem := make(chan struct{}, concurrency)
	var (
		mu      sync.Mutex
		cleaned int
		wg      sync.WaitGroup
	)
	for _, e := range envoys {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := deleteEnvoy(client, apiAddr, adminToken, id); err == nil {
				mu.Lock()
				cleaned++
				mu.Unlock()
			}
		}(e.id)
	}
	wg.Wait()
	return cleaned
}

// -- one trial --

type trialOpts struct {
	envoys               []testEnvoy
	grpcAddr             string
	apiAddr              string
	adminToken           string
	interval             time.Duration
	warmupSec            int
	baselineSec          int
	burstSettleSec       int
	cooldownSec          int
	serverContainer      string
	serverSampleInterval time.Duration
	trialID              int
	outputDir            string
}

// trialResult captures the headline numbers for one trial. Aggregate
// reports mean ± stdev across multiple trials so a single trial's
// percentile isn't quoted in isolation. Latency is reported in float
// milliseconds — sub-ms values (loopback CheckIn typically lands at
// 0.7-1.5ms) need fractional precision to be meaningful.
type trialResult struct {
	BaselineP50Ms        float64          `json:"baseline_p50_ms"`
	BaselineP95Ms        float64          `json:"baseline_p95_ms"`
	BaselineP99Ms        float64          `json:"baseline_p99_ms"`
	BaselineThruput      float64          `json:"baseline_throughput"`
	BaselineFailureRatio float64          `json:"baseline_failure_ratio"`
	PeakP99Ms            float64          `json:"peak_p99_ms"`
	PeakP99AtSec         int              `json:"peak_p99_at_sec"`
	SettleTimeSec        int              `json:"settle_time_sec"`
	PeakRSSMB            int64            `json:"peak_rss_mb"`
	PeakGoroutines       int64            `json:"peak_goroutines"`
	FailureCodes         map[string]int64 `json:"failure_codes"`
	ReloadStatus         int              `json:"reload_status"`
	ReloadAtSec          int              `json:"reload_at_sec"`
}

func runTrial(ctx context.Context, opts trialOpts, _ *http.Client) (trialResult, error) {
	tlsClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   30 * time.Second,
	}

	res := trialResult{ReloadAtSec: -1}

	// Per-trial histogram for the rolling per-second time series, and
	// separate histograms accumulating each phase's totals for the
	// summary percentiles. The per-second hist resets every tick; the
	// phase hists never reset within the trial.
	var (
		perSecond     Histogram
		baselineHist  Histogram
		burstHist     Histogram
		cooldownHist  Histogram
		failures      failureBreakdown
		series        secondSeries
		serverSeriesV serverSeries
		throughput    atomic.Int64 // resets each second
		pprofWG       sync.WaitGroup
	)

	// Create the trial output directory upfront so pprof captures
	// (CPU profile spans 30s of the burst phase; mutex/block snapshots
	// are point-in-time) have a place to land. End-of-trial writes
	// reuse this same dir for summary.json + CSVs.
	var trialDir string
	if opts.outputDir != "" {
		trialDir = filepath.Join(opts.outputDir, fmt.Sprintf("trial-%02d", opts.trialID))
		_ = os.MkdirAll(trialDir, 0o755)
	}

	// Phase clock: total elapsed seconds since trial start. Encodes the
	// phase boundaries; ticker writes a sample for each second observed.
	warmupEnd := opts.warmupSec
	baselineEnd := warmupEnd + opts.baselineSec
	burstEnd := baselineEnd + opts.burstSettleSec
	cooldownEnd := burstEnd + opts.cooldownSec

	phaseFor := func(sec int) string {
		switch {
		case sec < warmupEnd:
			return "warmup"
		case sec < baselineEnd:
			return "baseline"
		case sec < burstEnd:
			return "burst"
		case sec < cooldownEnd:
			return "cooldown"
		default:
			return "end"
		}
	}

	// Each envoy: dial in parallel, signal ready, then block until main
	// releases the start barrier. This way trialStart fires AFTER every
	// envoy is connected — at 50K envoys with serial-stagger the launch
	// alone consumed 5+ minutes. Parallel dial + barrier keeps the
	// "phase clock" honest.
	ctxTrial, cancelTrial := context.WithTimeout(ctx,
		time.Duration(cooldownEnd)*time.Second+time.Duration(opts.warmupSec)*time.Second+5*time.Minute)
	defer cancelTrial()

	var wg sync.WaitGroup
	dialReady := make(chan struct{}, len(opts.envoys))
	startGate := make(chan struct{})
	dialStart := time.Now()
	var dialFailed atomic.Int64

	var trialStart time.Time
	// 50K parallel goroutines doing trivial setup work is fine — grpc.NewClient
	// is lazy (no TCP/TLS handshake at construction time; the handshake fires
	// on the first RPC). No semaphore needed at this stage.
	for i := range opts.envoys {
		wg.Add(1)
		go func(e *testEnvoy) {
			runCheckinLoopGated(ctxTrial, opts.grpcAddr, e, opts.interval,
				&perSecond, &baselineHist, &burstHist, &cooldownHist,
				&failures, &throughput, &wg,
				phaseFor, &trialStart,
				dialReady, startGate, &dialFailed)
		}(&opts.envoys[i])
	}

	// Wait for every envoy to either dial or fail. Failures count toward
	// the barrier so a few stragglers don't deadlock the trial.
	dialedOK := 0
	for dialedOK+int(dialFailed.Load()) < len(opts.envoys) {
		select {
		case <-ctxTrial.Done():
			fmt.Fprintf(os.Stderr, "  dial barrier timed out at %d/%d dialed, %d failed\n",
				dialedOK, len(opts.envoys), dialFailed.Load())
			close(startGate)
			wg.Wait()
			return res, ctxTrial.Err()
		case <-dialReady:
			dialedOK++
			if dialedOK%5000 == 0 {
				fmt.Printf("  dialed %d/%d (%v elapsed)\n", dialedOK, len(opts.envoys), time.Since(dialStart).Round(time.Second))
			}
		}
	}
	fmt.Printf("  all envoys dialed (%d ok, %d failed) at %v elapsed; releasing start barrier\n",
		dialedOK, dialFailed.Load(), time.Since(dialStart).Round(time.Second))
	trialStart = time.Now()
	close(startGate)

	// Per-second sampler — runs until trial ends. Captures one
	// secondSample per real second (driven by a ticker).
	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		tick := time.NewTicker(1 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctxTrial.Done():
				return
			case <-tick.C:
				elapsed := int(time.Since(trialStart).Round(time.Second).Seconds())
				if elapsed >= cooldownEnd {
					return
				}
				snap := perSecond.SnapshotAndReset()
				thru := throughput.Swap(0)
				series.Append(secondSample{
					elapsedSec:  elapsed,
					throughput:  thru,
					p50:         snap.Percentile(50),
					p95:         snap.Percentile(95),
					p99:         snap.Percentile(99),
					p999:        snap.Percentile(99.9),
					maxObserved: snap.Max(),
					failures:    0, // failures are cumulative in failureBreakdown; per-second delta is below
					phase:       phaseFor(elapsed),
				})
			}
		}
	}()

	// Server-side resource sampler.
	if opts.serverContainer != "" {
		go func() {
			tick := time.NewTicker(opts.serverSampleInterval)
			defer tick.Stop()
			for {
				select {
				case <-ctxTrial.Done():
					return
				case <-tick.C:
					elapsed := int(time.Since(trialStart).Round(time.Second).Seconds())
					if elapsed >= cooldownEnd {
						return
					}
					serverSeriesV.Append(pollServer(ctxTrial, opts.serverContainer, phaseFor(elapsed), elapsed))
				}
			}
		}()
	}

	// Wait until baseline ends, then trigger the burst.
	burstTimer := time.NewTimer(time.Duration(baselineEnd) * time.Second)
	select {
	case <-ctxTrial.Done():
		burstTimer.Stop()
		wg.Wait()
		return res, ctxTrial.Err()
	case <-burstTimer.C:
	}

	fmt.Printf("  triggering reload burst at %v...\n", time.Since(trialStart).Round(time.Second))
	statusCode, err := triggerConfigReload(tlsClient, opts.apiAddr, opts.adminToken)
	res.ReloadStatus = statusCode
	res.ReloadAtSec = int(time.Since(trialStart).Round(time.Second).Seconds())
	if err != nil {
		fmt.Fprintf(os.Stderr, "  reload trigger failed: %v\n", err)
	}

	// Kick off pprof captures spanning the burst. CPU profile is the
	// load-bearing one — it's the only way to validate "is ed25519
	// verify still ~25 % of CPU at fleet scale" or "did the mutex
	// arc actually shift the contention shape." Mutex/block t0/t1
	// give a before/after delta over the same window. All async; we
	// wait at end of trial.
	if trialDir != "" && opts.serverContainer != "" {
		pprofWG.Add(5)
		go capturePprof(&pprofWG, ctxTrial, opts.serverContainer,
			"/debug/pprof/profile?seconds=30", filepath.Join(trialDir, "cpu.pprof"))
		go capturePprof(&pprofWG, ctxTrial, opts.serverContainer,
			"/debug/pprof/mutex", filepath.Join(trialDir, "mutex.t0.pprof"))
		go capturePprof(&pprofWG, ctxTrial, opts.serverContainer,
			"/debug/pprof/block", filepath.Join(trialDir, "block.t0.pprof"))
		// Second mutex/block + heap snapshot taken near end of burst,
		// so the t0→t1 delta covers the spike window.
		go func() {
			defer pprofWG.Done()
			select {
			case <-ctxTrial.Done():
				return
			case <-time.After(time.Duration(opts.burstSettleSec-5) * time.Second):
			}
			subWG := &sync.WaitGroup{}
			subWG.Add(3)
			capturePprof(subWG, ctxTrial, opts.serverContainer,
				"/debug/pprof/mutex", filepath.Join(trialDir, "mutex.t1.pprof"))
			capturePprof(subWG, ctxTrial, opts.serverContainer,
				"/debug/pprof/block", filepath.Join(trialDir, "block.t1.pprof"))
			capturePprof(subWG, ctxTrial, opts.serverContainer,
				"/debug/pprof/heap", filepath.Join(trialDir, "heap.pprof"))
			subWG.Wait()
		}()
		go func() {
			defer pprofWG.Done()
			select {
			case <-ctxTrial.Done():
				return
			case <-time.After(time.Duration(opts.burstSettleSec-5) * time.Second):
			}
			subWG := &sync.WaitGroup{}
			subWG.Add(1)
			capturePprof(subWG, ctxTrial, opts.serverContainer,
				"/debug/pprof/goroutine?debug=1", filepath.Join(trialDir, "goroutine.txt"))
			subWG.Wait()
		}()
	}

	// Let the test run to completion. pprofWG.Wait is wrapped in a
	// belt-and-braces timeout: if a docker exec hangs past every
	// per-call deadline AND past ctxTrial's deadline (shouldn't
	// happen, but did once at 50K), the trial still ends cleanly
	// and writes summary.json from whatever data did land.
	wg.Wait()
	<-tickerDone
	pprofDone := make(chan struct{})
	go func() { pprofWG.Wait(); close(pprofDone) }()
	select {
	case <-pprofDone:
	case <-time.After(3 * time.Minute):
		fmt.Fprintf(os.Stderr, "  pprof captures didn't finish within 3min; proceeding to summary\n")
	}

	// Compute summary numbers. Keep latency as float ms so sub-ms
	// loopback percentiles (typically 0.7-1.5 ms) aren't truncated.
	bsnap := baselineHist.Snapshot()
	res.BaselineP50Ms = float64(bsnap.Percentile(50)) / 1e6
	res.BaselineP95Ms = float64(bsnap.Percentile(95)) / 1e6
	res.BaselineP99Ms = float64(bsnap.Percentile(99)) / 1e6
	if opts.baselineSec > 0 {
		res.BaselineThruput = float64(bsnap.Total()) / float64(opts.baselineSec)
	}
	res.FailureCodes = failures.Snapshot()
	if total := bsnap.Total(); total > 0 {
		// Per-phase failure split would be cleaner; for v1 we report
		// cumulative-trial failure ratio relative to baseline successes.
		var allFails int64
		for _, c := range res.FailureCodes {
			allFails += c
		}
		res.BaselineFailureRatio = float64(allFails) / float64(allFails+total)
	}

	// Peak p99 during burst phase + when (rolling per-second).
	baselineP99NS := bsnap.Percentile(99)
	var peakNS int64
	peakAtSec := -1
	settleAtSec := -1
	for _, smp := range series.samples {
		if smp.phase != "burst" {
			continue
		}
		if smp.p99 > peakNS {
			peakNS = smp.p99
			peakAtSec = smp.elapsedSec
		}
		if settleAtSec < 0 && smp.elapsedSec > peakAtSec && peakAtSec >= 0 && smp.p99 < 2*baselineP99NS {
			settleAtSec = smp.elapsedSec - res.ReloadAtSec
		}
	}
	res.PeakP99Ms = float64(peakNS) / 1e6
	res.PeakP99AtSec = peakAtSec
	res.SettleTimeSec = settleAtSec

	for _, s := range serverSeriesV.samples {
		if s.rssMB > res.PeakRSSMB {
			res.PeakRSSMB = s.rssMB
		}
		if s.goroutines > res.PeakGoroutines {
			res.PeakGoroutines = s.goroutines
		}
	}

	if trialDir != "" {
		writeJSON(filepath.Join(trialDir, "summary.json"), res)
		if f, err := os.Create(filepath.Join(trialDir, "latency.csv")); err == nil {
			_ = series.WriteCSV(f)
			f.Close()
		}
		if f, err := os.Create(filepath.Join(trialDir, "server.csv")); err == nil {
			_ = serverSeriesV.WriteCSV(f)
			f.Close()
		}
	}
	_ = burstHist.Snapshot()
	_ = cooldownHist.Snapshot()

	return res, nil
}

// capturePprof runs `docker exec <container> wget -qO- <pprof-path>` and
// writes the resulting bytes to outPath. Each call gets its own 90s
// deadline derived from the trial context — long enough for the 30s CPU
// profile plus generous transport overhead, short enough that a stuck
// docker exec can't hold pprofWG open past one capture's share.
func capturePprof(wg *sync.WaitGroup, ctx context.Context, container, pprofPath, outPath string) {
	defer wg.Done()
	callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(callCtx, "docker", "exec", container,
		"wget", "-qO-", "--timeout=60",
		"http://127.0.0.1:6060"+pprofPath).Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  pprof capture failed (%s): %v\n", pprofPath, err)
		return
	}
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "  pprof write failed (%s): %v\n", outPath, err)
	}
}

// runCheckinLoopGated is the dial-then-barrier variant used by runTrial.
// Each envoy dials its gRPC connection, signals dialReady, then blocks
// on startGate until the main goroutine releases everyone simultaneously.
// This makes the trial clock honest: the warmup phase begins after every
// envoy is connected, not while the launcher is still spawning goroutines.
func runCheckinLoopGated(
	ctx context.Context,
	grpcAddr string,
	e *testEnvoy,
	interval time.Duration,
	perSecond, baselineHist, burstHist, cooldownHist *Histogram,
	failures *failureBreakdown,
	throughput *atomic.Int64,
	wg *sync.WaitGroup,
	phaseFor func(int) string,
	trialStart *time.Time,
	dialReady chan<- struct{},
	startGate <-chan struct{},
	dialFailed *atomic.Int64,
) bool {
	defer wg.Done()

	cfg, err := newTLSConfigFor(e)
	if err != nil {
		failures.Record(err)
		dialFailed.Add(1)
		return false
	}
	creds := credentials.NewTLS(cfg)
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		failures.Record(err)
		dialFailed.Add(1)
		return false
	}
	defer conn.Close()
	client := pb.NewVigoAgentClient(conn)

	dialReady <- struct{}{}
	select {
	case <-ctx.Done():
		return false
	case <-startGate:
	}

	// Per-envoy random initial delay so the first CheckIn cycle is
	// spread across the interval window rather than firing 50K tickers
	// at +0s. Real agents have natural process-start jitter (different
	// boot times, restart waves, network delays); without simulating
	// it, loopback at 50K causes a synchronized 50K-RPS connection
	// burst that overwhelms the server's accept path before any
	// steady-state CheckIn ever lands. randInitialDelay returns a
	// uniformly-random sleep in [0, interval).
	select {
	case <-ctx.Done():
		return false
	case <-time.After(randInitialDelay(interval)):
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	doCheckin := func() {
		start := time.Now()
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		msg := append([]byte(e.stateFingerprint), []byte(ts)...)
		sig := signWith(e, msg)
		resp, err := client.CheckIn(ctx, &pb.CheckInRequest{
			EnvoyId:          e.id,
			StateFingerprint: e.stateFingerprint,
			Signature:        sig,
			Timestamp:        timestamppb.Now(),
			AgentVersion:     "vigo-burst",
		})
		lat := time.Since(start).Nanoseconds()
		if err != nil {
			failures.Record(err)
			return
		}
		if rf := resp.GetResponseFingerprint(); rf != "" {
			e.stateFingerprint = rf
		}
		perSecond.Record(lat)
		throughput.Add(1)
		elapsed := int(time.Since(*trialStart).Round(time.Second).Seconds())
		switch phaseFor(elapsed) {
		case "baseline":
			baselineHist.Record(lat)
		case "burst":
			burstHist.Record(lat)
		case "cooldown":
			cooldownHist.Record(lat)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return true
		case <-ticker.C:
			doCheckin()
		}
	}
}

// runCheckinLoop is one envoy's CheckIn cycle for the trial duration.
// Records each call's latency into per-second + per-phase histograms;
// fingerprint cycling means steady-state CheckIns are cheap (the
// server short-circuits to no-change) and only the post-reload window
// triggers a full bundle rebuild on the server side.
func runCheckinLoop(
	ctx context.Context,
	grpcAddr string,
	e *testEnvoy,
	interval time.Duration,
	perSecond, baselineHist, burstHist, cooldownHist *Histogram,
	failures *failureBreakdown,
	throughput *atomic.Int64,
	wg *sync.WaitGroup,
	phaseFor func(int) string,
	trialStart time.Time,
) {
	defer wg.Done()

	cfg, err := newTLSConfigFor(e)
	if err != nil {
		failures.Record(err)
		return
	}
	creds := credentials.NewTLS(cfg)
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		failures.Record(err)
		return
	}
	defer conn.Close()
	client := pb.NewVigoAgentClient(conn)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	doCheckin := func() {
		start := time.Now()
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		// Payload-and-timestamp signature shape matches the real agent:
		// the payload byte slice is the state_fingerprint string, the
		// timestamp string is appended, ed25519-signed.
		msg := append([]byte(e.stateFingerprint), []byte(ts)...)
		sig := signWith(e, msg)
		resp, err := client.CheckIn(ctx, &pb.CheckInRequest{
			EnvoyId:          e.id,
			StateFingerprint: e.stateFingerprint,
			Signature:        sig,
			Timestamp:        timestamppb.Now(),
			AgentVersion:     "vigo-burst",
		})
		lat := time.Since(start).Nanoseconds()
		if err != nil {
			failures.Record(err)
			return
		}
		// Echo the server's response_fingerprint back next CheckIn — this
		// is what makes steady-state cheap. After a reload the server's
		// fingerprint differs and our cached one is stale → full bundle
		// path until the new fingerprint is captured here.
		if rf := resp.GetResponseFingerprint(); rf != "" {
			e.stateFingerprint = rf
		}
		perSecond.Record(lat)
		throughput.Add(1)
		elapsed := int(time.Since(trialStart).Round(time.Second).Seconds())
		switch phaseFor(elapsed) {
		case "baseline":
			baselineHist.Record(lat)
		case "burst":
			burstHist.Record(lat)
		case "cooldown":
			cooldownHist.Record(lat)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			doCheckin()
		}
	}
}

// signWith wraps ed25519.Sign so the signing-key access is in one place.
// (Direct call would do; keeping it as a helper aids future swap to
// hardware-signer indirection.)
func signWith(e *testEnvoy, msg []byte) []byte {
	return ed25519.Sign(e.signingKey, msg)
}

// -- reporting --

func printTrialSummary(r trialResult) {
	fmt.Printf("\n  ── trial summary ──\n")
	fmt.Printf("  baseline   p50=%.2fms  p95=%.2fms  p99=%.2fms\n",
		r.BaselineP50Ms, r.BaselineP95Ms, r.BaselineP99Ms)
	fmt.Printf("  baseline   throughput=%.0f/s   failure_ratio=%.4f%%\n",
		r.BaselineThruput, r.BaselineFailureRatio*100)
	if r.ReloadAtSec >= 0 {
		fmt.Printf("  burst      reload@%ds → peak p99=%.2fms at +%ds\n",
			r.ReloadAtSec, r.PeakP99Ms, r.PeakP99AtSec-r.ReloadAtSec)
		if r.SettleTimeSec >= 0 {
			fmt.Printf("  burst      settle (p99 ≤ 2× baseline) at +%ds\n", r.SettleTimeSec)
		} else {
			fmt.Printf("  burst      did NOT settle within burst window\n")
		}
	}
	if r.PeakRSSMB > 0 {
		fmt.Printf("  server     peak_rss=%dMB  peak_goroutines=%d\n", r.PeakRSSMB, r.PeakGoroutines)
	}
	if len(r.FailureCodes) > 0 {
		var keys []string
		for k := range r.FailureCodes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("  failures by code:\n")
		for _, k := range keys {
			fmt.Printf("    %-22s %d\n", k, r.FailureCodes[k])
		}
	}
}

func printAggregate(trials []trialResult, outputDir string) {
	if len(trials) < 2 {
		return
	}
	// Mean + stdev over numeric headline fields.
	mean := func(get func(trialResult) float64) (float64, float64) {
		var sum float64
		for _, t := range trials {
			sum += get(t)
		}
		m := sum / float64(len(trials))
		var sq float64
		for _, t := range trials {
			d := get(t) - m
			sq += d * d
		}
		s := 0.0
		if len(trials) > 1 {
			s = sq / float64(len(trials)-1)
			s = sqrt(s)
		}
		return m, s
	}
	bm, bs := mean(func(t trialResult) float64 { return float64(t.BaselineP99Ms) })
	pm, ps := mean(func(t trialResult) float64 { return float64(t.PeakP99Ms) })
	stm, sts := mean(func(t trialResult) float64 { return float64(t.SettleTimeSec) })

	fmt.Printf("\n=== aggregate over %d trials ===\n", len(trials))
	fmt.Printf("  baseline_p99_ms   mean=%.0f  stdev=%.0f\n", bm, bs)
	fmt.Printf("  peak_p99_ms       mean=%.0f  stdev=%.0f\n", pm, ps)
	fmt.Printf("  settle_time_sec   mean=%.1f  stdev=%.1f\n", stm, sts)

	if outputDir != "" {
		writeJSON(filepath.Join(outputDir, "aggregate.json"), map[string]any{
			"trials":            len(trials),
			"baseline_p99_mean": bm, "baseline_p99_stdev": bs,
			"peak_p99_mean": pm, "peak_p99_stdev": ps,
			"settle_time_mean": stm, "settle_time_stdev": sts,
		})
	}
}

func evalSLA(trials []trialResult, p99 time.Duration, settleSec int, failRate float64) int {
	if len(trials) == 0 {
		return 0
	}
	var bad bool
	last := trials[len(trials)-1]
	if p99 > 0 && last.BaselineP99Ms > float64(p99.Milliseconds()) {
		fmt.Fprintf(os.Stderr, "SLA: baseline_p99 %.2fms > target %dms\n", last.BaselineP99Ms, p99.Milliseconds())
		bad = true
	}
	if settleSec > 0 && last.SettleTimeSec > settleSec {
		fmt.Fprintf(os.Stderr, "SLA: settle_time %ds > target %ds\n", last.SettleTimeSec, settleSec)
		bad = true
	}
	if failRate > 0 && last.BaselineFailureRatio > failRate {
		fmt.Fprintf(os.Stderr, "SLA: failure_ratio %.4f > target %.4f\n", last.BaselineFailureRatio, failRate)
		bad = true
	}
	if bad {
		return 1
	}
	return 0
}

// -- environment helpers --

func hostnameOrUnknown() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func serverYAMLHash(ctx context.Context, container string) string {
	if container == "" {
		return ""
	}
	out, err := execIn(ctx, container, "sh", "-c", "sha256sum /srv/vigo/server.yaml 2>/dev/null | cut -d' ' -f1")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func vigoVersion(ctx context.Context, container string) string {
	if container == "" {
		return ""
	}
	out, err := exec.CommandContext(ctx, "docker", "exec", container, "vigosrv", "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func writeJSON(path string, v any) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func sqrt(x float64) float64 { return math.Sqrt(x) }

// randInitialDelay returns a uniformly-random delay in [0, interval).
// Used to spread the first CheckIn cycle across the interval window so
// the test doesn't fire a synchronized N-RPS connection-establishment
// burst at the server. math/rand is fine here — this is test jitter,
// not security-sensitive.
func randInitialDelay(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	return time.Duration(mathRandInt63n(int64(interval)))
}
