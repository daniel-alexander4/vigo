// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// vigo-burstwave drives two overlapping read+write bursts against the server
// to test behavior when a second convergence wave hits before the first
// drains. Each synthetic envoy runs the real agent cycle every interval —
// CheckIn (read: pull policy) then ReportResult (write: persist run results) —
// so the burst exercises both the SplitDB read pool and the single-writer pool.
//
// Two cohorts (A and B) are enrolled up front. Cohort A storms from t0; cohort
// B's storm is released when A reaches --second-burst-at of its measured window
// (0.8 = 80%), overlapping A's tail and roughly doubling the offered load for
// the overlap. The per-second CSV splits CheckIn (read) latency/rate from
// ReportResult (write) latency/rate/shed and marks how many cohorts are firing,
// so the question — do reads stay fast while writes shed harder under the
// doubled write load? — is answerable from the time series.
//
// Note: CheckIn is read-mostly but largely served from the in-memory
// FleetIndex (sig-verify + bundle build + a batched last_seen write), so its
// latency reflects the check-in path, not pure read-pool contention. The write
// path (ReportResult → single writer + admission semaphore) is where the
// overlap bites. Reuses test/resultstorm's enroll/histogram/sampling building
// blocks (per-harness duplication, as resultstorm did from burst).
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
	adminToken := flag.String("admin-token", "", "admin bearer token (e.g. $(sudo cat /srv/vigo/cli-admin-token)); required for cleanup")
	count := flag.Int("count", 1000, "envoys in the first burst (cohort A)")
	secondCount := flag.Int("second-count", 0, "envoys in the second burst (cohort B); 0 = same as --count")
	interval := flag.Duration("interval", 5*time.Second, "per-envoy cycle interval (one CheckIn + one ReportResult)")
	resultsPerReport := flag.Int("results-per-report", 12, "ConfigcrateResults per ReportResult (run_result rows written per report)")
	changedRatio := flag.Float64("changed-ratio", 0.15, "fraction of results with changed=true")
	errorRatio := flag.Float64("error-ratio", 0.02, "fraction of results carrying an error_message")
	snapshotBytes := flag.Int("snapshot-bytes", 0, "if >0, changed file results carry this many bytes of previous_content")
	warmupSec := flag.Int("warmup", 30, "warmup phase (seconds) for cohort A — discarded from A's measured percentiles")
	durationSec := flag.Int("duration", 300, "measured/firing window (seconds) for each cohort")
	secondBurstAt := flag.Float64("second-burst-at", 0.8, "release cohort B at this fraction of A's measured window (0.8 = 80%)")
	rpcTimeout := flag.Duration("rpc-timeout", 30*time.Second, "per-RPC deadline for both CheckIn and ReportResult")
	enrollmentQuiesceSec := flag.Int("enrollment-quiesce-sec", 10, "seconds between enrollment and first cycle; lets CSR signing settle")
	hostnamePrefix := flag.String("hostname-prefix", "wave-", "hostname prefix; bootstrap trusted_enrollment must accept it")
	serverContainer := flag.String("server-container", "vigo", "docker container for server-side pprof sampling; empty disables")
	serverSampleInterval := flag.Duration("server-sample-interval", 5*time.Second, "cadence for server-side resource samples")
	outputDir := flag.String("output", "", "directory for manifest/results/server CSVs; empty prints summary only")
	skipCleanup := flag.Bool("skip-cleanup", false, "leave simulated envoys enrolled after the run")
	startAt := flag.String("start-at", "", "RFC3339 instant to begin the measured phases; empty starts once every envoy is dialed")
	notes := flag.String("notes", "", "freeform notes embedded in manifest.json")
	flag.Parse()

	if *adminToken == "" {
		fmt.Fprintln(os.Stderr, "--admin-token is required (cleanup needs it)")
		os.Exit(2)
	}
	if *resultsPerReport < 1 {
		fmt.Fprintln(os.Stderr, "--results-per-report must be >= 1")
		os.Exit(2)
	}
	if *secondBurstAt < 0 || *secondBurstAt > 1 {
		fmt.Fprintln(os.Stderr, "--second-burst-at must be in [0,1]")
		os.Exit(2)
	}
	countB := *secondCount
	if countB <= 0 {
		countB = *count
	}

	var startAtTime time.Time
	if *startAt != "" {
		t, err := time.Parse(time.RFC3339, *startAt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "--start-at: %v (want RFC3339)\n", err)
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

	cli := redactedArgs()
	man := manifest{
		ToolVersion:          toolVersion,
		StartedUTC:           time.Now().UTC(),
		Hostname:             hostnameOrUnknown(),
		OS:                   runtime.GOOS + "/" + runtime.GOARCH,
		ServerAddr:           *grpcAddr,
		APIAddr:              *apiAddr,
		ServerContainer:      *serverContainer,
		EnvoyCount:           *count,
		SecondCount:          countB,
		SecondBurstAt:        *secondBurstAt,
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
		_ = writeManifest(filepath.Join(*outputDir, "manifest.json"), man)
	}

	offeredA := float64(*count) / interval.Seconds()
	fmt.Printf("vigo-burstwave %s\n", toolVersion)
	fmt.Printf("  server:    %s\n", *grpcAddr)
	fmt.Printf("  cohort A:  %d envoys @ %v (CheckIn+ReportResult) → ~%.0f checkins/s + ~%.0f reports/s\n", *count, *interval, offeredA, offeredA)
	fmt.Printf("  cohort B:  %d envoys, released at %.0f%% of A's measured window\n", countB, *secondBurstAt*100)
	fmt.Printf("  overlap:   ~%.0f checkins/s + ~%.0f reports/s offered\n", offeredA*2, offeredA*2)
	fmt.Println()

	cohortA, failA := enrollAll(tlsClient, *apiAddr, *hostnamePrefix+"a-", *count)
	cohortB, failB := enrollAll(tlsClient, *apiAddr, *hostnamePrefix+"b-", countB)
	if len(cohortA) == 0 {
		fmt.Fprintln(os.Stderr, "no cohort-A envoys enrolled — check trusted_enrollment pattern")
		os.Exit(2)
	}
	fmt.Printf("enrolled A=%d/%d B=%d/%d (failed A=%d B=%d)\n\n", len(cohortA), *count, len(cohortB), countB, failA, failB)

	defer func() {
		if *skipCleanup {
			return
		}
		all := append(append([]testEnvoy{}, cohortA...), cohortB...)
		fmt.Printf("\ncleaning up %d envoys...\n", len(all))
		fmt.Printf("  deleted %d/%d\n", cleanupEnvoys(tlsClient, *apiAddr, *adminToken, all), len(all))
	}()

	if *enrollmentQuiesceSec > 0 {
		q := time.Duration(*enrollmentQuiesceSec) * time.Second
		fmt.Printf("quiescing %v for CSR signing to settle...\n\n", q)
		select {
		case <-ctx.Done():
			return
		case <-time.After(q):
		}
	}

	runWave(ctx, waveOpts{
		cohortA:              cohortA,
		cohortB:              cohortB,
		grpcAddr:             *grpcAddr,
		interval:             *interval,
		resultsPerReport:     *resultsPerReport,
		changedRatio:         *changedRatio,
		errorRatio:           *errorRatio,
		snapshotBytes:        *snapshotBytes,
		warmupSec:            *warmupSec,
		durationSec:          *durationSec,
		secondBurstAt:        *secondBurstAt,
		rpcTimeout:           *rpcTimeout,
		serverContainer:      *serverContainer,
		serverSampleInterval: *serverSampleInterval,
		outputDir:            *outputDir,
		startAt:              startAtTime,
	})
}

