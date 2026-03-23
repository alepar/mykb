# K8s Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy all MyKB services to the existing k3s cluster with PVCs, daily postgres backups, GitHub Actions CI, and justfile deployment targets.

**Architecture:** 5 Deployments (mykb-api, postgres, qdrant, meilisearch, crawl4ai) in a `mykb` namespace, all pinned to the `hass` node. Two Ingress resources on `mykb.k3s` (gRPC via h2c + HTTP for the ingest API). GitHub Actions builds and pushes `ghcr.io/alepar/mykb` on main push.

**Tech Stack:** Kubernetes (k3s), Traefik ingress, local-path PVCs, GitHub Actions, GitHub Container Registry

**Spec:** `docs/superpowers/specs/2026-03-22-k8s-deployment-design.md`

---

### Task 1: Update .gitignore and Dockerfile

**Files:**
- Modify: `.gitignore`
- Modify: `Dockerfile`

- [ ] **Step 1: Add k8s secret files to .gitignore**

Append to `.gitignore`:

```
k8s/secrets.yaml
k8s/ghcr-secret.yaml
k8s/postgres-secret.yaml
```

- [ ] **Step 2: Add EXPOSE 9091 to Dockerfile**

In `Dockerfile`, after line `EXPOSE 9090`, add `EXPOSE 9091`.

- [ ] **Step 3: Commit**

```bash
git add .gitignore Dockerfile
git commit -m "chore: prepare for k8s deployment (gitignore secrets, expose HTTP port)"
```

---

### Task 2: Create namespace and secret templates

**Files:**
- Create: `k8s/namespace.yaml`
- Create: `k8s/secrets.yaml.example`
- Create: `k8s/ghcr-secret.yaml.example`
- Create: `k8s/postgres-secret.yaml.example`

- [ ] **Step 1: Create k8s directory and namespace manifest**

```yaml
# k8s/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: mykb
```

- [ ] **Step 2: Create app secrets template**

```yaml
# k8s/secrets.yaml.example
# Copy to secrets.yaml and fill in values from .env
apiVersion: v1
kind: Secret
metadata:
  name: mykb-secrets
  namespace: mykb
type: Opaque
stringData:
  VOYAGE_API_KEY: "your-voyage-api-key-here"
  MEILISEARCH_KEY: "your-meilisearch-key-here"
```

- [ ] **Step 3: Create ghcr registry secret template**

```yaml
# k8s/ghcr-secret.yaml.example
# Copy to ghcr-secret.yaml and fill in your GitHub PAT
#
# Generate token at: GitHub → Settings → Developer settings → Personal access tokens
# Required scopes: read:packages
#
# Get your token with: gh auth token
apiVersion: v1
kind: Secret
metadata:
  name: ghcr-secret
  namespace: mykb
type: kubernetes.io/dockerconfigjson
stringData:
  .dockerconfigjson: |
    {
      "auths": {
        "ghcr.io": {
          "username": "alepar",
          "password": "YOUR_GITHUB_PAT_HERE"
        }
      }
    }
```

- [ ] **Step 4: Create postgres secret template**

```yaml
# k8s/postgres-secret.yaml.example
# Copy to postgres-secret.yaml and set credentials
apiVersion: v1
kind: Secret
metadata:
  name: postgres-secret
  namespace: mykb
type: Opaque
stringData:
  POSTGRES_USER: "mykb"
  POSTGRES_PASSWORD: "your-password-here"
  POSTGRES_DB: "mykb"
```

- [ ] **Step 5: Commit**

```bash
git add k8s/namespace.yaml k8s/secrets.yaml.example k8s/ghcr-secret.yaml.example k8s/postgres-secret.yaml.example
git commit -m "feat(k8s): add namespace and secret templates"
```

---

### Task 3: Create PersistentVolumeClaims

**Files:**
- Create: `k8s/pvcs.yaml`

- [ ] **Step 1: Create PVCs manifest**

All four PVCs in a single file, using the default `local-path` storage class.

```yaml
# k8s/pvcs.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mykb-data
  namespace: mykb
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: postgres-data
  namespace: mykb
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 2Gi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: qdrant-data
  namespace: mykb
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 2Gi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: meilisearch-data
  namespace: mykb
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 2Gi
```

- [ ] **Step 2: Commit**

```bash
git add k8s/pvcs.yaml
git commit -m "feat(k8s): add PersistentVolumeClaims for stateful services"
```

---

### Task 4: Create postgres deployment and service

**Files:**
- Create: `k8s/postgres-deployment.yaml`
- Create: `k8s/postgres-service.yaml`

