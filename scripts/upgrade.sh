#!/usr/bin/env bash
set -euo pipefail

# Upgrade the Hobnob server to the latest container image.
# Preserves: server.yaml, .env, db/, secrets/, tls/, license/, hnb/, config-history/
# Replaces:  container image, docs/, examples/, docker-compose.yml (if unchanged)
#
# Usage:
#   sudo scripts/upgrade.sh                        # pull latest and restart
#   sudo scripts/upgrade.sh --build                # build from source and restart (dev)
#   sudo scripts/upgrade.sh --build --cross        # build + cross-compile agent (linux amd64+arm64)
#   sudo scripts/upgrade.sh --build --cross arm64  # build + cross-compile specific platform(s)
#   sudo scripts/upgrade.sh --build --cross all --push  # build all platforms and push to GHCR
#
# Cross-compile targets:
#   amd64      Linux x86_64 (musl static)
#   arm64      Linux aarch64 (musl static)
#   darwin     macOS x86_64 + arm64 (requires osxcross in PATH)
#   freebsd    FreeBSD x86_64 (via cross)
#   windows    Windows x86_64 (via cross)
#   all        All of the above

DATA_DIR="/srv/hobnob"
IMAGE="ghcr.io/daniel-alexander4/hobnob:latest"

if [ "$(id -u)" -ne 0 ]; then
    echo "Run as root: sudo $0" >&2
    exit 1
fi

# Resolve repo path before cd (needed for --build).
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"

cd "$DATA_DIR"

# Parse flags.
BUILD=false
PUSH=false
CROSS_TARGETS=()
parsing_cross=false
for arg in "$@"; do
    case "$arg" in
        --build) BUILD=true; parsing_cross=false ;;
        --push) PUSH=true; parsing_cross=false ;;
        --cross) parsing_cross=true ;;
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
for t in "${CROSS_TARGETS[@]}"; do
    if [ "$t" = "all" ]; then
        expanded=(amd64 arm64 darwin freebsd windows)
        break
    fi
    expanded+=("$t")
done
CROSS_TARGETS=("${expanded[@]+"${expanded[@]}"}")

echo "Stopping containers..."
docker compose down --remove-orphans 2>/dev/null || true

if $BUILD; then
    # Cross-compile agent binaries inside Docker (no local Rust toolchain needed).
    if [ ${#CROSS_TARGETS[@]} -gt 0 ]; then
        echo "Cross-compiling agent for: ${CROSS_TARGETS[*]}"

        # Determine which targets to pass into the container.
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
cp target/x86_64-unknown-linux-musl/release/hobnob /build/dist/agent/linux/amd64/hobnob"
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
cp target/aarch64-unknown-linux-musl/release/hobnob /build/dist/agent/linux/arm64/hobnob"
                    ;;
                freebsd)
                    echo "  freebsd: requires 'cross' tool — using cargo-cross container"
                    (cd "$REPO_DIR" && \
                        docker run --rm -v "$REPO_DIR/agent-rs:/build/agent-rs" -v "$REPO_DIR/proto:/build/proto" -v "$REPO_DIR/dist:/build/dist" -w /build/agent-rs \
                        ghcr.io/cross-rs/x86_64-unknown-freebsd:main sh -c '
                            cargo build --release --target x86_64-unknown-freebsd 2>&1
                            mkdir -p /build/dist/agent/freebsd/amd64
                            cp target/x86_64-unknown-freebsd/release/hobnob /build/dist/agent/freebsd/amd64/hobnob
                        ') || echo "  WARN: freebsd cross-compile failed (install cross: cargo install cross)"
                    ;;
                windows)
                    echo "  windows: requires 'cross' tool — using cargo-cross container"
                    (cd "$REPO_DIR" && \
                        docker run --rm -v "$REPO_DIR/agent-rs:/build/agent-rs" -v "$REPO_DIR/proto:/build/proto" -v "$REPO_DIR/dist:/build/dist" -w /build/agent-rs \
                        ghcr.io/cross-rs/x86_64-pc-windows-msvc:main sh -c '
                            cargo build --release --target x86_64-pc-windows-msvc 2>&1
                            mkdir -p /build/dist/agent/windows/amd64
                            cp target/x86_64-pc-windows-msvc/release/hobnob.exe /build/dist/agent/windows/amd64/hobnob.exe
                        ') || echo "  WARN: windows cross-compile failed (install cross: cargo install cross)"
                    ;;
                darwin)
                    echo "  darwin: requires osxcross — skipping unless available in PATH"
                    if command -v x86_64-apple-darwin-gcc > /dev/null 2>&1 || command -v o64-clang > /dev/null 2>&1; then
                        (cd "$REPO_DIR" && make dist-agent-macos) || echo "  WARN: darwin cross-compile failed"
                    else
                        echo "  SKIP: osxcross not found — build natively on macOS or install osxcross"
                    fi
                    ;;
                *)
                    echo "  Unknown target: $t (valid: amd64, arm64, freebsd, windows, darwin, all)"
                    ;;
            esac
        done

        # Run the Linux builds (amd64/arm64) together in a single container.
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
                    echo 'Linux builds complete.'
                "
        fi

        # Show results.
        echo ""
        echo "Agent binaries:"
        find "$REPO_DIR/dist/agent" -name 'hobnob' -o -name 'hobnob.exe' 2>/dev/null | while read -r f; do
            platform="${f#$REPO_DIR/dist/agent/}"
            platform="${platform%/hobnob*}"
            size=$(ls -lh "$f" | awk '{print $5}')
            echo "  $platform  $size"
        done
        echo ""
    fi

    # Touch source files to bust Docker layer cache for proto and Rust source.
    # Without this, COPY layers may be cached from a prior build even when
    # file contents have changed (Docker caches by content hash of the COPY
    # context, but the Rust dependency-caching layer uses a dummy main.rs
    # that doesn't change, so proto changes can be missed).
    touch "$REPO_DIR/proto/hobnob.proto" "$REPO_DIR/agent-rs/src/main.rs" 2>/dev/null || true

    echo "Building image from source..."
    DOCKER_BUILDKIT=1 docker build \
        --build-arg CACHEBUST="$(date +%s)" \
        --no-cache-filter go-builder \
        -f "$REPO_DIR/Dockerfile.server" -t "$IMAGE" "$REPO_DIR"

    if $PUSH; then
        echo "Pushing image to registry..."
        docker push "$IMAGE"
        echo "Pushed $IMAGE"
    fi
