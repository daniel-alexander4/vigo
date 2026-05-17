#!/usr/bin/env bash
set -euo pipefail

# Upgrade the Vigo server to the latest container image.
# Preserves: server.yaml, .env, db/, secrets/, tls/, license/, config-history/
# Replaces:  container image, example-configs/, scripts/
#
# Usage:
#   sudo scripts/upgrade.sh                  # pull/build only — does NOT touch the running container
#   sudo scripts/upgrade.sh --start          # also stop+start containers when work is done
#   sudo scripts/upgrade.sh --build          # full rebuild (Go + agent linux amd64+arm64 + image)
#   sudo scripts/upgrade.sh --server         # rebuild Go (server+CLI) only, fast
#   sudo scripts/upgrade.sh --agent          # rebuild Rust agent only
#   sudo scripts/upgrade.sh --cross          # cross-compile agent (linux amd64+arm64)
#   sudo scripts/upgrade.sh --cross all      # cross-compile all platforms
#   sudo scripts/upgrade.sh --push           # push image to GHCR after build
#   sudo scripts/upgrade.sh --test           # run test suite after upgrade
#
# Flags can be combined:
#   sudo scripts/upgrade.sh --build --start  # rebuild image AND deploy it now
#   sudo scripts/upgrade.sh --server --test --start  # rebuild Go, test, restart
#   sudo scripts/upgrade.sh --build --push   # full rebuild + push, leave running container alone
#   sudo scripts/upgrade.sh --cross arm64 freebsd --push
#
# Cross-compile targets:
#   amd64      Linux x86_64 (musl static)
#   arm64      Linux aarch64 (musl static)
#   darwin     macOS x86_64 + arm64 (requires osxcross in PATH)
#   freebsd    FreeBSD x86_64 (via cross)
#   windows    Windows x86_64 (via cross)
#   all        All of the above

DATA_DIR="/srv/vigo"
IMAGE="us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest"

if [ "$(id -u)" -ne 0 ]; then
    echo "Run as root: sudo $0" >&2
    exit 1
fi

# Resolve repo path before cd (needed for builds).
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"

# Ensure Go and Cargo are in PATH (sudo strips user PATH).
export PATH="/usr/local/go/bin:$HOME/.cargo/bin:/home/dan/.cargo/bin:$PATH"
export GOPATH="${GOPATH:-/home/dan/go}"

cd "$DATA_DIR"

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

# ---------------------------------------------------------------------------
# Parse flags
# ---------------------------------------------------------------------------

BUILD_ALL=false      # --build: full Docker image rebuild (Go + Rust + image)
BUILD_SERVER=false   # --server: rebuild Go binaries only, inject into container
BUILD_AGENT=false    # --agent: rebuild Rust agent only
PUSH=false           # --push: push image to GHCR
RUN_TESTS=false      # --test: run test-cli.sh after upgrade
START=false          # --start: stop+start containers when work is done (default: leave running state untouched)
CROSS_TARGETS=()
parsing_cross=false

for arg in "$@"; do
    case "$arg" in
        --build)   BUILD_ALL=true; parsing_cross=false ;;
        --server)  BUILD_SERVER=true; parsing_cross=false ;;
        --agent)   BUILD_AGENT=true; parsing_cross=false ;;
        --push)    PUSH=true; parsing_cross=false ;;
        --test)    RUN_TESTS=true; parsing_cross=false ;;
        --start)   START=true; parsing_cross=false ;;
        --cross)   BUILD_AGENT=true; parsing_cross=true ;;
        *)
            if $parsing_cross; then
                CROSS_TARGETS+=("$arg")
            fi
            ;;
    esac
done

