.PHONY: build proto certs test lint seed migrate sqlc dist clean

VERSION ?= dev

build:
	go build -o bin/hobd ./server
	go build -o bin/hob ./agent
	go build -o bin/hobcli ./cli

proto:
	buf generate

certs:
	mkdir -p certs
	@echo "Generating CA keypair..."
	openssl genrsa -out server-configs/certs/ca.key 4096
	openssl req -new -x509 -key server-configs/certs/ca.key -out server-configs/certs/ca.crt -days 3650 \
		-subj "/CN=Hobnob CA"
	@echo "Generating server keypair..."
	openssl genrsa -out server-configs/certs/server.key 4096
	openssl req -new -key server-configs/certs/server.key -out server-configs/certs/server.csr \
		-subj "/CN=hobnob" \
		-addext "subjectAltName=DNS:localhost,DNS:hobnob,DNS:server,IP:127.0.0.1"
	openssl x509 -req -in server-configs/certs/server.csr -CA server-configs/certs/ca.crt -CAkey server-configs/certs/ca.key \
		-CAcreateserial -out server-configs/certs/server.crt -days 365 \
		-copy_extensions copyall
	rm -f server-configs/certs/server.csr server-configs/certs/ca.srl
	@echo "Certificates generated in server-configs/certs/"

test:
	go test ./... -v -race -count=1

lint:
	golangci-lint run ./...
	buf lint

seed:
	go run ./cmd/seed/main.go

migrate:
	migrate -path migrations -database "$$DATABASE_URL" up

sqlc:
	sqlc generate

dist:
	GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/linux/amd64/hob ./agent
	GOOS=linux GOARCH=arm64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/linux/arm64/hob ./agent
	GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/darwin/amd64/hob ./agent
	GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/darwin/arm64/hob ./agent

clean:
	rm -rf bin/ server-configs/certs/ dist/
