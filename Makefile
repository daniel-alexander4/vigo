.PHONY: build build-agent build-connwaer \
       test test-agent test-connwaer \
       lint lint-agent lint-connwaer \
       dist dist-agent dist-connwaer \
       dist-agent-windows dist-agent-macos dist-agent-freebsd dist-agent-illumos \
       dist-sign dist-sign-agent \
       proto certs migrate sqlc schemas notices up down logs clean

VERSION ?= $(shell cat VERSION 2>/dev/null || echo dev)

# --- Primary build: server + CLI (Go), agent (Rust) ---

# GOFLAGS for hardened builds:
#   -trimpath       removes source file paths from binary
#   -s -w           strips symbol table and DWARF debug info
#   -buildid=       removes build ID
#   -buildmode=pie  Position Independent Executable (full ASLR)
# CGO hardening: stack protectors, FORTIFY_SOURCE, full RELRO, no-execstack
GO_VERSION_FLAG := -X main.version=$(VERSION)
GO_LDFLAGS := -ldflags="-s -w -buildid= $(GO_VERSION_FLAG)"
GO_HARDENED := -buildmode=pie -trimpath $(GO_LDFLAGS)
export CGO_CFLAGS := -D_FORTIFY_SOURCE=2 -fstack-protector-strong -O2
export CGO_LDFLAGS := -Wl,-z,relro,-z,now,-z,noexecstack

build: build-agent schemas
	go build -ldflags="-X main.version=$(VERSION) -X git.alexander4.io/vigo/server/hardening.serverSigningPubKey=$(VIGO_AGENT_SIGNING_PUBKEY)" -o bin/vigosrv ./server
	go build -ldflags="-X main.version=$(VERSION)" -o bin/vigocli ./cli

# ADR-031 detached-ED25519 signing key. Consumed by two paths:
#   - `build-agent` / `dist-agent` derive VIGO_AGENT_SIGNING_PUBKEY from
#     this and bake it into the agent so it can verify auto-update binaries.
#   - `dist-sign` / `dist-sign-agent` sign the distributables with the
#     private key after they're built.
# Declared here (not next to dist-sign) because the `:=` below evaluates
# immediately at parse time; a `?=` further down would resolve too late.
# Override with `SIGN_KEY=/path/to/key make ...`.
SIGN_KEY ?= signing.key

# Derive the agent's baked-in update-verification pubkey from the SIGN_KEY
# private key (ADR-031). When SIGN_KEY is absent — the common dev case —
# this resolves to empty and build.rs falls back to the all-zero sentinel
# pubkey; the resulting agent boots fine but refuses to self-update at
# runtime with an operator-actionable error pointing back here.
VIGO_AGENT_SIGNING_PUBKEY := $(shell test -f $(SIGN_KEY) && go run ./server/hardening/cmd/sign -key $(SIGN_KEY) -pubkey-only 2>/dev/null)

# Anti-tamper: bake the same pubkey into vigosrv so the startup
# VerifyBinarySignature gate verifies the binary's detached .sig against THIS
# key rather than the (attacker-controllable) pubkey carried in the .sig file.
# Defined after VIGO_AGENT_SIGNING_PUBKEY (which it references) and applied to
# the hardened server dist build below; empty in the keyless dev case →
# corruption-detection mode (see server/hardening/hardening.go). Server only —
# vigocli has no such gate, so the symbol stays off the CLI build.
GO_SERVER_HARDENED := -buildmode=pie -trimpath -ldflags="-s -w -buildid= $(GO_VERSION_FLAG) -X git.alexander4.io/vigo/server/hardening.serverSigningPubKey=$(VIGO_AGENT_SIGNING_PUBKEY)"

build-agent:
	cd agent && VIGO_AGENT_SIGNING_PUBKEY="$(VIGO_AGENT_SIGNING_PUBKEY)" cargo build --release
	cp agent/.build/release/vigo bin/vigo

test:
	go test $(shell go list ./... | grep -v test/integration) -v -race -count=1
	cd agent && cargo test

test-agent:
	cd agent && cargo test

test-integration: build-agent
	go test ./test/integration/... -v -count=1 -timeout 120s

