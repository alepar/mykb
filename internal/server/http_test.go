package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"mykb/internal/storage"
)

// mockPG is a test double for pgForHTTP.
type mockPG struct {
	insertDoc    storage.Document
	insertErr    error
	getDoc       storage.Document
	getErr       error
	getByURLDoc  storage.Document
	getByURLErr  error
}

func (m *mockPG) InsertDocument(_ context.Context, _ string) (storage.Document, error) {
	return m.insertDoc, m.insertErr
}

func (m *mockPG) GetDocument(_ context.Context, _ string) (storage.Document, error) {
	return m.getDoc, m.getErr
}

func (m *mockPG) GetDocumentByURL(_ context.Context, _ string) (storage.Document, error) {
	return m.getByURLDoc, m.getByURLErr
}

func (m *mockPG) DeleteDocument(_ context.Context, _ string) error { return nil }

func (m *mockPG) StatusCounts(_ context.Context) (map[string]int, int, error) {
	return nil, 0, nil
}

// mockWorker is a test double for workerForHTTP.
type mockWorker struct {
	notifiedIDs []string
}

func (m *mockWorker) Notify(documentID string) {
	m.notifiedIDs = append(m.notifiedIDs, documentID)
}

// mockFS is a test double for fsForHTTP.
type mockFS struct{}

func (m *mockFS) WritePrefetchHTML(_ string, _ []byte) error { return nil }

func TestHandleIngest(t *testing.T) {
	pg := &mockPG{
		insertDoc: storage.Document{ID: "doc-123", Step: "CRAWLING", State: "QUEUED"},
	}
	w := &mockWorker{}
	handler := NewHTTPHandler(pg, w, &mockFS{})

	body, _ := json.Marshal(map[string]interface{}{"url": "https://example.com"})
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", rec.Code)
	}

	var resp ingestResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID != "doc-123" {
		t.Errorf("expected ID doc-123, got %q", resp.ID)
	}

	if len(w.notifiedIDs) != 1 || w.notifiedIDs[0] != "doc-123" {
		t.Errorf("expected worker notified with doc-123, got %v", w.notifiedIDs)
	}

	if cors := rec.Header().Get("Access-Control-Allow-Origin"); cors != "*" {
		t.Errorf("expected CORS header *, got %q", cors)
	}
}

func TestHandleIngest_Duplicate(t *testing.T) {
	existingDoc := storage.Document{ID: "existing-456", Step: "DONE", State: "COMPLETED"}
	pg := &mockPG{
		insertErr:   fmt.Errorf("insert document: ERROR: duplicate key value violates unique constraint"),
		getByURLDoc: existingDoc,
	}
	w := &mockWorker{}
	handler := NewHTTPHandler(pg, w, &mockFS{})

	body, _ := json.Marshal(map[string]interface{}{"url": "https://example.com/existing"})
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d", rec.Code)
	}

	var resp ingestResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID != "existing-456" {
		t.Errorf("expected ID existing-456, got %q", resp.ID)
	}

	if len(w.notifiedIDs) != 0 {
		t.Errorf("expected no worker notifications for duplicate, got %v", w.notifiedIDs)
	}
}

func TestHandleIngestStatus(t *testing.T) {
	t.Run("normal status", func(t *testing.T) {
		pg := &mockPG{
			getDoc: storage.Document{ID: "doc-789", Step: "EMBEDDING", State: "PROCESSING"},
		}
		handler := NewHTTPHandler(pg, &mockWorker{}, &mockFS{})

		req := httptest.NewRequest(http.MethodGet, "/api/ingest/doc-789", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}

		var resp ingestStatusResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.ID != "doc-789" {
			t.Errorf("expected ID doc-789, got %q", resp.ID)
		}
		if resp.Status != "EMBEDDING" {
			t.Errorf("expected status EMBEDDING, got %q", resp.Status)
		}
		if resp.Error != nil {
			t.Errorf("expected nil error, got %v", resp.Error)
		}
	})

	t.Run("status with error", func(t *testing.T) {
		errMsg := "crawl failed: timeout"
		pg := &mockPG{
			getDoc: storage.Document{ID: "doc-err", Step: "CRAWLING", State: "FAILED", Error: &errMsg},
		}
		handler := NewHTTPHandler(pg, &mockWorker{}, &mockFS{})

		req := httptest.NewRequest(http.MethodGet, "/api/ingest/doc-err", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}

		var resp ingestStatusResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.Error == nil {
			t.Fatal("expected non-nil error")
		}
		if *resp.Error != errMsg {
			t.Errorf("expected error %q, got %q", errMsg, *resp.Error)
		}
	})

	t.Run("not found returns 404", func(t *testing.T) {
		pg := &mockPG{
			getErr: fmt.Errorf("get document: no rows in result set"),
		}
		handler := NewHTTPHandler(pg, &mockWorker{}, &mockFS{})

		req := httptest.NewRequest(http.MethodGet, "/api/ingest/nonexistent", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", rec.Code)
		}
	})
}
