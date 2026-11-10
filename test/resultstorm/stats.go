// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// failureBreakdown counts ReportResult failures by gRPC status code. The
// distinctions matter for write-path diagnosis: Internal almost always
// means a run-store write failed or timed out (the SQLite single-writer
// wall this harness exists to find); ResourceExhausted means the
// load-shedder engaged; DeadlineExceeded means the write queued behind
// the writer long enough to blow the client deadline.
type failureBreakdown struct {
	codes [17]atomic.Int64 // indexed by grpc/codes; 17 = highest defined + 1
	other atomic.Int64     // non-gRPC errors (transport, parse, etc.)

	perSecInternal  atomic.Int64
	perSecExhausted atomic.Int64
	perSecOther     atomic.Int64
}

func (f *failureBreakdown) Record(err error) {
	if err == nil {
		return
	}
	st, ok := status.FromError(err)
	if !ok {
		f.other.Add(1)
		f.perSecOther.Add(1)
		return
	}
	c := st.Code()
	if int(c) < len(f.codes) {
		f.codes[c].Add(1)
	} else {
		f.other.Add(1)
	}
	switch c {
	case codes.Internal:
		f.perSecInternal.Add(1)
	case codes.ResourceExhausted:
		f.perSecExhausted.Add(1)
	default:
		f.perSecOther.Add(1)
	}
}

// SwapPerSecond returns the three per-second deltas and zeros them.
func (f *failureBreakdown) SwapPerSecond() (internal, exhausted, other int64) {
	return f.perSecInternal.Swap(0), f.perSecExhausted.Swap(0), f.perSecOther.Swap(0)
}

func (f *failureBreakdown) Snapshot() map[string]int64 {
	out := map[string]int64{}
	for i := range f.codes {
		if v := f.codes[i].Load(); v > 0 {
			out[codes.Code(i).String()] = v
		}
	}
	if v := f.other.Load(); v > 0 {
		out["other"] = v
	}
	return out
}

// resultSample is one row of the per-second time series. reports/rows are
// deltas over the just-elapsed second; the three failure buckets are
// per-second deltas from failureBreakdown.SwapPerSecond.
type resultSample struct {
	elapsedSec          int
	phase               string // "warmup" or "measured"
	reports             int64  // successful reports this second
	rows                int64  // run_result rows written this second (reports × resultsPerReport)
	p50, p95, p99, p999 int64  // ReportResult latency, nanoseconds
	maxObserved         int64
	failInternal        int64
	failExhausted       int64
	failOther           int64
}

type resultSeries struct {
	samples []resultSample
}

// Append a sample. Not thread-safe — only the ticker goroutine writes.
func (s *resultSeries) Append(sample resultSample) { s.samples = append(s.samples, sample) }

func (s *resultSeries) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{
		"elapsed_sec", "phase", "reports", "rows", "p50_ms", "p95_ms", "p99_ms", "p999_ms", "max_ms",
		"fail_internal", "fail_exhausted", "fail_other",
	}); err != nil {
		return err
	}
	for _, r := range s.samples {
		if err := cw.Write([]string{
			strconv.Itoa(r.elapsedSec),
			r.phase,
			strconv.FormatInt(r.reports, 10),
			strconv.FormatInt(r.rows, 10),
			fmtMs(r.p50),
			fmtMs(r.p95),
			fmtMs(r.p99),
			fmtMs(r.p999),
			fmtMs(r.maxObserved),
			strconv.FormatInt(r.failInternal, 10),
			strconv.FormatInt(r.failExhausted, 10),
			strconv.FormatInt(r.failOther, 10),
		}); err != nil {
			return err
		}
	}
	return nil
}

func fmtMs(ns int64) string { return strconv.FormatFloat(float64(ns)/1e6, 'f', 2, 64) }

// serverSample is one server-side resource reading. The wait fields are
// the per-sample DELTA, converted from the cumulative pprof counter by
// serverSeries.Append.
type serverSample struct {
	elapsedSec  int
	phase       string
	rssMB       int64
	heapMB      int64
	goroutines  int64
	mutexWaitMs int64
	blockWaitMs int64
}

// serverSeries collects server-side samples taken from the pprof
// endpoint via `docker exec`. Owns the cumulative→delta conversion for
// the mutex/block wait counters — the contention signal that surfaces
// SQLite write serialization on this path.
type serverSeries struct {
	samples         []serverSample
	prevMutexWaitMs int64
	prevBlockWaitMs int64
	primed          bool
}

func (s *serverSeries) Append(sample serverSample) {
	rawMutex := sample.mutexWaitMs
	rawBlock := sample.blockWaitMs
	if !s.primed {
		sample.mutexWaitMs = 0
		sample.blockWaitMs = 0
		s.primed = true
	} else {
		sample.mutexWaitMs = rawMutex - s.prevMutexWaitMs
		sample.blockWaitMs = rawBlock - s.prevBlockWaitMs
		if sample.mutexWaitMs < 0 {
			sample.mutexWaitMs = 0
		}
		if sample.blockWaitMs < 0 {
			sample.blockWaitMs = 0
		}
	}
	s.prevMutexWaitMs = rawMutex
	s.prevBlockWaitMs = rawBlock
	s.samples = append(s.samples, sample)
}