// redactedArgs returns os.Args with the admin token scrubbed, for the manifest.
func redactedArgs() []string {
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
	return cli
}

// waveMetrics bundles the read (CheckIn) and write (ReportResult) histograms +
// per-second counters shared across every envoy goroutine. Per-second hists are
// snapshot+reset each tick; the cum hists accumulate for the final summary.
type waveMetrics struct {
	checkInPerSec, checkInCum Histogram
	reportPerSec, reportCum   Histogram

	checkInOKSec, reportOKSec atomic.Int64 // reset each tick
	checkInFailSec            atomic.Int64 // reset each tick
	reportFailures            failureBreakdown

	checkInOKTotal, reportOKTotal atomic.Int64 // cumulative
	reportRowsTotal               atomic.Int64
}

type waveOpts struct {
	cohortA, cohortB     []testEnvoy
	grpcAddr             string
	interval             time.Duration
	resultsPerReport     int
	changedRatio         float64
	errorRatio           float64
	snapshotBytes        int
	warmupSec            int
	durationSec          int
	secondBurstAt        float64
	rpcTimeout           time.Duration
	serverContainer      string
	serverSampleInterval time.Duration
	outputDir            string
	startAt              time.Time
}

func runWave(ctx context.Context, opts waveOpts) {
	m := &waveMetrics{}
	var series burstSeries
	var serverSeriesV serverSeries

	bStartSec := opts.warmupSec + int(opts.secondBurstAt*float64(opts.durationSec))
	endSec := opts.warmupSec + opts.durationSec
	if e := bStartSec + opts.durationSec; e > endSec {
		endSec = e
	}

	startLead := time.Duration(0)
	if !opts.startAt.IsZero() {
		if d := time.Until(opts.startAt); d > 0 {
			startLead = d
		}
	}
	ctxRun, cancelRun := context.WithTimeout(ctx, time.Duration(endSec)*time.Second+startLead+5*time.Minute)
	defer cancelRun()

	total := len(opts.cohortA) + len(opts.cohortB)
	var (
		wg         sync.WaitGroup
		dialReady  = make(chan struct{}, total)
		gateA      = make(chan struct{})
		gateB      = make(chan struct{})
		dialFailed atomic.Int64
		trialStart time.Time
	)

	launch := func(cohort []testEnvoy, gate <-chan struct{}) {
		for i := range cohort {
			tmpl := buildResultTemplate(opts.resultsPerReport, opts.changedRatio, opts.errorRatio,
				opts.snapshotBytes, rand.New(rand.NewSource(int64(i)+1)))
			rows := int64(len(tmpl))
			wg.Add(1)
			go func(e *testEnvoy, tmpl []*pb.ConfigcrateResult, rows int64) {
				defer wg.Done()
				runAgentLoop(ctxRun, reportLoopArgs{
					grpcAddr: opts.grpcAddr, e: e, tmpl: tmpl, rows: rows,
					interval: opts.interval, rpcTimeout: opts.rpcTimeout,
					m: m, gate: gate, trialStart: &trialStart,
					dialReady: dialReady, dialFailed: &dialFailed,
				})
			}(&cohort[i], tmpl, rows)
		}
	}
	launch(opts.cohortA, gateA)
	launch(opts.cohortB, gateB)

	fmt.Printf("dialing %d envoys (both cohorts)...\n", total)
	ready := 0
	for ready < total {
		select {
		case <-ctxRun.Done():
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
			case <-ctxRun.Done():
				return
			case <-time.After(d):
			}
		}
	}

	trialStart = time.Now()
	close(gateA)
	fmt.Printf("cohort A storming — second burst at +%ds\n\n", bStartSec)
	// Release cohort B at the overlap point.
	go func() {
		select {
		case <-ctxRun.Done():
		case <-time.After(time.Duration(bStartSec) * time.Second):
			close(gateB)
		}
	}()

	var sampWG sync.WaitGroup
	if opts.serverContainer != "" {
		sampWG.Add(1)
		go func() {
			defer sampWG.Done()
			t := time.NewTicker(opts.serverSampleInterval)
			defer t.Stop()
			for {
				select {
				case <-ctxRun.Done():
					return
				case <-t.C:
					elapsed := int(time.Since(trialStart).Seconds())
					if elapsed > endSec {
						return
					}
					phase := "A"
					if len(opts.cohortB) > 0 && elapsed >= bStartSec {
						phase = "A+B"
					}
					serverSeriesV.Append(pollServer(ctxRun, opts.serverContainer, phase, elapsed))
				}
			}
		}()
	}

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctxRun.Done():
			goto done
		case <-tick.C:
			elapsed := int(time.Since(trialStart).Seconds())
			ci := m.checkInPerSec.SnapshotAndReset()
			rr := m.reportPerSec.SnapshotAndReset()
			ciOK := m.checkInOKSec.Swap(0)
			rrOK := m.reportOKSec.Swap(0)
			ciFail := m.checkInFailSec.Swap(0)
			_, exh, other := m.reportFailures.SwapPerSecond()
			active := 1
			if len(opts.cohortB) > 0 && elapsed >= bStartSec {
				active = 2
			}
			series.Append(burstSample{
				elapsedSec: elapsed, activeCohorts: active,
				checkInOK: ciOK, checkInP50: ci.Percentile(50), checkInP99: ci.Percentile(99),
				reportOK: rrOK, reportP50: rr.Percentile(50), reportP99: rr.Percentile(99),
				reportShed: exh, otherFail: other + ciFail,
			})
			fmt.Printf("  [%4ds %dcoh] checkin %5d/s p50=%5.1f p99=%6.1f | report %5d/s p50=%5.1f p99=%6.1f shed=%5d | oth=%d\n",
				elapsed, active,
				ciOK, ms(ci.Percentile(50)), ms(ci.Percentile(99)),
				rrOK, ms(rr.Percentile(50)), ms(rr.Percentile(99)), exh, other+ciFail)
			if elapsed >= endSec {
				goto done
			}
		}
	}
