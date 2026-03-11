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
	if doc.Status != "PENDING" {
		t.Fatalf("Status = %q, want PENDING", doc.Status)
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

func TestUpdateDocumentStatus(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/status")
	if err := store.UpdateDocumentStatus(ctx, doc.ID, "CRAWLING"); err != nil {
		t.Fatalf("UpdateDocumentStatus: %v", err)
	}
	got, _ := store.GetDocument(ctx, doc.ID)
	if got.Status != "CRAWLING" {
		t.Fatalf("Status = %q, want CRAWLING", got.Status)
	}
}

func TestSetDocumentError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/err")
	if err := store.SetDocumentError(ctx, doc.ID, "boom"); err != nil {
		t.Fatalf("SetDocumentError: %v", err)
	}

	got, _ := store.GetDocument(ctx, doc.ID)
	if got.Error == nil || *got.Error != "boom" {
		t.Fatalf("Error = %v, want 'boom'", got.Error)
	}
	if got.RetryCount != 1 {
		t.Fatalf("RetryCount = %d, want 1", got.RetryCount)
	}
	if got.NextRetryAt == nil {
		t.Fatal("NextRetryAt should be set")
	}
	if !got.NextRetryAt.After(time.Now().Add(-time.Second)) {
		t.Fatal("NextRetryAt should be in the future (or very recent)")
	}
}

func TestClearDocumentError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	doc, _ := store.InsertDocument(ctx, "https://example.com/clr")
	_ = store.SetDocumentError(ctx, doc.ID, "fail")
	if err := store.ClearDocumentError(ctx, doc.ID); err != nil {
		t.Fatalf("ClearDocumentError: %v", err)
	}
	got, _ := store.GetDocument(ctx, doc.ID)
	if got.Error != nil {
		t.Fatalf("Error = %v, want nil", got.Error)
	}
}

func TestGetPendingDocuments(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Pending doc (no error).
	pending, _ := store.InsertDocument(ctx, "https://example.com/pending")
	_ = pending // used by assertion below

	// Done doc should be excluded.
	done, _ := store.InsertDocument(ctx, "https://example.com/done")
	_ = store.UpdateDocumentStatus(ctx, done.ID, "DONE")

	// Doc with error but retry eligible (next_retry_at in past).
	retryable, _ := store.InsertDocument(ctx, "https://example.com/retryable")
	_ = store.SetDocumentError(ctx, retryable.ID, "transient")
	// Force next_retry_at to past so it's eligible.
	_, _ = store.pool.Exec(ctx,
		`UPDATE documents SET next_retry_at = now() - interval '1 hour' WHERE id = $1`,
		retryable.ID)

	// Doc with error over max retries.
	overMax, _ := store.InsertDocument(ctx, "https://example.com/overmax")
	for i := 0; i < 5; i++ {
		_ = store.SetDocumentError(ctx, overMax.ID, "fail")
	}
	// Force next_retry_at to past.
	_, _ = store.pool.Exec(ctx,
		`UPDATE documents SET next_retry_at = now() - interval '1 hour' WHERE id = $1`,
		overMax.ID)

	docs, err := store.GetPendingDocuments(ctx, 3)
	if err != nil {
		t.Fatalf("GetPendingDocuments: %v", err)
	}

	ids := make(map[string]bool)
	for _, d := range docs {
		ids[d.ID] = true
	}

	if !ids[pending.ID] {
		t.Error("expected pending doc in results")
	}
	if ids[done.ID] {
		t.Error("DONE doc should be excluded")
	}
	if !ids[retryable.ID] {
		t.Error("retryable doc should be included")
	}
	if ids[overMax.ID] {
		t.Error("over-max-retries doc should be excluded")
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
