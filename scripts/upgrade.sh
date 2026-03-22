#!/usr/bin/env bash
set -euo pipefail

# Upgrade the Vigo server to the latest container image.
# Preserves: server.yaml, .env, db/, secrets/, tls/, license/, config-history/
# Replaces:  container image, docs/, examples/, scripts/
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
                    docker run --rm -v "$REPO_DIR/agent-rs:/build/agent-rs" -v "$REPO_DIR/proto:/build/proto" -v "$REPO_DIR/dist:/build/dist" -w /build/agent-rs \
                    ghcr.io/cross-rs/x86_64-unknown-freebsd:main sh -c '
                        cargo build --release --target x86_64-unknown-freebsd 2>&1
                        mkdir -p /build/dist/agent/freebsd/amd64
                        cp target/x86_64-unknown-freebsd/release/vigo /build/dist/agent/freebsd/amd64/vigo
                    ') || echo "  WARN: freebsd cross-compile failed"
                ;;
            windows)
                echo "  windows: using cargo-cross container"
                (cd "$REPO_DIR" && \
                    docker run --rm -v "$REPO_DIR/agent-rs:/build/agent-rs" -v "$REPO_DIR/proto:/build/proto" -v "$REPO_DIR/dist:/build/dist" -w /build/agent-rs \
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
        docker run --rm \
            -v "$REPO_DIR/agent-rs:/build/agent-rs" \
            -v "$REPO_DIR/proto:/build/proto" \
            -v "$REPO_DIR/dist:/build/dist" \
            -w /build/agent-rs \
            rust:bookworm sh -c "
                set -e
                apt-get update -qq && apt-get install -y -qq $APT_PACKAGES > /dev/null 2>&1
                rustup target add $RUST_TARGETS
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
    CGO_ENABLED=1 go build -ldflags="-X main.version=$VIGO_VERSION" -o bin/vigogo ./cli
    echo "  vigosrv:       $(ls -lh bin/vigosrv | awk '{print $5}')"
    echo "  vigogo:  $(ls -lh bin/vigogo | awk '{print $5}')"
    cd "$DATA_DIR"
fi

# ---------------------------------------------------------------------------
# Build Rust agent locally (--agent without --cross)
# ---------------------------------------------------------------------------

if $BUILD_AGENT && [ ${#CROSS_TARGETS[@]} -eq 0 ]; then
    echo "Building Rust agent..."
    cd "$REPO_DIR/agent-rs"
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
    touch "$REPO_DIR/proto/vigo.proto" "$REPO_DIR/agent-rs/src/main.rs" 2>/dev/null || true

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
# Seed docs, examples, and scripts from the image (or repo for --server)
# ---------------------------------------------------------------------------

echo "Updating docs, scripts, and compose file..."
if $BUILD_SERVER || $BUILD_AGENT || $BUILD_ALL; then
    # Copy directly from repo — faster than spinning up a container.
    mkdir -p "$DATA_DIR/docs" "$DATA_DIR/scripts"
    cp -r "$REPO_DIR/docs/." "$DATA_DIR/docs/"
    # Copy all scripts that work without the source tree.
    for s in test-cli.sh upgrade.sh install-license-manager.sh use-example.sh reset-envoy.sh; do
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
        cp "$REPO_DIR/bin/vigogo" /usr/local/bin/vigogo
    else
        # Pull or full rebuild: extract from the new container.
        docker cp vigo:/usr/local/bin/vigogo /usr/local/bin/vigogo
    fi

    # Agent binary for the host.
    HOST_ARCH=$(uname -m)
    case "$HOST_ARCH" in
        x86_64)  AGENT_ARCH="amd64" ;;
        aarch64) AGENT_ARCH="arm64" ;;
        *)       AGENT_ARCH="$HOST_ARCH" ;;
    esac
    HOST_OS=$(uname -s | tr '[:upper:]' '[:lower:]')

    # Stop the agent service before overwriting the binary (avoids ETXTBSY).
    systemctl stop vigo-envoy 2>/dev/null || true

    AGENT_INSTALLED=false
    # Prefer host dist/ for cross-compiled binaries (fresher than what's in the container image).
    if [ -f "$REPO_DIR/dist/agent/$HOST_OS/$AGENT_ARCH/vigo" ]; then
        cp "$REPO_DIR/dist/agent/$HOST_OS/$AGENT_ARCH/vigo" /usr/local/sbin/vigo
        echo "Installed agent binary from dist: $HOST_OS/$AGENT_ARCH"
        AGENT_INSTALLED=true
    elif $BUILD_AGENT && [ -f "$REPO_DIR/bin/vigo" ]; then
        cp "$REPO_DIR/bin/vigo" /usr/local/sbin/vigo
        echo "Installed agent binary from local build"
        AGENT_INSTALLED=true
    elif docker exec vigo test -f "/dist/agent/$HOST_OS/$AGENT_ARCH/vigo" 2>/dev/null; then
        docker cp "vigo:/dist/agent/$HOST_OS/$AGENT_ARCH/vigo" /usr/local/sbin/vigo
        echo "Installed agent binary: $HOST_OS/$AGENT_ARCH"
        AGENT_INSTALLED=true
    else
        echo "WARNING: No agent binary for $HOST_OS/$AGENT_ARCH"
    fi

    if $AGENT_INSTALLED; then
        systemctl start vigo-envoy 2>/dev/null || true
    fi
