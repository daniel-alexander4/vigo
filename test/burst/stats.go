// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
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

// jsonEncoder is a tiny indirection so callers don't import encoding/json.
func jsonEncoder(w io.Writer) *json.Encoder { return json.NewEncoder(w) }

// failureBreakdown counts CheckIn failures by gRPC status code. The
// simple loadtest only reports a single failure total, hiding whether
// the test was bound by network timeouts (DeadlineExceeded), server
// rejection (ResourceExhausted), or connection breakage (Unavailable).
// All paths matter for diagnosis but for very different reasons.
type failureBreakdown struct {
	codes [17]atomic.Int64 // indexed by grpc/codes; 17 = highest defined + 1
	other atomic.Int64     // non-gRPC errors (transport, parse, etc.)
}

func (f *failureBreakdown) Record(err error) {
	if err == nil {
		return
	}
	st, ok := status.FromError(err)
	if !ok {
		f.other.Add(1)
		return
	}
	c := st.Code()
	if int(c) < len(f.codes) {
		f.codes[c].Add(1)
	} else {
		f.other.Add(1)
	}
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

// secondSample is one row of the per-second time series.
type secondSample struct {
	elapsedSec int
	throughput int64
	p50, p95, p99, p999 int64 // nanoseconds
	maxObserved int64        // upper bound of highest non-empty bucket
	failures   int64
	phase      string // "warmup", "baseline", "burst", "cooldown"
}

// secondSeries is the time-series sink. Each tick the per-second
// histogram is read+reset, percentiles computed, and a sample appended.
type secondSeries struct {
	samples []secondSample
}

// Append a sample. Not thread-safe — only one goroutine writes (the
// ticker loop in main).
func (s *secondSeries) Append(sample secondSample) {
	s.samples = append(s.samples, sample)
}

// WriteCSV dumps the series to a writer. Columns:
//   elapsed_sec, phase, throughput, p50_ms, p95_ms, p99_ms, p999_ms, max_ms, failures
func (s *secondSeries) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{
		"elapsed_sec", "phase", "throughput", "p50_ms", "p95_ms", "p99_ms", "p999_ms", "max_ms", "failures",
	}); err != nil {
		return err
	}
	for _, r := range s.samples {
		if err := cw.Write([]string{
			strconv.Itoa(r.elapsedSec),
			r.phase,
			strconv.FormatInt(r.throughput, 10),
			fmtMs(r.p50),
			fmtMs(r.p95),
			fmtMs(r.p99),
			fmtMs(r.p999),
			fmtMs(r.maxObserved),
			strconv.FormatInt(r.failures, 10),
		}); err != nil {
			return err
		}
	}
	return nil
}

func fmtMs(ns int64) string {
	return strconv.FormatFloat(float64(ns)/1e6, 'f', 2, 64)
}

// serverSample is one server-side resource reading.
type serverSample struct {
	elapsedSec   int
	phase        string
	rssMB        int64
	heapMB       int64
	goroutines   int64
	mutexWaitMs  int64
	blockWaitMs  int64
}

// serverSeries collects server-side samples taken from the pprof
// endpoint via `docker exec vigo wget`. Cadence is operator-set
// (--server-sample-interval), defaulting to 5 s.
type serverSeries struct {
	samples []serverSample
}

