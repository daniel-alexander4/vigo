// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ADR-033 split-traits payloads for the harness.
//
// A real agent sends three things on the steady-state check-in: a small
// volatile "vitals" blob every tick (~5 KB), a large stable "inventory" blob
// only when it changes (~219–332 KB across the live fleet), and the inventory
// hash every tick so the server can detect a cache desync. The server caches
// the inventory PER ENVOY
// and, on every tick, byte-concatenates it with the fresh vitals and runs ONE
// shallow json.Unmarshal of the ~218 KB result (server/grpc/agentstream.go) —
// before the no_change short-circuit, so the parse cost lands on every tick.
//
// The old harness sent a ~350-byte toy blob in the legacy traits_json field,
// so that parse was unrealistically cheap and the inventory cache was never
// exercised at all. This file makes the harness a faithful split agent so the
// load test measures the real per-tick parse CPU and the real per-envoy
// inventory-cache RAM (~280 KB × fleet).
//
// Generator economy: the big inventory BULK and the vitals blob are SHARED
// across all envoys (one copy each in the process), and only the small
// per-host identity portion varies — so the generator holds ~285 KB total, not
// ~280 KB × fleet. The server still copies the reconstructed inventory per
// envoy, so server-side RAM stays realistic. The full per-host inventory
// (identity ∪ bulk) is rebuilt on demand only when it's actually shipped (first
// tick + a server-requested resend), never on the vitals-only steady-state
// path.

// sharedInventoryBulk and sharedVitals are installed once by loadPayloads at
// the start of every drive mode and are read-only thereafter, so the
// per-envoy goroutines share them without locking.
var (
	sharedInventoryBulk json.RawMessage
	sharedVitals        json.RawMessage
)

// loadPayloads installs the shared bulk + vitals for this process from the
// manifest, regenerating them deterministically if an older manifest predates
// the split-traits fields.
func loadPayloads(st *fleetState) {
	if len(st.InventoryBulk) > 0 {
		sharedInventoryBulk = st.InventoryBulk
	} else {
		sharedInventoryBulk = genInventoryBulk()
	}
	if len(st.Vitals) > 0 {
		sharedVitals = st.Vitals
	} else {
		sharedVitals = genVitals()
	}
}

// fullInventory rebuilds one host's complete inventory = per-host identity ∪
// the shared bulk. Deterministic (fixed byte inputs, byte-level merge), so the
// hash computed at create time matches the bytes shipped at run time. Called
// only when the inventory is actually shipped (first tick / resend), never on
// the steady-state vitals-only path.
func fullInventory(h *hostState) []byte {
	return mergeJSONObjects(h.IdentityJSON, sharedInventoryBulk)
}

// inventoryHashFor is the stable per-host inventory hash sent on every tick.
// The server trusts this hash verbatim (it does not recompute), so it only has
// to be stable; computing the real sha256 of the shipped bytes keeps it honest.
func inventoryHashFor(h *hostState) string {
	sum := sha256.Sum256(fullInventory(h))
	return hex.EncodeToString(sum[:])
}

// mergeJSONObjects joins two compact JSON objects {a...} + {b...} -> {a...,b...}
// at the byte level, mirroring the server's mergeTraitsObjects. The key sets
// must be disjoint (the generators below keep identity/bulk/vitals keys
// distinct), so the join produces no duplicate keys.
func mergeJSONObjects(a, b json.RawMessage) []byte {
	at := bytes.TrimSpace(a)
	bt := bytes.TrimSpace(b)
	if len(at) <= 2 {
		return append([]byte(nil), bt...)
	}
	if len(bt) <= 2 {
		return append([]byte(nil), at...)
	}
	out := make([]byte, 0, len(at)+len(bt))
	out = append(out, at[:len(at)-1]...) // drop trailing '}'
	out = append(out, ',')
	out = append(out, bt[1:]...) // drop leading '{'
	return out
}