done:
	cancelRun()
	wg.Wait()
	sampWG.Wait()

	printSummary(m)
	if opts.outputDir != "" {
		writeCSV(filepath.Join(opts.outputDir, "results.csv"), series.WriteCSV)
		if opts.serverContainer != "" {
			writeCSV(filepath.Join(opts.outputDir, "server.csv"), serverSeriesV.WriteCSV)
		}
		fmt.Printf("\nwrote results.csv + manifest.json to %s\n", opts.outputDir)
	}
}

type reportLoopArgs struct {
	grpcAddr             string
	e                    *testEnvoy
	tmpl                 []*pb.ConfigcrateResult
	rows                 int64
	interval, rpcTimeout time.Duration
	m                    *waveMetrics
	gate                 <-chan struct{}
	trialStart           *time.Time
	dialReady            chan<- struct{}
	dialFailed           *atomic.Int64
}

// runAgentLoop runs one envoy's full agent cycle: every interval it CheckIns
// (read) then ReportResults (write), recording each path's latency separately.
func runAgentLoop(ctx context.Context, a reportLoopArgs) {
	tlsCfg, err := newTLSConfigFor(a.e)
	if err != nil {
		a.dialFailed.Add(1)
		a.dialReady <- struct{}{}
		return
	}
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
	case <-a.gate: // wait for THIS cohort's release
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(randInitialDelay(a.interval)):
	}

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	fingerprint := "" // echo the server's response fingerprint back next cycle
	var seq int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// CheckIn (read). Signs state_fingerprint || unix-secs from one instant.
			now := time.Now()
			ts := strconv.FormatInt(now.Unix(), 10)
			ciStart := time.Now()
			ciCtx, ciCancel := context.WithTimeout(ctx, a.rpcTimeout)
			resp, ciErr := client.CheckIn(ciCtx, &pb.CheckInRequest{
				EnvoyId:          a.e.id,
				StateFingerprint: fingerprint,
				Signature:        signWith(a.e, append([]byte(fingerprint), ts...)),
				Timestamp:        timestamppb.New(now),
				AgentVersion:     "burstwave",
			})
			ciCancel()
			ciLat := time.Since(ciStart).Nanoseconds()
			a.m.checkInPerSec.Record(ciLat)
			a.m.checkInCum.Record(ciLat)
			if ciErr != nil {
				a.m.checkInFailSec.Add(1)
				a.m.reportFailures.Record(ciErr) // fold into the cumulative code breakdown
			} else {
				a.m.checkInOKSec.Add(1)
				a.m.checkInOKTotal.Add(1)
				if fp := resp.GetResponseFingerprint(); fp != "" {
					fingerprint = fp
				}
			}

			// ReportResult (write). Signs run_id || unix-secs from one instant.
			seq++
			now2 := time.Now()
			ts2 := strconv.FormatInt(now2.Unix(), 10)
			runID := a.e.id + "-" + strconv.FormatInt(seq, 10)
			rrStart := time.Now()
			rrCtx, rrCancel := context.WithTimeout(ctx, a.rpcTimeout)
			_, rrErr := client.ReportResult(rrCtx, &pb.ReportResultRequest{
				RunId:     runID,
				EnvoyId:   a.e.id,
				Results:   a.tmpl,
				Signature: signWith(a.e, append([]byte(runID), ts2...)),
				Timestamp: timestamppb.New(now2),
			})
			rrCancel()
			rrLat := time.Since(rrStart).Nanoseconds()
			a.m.reportPerSec.Record(rrLat)
			a.m.reportCum.Record(rrLat)
			if rrErr != nil {
				a.m.reportFailures.Record(rrErr)
			} else {
				a.m.reportOKSec.Add(1)
				a.m.reportOKTotal.Add(1)
				a.m.reportRowsTotal.Add(a.rows)
			}
		}
	}
}

