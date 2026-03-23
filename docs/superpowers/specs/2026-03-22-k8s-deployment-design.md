# Kubernetes Deployment Design for MyKB

## Overview

Deploy all MyKB services (mykb-api, PostgreSQL, Qdrant, Meilisearch, Crawl4AI) to the existing k3s cluster, following the deployment patterns established by the meilisearch-movies project. All services run as separate pods in a dedicated namespace, with PersistentVolumeClaims for stateful data and a daily postgres backup CronJob.

## Target Cluster

- **Cluster:** 2-node k3s (v1.34.5) — `hass` (control-plane) and `debby` (worker)
- **Ingress:** Traefik (default ingress class), MetalLB load balancer at `192.168.1.193`
- **DNS pattern:** `*.k3s` (e.g., `movies.k3s`, `grafana.k3s`)
- **Storage:** Rancher local-path provisioner (only storage class)
- **All pods pinned to `hass`** via `nodeSelector: { kubernetes.io/hostname: hass }`

## Namespace & Secrets

**Namespace:** `mykb`

**Three Secrets:**

1. **`mykb-secrets`** — application secrets
   - `VOYAGE_API_KEY` — Voyage AI embedding API key
   - `MEILISEARCH_KEY` — Meilisearch master key (shared between mykb-api and meilisearch)

2. **`postgres-secret`** — database credentials
   - `POSTGRES_USER`
   - `POSTGRES_PASSWORD`
   - `POSTGRES_DB`

3. **`ghcr-secret`** — GitHub Container Registry credentials (`kubernetes.io/dockerconfigjson`)
   - Used as `imagePullSecrets` by the mykb-api deployment only (other images are public)

Secret YAML files are gitignored. Example templates (`secrets.yaml.example`, `ghcr-secret.yaml.example`) are committed to the repo.

## Deployments

All deployments include `nodeSelector: { kubernetes.io/hostname: hass }`.

### mykb-api

- **Image:** `ghcr.io/alepar/mykb:latest`
- **Port:** 9090 (gRPC)
- **Strategy:** RollingUpdate
- **imagePullSecrets:** ghcr-secret
- **PVC:** `mykb-data` (1Gi) mounted at `/data/documents`
- **Readiness probe:** TCP on port 9090
- **Resources:** requests 64Mi/128m, limits 256Mi/500m
- **Environment:**
  - `POSTGRES_DSN` — `postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@postgres:5432/$(POSTGRES_DB)?sslmode=disable`
  - `QDRANT_GRPC_HOST` — `qdrant:6334`
  - `MEILISEARCH_HOST` — `http://meilisearch:7700`
  - `MEILISEARCH_KEY` — from `mykb-secrets`
  - `VOYAGE_API_KEY` — from `mykb-secrets`
  - `CRAWL4AI_URL` — `http://crawl4ai:11235`
  - `DATA_DIR` — `/data/documents`

### postgres

- **Image:** `postgres:17`
- **Port:** 5432
- **Strategy:** Recreate (avoid two pods mounting the same PVC)
- **PVC:** `postgres-data` (2Gi) mounted at `/var/lib/postgresql/data`
- **Readiness probe:** TCP on port 5432
- **Resources:** requests 64Mi/100m, limits 256Mi/500m
- **Environment:** `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` from `postgres-secret`

### qdrant

- **Image:** `qdrant/qdrant:v1.17.0`
- **Ports:** 6334 (gRPC), 6333 (HTTP)
- **Strategy:** Recreate
- **PVC:** `qdrant-data` (2Gi) mounted at `/qdrant/storage`
- **Readiness probe:** HTTP GET on port 6333 `/readyz`
- **Resources:** requests 128Mi/100m, limits 512Mi/500m

### meilisearch

- **Image:** `getmeili/meilisearch:v1.37.0`
- **Port:** 7700
- **Strategy:** Recreate
- **PVC:** `meilisearch-data` (2Gi) mounted at `/meili_data`
- **Readiness probe:** HTTP GET on port 7700 `/health`
- **Resources:** requests 256Mi/250m, limits 1Gi/500m
- **Environment:** `MEILI_MASTER_KEY` from `mykb-secrets` (key `MEILISEARCH_KEY`)

### crawl4ai

