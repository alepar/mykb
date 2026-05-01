package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newTestStore creates a PostgresStore backed by either testcontainers or
// TEST_POSTGRES_DSN. It calls t.Skip if neither is available.
func newTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	ctx := context.Background()

	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		// Try testcontainers.
		ctr, err := postgres.Run(ctx,
			"postgres:16-alpine",
			postgres.WithDatabase("testdb"),
			postgres.WithUsername("test"),
			postgres.WithPassword("test"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(30*time.Second),
			),
		)
		if err != nil {
			t.Skipf("skipping postgres tests: testcontainers unavailable: %v", err)
		}
		t.Cleanup(func() {
			_ = ctr.Terminate(ctx)
		})

		var connErr error
		dsn, connErr = ctr.ConnectionString(ctx, "sslmode=disable")
		if connErr != nil {
			t.Fatalf("connection string: %v", connErr)
		}
	}

	store, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.RunMigrations(ctx); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// Clean tables for a fresh test run.
	_, _ = store.pool.Exec(ctx, "DELETE FROM chunks")
	_, _ = store.pool.Exec(ctx, "DELETE FROM documents")

	return store
}

func TestDisplayStatus(t *testing.T) {
	tests := []struct {
		step, state string
		retriable   bool
		want        string
	}{
		{"CRAWLING", "QUEUED", true, "PENDING"},
		{"CRAWLING", "PROCESSING", true, "CRAWLING"},
		{"EMBEDDING", "PROCESSING", true, "EMBEDDING"},
		{"DONE", "COMPLETED", true, "DONE"},
		{"CRAWLING", "FAILED", true, "CRAWLING"},
		{"CRAWLING", "FAILED", false, "ERROR"},
		{"EMBEDDING", "FAILED", false, "ERROR"},
		{"CRAWLING", "ABANDONED", true, "CRAWLING"},
	}
	for _, tt := range tests {
		d := Document{Step: tt.step, State: tt.state, IsRetriable: tt.retriable}
		if got := d.DisplayStatus(); got != tt.want {
			t.Errorf("DisplayStatus(%q, %q, retriable=%v) = %q, want %q",
				tt.step, tt.state, tt.retriable, got, tt.want)
		}
	}
}

func TestInsertAndGetDocument(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, err := store.InsertDocument(ctx, "https://example.com/page1")
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	if doc.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if doc.URL != "https://example.com/page1" {
		t.Fatalf("URL = %q, want %q", doc.URL, "https://example.com/page1")
	}
	if doc.Step != "CRAWLING" {
		t.Fatalf("Step = %q, want CRAWLING", doc.Step)
	}
	if doc.State != "QUEUED" {
		t.Fatalf("State = %q, want QUEUED", doc.State)
	}
	if !doc.IsRetriable {
		t.Fatal("expected IsRetriable = true")
	}

	got, err := store.GetDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.ID != doc.ID || got.URL != doc.URL {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
}

func TestDuplicateURL(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, err := store.InsertDocument(ctx, "https://example.com/dup")
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err = store.InsertDocument(ctx, "https://example.com/dup")
	if err == nil {
		t.Fatal("expected error on duplicate URL")
	}
}

func TestClaimDocument(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/claim")

	claimed, got, err := store.ClaimDocument(ctx, doc.ID, "worker-1")
	if err != nil {
		t.Fatalf("ClaimDocument: %v", err)
	}
	if !claimed {
		t.Fatal("expected claim to succeed")
	}
	if got.State != "PROCESSING" {
		t.Fatalf("State = %q, want PROCESSING", got.State)
	}
	if got.LockedBy == nil || *got.LockedBy != "worker-1" {
		t.Fatalf("LockedBy = %v, want worker-1", got.LockedBy)
	}
	if got.LockedAt == nil {
		t.Fatal("LockedAt should be set")
	}

	claimed2, _, err := store.ClaimDocument(ctx, doc.ID, "worker-2")
	if err != nil {
		t.Fatalf("second ClaimDocument: %v", err)
	}
	if claimed2 {
		t.Fatal("expected second claim to fail")
	}
}

func TestClaimDocument_ClearsError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/claimerr")
	store.ClaimDocument(ctx, doc.ID, "w1")
	store.FailDocument(ctx, doc.ID, "CRAWLING", "boom", true, 3)

	claimed, got, _ := store.ClaimDocument(ctx, doc.ID, "w2")
	if !claimed {
		t.Fatal("expected re-claim to succeed for FAILED doc")
	}
	if got.Error != nil {
		t.Fatalf("Error should be nil after re-claim, got %v", got.Error)
	}
	if got.FailedStep != nil {
		t.Fatalf("FailedStep should be nil after re-claim, got %v", got.FailedStep)
	}
}

