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
// only when it changes, and the inventory hash every tick so the server can
// detect a cache desync. The server caches the inventory PER ENVOY and, on
// every tick, byte-concatenates it with the fresh vitals and runs ONE shallow
// json.Unmarshal of the merged result (server/grpc/agentstream.go) — before
// the no_change short-circuit, so the parse cost lands on every tick.
//
// The old harness sent a ~350-byte toy blob in the legacy traits_json field,
// so that parse was unrealistically cheap and the inventory cache was never
// exercised at all. This file makes the harness a faithful split agent so the
// load test measures the real per-tick parse CPU and the real per-envoy
// inventory-cache RAM (≈ inventory size × fleet).
//
// Sizing: the fleet being simulated is 60k CLOUD SERVERS, not laptops. The
// ADR-033 measurements were home-lab laptops (219–332 KB, package-heavy with
// GUI/dev toolchains); a typical Linux cloud server is lighter — fewer
// packages, no gpu/display/battery — but still carries the OS CA trust store
// (cert_lifecycle ~60 KB). So the default target is ~150 KB (defaultInventoryKB)
// and is tunable via --inventory-kb, scaling the `packages` array while the
// CA-bundle-sized cert_lifecycle and the fixed collectors stay constant — so a
// sweep maps the per-envoy RAM + parse cost as a curve, not a single guess.
//
// Generator economy: the inventory BULK and the vitals blob are SHARED across
// all envoys (one copy each in the process); only the small per-host identity
// portion varies — so the generator holds ~one bulk total, not bulk × fleet.
// The server still copies the reconstructed inventory per envoy, so server-side
// RAM stays realistic. The full per-host inventory (identity ∪ bulk) is rebuilt
// on demand only when it's actually shipped (first tick + a server-requested
// resend), never on the vitals-only steady-state path.

// defaultInventoryKB is the server-realistic default full-inventory target when
// --inventory-kb isn't given. ~150 KB models a typical Linux cloud server.
const defaultInventoryKB = 150

// sharedInventoryBulk and sharedVitals are installed once by loadPayloads at
// the start of every drive mode and are read-only thereafter, so the
// per-envoy goroutines share them without locking.
var (
	sharedInventoryBulk json.RawMessage
	sharedVitals        json.RawMessage
)

// loadPayloads installs the shared bulk + vitals for this process.
//
// overrideKB == 0: use the bulk the manifest baked at create time (the common
// run/tick path), regenerating deterministically only if an older manifest
// predates the split-traits fields.
//
// overrideKB > 0: a sweep run — regenerate the bulk at the requested size and
// recompute every host's inventory hash IN MEMORY, so the same enrolled fleet
// can be re-measured at a different inventory size without a re-enroll. The
// manifest on disk is untouched; only this process's view changes.
func loadPayloads(st *fleetState, overrideKB int) {
	switch {
	case overrideKB > 0:
		sharedInventoryBulk = genInventoryBulk(overrideKB)
	case len(st.InventoryBulk) > 0:
		sharedInventoryBulk = st.InventoryBulk
	default:
		sharedInventoryBulk = genInventoryBulk(defaultInventoryKB)
	}
	if len(st.Vitals) > 0 {
		sharedVitals = st.Vitals
	} else {
		sharedVitals = genVitals()
	}
	if overrideKB > 0 {
		// The manifest's stored hashes were computed for the old bulk; recompute
		// against the regenerated bulk so vitals-only ticks keep hitting the cache.
		for i := range st.Hosts {
			st.Hosts[i].InventoryHash = inventoryHashFor(&st.Hosts[i])
		}
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

// genInventoryBulk builds the shared stable-collector blob sized so the full
// per-host inventory (identity ∪ this bulk) lands near targetKB. The SHAPE is
// what matters for the measurement: a realistic top-level key count (so the
// server's shallow top-level parse walks a real-sized map) and a realistic byte
// size dominated by `packages` and `cert_lifecycle`. cert_lifecycle and the
// fixed collectors are size-stable on real hosts (the CA trust store + one-shot
// facts), so only `packages` scales to hit targetKB — matching how a real
// fleet's inventory size varies (a lean VM vs a package-heavy app server). None
// of these keys overlap the per-host identity keys (gen.go) or the vitals keys
// (genVitals). A target below the fixed floor (~110 KB: cert_lifecycle + the
// other size-stable collectors) yields zero packages and lands at the floor.
func genInventoryBulk(targetKB int) json.RawMessage {
	m := map[string]any{}

	// cert_lifecycle: ~60 KB and size-stable — it's largely the OS CA trust
	// store (ca-certificates), as big on a server as a laptop. Does NOT scale.
	certs := make([]map[string]string, 330)
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
	m["security_scan"] = genObjArray(30, "finding", []string{"id", "severity", "title", "package"})
	m["kernel_module_attestation"] = genObjArray(110, "module", []string{"name", "signer", "sha256", "loaded"})
	m["package_source_audit"] = genObjArray(30, "src", []string{"package", "repo", "trusted"})
	m["listeners"] = genObjArray(24, "listener", []string{"proto", "address", "process", "pid"})
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

	// packages scales to hit targetKB: measure the fixed collectors, then add
	// however many package entries fill the remaining budget. (The result is the
	// FULL per-host inventory target, so reserve a little for the per-host
	// identity bytes that get merged in later.)
	mkPkg := func(i int) map[string]string {
		return map[string]string{
			"name":    fmt.Sprintf("pkg-%s-%d", pkgStems[i%len(pkgStems)], i),
			"version": fmt.Sprintf("%d.%d.%d-%dubuntu%d", i%9, i%19, i%31, i%7, i%4),
			"arch":    "amd64",
			"origin":  "distro",
		}
	}
	fixed, _ := json.Marshal(m)
	sample, _ := json.Marshal(mkPkg(1000))
	perPkg := len(sample) + 1 // + the joining comma
	const identityReserve = 400
	remain := targetKB*1024 - len(fixed) - identityReserve - len(`,"packages":[]`)
	n := 0
	if remain > 0 {
		n = remain / perPkg
	}
	pkgs := make([]map[string]string, n)
	for i := range pkgs {
		pkgs[i] = mkPkg(i)
	}
	m["packages"] = pkgs

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
