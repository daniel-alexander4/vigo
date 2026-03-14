.PHONY: build build-agent test test-agent lint lint-agent dist dist-agent \
       dist-agent-windows dist-agent-macos dist-agent-freebsd dist-agent-illumos \
       proto certs migrate sqlc up down logs clean

VERSION ?= dev

# --- Primary build: server + CLI (Go), agent (Rust) ---

build: build-agent
	go build -o bin/hobd ./server
	go build -o bin/hobnobcli ./cli
	go build -o bin/hobnob-license-manager ./license-manager
	ln -sf hobnobcli bin/hnbvi

build-agent:
	cd agent-rs && cargo build --release
	cp agent-rs/target/release/hobnob bin/hobnob

test:
	go test $(shell go list ./... | grep -v test/integration) -v -race -count=1
	cd agent-rs && cargo test

test-agent:
	cd agent-rs && cargo test

test-integration: build-agent
	go test ./test/integration/... -v -count=1 -timeout 120s

lint:
	golangci-lint run ./...
	buf lint
	cd agent-rs && cargo clippy -- -D warnings

lint-agent:
	cd agent-rs && cargo clippy -- -D warnings

# --- Distribution: cross-compiled static binaries ---

dist: dist-agent
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -o dist/server/linux/amd64/hobd ./server
	GOOS=linux GOARCH=amd64 go build -o dist/cli/linux/amd64/hobnobcli ./cli

# Cross-compile Rust agent to static musl binaries.
# Prerequisites:
#   rustup target add x86_64-unknown-linux-musl aarch64-unknown-linux-musl
#   apt install musl-tools gcc-aarch64-linux-gnu
dist-agent:
	mkdir -p dist/agent/linux/amd64 dist/agent/linux/arm64
	cd agent-rs && CC=musl-gcc cargo build --release --target x86_64-unknown-linux-musl
	cp agent-rs/target/x86_64-unknown-linux-musl/release/hobnob dist/agent/linux/amd64/hobnob
	cd agent-rs && CC=aarch64-linux-gnu-gcc cargo build --release --target aarch64-unknown-linux-musl
	cp agent-rs/target/aarch64-unknown-linux-musl/release/hobnob dist/agent/linux/arm64/hobnob

# Cross-compile Rust agent for Windows.
# Prerequisites:
#   rustup target add x86_64-pc-windows-msvc
#   Install cross: cargo install cross
dist-agent-windows:
	mkdir -p dist/agent/windows/amd64
	cd agent-rs && cross build --release --target x86_64-pc-windows-msvc
	cp agent-rs/target/x86_64-pc-windows-msvc/release/hobnob.exe dist/agent/windows/amd64/hobnob.exe

# Cross-compile Rust agent for macOS.
# Prerequisites (from Linux):
#   Install osxcross or build natively on macOS.
#   rustup target add x86_64-apple-darwin aarch64-apple-darwin
dist-agent-macos:
	mkdir -p dist/agent/darwin/amd64 dist/agent/darwin/arm64
	cd agent-rs && cargo build --release --target x86_64-apple-darwin
	cp agent-rs/target/x86_64-apple-darwin/release/hobnob dist/agent/darwin/amd64/hobnob
	cd agent-rs && cargo build --release --target aarch64-apple-darwin
	cp agent-rs/target/aarch64-apple-darwin/release/hobnob dist/agent/darwin/arm64/hobnob

# Cross-compile Rust agent for FreeBSD (Rust Tier 2).
# Prerequisites:
#   rustup target add x86_64-unknown-freebsd
#   Install cross: cargo install cross
dist-agent-freebsd:
	mkdir -p dist/agent/freebsd/amd64
	cd agent-rs && cross build --release --target x86_64-unknown-freebsd
	cp agent-rs/target/x86_64-unknown-freebsd/release/hobnob dist/agent/freebsd/amd64/hobnob

# Build Rust agent for illumos (native build only).
# Prerequisites:
#   rustup target add x86_64-unknown-illumos
#   Must be built on an illumos host or via a cross-compilation toolchain.
dist-agent-illumos:
	mkdir -p dist/agent/illumos/amd64
	cd agent-rs && cargo build --release --target x86_64-unknown-illumos
	cp agent-rs/target/x86_64-unknown-illumos/release/hobnob dist/agent/illumos/amd64/hobnob

# --- Codegen ---

proto:
	buf generate

migrate:
	migrate -path migrations -database "$$DATABASE_URL" up

sqlc:
	sqlc generate

certs:
	mkdir -p certs
	@echo "Generating CA keypair..."
	openssl genrsa -out certs/ca.key 4096
	openssl req -new -x509 -key certs/ca.key -out certs/ca.crt -days 3650 \
		-subj "/CN=Hobnob CA"
	@echo "Generating server keypair..."
	openssl genrsa -out certs/server.key 4096
	openssl req -new -key certs/server.key -out certs/server.csr \
		-subj "/CN=hobnob" \
		-addext "subjectAltName=DNS:localhost,DNS:hobnob,DNS:server,IP:127.0.0.1"
	openssl x509 -req -in certs/server.csr -CA certs/ca.crt -CAkey certs/ca.key \
		-CAcreateserial -out certs/server.crt -days 365 \
		-copy_extensions copyall
	rm -f certs/server.csr certs/ca.srl
	@echo "Certificates generated in certs/"

# --- Docker Compose (reads COMPOSE_PROFILES from server .env) ---

ENVFILE ?= /etc/hobnob-server/.env

up:
	docker compose --env-file $(ENVFILE) up -d --build

down:
	docker compose --env-file $(ENVFILE) down

logs:
	docker compose --env-file $(ENVFILE) logs -f

clean:
	rm -rf bin/ certs/ dist/
	cd agent-rs && cargo clean 2>/dev/null || true
