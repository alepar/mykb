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

proto:
    protoc --go_out=. --go-grpc_out=. proto/mykb/v1/kb.proto
