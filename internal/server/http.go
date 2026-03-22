package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"mykb/internal/storage"
)

// pgForHTTP is the subset of PostgresStore used by the HTTP handler.
type pgForHTTP interface {
	InsertDocument(ctx context.Context, url string) (storage.Document, error)
	GetDocument(ctx context.Context, id string) (storage.Document, error)
	GetDocumentByURL(ctx context.Context, url string) (storage.Document, error)
}

// workerForHTTP is the subset of Worker used by the HTTP handler.
type workerForHTTP interface {
	Notify(documentID string)
}

// NewHTTPHandler returns an http.Handler with all HTTP API routes registered.
func NewHTTPHandler(pg pgForHTTP, w workerForHTTP) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/ingest", handleIngest(pg, w))
	mux.HandleFunc("OPTIONS /api/ingest", handleCORSPreflight)
	mux.HandleFunc("GET /api/ingest/{id}", handleIngestStatus(pg))
	mux.HandleFunc("OPTIONS /api/ingest/{id}", handleCORSPreflight)
	return mux
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

func handleCORSPreflight(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.WriteHeader(http.StatusNoContent)
}

type ingestRequest struct {
	URL   string `json:"url"`
	Force bool   `json:"force"`
}

type ingestResponse struct {
	ID string `json:"id"`
}

func handleIngest(pg pgForHTTP, w workerForHTTP) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		setCORSHeaders(rw)

		var req ingestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(rw, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			http.Error(rw, "url is required", http.StatusBadRequest)
			return
		}

		doc, err := pg.InsertDocument(r.Context(), req.URL)
		if err != nil {
			if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
				existing, lookupErr := pg.GetDocumentByURL(r.Context(), req.URL)
				if lookupErr != nil {
					http.Error(rw, "conflict: url already ingested", http.StatusConflict)
					return
				}
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(rw).Encode(ingestResponse{ID: existing.ID})
				return
			}
			http.Error(rw, "internal error", http.StatusInternalServerError)
			return
		}

		w.Notify(doc.ID)

		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(rw).Encode(ingestResponse{ID: doc.ID})
	}
}

type ingestStatusResponse struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Error  *string `json:"error"`
}

func handleIngestStatus(pg pgForHTTP) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		setCORSHeaders(rw)

		id := r.PathValue("id")
		doc, err := pg.GetDocument(r.Context(), id)
		if err != nil {
			http.Error(rw, "not found", http.StatusNotFound)
			return
		}

		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(ingestStatusResponse{
			ID:     doc.ID,
			Status: doc.Status,
			Error:  doc.Error,
		})
	}
}