- [ ] **Step 1: Create postgres deployment**

```yaml
# k8s/postgres-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
  namespace: mykb
  labels:
    app: postgres
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      nodeSelector:
        kubernetes.io/hostname: hass
      containers:
        - name: postgres
          image: postgres:17
          ports:
            - containerPort: 5432
          env:
            - name: POSTGRES_USER
              valueFrom:
                secretKeyRef:
                  name: postgres-secret
                  key: POSTGRES_USER
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: postgres-secret
                  key: POSTGRES_PASSWORD
            - name: POSTGRES_DB
              valueFrom:
                secretKeyRef:
                  name: postgres-secret
                  key: POSTGRES_DB
            - name: PGDATA
              value: /var/lib/postgresql/data/pgdata
          volumeMounts:
            - name: postgres-data
              mountPath: /var/lib/postgresql/data
          resources:
            requests:
              memory: "64Mi"
              cpu: "100m"
            limits:
              memory: "256Mi"
              cpu: "500m"
          readinessProbe:
            tcpSocket:
              port: 5432
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            tcpSocket:
              port: 5432
            initialDelaySeconds: 15
            periodSeconds: 20
      volumes:
        - name: postgres-data
          persistentVolumeClaim:
            claimName: postgres-data
```

- [ ] **Step 2: Create postgres service**

```yaml
# k8s/postgres-service.yaml
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: mykb
spec:
  selector:
    app: postgres
  ports:
    - port: 5432
      targetPort: 5432
  type: ClusterIP
```

- [ ] **Step 3: Commit**

```bash
git add k8s/postgres-deployment.yaml k8s/postgres-service.yaml
git commit -m "feat(k8s): add postgres deployment and service"
```

---

### Task 5: Create qdrant deployment and service

**Files:**
- Create: `k8s/qdrant-deployment.yaml`
- Create: `k8s/qdrant-service.yaml`

- [ ] **Step 1: Create qdrant deployment**

```yaml
# k8s/qdrant-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: qdrant
  namespace: mykb
  labels:
    app: qdrant
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: qdrant
  template:
    metadata:
      labels:
        app: qdrant
    spec:
      nodeSelector:
        kubernetes.io/hostname: hass
      containers:
        - name: qdrant
          image: qdrant/qdrant:v1.17.0
          ports:
            - containerPort: 6334
              name: grpc
            - containerPort: 6333
              name: http
          volumeMounts:
            - name: qdrant-data
              mountPath: /qdrant/storage
          resources:
            requests:
              memory: "128Mi"
              cpu: "100m"
            limits:
              memory: "512Mi"
              cpu: "500m"
          readinessProbe:
            httpGet:
              path: /readyz
              port: 6333
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /readyz
              port: 6333
            initialDelaySeconds: 10
            periodSeconds: 20
      volumes:
        - name: qdrant-data
          persistentVolumeClaim:
            claimName: qdrant-data
```

- [ ] **Step 2: Create qdrant service**

Only gRPC port exposed — HTTP port 6333 is only used for container health probes.

```yaml
# k8s/qdrant-service.yaml
apiVersion: v1
kind: Service
metadata:
  name: qdrant
  namespace: mykb
spec:
  selector:
    app: qdrant
  ports:
    - port: 6334
      targetPort: 6334
  type: ClusterIP
```

- [ ] **Step 3: Commit**

```bash
git add k8s/qdrant-deployment.yaml k8s/qdrant-service.yaml
git commit -m "feat(k8s): add qdrant deployment and service"
```

---

### Task 6: Create meilisearch deployment and service

**Files:**
- Create: `k8s/meilisearch-deployment.yaml`
- Create: `k8s/meilisearch-service.yaml`

- [ ] **Step 1: Create meilisearch deployment**

```yaml
# k8s/meilisearch-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: meilisearch
  namespace: mykb
  labels:
    app: meilisearch
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: meilisearch
  template:
    metadata:
      labels:
        app: meilisearch
    spec:
      nodeSelector:
        kubernetes.io/hostname: hass
      containers:
        - name: meilisearch
          image: getmeili/meilisearch:v1.37.0
          ports:
            - containerPort: 7700
          env:
            - name: MEILI_MASTER_KEY
              valueFrom:
                secretKeyRef:
                  name: mykb-secrets
                  key: MEILISEARCH_KEY
          volumeMounts:
            - name: meilisearch-data
              mountPath: /meili_data
          resources:
            requests:
              memory: "256Mi"
              cpu: "250m"
            limits:
              memory: "1Gi"
              cpu: "500m"
          readinessProbe:
            httpGet:
              path: /health
              port: 7700
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /health
              port: 7700
            initialDelaySeconds: 10
            periodSeconds: 20
      volumes:
        - name: meilisearch-data
          persistentVolumeClaim:
            claimName: meilisearch-data
```

