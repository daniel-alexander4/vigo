// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// vigo-resultstorm measures the server's run-result *ingestion* path
// under sustained write load — the half of the agent lifecycle the
// CheckIn-only harnesses (test/loadtest, test/burst) never exercise.
//
// N enrolled envoys each fire ReportResult at a fixed interval; every
// report carries M ResourceRunResults. The server, per report, creates a run
// row, batch-inserts M run_result rows, finishes the run, updates the
// in-memory fleet index, dispatches integration events, and (when
// --snapshot-bytes>0 on changed file results) inserts a file snapshot.
// That makes the storm a direct probe of the SQLite single-writer wall:
// rows/sec is the headline number, and ReportResult p99 climbing as the
// run_results table grows is the symptom to watch.
//
// The synthetic envoys never CheckIn — enrollment records the pubkey,
// and ReportResult only needs that plus a valid signature, so the tool
// goes straight to the write storm. See README.md for methodology.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
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

const toolVersion = "0.1"

func main() {
	grpcAddr := flag.String("server", "localhost:1530", "gRPC server address (host:port)")
	apiAddr := flag.String("api", "https://localhost:8443", "REST API base (enroll + cleanup)")
	count := flag.Int("count", 1000, "number of simulated envoys")
	interval := flag.Duration("interval", 5*time.Second, "per-envoy ReportResult interval")
	resultsPerReport := flag.Int("results-per-report", 12, "ResourceRunResults per report (= run_result rows written per report)")
	changedRatio := flag.Float64("changed-ratio", 0.15, "fraction of results with changed=true (drives drift.detected + DriftDetected metric + snapshot eligibility)")
	errorRatio := flag.Float64("error-ratio", 0.02, "fraction of results carrying an error_message (drives 'failure' run status)")
	snapshotBytes := flag.Int("snapshot-bytes", 0, "if >0, changed file results carry this many bytes of previous_content → exercises the InsertFileSnapshot write path (heavier; off by default)")
	warmupSec := flag.Int("warmup", 30, "warmup phase (seconds) — discarded from measured percentiles")
	durationSec := flag.Int("duration", 300, "measured phase (seconds)")
	rpcTimeout := flag.Duration("rpc-timeout", 30*time.Second, "per-ReportResult deadline; exceeded → DeadlineExceeded (write queued behind the SQLite writer too long)")
	enrollmentQuiesceSec := flag.Int("enrollment-quiesce-sec", 10, "seconds between enrollment and first report; lets CSR signing settle")
	adminToken := flag.String("admin-token", "", "admin bearer token (e.g. $(sudo cat /srv/vigo/cli-admin-token)); required for cleanup")
	hostnamePrefix := flag.String("hostname-prefix", "storm-", "hostname prefix; bootstrap trusted_enrollment pattern must accept this")
	serverContainer := flag.String("server-container", "vigo", "docker container for server-side pprof sampling; empty disables")
	serverSampleInterval := flag.Duration("server-sample-interval", 5*time.Second, "cadence for server-side resource samples")
	outputDir := flag.String("output", "", "directory for manifest/summary/CSV; empty prints summary only")
	skipCleanup := flag.Bool("skip-cleanup", false, "leave simulated envoys enrolled after the run")
	startAt := flag.String("start-at", "", "RFC3339 instant to begin the measured phases; empty starts once every envoy is dialed. Pass the same value to two driver hosts to align their phase clocks.")
	notes := flag.String("notes", "", "freeform notes embedded in manifest.json (e.g. hardware label)")
	flag.Parse()

	if *adminToken == "" {
		fmt.Fprintln(os.Stderr, "--admin-token is required (cleanup needs it)")
		os.Exit(2)
	}
	if *resultsPerReport < 1 {
		fmt.Fprintln(os.Stderr, "--results-per-report must be >= 1")
		os.Exit(2)
	}

	var startAtTime time.Time
	if *startAt != "" {
		t, err := time.Parse(time.RFC3339, *startAt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "--start-at: %v (want RFC3339, e.g. 2026-05-29T15:30:00Z)\n", err)
			os.Exit(2)
		}
		startAtTime = t
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	tlsClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   30 * time.Second,
	}

	// Redact the admin token before it's persisted to the manifest.
	cli := append([]string{}, os.Args[1:]...)
	for i, a := range cli {
		switch {
		case a == "--admin-token" || a == "-admin-token":
			if i+1 < len(cli) {
				cli[i+1] = "***"
			}
		case strings.HasPrefix(a, "--admin-token=") || strings.HasPrefix(a, "-admin-token="):
			cli[i] = strings.SplitN(a, "=", 2)[0] + "=***"
		}
	}

	man := manifest{
		ToolVersion:          toolVersion,
		StartedUTC:           time.Now().UTC(),
		Hostname:             hostnameOrUnknown(),
		OS:                   runtime.GOOS + "/" + runtime.GOARCH,
		ServerAddr:           *grpcAddr,
		APIAddr:              *apiAddr,
		ServerContainer:      *serverContainer,
		EnvoyCount:           *count,
		Interval:             interval.String(),
		ResultsPerReport:     *resultsPerReport,
		ChangedRatio:         *changedRatio,
		ErrorRatio:           *errorRatio,
		SnapshotBytes:        *snapshotBytes,
		EnrollmentQuiesceSec: *enrollmentQuiesceSec,
		WarmupSec:            *warmupSec,
		DurationSec:          *durationSec,
		HostnamePrefix:       *hostnamePrefix,
		CLIArgs:              cli,
		ServerYAMLSHA256:     serverYAMLHash(ctx, *serverContainer),
		VigoVersion:          vigoVersion(ctx, *serverContainer),
		Notes:                *notes,
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

	offered := float64(*count) / interval.Seconds()
	fmt.Printf("vigo-resultstorm %s\n", toolVersion)
	fmt.Printf("  server:    %s\n", *grpcAddr)
	fmt.Printf("  envoys:    %d @ %v interval\n", *count, *interval)
	fmt.Printf("  payload:   %d results/report → offered ≈ %.0f reports/s, %.0f rows/s\n",
		*resultsPerReport, offered, offered*float64(*resultsPerReport))
	fmt.Printf("  phases:    warmup=%ds measured=%ds\n", *warmupSec, *durationSec)
	fmt.Println()

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
		fmt.Printf("  deleted %d/%d\n", cleanupEnvoys(tlsClient, *apiAddr, *adminToken, envoys), len(envoys))
	}()

	if *enrollmentQuiesceSec > 0 {
		quiesce := time.Duration(*enrollmentQuiesceSec) * time.Second
		fmt.Printf("quiescing %v for CSR signing to settle...\n\n", quiesce)
		select {
		case <-ctx.Done():
			return
		case <-time.After(quiesce):
		}
	}

	runStorm(ctx, stormOpts{
		envoys:               envoys,
		grpcAddr:             *grpcAddr,
		interval:             *interval,
		resultsPerReport:     *resultsPerReport,
		changedRatio:         *changedRatio,
		errorRatio:           *errorRatio,
		snapshotBytes:        *snapshotBytes,
		warmupSec:            *warmupSec,
		durationSec:          *durationSec,
		rpcTimeout:           *rpcTimeout,
		serverContainer:      *serverContainer,
		serverSampleInterval: *serverSampleInterval,
		outputDir:            *outputDir,
		startAt:              startAtTime,
	})
}