func buildResultTemplate(n int, changedRatio, errorRatio float64, snapshotBytes int, rng *rand.Rand) []*pb.ConfigcrateResult {
	types := []string{"file", "package", "service"}
	var snap []byte
	if snapshotBytes > 0 {
		snap = make([]byte, snapshotBytes)
		for i := range snap {
			snap[i] = 'x'
		}
	}
	out := make([]*pb.ConfigcrateResult, n)
	for j := 0; j < n; j++ {
		rt := types[j%len(types)]
		changed := rng.Float64() < changedRatio
		action := "settled"
		if changed {
			action = "updated"
		}
		mr := &pb.ConfigcrateResult{
			ConfigcrateName:   fmt.Sprintf("mod-%02d", j),
			ResourceType: rt,
			ResourceName: fmt.Sprintf("res-%02d", j),
			ActionTaken:  action,
			Changed:      changed,
			DurationMs:   int32(rng.Intn(50)),
		}
		if rng.Float64() < errorRatio {
			mr.ErrorMessage = "simulated convergence error"
		}
		if changed && rt == "file" && snapshotBytes > 0 {
			mr.PreviousContent = snap
			mr.FilePath = fmt.Sprintf("/etc/vigo-wave/res-%02d.conf", j)
			mr.ContentHash = "0000000000000000000000000000000000000000000000000000000000000000"
		}
		out[j] = mr
	}
	return out
}