func TestAdvanceStep(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/advance")
	store.ClaimDocument(ctx, doc.ID, "w1")

	if err := store.AdvanceStep(ctx, doc.ID, "CHUNKING"); err != nil {
		t.Fatalf("AdvanceStep: %v", err)
	}
	got, _ := store.GetDocument(ctx, doc.ID)
	if got.Step != "CHUNKING" {
		t.Fatalf("Step = %q, want CHUNKING", got.Step)
	}
	if got.State != "PROCESSING" {
		t.Fatalf("State = %q, want PROCESSING (unchanged)", got.State)
	}
}

func TestCompleteDocument(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/complete")
	store.ClaimDocument(ctx, doc.ID, "w1")

	if err := store.CompleteDocument(ctx, doc.ID); err != nil {
		t.Fatalf("CompleteDocument: %v", err)
	}
	got, _ := store.GetDocument(ctx, doc.ID)
	if got.Step != "DONE" {
		t.Fatalf("Step = %q, want DONE", got.Step)
	}
	if got.State != "COMPLETED" {
		t.Fatalf("State = %q, want COMPLETED", got.State)
	}
	if got.LockedAt != nil || got.LockedBy != nil {
		t.Fatal("lock should be released")
	}
}

func TestFailDocument_Retriable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/failretry")
	store.ClaimDocument(ctx, doc.ID, "w1")

	if err := store.FailDocument(ctx, doc.ID, "CRAWLING", "timeout", true, 3); err != nil {
		t.Fatalf("FailDocument: %v", err)
	}
	got, _ := store.GetDocument(ctx, doc.ID)
	if got.State != "FAILED" {
		t.Fatalf("State = %q, want FAILED", got.State)
	}
	if got.FailedStep == nil || *got.FailedStep != "CRAWLING" {
		t.Fatalf("FailedStep = %v, want CRAWLING", got.FailedStep)
	}
	if !got.IsRetriable {
		t.Fatal("expected IsRetriable = true")
	}
	if got.RetryCount != 1 {
		t.Fatalf("RetryCount = %d, want 1", got.RetryCount)
	}
	if got.NextRetryAt == nil {
		t.Fatal("NextRetryAt should be set")
	}
	if got.LockedBy != nil || got.LockedAt != nil {
		t.Fatal("lock should be released on failure")
	}
}

func TestFailDocument_Permanent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/failperm")
	store.ClaimDocument(ctx, doc.ID, "w1")

	if err := store.FailDocument(ctx, doc.ID, "CRAWLING", "invalid URL", false, 3); err != nil {
		t.Fatalf("FailDocument: %v", err)
	}
	got, _ := store.GetDocument(ctx, doc.ID)
	if got.IsRetriable {
		t.Fatal("expected IsRetriable = false for permanent error")
	}
}

func TestFailDocument_MaxRetries(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/failmax")
	for i := 0; i < 3; i++ {
		store.ClaimDocument(ctx, doc.ID, "w1")
		store.FailDocument(ctx, doc.ID, "CRAWLING", "error", true, 3)
	}

	got, _ := store.GetDocument(ctx, doc.ID)
	if got.IsRetriable {
		t.Fatal("expected IsRetriable = false after max retries exhausted")
	}
	if got.RetryCount != 3 {
		t.Fatalf("RetryCount = %d, want 3", got.RetryCount)
	}
}

