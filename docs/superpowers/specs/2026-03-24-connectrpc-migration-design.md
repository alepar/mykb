# ConnectRPC Migration Design

## Overview

Replace the `google.golang.org/grpc` server and client with `connectrpc.com/connect`. This simplifies the RPC layer: a single HTTP server serves Connect protocol (gRPC-compatible, gRPC-web-compatible, and JSON-over-HTTP), eliminating the need for separate gRPC and HTTP ports, h2c ingress annotations, and dual k8s services.

## What Changes

### Proto codegen

- Keep `protoc-gen-go` for message types (`kb.pb.go` — unchanged)
- Replace `protoc-gen-go-grpc` with `protoc-gen-connect-go`
- Generated code: new `gen/mykb/v1/mykbv1connect/` package with service handler and client interfaces
- Remove old `gen/mykb/v1/kb_grpc.pb.go`
- Install tool: `go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest`

Justfile `proto` target becomes:
```
protoc --proto_path=proto \
  --go_out=paths=source_relative:gen \
  --connect-go_out=paths=source_relative:gen \
  mykb/v1/kb.proto
```

### Server (`cmd/mykb-api/main.go`)

Replace the gRPC server with a single `net/http` server on port 9091 that mounts:
- Connect service handler via `mykbv1connect.NewKBServiceHandler(srv)`
- Existing `/api/ingest` HTTP handler (from `server.NewHTTPHandler`)

The current two-server setup (gRPC on 9090 + HTTP on 9091) becomes one HTTP server on 9091. Remove gRPC-specific code: `grpc.NewServer()`, `reflection.Register()`, `net.Listen("tcp", ":"+cfg.GRPCPort)`.

### Server interface (`internal/server/server.go`)

Method signatures change from gRPC types to Connect types:

| Method | gRPC signature | Connect signature |
|--------|---------------|-------------------|
| `IngestURL` | `(req *mykbv1.IngestURLRequest, stream grpc.ServerStreamingServer[mykbv1.IngestProgress]) error` | `(ctx context.Context, req *connect.Request[mykbv1.IngestURLRequest], stream *connect.ServerStream[mykbv1.IngestProgress]) error` |
| `IngestURLs` | `(req *mykbv1.IngestURLsRequest, stream grpc.ServerStreamingServer[mykbv1.IngestURLsProgress]) error` | `(ctx context.Context, req *connect.Request[mykbv1.IngestURLsRequest], stream *connect.ServerStream[mykbv1.IngestURLsProgress]) error` |
| `Query` | `(ctx context.Context, req *mykbv1.QueryRequest) (*mykbv1.QueryResponse, error)` | `(ctx context.Context, req *connect.Request[mykbv1.QueryRequest]) (*connect.Response[mykbv1.QueryResponse], error)` |
| `ListDocuments` | same pattern | same pattern |
| `GetDocuments` | same pattern | same pattern |
| `DeleteDocument` | same pattern | same pattern |

Key differences:
- Context comes as first parameter (not from `stream.Context()`)
- Request wrapped in `connect.Request[T]`, access message via `req.Msg`
- Response wrapped in `connect.Response[T]`, return via `connect.NewResponse(msg)`
- Streaming: `stream.Send(msg)` instead of `stream.Send(msg)` (same method name, different type)
- Error codes: `connect.CodeAlreadyExists` instead of `codes.AlreadyExists`, `connect.NewError()` instead of `status.Errorf()`
- Remove `mykbv1.UnimplementedKBServiceServer` embed

### CLI client (`cmd/mykb/main.go`)

Replace gRPC client with Connect client:

- `connect.NewClient()` instead of `grpc.NewClient()`
- Takes a URL (`http://host`) instead of `host:port`
- No `credentials/insecure` needed
- Unary calls: `client.Query(ctx, connect.NewRequest(msg))` returns `(*connect.Response[T], error)`
- Server streaming: `client.IngestURL(ctx, connect.NewRequest(msg))` returns a `*connect.ServerStreamForClient[T]`, iterate via `stream.Receive()` (returns `bool`) + `stream.Msg()`

The `--host` flag semantics change: value is now an HTTP URL (e.g., `http://mykb.k3s`) instead of `host:port`. The `~/.mykb.conf` host config should also be updated to expect URLs.

The `connect()` helper function is removed — Connect clients are created directly per-service.

### Go dependencies

- Add: `connectrpc.com/connect`
- Remove: `google.golang.org/grpc`, `google.golang.org/grpc/credentials/insecure`, `google.golang.org/grpc/codes`, `google.golang.org/grpc/status`, `google.golang.org/grpc/reflection`
- Keep: `google.golang.org/protobuf`

### Dockerfile

Change `EXPOSE 9090` + `EXPOSE 9091` to just `EXPOSE 9091`.

### Config (`internal/config/config.go`)

Remove `GRPCPort` field. Keep `HTTPPort` (default 9091) as the single server port.

### K8s manifests

**Remove:**
- `k8s/mykb-api-service.yaml` — delete the file (contained two services: `mykb-api-grpc` and `mykb-api-http`)
- `k8s/ingress-grpc.yaml` — delete
- `k8s/ingress-http.yaml` — delete

**Create:**
- `k8s/mykb-api-service.yaml` — single service `mykb-api` on port 9091
- `k8s/ingress.yaml` — single ingress `mykb-ingress`, host `mykb.k3s`, path `/`, backend `mykb-api:9091`. No h2c annotation.

**Modify:**
- `k8s/mykb-api-deployment.yaml` — remove port 9090 (`grpc`), keep port 9091 (`http`). Update readiness/liveness probes to use HTTP GET on port 9091 (Connect serves a default health endpoint, or we add a simple `/healthz` handler).

### CLI config (`internal/cliconfig/`)

Update default host from `localhost:9090` to `http://localhost:9091`. The config file `~/.mykb.conf` host value should be a URL.

## What Does Not Change

- `proto/mykb/v1/kb.proto` — same RPCs, same messages
- `gen/mykb/v1/kb.pb.go` — message types unchanged
- `internal/server/server.go` business logic — same operations, just different wrapper types
- Pipeline (crawl, chunk, embed, index)
- Worker (batch coordinator)
- Storage backends
- Rate limiter
- `internal/server/http.go` — `/api/ingest` handler stays as-is, mounted on the same mux