type stormOpts struct {
	envoys               []testEnvoy
	grpcAddr             string
	interval             time.Duration
	resultsPerReport     int
	changedRatio         float64
	errorRatio           float64
	snapshotBytes        int
	warmupSec            int
	durationSec          int
	rpcTimeout           time.Duration
	serverContainer      string
	serverSampleInterval time.Duration
	outputDir            string
	startAt              time.Time
}

func runStorm(ctx context.Context, opts stormOpts) {
	var (
		perSecond     Histogram // reset each tick → per-second percentiles
		measuredHist  Histogram // accumulates the measured window → headline percentiles
		failures      failureBreakdown
		series        resultSeries
		serverSeriesV serverSeries

		throughput     atomic.Int64 // successful reports this second (reset each tick)
		measuredReport atomic.Int64
		measuredRows   atomic.Int64
		measuredFail   atomic.Int64
	)

	warmupEnd := opts.warmupSec
	measuredEnd := warmupEnd + opts.durationSec
	phaseFor := func(sec int) string {
		if sec < warmupEnd {
			return "warmup"
		}
		return "measured"
	}

	startLead := time.Duration(0)
	if !opts.startAt.IsZero() {
		if d := time.Until(opts.startAt); d > 0 {
			startLead = d
		}
	}
	ctxStorm, cancelStorm := context.WithTimeout(ctx,
		time.Duration(measuredEnd)*time.Second+startLead+5*time.Minute)
	defer cancelStorm()

	var (
		wg         sync.WaitGroup
		dialReady  = make(chan struct{}, len(opts.envoys))
		startGate  = make(chan struct{})
		dialFailed atomic.Int64
		trialStart time.Time
	)

	for i := range opts.envoys {
		// Per-envoy deterministic result mix so the fleet is heterogeneous
		// (some envoys report more drift / errors than others) but each
		// envoy's payload is fixed — built once, reused every report — so
		// the client doesn't churn the GC and distort its own latency
		// measurement.
		tmpl := buildResultTemplate(opts.resultsPerReport, opts.changedRatio, opts.errorRatio,
			opts.snapshotBytes, rand.New(rand.NewSource(int64(i)+1)))
		rows := countRows(tmpl)
		wg.Add(1)
		go func(e *testEnvoy, tmpl []*pb.ResourceRunResult, rows int64) {
			defer wg.Done()
			runReportLoop(ctxStorm, reportLoopArgs{
				grpcAddr: opts.grpcAddr, e: e, tmpl: tmpl, rows: rows,
				interval: opts.interval, rpcTimeout: opts.rpcTimeout,
				perSecond: &perSecond, measured: &measuredHist, failures: &failures,
				throughput: &throughput, measuredReport: &measuredReport,
				measuredRows: &measuredRows, measuredFail: &measuredFail,
				phaseFor: phaseFor, trialStart: &trialStart,
				dialReady: dialReady, startGate: startGate, dialFailed: &dialFailed,
			})
		}(&opts.envoys[i], tmpl, rows)
	}

	// Barrier: wait for every envoy to dial (or fail) before the phase
	// clock starts, so warmup/measured boundaries are honest.
	fmt.Printf("dialing %d envoys...\n", len(opts.envoys))
	ready := 0
	for ready < len(opts.envoys) {
		select {
		case <-ctxStorm.Done():
			fmt.Fprintln(os.Stderr, "cancelled during dial")
			return
		case <-dialReady:
			ready++
		}
	}
	if df := dialFailed.Load(); df > 0 {
		fmt.Printf("  %d envoys failed to dial\n", df)
	}
	if !opts.startAt.IsZero() {
		if d := time.Until(opts.startAt); d > 0 {
			fmt.Printf("  holding %v until --start-at...\n", d.Round(time.Second))
			select {
			case <-ctxStorm.Done():
				return
			case <-time.After(d):
			}
		}
	}
	trialStart = time.Now()
	close(startGate)
	fmt.Printf("storm started — warmup %ds, measured %ds\n\n", opts.warmupSec, opts.durationSec)

	// Server-side sampling goroutine.
	var sampWG sync.WaitGroup
	if opts.serverContainer != "" {
		sampWG.Add(1)
		go func() {
			defer sampWG.Done()
			t := time.NewTicker(opts.serverSampleInterval)
			defer t.Stop()
			for {
				select {
				case <-ctxStorm.Done():
					return
				case <-t.C:
					elapsed := int(time.Since(trialStart).Seconds())
					if elapsed > measuredEnd {
						return
					}
					serverSeriesV.Append(pollServer(ctxStorm, opts.serverContainer, phaseFor(elapsed), elapsed))
				}
			}
		}()
	}

	// Per-second time-series ticker.
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctxStorm.Done():
			goto done
		case <-tick.C:
			elapsed := int(time.Since(trialStart).Seconds())
			snap := perSecond.SnapshotAndReset()
			reports := throughput.Swap(0)
			fi, fe, fo := failures.SwapPerSecond()
			phase := phaseFor(elapsed)
			series.Append(resultSample{
				elapsedSec: elapsed, phase: phase,
				reports: reports, rows: reports * int64(opts.resultsPerReport),
				p50: snap.Percentile(50), p95: snap.Percentile(95),
				p99: snap.Percentile(99), p999: snap.Percentile(99.9),
				maxObserved:  snap.Max(),
				failInternal: fi, failExhausted: fe, failOther: fo,
			})
			fmt.Printf("  [%4ds %-8s] %6d rep/s  %8d row/s  p50=%5.1fms p99=%6.1fms max=%6.1fms  fail(int=%d exh=%d oth=%d)\n",
				elapsed, phase, reports, reports*int64(opts.resultsPerReport),
				float64(snap.Percentile(50))/1e6, float64(snap.Percentile(99))/1e6,
				float64(snap.Max())/1e6, fi, fe, fo)
			if elapsed >= measuredEnd {
				goto done
			}
		}
	}