func TestAbandonStaleDocuments(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/abandon")
	store.ClaimDocument(ctx, doc.ID, "w1")

	_, _ = store.pool.Exec(ctx,
		`UPDATE documents SET locked_at = now() - interval '10 minutes' WHERE id = $1`, doc.ID)

	count, err := store.AbandonStaleDocuments(ctx, 5*time.Minute)
	if err != nil {
		t.Fatalf("AbandonStaleDocuments: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	got, _ := store.GetDocument(ctx, doc.ID)
	if got.State != "ABANDONED" {
		t.Fatalf("State = %q, want ABANDONED", got.State)
	}
	if got.LockedAt != nil || got.LockedBy != nil {
		t.Fatal("lock should be cleared on abandon")
	}
}

func TestGetPendingDocuments(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	queued, _ := store.InsertDocument(ctx, "https://example.com/queued")

	done, _ := store.InsertDocument(ctx, "https://example.com/done")
	store.ClaimDocument(ctx, done.ID, "w1")
	store.CompleteDocument(ctx, done.ID)

	abandoned, _ := store.InsertDocument(ctx, "https://example.com/abandoned")
	store.ClaimDocument(ctx, abandoned.ID, "w1")
	_, _ = store.pool.Exec(ctx,
		`UPDATE documents SET state = 'ABANDONED', locked_at = NULL, locked_by = NULL WHERE id = $1`,
		abandoned.ID)

	retriable, _ := store.InsertDocument(ctx, "https://example.com/retriable")
	store.ClaimDocument(ctx, retriable.ID, "w1")
	store.FailDocument(ctx, retriable.ID, "CRAWLING", "error", true, 3)
	_, _ = store.pool.Exec(ctx,
		`UPDATE documents SET next_retry_at = now() - interval '1 hour' WHERE id = $1`, retriable.ID)

	perm, _ := store.InsertDocument(ctx, "https://example.com/perm")
	store.ClaimDocument(ctx, perm.ID, "w1")
	store.FailDocument(ctx, perm.ID, "CRAWLING", "invalid", false, 3)
	_, _ = store.pool.Exec(ctx,
		`UPDATE documents SET next_retry_at = now() - interval '1 hour' WHERE id = $1`, perm.ID)

	docs, err := store.GetPendingDocuments(ctx, 3)
	if err != nil {
		t.Fatalf("GetPendingDocuments: %v", err)
	}

	ids := make(map[string]bool)
	for _, d := range docs {
		ids[d.ID] = true
	}

	if !ids[queued.ID] {
		t.Error("QUEUED doc should be included")
	}
	if ids[done.ID] {
		t.Error("COMPLETED doc should be excluded")
	}
	if !ids[abandoned.ID] {
		t.Error("ABANDONED doc should be included")
	}
	if !ids[retriable.ID] {
		t.Error("retriable FAILED doc should be included")
	}
	if ids[perm.ID] {
		t.Error("non-retriable FAILED doc should be excluded")
	}
}

func TestInsertChunks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/chunks")
	chunks, err := store.InsertChunks(ctx, doc.ID, 5)
	if err != nil {
		t.Fatalf("InsertChunks: %v", err)
	}
	if len(chunks) != 5 {
		t.Fatalf("len(chunks) = %d, want 5", len(chunks))
	}
	for i, c := range chunks {
		if c.ChunkIndex != i {
			t.Errorf("chunk[%d].ChunkIndex = %d", i, c.ChunkIndex)
		}
		if c.DocumentID != doc.ID {
			t.Errorf("chunk[%d].DocumentID = %s, want %s", i, c.DocumentID, doc.ID)
		}
		if c.Status != "PENDING" {
			t.Errorf("chunk[%d].Status = %q, want PENDING", i, c.Status)
		}
	}
}

func TestGetChunksByDocument(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/getchunks")
	_, _ = store.InsertChunks(ctx, doc.ID, 3)

	chunks, err := store.GetChunksByDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("GetChunksByDocument: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("len = %d, want 3", len(chunks))
	}
	for i, c := range chunks {
		if c.ChunkIndex != i {
			t.Errorf("chunk_index = %d, want %d", c.ChunkIndex, i)
		}
	}
}

func TestUpdateChunkStatus(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/chunkstatus")
	chunks, _ := store.InsertChunks(ctx, doc.ID, 1)

	if err := store.UpdateChunkStatus(ctx, chunks[0].ID, "EMBEDDED"); err != nil {
		t.Fatalf("UpdateChunkStatus: %v", err)
	}

	got, _ := store.GetChunksByDocument(ctx, doc.ID)
	if got[0].Status != "EMBEDDED" {
		t.Fatalf("Status = %q, want EMBEDDED", got[0].Status)
	}
}

func TestDeleteDocumentCascade(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/cascade")
	_, _ = store.InsertChunks(ctx, doc.ID, 3)

	if err := store.DeleteDocument(ctx, doc.ID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}

	chunks, err := store.GetChunksByDocument(ctx, doc.ID)
	if err != nil {
		t.Fatalf("GetChunksByDocument after delete: %v", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks after cascade delete, got %d", len(chunks))
	}
}

func TestListDocuments(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, _ = store.InsertDocument(ctx, "https://example.com/list/"+string(rune('a'+i)))
	}

	docs, total, err := store.ListDocuments(ctx, 2, 0)
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(docs) != 2 {
		t.Fatalf("len = %d, want 2", len(docs))
	}

	docs2, _, _ := store.ListDocuments(ctx, 2, 2)
	if len(docs2) != 2 {
		t.Fatalf("page 2 len = %d, want 2", len(docs2))
	}
}

func TestRunMigrations_Idempotent(t *testing.T) {
	store := newTestStore(t) // calls RunMigrations internally
	ctx := context.Background()

	// schema_migrations table should exist with recorded entries.
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count == 0 {
		t.Fatal("expected migrations to be recorded")
	}

	// Running again should be idempotent.
	if err := store.RunMigrations(ctx); err != nil {
		t.Fatalf("RunMigrations (idempotent): %v", err)
	}
}

func TestStatusCounts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// 1. QUEUED doc → should display as "PENDING"
	_, _ = store.InsertDocument(ctx, "https://example.com/sc-queued")

	// 2. PROCESSING doc at CRAWLING → should display as "CRAWLING"
	processing, _ := store.InsertDocument(ctx, "https://example.com/sc-processing")
	store.ClaimDocument(ctx, processing.ID, "w1")
	// ClaimDocument sets state=PROCESSING, step stays CRAWLING

	// 3. COMPLETED doc → should display as "DONE"
	completed, _ := store.InsertDocument(ctx, "https://example.com/sc-completed")
	store.ClaimDocument(ctx, completed.ID, "w1")
	store.CompleteDocument(ctx, completed.ID)

	// 4. Retriable FAILED doc at CRAWLING → should display as "CRAWLING"
	retriable, _ := store.InsertDocument(ctx, "https://example.com/sc-retriable")
	store.ClaimDocument(ctx, retriable.ID, "w1")
	store.FailDocument(ctx, retriable.ID, "CRAWLING", "timeout", true, 3)

	// 5. Non-retriable FAILED doc → should display as "ERROR"
	permanent, _ := store.InsertDocument(ctx, "https://example.com/sc-permanent")
	store.ClaimDocument(ctx, permanent.ID, "w1")
	store.FailDocument(ctx, permanent.ID, "CRAWLING", "bad url", false, 3)

	counts, _, err := store.StatusCounts(ctx)
	if err != nil {
		t.Fatalf("StatusCounts: %v", err)
	}

	// Expected:
	//   "PENDING"  = 1 (QUEUED doc)
	//   "CRAWLING" = 2 (PROCESSING doc + retriable FAILED doc)
	//   "DONE"     = 1 (COMPLETED doc)
	//   "ERROR"    = 1 (non-retriable FAILED doc)
	want := map[string]int{
		"PENDING":  1,
		"CRAWLING": 2,
		"DONE":     1,
		"ERROR":    1,
	}
	for status, wantCount := range want {
		if counts[status] != wantCount {
			t.Errorf("counts[%q] = %d, want %d", status, counts[status], wantCount)
		}
	}
	// Ensure no unexpected statuses.
	for status, count := range counts {
		if _, ok := want[status]; !ok {
			t.Errorf("unexpected status %q with count %d", status, count)
		}
	}
}

