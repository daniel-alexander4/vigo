#!/usr/bin/env bash
# migrate-0.30.sh — rename on-disk state for the 0.29.2 → 0.29.3 subsystem rename.
#
# Run as root on every envoy before installing the new vigo agent binary.
# Idempotent: safe to rerun.
#
# Renames (if source exists and target is absent):
#   /var/lib/vigo/vigorepo      → /var/lib/vigo/cairn
#   ~<user>/vigosync            → ~<user>/echo
#   ~<user>/vigorepo            → ~<user>/cairn
#   ~<user>/vigocrypt           → ~<user>/sigil
#   ~<user>/.vigosync           → ~<user>/.echo
#   ~<user>/.vigocrypt          → ~<user>/.sigil
#   ~<user>/vigorepo/<proj>/.vigorepo.yaml → ~<user>/cairn/<proj>/.cairn.yaml
#
# Does NOT touch:
#   /var/lib/vigo/swarm (unchanged — Swarm kept its name)
#   Dan's monorepo checkout at ~dan/vigorepo/<clone>/
#     (this script only renames dirs that match the subsystem pattern; the
#     monorepo has no .vigorepo.yaml sidecar so it's invisible to the scan)

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "migrate-0.30.sh: must run as root" >&2
  exit 1
fi

log() { printf '[migrate-0.30] %s\n' "$*"; }

rename_if() {
  local src="$1" dst="$2"
  if [[ -e "$dst" ]]; then
    log "skip: $dst already exists"
    return 0
  fi
  if [[ -e "$src" ]]; then
    mv -- "$src" "$dst"
    log "moved: $src → $dst"
  fi
}

# ---- system-wide state ----
rename_if /var/lib/vigo/vigorepo /var/lib/vigo/cairn

# ---- per-user state ----
# Walk real human users from /etc/passwd (uid ≥ 1000, valid login shell).
while IFS=: read -r user _ uid _ _ home shell; do
  [[ "$uid" -ge 1000 ]] || continue
  [[ -d "$home" ]] || continue
  case "$shell" in
    */nologin|*/false|"") continue ;;
  esac

  # Per-user directories.
  rename_if "$home/vigosync"   "$home/echo"
  rename_if "$home/vigorepo"   "$home/cairn"
  rename_if "$home/vigocrypt"  "$home/sigil"
  rename_if "$home/.vigosync"  "$home/.echo"
  rename_if "$home/.vigocrypt" "$home/.sigil"

  # Sidecar rename inside each cairn project.
  if [[ -d "$home/cairn" ]]; then
    find "$home/cairn" -maxdepth 2 -name '.vigorepo.yaml' -print0 2>/dev/null | \
      while IFS= read -r -d '' sidecar; do
        new="${sidecar%.vigorepo.yaml}.cairn.yaml"
        if [[ -e "$new" ]]; then
          log "skip: $new already exists"
        else
          mv -- "$sidecar" "$new"
          log "moved: $sidecar → $new"
        fi
      done
  fi
done < /etc/passwd

log "done"