- [ ] **Step 2: Create meilisearch service**

```yaml
# k8s/meilisearch-service.yaml
apiVersion: v1
kind: Service
metadata:
  name: meilisearch
  namespace: mykb
spec:
  selector:
    app: meilisearch
  ports:
    - port: 7700
      targetPort: 7700
  type: ClusterIP
```

- [ ] **Step 3: Commit**

```bash
git add k8s/meilisearch-deployment.yaml k8s/meilisearch-service.yaml
git commit -m "feat(k8s): add meilisearch deployment and service"
```

---

### Task 7: Create crawl4ai deployment and service

**Files:**
- Create: `k8s/crawl4ai-deployment.yaml`
- Create: `k8s/crawl4ai-service.yaml`

- [ ] **Step 1: Create crawl4ai deployment**

```yaml
# k8s/crawl4ai-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: crawl4ai
  namespace: mykb
  labels:
    app: crawl4ai
spec:
  replicas: 1
  selector:
    matchLabels:
      app: crawl4ai
  template:
    metadata:
      labels:
        app: crawl4ai
    spec:
      nodeSelector:
        kubernetes.io/hostname: hass
      containers:
        - name: crawl4ai
          image: unclecode/crawl4ai:latest
          ports:
            - containerPort: 11235
          resources:
            requests:
              memory: "256Mi"
              cpu: "250m"
            limits:
              memory: "1Gi"
              cpu: "1000m"
          readinessProbe:
            httpGet:
              path: /health
              port: 11235
            initialDelaySeconds: 10
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /health
              port: 11235
            initialDelaySeconds: 30
            periodSeconds: 20
```

- [ ] **Step 2: Create crawl4ai service**

```yaml
# k8s/crawl4ai-service.yaml
apiVersion: v1
kind: Service
metadata:
  name: crawl4ai
  namespace: mykb
spec:
  selector:
    app: crawl4ai
  ports:
    - port: 11235
      targetPort: 11235
  type: ClusterIP
```

- [ ] **Step 3: Commit**

```bash
git add k8s/crawl4ai-deployment.yaml k8s/crawl4ai-service.yaml
git commit -m "feat(k8s): add crawl4ai deployment and service"
```

---

### Task 8: Create mykb-api deployment and services

**Files:**
- Create: `k8s/mykb-api-deployment.yaml`
- Create: `k8s/mykb-api-service.yaml` (contains both gRPC and HTTP Service resources)

- [ ] **Step 1: Create mykb-api deployment**

Note the env ordering: `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` must appear before `POSTGRES_DSN` for k8s `$(VAR)` expansion to work.

```yaml
# k8s/mykb-api-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mykb-api
  namespace: mykb
  labels:
    app: mykb-api
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mykb-api
  template:
    metadata:
      labels:
        app: mykb-api
    spec:
      nodeSelector:
        kubernetes.io/hostname: hass
      imagePullSecrets:
        - name: ghcr-secret
      containers:
        - name: mykb-api
          image: ghcr.io/alepar/mykb:latest
          ports:
            - containerPort: 9090
              name: grpc
            - containerPort: 9091
              name: http
          env:
            # postgres-secret values MUST come before POSTGRES_DSN (k8s var expansion order)
            - name: POSTGRES_USER
              valueFrom:
                secretKeyRef:
                  name: postgres-secret
                  key: POSTGRES_USER
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: postgres-secret
                  key: POSTGRES_PASSWORD
            - name: POSTGRES_DB
              valueFrom:
                secretKeyRef:
                  name: postgres-secret
                  key: POSTGRES_DB
            - name: POSTGRES_DSN
              value: "postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@postgres:5432/$(POSTGRES_DB)?sslmode=disable"
            - name: QDRANT_GRPC_HOST
              value: "qdrant:6334"
            - name: MEILISEARCH_HOST
              value: "http://meilisearch:7700"
            - name: MEILISEARCH_KEY
              valueFrom:
                secretKeyRef:
                  name: mykb-secrets
                  key: MEILISEARCH_KEY
            - name: VOYAGE_API_KEY
              valueFrom:
                secretKeyRef:
                  name: mykb-secrets
                  key: VOYAGE_API_KEY
            - name: CRAWL4AI_URL
              value: "http://crawl4ai:11235"
            - name: DATA_DIR
              value: "/data/documents"
          volumeMounts:
            - name: mykb-data
              mountPath: /data/documents
          resources:
            requests:
              memory: "64Mi"
              cpu: "128m"
            limits:
              memory: "256Mi"
              cpu: "500m"
          readinessProbe:
            tcpSocket:
              port: 9090
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            tcpSocket:
              port: 9090
            initialDelaySeconds: 5
            periodSeconds: 20
      volumes:
        - name: mykb-data
          persistentVolumeClaim:
            claimName: mykb-data
```