func TestGetDocumentsByIDs(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	d1, _ := store.InsertDocument(ctx, "https://example.com/multi1")
	d2, _ := store.InsertDocument(ctx, "https://example.com/multi2")
	_, _ = store.InsertDocument(ctx, "https://example.com/multi3")

	docs, err := store.GetDocumentsByIDs(ctx, []string{d1.ID, d2.ID})
	if err != nil {
		t.Fatalf("GetDocumentsByIDs: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("len = %d, want 2", len(docs))
	}

	ids := map[string]bool{docs[0].ID: true, docs[1].ID: true}
	if !ids[d1.ID] || !ids[d2.ID] {
		t.Fatalf("unexpected IDs: %v", ids)
	}
}

func TestListWikiDocuments(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()

	// Seed: two wiki docs in "main", one in "personal", one raw source.
	main1, err := pg.UpsertWikiDocument(ctx, "wiki://main/concepts/foo.md", "Foo", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	main2, err := pg.UpsertWikiDocument(ctx, "wiki://main/entities/bar.md", "Bar", "def456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.UpsertWikiDocument(ctx, "wiki://personal/x.md", "X", "xyz"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.InsertDocument(ctx, "https://example.com/raw"); err != nil {
		t.Fatal(err)
	}

	got, err := pg.ListWikiDocuments(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 wiki docs in main, got %d", len(got))
	}
	urls := map[string]string{}
	for _, d := range got {
		urls[d.URL] = d.ContentHash
	}
	if urls[main1.URL] != "abc123" || urls[main2.URL] != "def456" {
		t.Errorf("unexpected hashes: %+v", urls)
	}
}

func TestListWikiDocumentsLikeWildcardSafety(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()

	// Two wikis whose names differ by a character that's a LIKE wildcard.
	if _, err := pg.UpsertWikiDocument(ctx, "wiki://my_wiki/foo.md", "Foo", "h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.UpsertWikiDocument(ctx, "wiki://myXwiki/bar.md", "Bar", "h2"); err != nil {
		t.Fatal(err)
	}

	got, err := pg.ListWikiDocuments(ctx, "my_wiki")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 doc for my_wiki, got %d: %+v", len(got), got)
	}
	if got[0].URL != "wiki://my_wiki/foo.md" {
		t.Errorf("got %q want wiki://my_wiki/foo.md", got[0].URL)
	}
}

func TestUpsertWikiDocumentIdempotent(t *testing.T) {
	pg := newTestStore(t)
	ctx := context.Background()

	doc1, err := pg.UpsertWikiDocument(ctx, "wiki://main/concepts/foo.md", "Foo", "hash-v1")
	if err != nil {
		t.Fatal(err)
	}

	doc2, err := pg.UpsertWikiDocument(ctx, "wiki://main/concepts/foo.md", "Foo Updated", "hash-v2")
	if err != nil {
		t.Fatal(err)
	}
	if doc1.ID != doc2.ID {
		t.Errorf("expected same ID on upsert, got %s then %s", doc1.ID, doc2.ID)
	}
	if doc2.Title == nil || *doc2.Title != "Foo Updated" {
		t.Errorf("expected title 'Foo Updated', got %v", doc2.Title)
	}
	// content_hash is intentionally NOT updated on conflict; SetContentHash is the
	// authoritative final write at the end of the wiki ingest pipeline. The first
	// upsert set it to "hash-v1"; the second upsert leaves it as-is.
	if doc2.ContentHash != "hash-v1" {
		t.Errorf("content_hash should remain hash-v1 after second upsert; got %q", doc2.ContentHash)
	}
}
