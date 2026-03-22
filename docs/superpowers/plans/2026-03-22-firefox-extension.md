# Firefox Ingest Extension Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an HTTP API to mykb-api for ingestion, and a Firefox extension that uses it to ingest the current page with one click.

**Architecture:** HTTP server on port 9091 alongside the existing gRPC server on 9090. Two endpoints: POST to start ingestion, GET to poll status. Firefox WebExtension (Manifest V2) calls the API and shows progress via toolbar badge.

**Tech Stack:** Go `net/http`, Firefox WebExtension API (Manifest V2), JavaScript

**Spec:** `docs/superpowers/specs/2026-03-21-firefox-extension-design.md`

---

## Chunk 1: HTTP API Server

### Task 1: Add HTTP port config

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add HTTPPort field to Config struct**

Add after the `GRPCPort` field:

```go
HTTPPort string
```

- [ ] **Step 2: Load HTTPPort in Load()**

Add after the GRPCPort line:

```go
HTTPPort: envOr("HTTP_PORT", "9091"),
```

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "feat: add HTTP_PORT config for HTTP API server"
```

### Task 2: HTTP handlers

**Files:**
- Create: `internal/server/http.go`
- Create: `internal/server/http_test.go`

- [ ] **Step 1: Write tests for HTTP handlers**

Create `internal/server/http_test.go`:

```go
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mykb/internal/storage"
)

// mockPG implements the postgres methods needed by HTTP handlers.
type mockPG struct {
	docs     map[string]storage.Document
	inserted *storage.Document
	insertErr error
}

func (m *mockPG) InsertDocument(ctx context.Context, url string) (storage.Document, error) {
	if m.insertErr != nil {
		return storage.Document{}, m.insertErr
	}
	doc := storage.Document{
		ID:        "test-doc-id",
		URL:       url,
		Status:    "PENDING",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	m.inserted = &doc
	return doc, nil
}

func (m *mockPG) GetDocument(ctx context.Context, id string) (storage.Document, error) {
	if doc, ok := m.docs[id]; ok {
		return doc, nil
	}
	return storage.Document{}, fmt.Errorf("not found")
}

func (m *mockPG) GetDocumentByURL(ctx context.Context, url string) (storage.Document, error) {
	for _, doc := range m.docs {
		if doc.URL == url {
			return doc, nil
		}
	}
	return storage.Document{}, fmt.Errorf("not found")
}

// mockWorker implements the worker method needed.
type mockWorker struct {
	notified string
}

func (m *mockWorker) Notify(documentID string) {
	m.notified = documentID
}

func TestHandleIngest(t *testing.T) {
	pg := &mockPG{}
	w := &mockWorker{}
	h := NewHTTPHandler(pg, w)

	body := `{"url": "https://example.com"}`
	req := httptest.NewRequest("POST", "/api/ingest", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}

	var resp ingestResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.ID != "test-doc-id" {
		t.Errorf("got id %q, want test-doc-id", resp.ID)
	}
	if w.notified != "test-doc-id" {
		t.Errorf("worker not notified, got %q", w.notified)
	}
	// Check CORS header
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("missing CORS header")
	}
}

func TestHandleIngest_Duplicate(t *testing.T) {
	pg := &mockPG{
		insertErr: fmt.Errorf("unique constraint violation"),
		docs: map[string]storage.Document{
			"existing-id": {ID: "existing-id", URL: "https://example.com", Status: "DONE"},
		},
	}
	w := &mockWorker{}
	h := NewHTTPHandler(pg, w)

	body := `{"url": "https://example.com"}`
	req := httptest.NewRequest("POST", "/api/ingest", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("got status %d, want 409", rr.Code)
	}
}

