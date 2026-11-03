#!/usr/bin/env bash
# Regenerate THIRD-PARTY-NOTICES.{go,rust}.txt — the attribution files
# that satisfy the MIT/BSD/Apache-2.0/ISC notice obligations of Vigo's
# dependencies. Vigo itself ships under Apache-2.0; every
# bundled dependency is permissive (no copyleft reaches a distributed
# binary). For OR-licensed crates we elect the permissive arm and omit
# the copyleft text.
#
# Usage:  scripts/gen-notices.sh
# Deps:   go, go-licenses (auto-installed to GOBIN if missing), cargo, python3
#
# Re-run when go.mod or agent/Cargo.lock change. Wired into `make notices`
# and `make dist`.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

GOBIN="$(go env GOPATH)/bin"
export PATH="$GOBIN:$PATH"

# --- Go: server + CLI -------------------------------------------------------
if ! command -v go-licenses >/dev/null 2>&1; then
  echo "==> installing go-licenses"
  go install github.com/google/go-licenses@latest
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> Go: scanning licenses (server + cli)"
go-licenses csv ./server/... ./cli/... > "$WORK/golic.csv" 2>/dev/null
go-licenses save ./server/... ./cli/... \
  --save_path="$WORK/go-notices" \
  --ignore git.alexander4.io/vigo \
  --force 2>/dev/null

echo "==> Go: assembling THIRD-PARTY-NOTICES.go.txt"
NOTICE_DIR="$WORK/go-notices" CSV="$WORK/golic.csv" python3 - <<'PYEOF'
import csv, os
NOTICE_DIR = os.environ["NOTICE_DIR"]; CSV = os.environ["CSV"]
meta = {}
with open(CSV) as f:
    for row in csv.reader(f):
        if len(row) < 3 or row[2] == "Unknown":  # Unknown == our own modules
            continue
        meta[row[0]] = (row[1], row[2])
entries = {}
for root, _, files in os.walk(NOTICE_DIR):
    for fn in files:
        rel = os.path.relpath(root, NOTICE_DIR)
        with open(os.path.join(root, fn), encoding="utf-8", errors="replace") as fh:
            entries.setdefault(rel, []).append((fn, fh.read()))
def lookup(moddir):
    best = None
    for m in meta:
        if (moddir == m or moddir.startswith(m + "/")) and (best is None or len(m) > len(best)):
            best = m
    return best
out = ["VIGO — THIRD-PARTY SOFTWARE NOTICES (Go components: vigosrv, vigocli)",
"="*78, "",
"The Vigo server (vigosrv) and command-line interface (vigocli) incorporate",
"the following third-party open-source software. Each component is provided",
"under the license reproduced below. All components are under permissive",
"licenses (BSD / MIT / Apache-2.0 / ISC); none impose copyleft obligations.", "",
"This file satisfies the attribution requirements of those licenses",
"(e.g., Apache-2.0 section 4, the BSD/MIT copyright-notice clause).", "",
"="*78, ""]
for moddir in sorted(entries):
    m = lookup(moddir)
    if m:
        url, lic = meta[m]; title, license_type, src = m, lic, url
    else:
        title, license_type, src = moddir, "(see license text below)", ""
    out += ["-"*78, f"Component: {title}", f"License:   {license_type}"]
    if src and src != "Unknown":
        out.append(f"Source:    {src}")
    out += ["-"*78, ""]
    for fn, text in sorted(entries[moddir]):
        if fn.upper().startswith("NOTICE"):
            out.append(f"[{fn}]")
        out += [text.rstrip(), ""]
    out.append("")
open("THIRD-PARTY-NOTICES.go.txt", "w").write("\n".join(out))
print(f"    {len(entries)} components")
PYEOF

# --- Rust: agent ------------------------------------------------------------
echo "==> Rust: resolving shipped (normal-edge) crate set"
( cd agent && cargo tree -e normal --prefix none --no-dedupe 2>/dev/null ) \
  | sed -E 's/ \(.*//' | awk '{print $1" "$2}' | sort -u | grep -v '^vigo ' > "$WORK/rust-shipped.txt"
( cd agent && cargo metadata --format-version 1 --quiet 2>/dev/null ) > "$WORK/cargo-meta.json"

# Canonical Apache-2.0 text — reused from any crate that bundles it.
# `find | head` would SIGPIPE find under pipefail, so capture then slice.
APACHE_SRC="$(find "$HOME/.cargo/registry/src" -maxdepth 3 -iname 'LICENSE-APACHE*' -type f 2>/dev/null | { head -1; cat >/dev/null; })"
if [ -n "$APACHE_SRC" ]; then
  cat "$APACHE_SRC" > "$WORK/APACHE-2.0.txt"
