# Web UI Design

## Overview

Add a React + TypeScript web frontend for MyKB served at `mykb.k3s`, with the API moved to `api.mykb.k3s`. Three pages: status dashboard, URL ingestion, and query with a two-panel result viewer mirroring the CLI TUI layout.

## Architecture

**Frontend container:** React + TypeScript + Vite, built to static files, served by nginx. Separate k8s pod and Docker image (`ghcr.io/alepar/mykb-frontend`). Follows the meilisearch-movies project pattern.

**API communication:** Plain `fetch()` to ConnectRPC JSON endpoints at `http://api.mykb.k3s`. Endpoints:
- `POST /mykb.v1.KBService/ListDocuments` — status page
- `POST /mykb.v1.KBService/Query` — search
- `POST /mykb.v1.KBService/GetDocuments` — document metadata for query results
- `POST /api/ingest` — submit URL for ingestion

**DNS split:**
- `mykb.k3s` → frontend pod (nginx, port 80)
- `api.mykb.k3s` → mykb-api pod (ConnectRPC, port 9091)

## Pages

### Status Page (`/`)

- Document counts by status: DONE, PENDING, CRAWLING, EMBEDDING, ERROR — displayed as a simple summary bar or card grid
- Recent documents table (last 20): URL, title, status, created date
- Auto-refreshes every 10 seconds
- Calls `ListDocuments` with `limit=20` and uses the `total` field for counts

### Ingest Page (`/ingest`)

- Text input for URL + submit button
- Shows result (document ID, status) or error message
- Calls `POST /api/ingest` on the backend (existing REST endpoint)

### Query Page (`/query`)

Two-panel layout mirroring the CLI TUI:

**Left sidebar (~300px):**
- Search input at top + submit button
- Ranked results list below. Each entry shows: `#rank {score}`, domain, truncated title
- Click to select. Active item highlighted with background color
- First result auto-selected

**Right main pane (rest of width):**
- Pinned header: title (clickable link to URL), chunk position (e.g., "3-5/8"), dates (added, ingested), separator line
- Scrollable markdown body rendered via `react-markdown` + `remark-gfm`
- Code blocks syntax-highlighted

**Data flow:**
1. User submits query → `POST /mykb.v1.KBService/Query`
2. Response contains results with `document_id`, `chunk_index`, `chunk_index_end`, `score`, `text`
3. Fetch document metadata via `POST /mykb.v1.KBService/GetDocuments` with unique doc IDs
4. Display in two-panel layout

## Tech Stack

- **React 19** + **TypeScript**
- **Vite** for dev server and build
- **React Router** for SPA routing (3 pages)
- **Pico CSS** for base styling (classless CSS framework, minimal custom CSS)
- **react-markdown** + **remark-gfm** for markdown rendering
- **nginx:alpine** for production serving

## Frontend Directory Structure

```
frontend/
├── package.json
├── tsconfig.json
├── vite.config.ts
├── nginx.prod.conf
├── Dockerfile
├── index.html
└── src/
    ├── main.tsx
    ├── App.tsx
    ├── api.ts              # fetch wrappers for ConnectRPC + REST endpoints
    ├── config.ts            # API_BASE URL
    ├── pages/
    │   ├── StatusPage.tsx
    │   ├── IngestPage.tsx
    │   └── QueryPage.tsx
    └── components/
        ├── Nav.tsx          # simple navigation header
        ├── ResultSidebar.tsx # ranked results list
        └── ResultDetail.tsx  # header + rendered markdown
```

## K8s Changes

**New files:**
- `k8s/frontend-deployment.yaml` — deployment for frontend pod (nginx, port 80)
- `k8s/frontend-service.yaml` — ClusterIP service on port 80

**Modified files:**
- `k8s/ingress.yaml` — replace single ingress with two:
  - `mykb-ingress` on `mykb.k3s` → `frontend:80`
  - `mykb-api-ingress` on `api.mykb.k3s` → `mykb-api:9091`

**New GitHub Actions job:**
- Add `build-frontend` job to `.github/workflows/build-image.yaml`
- Triggers on changes to `frontend/` directory
- Builds and pushes `ghcr.io/alepar/mykb-frontend`

## CORS

The mykb-api needs to allow requests from `http://mykb.k3s` since the frontend and API are on different domains. The existing `/api/ingest` handler already sets `Access-Control-Allow-Origin: *`. For ConnectRPC endpoints, wrap the HTTP mux with CORS headers in `cmd/mykb-api/main.go`:

```go
mux.Handle(path, corsMiddleware(handler))
```

The middleware sets:
- `Access-Control-Allow-Origin: *`
- `Access-Control-Allow-Methods: POST, OPTIONS`
- `Access-Control-Allow-Headers: Content-Type, Connect-Protocol-Version`
- Handles OPTIONS preflight requests

## Nginx Config (`nginx.prod.conf`)

```nginx
server {
    listen 80;
    root /usr/share/nginx/html;

    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

Simple SPA config — all routes fall through to `index.html` for React Router to handle.

## Frontend Dockerfile

```dockerfile
FROM node:22-alpine AS builder
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:alpine
COPY nginx.prod.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
```

## What Does Not Change

- Proto file
- Backend server logic (except adding CORS middleware)
- Pipeline, worker, storage
- CLI client
- Existing k8s pods (postgres, qdrant, meilisearch, crawl4ai)
