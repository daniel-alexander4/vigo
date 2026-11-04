#!/usr/bin/env bash
set -euo pipefail

# Rebuild and redeploy the Vigo server and agent binaries.
# Preserves: server.yaml, .env, db/, secrets/, tls/, config-history/
#
# Usage:
#   sudo scripts/upgrade.sh [components...] [--norun] [--push]
#
# Components — presence-based: name it to build it, omit it to skip it.
#   server-only  Rebuild vigosrv + vigocli. Hot-swap vigosrv into the
#                running container (ephemeral — a recreate reverts it);
#                refresh vigocli in the container and the host
#                /usr/local/bin. For UI / handler / API / CLI changes —
#                no agent rebuild.
#   full         Full Docker image rebuild (vigosrv + vigocli + amd64
#                agent), redeploy from the new image. Durable; installs
#                host vigocli.
#   both         Shorthand for `full arm64` — full image plus a fresh
#                arm64 cross-compile in the same run.
#   all          Shorthand for `full arm64 darwin freebsd windows` —
#                full image plus every supported cross-target.
#   amd64        Cross-compile the linux/amd64 agent, inject it into the
#                running container.
#   arm64        Cross-compile the linux/arm64 agent, inject it.
#   (darwin / freebsd / windows are also accepted as agent cross-targets.)
#
# Modifiers:
#   --norun  Build/swap but do not restart or redeploy the container.
#   --push   Push the rebuilt image to the registry (requires `full`).
#
# Rules:
#   - No components  → defaults to `full amd64 arm64` (full durable build).
#   - `full` already rebuilds vigosrv and the amd64 agent (the Dockerfile
#     rust-builder stage), so `server-only` and `amd64` are deduped when
#     `full` is present.
#   - `full` does NOT rebuild arm64 — it bakes whatever is in
#     dist/agent/linux/arm64/ and warns if that is stale. Add `arm64` to
#     refresh it in the same build.
#   - Unknown arguments are rejected.
#
# Examples:
#   sudo scripts/upgrade.sh server-only     # fast vigosrv hot-swap
#   sudo scripts/upgrade.sh both            # = full arm64
#   sudo scripts/upgrade.sh all             # = full arm64 darwin freebsd windows
#   sudo scripts/upgrade.sh arm64           # cross-compile + inject arm64
#   sudo scripts/upgrade.sh                 # = full amd64 arm64
#   sudo scripts/upgrade.sh full --push     # full image, push to registry

DATA_DIR="/srv/vigo"
IMAGE="us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest"

if [ "$(id -u)" -ne 0 ]; then
    echo "Run as root: sudo $0" >&2
    exit 1
fi

# Resolve repo path before cd (needed for builds).
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"

# Ensure Go and Cargo are in PATH (sudo strips user PATH). The toolchains
# belong to the user who ran sudo, so resolve that user's home from the
# passwd database rather than assuming one; with no SUDO_USER (a real root
# login) fall back to root's own home.
BUILD_HOME=""
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
    BUILD_HOME="$(getent passwd "$SUDO_USER" | cut -d: -f6 || true)"
fi
BUILD_HOME="${BUILD_HOME:-$HOME}"
export PATH="/usr/local/go/bin:$BUILD_HOME/.cargo/bin:$HOME/.cargo/bin:$PATH"
export GOPATH="${GOPATH:-$BUILD_HOME/go}"

# A fresh install has no data dir yet (the README runs this before seeding).
mkdir -p "$DATA_DIR"
cd "$DATA_DIR"

usage() {
    cat >&2 <<'EOF'
Usage: sudo scripts/upgrade.sh [components...] [--norun] [--push]

Components (name it to build it, omit it to skip it):
  server-only  hot-swap vigosrv + refresh vigocli (container + host); fast
  full         full Docker image rebuild + redeploy (durable; rebuilds amd64 agent)
  both         shorthand for `full arm64`
  all          shorthand for `full arm64 darwin freebsd windows` (every cross-target)
  amd64        cross-compile + inject the linux/amd64 agent
  arm64        cross-compile + inject the linux/arm64 agent
               (darwin / freebsd / windows are also accepted)

Modifiers:
  --norun  build/swap but do not restart the container
  --push   push the rebuilt image to the registry (requires `full`)

No components → defaults to `full amd64 arm64`.
EOF
}

# Extract an agent binary's compiled-in version from its embedded
# VIGO-BUILD-VERSION marker (the #[used]/#[no_mangle] static in
# agent/src/main.rs, section .vigo_version) — works for any target arch
# without executing the binary. The server reads the .version sidecar at
# startup to decide what version to advertise per arch; deriving the
# sidecar from this guarantees it can never disagree with the binary it
# labels (a disagreement = the agent's update guard aborts in a loop).
# Empty string if the marker is missing (should never happen).
agent_bin_version() {
    grep -aoE -m1 'VIGO-BUILD-VERSION=[0-9.]+' "$1" 2>/dev/null | cut -d= -f2
}

# Block until vigosrv logs "server ready" (or 60s elapse).
wait_for_server() {
    echo "Waiting for server..."
    timeout 60 docker compose logs -f vigo 2>&1 | grep -m1 "server ready" || true
}