# End-to-end swarm validation against a real fleet (defaults to
# plex,girlslaptop,annlap; override via VIGO_FLEET). Destructive: wipes
# per-user swarm state and root-owned /var/lib/vigo/swarm/{gitback,
# lockbox} on every envoy, then runs the full 5-step substrate
# sequence (payload → longdrawer → puddle → lockbox → gitback) from
# a clean slate. Mirrors tshoot/log-substrate-2026-05-09.md.
#
# For a fleet-free quick smoke, run `make test-agent` (picks up
# tests/gitback_helper.rs and tests/lockbox_roundtrip.rs).
test-substrate:
	test/swarm-substrate.sh --wipe

lint:
	bash scripts/check-legacy-naming.sh
	golangci-lint run ./...
	buf lint
	cd agent && cargo clippy -- -D warnings
	$(MAKE) lint-examples

# Run modlint over every shipped .vgo.example. Catches drift between
# the executor params, server/validate/schemas_gen.go, and the
# operator-facing example modules. Regenerate the schema first
# (`scripts/gen-executor-schemas.sh > server/validate/schemas_gen.go`)
# if you've touched executor params.
lint-examples:
	go test ./server/modlint/... -run TestExampleModulesLintClean -count=1

lint-agent:
	cd agent && cargo clippy -- -D warnings

# --- Distribution: cross-compiled static binaries ---

# Distribution builds apply PIE, trimpath, strip, and CGO hardening
# (see GO_HARDENED above).
#
# To sign binaries after build: make dist-sign SIGN_KEY=path/to/ed25519.key
dist: dist-agent schemas notices
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build $(GO_SERVER_HARDENED) -o dist/server/linux/amd64/vigosrv ./server
	GOOS=linux GOARCH=amd64 go build $(GO_HARDENED) -o dist/cli/linux/amd64/vigocli ./cli

# Regenerate the third-party attribution files (THIRD-PARTY-NOTICES.{go,rust}.txt)
# that satisfy the MIT/BSD/Apache-2.0/ISC notice obligations of our deps.
# Re-run when go.mod or agent/Cargo.lock change. Folded into `dist` so a
# release never ships stale attribution.
notices:
	bash scripts/gen-notices.sh

# Sign distribution binaries with ED25519 key for tamper detection.
# Usage: make dist-sign SIGN_KEY=/path/to/signing.key (default: ./signing.key)
dist-sign: dist-sign-agent
	@test -f $(SIGN_KEY) || (echo "error: SIGN_KEY=$(SIGN_KEY) not found" && exit 1)
	@for bin in dist/server/linux/amd64/vigosrv dist/cli/linux/amd64/vigocli; do \
		if [ -f "$$bin" ]; then \
			go run ./server/hardening/cmd/sign -key $(SIGN_KEY) -binary "$$bin" -output "$$bin.sig"; \
			echo "signed $$bin"; \
		fi; \
	done

# Sign agent distribution binaries with ED25519 key.
# Usage: make dist-sign-agent SIGN_KEY=/path/to/signing.key
dist-sign-agent:
	@test -f $(SIGN_KEY) || (echo "error: SIGN_KEY=$(SIGN_KEY) not found" && exit 1)
	@find dist/agent -type f \( -name 'vigo' -o -name 'vigo.exe' \) 2>/dev/null | while read bin; do \
		go run ./server/hardening/cmd/sign -key $(SIGN_KEY) -binary "$$bin" -output "$$bin.sig"; \
		echo "signed $$bin"; \
	done

