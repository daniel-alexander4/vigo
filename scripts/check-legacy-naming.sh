#!/usr/bin/env bash
# check-legacy-naming.sh — fail if any tracked file still uses a
# retired subsystem/vocabulary name.
#
# Substrate rename map (committed in 0.31.0):
#   lode    -> payload     (admin-pushed binary distribution)
#   cairn   -> swarmgit    (P2P git repo backup) — itself retired in 0.50.0, see below
#   echo    -> longdrawer  (LAN plaintext per-user dir sync)
#   sigil   -> lockbox     (LAN encrypted per-user dir sync)
#   stones  -> modules     (config primitive directory)
#
# Swarm-subsystem rename map (committed in 0.50.0):
#   swarmcast    -> filecast   (admin-pushed fleet file distribution)
#   swarmgit     -> gitback    (git over swarm; swarmgitmesh -> gitbackmesh,
#                               git-remote-swarmgit -> git-remote-gitback,
#                               swarmgit:// -> gitback://)
#   swarmresolve -> hostdox    (agent-managed /etc/hosts block from peer gossip)
#   swarmreg     -> curator    (planned P2P artifact registry, ADR-024)
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
  # --- 0.50.0 swarm-subsystem rename ---
  '\bswarmcast\b'
  '\bSwarmcast\b'
  '\bSWARMCAST\b'
  '\bswarmgit\b'
  '\bSwarmgit\b'
  '\bSWARMGIT\b'
  '\bswarmgitmesh\b'
  '\bSwarmgitmesh\b'
  '\bswarmresolve\b'
  '\bSwarmresolve\b'
  '\bSWARMRESOLVE\b'
  '\bswarmdns\b'
  '\bSwarmdns\b'
  '\bSWARMDNS\b'
  '\bswarmreg\b'
  '\bSwarmreg\b'
  '\bSWARMREG\b'
  # --- 0.71.0 module -> configcrate rename (the reusable resource-set
  #     config-kind; usercrate/hostcrate keep their names; kernel_module
  #     is the unrelated Linux kernel-module executor/trait, NOT retired).
  #     Distinctive identifiers only — the lowercase `modules:` /
  #     `exclude_modules:` YAML keys and `module_name`/`module_count` DB
  #     columns are guarded at runtime (loader hard-error + the 000016
  #     migration) and legitimately appear in those messages, so they are
  #     intentionally not static-checked here.
  '\bModuleConfig\b'
  '\bModuleDefinition\b'
  '\bModuleKind\b'
  '\bModuleRef\b'
  '\bPolicyModule\b'
  '\bModuleResult\b'
  '\bModuleCache\b'
  '\bModuleActions\b'
  '\bModuleComplianceFiles\b'
  '\bModuleProvisions\b'
  '\bModuleBundles\b'
  '\bModuleSources\b'
  '\bModuleHealthPct\b'
  '\bModuleFilter\b'
  '\bTopFailedModule\b'
  '\bvigo_module_actions\b'
  # --- 0.73.0 stockpile -> stacks collapse. The operator config-tree
  #     directory layer was removed (operators now edit /srv/vigo/stacks
  #     directly), and the word "stockpile" is fully retired: the gitback
  #     project handle, the `vigocli config stockpile` / `spanner stockpile`
  #     verbs, and the server/stockpilewatch package all became "stacks".
  #     The path-name scan below also catches a `stockpile/` directory name.
  '\bstockpile\b'
  '\bStockpile\b'
  '\bSTOCKPILE\b'
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
  # --- 0.50.0 swarm-subsystem rename ---
  '/var/lib/vigo/swarm/swarmgit\b'
  '/var/lib/vigo/swarm/swarmcast\b'
  '/api/v1/swarm/swarmgit/'
  '/api/v1/swarm/swarmcast/'
  '/api/v1/swarm/swarmreg/'
  'agent/src/swarm/swarmgit/'
  'server/swarm/swarmgitmesh\b'
  'git-remote-swarmgit\b'
  'swarmgit://'
  '\.vigo-swarmgit\b'
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

# ---------------------------------------------------------------------------
# Path-name scan
# ---------------------------------------------------------------------------
# The scan above greps file *bodies*. A retired *directory name* whose only
# file is content-free — e.g. an empty `.gitkeep` — leaves no legacy token in
# any scanned body, and `.gitkeep` isn't in EXTS_RE, so it sails past the
# content scan. That's exactly how `stockpile/stones/.gitkeep` survived
# `make lint` across the stones→modules→configcrates renames. Scan the tracked
# path strings themselves (every tracked file, no extension filter) against the
# same legacy patterns to catch retired vocabulary embedded in a path.
#
# Allowlist a path hit with a `<path>:path:<term>` line (the pseudo-line token
# is literally `path`), or whole-file with `<path>:*:<term>` as usual.
while IFS= read -r f; do
  is_file_allowlisted "$f" && continue
  matched_term=""
  for t in "${TOKENS[@]}" "${PATHS[@]}"; do
    if grep -qE "$t" <<<"$f"; then matched_term="$t"; break; fi
  done
  [[ -z "$matched_term" ]] && continue
  key="$f:path:$matched_term"
  [[ -n "${ALLOW_LINES[$key]:-}" ]] && continue
  printf '%s: (legacy name in path) — matched: %s\n' "$f" "$matched_term"
  failed=1
  total_hits=$((total_hits + 1))
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