func TestHandleIngestStatus(t *testing.T) {
	errMsg := "crawl failed"
	pg := &mockPG{
		docs: map[string]storage.Document{
			"doc-1": {ID: "doc-1", Status: "CRAWLING", Error: nil},
			"doc-2": {ID: "doc-2", Status: "CRAWLING", Error: &errMsg},
		},
	}
	h := NewHTTPHandler(pg, &mockWorker{})

	// Test normal status
	req := httptest.NewRequest("GET", "/api/ingest/doc-1", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rr.Code)
	}
	var resp statusResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Status != "CRAWLING" {
		t.Errorf("got status %q", resp.Status)
	}
	if resp.Error != nil {
		t.Errorf("expected nil error, got %q", *resp.Error)
	}

	// Test status with error
	req = httptest.NewRequest("GET", "/api/ingest/doc-2", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Error == nil || *resp.Error != "crawl failed" {
		t.Errorf("expected error message")
	}

	// Test not found
	req = httptest.NewRequest("GET", "/api/ingest/nonexistent", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want 404", rr.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/server/ -run TestHandle -v
```
Expected: FAIL — types not defined.

- [ ] **Step 3: Implement HTTP handlers**

Create `internal/server/http.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// pgForHTTP defines the postgres methods needed by HTTP handlers.
type pgForHTTP interface {
	InsertDocument(ctx context.Context, url string) (storage.Document, error)
	GetDocument(ctx context.Context, id string) (storage.Document, error)
	GetDocumentByURL(ctx context.Context, url string) (storage.Document, error)
}

// workerForHTTP defines the worker methods needed by HTTP handlers.
type workerForHTTP interface {
	Notify(documentID string)
}

type ingestRequest struct {
	URL   string `json:"url"`
	Force bool   `json:"force"`
}

type ingestResponse struct {
	ID string `json:"id"`
}

type statusResponse struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Error  *string `json:"error"`
}

// NewHTTPHandler creates an http.Handler with the ingestion API routes.
func NewHTTPHandler(pg pgForHTTP, w workerForHTTP) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/ingest", func(rw http.ResponseWriter, r *http.Request) {
		handleIngest(rw, r, pg, w)
	})
	mux.HandleFunc("GET /api/ingest/{id}", func(rw http.ResponseWriter, r *http.Request) {
		handleIngestStatus(rw, r, pg)
	})
	// CORS preflight
	mux.HandleFunc("OPTIONS /api/ingest", corsHandler)
	mux.HandleFunc("OPTIONS /api/ingest/{id}", corsHandler)
	return mux
}

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func corsHandler(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

func jsonResponse(w http.ResponseWriter, status int, v any) {
	corsHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func handleIngest(w http.ResponseWriter, r *http.Request, pg pgForHTTP, wk workerForHTTP) {
	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.URL == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}

	doc, err := pg.InsertDocument(r.Context(), req.URL)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			existing, lookupErr := pg.GetDocumentByURL(r.Context(), req.URL)
			if lookupErr == nil {
				jsonResponse(w, http.StatusConflict, ingestResponse{ID: existing.ID})
				return
			}
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("insert: %v", err)})
		return
	}

	wk.Notify(doc.ID)
	jsonResponse(w, http.StatusOK, ingestResponse{ID: doc.ID})
}