# Copy an agent binary + its .version sidecar into the running container.
# The sidecar MUST travel with the binary — the server reads it at startup
# to learn the binary's compiled-in version (it can't run a foreign-arch
# binary itself). Without the sidecar the server disables auto-update for
# that arch (safe); with a stale-or-missing one, cross-arch envoys can get
# stuck in update loops — see tshoot/log-archive-rpi-update-loop-*.md.
inject_with_sidecar() {
    # $1 = host path to binary, $2 = container path. Also copies .version
    # sidecar (required: server reads it to advertise per-arch versions)
    # and .sig sidecar when present (ADR-031: agents won't auto-update
    # without it — silent omission would re-introduce today's symptom).
    local src="$1"
    local dst="$2"
    docker cp "$src" "vigo:$dst"
    if [ -f "$src.version" ]; then
        docker cp "$src.version" "vigo:$dst.version"
    fi
    if [ -f "$src.sig" ]; then
        docker cp "$src.sig" "vigo:$dst.sig"
    fi
}

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------

DO_SERVERONLY=false
DO_FULL=false
DO_AMD64=false
DO_ARM64=false
DO_DARWIN=false
DO_FREEBSD=false
DO_WINDOWS=false
START=true           # --norun clears this
PUSH=false           # --push sets this
n_components=0

for arg in "$@"; do
    case "$arg" in
        server-only) DO_SERVERONLY=true; n_components=$((n_components + 1)) ;;
        full)        DO_FULL=true;       n_components=$((n_components + 1)) ;;
        both)        DO_FULL=true; DO_ARM64=true; n_components=$((n_components + 1)) ;;
        all)         DO_FULL=true; DO_ARM64=true; DO_DARWIN=true; DO_FREEBSD=true; DO_WINDOWS=true; n_components=$((n_components + 1)) ;;
        amd64)       DO_AMD64=true;      n_components=$((n_components + 1)) ;;
        arm64)       DO_ARM64=true;      n_components=$((n_components + 1)) ;;
        darwin)      DO_DARWIN=true;     n_components=$((n_components + 1)) ;;
        freebsd)     DO_FREEBSD=true;    n_components=$((n_components + 1)) ;;
        windows)     DO_WINDOWS=true;    n_components=$((n_components + 1)) ;;
        --norun)     START=false ;;
        --push)      PUSH=true ;;
        *)
            echo "ERROR: unknown argument: $arg" >&2
            echo "" >&2
            usage
            exit 1
            ;;
    esac
done

# No components → full durable build.
if [ "$n_components" -eq 0 ]; then
    DO_FULL=true
    DO_AMD64=true
    DO_ARM64=true
fi

# --push only means something with an image rebuild.
if $PUSH && ! $DO_FULL; then
    echo "ERROR: --push requires the 'full' component — nothing to push otherwise." >&2
    exit 1
fi

# `full` already rebuilds vigosrv (→ server-only) and recompiles the amd64
# agent in-image (Dockerfile.server's rust-builder stage), so a standalone
# `server-only` or `amd64` step would be redundant — dedupe them silently.
if $DO_FULL; then
    DO_SERVERONLY=false
    DO_AMD64=false
fi

# Agent cross-compile targets. amd64 is absent here whenever `full` is set
# (built in-image); arm64/darwin/freebsd/windows always need an explicit
# cross-compile to reach dist/agent/.
CROSS_TARGETS=()
if $DO_AMD64;   then CROSS_TARGETS+=(amd64);   fi
if $DO_ARM64;   then CROSS_TARGETS+=(arm64);   fi
if $DO_DARWIN;  then CROSS_TARGETS+=(darwin);  fi
if $DO_FREEBSD; then CROSS_TARGETS+=(freebsd); fi
if $DO_WINDOWS; then CROSS_TARGETS+=(windows); fi

# ---------------------------------------------------------------------------
# Version consistency check
# ---------------------------------------------------------------------------
#
# If VERSION and agent/Cargo.toml disagree, the cross-compile will bake the
# Cargo.toml version into agent binaries while the Go server/CLI build will
# bake the VERSION-file number into vigosrv/vigocli — producing a split
# where envoys perpetually report a different version from the server and
# the force-update loop never converges. This exact race bit us once when
# a VERSION bump landed mid-upgrade.
#
# Check up-front and bail loudly instead of producing mismatched binaries.

