#!/usr/bin/env bash
set -euo pipefail

# Upgrade the Vigo server to the latest container image.
# Preserves: server.yaml, .env, db/, secrets/, tls/, license/, config-history/
# Replaces:  container image, example-configs/, scripts/
#
# Usage:
#   sudo scripts/upgrade.sh                  # pull latest image and restart
#   sudo scripts/upgrade.sh --build          # rebuild everything from source
#   sudo scripts/upgrade.sh --server         # rebuild Go (server+CLI) only, fast
#   sudo scripts/upgrade.sh --agent          # rebuild Rust agent only
#   sudo scripts/upgrade.sh --cross          # cross-compile agent (linux amd64+arm64)
#   sudo scripts/upgrade.sh --cross all      # cross-compile all platforms
#   sudo scripts/upgrade.sh --push           # push image to GHCR after build
#   sudo scripts/upgrade.sh --test           # run test suite after upgrade
#
# Flags can be combined:
#   sudo scripts/upgrade.sh --server --test  # rebuild Go, test, restart
#   sudo scripts/upgrade.sh --build --push   # full rebuild + push
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
IMAGE="ghcr.io/daniel-alexander4/vigo:latest"

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

# ---------------------------------------------------------------------------
# Parse flags
# ---------------------------------------------------------------------------

BUILD_ALL=false      # --build: full Docker image rebuild (Go + Rust + image)
BUILD_SERVER=false   # --server: rebuild Go binaries only, inject into container
BUILD_AGENT=false    # --agent: rebuild Rust agent only
PUSH=false           # --push: push image to GHCR
RUN_TESTS=false      # --test: run test-cli.sh after upgrade
CROSS_TARGETS=()
parsing_cross=false

for arg in "$@"; do
    case "$arg" in
        --build)   BUILD_ALL=true; parsing_cross=false ;;
        --server)  BUILD_SERVER=true; parsing_cross=false ;;
        --agent)   BUILD_AGENT=true; parsing_cross=false ;;
        --push)    PUSH=true; parsing_cross=false ;;
        --test)    RUN_TESTS=true; parsing_cross=false ;;
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
# Stop containers
# ---------------------------------------------------------------------------

