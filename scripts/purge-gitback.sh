#!/usr/bin/env bash
# Purge local gitback state on one or more envoys so they re-adopt clean.
#
# Wipes, per envoy:
#   - every user's ~/.vigo-gitback/             (per-user project state + auth chains)
#   - /var/lib/vigo/swarm/gitback/projects/     (root-owned bare repos)
#   - /root/.vigo-gitback/                      (if present)
#
# It does NOT run `vigo swarm gitback project leave` — peers keep the
# projects. After the agent re-announces and the user runs
# `vigo swarm puddle unlock`, ADR-023 auto-adoption (0.49.0+) re-creates the
# per-user state and the DR catch-up reactor re-pulls the bare repos within a
# couple of ticks. Use this to clear stale state (e.g. left over from before
# the `gitback://` rename) or to force a clean re-adopt.
#
# IMPORTANT: do NOT run this on the envoy you *founded* the projects on — that
# one holds the founder bootstrap tokens; purging it loses the project's
# authority anchor and forces `vigo swarm gitback project re-found`.
#
# Usage:
#   scripts/purge-gitback.sh <envoy> [<envoy> ...]          # dry run: report state, change nothing
#   scripts/purge-gitback.sh --yes <envoy> [<envoy> ...]    # actually purge
#   scripts/purge-gitback.sh --local [--yes]                # this host only, no SSH
#
# Requires SSH access to each envoy and a remote account that can sudo
# non-interactively. Linux envoys (uses getent / /etc/passwd to enumerate users).

set -uo pipefail

# Colors -- Catppuccin Mocha palette (24-bit RGB)
RED='\033[38;2;243;139;168m'     # #f38ba8
GREEN='\033[38;2;166;227;161m'   # #a6e3a1
YELLOW='\033[38;2;249;226;175m'  # #f9e2af
BLUE='\033[38;2;137;180;250m'    # #89b4fa
BOLD='\033[1m'
NC='\033[0m'

DO_PURGE=false
LOCAL=false
ENVOYS=()
for arg in "$@"; do
  case "$arg" in
    --yes|-y)  DO_PURGE=true ;;
    --local)   LOCAL=true ;;
    -h|--help)
      cat <<'USAGE'
purge-gitback.sh — wipe local gitback state on one or more envoys so they re-adopt clean.

Usage:
  scripts/purge-gitback.sh <envoy> [<envoy> ...]        # dry run: report state, change nothing
  scripts/purge-gitback.sh --yes <envoy> [<envoy> ...]  # actually purge
  scripts/purge-gitback.sh --local [--yes]              # this host only, no SSH

Per envoy, --yes wipes every user's ~/.vigo-gitback/, /var/lib/vigo/swarm/gitback/projects/,
and /root/.vigo-gitback/. It does NOT gossip a `project leave` — peers keep the projects;
after `vigo swarm puddle unlock` the envoy re-adopts them (ADR-023, 0.49.0+) and the DR reactor
re-pulls the bare repos. Do NOT run on the envoy you founded the projects on (it holds the
founder bootstrap tokens — purging it forces `vigo swarm gitback project re-found`).
USAGE
      exit 0 ;;
    --*)       echo "unknown flag: $arg" >&2; exit 2 ;;
    *)         ENVOYS+=("$arg") ;;
  esac
done