if [ -d "$REPO_DIR" ]; then
    VERSION_FILE="$(cat "$REPO_DIR/VERSION" 2>/dev/null | tr -d '[:space:]')"
    CARGO_VERSION="$(grep -E -m1 '^version = ' "$REPO_DIR/agent/Cargo.toml" 2>/dev/null | sed -E 's/version = "([^"]+)"/\1/')"
    if [ -n "$VERSION_FILE" ] && [ -n "$CARGO_VERSION" ] && [ "$VERSION_FILE" != "$CARGO_VERSION" ]; then
        echo "ERROR: VERSION and agent/Cargo.toml are out of sync." >&2
        echo "  VERSION            = $VERSION_FILE" >&2
        echo "  agent/Cargo.toml   = $CARGO_VERSION" >&2
        echo "" >&2
        echo "Both files must agree before building — otherwise the agent" >&2
        echo "binaries and the server binary will be at different versions" >&2
        echo "and the force-update loop will never converge." >&2
        echo "" >&2
        echo "Fix: update whichever file is stale, then re-run this script." >&2
        exit 1
    fi
fi

# ---------------------------------------------------------------------------
# ADR-031 signing key detection
# ---------------------------------------------------------------------------
#
# When SIGN_KEY is present, every agent binary built here gets a matching
# ED25519 .sig and the agent's baked-in verification pubkey derives from
# the same private key. When the key is absent, build.rs falls back to
# the all-zero sentinel pubkey and the agent boots in dev mode (refuses
# auto-update). The build still succeeds in dev mode — operator just
# can't roll the fleet via auto-update.

SIGN_KEY="${SIGN_KEY:-$REPO_DIR/signing.key}"
SIGN_PUBKEY=""
if [ -s "$SIGN_KEY" ]; then
    SIGN_PUBKEY="$(cd "$REPO_DIR" && go run ./server/hardening/cmd/sign -key "$SIGN_KEY" -pubkey-only 2>/dev/null)"
    if [ -z "$SIGN_PUBKEY" ]; then
        echo "ERROR: failed to derive pubkey from SIGN_KEY=$SIGN_KEY" >&2
        exit 1
    fi
    echo "Signing key: $SIGN_KEY (pubkey ${SIGN_PUBKEY:0:16}…)"
else
    echo "WARNING: SIGN_KEY=$SIGN_KEY not found — building in ADR-031 dev mode (sentinel pubkey; agents will refuse auto-update)."
    echo "         Generate one with: go run ./server/hardening/cmd/sign -genkey -output $REPO_DIR/signing.key"
fi

# ---------------------------------------------------------------------------
# Cross-compile agent binaries (before the Docker build so dist/ is populated)
# ---------------------------------------------------------------------------

if [ ${#CROSS_TARGETS[@]} -gt 0 ]; then
    # Sync Cargo.lock with Cargo.toml — manual version bumps don't update
    # the lockfile, and cross-compile containers would build the old version.
    if command -v cargo > /dev/null 2>&1; then
        echo "Syncing agent Cargo.lock..."
        (cd "$REPO_DIR/agent" && cargo update -p vigo --quiet 2>/dev/null) || true
    fi

    # Pre-flight: detect stale cached binaries from a prior build at a
    # different version. Cargo's incremental rebuild can miss
    # CARGO_PKG_VERSION changes for workspace-root crates — a known
    # flakiness in env!() invalidation — so a partial cache hit will
    # quietly produce a binary with the previous version baked in even
    # when Cargo.toml is correct. The post-build gate below catches this,
    # but only after burning a full compile per arch; cheaper to clear the
    # vigo crate's fingerprint here and let cargo rebuild from clean.
    REPO_VERSION="$(cat "$REPO_DIR/VERSION" 2>/dev/null | tr -d '[:space:]')"
    if [ -n "$REPO_VERSION" ]; then
        for t in "${CROSS_TARGETS[@]}"; do
            case "$t" in
                amd64)   rust_target="x86_64-unknown-linux-musl"; bin_name="vigo" ;;
                arm64)   rust_target="aarch64-unknown-linux-musl"; bin_name="vigo" ;;
                freebsd) rust_target="x86_64-unknown-freebsd";    bin_name="vigo" ;;
                windows) rust_target="x86_64-pc-windows-msvc";    bin_name="vigo.exe" ;;
                *)       continue ;;
            esac
            cache_bin="$REPO_DIR/agent/.build/$rust_target/release/$bin_name"
            [ -f "$cache_bin" ] || continue
            cached_v="$(agent_bin_version "$cache_bin")"
            if [ -n "$cached_v" ] && [ "$cached_v" != "$REPO_VERSION" ]; then
                echo "Pre-flight: cached $t agent is $cached_v, VERSION says $REPO_VERSION — clearing stale vigo build."
                rm -f "$cache_bin"
                rm -rf "$REPO_DIR/agent/.build/$rust_target/release/.fingerprint/vigo-"*
            fi
        done
    fi

    echo "Cross-compiling agent for: ${CROSS_TARGETS[*]}"

    # Each cross-compile writes its vigo.version sidecar from the binary's
    # OWN embedded VIGO-BUILD-VERSION marker (agent/src/main.rs) — never
    # from Cargo.toml — so the sidecar can't drift from the binary. The
    # server reads these sidecars at startup to learn each arch's version
    # (it can't run a foreign-arch binary); a sidecar that disagrees with
    # its binary makes the server advertise the wrong version and the
    # agent's update guard aborts in a loop.

    RUST_TARGETS=""
    APT_PACKAGES="musl-tools protobuf-compiler"
    BUILD_SCRIPT=""

    for t in "${CROSS_TARGETS[@]}"; do
        case "$t" in
            amd64)
                RUST_TARGETS="$RUST_TARGETS x86_64-unknown-linux-musl"
                BUILD_SCRIPT="$BUILD_SCRIPT
echo 'Building linux/amd64...'
CC=musl-gcc cargo build --release --target x86_64-unknown-linux-musl
mkdir -p /build/dist/agent/linux/amd64
cp .build/x86_64-unknown-linux-musl/release/vigo /build/dist/agent/linux/amd64/vigo
grep -aoE -m1 'VIGO-BUILD-VERSION=[0-9.]+' /build/dist/agent/linux/amd64/vigo | cut -d= -f2 > /build/dist/agent/linux/amd64/vigo.version"
                ;;
            arm64)
                RUST_TARGETS="$RUST_TARGETS aarch64-unknown-linux-musl"
                APT_PACKAGES="$APT_PACKAGES gcc-aarch64-linux-gnu"
                BUILD_SCRIPT="$BUILD_SCRIPT
echo 'Building linux/arm64...'
CARGO_TARGET_AARCH64_UNKNOWN_LINUX_MUSL_LINKER=aarch64-linux-gnu-gcc \
CC_aarch64_unknown_linux_musl=aarch64-linux-gnu-gcc \
    cargo build --release --target aarch64-unknown-linux-musl