if $NEED_CONTAINER_RESTART; then
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
cp target/x86_64-unknown-linux-musl/release/vigo /build/dist/agent/linux/amd64/vigo"
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
cp target/aarch64-unknown-linux-musl/release/vigo /build/dist/agent/linux/arm64/vigo"
                ;;
            freebsd)
                echo "  freebsd: using cargo-cross container"
                (cd "$REPO_DIR" && \
                    docker run --rm --dns 1.1.1.1 --dns 8.8.8.8 -v "$REPO_DIR/agent:/build/agent" -v "$REPO_DIR/protobuf:/build/protobuf" -v "$REPO_DIR/dist:/build/dist" -w /build/agent \
                    ghcr.io/cross-rs/x86_64-unknown-freebsd:main sh -c '
                        cargo build --release --target x86_64-unknown-freebsd 2>&1
                        mkdir -p /build/dist/agent/freebsd/amd64
                        cp target/x86_64-unknown-freebsd/release/vigo /build/dist/agent/freebsd/amd64/vigo
                    ') || echo "  WARN: freebsd cross-compile failed"
                ;;
            windows)
                echo "  windows: using cargo-cross container"
                (cd "$REPO_DIR" && \
                    docker run --rm --dns 1.1.1.1 --dns 8.8.8.8 -v "$REPO_DIR/agent:/build/agent" -v "$REPO_DIR/protobuf:/build/protobuf" -v "$REPO_DIR/dist:/build/dist" -w /build/agent \
                    ghcr.io/cross-rs/x86_64-pc-windows-msvc:main sh -c '
                        cargo build --release --target x86_64-pc-windows-msvc 2>&1
                        mkdir -p /build/dist/agent/windows/amd64
                        cp target/x86_64-pc-windows-msvc/release/vigo.exe /build/dist/agent/windows/amd64/vigo.exe
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
        # Touch dist binaries so their mtime is newer than any source file
        # (cargo build with bind mounts can update src mtimes during multi-target builds).
        find "$REPO_DIR/dist/agent" -name 'vigo' -exec touch {} +
    fi

    echo ""
    echo "Agent binaries:"
    find "$REPO_DIR/dist/agent" -name 'vigo' -o -name 'vigo.exe' 2>/dev/null | while read -r f; do
        platform="${f#$REPO_DIR/dist/agent/}"
        platform="${platform%/vigo*}"
        size=$(ls -lh "$f" | awk '{print $5}')
        echo "  $platform  $size"
    done
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
    CGO_ENABLED=1 go build -ldflags="-X main.version=$VIGO_VERSION" -o bin/vigo-license-manager ./license-manager
    echo "  vigosrv:              $(ls -lh bin/vigosrv | awk '{print $5}')"
    echo "  vigocli:               $(ls -lh bin/vigocli | awk '{print $5}')"
    echo "  vigo-license-manager: $(ls -lh bin/vigo-license-manager | awk '{print $5}')"
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
    cp target/release/vigo "$REPO_DIR/bin/vigo"
    echo "  vigo:     $(ls -lh "$REPO_DIR/bin/vigo" | awk '{print $5}')"
    cd "$DATA_DIR"
fi

# ---------------------------------------------------------------------------
# Docker image: full rebuild or pull
# ---------------------------------------------------------------------------

if $NEED_IMAGE_REBUILD; then
    # Bust all caches to ensure fresh binaries in the runtime image.
    touch "$REPO_DIR/protobuf/vigo.proto" "$REPO_DIR/agent/src/main.rs" 2>/dev/null || true

    echo "Building Docker image from source..."
    DOCKER_BUILDKIT=1 docker build \
        --no-cache \
        -f "$REPO_DIR/Dockerfile.server" -t "$IMAGE" "$REPO_DIR"

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
    for s in test-cli.sh upgrade.sh install-license-manager.sh example2stockpile.sh reset-envoy.sh; do
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

if $NEED_CONTAINER_RESTART; then
    echo "Starting containers..."
    docker compose up -d

    echo ""
    echo "Waiting for server..."
    timeout 60 docker compose logs -f vigo 2>&1 | grep -m1 "server ready" || true
fi

# Inject cross-compiled agent binaries into the running container so the
# bootstrap endpoint serves the freshly built binaries (not stale ones baked
# into the image). Must happen after the container starts.
if [ ${#CROSS_TARGETS[@]} -gt 0 ] && docker ps -q -f name=vigo | grep -q .; then
    echo "Injecting cross-compiled agent binaries into container..."
    for t in "${CROSS_TARGETS[@]}"; do
        case "$t" in
            amd64)
                if [ -f "$REPO_DIR/dist/agent/linux/amd64/vigo" ]; then
                    docker cp "$REPO_DIR/dist/agent/linux/amd64/vigo" vigo:/dist/agent/linux/amd64/vigo
                    echo "  linux/amd64 → container"
                fi
                ;;
            arm64)
                if [ -f "$REPO_DIR/dist/agent/linux/arm64/vigo" ]; then
                    docker exec vigo mkdir -p /dist/agent/linux/arm64
                    docker cp "$REPO_DIR/dist/agent/linux/arm64/vigo" vigo:/dist/agent/linux/arm64/vigo
                    echo "  linux/arm64 → container"
                fi
                ;;
            freebsd)
                if [ -f "$REPO_DIR/dist/agent/freebsd/amd64/vigo" ]; then
                    docker exec vigo mkdir -p /dist/agent/freebsd/amd64
                    docker cp "$REPO_DIR/dist/agent/freebsd/amd64/vigo" vigo:/dist/agent/freebsd/amd64/vigo
                    echo "  freebsd/amd64 → container"
                fi
                ;;
            windows)
                if [ -f "$REPO_DIR/dist/agent/windows/amd64/vigo.exe" ]; then
                    docker exec vigo mkdir -p /dist/agent/windows/amd64
                    docker cp "$REPO_DIR/dist/agent/windows/amd64/vigo.exe" vigo:/dist/agent/windows/amd64/vigo.exe
                    echo "  windows/amd64 → container"
                fi
                ;;
            darwin)
                for arch in amd64 arm64; do
                    if [ -f "$REPO_DIR/dist/agent/darwin/$arch/vigo" ]; then
                        docker exec vigo mkdir -p /dist/agent/darwin/$arch
                        docker cp "$REPO_DIR/dist/agent/darwin/$arch/vigo" vigo:/dist/agent/darwin/$arch/vigo
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
        # Pull or full rebuild: extract from the new container.
        docker cp vigo:/usr/local/bin/vigocli /usr/local/bin/vigocli
    fi

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
    LIVE_DIR="/srv/vigo/.live"
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

