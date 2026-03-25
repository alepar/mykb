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
