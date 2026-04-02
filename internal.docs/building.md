# Development Guide

Build, test, and release instructions for Vigo contributors. This file is not shipped to customers.

## Prerequisites

- Go 1.25+
- Docker
- Rust toolchain (for agent development only — cross-compilation runs in Docker)

## Build Commands

See `CLAUDE.md` for the full command reference (`make build`, `make test`, `make lint`, etc.).

## Upgrade Script

The upgrade script rebuilds the server image and restarts containers. It operates on the deployed instance at `/srv/vigo/`.

### Basic Usage

```bash
# Pull latest image from registry and restart
sudo scripts/upgrade.sh

# Build from local source and restart
sudo scripts/upgrade.sh --build
```

### Cross-Compiling Agent Binaries

The `--cross` flag cross-compiles agent binaries inside Docker containers — no local Rust toolchain required. Binaries are placed in `dist/agent/` and picked up by the Docker image build.

```bash
# Default: linux amd64 + arm64
sudo scripts/upgrade.sh --build --cross

# Specific target
sudo scripts/upgrade.sh --build --cross arm64

# Multiple targets
sudo scripts/upgrade.sh --build --cross arm64 freebsd

# All supported platforms
sudo scripts/upgrade.sh --build --cross all

# Build all platforms and push to GHCR
sudo scripts/upgrade.sh --build --cross all --push
```

#### Available Targets

| Target | Platform | Method |
|--------|----------|--------|
| `amd64` | Linux x86_64 | musl static binary via `rust:bookworm` container |
| `arm64` | Linux aarch64 | musl static binary via `rust:bookworm` + `gcc-aarch64-linux-gnu` |
| `freebsd` | FreeBSD x86_64 | `ghcr.io/cross-rs/x86_64-unknown-freebsd` container |
| `windows` | Windows x86_64 | `ghcr.io/cross-rs/x86_64-pc-windows-msvc` container |
| `darwin` | macOS x86_64 + arm64 | requires osxcross in PATH (or build natively on macOS) |
| `all` | All of the above | |

Linux targets (amd64/arm64) are built together in a single container. FreeBSD and Windows each use a dedicated cross-rs container. Darwin requires osxcross or a native macOS build.

### What the Upgrade Script Does

1. Stops running containers
2. Cross-compiles agent binaries (if `--cross`)
3. Builds the Docker image from source (if `--build`) or pulls from registry
4. Seeds docs and examples into `/srv/vigo/` (non-destructive to user files)
5. Starts containers
6. Copies `vigocli` to the host at `/usr/local/bin/`
7. Copies the host-architecture agent binary to `/usr/local/sbin/vigo`
8. Runs a freshness check comparing source timestamps against container binaries

### Freshness Check

The `--build` mode compares source file timestamps against container binary timestamps and flags stale binaries. Common causes:

- **Stale agent**: Rust source changed but `--cross` wasn't used — the Docker build only compiles `linux/amd64` natively; other architectures come from `dist/agent/`
- **Stale vigosrv/vigocli**: Go source changed after the Docker build completed (e.g., edits during a long build)
- **Stale docker-compose.yml**: The repo version differs from `/srv/vigo/docker-compose.yml` (user may have customized it)

### Preserved vs Replaced on Upgrade

**Never overwritten** (only created if missing):
- `server.yaml`, `.env`, `docker-compose.yml`
- `tls/`, `secrets/`, `license/`, `db/`
- `stack/stockpile/`, `stack/.live/`, `config-history/`

**Always refreshed**:
- `docs/` (server-provided documentation)
- Container image and binaries
- Host CLI (`/usr/local/bin/vigocli`)

## Makefile Targets

```
make build          # Build all binaries (server + CLI + agent)
make build-agent    # Build Rust agent only (release mode)
make test           # Run all tests (Go + Rust)
make lint           # Run golangci-lint + buf lint + cargo clippy
make dist           # Cross-compile server/CLI static binaries + agent
make dist-agent     # Cross-compile Rust agent (musl, amd64+arm64) — requires local Rust
make proto          # Regenerate protobuf/gRPC code
make sqlc           # Regenerate type-safe Go from SQL queries
```

## Docker Image

The `Dockerfile.server` is a multi-stage build:

1. **rust-builder**: Compiles the agent for `x86_64-unknown-linux-musl`
2. **go-builder**: Compiles `vigosrv`, `vigocli`, and `vigo-license-manager`
3. **runtime**: Alpine with the compiled binaries, migrations, seed configs, and docs

The Dockerfile copies pre-built cross-compiled agent binaries from `dist/agent/` first, then overlays the freshly built `linux/amd64` binary on top. This means `dist/agent/linux/amd64/vigo` is always overridden by the fresh build, but other architectures (arm64, freebsd, etc.) use whatever is in `dist/agent/`.

To ensure all architectures are fresh, run `--cross` before building the image.
