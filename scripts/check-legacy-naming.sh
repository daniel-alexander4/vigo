#!/usr/bin/env bash
# check-legacy-naming.sh — fail if any tracked file still uses the
# pre-0.31.0 substrate vocabulary.
#
# Substrate rename map (committed in 0.31.0):
#   lode    -> payload     (admin-pushed binary distribution)
#   cairn   -> gitmirror   (P2P git repo backup)
#   echo    -> longdrawer    (LAN plaintext per-user dir sync)
#   sigil   -> lockbox   (LAN encrypted per-user dir sync)
#   stones  -> modules     (config primitive directory)
#
# This script greps every tracked source/doc/config file for the legacy
# tokens and the legacy filesystem/REST paths. Exit 1 on any match
# outside the allowlist; exit 0 when clean.
#
# Wired into `make lint` so any reintroduction during ongoing work fails
# in CI before it hits a release.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

ALLOWLIST="scripts/check-legacy-naming.allowlist"

# Whole-word legacy tokens. PCRE-style \b boundaries; case-sensitive
# because the substrate vocabulary is uniformly lowercase.
TOKENS=(
  '\blode\b'
  '\bLode\b'
  '\bLODE\b'
  '\bcairn\b'
  '\bCairn\b'
  '\bCAIRN\b'
  '\bsigil\b'
  '\bSigil\b'
  '\bSIGIL\b'
  '\bechomesh\b'
  '\bEchomesh\b'
  '\bcairnmesh\b'
  '\bCairnmesh\b'
  '\bsigilmesh\b'
  '\bSigilmesh\b'
  '\bvigorepo\b'
  '\bVigorepo\b'
  '\bstones\b'
  '\bStones\b'
  '\bSTONES\b'
)

# Legacy paths. These would silently break runtime because the new
# binary writes/reads the new locations only.
PATHS=(
  '/var/lib/vigo/lode\b'
  '/var/lib/vigo/cairn\b'
  '/var/lib/vigo/sigil\b'
  '/var/lib/vigo/echo\b'
  '/api/v1/lode/'
  '/api/v1/cairn/'
  '/api/v1/sigil/'
  '/api/v1/echo/'
  'agent/src/cairn/'
  'agent/src/sigil/'
  'agent/src/echo/'
  'server/cairnmesh\b'
  'server/sigilmesh\b'
  'server/echomesh\b'
  'stockpile/stones\b'
)

# `echo` is too generic to whole-word grep — it's the shell builtin in
# every script and turns up in test fixtures. We don't add a global
# `\becho\b` token; instead we lean on the path patterns
# (`/api/v1/echo/`, `agent/src/echo/`, `~/echo/`, `~/.echo/`) and
# specific identifiers (`echomesh`, `EchoFleetURL`) which catch every
# substrate use without false-positiving the shell builtin.

# Files to scan. Tracked-only — git ls-files keeps untracked working-dir
# cruft (build artifacts, scratch notes) out of the scan.
EXTS_RE='\.(go|rs|md|vgo|vgo\.example|example|yaml|yml|sql|proto|html|tmpl|css|js|sh|toml)$|^Makefile$|^Dockerfile|^Cargo\.toml$|^go\.mod$'

# Build the master pattern: tokens | paths.
PATTERN=""
for t in "${TOKENS[@]}" "${PATHS[@]}"; do
  if [[ -z "$PATTERN" ]]; then PATTERN="$t"; else PATTERN="$PATTERN|$t"; fi
done

# Allowlist: lines of the form  path:line:term  (exact)
#                          or   path:*:term     (whole-file allowlist)
declare -a ALLOW_FILE_GLOBS=()
declare -A ALLOW_LINES
if [[ -f "$ALLOWLIST" ]]; then
  while IFS= read -r line; do
    [[ -z "$line" || "${line:0:1}" == "#" ]] && continue
    if [[ "$line" == *':*:'* ]]; then
      ALLOW_FILE_GLOBS+=("${line%%:\*:*}")
    else
      ALLOW_LINES["$line"]=1
    fi
  done < "$ALLOWLIST"
fi

is_file_allowlisted() {
  local f="$1"
  for g in "${ALLOW_FILE_GLOBS[@]}"; do
    if [[ "$f" == "$g" ]]; then return 0; fi
  done
  return 1
}

failed=0
total_hits=0

while IFS= read -r f; do
  [[ ! "$f" =~ $EXTS_RE ]] && continue
  is_file_allowlisted "$f" && continue

  while IFS= read -r match; do
    # match is "lineno:content"
    lineno="${match%%:*}"
    content="${match#*:}"
    # Identify which token matched (best-effort; just the first that hits).
    matched_term=""
    for t in "${TOKENS[@]}" "${PATHS[@]}"; do
      if grep -qE "$t" <<<"$content"; then matched_term="$t"; break; fi
    done
    key="$f:$lineno:$matched_term"
    [[ -n "${ALLOW_LINES[$key]:-}" ]] && continue
    printf '%s:%s: %s — matched: %s\n' "$f" "$lineno" "${content:0:120}" "$matched_term"
    failed=1
    total_hits=$((total_hits + 1))
  done < <(grep -nE "$PATTERN" "$f" || true)
done < <(git ls-files)

if [[ $failed -eq 0 ]]; then
  echo "check-legacy-naming: clean (0 hits)"
  exit 0
fi

echo
echo "check-legacy-naming: $total_hits hit(s) outside allowlist"
echo "Resolve each by either:"
echo "  - rewriting the legacy term to its new vocabulary, OR"
echo "  - adding a specific allowlist line to $ALLOWLIST"
exit 1
