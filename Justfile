set dotenv-load := true

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

# Bring up the parallel e2e stack (rebuilds mykb image; alt ports + volumes).
e2e-up:
    docker compose -p mykb-e2e -f docker-compose.e2e.yml up -d --build --wait

# Tear down the e2e stack and delete its volumes.
e2e-down:
    docker compose -p mykb-e2e -f docker-compose.e2e.yml down -v

# Run the full e2e suite (TestMain handles up/down internally).
e2e:
    go test -tags=e2e -timeout=10m -count=1 ./e2e/...

cli:
    go build -o mykb ./cmd/mykb/

proto:
    protoc --proto_path=proto --go_out=paths=source_relative:gen --connect-go_out=paths=source_relative:gen mykb/v1/kb.proto

# k8s deployment

k8s-deploy:
    kubectl apply -f k8s/

k8s-restart:
    kubectl rollout restart deployment/mykb-api -n mykb

k8s-status:
    kubectl get pods,svc,ingress -n mykb

k8s-logs svc:
    kubectl logs -f deployment/{{svc}} -n mykb