fi

echo ""
docker compose ps

# ---------------------------------------------------------------------------
# Freshness check
# ---------------------------------------------------------------------------

echo ""
echo "Checking freshness..."

STALE=0

# Compare host CLI against container CLI.
if [ -f /usr/local/bin/vigogo ]; then
    HOST_MD5=$(md5sum /usr/local/bin/vigogo 2>/dev/null | awk '{print $1}')
    CTR_MD5=$(docker exec vigo md5sum /usr/local/bin/vigogo 2>/dev/null | awk '{print $1}')
    if [ "$HOST_MD5" != "$CTR_MD5" ]; then
        echo "  STALE  /usr/local/bin/vigogo (host differs from container)"
        STALE=1
    else
        echo "  OK     /usr/local/bin/vigogo"
    fi
fi

# Compare container binaries against repo source (build modes only).
if ($BUILD_SERVER || $BUILD_ALL) && [ -d "$REPO_DIR" ]; then
    NEWEST_GO=$(find "$REPO_DIR/server" "$REPO_DIR/ui" "$REPO_DIR/cli" -name '*.go' -printf '%T@\n' 2>/dev/null | sort -rn | head -1)
    NEWEST_GO=${NEWEST_GO%.*}
    CTR_VIGOSRV=$(docker exec vigo stat -c '%Y' /usr/local/sbin/vigosrv 2>/dev/null || echo 0)
    if [ -n "$NEWEST_GO" ] && [ "$NEWEST_GO" -gt "$CTR_VIGOSRV" ]; then
        echo "  STALE  vigosrv in container (source is newer — restart container to activate)"
        STALE=1
    else
        echo "  OK     vigosrv"
    fi
fi

# Agent freshness.
if ($BUILD_AGENT || $BUILD_ALL) && [ -d "$REPO_DIR" ]; then
    NEWEST_RS=$(find "$REPO_DIR/agent-rs/src" -name '*.rs' -printf '%T@\n' 2>/dev/null | sort -rn | head -1)
    NEWEST_RS=${NEWEST_RS%.*}
    AGENT_PLATFORMS=$(docker exec vigo find /dist/agent -name 'vigo' -o -name 'vigo.exe' 2>/dev/null || true)
    if [ -n "$AGENT_PLATFORMS" ]; then
        for agent_path in $AGENT_PLATFORMS; do
            CTR_AGENT=$(docker exec vigo stat -c '%Y' "$agent_path" 2>/dev/null || echo 0)
            platform=$(echo "$agent_path" | sed 's|/dist/agent/||; s|/vigo.*||')
            if [ -n "$NEWEST_RS" ] && [ "$NEWEST_RS" -gt "$CTR_AGENT" ]; then
                echo "  STALE  agent $platform (source is newer than binary)"
                STALE=1
            else
                echo "  OK     agent $platform"
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
echo "Upgrade complete."