- [ ] **Step 2: Create mykb-api services (both gRPC and HTTP)**

Two Service resources in one file. Both select the same pod (`app: mykb-api`) but expose different ports for the two Ingress resources.

```yaml
# k8s/mykb-api-service.yaml
apiVersion: v1
kind: Service
metadata:
  name: mykb-api-grpc
  namespace: mykb
spec:
  selector:
    app: mykb-api
  ports:
    - port: 9090
      targetPort: 9090
  type: ClusterIP
---
apiVersion: v1
kind: Service
metadata:
  name: mykb-api-http
  namespace: mykb
spec:
  selector:
    app: mykb-api
  ports:
    - port: 9091
      targetPort: 9091
  type: ClusterIP
```

- [ ] **Step 3: Commit**

```bash
git add k8s/mykb-api-deployment.yaml k8s/mykb-api-service.yaml
git commit -m "feat(k8s): add mykb-api deployment and services (gRPC + HTTP)"
```

---

### Task 9: Create ingress resources

**Files:**
- Create: `k8s/ingress-grpc.yaml`
- Create: `k8s/ingress-http.yaml`

- [ ] **Step 1: Create gRPC ingress**

The `h2c` annotation tells Traefik to forward as cleartext HTTP/2 (gRPC).

```yaml
# k8s/ingress-grpc.yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: mykb-ingress-grpc
  namespace: mykb
  annotations:
    traefik.ingress.kubernetes.io/service.serversscheme: h2c
spec:
  rules:
    - host: mykb.k3s
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: mykb-api-grpc
                port:
                  number: 9090
```

- [ ] **Step 2: Create HTTP ingress**

More specific path matches first in Traefik routing, so `/api/ingest` takes priority over `/`.

```yaml
# k8s/ingress-http.yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: mykb-ingress-http
  namespace: mykb
spec:
  rules:
    - host: mykb.k3s
      http:
        paths:
          - path: /api/ingest
            pathType: Prefix
            backend:
              service:
                name: mykb-api-http
                port:
                  number: 9091
```

- [ ] **Step 3: Commit**

```bash
git add k8s/ingress-grpc.yaml k8s/ingress-http.yaml
git commit -m "feat(k8s): add ingress resources for gRPC and HTTP"
```

---

### Task 10: Create postgres backup CronJob

**Files:**
- Create: `k8s/postgres-backup-cronjob.yaml`

- [ ] **Step 1: Create CronJob manifest**

Daily at 3 AM. Deletes backups older than 30 days, then runs pg_dump. Uses hostPath volume at `/backup/mykb-postgres` on hass (directory already created).

```yaml
# k8s/postgres-backup-cronjob.yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: postgres-backup
  namespace: mykb
spec:
  schedule: "0 3 * * *"
  jobTemplate:
    spec:
      template:
        spec:
          nodeSelector:
            kubernetes.io/hostname: hass
          restartPolicy: OnFailure
          containers:
            - name: pg-dump
              image: postgres:17
              command:
                - /bin/sh
                - -c
                - |
                  find /backups -name "*.dump" -mtime +30 -delete
                  pg_dump -h postgres -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc \
                    -f "/backups/mykb-$(date +%Y%m%d).dump"
                  echo "Backup complete: mykb-$(date +%Y%m%d).dump"
              env:
                - name: POSTGRES_USER
                  valueFrom:
                    secretKeyRef:
                      name: postgres-secret
                      key: POSTGRES_USER
                - name: POSTGRES_DB
                  valueFrom:
                    secretKeyRef:
                      name: postgres-secret
                      key: POSTGRES_DB
                - name: PGPASSWORD
                  valueFrom:
                    secretKeyRef:
                      name: postgres-secret
                      key: POSTGRES_PASSWORD
              volumeMounts:
                - name: backups
                  mountPath: /backups
          volumes:
            - name: backups
              hostPath:
                path: /backup/mykb-postgres
                type: Directory
```

- [ ] **Step 2: Commit**

