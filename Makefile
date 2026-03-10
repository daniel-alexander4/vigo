.PHONY: build proto certs test lint seed migrate sqlc dist clean

VERSION ?= dev

build:
	go build -o bin/hobd ./server
	go build -o bin/hobnob ./agent
	go build -o bin/hobcli ./cli
	ln -sf hobcli bin/hnbvi

proto:
	buf generate

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
	GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/linux/amd64/hobnob ./agent
	GOOS=linux GOARCH=arm64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/linux/arm64/hobnob ./agent
	GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/darwin/amd64/hobnob ./agent
	GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.version=$(VERSION)" -o dist/agent/darwin/arm64/hobnob ./agent

clean:
	rm -rf bin/ certs/ dist/
