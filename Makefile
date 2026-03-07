.PHONY: build proto certs test lint seed migrate sqlc dist clean

VERSION ?= dev

build:
	go build -o bin/isogov-server ./server
	go build -o bin/isogov-agent ./agent
	go build -o bin/gvctl ./cli

proto:
	buf generate

certs:
	mkdir -p certs
	@echo "Generating CA keypair..."
	openssl genrsa -out certs/ca.key 4096
	openssl req -new -x509 -key certs/ca.key -out certs/ca.crt -days 3650 \
		-subj "/CN=Isogov CA"
	@echo "Generating server keypair..."
	openssl genrsa -out certs/server.key 4096
	openssl req -new -key certs/server.key -out certs/server.csr \
		-subj "/CN=isogov" \
		-addext "subjectAltName=DNS:localhost,DNS:isogov,DNS:server,IP:127.0.0.1"
	openssl x509 -req -in certs/server.csr -CA certs/ca.crt -CAkey certs/ca.key \
		-CAcreateserial -out certs/server.crt -days 365 \
		-copy_extensions copyall
	rm -f certs/server.csr certs/ca.srl
	@echo "Certificates generated in certs/"

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
	GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/linux/amd64/isogov-agent ./agent
	GOOS=linux GOARCH=arm64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/linux/arm64/isogov-agent ./agent
	GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/darwin/amd64/isogov-agent ./agent
	GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/darwin/arm64/isogov-agent ./agent

clean:
	rm -rf bin/ certs/ dist/