else
    echo "Pulling latest image..."
    docker compose pull hobnob
fi

# Re-seed docs and examples from the new image (non-destructive to user files).
echo "Updating docs and examples..."
docker run --rm -v "$DATA_DIR:$DATA_DIR" "$IMAGE" --seed-only 2>/dev/null || true

echo "Starting containers..."
docker compose up -d

echo ""
echo "Waiting for server..."
timeout 60 docker compose logs -f hobnob 2>&1 | grep -m1 "server ready" || true

# Update host binaries from the new container.
echo "Updating host binaries..."
docker cp hobnob:/usr/local/bin/hobnobcli /usr/local/bin/hobnobcli
ln -sf hobnobcli /usr/local/bin/hnbvi

# Copy the agent binary matching the host architecture.
HOST_ARCH=$(uname -m)
case "$HOST_ARCH" in
    x86_64)  AGENT_ARCH="amd64" ;;
    aarch64) AGENT_ARCH="arm64" ;;
    *)       AGENT_ARCH="$HOST_ARCH" ;;
esac
HOST_OS=$(uname -s | tr '[:upper:]' '[:lower:]')

if docker exec hobnob test -f "/dist/agent/$HOST_OS/$AGENT_ARCH/hobnob" 2>/dev/null; then
    docker cp "hobnob:/dist/agent/$HOST_OS/$AGENT_ARCH/hobnob" /usr/local/sbin/hobnob
    echo "Installed agent binary: $HOST_OS/$AGENT_ARCH"
    systemctl restart hobnob-thrall 2>/dev/null || true
else
    echo "WARNING: No agent binary for $HOST_OS/$AGENT_ARCH in container"
fi

echo ""
docker compose ps

# ── Freshness check ────────────────────────────────────────────────────
# Compare binaries and key files between the container and the host/repo
# to flag anything that didn't get updated.
echo ""
echo "Checking freshness..."

STALE=0

# Compare host CLI against container CLI.
if [ -f /usr/local/bin/hobnobcli ]; then
    HOST_MD5=$(md5sum /usr/local/bin/hobnobcli 2>/dev/null | awk '{print $1}')
    CTR_MD5=$(docker exec hobnob md5sum /usr/local/bin/hobnobcli 2>/dev/null | awk '{print $1}')
    if [ "$HOST_MD5" != "$CTR_MD5" ]; then
        echo "  STALE  /usr/local/bin/hobnobcli (host differs from container)"
        STALE=1
    else
        echo "  OK     /usr/local/bin/hobnobcli"
    fi
fi