func (s *serverSeries) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{
		"elapsed_sec", "phase", "rss_mb", "heap_mb", "goroutines", "mutex_wait_ms", "block_wait_ms",
	}); err != nil {
		return err
	}
	for _, r := range s.samples {
		if err := cw.Write([]string{
			strconv.Itoa(r.elapsedSec),
			r.phase,
			strconv.FormatInt(r.rssMB, 10),
			strconv.FormatInt(r.heapMB, 10),
			strconv.FormatInt(r.goroutines, 10),
			strconv.FormatInt(r.mutexWaitMs, 10),
			strconv.FormatInt(r.blockWaitMs, 10),
		}); err != nil {
			return err
		}
	}
	return nil
}

// pollServer reads pprof endpoints inside the running container and
// returns a populated serverSample. Never blocks the driver on a single
// failed sample — missing fields stay zero.
func pollServer(ctx context.Context, container, phase string, elapsed int) serverSample {
	s := serverSample{elapsedSec: elapsed, phase: phase}

	if out, err := dockerExecPprof(ctx, container, "/debug/pprof/goroutine?debug=1"); err == nil {
		s.goroutines = parseGoroutineTotal(out)
	}
	if out, err := execIn(ctx, container, "sh", "-c", "cat /proc/$(pgrep -n vigosrv)/status | grep -E '^VmRSS|^VmHWM'"); err == nil {
		s.rssMB = parseVmRSS(out)
	}
	if out, err := dockerExecPprof(ctx, container, "/debug/pprof/heap?debug=1"); err == nil {
		s.heapMB = parseHeapInUse(out)
	}
	if out, err := dockerExecPprof(ctx, container, "/debug/pprof/mutex?debug=1"); err == nil {
		s.mutexWaitMs = parseTopWaitMs(out)
	}
	if out, err := dockerExecPprof(ctx, container, "/debug/pprof/block?debug=1"); err == nil {
		s.blockWaitMs = parseTopWaitMs(out)
	}
	return s
}

func dockerExecPprof(ctx context.Context, container, path string) (string, error) {
	return execIn(ctx, container, "wget", "-qO-", "--timeout=10", "http://127.0.0.1:6060"+path)
}

func execIn(ctx context.Context, container string, cmd ...string) (string, error) {
	args := append([]string{"exec", container}, cmd...)
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	return string(out), err
}

func parseGoroutineTotal(s string) int64 {
	if i := strings.Index(s, "total "); i >= 0 {
		rest := s[i+6:]
		end := strings.IndexAny(rest, " \n")
		if end < 0 {
			end = len(rest)
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func parseVmRSS(s string) int64 {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					return kb / 1024
				}
			}
		}
	}
	return 0
}

func parseHeapInUse(s string) int64 {
	if i := strings.Index(s, "heap profile:"); i >= 0 {
		rest := s[i+len("heap profile:"):]
		f := strings.Fields(rest)
		if len(f) >= 2 {
			if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				return n / 1024 / 1024
			}
		}
	}
	return 0
}

// parseTopWaitMs reads the cumulative wait time (ns→ms) from a pprof
// debug=1 contention dump; the first non-comment line's first field is
// the total. Best effort — units depend on the runtime; 0 on miss.
func parseTopWaitMs(s string) int64 {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "--- contention:") || strings.HasPrefix(line, "cycles/second=") {
			continue
		}
		if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "---") && line != "" {
			f := strings.Fields(line)
			if len(f) >= 1 {
				if n, err := strconv.ParseInt(f[0], 10, 64); err == nil {
					return n / 1e6
				}
			}
			break
		}
	}
	return 0
}

// manifest captures the full run config + environment so a reproduction
// has every variable in one place. The admin token is redacted from
// CLIArgs before this is written (see main).
type manifest struct {
	ToolVersion          string    `json:"tool_version"`
	StartedUTC           time.Time `json:"started_utc"`
	Hostname             string    `json:"hostname"`
	OS                   string    `json:"os"`
	ServerAddr           string    `json:"server_addr"`
	APIAddr              string    `json:"api_addr"`
	ServerContainer      string    `json:"server_container,omitempty"`
	EnvoyCount           int       `json:"envoy_count"`
	Interval             string    `json:"interval"`
	ResultsPerReport     int       `json:"results_per_report"`
	ChangedRatio         float64   `json:"changed_ratio"`
	ErrorRatio           float64   `json:"error_ratio"`
	SnapshotBytes        int       `json:"snapshot_bytes"`
	EnrollmentQuiesceSec int       `json:"enrollment_quiesce_sec"`
	WarmupSec            int       `json:"warmup_sec"`
	DurationSec          int       `json:"duration_sec"`
	HostnamePrefix       string    `json:"hostname_prefix"`
	CLIArgs              []string  `json:"cli_args"`
	ServerYAMLSHA256     string    `json:"server_yaml_sha256,omitempty"`
	VigoVersion          string    `json:"vigo_version,omitempty"`
	Notes                string    `json:"notes,omitempty"`
}

func writeManifest(path string, m manifest) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

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