if ! $LOCAL && [ ${#ENVOYS[@]} -eq 0 ]; then
  echo "usage: $0 [--yes] <envoy> [<envoy> ...]   |   $0 --local [--yes]" >&2
  exit 2
fi

# ---- the snippet that runs ON an envoy --------------------------------------
# Stdin'd into `bash -s -- <mode>` where <mode> is "report" or "purge".
remote_script() {
cat <<'EOS'
set -uo pipefail
MODE="${1:-report}"

VIGO=/usr/local/sbin/vigo
[ -x "$VIGO" ] || VIGO="$(command -v vigo 2>/dev/null || true)"
if [ -z "$VIGO" ] || [ ! -x "$VIGO" ]; then
  echo "  ! vigo binary not found (looked at /usr/local/sbin/vigo and \$PATH)"
  exit 1
fi

# Enumerate users whose home holds ~/.vigo-gitback/.
list_users() {
  if command -v getent >/dev/null 2>&1; then getent passwd; else cat /etc/passwd; fi \
    | while IFS=: read -r name _ _ _ _ home _; do
        [ -n "$home" ] && [ -d "$home/.vigo-gitback" ] && printf '%s\t%s\n' "$name" "$home"
      done
}

PROJECTS_DIR=/var/lib/vigo/swarm/gitback/projects

if [ "$MODE" = report ]; then
  found=0
  while IFS=$'\t' read -r u h; do
    [ -z "$u" ] && continue
    found=1
    n=$(find "$h/.vigo-gitback" -maxdepth 1 -mindepth 1 -type d 2>/dev/null | wc -l | tr -d ' ')
    sz=$(du -sh "$h/.vigo-gitback" 2>/dev/null | cut -f1)
    echo "  user $u: $h/.vigo-gitback  ($n project(s), $sz)"
  done < <(list_users)
  [ "$found" = 0 ] && echo "  (no per-user ~/.vigo-gitback/ state)"
  if [ -d "$PROJECTS_DIR" ] && [ -n "$(ls -A "$PROJECTS_DIR" 2>/dev/null)" ]; then
    n=$(find "$PROJECTS_DIR" -maxdepth 1 -mindepth 1 -name '*.git' 2>/dev/null | wc -l | tr -d ' ')
    sz=$(sudo du -sh "$PROJECTS_DIR" 2>/dev/null | cut -f1)
    echo "  root: $PROJECTS_DIR  ($n bare repo(s), ${sz:-?})"
  else
    echo "  (no root-owned bare repos under $PROJECTS_DIR)"
  fi
  [ -d /root/.vigo-gitback ] && echo "  root: /root/.vigo-gitback  (present)"
  exit 0
fi

# MODE = purge
rc=0
while IFS=$'\t' read -r u h; do
  [ -z "$u" ] && continue
  echo "  purging $h/.vigo-gitback (user $u)"
  sudo -u "$u" "$VIGO" swarm gitback purge --yes 2>&1 | sed 's/^/    /' || { echo "    ! per-user purge for $u returned $?"; rc=1; }
done < <(list_users)
echo "  purging envoy-wide root-owned state"
sudo "$VIGO" swarm gitback purge --yes 2>&1 | sed 's/^/    /' || { echo "    ! root purge returned $?"; rc=1; }
exit $rc
EOS
}

run_on() {                # run_on <label> <ssh-target|""> <mode>
  local label="$1" target="$2" mode="$3"
  echo -e "${BOLD}${BLUE}== $label ==${NC}"
  if [ -z "$target" ]; then
    remote_script | bash -s -- "$mode"
  else
    remote_script | ssh -o BatchMode=yes "$target" "bash -s -- $mode"
  fi
  local rc=$?
  if [ $rc -ne 0 ]; then echo -e "  ${RED}(exit $rc)${NC}"; fi
  return $rc
}

# ---- dry run first (always) -------------------------------------------------
echo -e "${BOLD}gitback purge — current state${NC}"
if $LOCAL; then
  run_on "$(hostname) (local)" "" report
else
  for e in "${ENVOYS[@]}"; do run_on "$e" "$e" report; done
fi
echo

if ! $DO_PURGE; then
  echo -e "${YELLOW}Dry run. Re-run with --yes to actually purge.${NC}"
  echo "After purging, on each envoy run:  vigo swarm puddle unlock"
  echo "(0.49.0+ auto-adopts the puddle's founded projects; the DR reactor re-pulls the repos.)"
  exit 0
fi

echo -e "${BOLD}${RED}Purging in 5s — Ctrl-C to abort${NC}"
sleep 5
echo

fails=0
if $LOCAL; then
  run_on "$(hostname) (local)" "" purge || fails=$((fails+1))
else
  for e in "${ENVOYS[@]}"; do run_on "$e" "$e" purge || fails=$((fails+1)); done
fi

echo
if [ $fails -eq 0 ]; then
  echo -e "${GREEN}Done.${NC} On each purged envoy run:  vigo swarm puddle unlock"
  echo "ADR-023 auto-adoption re-creates per-user state; the catch-up reactor re-pulls each bare repo within ~1–2 ticks."
  echo "Verify:  vigocli swarm gitback list   (HOSTS counts should climb back up)"
else
  echo -e "${RED}$fails envoy(s) reported errors — see output above.${NC}"
  exit 1
fi
