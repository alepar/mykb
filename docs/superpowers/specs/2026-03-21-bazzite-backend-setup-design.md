# Bazzite Backend Setup Design

## Problem

mykb was developed on macOS with Docker Desktop. Moving to Bazzite OS (Fedora immutable) running inside a Debian distrobox with rootless podman introduces:

1. **Syncthing conflict** — the project folder syncs between macOS and Bazzite. Live database files (Postgres, Qdrant, Meilisearch) in `data/` will corrupt if synced while services run.
2. **Permission mismatch** — rootless podman maps container UIDs through user namespaces differently than Docker Desktop. Files written by containers on one platform have wrong ownership on the other.
3. **Podman compatibility** — minor differences from Docker (compose alias, SELinux labels, rootless networking).

## Solution

Move all data storage out of the project directory to `~/.local/share/mykb/`. Both macOS and Bazzite use the same `docker-compose.yml` with `${HOME}` expansion — the path resolves correctly on both platforms (`/Users/<user>/.local/share/mykb/` on macOS, `/var/home/<user>/.local/share/mykb/` on Bazzite). Each machine maintains independent database state. Re-ingest from `urls.txt` when setting up a new machine.

## Changes

### 1. docker-compose.yml Volume Paths

All four volume mounts change from `./data/...` to `${HOME}/.local/share/mykb/...`:

| Service | Old Path | New Path |
|---------|----------|----------|
| mykb (documents) | `./data/documents:/data/documents:Z` | `${HOME}/.local/share/mykb/documents:/data/documents:Z` |
| postgres | `./data/postgres:/var/lib/postgresql/data:Z` | `${HOME}/.local/share/mykb/postgres:/var/lib/postgresql/data:Z` |
| qdrant | `./data/qdrant:/qdrant/storage:Z` | `${HOME}/.local/share/mykb/qdrant:/qdrant/storage:Z` |
| meilisearch | `./data/meili:/meili_data:Z` | `${HOME}/.local/share/mykb/meili:/meili_data:Z` |

Note: `~` is not expanded by Docker Compose; `${HOME}` is.

### 2. Qdrant/Meilisearch User Setting

Add `user: "0:1000"` to qdrant and meilisearch services. This runs the process as root inside the container with GID 1000. In rootless podman, container UID 0 maps to the host user's UID via user namespace, so files are owned by the host user. On macOS Docker Desktop, root is the default anyway, so this is harmless.

### 3. Directory Setup

```bash
mkdir -p ~/.local/share/mykb/{postgres,qdrant,meili,documents}
```

Run on each machine before first `docker compose up`.

### 4. Cleanup

- Copy `data/documents/` to `~/.local/share/mykb/documents/` on the current machine to preserve cached markdown and avoid re-crawling (some URLs may have gone offline).
- Delete `data/` from project directory. Do NOT add to `.stignore` yet — let Syncthing propagate the deletion to all replicas first. Add `.stignore` entry later.
- On macOS: run `mkdir -p ~/.local/share/mykb/{postgres,qdrant,meili,documents}` before next `docker compose up`.

### 5. Podman Compatibility Notes

- `:Z` SELinux volume labels already present — works with podman.
- `docker compose` in distrobox is an alias to podman compose — should work transparently.
- Dockerfile uses standard multi-stage Alpine build — no platform-specific concerns.

### 6. Data Bootstrapping

Re-ingest from `urls.txt` (1,108 URLs) on each new machine. Embedding cost is minimal (~$2 for Voyage API calls). Copy `data/documents/` from any existing machine to skip re-crawling.

### 7. CLAUDE.md Updates

- Update data path references and clearing test data instructions to reflect `~/.local/share/mykb/`.
- Remove the "known issue" about `go test ./...` failing with permission denied on `data/postgres/` — this is resolved since `data/` no longer exists in the project directory.
