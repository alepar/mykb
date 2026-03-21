# Bazzite Backend Setup Design

## Problem

mykb was developed on macOS with Docker Desktop. Moving to Bazzite OS (Fedora immutable) running inside a Debian distrobox with rootless podman introduces:

1. **Syncthing conflict** — the project folder syncs between macOS and Bazzite. Live database files (Postgres, Qdrant, Meilisearch) in `data/` will corrupt if synced while services run.
2. **Permission mismatch** — rootless podman maps container UIDs through user namespaces differently than Docker Desktop. Files written by containers on one platform have wrong ownership on the other.
3. **Podman compatibility** — minor differences from Docker (compose alias, SELinux labels, rootless networking).

## Solution

Move all data storage out of the project directory to `~/.local/share/mykb/`. Each machine maintains independent database state. Re-ingest from `urls.txt` when setting up a new machine.

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

### 2. Directory Setup

```bash
mkdir -p ~/.local/share/mykb/{postgres,qdrant,meili,documents}
```

### 3. Cleanup

- Delete `data/` from project directory. Do NOT add to `.stignore` yet — let Syncthing propagate the deletion to all replicas first. Add `.stignore` entry later.

### 4. Podman Compatibility Notes

- `:Z` SELinux volume labels already present — works with podman.
- `user: "0:1000"` on qdrant/meilisearch — may need adjustment if rootless podman permission issues arise.
- `docker compose` in distrobox is an alias to podman compose — should work transparently.
- Dockerfile uses standard multi-stage Alpine build — no platform-specific concerns.

### 5. Data Bootstrapping

Re-ingest from `urls.txt` (1,108 URLs) on each new machine. Costs Voyage API embedding calls but avoids cross-platform data migration issues.

### 6. CLAUDE.md Updates

Update data path references and clearing test data instructions to reflect `~/.local/share/mykb/`.
