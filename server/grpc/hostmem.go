// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"os"
	"strconv"
	"strings"
)

// connRSSBytes is the measured resident memory a single held AgentStream
// costs (~220 KB — gRPC read/write buffers, three goroutine stacks, HTTP/2
// stream + flow-control state, GC headroom). The held-connection working set
// is the exact, linear driver of vigosrv's single-host memory ceiling; see
// docs/howto/sizing.md.
const connRSSBytes = 220 * 1024

// connWorkingSetFraction is the share of host RAM the auto-derived
// max_connections cap budgets for the held-connection working set. 0.40
// reproduces the published safe-ceiling table (≈60k on a 31 GiB host) and
// sits safely below the ~47%-of-RAM point where the box OOMs under a wave —
// the remaining headroom absorbs GC transient-doubling, page cache, the
// co-located vigo agent, and correlated check-in bursts.
const connWorkingSetFraction = 0.40

// defaultMaxConnections returns the held-stream admission cap used when
// checkin.max_connections is unset (0): the safe-ceiling connection count
// derived from host RAM. This mirrors defaultCheckInConcurrency()'s contract
// — 0 means a host-scaled built-in, never silently off — so out of the box
// the server sheds excess streams with ResourceExhausted (agent backs off)
// at the wall instead of being kernel-OOM-killed. Returns 0 (no cap, the
// legacy behavior) only when host RAM can't be read, so a detection failure
// degrades to "no surprise" rather than imposing a wrong cap.
func defaultMaxConnections(hostMemBytes uint64) int {
	if hostMemBytes == 0 {
		return 0
	}
	return int(float64(hostMemBytes) * connWorkingSetFraction / float64(connRSSBytes))
}

// hostMemoryBytes returns the memory budget the server should size the
// connection cap against: the cgroup memory limit when one is set (the
// container's hard ceiling), otherwise /proc/meminfo MemTotal (host RAM).
// Returns 0 when neither is readable (non-Linux, or an unexpected layout) —
// the caller treats 0 as "unknown" and leaves the cap off.
func hostMemoryBytes() uint64 {
	memTotal := procMemTotalBytes()
	if lim := cgroupMemLimitBytes(); lim > 0 && (memTotal == 0 || lim < memTotal) {
		return lim
	}
	return memTotal
}

// procMemTotalBytes reads MemTotal from /proc/meminfo, in bytes. 0 if absent.
func procMemTotalBytes() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return parseMemTotal(string(data))
}

// parseMemTotal extracts the MemTotal value (reported in kB) from
// /proc/meminfo content and returns it in bytes. 0 if not found.
func parseMemTotal(meminfo string) uint64 {
	for _, line := range strings.Split(meminfo, "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		// Expect: ["MemTotal:", "<kb>", "kB"]
		if len(fields) >= 2 {
			if kb, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				return kb * 1024
			}
		}
		return 0
	}
	return 0
}

// cgroupMemLimitBytes returns the cgroup memory limit in bytes if a real
// (non-"unlimited") limit is set, else 0. Tries cgroup v2 (memory.max) then
// v1 (memory.limit_in_bytes); both use a sentinel for "no limit".
func cgroupMemLimitBytes() uint64 {
	if data, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		return parseCgroupMax(string(data))
	}
	if data, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		return parseCgroupMax(string(data))
	}
	return 0
}

// parseCgroupMax parses a cgroup memory-limit file. cgroup v2 writes the
// literal "max" when unlimited; v1 writes a near-uint64-max sentinel. Either
// way an "unlimited" value (or a garbage parse) returns 0 so the caller falls
// back to host MemTotal.
func parseCgroupMax(content string) uint64 {
	s := strings.TrimSpace(content)
	if s == "" || s == "max" {
		return 0
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	// v1 "unlimited" is PAGE_COUNTER_MAX-ish — anything above a sane ceiling
	// (1 PiB) is the no-limit sentinel, not a real cap.
	if v >= 1<<50 {
		return 0
	}
	return v
}