func (s *serverSeries) Append(sample serverSample) {
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
// returns a populated serverSample. Returns a zero sample with phase
// set + an error appended to the log on failure — never blocks the
// driving goroutine on a single failed sample.
func pollServer(ctx context.Context, container, _phase string, elapsed int) serverSample {
	s := serverSample{elapsedSec: elapsed, phase: _phase}

	// Goroutine count via `pprof/goroutine?debug=1` — first line is
	// "goroutine profile: total NNNN" — cheap parse.
	if out, err := dockerExecPprof(ctx, container, "/debug/pprof/goroutine?debug=1"); err == nil {
		s.goroutines = parseGoroutineTotal(out)
	}

	// Heap RSS from /proc/<pid>/status inside the container; the
	// pprof heap endpoint reports Go-heap-only, not the process RSS.
	if out, err := execIn(ctx, container, "sh", "-c", "cat /proc/$(pgrep -n vigosrv)/status | grep -E '^VmRSS|^VmHWM'"); err == nil {
		s.rssMB = parseVmRSS(out)
	}

	// Heap from pprof: total in_use_bytes — light weight; full profile
	// is heavy so we only fetch the count metric.
	if out, err := dockerExecPprof(ctx, container, "/debug/pprof/heap?debug=1"); err == nil {
		s.heapMB = parseHeapInUse(out)
	}

	// Mutex contention is the cum-wait counter shown at the top of
	// the mutex profile. Reading the full profile every 5s is too
	// expensive; instead use the top counter via the `?debug=1` text
	// form.
	if out, err := dockerExecPprof(ctx, container, "/debug/pprof/mutex?debug=1"); err == nil {
		s.mutexWaitMs = parseTopWaitMs(out)
	}
	if out, err := dockerExecPprof(ctx, container, "/debug/pprof/block?debug=1"); err == nil {
		s.blockWaitMs = parseTopWaitMs(out)
	}

	return s
}

func dockerExecPprof(ctx context.Context, container, path string) (string, error) {
	return execIn(ctx, container, "wget", "-qO-", "--timeout=10",
		"http://127.0.0.1:6060"+path)
}

func execIn(ctx context.Context, container string, cmd ...string) (string, error) {
	args := append([]string{"exec", container}, cmd...)
	c := exec.CommandContext(ctx, "docker", args...)
	out, err := c.Output()
	return string(out), err
}

func parseGoroutineTotal(s string) int64 {
	// First line: "goroutine profile: total 12345"
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
	// Looking for `VmRSS:    NNNN kB`
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
	// pprof/heap?debug=1 starts with:
	//   heap profile: NNNN: BYTES_USED [NNNN: BYTES_ALLOCATED] ...
	if i := strings.Index(s, "heap profile:"); i >= 0 {
		rest := s[i+len("heap profile:"):]
		f := strings.Fields(rest)
		if len(f) >= 2 {
			// f[1] = "<count>:" — strip; we want the third token's bytes.
			bytesStr := strings.Trim(f[1], ":")
			_ = bytesStr // not what we need
			// Actually format is `NNNN: BYTES NNNN: BYTES` — we want the
			// second numeric (in_use_bytes).
			if len(f) >= 2 {
				if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					return n / 1024 / 1024
				}
			}
		}
	}
	return 0
}

// parseTopWaitMs reads the cumulative wait time from a pprof debug=1
// dump. Header line includes "--- contention:" then a cycles counter;
// we report it in ms (best effort — the units depend on what the
// runtime is reporting). Fallback: 0.
func parseTopWaitMs(s string) int64 {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "--- contention:") {
			// "--- contention: cycles/second=NNN" — skip; we want the
			// top-of-profile total which is on a subsequent line.
			continue
		}
		if strings.HasPrefix(line, "cycles/second=") {
			continue
		}
		// First non-comment line is "TOTAL_NS COUNT @ ..."; pull total ns.
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

// writeManifest dumps the run config + environment to <output>/manifest.json
// so a reproduction attempt has every variable in one place. SHA256s
// of server.yaml (when reachable) are captured so a later reload that
// silently changed config is visible.
func writeManifest(path string, m manifest) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := jsonEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

type manifest struct {
	ToolVersion         string            `json:"tool_version"`
	StartedUTC          time.Time         `json:"started_utc"`
	Hostname            string            `json:"hostname"`
	OS                  string            `json:"os"`
	ServerAddr          string            `json:"server_addr"`
	APIAddr             string            `json:"api_addr"`
	ServerContainer     string            `json:"server_container,omitempty"`
	EnvoyCount          int               `json:"envoy_count"`
	Interval            string            `json:"interval"`
	WarmupSec           int               `json:"warmup_sec"`
	BaselineSec         int               `json:"baseline_sec"`
	BurstSettleSec      int               `json:"burst_settle_sec"`
	CooldownSec         int               `json:"cooldown_sec"`
	Trials              int               `json:"trials"`
	HostnamePrefix      string            `json:"hostname_prefix"`
	CLIArgs             []string          `json:"cli_args"`
	ServerYAMLSHA256    string            `json:"server_yaml_sha256,omitempty"`
	VigoVersion         string            `json:"vigo_version,omitempty"`
	SLABaselineP99Ms    int64             `json:"sla_baseline_p99_ms,omitempty"`
	SLASettleSec        int               `json:"sla_settle_sec,omitempty"`
	SLAFailureRate      float64           `json:"sla_failure_rate,omitempty"`
	Notes               string            `json:"notes,omitempty"`
	Extra               map[string]string `json:"extra,omitempty"`
}

// fmtDuration is a short readable form (e.g. "247ms", "1.4s", "12.7s").
func fmtDuration(ns int64) string {
	d := time.Duration(ns)
	switch {
	case d < time.Microsecond:
		return fmt.Sprintf("%dns", d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return d.Round(10 * time.Millisecond).String()
	}
}