# Cross-compile Rust agent to static musl binaries.
# Prerequisites:
#   rustup target add x86_64-unknown-linux-musl aarch64-unknown-linux-musl
#   apt install musl-tools gcc-aarch64-linux-gnu
dist-agent:
	mkdir -p dist/agent/linux/amd64 dist/agent/linux/arm64
	cd agent && VIGO_AGENT_SIGNING_PUBKEY="$(VIGO_AGENT_SIGNING_PUBKEY)" CC=musl-gcc cargo build --release --target x86_64-unknown-linux-musl
	cp agent/.build/x86_64-unknown-linux-musl/release/vigo dist/agent/linux/amd64/vigo
	cd agent && VIGO_AGENT_SIGNING_PUBKEY="$(VIGO_AGENT_SIGNING_PUBKEY)" CC=aarch64-linux-gnu-gcc cargo build --release --target aarch64-unknown-linux-musl
	cp agent/.build/aarch64-unknown-linux-musl/release/vigo dist/agent/linux/arm64/vigo
	@# Write each binary's .version sidecar from the binary's OWN embedded
	@# VIGO-BUILD-VERSION marker (agent/src/main.rs) — never from Cargo.toml —
	@# so a sidecar can't drift from the binary it labels. The server reads
	@# these at startup to learn each cross-arch binary's version (it can't
	@# run a foreign-arch binary); a sidecar that disagrees with its binary
	@# makes the server advertise the wrong version and the agent's update
	@# guard aborts in a loop. See tshoot/log-archive-*update-loop*.
	@for a in amd64 arm64 ; do \
		v=$$(grep -aoE 'VIGO-BUILD-VERSION=[0-9.]+' dist/agent/linux/$$a/vigo | head -1 | cut -d= -f2) ; \
		[ -n "$$v" ] || { echo "ERROR: no VIGO-BUILD-VERSION marker in dist/agent/linux/$$a/vigo" >&2 ; exit 1 ; } ; \
		printf '%s\n' "$$v" > dist/agent/linux/$$a/vigo.version ; \
		echo "wrote dist/agent/linux/$$a/vigo.version=$$v" ; \
	done

# Cross-compile Rust agent for Windows.
# Prerequisites:
#   rustup target add x86_64-pc-windows-msvc
#   Install cross: cargo install cross
dist-agent-windows:
	mkdir -p dist/agent/windows/amd64
	cd agent && VIGO_AGENT_SIGNING_PUBKEY="$(VIGO_AGENT_SIGNING_PUBKEY)" cross build --release --target x86_64-pc-windows-msvc
	cp agent/.build/x86_64-pc-windows-msvc/release/vigo.exe dist/agent/windows/amd64/vigo.exe
	@# Sidecar from the binary's own VIGO-BUILD-VERSION marker, not Cargo.toml — see dist-agent.
	@v=$$(grep -aoE 'VIGO-BUILD-VERSION=[0-9.]+' dist/agent/windows/amd64/vigo.exe | head -1 | cut -d= -f2) ; \
		[ -n "$$v" ] || { echo "ERROR: no VIGO-BUILD-VERSION marker in dist/agent/windows/amd64/vigo.exe" >&2 ; exit 1 ; } ; \
		printf '%s\n' "$$v" > dist/agent/windows/amd64/vigo.exe.version ; \
		echo "wrote dist/agent/windows/amd64/vigo.exe.version=$$v"

# Cross-compile Rust agent for macOS.
# Prerequisites (from Linux):
#   Install osxcross or build natively on macOS.
#   rustup target add x86_64-apple-darwin aarch64-apple-darwin
dist-agent-macos:
	mkdir -p dist/agent/darwin/amd64 dist/agent/darwin/arm64
	cd agent && VIGO_AGENT_SIGNING_PUBKEY="$(VIGO_AGENT_SIGNING_PUBKEY)" cargo build --release --target x86_64-apple-darwin
	cp agent/.build/x86_64-apple-darwin/release/vigo dist/agent/darwin/amd64/vigo
	cd agent && VIGO_AGENT_SIGNING_PUBKEY="$(VIGO_AGENT_SIGNING_PUBKEY)" cargo build --release --target aarch64-apple-darwin
	cp agent/.build/aarch64-apple-darwin/release/vigo dist/agent/darwin/arm64/vigo
	@# Sidecars from each binary's VIGO-BUILD-VERSION marker — see dist-agent.
	@for a in amd64 arm64 ; do \
		v=$$(grep -aoE 'VIGO-BUILD-VERSION=[0-9.]+' dist/agent/darwin/$$a/vigo | head -1 | cut -d= -f2) ; \
		[ -n "$$v" ] || { echo "ERROR: no VIGO-BUILD-VERSION marker in dist/agent/darwin/$$a/vigo" >&2 ; exit 1 ; } ; \
		printf '%s\n' "$$v" > dist/agent/darwin/$$a/vigo.version ; \
		echo "wrote dist/agent/darwin/$$a/vigo.version=$$v" ; \
	done