done:
	cancelStorm()
	wg.Wait()
	sampWG.Wait()

	printSummary(opts, &measuredHist, &failures,
		measuredReport.Load(), measuredRows.Load(), measuredFail.Load())

	if opts.outputDir != "" {
		writeCSV(filepath.Join(opts.outputDir, "results.csv"), series.WriteCSV)
		if opts.serverContainer != "" {
			writeCSV(filepath.Join(opts.outputDir, "server.csv"), serverSeriesV.WriteCSV)
		}
		fmt.Printf("\nwrote results.csv + manifest.json to %s\n", opts.outputDir)
	}
}

type reportLoopArgs struct {
	grpcAddr                   string
	e                          *testEnvoy
	tmpl                       []*pb.ResourceRunResult
	rows                       int64
	interval, rpcTimeout       time.Duration
	perSecond, measured        *Histogram
	failures                   *failureBreakdown
	throughput, measuredReport *atomic.Int64
	measuredRows, measuredFail *atomic.Int64
	phaseFor                   func(int) string
	trialStart                 *time.Time
	dialReady                  chan<- struct{}
	startGate                  <-chan struct{}
	dialFailed                 *atomic.Int64
}

func runReportLoop(ctx context.Context, a reportLoopArgs) {
	tlsCfg, err := newTLSConfigFor(a.e)
	if err != nil {
		a.dialFailed.Add(1)
		a.dialReady <- struct{}{}
		return
	}
	// grpc.NewClient is lazy — no handshake until the first RPC — so this
	// is cheap even at high envoy counts.
	conn, err := grpc.NewClient(a.grpcAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		a.dialFailed.Add(1)
		a.dialReady <- struct{}{}
		return
	}
	defer conn.Close()
	client := pb.NewVigoAgentClient(conn)

	a.dialReady <- struct{}{}
	select {
	case <-ctx.Done():
		return
	case <-a.startGate:
	}

	// Spread the first report across the interval so the fleet doesn't
	// fire one synchronized write burst at t=0.
	select {
	case <-ctx.Done():
		return
	case <-time.After(randInitialDelay(a.interval)):
	}

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	var seq int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			seq++
			now := time.Now()
			runID := a.e.id + "-" + strconv.FormatInt(seq, 10)
			tsStr := strconv.FormatInt(now.Unix(), 10)
			// ReportResult signs run_id || unix-seconds; derive both from
			// one instant so they can't straddle a second boundary.
			sig := signWith(a.e, append([]byte(runID), tsStr...))

			callCtx, cancelCall := context.WithTimeout(ctx, a.rpcTimeout)
			start := time.Now()
			_, rpcErr := client.ReportResult(callCtx, &pb.ReportResultRequest{
				RunId:     runID,
				EnvoyId:   a.e.id,
				Results:   a.tmpl,
				Signature: sig,
				Timestamp: timestamppb.New(now),
			})
			cancelCall()

			lat := time.Since(start).Nanoseconds()
			elapsed := int(time.Since(*a.trialStart).Seconds())
			measured := a.phaseFor(elapsed) == "measured"
			a.perSecond.Record(lat)
			if measured {
				a.measured.Record(lat)
			}
			if rpcErr != nil {
				a.failures.Record(rpcErr)
				if measured {
					a.measuredFail.Add(1)
				}
				continue
			}
			a.throughput.Add(1)
			if measured {
				a.measuredReport.Add(1)
				a.measuredRows.Add(a.rows)
			}
		}
	}
}