```bash
git add k8s/postgres-backup-cronjob.yaml
git commit -m "feat(k8s): add daily postgres backup CronJob"
```

---

### Task 11: Create GitHub Actions workflow

**Files:**
- Create: `.github/workflows/build-image.yaml`

- [ ] **Step 1: Create workflow file**

```yaml
# .github/workflows/build-image.yaml
name: Build and Push Image

on:
  push:
    branches: [main]
    tags:
      - 'v*'
    paths-ignore:
      - 'k8s/**'
      - 'docs/**'
      - '*.md'

env:
  REGISTRY: ghcr.io
  IMAGE: ghcr.io/alepar/mykb

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write

    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Log in to Container Registry
        uses: docker/login-action@v3
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Extract metadata
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: ${{ env.IMAGE }}
          tags: |
            type=raw,value=latest,enable=${{ github.ref == 'refs/heads/main' }}
            type=sha,prefix=sha-,enable=${{ github.ref == 'refs/heads/main' }}
            type=semver,pattern={{version}},enable=${{ startsWith(github.ref, 'refs/tags/v') }}

      - name: Build and push
        uses: docker/build-push-action@v5
        with:
          context: .
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
```

- [ ] **Step 2: Commit**

```bash
mkdir -p .github/workflows
git add .github/workflows/build-image.yaml
git commit -m "feat: add GitHub Actions workflow for Docker image build"
```

---

### Task 12: Add Justfile deployment targets

**Files:**
- Modify: `Justfile`

- [ ] **Step 1: Add k8s targets to Justfile**

Append these targets after the existing `proto` target:

```just
# k8s deployment

k8s-deploy:
    kubectl apply -f k8s/

k8s-restart:
    kubectl rollout restart deployment/mykb-api -n mykb

k8s-status:
    kubectl get pods,svc,ingress -n mykb

k8s-logs svc:
    kubectl logs -f deployment/{{svc}} -n mykb
```

- [ ] **Step 2: Commit**

```bash
git add Justfile
git commit -m "feat: add k8s deployment targets to Justfile"
```

---

### Task 13: Create secrets, deploy, and verify

This task requires the user to fill in real secret values. It is not automatable.

**Files:**
- Create (local only, gitignored): `k8s/secrets.yaml`, `k8s/ghcr-secret.yaml`, `k8s/postgres-secret.yaml`

- [ ] **Step 1: Create real secret files from templates**

```bash
cp k8s/secrets.yaml.example k8s/secrets.yaml
cp k8s/ghcr-secret.yaml.example k8s/ghcr-secret.yaml
cp k8s/postgres-secret.yaml.example k8s/postgres-secret.yaml
```

Edit each file with real values:
- `secrets.yaml`: Set `VOYAGE_API_KEY` (from `.env`) and `MEILISEARCH_KEY`
- `ghcr-secret.yaml`: Set GitHub PAT (get via `gh auth token`)
- `postgres-secret.yaml`: Set `POSTGRES_PASSWORD` (can keep user/db as `mykb`)

- [ ] **Step 2: Deploy all manifests**

```bash
just k8s-deploy
```

Expected: All resources created in the `mykb` namespace.

- [ ] **Step 3: Verify pods are running**

```bash
just k8s-status
```

Expected: All 5 pods in `Running` state (mykb-api may take a few crash-loop restarts while backends initialize), 6 services, 2 ingresses.

- [ ] **Step 4: Verify gRPC connectivity**

```bash
mykb query --host mykb.k3s:80 "test query"
```

Expected: Query returns results (or empty results if no data ingested yet, but no connection error).

- [ ] **Step 5: Verify HTTP ingest endpoint**

```bash
curl -s -X POST http://mykb.k3s/api/ingest -H 'Content-Type: application/json' -d '{"url":"https://example.com"}'
```

Expected: JSON response with a document ID and status.

- [ ] **Step 6: Check postgres backup CronJob exists**

```bash
kubectl get cronjob -n mykb
```

Expected: `postgres-backup` CronJob with schedule `0 3 * * *`.

- [ ] **Step 7: Manually trigger a backup to verify it works**

```bash
kubectl create job --from=cronjob/postgres-backup postgres-backup-test -n mykb
kubectl wait --for=condition=complete job/postgres-backup-test -n mykb --timeout=60s
kubectl logs job/postgres-backup-test -n mykb
```

Expected: Log output shows "Backup complete: mykb-YYYYMMDD.dump".

```bash
ssh hass.dayton ls -la /backup/mykb-postgres/
```

Expected: A `.dump` file present.

```bash
kubectl delete job postgres-backup-test -n mykb
```