# Cross-compile Rust agent for FreeBSD (Rust Tier 2).
# Prerequisites:
#   rustup target add x86_64-unknown-freebsd
#   Install cross: cargo install cross
dist-agent-freebsd:
	mkdir -p dist/agent/freebsd/amd64
	cd agent && cross build --release --target x86_64-unknown-freebsd
	cp agent/.build/x86_64-unknown-freebsd/release/vigo dist/agent/freebsd/amd64/vigo
	@v=$$(grep -aoE 'VIGO-BUILD-VERSION=[0-9.]+' dist/agent/freebsd/amd64/vigo | head -1 | cut -d= -f2) ; \
		[ -n "$$v" ] || { echo "ERROR: no VIGO-BUILD-VERSION marker in dist/agent/freebsd/amd64/vigo" >&2 ; exit 1 ; } ; \
		printf '%s\n' "$$v" > dist/agent/freebsd/amd64/vigo.version ; \
		echo "wrote dist/agent/freebsd/amd64/vigo.version=$$v"

# Build Rust agent for illumos (native build only).
# Prerequisites:
#   rustup target add x86_64-unknown-illumos
#   Must be built on an illumos host or via a cross-compilation toolchain.
dist-agent-illumos:
	mkdir -p dist/agent/illumos/amd64
	cd agent && cargo build --release --target x86_64-unknown-illumos
	cp agent/.build/x86_64-unknown-illumos/release/vigo dist/agent/illumos/amd64/vigo
	@v=$$(grep -aoE 'VIGO-BUILD-VERSION=[0-9.]+' dist/agent/illumos/amd64/vigo | head -1 | cut -d= -f2) ; \
		[ -n "$$v" ] || { echo "ERROR: no VIGO-BUILD-VERSION marker in dist/agent/illumos/amd64/vigo" >&2 ; exit 1 ; } ; \
		printf '%s\n' "$$v" > dist/agent/illumos/amd64/vigo.version ; \
		echo "wrote dist/agent/illumos/amd64/vigo.version=$$v"

# --- Connwaer (compliance verification agent) ---

build-connwaer:
	cd connwaer && cargo build --release
	cp connwaer/target/release/connwaer bin/connwaer

test-connwaer:
	cd connwaer && cargo test

lint-connwaer:
	cd connwaer && cargo clippy -- -D warnings

dist-connwaer:
	mkdir -p dist/connwaer/linux/amd64 dist/connwaer/linux/arm64
	cd connwaer && CC=musl-gcc cargo build --release --target x86_64-unknown-linux-musl
	cp connwaer/target/x86_64-unknown-linux-musl/release/connwaer dist/connwaer/linux/amd64/connwaer
	cd connwaer && CC=aarch64-linux-gnu-gcc cargo build --release --target aarch64-unknown-linux-musl
	cp connwaer/target/aarch64-unknown-linux-musl/release/connwaer dist/connwaer/linux/arm64/connwaer

# --- Codegen ---

proto:
	buf generate

migrate:
	migrate -path migrations -database "$$DATABASE_URL" up

sqlc:
	sqlc generate

schemas:
	scripts/gen-executor-schemas.sh > server/validate/schemas_gen.go

certs:
	mkdir -p certs
	@echo "Generating CA keypair..."
	openssl genrsa -out certs/ca.key 4096
	openssl req -new -x509 -key certs/ca.key -out certs/ca.crt -days 3650 \
		-subj "/CN=Vigo CA"
	@echo "Generating server keypair..."
	openssl genrsa -out certs/server.key 4096
	openssl req -new -key certs/server.key -out certs/server.csr \
		-subj "/CN=vigo" \
		-addext "subjectAltName=DNS:localhost,DNS:vigo,DNS:server,IP:127.0.0.1"
	openssl x509 -req -in certs/server.csr -CA certs/ca.crt -CAkey certs/ca.key \
		-CAcreateserial -out certs/server.crt -days 365 \
		-copy_extensions copyall
	rm -f certs/server.csr certs/ca.srl
	@echo "Certificates generated in certs/"

# --- Docker Compose (reads VIGO_SERVICES from server .env) ---

ENVFILE ?= /srv/vigo/.env

IMAGE ?= us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest

push-image:
	docker build -f Dockerfile.server -t $(IMAGE) .
	docker push $(IMAGE)

up:
	docker compose --env-file $(ENVFILE) up -d --build

down:
	docker compose --env-file $(ENVFILE) down

logs:
	docker compose --env-file $(ENVFILE) logs -f

clean:
	rm -rf bin/ certs/ dist/
	cd agent && cargo clean 2>/dev/null || true
	cd connwaer && cargo clean 2>/dev/null || true