mkdir -p /build/dist/agent/linux/arm64
cp .build/aarch64-unknown-linux-musl/release/vigo /build/dist/agent/linux/arm64/vigo
grep -aoE -m1 'VIGO-BUILD-VERSION=[0-9.]+' /build/dist/agent/linux/arm64/vigo | cut -d= -f2 > /build/dist/agent/linux/arm64/vigo.version"
                ;;
            freebsd)
                echo "  freebsd: using cargo-cross container"
                (cd "$REPO_DIR" && \
                    docker run --rm --dns 1.1.1.1 --dns 8.8.8.8 \
                    -e VIGO_AGENT_SIGNING_PUBKEY="$SIGN_PUBKEY" \
                    -v "$REPO_DIR/agent:/build/agent" -v "$REPO_DIR/protobuf:/build/protobuf" -v "$REPO_DIR/dist:/build/dist" -w /build/agent \
                    ghcr.io/cross-rs/x86_64-unknown-freebsd:main sh -c '
                        cargo build --release --target x86_64-unknown-freebsd 2>&1
                        mkdir -p /build/dist/agent/freebsd/amd64
                        cp .build/x86_64-unknown-freebsd/release/vigo /build/dist/agent/freebsd/amd64/vigo
                        grep -aoE -m1 "VIGO-BUILD-VERSION=[0-9.]+" /build/dist/agent/freebsd/amd64/vigo | cut -d= -f2 > /build/dist/agent/freebsd/amd64/vigo.version
                    ') || echo "  WARN: freebsd cross-compile failed"
                ;;
            windows)
                echo "  windows: using cargo-cross container"
                (cd "$REPO_DIR" && \
                    docker run --rm --dns 1.1.1.1 --dns 8.8.8.8 \
                    -e VIGO_AGENT_SIGNING_PUBKEY="$SIGN_PUBKEY" \
                    -v "$REPO_DIR/agent:/build/agent" -v "$REPO_DIR/protobuf:/build/protobuf" -v "$REPO_DIR/dist:/build/dist" -w /build/agent \
                    ghcr.io/cross-rs/x86_64-pc-windows-msvc:main sh -c '
                        cargo build --release --target x86_64-pc-windows-msvc 2>&1
                        mkdir -p /build/dist/agent/windows/amd64
                        cp .build/x86_64-pc-windows-msvc/release/vigo.exe /build/dist/agent/windows/amd64/vigo.exe
                        grep -aoE -m1 "VIGO-BUILD-VERSION=[0-9.]+" /build/dist/agent/windows/amd64/vigo.exe | cut -d= -f2 > /build/dist/agent/windows/amd64/vigo.exe.version
                    ') || echo "  WARN: windows cross-compile failed"
                ;;
            darwin)
                echo "  darwin: requires osxcross"
                if command -v x86_64-apple-darwin-gcc > /dev/null 2>&1 || command -v o64-clang > /dev/null 2>&1; then
                    # `make dist-agent-macos` derives VIGO_AGENT_SIGNING_PUBKEY
                    # from SIGN_KEY internally; export it so the makefile picks it up.
                    (cd "$REPO_DIR" && SIGN_KEY="$SIGN_KEY" make dist-agent-macos) || echo "  WARN: darwin cross-compile failed"
                else
                    echo "  SKIP: osxcross not found"
                fi
                ;;
            *)
                echo "  Unknown target: $t (valid: amd64, arm64, freebsd, windows, darwin)"
                ;;
        esac
    done

    # Run Linux builds (amd64/arm64) together in a single container.
    if [ -n "$BUILD_SCRIPT" ]; then
        docker run --rm --dns 1.1.1.1 --dns 8.8.8.8 \
            -e VIGO_AGENT_SIGNING_PUBKEY="$SIGN_PUBKEY" \
            -v "$REPO_DIR/agent:/build/agent" \
            -v "$REPO_DIR/protobuf:/build/protobuf" \
            -v "$REPO_DIR/dist:/build/dist" \
            -w /build/agent \
            rust:bookworm sh -c "
                set -e
                apt-get update -qq && apt-get install -y -qq $APT_PACKAGES > /dev/null 2>&1
                rustup target add $RUST_TARGETS
                cargo update -p vigo --quiet 2>/dev/null || true
                $BUILD_SCRIPT
                echo 'Linux agent builds complete.'
            "
    fi

    # Cross-compiles in docker run as root and write to host-mounted
    # agent/.build/ + dist/agent/ — without this chown, the invoking
    # user can no longer `cargo check` from the host afterward and
    # every dist artifact is stuck as root:root. The whole point of
    # moving away from agent/target/ was to avoid this footgun, so
    # hand ownership back here.
    if [ -n "$SUDO_USER" ]; then
        [ -d "$REPO_DIR/agent/.build" ] && \
            chown -R "$SUDO_USER:$SUDO_USER" "$REPO_DIR/agent/.build" 2>/dev/null || true
        [ -d "$REPO_DIR/dist/agent" ] && \
            chown -R "$SUDO_USER:$SUDO_USER" "$REPO_DIR/dist/agent" 2>/dev/null || true
    fi

    echo ""
    echo "Agent binaries:"
    find "$REPO_DIR/dist/agent" \( -name 'vigo' -o -name 'vigo.exe' \) 2>/dev/null | while read -r f; do
        platform="${f#$REPO_DIR/dist/agent/}"
        platform="${platform%/vigo*}"
        size=$(ls -lh "$f" | awk '{print $5}')
        echo "  $platform  $size"
    done
    echo ""

    # Gate before anything bakes this tree into an image: every freshly
    # built agent binary must report the repo's VERSION via its embedded
    # VIGO-BUILD-VERSION marker. Catches a stale cargo cache-hit, a version
    # bump that landed mid-build, or a stripped marker — bail loudly rather
    # than ship a tree that loops the fleet on update.
    REPO_VERSION="$(cat "$REPO_DIR/VERSION" 2>/dev/null | tr -d '[:space:]')"
    for t in "${CROSS_TARGETS[@]}"; do
        case "$t" in
            amd64)   bins="$REPO_DIR/dist/agent/linux/amd64/vigo" ;;
            arm64)   bins="$REPO_DIR/dist/agent/linux/arm64/vigo" ;;
            freebsd) bins="$REPO_DIR/dist/agent/freebsd/amd64/vigo" ;;
            windows) bins="$REPO_DIR/dist/agent/windows/amd64/vigo.exe" ;;
            darwin)  bins="$REPO_DIR/dist/agent/darwin/amd64/vigo $REPO_DIR/dist/agent/darwin/arm64/vigo" ;;
            *)       continue ;;
        esac
        for b in $bins; do
            [ -f "$b" ] || continue   # this cross-compile may have failed (WARN'd above)
            bv="$(agent_bin_version "$b")"
            if [ -z "$bv" ]; then
                echo "ERROR: $b has no VIGO-BUILD-VERSION marker — refusing to ship it." >&2
                exit 1
            fi
            if [ -n "$REPO_VERSION" ] && [ "$bv" != "$REPO_VERSION" ]; then
                echo "ERROR: $b is version $bv but VERSION says $REPO_VERSION — stale build." >&2
                echo "       Run 'make clean' (or 'cd agent && cargo clean -p vigo'), clear dist/agent/, and re-run." >&2
                exit 1
            fi
        done
    done
    [ -n "$REPO_VERSION" ] && echo "Agent dist tree verified — all built arches at $REPO_VERSION, sidecars derived from binaries."
    echo ""

    # Sign each cross-compiled binary on the host (the in-image-built
    # linux/amd64 binary is signed inside go-builder via the BuildKit
    # secret; here we cover arm64/freebsd/windows/darwin). Writes
    # <binary>.sig next to each, format = signature hex on line 1,
    # pubkey hex on line 2. Agents at ADR-031 fail-closed against the
    # baked-in pubkey on auto-update.
    if [ -s "$SIGN_KEY" ]; then
        echo "Signing cross-compiled binaries with $SIGN_KEY..."
        for t in "${CROSS_TARGETS[@]}"; do
            case "$t" in
                amd64)   bins="$REPO_DIR/dist/agent/linux/amd64/vigo" ;;
                arm64)   bins="$REPO_DIR/dist/agent/linux/arm64/vigo" ;;
                freebsd) bins="$REPO_DIR/dist/agent/freebsd/amd64/vigo" ;;
                windows) bins="$REPO_DIR/dist/agent/windows/amd64/vigo.exe" ;;
                darwin)  bins="$REPO_DIR/dist/agent/darwin/amd64/vigo $REPO_DIR/dist/agent/darwin/arm64/vigo" ;;
                *)       continue ;;
            esac
            for b in $bins; do
                [ -f "$b" ] || continue
                (cd "$REPO_DIR" && go run ./server/hardening/cmd/sign \
                    -key "$SIGN_KEY" \
                    -binary "$b" \
                    -output "$b.sig") || { echo "ERROR: sign $b failed" >&2; exit 1; }
                if [ -n "$SUDO_USER" ]; then
                    chown "$SUDO_USER:$SUDO_USER" "$b.sig" 2>/dev/null || true
                fi
                echo "  signed $b"
            done
        done
        echo ""
    fi