// buildResultTemplate constructs one envoy's fixed per-report payload of
// M ResourceRunResults with a randomized changed/error mix. Reused on every
// report so the client stays allocation-light.
func buildResultTemplate(m int, changedRatio, errorRatio float64, snapshotBytes int, rng *rand.Rand) []*pb.ResourceRunResult {
	types := []string{"file", "package", "service"}
	var snap []byte
	if snapshotBytes > 0 {
		snap = make([]byte, snapshotBytes)
		for i := range snap {
			snap[i] = 'x'
		}
	}
	out := make([]*pb.ResourceRunResult, m)
	for j := 0; j < m; j++ {
		rt := types[j%len(types)]
		changed := rng.Float64() < changedRatio
		action := "settled"
		if changed {
			action = "updated"
		}
		mr := &pb.ResourceRunResult{
			ConfigcrateName: fmt.Sprintf("mod-%02d", j),
			ResourceType:    rt,
			ResourceName:    fmt.Sprintf("res-%02d", j),
			ActionTaken:     action,
			Changed:         changed,
			DurationMs:      int32(rng.Intn(50)),
		}
		// error_message with a non-blocked action → server counts a
		// failed result → "failure" run status.
		if rng.Float64() < errorRatio {
			mr.ErrorMessage = "simulated convergence error"
		}
		if changed && rt == "file" && snapshotBytes > 0 {
			mr.PreviousContent = snap
			mr.FilePath = fmt.Sprintf("/etc/vigo-storm/res-%02d.conf", j)
			mr.ContentHash = "0000000000000000000000000000000000000000000000000000000000000000"
		}
		out[j] = mr
	}
	return out
}