func handleIngestStatus(w http.ResponseWriter, r *http.Request, pg pgForHTTP) {
	id := r.PathValue("id")
	doc, err := pg.GetDocument(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	jsonResponse(w, http.StatusOK, statusResponse{
		ID:     doc.ID,
		Status: doc.Status,
		Error:  doc.Error,
	})
}
```

Note: The `pgForHTTP` interface needs `"mykb/internal/storage"` imported. Add to the import block:

```go
"mykb/internal/storage"
```

- [ ] **Step 4: Add `"fmt"` import to http_test.go**

The test file uses `fmt.Errorf` — add `"fmt"` to the imports.

- [ ] **Step 5: Run tests**

```bash
go test ./internal/server/ -run TestHandle -v
```
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/server/http.go internal/server/http_test.go
git commit -m "feat: add HTTP API handlers for ingestion and status polling"
```

### Task 3: Start HTTP server in main

**Files:**
- Modify: `cmd/mykb-api/main.go`
- Modify: `docker-compose.yml`

- [ ] **Step 1: Add HTTP server startup to main.go**

After the gRPC server setup (after `reflection.Register(grpcServer)`) and before the graceful shutdown goroutine, add:

```go
// HTTP API server
httpHandler := server.NewHTTPHandler(pg, w)
httpServer := &http.Server{
    Addr:    ":" + cfg.HTTPPort,
    Handler: httpHandler,
}
go func() {
    log.Printf("HTTP API server listening on :%s", cfg.HTTPPort)
    if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
        log.Fatalf("http: %v", err)
    }
}()
```

Add `"net/http"` to imports.

Update the graceful shutdown goroutine to also shut down the HTTP server:

```go
go func() {
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
    <-sigCh
    log.Println("shutting down...")
    grpcServer.GracefulStop()
    httpServer.Shutdown(context.Background())
    cancel()
}()
```

- [ ] **Step 2: Expose port 9091 in docker-compose.yml**

Add to the `mykb` service, after the existing ports line:

```yaml
      - "9091:9091"
```

- [ ] **Step 3: Verify it builds**

```bash
go build ./cmd/mykb-api/
```
Expected: Success

- [ ] **Step 4: Commit**

```bash
git add cmd/mykb-api/main.go docker-compose.yml
git commit -m "feat: start HTTP API server on port 9091 alongside gRPC"
```

### Task 4: Manual test with curl

- [ ] **Step 1: Rebuild and restart the mykb service**

```bash
docker compose up -d --build mykb
```

- [ ] **Step 2: Test POST endpoint**

```bash
curl -s -X POST http://localhost:9091/api/ingest \
  -H 'Content-Type: application/json' \
  -d '{"url": "https://example.com"}' | jq .
```
Expected: `{"id": "<uuid>"}`

- [ ] **Step 3: Test GET status endpoint**

```bash
curl -s http://localhost:9091/api/ingest/<id-from-step-2> | jq .
```
Expected: `{"id": "...", "status": "CRAWLING|DONE|...", "error": null}`

- [ ] **Step 4: Test duplicate URL (409)**

```bash
curl -s -w "\n%{http_code}\n" -X POST http://localhost:9091/api/ingest \
  -H 'Content-Type: application/json' \
  -d '{"url": "https://example.com"}'
```
Expected: 409 with existing document ID

## Chunk 2: Firefox Extension

### Task 5: Extension manifest and background script

**Files:**
- Create: `extension/manifest.json`
- Create: `extension/background.js`

- [ ] **Step 1: Create manifest.json**

Create `extension/manifest.json`:

```json
{
  "manifest_version": 2,
  "name": "MyKB Ingest",
  "version": "1.0",
  "description": "Ingest the current page into MyKB",
  "permissions": [
    "activeTab",
    "storage",
    "<all_urls>"
  ],
  "browser_action": {
    "default_icon": {
      "48": "icons/icon-48.png",
      "96": "icons/icon-96.png"
    },
    "default_title": "Ingest into MyKB"
  },
  "background": {
    "scripts": ["background.js"]
  },
  "options_ui": {
    "page": "options.html"
  },
  "browser_specific_settings": {
    "gecko": {
      "id": "mykb-ingest@alepar",
      "strict_min_version": "109.0"
    },
    "gecko_android": {}
  }
}
```

- [ ] **Step 2: Create background.js**

Create `extension/background.js`:

```javascript
const DEFAULT_SERVER = "http://localhost:9091";

async function getServer() {
  const result = await browser.storage.local.get("server");
  return result.server || DEFAULT_SERVER;
}

function setBadge(text, color) {
  browser.browserAction.setBadgeText({ text });
  browser.browserAction.setBadgeBackgroundColor({ color });
}

function clearBadge() {
  setBadge("", "#000");
}

async function pollStatus(server, docId, tabId) {
  const startTime = Date.now();
  const TIMEOUT = 5 * 60 * 1000; // 5 minutes
  const INTERVAL = 2000; // 2 seconds

  const poll = async () => {
    if (Date.now() - startTime > TIMEOUT) {
      setBadge("!", "#ff0000");
      setTimeout(clearBadge, 5000);
      return;
    }

    try {
      const resp = await fetch(`${server}/api/ingest/${docId}`);
      const data = await resp.json();

      if (data.error) {
        setBadge("!", "#ff0000");
        setTimeout(clearBadge, 5000);
        return;
      }

      switch (data.status) {
        case "DONE":
          setBadge("\u2713", "#00aa00");
          setTimeout(clearBadge, 3000);
          return;
        case "CRAWLING":
          setBadge("CRL", "#ddaa00");
          break;
        case "CHUNKING":
          setBadge("CHK", "#ddaa00");
          break;
        case "EMBEDDING":
          setBadge("EMB", "#ddaa00");
          break;
        case "INDEXING":
          setBadge("IDX", "#ddaa00");
          break;
        default:
          setBadge("...", "#ddaa00");
      }

      setTimeout(poll, INTERVAL);
    } catch (e) {
      setBadge("!", "#ff0000");
      setTimeout(clearBadge, 5000);
    }
  };

  poll();
}

browser.browserAction.onClicked.addListener(async (tab) => {
  if (!tab.url || tab.url.startsWith("about:") || tab.url.startsWith("moz-extension:")) {
    setBadge("!", "#ff0000");
    setTimeout(clearBadge, 3000);
    return;
  }

  const server = await getServer();
  setBadge("...", "#ddaa00");

  try {
    const resp = await fetch(`${server}/api/ingest`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ url: tab.url }),
    });

    if (resp.status === 409) {
      setBadge("dup", "#4488ff");
      setTimeout(clearBadge, 3000);
      return;
    }

    if (!resp.ok) {
      setBadge("!", "#ff0000");
      setTimeout(clearBadge, 5000);
      return;
    }

    const data = await resp.json();
    pollStatus(server, data.id, tab.id);
  } catch (e) {
    setBadge("ERR", "#ff0000");
    setTimeout(clearBadge, 5000);
  }
});
```

- [ ] **Step 3: Commit**

```bash
git add extension/manifest.json extension/background.js
git commit -m "feat: add Firefox extension with background script for ingestion"
```

### Task 6: Options page

**Files:**
- Create: `extension/options.html`
- Create: `extension/options.js`

- [ ] **Step 1: Create options.html**

Create `extension/options.html`:

```html
<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <style>
    body { font-family: system-ui; padding: 16px; max-width: 400px; }
    label { display: block; margin-bottom: 4px; font-weight: bold; }
    input { width: 100%; padding: 8px; box-sizing: border-box; }
    button { margin-top: 8px; padding: 8px 16px; }
    .status { margin-top: 8px; color: #00aa00; }
  </style>
</head>
<body>
  <label for="server">Server address:</label>
  <input type="text" id="server" placeholder="http://localhost:9091">
  <button id="save">Save</button>
  <div id="status" class="status"></div>
  <script src="options.js"></script>
</body>
</html>
```

- [ ] **Step 2: Create options.js**

Create `extension/options.js`:

```javascript
const serverInput = document.getElementById("server");
const saveButton = document.getElementById("save");
const statusDiv = document.getElementById("status");

browser.storage.local.get("server").then((result) => {
  serverInput.value = result.server || "http://localhost:9091";
});

saveButton.addEventListener("click", () => {
  const server = serverInput.value.replace(/\/+$/, "");
  browser.storage.local.set({ server }).then(() => {
    statusDiv.textContent = "Saved!";
    setTimeout(() => { statusDiv.textContent = ""; }, 2000);
  });
});
```

- [ ] **Step 3: Commit**

```bash
git add extension/options.html extension/options.js
git commit -m "feat: add options page for server address configuration"
```

### Task 7: Icons

**Files:**
- Create: `extension/icons/icon-48.png`
- Create: `extension/icons/icon-96.png`

- [ ] **Step 1: Generate simple placeholder icons**

Create simple colored square icons using ImageMagick (or any available tool):

```bash
mkdir -p extension/icons
convert -size 48x48 xc:'#4488ff' -fill white -gravity center -pointsize 24 -annotate 0 'KB' extension/icons/icon-48.png
convert -size 96x96 xc:'#4488ff' -fill white -gravity center -pointsize 48 -annotate 0 'KB' extension/icons/icon-96.png
```

If ImageMagick is not available, create any 48x48 and 96x96 PNG files as placeholders.

- [ ] **Step 2: Commit**

```bash
git add extension/icons/
git commit -m "feat: add extension icons"
```

### Task 8: Manual test of extension

- [ ] **Step 1: Load the extension in Firefox**

Open `about:debugging#/runtime/this-firefox`, click "Load Temporary Add-on", select `extension/manifest.json`.

- [ ] **Step 2: Navigate to any web page and click the toolbar icon**

Expected: Badge shows "..." → "CRL" → "CHK" → "EMB" → "IDX" → checkmark → clears.

- [ ] **Step 3: Click again on the same page**

Expected: Badge shows "dup" briefly, then clears (409 from server).

- [ ] **Step 4: Open extension options and verify settings page works**

Change server address, save, verify it persists.