# --cross with no args defaults to amd64+arm64.
if $parsing_cross && [ ${#CROSS_TARGETS[@]} -eq 0 ]; then
    CROSS_TARGETS=(amd64 arm64)
fi

# Expand "all".
expanded=()
for t in "${CROSS_TARGETS[@]+"${CROSS_TARGETS[@]}"}"; do
    if [ "$t" = "all" ]; then
        expanded=(amd64 arm64 darwin freebsd windows)
        break
    fi
    expanded+=("$t")
done
CROSS_TARGETS=("${expanded[@]+"${expanded[@]}"}")

# --build rebuilds the Docker image, which COPYs the host's dist/agent/
# tree in (Dockerfile.server) — so any stale arch left in dist/agent/ gets
# baked into the image and the fleet splits versions / loops on update.
# Make --build self-contained: cross-compile the deployed Linux arches
# first so the tree is fresh and internally consistent before the image
# build. (--cross all / --cross arm64 etc. still override this.)
if $BUILD_ALL && [ ${#CROSS_TARGETS[@]} -eq 0 ]; then
    CROSS_TARGETS=(amd64 arm64)
fi

# --build does a full Docker image rebuild. Go and Rust are both compiled
# inside the Dockerfile — no local toolchain needed. --server and --agent
# are for fast local rebuilds outside of Docker.
# Don't set BUILD_SERVER/BUILD_AGENT from --build — those mean "local build".

# Determine what kind of upgrade this is.
NEED_IMAGE_REBUILD=false
NEED_CONTAINER_RESTART=false
NEED_HOST_UPDATE=false

if $BUILD_ALL; then
    NEED_IMAGE_REBUILD=true
    NEED_CONTAINER_RESTART=true
    NEED_HOST_UPDATE=true
elif $BUILD_SERVER; then
    # Server binary runs inside Alpine (musl). Must rebuild the Docker image
    # so Go compiles in the Alpine build stage. Local build is host-only (CLI).
    NEED_IMAGE_REBUILD=true
    NEED_CONTAINER_RESTART=true
    NEED_HOST_UPDATE=true
elif $BUILD_AGENT; then
    NEED_CONTAINER_RESTART=true
    NEED_HOST_UPDATE=true
elif [ ${#CROSS_TARGETS[@]} -gt 0 ]; then
    NEED_HOST_UPDATE=true
else
    # Pull mode — pull latest image, restart.
    NEED_IMAGE_REBUILD=false
    NEED_CONTAINER_RESTART=true
    NEED_HOST_UPDATE=true
fi

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
# Stop containers
# ---------------------------------------------------------------------------

if $NEED_CONTAINER_RESTART && $START; then
    echo "Stopping containers..."
    docker compose down --remove-orphans 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# Cross-compile agent binaries (before Docker build so dist/ is populated)
# ---------------------------------------------------------------------------

if [ ${#CROSS_TARGETS[@]} -gt 0 ]; then
    # Sync Cargo.lock with Cargo.toml — manual version bumps don't update
    # the lockfile, and cross-compile containers would build the old version.
    if command -v cargo > /dev/null 2>&1; then
        echo "Syncing agent Cargo.lock..."
        (cd "$REPO_DIR/agent" && cargo update -p vigo --quiet 2>/dev/null) || true
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
                    docker run --rm --dns 1.1.1.1 --dns 8.8.8.8 -v "$REPO_DIR/agent:/build/agent" -v "$REPO_DIR/protobuf:/build/protobuf" -v "$REPO_DIR/dist:/build/dist" -w /build/agent \
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
                    docker run --rm --dns 1.1.1.1 --dns 8.8.8.8 -v "$REPO_DIR/agent:/build/agent" -v "$REPO_DIR/protobuf:/build/protobuf" -v "$REPO_DIR/dist:/build/dist" -w /build/agent \
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
                    (cd "$REPO_DIR" && make dist-agent-macos) || echo "  WARN: darwin cross-compile failed"
                else
                    echo "  SKIP: osxcross not found"
                fi
                ;;
            *)
                echo "  Unknown target: $t (valid: amd64, arm64, freebsd, windows, darwin, all)"
                ;;
        esac
    done

    # Run Linux builds (amd64/arm64) together in a single container.
    if [ -n "$BUILD_SCRIPT" ]; then
        docker run --rm --dns 1.1.1.1 --dns 8.8.8.8 \
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
fi

# ---------------------------------------------------------------------------
# Build Go binaries locally (--server or --build)
# ---------------------------------------------------------------------------

if $BUILD_SERVER; then
    echo "Building Go binaries..."
    cd "$REPO_DIR"
    VIGO_VERSION=$(cat "$REPO_DIR/VERSION" 2>/dev/null || echo dev)
    CGO_ENABLED=1 go build -ldflags="-X main.version=$VIGO_VERSION" -o bin/vigosrv ./server
    CGO_ENABLED=1 go build -ldflags="-X main.version=$VIGO_VERSION" -o bin/vigocli ./cli
    echo "  vigosrv:              $(ls -lh bin/vigosrv | awk '{print $5}')"
    echo "  vigocli:               $(ls -lh bin/vigocli | awk '{print $5}')"
    cd "$DATA_DIR"
fi

# ---------------------------------------------------------------------------
# Build Rust agent locally (--agent without --cross)
# ---------------------------------------------------------------------------

if $BUILD_AGENT && [ ${#CROSS_TARGETS[@]} -eq 0 ]; then
    echo "Building Rust agent..."
    cd "$REPO_DIR/agent"
    cargo build --release
    mkdir -p "$REPO_DIR/bin"
    cp .build/release/vigo "$REPO_DIR/bin/vigo"
    echo "  vigo:     $(ls -lh "$REPO_DIR/bin/vigo" | awk '{print $5}')"
    # Non-cross local build also runs as root under sudo; hand
    # ownership of agent/.build and the vigo binary back to the
    # invoking user so subsequent `cargo check` and `bin/vigo`
    # usage don't hit EACCES.
    if [ -n "$SUDO_USER" ]; then
        chown -R "$SUDO_USER:$SUDO_USER" "$REPO_DIR/agent/.build" 2>/dev/null || true
        chown "$SUDO_USER:$SUDO_USER" "$REPO_DIR/bin/vigo" 2>/dev/null || true
    fi
    cd "$DATA_DIR"
fi

# ---------------------------------------------------------------------------
# Docker image: full rebuild or pull
# ---------------------------------------------------------------------------

if $NEED_IMAGE_REBUILD; then
    # Bust all caches to ensure fresh binaries in the runtime image.
    touch "$REPO_DIR/protobuf/vigo.proto" "$REPO_DIR/agent/src/main.rs" 2>/dev/null || true

    # vigo's go.mod uses `replace git.alexander4.io/tessera => ../tessera`
    # so building needs the sibling tessera repo visible inside the Docker
    # build context. Docker build contexts can't reach outside their root,
    # so stage a combined tree in a tempdir: vigo as the context root,
    # tessera copied to `.build-deps/tessera/`, and rewrite the replace
    # directive in the staged go.mod to point at the in-context path. The
    # real repo's go.mod is untouched.
    TESSERA_DIR="$(dirname "$REPO_DIR")/tessera"
    if [ ! -d "$TESSERA_DIR" ]; then
        echo "ERROR: sibling tessera repo not found at $TESSERA_DIR" >&2
        echo "       vigo's go.mod requires it (replace => ../tessera)." >&2
        echo "       Clone it next to vigo: cd $(dirname "$REPO_DIR") && git clone <tessera-url>" >&2
        exit 1
    fi

    STAGE_DIR="$(mktemp -d -t vigo-build-XXXXXX)"
    trap 'rm -rf "$STAGE_DIR"' EXIT
    echo "Staging Docker build context at $STAGE_DIR (includes tessera)..."
    # Mirror the repo's .dockerignore exclusions: skip the Rust target dirs
    # (54+ GiB), host build outputs, git history, TLS certs, IDE state,
    # Claude scratch, and test/coverage artifacts.
    rsync -a \
        --exclude='agent/target/' \
        --exclude='agent/.build/' \
        --exclude='bin/' \
        --exclude='dist/server/' \
        --exclude='dist/cli/' \
        --exclude='.git/' \
        --exclude='certs/' \
        --exclude='.vscode/' \
        --exclude='.idea/' \
        --exclude='*.out' \
        --exclude='coverage.html' \
        --exclude='full.docker.reset.sh' \
        --exclude='.claude/' \
        "$REPO_DIR/" "$STAGE_DIR/"
    mkdir -p "$STAGE_DIR/.build-deps"
    rsync -a --exclude='.git/' --exclude='bin/' \
        "$TESSERA_DIR/" "$STAGE_DIR/.build-deps/tessera/"
    # Rewrite the replace directive to target the in-context path so
    # `go mod download` inside the builder resolves tessera without
    # needing to reach outside the Docker context.
    sed -i 's|=> \.\./tessera|=> ./.build-deps/tessera|' "$STAGE_DIR/go.mod"

    echo "Building Docker image from source..."
    DOCKER_BUILDKIT=1 docker build \
        --no-cache \
        -f "$STAGE_DIR/Dockerfile.server" -t "$IMAGE" "$STAGE_DIR"

    if $PUSH; then
        echo "Pushing image to registry..."
        docker push "$IMAGE"
        echo "Pushed $IMAGE"
    fi
elif ! $BUILD_SERVER && ! $BUILD_AGENT && ! $BUILD_ALL; then
    # Pull mode.
    echo "Pulling latest image..."
    docker compose pull vigo
fi

# ---------------------------------------------------------------------------
# Seed scripts from the image (or repo for --server)
# Docs stay inside the container at /seed/docs/ — not copied to bind mount.
# ---------------------------------------------------------------------------

echo "Updating scripts and compose file..."
if $BUILD_SERVER || $BUILD_AGENT || $BUILD_ALL; then
    # Copy directly from repo — faster than spinning up a container.
    mkdir -p "$DATA_DIR/scripts"
    # Copy all scripts that work without the source tree.
    for s in test-cli.sh upgrade.sh example2stockpile.sh reset-envoy.sh; do
        [ -f "$REPO_DIR/scripts/$s" ] && cp "$REPO_DIR/scripts/$s" "$DATA_DIR/scripts/$s"
    done
    chmod +x "$DATA_DIR"/scripts/*.sh 2>/dev/null || true
    # Keep docker-compose.yml in sync with the repo.
    cp "$REPO_DIR/docker-compose.prod.yml" "$DATA_DIR/docker-compose.yml"
else
    # Pull mode — seed from the image.
    docker run --rm -v "$DATA_DIR:$DATA_DIR" "$IMAGE" --seed-only 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# Start containers
# ---------------------------------------------------------------------------

if $NEED_CONTAINER_RESTART && $START; then
    echo "Starting containers..."
    docker compose up -d

    echo ""
    echo "Waiting for server..."
    timeout 60 docker compose logs -f vigo 2>&1 | grep -m1 "server ready" || true
elif $NEED_CONTAINER_RESTART && ! $START; then
    echo ""
    echo "Built artifacts are ready; running container is untouched."
    echo "Pass --start (or run 'docker compose up -d' from $DATA_DIR) to deploy."
fi

# Inject cross-compiled agent binaries into the running container so the
# bootstrap endpoint serves the freshly built binaries (not stale ones baked
# into the image). Must happen after the container starts.
#
# The .version sidecar files MUST be copied alongside each binary — the
# server reads them at startup to learn the binary's compiled-in version
# (it can't run a foreign-arch binary itself). Without the sidecar, the
# server disables auto-update for that arch (safe), but envoys never get
# new binaries. With a stale-or-missing sidecar, cross-arch envoys can
# get stuck in update loops — see tshoot/log-archive-rpi-update-loop-*.md.
inject_with_sidecar() {
    # $1 = host path to binary, $2 = container path
    local src="$1"
    local dst="$2"
    docker cp "$src" "vigo:$dst"
    if [ -f "$src.version" ]; then
        docker cp "$src.version" "vigo:$dst.version"
    fi
}

if [ ${#CROSS_TARGETS[@]} -gt 0 ] && docker ps -q -f name=vigo | grep -q .; then
    echo "Injecting cross-compiled agent binaries into container..."
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
# Install binaries to host
# ---------------------------------------------------------------------------

if $NEED_HOST_UPDATE; then
    echo "Updating host binaries..."

    if $BUILD_SERVER; then
        # --server: host CLI from local build, server binary from Docker image.
        cp "$REPO_DIR/bin/vigocli" /usr/local/bin/vigocli
    else
        # Pull or full rebuild: extract from the IMAGE, not a running container —
        # `--build` without `--start` leaves the old container down and the new
        # one not yet up. `docker create` makes an unstarted container we can
        # `docker cp` out of regardless of compose state.
        cid=$(docker create "$IMAGE")
        docker cp "$cid:/usr/local/bin/vigocli" /usr/local/bin/vigocli
        docker rm "$cid" >/dev/null
    fi
    # Root-only: peer auth trusts any localhost caller, so file mode is the gate.
    chown root:root /usr/local/bin/vigocli
    chmod 0700 /usr/local/bin/vigocli

    # Agent binary: NOT installed to the host here. The local envoy receives
    # the new binary via auto-update (server detects version mismatch on
    # check-in and sends force_update), same as all other envoys.
fi

echo ""
docker compose ps

# ---------------------------------------------------------------------------
# Config health check
# ---------------------------------------------------------------------------

if $NEED_CONTAINER_RESTART && docker ps -q -f name=vigo | grep -q .; then
    echo ""
    echo "Checking config health..."
    LIVE_DIR="/srv/vigo/stacks/.live"
    if [ -d "$LIVE_DIR" ]; then
        # Ask the server if config loaded successfully.
        RELOAD_ERR=$(docker exec vigo vigocli config trace __health_check__ 2>&1 || true)
        CONFIG_LOG=$(docker logs vigo 2>&1 | grep "config loaded" | tail -1)
        if echo "$CONFIG_LOG" | grep -q "envoys=0 modules=0"; then
            echo "  WARNING: server has empty config (envoys=0, modules=0)"
            echo "  Run: vigocli config publish"
            echo "  Then: docker compose restart vigo"
        elif echo "$CONFIG_LOG" | grep -q "error"; then
            echo "  WARNING: config loaded with errors"
            echo "  Run: vigocli config publish"
        else
            ENVOYS=$(echo "$CONFIG_LOG" | grep -o 'envoys=[0-9]*' | cut -d= -f2)
            MODULES=$(echo "$CONFIG_LOG" | grep -o 'modules=[0-9]*' | cut -d= -f2)
            echo "  OK     config loaded: ${ENVOYS:-?} envoys, ${MODULES:-?} modules"
        fi
    else
        echo "  No .live/ config — run: vigocli config publish"
    fi
fi

# ---------------------------------------------------------------------------
# Re-read agent versions in the server (after a --cross-only inject)
# ---------------------------------------------------------------------------
#
# vigosrv snapshots per-arch agent versions (detectAgentVersions) once, at
# startup, from the dist tree's vigo.version sidecars. When --cross fired
# WITHOUT a full image rebuild, the inject step above changed the running
# container's /dist/agent/ tree AFTER that snapshot — so the server is
# still advertising the old versions and envoys won't update. Restart it
# so it re-reads. (--build already brings the container up fresh from the
# rebuilt image, after the cross-compile, so its snapshot is current.)
if [ ${#CROSS_TARGETS[@]} -gt 0 ] && ! $BUILD_ALL && $START && docker ps -q -f name=vigo | grep -q .; then
    echo "Restarting vigo so it re-reads the injected agent versions..."
    (cd "$DATA_DIR" && docker compose restart vigo)
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
                echo "  MISMATCH agent $platform  binary=$BIN_VER  sidecar=$SIDE_VER  (server advertises the wrong version — envoys loop; re-run with --cross $cross_t)"
                STALE=1
            elif [ -n "$REPO_VERSION" ] && [ "$BIN_VER" != "$REPO_VERSION" ]; then
                echo "  STALE  agent $platform  version=$BIN_VER  (repo is $REPO_VERSION — re-run with --cross $cross_t)"
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
    echo "NOTE: Some files are stale. Use --build for a full image rebuild."
fi

# ---------------------------------------------------------------------------
# Run tests if requested
# ---------------------------------------------------------------------------

if $RUN_TESTS; then
    echo ""
    echo "Running CLI test suite..."
    "$DATA_DIR/scripts/test-cli.sh" --server "https://localhost:8443"
fi

echo ""
echo "Upgrade complete at $(date '+%Y-%m-%d %H:%M:%S %Z')."