func countRows(tmpl []*pb.ResourceRunResult) int64 { return int64(len(tmpl)) }

func signWith(e *testEnvoy, msg []byte) []byte { return ed25519.Sign(e.signingKey, msg) }

func randInitialDelay(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(interval)))
}

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

func printSummary(opts stormOpts, measured *Histogram, failures *failureBreakdown, reports, rows, fails int64) {
	snap := measured.Snapshot()
	dur := float64(opts.durationSec)
	fmt.Printf("\n  ── measured window (%ds) ──\n", opts.durationSec)
	fmt.Printf("  reports    %d ok  (%.0f/s)\n", reports, float64(reports)/dur)
	fmt.Printf("  rows       %d written  (%.0f/s)  ← run_result INSERTs\n", rows, float64(rows)/dur)
	fmt.Printf("  latency    p50=%.2fms  p95=%.2fms  p99=%.2fms  p99.9=%.2fms  max=%.2fms\n",
		ms(snap.Percentile(50)), ms(snap.Percentile(95)), ms(snap.Percentile(99)),
		ms(snap.Percentile(99.9)), ms(snap.Max()))
	total := reports + fails
	ratio := 0.0
	if total > 0 {
		ratio = float64(fails) / float64(total)
	}
	fmt.Printf("  failures   %d  (%.4f%%)\n", fails, ratio*100)
	if fc := failures.Snapshot(); len(fc) > 0 {
		var keys []string
		for k := range fc {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("  by code (cumulative incl. warmup):\n")
		for _, k := range keys {
			fmt.Printf("    %-22s %d\n", k, fc[k])
		}
	}
}

func ms(ns int64) float64 { return float64(ns) / 1e6 }

// writeCSV opens path and hands the file to the series' WriteCSV method.
func writeCSV(path string, write func(io.Writer) error) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create %s: %v\n", path, err)
		return
	}
	defer f.Close()
	if err := write(f); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
	}
}