fi

# ---------------------------------------------------------------------------
# Stage a self-contained Docker build context (shared by `server-only` and `full`)
# ---------------------------------------------------------------------------
#
# stage-build-context.sh copies the repo into a temp dir, minus host build
# outputs and other .dockerignore'd paths.

STAGE_DIR=""
if $DO_SERVERONLY || $DO_FULL; then
    STAGE_DIR="$(mktemp -d -t vigo-build-XXXXXX)"
    trap 'rm -rf "$STAGE_DIR"' EXIT
    echo "Staging Docker build context at $STAGE_DIR..."
    "$SCRIPT_DIR/stage-build-context.sh" "$REPO_DIR" "$STAGE_DIR"
fi

# ---------------------------------------------------------------------------
# server-only — hot-swap vigosrv + vigocli without a full image rebuild
# ---------------------------------------------------------------------------
#
# The UI templates + CSS are //go:embed'd into vigosrv, so a UI change means
# recompiling the binary. Build vigosrv + vigocli via Dockerfile.server's
# go-builder stage (same prod build environment + flags; `--target go-builder`
# skips the Rust stage entirely), then docker-cp them into place:
#   - vigosrv  → the running container only. Ephemeral: a container recreate
#                (docker compose up) reverts to the image's binary — that's
#                `full`'s job.
#   - vigocli  → both the running container AND the host /usr/local/bin, so
#                CLI-only changes (new subcommands, flags) land without a
#                full image rebuild. The host copy is durable; both copies
#                are refreshed so the freshness check below stays honest.