// genInventoryBulk builds the shared ~280 KB stable-collector blob (the live
// fleet's full inventory ranges 219–332 KB). Content is
// synthetic but the SHAPE is what matters for the measurement: a realistic
// top-level key count (so the server's shallow top-level parse walks a
// real-sized map) and a realistic byte size dominated by `packages` and
// `cert_lifecycle`, exactly as on the live fleet. None of these keys overlap
// the per-host identity keys (gen.go) or the vitals keys (genVitals).
func genInventoryBulk() json.RawMessage {
	m := map[string]any{}

	// packages: the dominant real key (~80 KB on danlap).
	pkgs := make([]map[string]string, 1400)
	for i := range pkgs {
		pkgs[i] = map[string]string{
			"name":    fmt.Sprintf("pkg-%s-%d", pkgStems[i%len(pkgStems)], i),
			"version": fmt.Sprintf("%d.%d.%d-%dubuntu%d", i%9, i%19, i%31, i%7, i%4),
			"arch":    "amd64",
			"origin":  "distro",
		}
	}
	m["packages"] = pkgs

	// cert_lifecycle: the second dominant real key (~63 KB on danlap).
	certs := make([]map[string]string, 450)
	for i := range certs {
		certs[i] = map[string]string{
			"subject":   fmt.Sprintf("CN=svc-%d.internal,O=Synthetic,C=US", i),
			"issuer":    "CN=Synthetic Internal Issuing CA,O=Synthetic,C=US",
			"serial":    fmt.Sprintf("%032x", i),
			"not_after": fmt.Sprintf("202%d-%02d-%02dT00:00:00Z", 6+i%4, 1+i%12, 1+i%28),
			"path":      fmt.Sprintf("/etc/ssl/certs/svc-%d.pem", i),
		}
	}
	m["cert_lifecycle"] = certs

	// A spread of the remaining stable collectors so the top-level key count
	// matches a real ~50-key payload. Sizes are incidental; the names mirror
	// agent/src/traits stable_collectors().
	m["security_scan"] = genObjArray(140, "finding", []string{"id", "severity", "title", "package"})
	m["kernel_module_attestation"] = genObjArray(220, "module", []string{"name", "signer", "sha256", "loaded"})
	m["package_source_audit"] = genObjArray(70, "src", []string{"package", "repo", "trusted"})
	m["listeners"] = genObjArray(48, "listener", []string{"proto", "address", "process", "pid"})
	m["block_devices"] = genObjArray(10, "dev", []string{"name", "model", "serial", "size_gb"})
	m["systemd"] = genObjArray(120, "unit", []string{"name", "load", "active", "sub"})
	m["processors"] = map[string]any{"model": "Synthetic Xeon Platinum", "cores": 8, "threads": 16, "mhz": 2600, "flags": "fpu vme de pse tsc msr pae mce cx8 apic sep mtrr"}
	m["network"] = genObjArray(6, "iface", []string{"name", "mac", "ipv4", "mtu"})
	m["dmi"] = map[string]any{"vendor": "Synthetic", "product": "CloudVM", "serial": "SYN-0000-0000", "bios_version": "1.16.0", "uuid": "00000000-0000-0000-0000-000000000000"}
	m["hardware"] = map[string]any{"vendor": "Synthetic", "model": "CloudVM", "form_factor": "vm"}
	m["docker"] = map[string]any{"installed": true, "version": "26.1.0", "containers": 0, "images": 0}
	m["selinux"] = map[string]any{"enabled": false, "mode": "disabled"}
	m["timezone"] = map[string]any{"name": "UTC", "offset": "+00:00"}
	m["locale"] = map[string]any{"lang": "en_US.UTF-8"}
	m["gpu"] = []any{}
	m["tpm"] = map[string]any{"present": true, "version": "2.0"}
	m["secure_boot"] = map[string]any{"enabled": true}
	m["connwaer"] = map[string]any{"installed": false}
	m["display"] = map[string]any{"headless": true}
	m["hypervisor"] = map[string]any{"host_is_libvirt": false, "virt": "kvm"}
	m["virtual"] = map[string]any{"is_vm": true, "technology": "kvm"}
	m["identity"] = map[string]any{"machine_id": "00000000000000000000000000000000", "boot_id": "00000000-0000-0000-0000-000000000000"}

	b, _ := json.Marshal(m)
	return b
}

// genVitals builds the shared ~5 KB volatile-collector blob, sent every tick.
// Keys mirror agent/src/traits volatile_collectors() and are disjoint from the
// identity and inventory keys.
func genVitals() json.RawMessage {
	m := map[string]any{
		"time":            map[string]any{"unix": 1750000000, "rfc3339": "2026-06-22T12:00:00Z"},
		"uptime":          map[string]any{"seconds": 864000},
		"loadavg":         map[string]any{"1m": 0.42, "5m": 0.55, "15m": 0.48},
		"memory":          map[string]any{"total_mb": 16384, "used_mb": 8192, "available_mb": 8192, "cached_mb": 4096},
		"swap":            map[string]any{"total_mb": 4096, "used_mb": 128},
		"mountpoints":     genObjArray(8, "mount", []string{"device", "mountpoint", "fstype", "options"}),
		"filesystem":      genObjArray(8, "fs", []string{"device", "size_gb", "used_gb", "use_pct"}),
		"users":           genObjArray(6, "user", []string{"name", "uid", "shell", "home"}),
		"ports":           genObjArray(24, "port", []string{"proto", "port", "process", "state"}),
		"agent_self":      map[string]any{"version": agentVersion, "threads": 57, "rss_mb": 38, "fds": 24},
		"host_self":       map[string]any{"fd_pct": 3, "rootfs_pct": 41, "inode_pct": 12, "oom_recent": false, "pkg_repo_ok": true},
		"host_containers": []any{},
		"battery":         map[string]any{},
	}
	b, _ := json.Marshal(m)
	return b
}

// genObjArray builds a deterministic array of n objects, each carrying the
// given fields with index-derived string values — enough to give a collector a
// realistic byte footprint without modeling its real schema.
func genObjArray(n int, prefix string, fields []string) []map[string]string {
	out := make([]map[string]string, n)
	for i := range out {
		o := make(map[string]string, len(fields))
		for j, f := range fields {
			o[f] = fmt.Sprintf("%s-%s-%d", prefix, f, i*len(fields)+j)
		}
		out[i] = o
	}
	return out
}

// pkgStems gives package names a little lexical variety so the synthetic
// package list reads less like a single repeated token.
var pkgStems = []string{
	"core", "libssl", "python", "perl", "gcc", "glib", "systemd", "kernel",
	"util", "net", "crypto", "xml", "http", "sql", "json", "yaml", "zlib",
	"curl", "git", "node",
}
