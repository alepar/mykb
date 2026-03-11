default:
    @just --list

up:
    docker compose up -d

down:
    docker compose down

build:
    go build ./...

lint:
    golangci-lint run ./...

fmt:
    gofmt -w .

test:
    go test ./...

cli:
    go build -o mykb ./cmd/mykb/

proto:
    protoc --proto_path=proto --go_out=paths=source_relative:gen --go-grpc_out=paths=source_relative:gen mykb/v1/kb.proto