if $DO_SERVERONLY; then
    if ! docker ps -q -f name=vigo | grep -q .; then
        echo "ERROR: the vigo container is not running — 'server-only' hot-swaps binaries into it." >&2
        echo "       Start it with 'docker compose up -d', or run 'upgrade.sh full'." >&2
        exit 1
    fi
    echo "Building vigosrv + vigocli (go-builder stage)..."
    DOCKER_BUILD_SIGN_ARGS=()
    if [ -s "$SIGN_KEY" ]; then
        DOCKER_BUILD_SIGN_ARGS+=(--secret "id=signing_key,src=$SIGN_KEY")
    fi
    DOCKER_BUILDKIT=1 docker build \
        --target go-builder \
        --build-arg VIGO_AGENT_SIGNING_PUBKEY="$SIGN_PUBKEY" \
        "${DOCKER_BUILD_SIGN_ARGS[@]}" \
        -f "$STAGE_DIR/Dockerfile.server" -t vigo-uibuild:latest "$STAGE_DIR"
    cid="$(docker create vigo-uibuild:latest)"
    docker cp "$cid:/build/vigosrv" "$STAGE_DIR/vigosrv"
    docker cp "$cid:/build/vigocli" "$STAGE_DIR/vigocli"
    # vigosrv.sig exists only when SIGN_KEY was supplied (signed build with
    # the pubkey baked in). It must travel with the binary: a signed vigosrv
    # without its .sig fails closed at startup, and a stale .sig next to a
    # new binary fails its corruption check.
    rm -f "$STAGE_DIR/vigosrv.sig"
    docker cp "$cid:/build/vigosrv.sig" "$STAGE_DIR/vigosrv.sig" 2>/dev/null || true
    docker rm "$cid" >/dev/null

    # vigosrv → container (hot-swap; reverts on a container recreate).
    docker cp "$STAGE_DIR/vigosrv" vigo:/usr/local/sbin/vigosrv
    if [ -s "$STAGE_DIR/vigosrv.sig" ]; then
        docker cp "$STAGE_DIR/vigosrv.sig" vigo:/usr/local/sbin/vigosrv.sig
    else
        docker exec vigo rm -f /usr/local/sbin/vigosrv.sig
    fi
    echo "  vigosrv → container ($(ls -lh "$STAGE_DIR/vigosrv" | awk '{print $5}'))"

    # vigocli → container + host. The go-builder vigocli is the exact binary
    # the image ships at /usr/local/bin/vigocli (Dockerfile.server's runtime
    # COPY), so this is a faithful CLI refresh. Root-only on the host: peer
    # auth trusts any localhost caller, so the 0700 mode is the gate.
    docker cp "$STAGE_DIR/vigocli" vigo:/usr/local/bin/vigocli
    docker exec vigo chmod 0700 /usr/local/bin/vigocli
    cp "$STAGE_DIR/vigocli" /usr/local/bin/vigocli
    chown root:root /usr/local/bin/vigocli
    chmod 0700 /usr/local/bin/vigocli
    echo "  vigocli → container + host /usr/local/bin/vigocli ($(ls -lh "$STAGE_DIR/vigocli" | awk '{print $5}'))"
    echo "  (vigosrv hot-swap is ephemeral — a container recreate reverts it)"
fi

# ---------------------------------------------------------------------------
# full — full Docker image rebuild + redeploy
# ---------------------------------------------------------------------------