else
  : > "$WORK/APACHE-2.0.txt"
fi

echo "==> Rust: assembling THIRD-PARTY-NOTICES.rust.txt"
META="$WORK/cargo-meta.json" SHIPPED="$WORK/rust-shipped.txt" APACHE_TXT="$WORK/APACHE-2.0.txt" python3 - <<'PYEOF'
import json, os, re
meta = json.load(open(os.environ["META"]))
pkgs = {(p["name"], p["version"]): p for p in meta["packages"]}
shipped = []
for line in open(os.environ["SHIPPED"]):
    parts = line.split()
    if len(parts) >= 2:
        shipped.append((parts[0], parts[1].lstrip("v")))
lic_re = re.compile(r'(?i)^(licen[sc]e|copying|notice|unlicense|copyright)')
copyleft_file_re = re.compile(r'(?i)(gpl|lgpl|agpl)')
def find_license_files(crate_dir, spdx):
    is_disjunctive = " OR " in spdx or "/" in spdx
    permissive_arm = any(t in spdx.upper() for t in ("MIT","APACHE","BSD","ISC","ZLIB","UNLICENSE","0BSD"))
    drop_copyleft = is_disjunctive and permissive_arm
    found = []
    try:
        for fn in sorted(os.listdir(crate_dir)):
            if not lic_re.match(fn): continue
            if drop_copyleft and copyleft_file_re.search(fn): continue
            fp = os.path.join(crate_dir, fn)
            if os.path.isfile(fp):
                found.append((fn, open(fp, encoding="utf-8", errors="replace").read()))
    except FileNotFoundError:
        pass
    return found
MIT = """MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE."""
APACHE = open(os.environ["APACHE_TXT"], encoding="utf-8", errors="replace").read().rstrip()
out = ["VIGO — THIRD-PARTY SOFTWARE NOTICES (Rust component: vigo agent)", "="*78, "",
"The Vigo agent (vigo) statically links the following third-party open-source",
"Rust crates. Each is provided under the license(s) reproduced below. This list",
"covers the normal (shipped) build graph only; development- and build-time",
"dependencies (e.g. mockito, colored, tonic-build, serial_test) are excluded as",
"they are not distributed in the binary.", "",
"Where a crate is offered under a disjunctive license (\"A OR B\"), Vigo elects",
"the permissive option; the copyleft arm is not selected and its text is omitted.",
"This file satisfies the attribution requirements of the MIT, BSD, Apache-2.0,",
"ISC, and similar licenses.", "", "="*78, ""]
missing = []
for name, ver in shipped:
    p = pkgs.get((name, ver)) or next((pkgs[k] for k in pkgs if k[0]==name), None)
    spdx = (p.get("license") if p else None) or "(see bundled text / repository)"
    repo = (p.get("repository") if p else None) or ""
    crate_dir = os.path.dirname(p["manifest_path"]) if p and p.get("manifest_path") else ""
    files = find_license_files(crate_dir, spdx) if crate_dir else []
    out += ["-"*78, f"Crate:   {name} {ver}", f"License: {spdx}"]
    if repo: out.append(f"Source:  {repo}")
    out += ["-"*78, ""]
    if files:
        for fn, text in files:
            out += [f"[{fn}]", text.rstrip(), ""]
    else:
        missing.append((name, ver, spdx))
        out += ["(No license file bundled in crate source; license is the SPDX expression",
                " above. Canonical text is reproduced in the Appendix at end of file.)", ""]
    out.append("")
out += ["="*78, "APPENDIX — Canonical license texts for SPDX-only crates", "="*78, "",
"The following crates declared their license via SPDX identifier without bundling",
"a license file in their published source. The canonical MIT and Apache-2.0 texts",
"that cover them follow.", "", "Crates relying on the appendix:"]
for n,v,s in missing: out.append(f"  - {n} {v}  ({s})")
out += ["", "-"*78, "[MIT License — canonical text]", "-"*78, MIT, "",
        "-"*78, "[Apache License 2.0 — canonical text]", "-"*78, APACHE, ""]
open("THIRD-PARTY-NOTICES.rust.txt", "w").write("\n".join(out))
print(f"    {len(shipped)} crates ({len(shipped)-len(missing)} bundled, {len(missing)} via appendix)")
PYEOF

echo "==> done:"
ls -la THIRD-PARTY-NOTICES.go.txt THIRD-PARTY-NOTICES.rust.txt