# Compare container binaries against repo source (--build mode only).
if $BUILD && [ -d "$REPO_DIR" ]; then
    # Check server binary build time vs latest source modification.
    NEWEST_GO=$(find "$REPO_DIR/server" "$REPO_DIR/ui" -name '*.go' -printf '%T@\n' 2>/dev/null | sort -rn | head -1)
    NEWEST_GO=${NEWEST_GO%.*}
    CTR_HOBD=$(docker exec hobnob stat -c '%Y' /usr/local/sbin/hobd 2>/dev/null || echo 0)
    if [ -n "$NEWEST_GO" ] && [ "$NEWEST_GO" -gt "$CTR_HOBD" ]; then
        echo "  STALE  hobd in container (source is newer than binary)"
        STALE=1
    else
        echo "  OK     hobd"
    fi

    # Check CLI binary.
    NEWEST_CLI=$(find "$REPO_DIR/cli" -name '*.go' -printf '%T@\n' 2>/dev/null | sort -rn | head -1)
    NEWEST_CLI=${NEWEST_CLI%.*}
    CTR_CLI=$(docker exec hobnob stat -c '%Y' /usr/local/bin/hobnobcli 2>/dev/null || echo 0)
    if [ -n "$NEWEST_CLI" ] && [ "$NEWEST_CLI" -gt "$CTR_CLI" ]; then
        echo "  STALE  hobnobcli in container (source is newer than binary)"
        STALE=1
    else
        echo "  OK     hobnobcli"
    fi

    # Check agent binaries in container.
    if [ ${#CROSS_TARGETS[@]} -gt 0 ]; then
        # Cross-compile just ran — binaries are fresh by definition.
        echo "  OK     agent binaries (just cross-compiled)"
    else
        # No cross-compile: compare source mtime against binary mtime.
        NEWEST_RS=$(find "$REPO_DIR/agent-rs/src" -name '*.rs' -printf '%T@\n' 2>/dev/null | sort -rn | head -1)
        NEWEST_RS=${NEWEST_RS%.*}
        AGENT_PLATFORMS=$(docker exec hobnob find /dist/agent -name 'hobnob' -o -name 'hobnob.exe' 2>/dev/null)
        if [ -n "$AGENT_PLATFORMS" ]; then
            for agent_path in $AGENT_PLATFORMS; do
                CTR_AGENT=$(docker exec hobnob stat -c '%Y' "$agent_path" 2>/dev/null || echo 0)
                platform=$(echo "$agent_path" | sed 's|/dist/agent/||; s|/hobnob.*||')
                if [ -n "$NEWEST_RS" ] && [ "$NEWEST_RS" -gt "$CTR_AGENT" ]; then
                    echo "  STALE  agent $platform (source is newer than binary)"
                    STALE=1
                else
                    echo "  OK     agent $platform"
                fi
            done
        else
            echo "  WARN   no agent binaries found in container"
            STALE=1
        fi
    fi

    # Check entrypoint.sh.
    REPO_EP_MD5=$(md5sum "$REPO_DIR/entrypoint.sh" 2>/dev/null | awk '{print $1}')
    CTR_EP_MD5=$(docker exec hobnob md5sum /entrypoint.sh 2>/dev/null | awk '{print $1}')
    if [ "$REPO_EP_MD5" != "$CTR_EP_MD5" ]; then
        echo "  STALE  entrypoint.sh (repo differs from container)"
        STALE=1
    else
        echo "  OK     entrypoint.sh"
    fi

    # Check Dockerfile itself (was the image built from the current Dockerfile?).
    DOCKERFILE_TIME=$(stat -c '%Y' "$REPO_DIR/Dockerfile.server" 2>/dev/null || echo 0)
    IMAGE_TIME=$(docker inspect --format='{{.Created}}' "$IMAGE" 2>/dev/null | xargs -I{} date -d {} +%s 2>/dev/null || echo 0)
    if [ "$DOCKERFILE_TIME" -gt "$IMAGE_TIME" ]; then
        echo "  STALE  Dockerfile.server (modified after image was built)"
        STALE=1
    else
        echo "  OK     Dockerfile.server"
    fi
fi

# Check docker-compose.yml.
if [ -f "$DATA_DIR/docker-compose.yml" ] && [ -f "$REPO_DIR/docker-compose.yml" ]; then
    DATA_DC_MD5=$(md5sum "$DATA_DIR/docker-compose.yml" 2>/dev/null | awk '{print $1}')
    REPO_DC_MD5=$(md5sum "$REPO_DIR/docker-compose.yml" 2>/dev/null | awk '{print $1}')
    if [ "$DATA_DC_MD5" != "$REPO_DC_MD5" ]; then
        echo "  STALE  docker-compose.yml ($DATA_DIR differs from repo)"
        STALE=1
    else
        echo "  OK     docker-compose.yml"
    fi
fi

echo ""
if [ "$STALE" -eq 0 ]; then
    echo "All files are fresh."
else
    echo "WARNING: Stale files detected. Re-run with --build to update."
fi

echo ""
echo "Upgrade complete."
