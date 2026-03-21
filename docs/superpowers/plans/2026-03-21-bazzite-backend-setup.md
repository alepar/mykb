# Bazzite Backend Setup Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move mykb data storage to `~/.local/share/mykb/` and get the backend running on Bazzite OS with rootless podman.

**Architecture:** Update docker-compose.yml volume paths to use `${HOME}/.local/share/mykb/`, migrate existing document cache, delete old data dir, update CLAUDE.md, then bring up services and verify.

**Tech Stack:** Docker Compose, podman (rootless), bash

**Spec:** `docs/superpowers/specs/2026-03-21-bazzite-backend-setup-design.md`

---

## Chunk 1: Configuration Changes

### Task 1: Update docker-compose.yml volume paths

**Files:**
- Modify: `docker-compose.yml:19,30,39,49`

- [ ] **Step 1: Update mykb service volume**

Change line 19 from:
```yaml
      - ./data/documents:/data/documents:Z
```
to:
```yaml
      - ${HOME}/.local/share/mykb/documents:/data/documents:Z
```

- [ ] **Step 2: Update postgres service volume**

Change line 30 from:
```yaml
      - ./data/postgres:/var/lib/postgresql/data:Z
```
to:
```yaml
      - ${HOME}/.local/share/mykb/postgres:/var/lib/postgresql/data:Z
```

- [ ] **Step 3: Update qdrant service volume**

Change line 39 from:
```yaml
      - ./data/qdrant:/qdrant/storage:Z
```
to:
```yaml
      - ${HOME}/.local/share/mykb/qdrant:/qdrant/storage:Z
```

- [ ] **Step 4: Update meilisearch service volume**

Change line 49 from:
```yaml
      - ./data/meili:/meili_data:Z
```
to:
```yaml
      - ${HOME}/.local/share/mykb/meili:/meili_data:Z
```

- [ ] **Step 5: Verify the compose file parses correctly**

Run: `docker compose config --quiet`
Expected: No output (exit 0), meaning the file is valid.

- [ ] **Step 6: Commit**

Note: the working tree already has `user: "0:1000"` on qdrant and meilisearch (for rootless podman compatibility). This commit includes both the volume path changes and those user settings.

```bash
git add docker-compose.yml
git commit -m "chore: move data volumes to ~/.local/share/mykb/, add user mapping for rootless podman"
```

### Task 2: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Remove the "Known issue" block**

Remove these lines:
```markdown
**Known issue:** `go test ./...` and `go build ./...` fail with permission denied on `data/postgres/` (Docker-owned volume). Use specific package paths instead:
\```bash
go test ./cmd/... ./internal/... ./gen/...
go build ./cmd/... ./internal/... ./gen/...
\```
```

- [ ] **Step 2: Update the "Clearing Test Data" section**

The commands reference `localhost` ports which are unchanged. The `docker compose restart mykb` is still correct. No changes needed here.

- [ ] **Step 3: Update the "Data Preservation" section**

Add a note that data lives in `~/.local/share/mykb/` and is machine-local (not synced). Replace the warning about `docker compose down -v` with:
```markdown
**IMPORTANT: Protect local database contents.** Data is stored in `~/.local/share/mykb/` (machine-local, not synced between machines). The PostgreSQL `documents` table records which URLs have been ingested — this metadata is very hard to recreate (requires manually rediscovering all original URLs). Never run `docker compose down -v` or otherwise destroy the Postgres volume. Avoid `DELETE FROM documents` unless explicitly asked.
```

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md for new data path, remove stale known issue"
```

## Chunk 2: Data Migration and Service Startup

### Task 3: Create directories and migrate document cache

- [ ] **Step 0: Verify .env file exists**

```bash
test -f /var/home/alepar/AleCode/mykb/.env && echo "OK" || echo "MISSING"
grep -q VOYAGE_API_KEY /var/home/alepar/AleCode/mykb/.env && echo "VOYAGE_API_KEY: set" || echo "VOYAGE_API_KEY: MISSING"
grep -q MEILISEARCH_KEY /var/home/alepar/AleCode/mykb/.env && echo "MEILISEARCH_KEY: set" || echo "MEILISEARCH_KEY: MISSING"
```
Expected: All three say OK/set. If missing, create `.env` with the required keys before proceeding.

- [ ] **Step 1: Create the data directories**

```bash
mkdir -p ~/.local/share/mykb/{postgres,qdrant,meili,documents}
```

- [ ] **Step 2: Copy existing document cache**

```bash
cp -a /var/home/alepar/AleCode/mykb/data/documents/* ~/.local/share/mykb/documents/
```

- [ ] **Step 3: Verify the copy**

```bash
diff <(ls /var/home/alepar/AleCode/mykb/data/documents/) <(ls ~/.local/share/mykb/documents/)
```
Expected: No output (directories match).

### Task 4: Bring up services

- [ ] **Step 1: Build and start all services**

```bash
cd /var/home/alepar/AleCode/mykb && docker compose up -d
```

Expected: All 5 services start. Watch for:
- Image pull failures (network)
- Build failures on the `mykb` service (Dockerfile)
- Permission errors on volume mounts

- [ ] **Step 2: Check service health**

```bash
docker compose ps
```
Expected: All services show as "running" (or "Up").

- [ ] **Step 3: Check logs for errors**

```bash
docker compose logs --tail=20
```

Look for:
- Postgres: "database system is ready to accept connections"
- Qdrant: "Qdrant gRPC listening on 6334"
- Meilisearch: "Server listening on"
- mykb: successful startup log
- crawl4ai: ready message

- [ ] **Step 4: If permission errors occur on any service**

Check which service failed with `docker compose logs <service>`. Common fixes:
- Postgres needs its data dir owned by UID 999 inside container. In rootless podman, try: `podman unshare chown 999:999 ~/.local/share/mykb/postgres`
- Qdrant/Meilisearch already have `user: "0:1000"` so should be fine.
- If `:Z` label causes issues, try removing it (SELinux may not apply inside distrobox).

- [ ] **Step 5: Verify service connectivity**

```bash
# Postgres
docker compose exec -T postgres psql -U mykb -d mykb -c "SELECT 1;"

# Qdrant
curl -s http://localhost:6336/collections | head -5

# Meilisearch
curl -s -H 'Authorization: Bearer mykb-dev-key' http://localhost:7701/health

# mykb gRPC (basic connectivity)
curl -s http://localhost:9090 || true  # gRPC won't respond to HTTP, but connection should not be refused
```

### Task 5: Delete old data directory

Only do this after Task 4 confirms services are healthy with the new paths.

- [ ] **Step 1: Delete the old data directory**

```bash
rm -rf /var/home/alepar/AleCode/mykb/data/
```

Do NOT add to `.stignore` — let Syncthing propagate the deletion to all replicas first.

- [ ] **Step 2: Verify go test works with ./...**

```bash
cd /var/home/alepar/AleCode/mykb && go test ./...
```
Expected: Tests pass (or fail for reasons unrelated to `data/postgres/` permissions).

No git commit needed — `data/` is already in `.gitignore`.

### Task 6: Verify CLI connectivity

- [ ] **Step 1: Build the CLI**

```bash
cd /var/home/alepar/AleCode/mykb && go build -o mykb ./cmd/mykb/
```

- [ ] **Step 2: Test a query (expects empty results since DB is fresh)**

```bash
./mykb query "test"
```
Expected: Empty results or a "no results" message (not a connection error).
