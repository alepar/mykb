#!/usr/bin/env python3
"""Clean up orphan data from Qdrant and Meilisearch that has no corresponding Postgres entry."""

import urllib.request
import json
import subprocess


def get_postgres_ids():
    """Get all chunk IDs from Postgres."""
    result = subprocess.run(
        ["docker", "compose", "exec", "-T", "postgres", "psql", "-U", "mykb", "-t", "-A", "-c", "SELECT id FROM chunks;"],
        capture_output=True, text=True
    )
    ids = set(line.strip() for line in result.stdout.strip().split('\n') if line.strip())
    print(f"Postgres chunk count: {len(ids)}")
    return ids


def get_qdrant_ids():
    """Scroll through all points in Qdrant 'chunks' collection."""
    qdrant_ids = set()
    offset = None
    while True:
        body = {"limit": 1000, "with_payload": False, "with_vector": False}
        if offset is not None:
            body["offset"] = offset
        req = urllib.request.Request(
            "http://localhost:6333/collections/chunks/points/scroll",
            data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json"},
            method="POST"
        )
        resp = urllib.request.urlopen(req)
        data = json.loads(resp.read())
        points = data["result"]["points"]
        for p in points:
            qdrant_ids.add(str(p["id"]))
        offset = data["result"].get("next_page_offset")
        if offset is None or len(points) == 0:
            break
    print(f"Qdrant point count: {len(qdrant_ids)}")
    return qdrant_ids


def get_meilisearch_ids():
    """Get all document IDs from Meilisearch 'chunks' index."""
    meili_ids = set()
    ms_offset = 0
    while True:
        url = f"http://localhost:7700/indexes/chunks/documents?limit=1000&offset={ms_offset}&fields=id"
        req = urllib.request.Request(url)
        resp = urllib.request.urlopen(req)
        data = json.loads(resp.read())
        results = data.get("results", [])
        if not results:
            break
        for doc in results:
            meili_ids.add(str(doc["id"]))
        ms_offset += len(results)
        if len(results) < 1000:
            break
    print(f"Meilisearch document count: {len(meili_ids)}")
    return meili_ids


def delete_qdrant_orphans(orphans):
    """Delete orphan points from Qdrant."""
    if not orphans:
        print("No Qdrant orphans to delete")
        return
    orphan_list = list(orphans)
    for i in range(0, len(orphan_list), 500):
        batch = orphan_list[i:i+500]
        body = {"points": batch}
        req = urllib.request.Request(
            "http://localhost:6333/collections/chunks/points/delete",
            data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json"},
            method="POST"
        )
        resp = urllib.request.urlopen(req)
        r = json.loads(resp.read())
        status = r.get("status", r.get("result", {}).get("status", "?"))
        print(f"  Qdrant delete batch {i//500+1}: status={status}")
    print(f"Deleted {len(orphans)} orphan points from Qdrant")


def delete_meilisearch_orphans(orphans):
    """Delete orphan documents from Meilisearch."""
    if not orphans:
        print("No Meilisearch orphans to delete")
        return
    orphan_list = list(orphans)
    for i in range(0, len(orphan_list), 500):
        batch = orphan_list[i:i+500]
        req = urllib.request.Request(
            "http://localhost:7700/indexes/chunks/documents/delete-batch",
            data=json.dumps(batch).encode(),
            headers={"Content-Type": "application/json"},
            method="POST"
        )
        resp = urllib.request.urlopen(req)
        r = json.loads(resp.read())
        print(f"  Meilisearch delete batch {i//500+1}: taskUid={r.get('taskUid', '?')}")
    print(f"Deleted {len(orphans)} orphan documents from Meilisearch")


def main():
    pg_ids = get_postgres_ids()
    qdrant_ids = get_qdrant_ids()
    meili_ids = get_meilisearch_ids()

    qdrant_orphans = qdrant_ids - pg_ids
    meili_orphans = meili_ids - pg_ids

    print(f"\nQdrant orphans found: {len(qdrant_orphans)}")
    print(f"Meilisearch orphans found: {len(meili_orphans)}")
    print()

    delete_qdrant_orphans(qdrant_orphans)
    delete_meilisearch_orphans(meili_orphans)

    print("\nDone!")


if __name__ == "__main__":
    main()
