.PHONY: build build-agent test test-agent lint lint-agent dist dist-agent \
       proto certs seed migrate sqlc up down logs clean

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
	go test ./... -v -race -count=1
	cd agent-rs && cargo test

test-agent:
	cd agent-rs && cargo test

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

# --- Codegen ---

proto:
	buf generate

seed:
	go run ./cmd/seed/main.go

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