# Compare container binaries against repo source (build modes only).
if ($BUILD_SERVER || $BUILD_ALL) && [ -d "$REPO_DIR" ]; then
    SRV_VER=$(docker exec vigo vigosrv --version 2>/dev/null | awk '{print $NF}' || echo "unknown")
    NEWEST_GO=$(find "$REPO_DIR/server" "$REPO_DIR/ui" "$REPO_DIR/cli" -name '*.go' -printf '%T@\n' 2>/dev/null | sort -rn | head -1)
    NEWEST_GO=${NEWEST_GO%.*}
    CTR_VIGOSRV=$(docker exec vigo stat -c '%Y' /usr/local/sbin/vigosrv 2>/dev/null || echo 0)
    if [ -n "$NEWEST_GO" ] && [ "$NEWEST_GO" -gt "$CTR_VIGOSRV" ]; then
        echo "  STALE  vigosrv  version=$SRV_VER  (source is newer — restart container to activate)"
        STALE=1
    else
        echo "  OK     vigosrv  version=$SRV_VER"
    fi
fi

# Agent freshness.
if ($BUILD_AGENT || $BUILD_ALL) && [ -d "$REPO_DIR" ]; then
    NEWEST_RS=$(find "$REPO_DIR/agent/src" -name '*.rs' -printf '%T@\n' 2>/dev/null | sort -rn | head -1)
    NEWEST_RS=${NEWEST_RS%.*}
    AGENT_PLATFORMS=$(docker exec vigo find /dist/agent -name 'vigo' -o -name 'vigo.exe' 2>/dev/null || true)
    if [ -n "$AGENT_PLATFORMS" ]; then
        for agent_path in $AGENT_PLATFORMS; do
            CTR_AGENT=$(docker exec vigo stat -c '%Y' "$agent_path" 2>/dev/null || echo 0)
            platform=$(echo "$agent_path" | sed 's|/dist/agent/||; s|/vigo.*||')
            # Read the version embedded in the binary itself, regardless of arch.
            # The agent ships a `VIGO-BUILD-VERSION=x.y.z` marker (see agent/src/main.rs).
            AGENT_VER=$(docker exec vigo grep -aoE 'VIGO-BUILD-VERSION=[0-9.]+' "$agent_path" 2>/dev/null | head -1 | cut -d= -f2)
            [ -z "$AGENT_VER" ] && AGENT_VER="unknown"
            if [ -n "$NEWEST_RS" ] && [ "$NEWEST_RS" -gt "$CTR_AGENT" ]; then
                echo "  STALE  agent $platform  version=$AGENT_VER  (source is newer than binary)"
                STALE=1
            else
                echo "  OK     agent $platform  version=$AGENT_VER"
            fi
        done
    fi
fi

# Check entrypoint.sh and Dockerfile.
if ($BUILD_ALL) && [ -d "$REPO_DIR" ]; then
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