- **Image:** `unclecode/crawl4ai:latest`
- **Port:** 11235
- **Strategy:** RollingUpdate
- **No PVC** (stateless)
- **Readiness probe:** HTTP GET on port 11235 `/health`
- **Resources:** requests 256Mi/250m, limits 1Gi/1000m (headless browser is heavier)

## Services

Five ClusterIP services, one per deployment:

| Service | Port | Target Port |
|---------|------|-------------|
| mykb-api | 9090 | 9090 |
| postgres | 5432 | 5432 |
| qdrant | 6334 | 6334 |
| meilisearch | 7700 | 7700 |
| crawl4ai | 11235 | 11235 |

## Ingress

```yaml
host: mykb.k3s
path: /
backend: mykb-api:9090
```

Annotation `traefik.ingress.kubernetes.io/service.serversscheme: h2c` to enable gRPC proxying through Traefik. The CLI client connects to `mykb.k3s:80` and Traefik forwards as h2c (cleartext HTTP/2) to the gRPC backend.

## PersistentVolumeClaims

All use the default `local-path` storage class with `ReadWriteOnce` access mode.

| PVC | Size | Mount Path | Used By |
|-----|------|------------|---------|
| mykb-data | 1Gi | /data/documents | mykb-api |
| postgres-data | 2Gi | /var/lib/postgresql/data | postgres |
| qdrant-data | 2Gi | /qdrant/storage | qdrant |
| meilisearch-data | 2Gi | /meili_data | meilisearch |

## Postgres Backup CronJob

- **Schedule:** `0 3 * * *` (daily at 3 AM)
- **Image:** `postgres:17`
- **Command:** Run `pg_dump` with custom format, output to `/backups/mykb-YYYYMMDD.dump`. Delete backups older than 30 days before each dump.
- **Storage:** `hostPath` volume pointing to `/backup/mykb-postgres` on the `hass` node (directory already created)
- **Credentials:** From `postgres-secret`
- **nodeSelector:** `kubernetes.io/hostname: hass`
- **restartPolicy:** OnFailure

## GitHub Actions CI/CD

**Workflow:** `.github/workflows/build-image.yaml`

**Triggers:**
- Push to `main` branch (path filter excludes `k8s/`, `docs/`, `*.md`)
- Git tags `v*`

**Steps:**
1. Checkout code
2. Login to ghcr.io (`github.actor` + `GITHUB_TOKEN`)
3. Extract metadata (`docker/metadata-action@v5`):
   - On `main` push: tags `latest` and `sha-<7char>`
   - On version tag: semver tag
4. Build and push (`docker/build-push-action@v5`, context: `.`)

**Single image:** `ghcr.io/alepar/mykb` — only the mykb-api binary. Other services use stock public images.

## Justfile Targets

New targets added to the existing Justfile:

| Target | Command | Purpose |
|--------|---------|---------|
| `k8s-deploy` | `kubectl apply -f k8s/` | Apply all k8s manifests |
| `k8s-restart` | `kubectl rollout restart deployment/mykb-api -n mykb` | Restart mykb-api after image update |
| `k8s-status` | `kubectl get pods,svc,ingress -n mykb` | Check deployment status |
| `k8s-logs` | `kubectl logs -f deployment/<svc> -n mykb` | Tail logs for a service |

**Deployment workflow:**

1. **First time:** Copy secret examples, fill in values, run `just k8s-deploy`
2. **After code changes:** `git push` → GitHub Actions builds image → `just k8s-restart`
3. **Check status:** `just k8s-status`

## Manifest File Structure

```
k8s/
├── namespace.yaml
├── secrets.yaml.example
├── ghcr-secret.yaml.example
├── postgres-secret.yaml.example
├── mykb-api-deployment.yaml
├── mykb-api-service.yaml
├── postgres-deployment.yaml
├── postgres-service.yaml
├── qdrant-deployment.yaml
├── qdrant-service.yaml
├── meilisearch-deployment.yaml
├── meilisearch-service.yaml
├── crawl4ai-deployment.yaml
├── crawl4ai-service.yaml
├── ingress.yaml
├── pvcs.yaml
└── postgres-backup-cronjob.yaml
```

Secret YAML files (`secrets.yaml`, `ghcr-secret.yaml`, `postgres-secret.yaml`) are gitignored.