if $DO_FULL; then
    # The image build COPYs the host's dist/agent/ tree in (Dockerfile.server)
    # and recompiles only the amd64 agent in its rust-builder stage. arm64 is
    # baked from dist/ as-is — warn if it's stale, but proceed (the operator
    # can re-run with `arm64` to refresh it).
    if ! $DO_ARM64; then
        arm_bin="$REPO_DIR/dist/agent/linux/arm64/vigo"
        repo_v="$(cat "$REPO_DIR/VERSION" 2>/dev/null | tr -d '[:space:]')"
        if [ -f "$arm_bin" ]; then
            arm_v="$(agent_bin_version "$arm_bin")"
            if [ -n "$repo_v" ] && [ -n "$arm_v" ] && [ "$arm_v" != "$repo_v" ]; then
                echo "WARNING: dist/agent/linux/arm64 is $arm_v, repo VERSION is $repo_v —"
                echo "         the image will bake this stale arm64 agent and arm64 envoys"
                echo "         will lag. Re-run as 'upgrade.sh full arm64' to refresh it."
                echo ""
            fi
        else
            echo "WARNING: no dist/agent/linux/arm64/vigo — the image will ship no arm64"
            echo "         agent. Run 'upgrade.sh full arm64' if you have arm64 envoys."
            echo ""
        fi
    fi

    echo "Building Docker image from source..."
    DOCKER_BUILD_SIGN_ARGS=()
    if [ -s "$SIGN_KEY" ]; then
        DOCKER_BUILD_SIGN_ARGS+=(--secret "id=signing_key,src=$SIGN_KEY")
    fi
    DOCKER_BUILDKIT=1 docker build \
        --no-cache \
        --build-arg VIGO_AGENT_SIGNING_PUBKEY="$SIGN_PUBKEY" \
        "${DOCKER_BUILD_SIGN_ARGS[@]}" \
        -f "$STAGE_DIR/Dockerfile.server" -t "$IMAGE" "$STAGE_DIR"

    if $PUSH; then
        echo "Pushing image to registry..."
        docker push "$IMAGE"
        echo "Pushed $IMAGE"
    fi

    # Keep the deployed scripts + compose file in sync with the repo.
    echo "Updating scripts and compose file..."
    mkdir -p "$DATA_DIR/scripts"
    for s in test-cli.sh upgrade.sh stage-build-context.sh example2stacks.sh reset-envoy.sh; do
        [ -f "$REPO_DIR/scripts/$s" ] && cp "$REPO_DIR/scripts/$s" "$DATA_DIR/scripts/$s"
    done
    chmod +x "$DATA_DIR"/scripts/*.sh 2>/dev/null || true
    cp "$REPO_DIR/docker-compose.prod.yml" "$DATA_DIR/docker-compose.yml"

    if $START; then
        echo "Redeploying container from the new image..."
        docker compose down --remove-orphans 2>/dev/null || true
        docker compose up -d
        echo ""
        wait_for_server
    else
        echo ""
        echo "Image built; the running container is untouched (--norun)."
        echo "Run 'docker compose up -d' from $DATA_DIR to deploy."
    fi

    # Host vigocli — extracted from the freshly built image (works whether or
    # not the container is up). Root-only (0700): it is the binary that reads
    # the root-only local-admin token.
    echo "Updating host vigocli..."
    cid="$(docker create "$IMAGE")"
    docker cp "$cid:/usr/local/bin/vigocli" /usr/local/bin/vigocli
    docker rm "$cid" >/dev/null
    chown root:root /usr/local/bin/vigocli
    chmod 0700 /usr/local/bin/vigocli

    # The local envoy's agent binary is NOT installed here — it arrives via
    # auto-update (the server detects the version mismatch on check-in and
    # sends force_update), same as every other envoy.
fi

# ---------------------------------------------------------------------------
# Inject cross-compiled agent binaries into the running container
# ---------------------------------------------------------------------------
#
# Only when NOT rebuilding the image — a `full` build bakes the dist tree
# in via COPY (and recompiles amd64 in-Dockerfile), so an inject would be
# redundant. Must run while the container is up; the restart below makes the
# server re-read the injected .version sidecars.

if [ ${#CROSS_TARGETS[@]} -gt 0 ] && ! $DO_FULL && docker ps -q -f name=vigo | grep -q .; then
    echo "Injecting cross-compiled agent binaries into the container..."
    for t in "${CROSS_TARGETS[@]}"; do
        case "$t" in
            amd64)
                if [ -f "$REPO_DIR/dist/agent/linux/amd64/vigo" ]; then
                    inject_with_sidecar "$REPO_DIR/dist/agent/linux/amd64/vigo" /dist/agent/linux/amd64/vigo
                    echo "  linux/amd64 → container"
                fi
                ;;
            arm64)
                if [ -f "$REPO_DIR/dist/agent/linux/arm64/vigo" ]; then
                    docker exec vigo mkdir -p /dist/agent/linux/arm64
                    inject_with_sidecar "$REPO_DIR/dist/agent/linux/arm64/vigo" /dist/agent/linux/arm64/vigo
                    echo "  linux/arm64 → container"
                fi
                ;;
            freebsd)
                if [ -f "$REPO_DIR/dist/agent/freebsd/amd64/vigo" ]; then
                    docker exec vigo mkdir -p /dist/agent/freebsd/amd64
                    inject_with_sidecar "$REPO_DIR/dist/agent/freebsd/amd64/vigo" /dist/agent/freebsd/amd64/vigo
                    echo "  freebsd/amd64 → container"
                fi
                ;;
            windows)
                if [ -f "$REPO_DIR/dist/agent/windows/amd64/vigo.exe" ]; then
                    docker exec vigo mkdir -p /dist/agent/windows/amd64
                    inject_with_sidecar "$REPO_DIR/dist/agent/windows/amd64/vigo.exe" /dist/agent/windows/amd64/vigo.exe
                    echo "  windows/amd64 → container"
                fi
                ;;
            darwin)
                for arch in amd64 arm64; do
                    if [ -f "$REPO_DIR/dist/agent/darwin/$arch/vigo" ]; then
                        docker exec vigo mkdir -p /dist/agent/darwin/$arch
                        inject_with_sidecar "$REPO_DIR/dist/agent/darwin/$arch/vigo" /dist/agent/darwin/$arch/vigo
                        echo "  darwin/$arch → container"
                    fi
                done
                ;;
        esac
    done
fi

# ---------------------------------------------------------------------------
# Activate non-full swaps (server-only hot-swap / agent inject)
# ---------------------------------------------------------------------------
#
# A swapped vigosrv binary or injected agent does nothing until vigosrv
# restarts — it re-execs the new binary and re-reads the agent .version
# sidecars. `full` already redeployed above, so this only runs for
# non-`full` builds.

if ! $DO_FULL && { $DO_SERVERONLY || [ ${#CROSS_TARGETS[@]} -gt 0 ]; }; then
    if ! $START; then
        echo ""
        echo "Binaries swapped into the container but not activated (--norun)."
        echo "Run 'docker compose restart vigo' from $DATA_DIR to activate."
    elif docker ps -q -f name=vigo | grep -q .; then
        echo "Restarting vigo to activate the swap..."
        docker compose restart vigo
        echo ""
        wait_for_server
    fi
fi

echo ""
# Before the first --seed-only there is no .env, and compose refuses to parse
# a file whose env_file is missing; there is nothing to list yet anyway.
if [ -f "$DATA_DIR/.env" ]; then
    docker compose ps
fi

# ---------------------------------------------------------------------------
# Config health check
# ---------------------------------------------------------------------------

if docker ps -q -f name=vigo | grep -q .; then
    echo ""
    echo "Checking config health..."
    LIVE_DIR="/srv/vigo/.live"
    if [ -d "$LIVE_DIR" ]; then
        # Ask the server if config loaded successfully.
        RELOAD_ERR=$(docker exec vigo vigocli config trace __health_check__ 2>&1 || true)
        CONFIG_LOG=$(docker logs vigo 2>&1 | grep "config loaded" | tail -1)
        if echo "$CONFIG_LOG" | grep -q "envoys=0 configcrates=0"; then
            echo "  WARNING: server has empty config (envoys=0, configcrates=0)"
            echo "  Run: vigocli config publish"
            echo "  Then: docker compose restart vigo"
        elif echo "$CONFIG_LOG" | grep -q "error"; then
            echo "  WARNING: config loaded with errors"
            echo "  Run: vigocli config publish"
        else
            # || true: never let a missing field abort the script under
            # `set -euo pipefail` — the ${VAR:-?} fallbacks render "?" instead.
            ENVOYS=$(echo "$CONFIG_LOG" | grep -o 'envoys=[0-9]*' | cut -d= -f2 || true)
            CONFIGCRATES=$(echo "$CONFIG_LOG" | grep -o 'configcrates=[0-9]*' | cut -d= -f2 || true)
            echo "  OK     config loaded: ${ENVOYS:-?} envoys, ${CONFIGCRATES:-?} configcrates"
        fi
    else
        echo "  No .live/ config — run: vigocli config publish"
    fi
fi

# ---------------------------------------------------------------------------
# Freshness check
# ---------------------------------------------------------------------------

echo ""
echo "Checking freshness..."

STALE=0

# Compare host CLI against container CLI.
if [ -f /usr/local/bin/vigocli ]; then
    HOST_VER=$(vigocli --version 2>/dev/null | awk '{print $NF}' || echo "unknown")
    CTR_VER=$(docker exec vigo vigocli --version 2>/dev/null | awk '{print $NF}' || echo "unknown")
    HOST_MD5=$(md5sum /usr/local/bin/vigocli 2>/dev/null | awk '{print $1}')
    CTR_MD5=$(docker exec vigo md5sum /usr/local/bin/vigocli 2>/dev/null | awk '{print $1}')
    if [ "$HOST_MD5" != "$CTR_MD5" ]; then
        echo "  STALE  vigocli  host=$HOST_VER  container=$CTR_VER  (host differs from container)"
        STALE=1
    else
        echo "  OK     vigocli  version=$HOST_VER"
    fi
fi

# Compare container vigosrv against repo source. Runs regardless of build
# mode — freshness is informational, not gated on rebuild intent.
if [ -d "$REPO_DIR" ]; then
    SRV_VER=$(docker exec vigo vigosrv --version 2>/dev/null | awk '{print $NF}' || echo "unknown")
    NEWEST_GO=$(find "$REPO_DIR/server" "$REPO_DIR/ui" "$REPO_DIR/cli" -name '*.go' -printf '%T@\n' 2>/dev/null | awk 'BEGIN{m=0} $0>m{m=$0} END{if(m>0)print m}')
    NEWEST_GO=${NEWEST_GO%.*}
    CTR_VIGOSRV=$(docker exec vigo stat -c '%Y' /usr/local/sbin/vigosrv 2>/dev/null || echo 0)
    if [ -n "$NEWEST_GO" ] && [ "$NEWEST_GO" -gt "$CTR_VIGOSRV" ]; then
        echo "  STALE  vigosrv  version=$SRV_VER  (source is newer — restart container to activate)"
        STALE=1
    else
        echo "  OK     vigosrv  version=$SRV_VER"
    fi
fi

# Agent freshness — per arch shipped in the dist tree. The check that
# matters: each binary's embedded version must equal the repo VERSION,
# AND its .version sidecar (which the server reads at startup to decide
# what to advertise per arch) must equal the binary. A sidecar that
# disagrees with its binary puts that arch's envoys in an update loop.
# (Container runs busybox: LC_ALL=C forces byte-treatment of the binary
# so the regex matches; the trailing `|| true`s keep a missing marker
# from tripping `set -e` and aborting the whole freshness check.)
if [ -d "$REPO_DIR" ]; then
    REPO_VERSION="$(cat "$REPO_DIR/VERSION" 2>/dev/null | tr -d '[:space:]')"
    AGENT_PLATFORMS=$(docker exec vigo find /dist/agent \( -name 'vigo' -o -name 'vigo.exe' \) 2>/dev/null || true)
    if [ -n "$AGENT_PLATFORMS" ]; then
        for agent_path in $AGENT_PLATFORMS; do
            platform=$(echo "$agent_path" | sed 's|/dist/agent/||; s|/vigo.*||')
            cross_t=$(echo "$platform" | sed 's|^linux/||; s|/.*$||')   # linux/amd64 → amd64
            BIN_VER=$(docker exec vigo sh -c "LC_ALL=C grep -oE -m1 'VIGO-BUILD-VERSION=[0-9.]+' '$agent_path'" 2>/dev/null | cut -d= -f2 || true)
            SIDE_VER=$(docker exec vigo sh -c "cat '$agent_path.version' 2>/dev/null" 2>/dev/null | tr -d '[:space:]' || true)
            [ -z "$BIN_VER" ] && BIN_VER="unknown"
            [ -z "$SIDE_VER" ] && SIDE_VER="(missing)"
            if [ "$SIDE_VER" != "$BIN_VER" ]; then
                echo "  MISMATCH agent $platform  binary=$BIN_VER  sidecar=$SIDE_VER  (server advertises the wrong version — envoys loop; re-run: upgrade.sh $cross_t)"
                STALE=1
            elif [ -n "$REPO_VERSION" ] && [ "$BIN_VER" != "$REPO_VERSION" ]; then
                echo "  STALE  agent $platform  version=$BIN_VER  (repo is $REPO_VERSION — re-run: upgrade.sh $cross_t)"
                STALE=1
            else
                echo "  OK     agent $platform  version=$BIN_VER"
            fi
        done
    fi
fi

# Check entrypoint.sh against the copy baked into the container.
if [ -d "$REPO_DIR" ]; then
    REPO_EP_MD5=$(md5sum "$REPO_DIR/entrypoint.sh" 2>/dev/null | awk '{print $1}')
    CTR_EP_MD5=$(docker exec vigo md5sum /entrypoint.sh 2>/dev/null | awk '{print $1}')
    if [ "$REPO_EP_MD5" != "$CTR_EP_MD5" ]; then
        echo "  STALE  entrypoint.sh (repo differs from container)"
        STALE=1
    else
        echo "  OK     entrypoint.sh"
    fi
fi

echo ""
if [ "$STALE" -eq 0 ]; then
    echo "All files are fresh."
else
    echo "NOTE: Some files are stale. Run 'upgrade.sh full' for a full image rebuild."
fi

echo ""
echo "Upgrade complete at $(date '+%Y-%m-%d %H:%M:%S %Z')."