func signWith(e *testEnvoy, msg []byte) []byte { return ed25519.Sign(e.signingKey, msg) }

func randInitialDelay(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(interval)))
}

func enrollAll(client *http.Client, apiAddr, prefix string, count int) ([]testEnvoy, int) {
	if count <= 0 {
		return nil, 0
	}
	fmt.Printf("enrolling %d envoys (%s*)...\n", count, prefix)
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
		if (i+1)%2000 == 0 {
			fmt.Printf("  enrolled %d/%d\n", i+1, count)
		}
	}
	return envoys, fails
}

func cleanupEnvoys(client *http.Client, apiAddr, adminTok string, envoys []testEnvoy) int {
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
			if err := deleteEnvoy(client, apiAddr, adminTok, id); err == nil {
				mu.Lock()
				cleaned++
				mu.Unlock()
			}
		}(e.id)
	}
	wg.Wait()
	return cleaned
}

func printSummary(m *waveMetrics) {
	ci := m.checkInCum.Snapshot()
	rr := m.reportCum.Snapshot()
	fmt.Printf("\n  ── summary ──\n")
	fmt.Printf("  checkin (read)   %d ok   p50=%.2fms p95=%.2fms p99=%.2fms max=%.2fms\n",
		m.checkInOKTotal.Load(), ms(ci.Percentile(50)), ms(ci.Percentile(95)), ms(ci.Percentile(99)), ms(ci.Max()))
	fmt.Printf("  report (write)   %d ok   p50=%.2fms p95=%.2fms p99=%.2fms max=%.2fms\n",
		m.reportOKTotal.Load(), ms(rr.Percentile(50)), ms(rr.Percentile(95)), ms(rr.Percentile(99)), ms(rr.Max()))
	fmt.Printf("  rows written     %d (run_result INSERTs)\n", m.reportRowsTotal.Load())
	if fc := m.reportFailures.Snapshot(); len(fc) > 0 {
		var keys []string
		for k := range fc {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Printf("  failures by code (CheckIn + ReportResult, cumulative):\n")
		for _, k := range keys {
			fmt.Printf("    %-22s %d\n", k, fc[k])
		}
	}
}

func ms(ns int64) float64 { return float64(ns) / 1e6 }

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
