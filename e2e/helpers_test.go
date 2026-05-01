//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mykbv1 "mykb/gen/mykb/v1"
	"mykb/gen/mykb/v1/mykbv1connect"
)

// fakeHomeOnce caches the temp HOME directory used to make CLI invocations
// auto-target the e2e API at localhost:9092 (via a synthetic ~/.mykb.conf).
// Lazily created on first runMykb call.
var fakeHomeDir string

// fakeHome returns a process-global temp HOME directory containing a
// .mykb.conf that points the CLI at the e2e API. Created on first use.
func fakeHome(t *testing.T) string {
	t.Helper()
	if fakeHomeDir != "" {
		return fakeHomeDir
	}
	dir, err := os.MkdirTemp("", "mykb-e2e-home-")
	if err != nil {
		t.Fatalf("mktempdir for fake HOME: %v", err)
	}
	cfg := fmt.Sprintf("host = %q\n", apiHost)
	if err := os.WriteFile(filepath.Join(dir, ".mykb.conf"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write fake .mykb.conf: %v", err)
	}
	fakeHomeDir = dir
	return dir
}

// runMykb execs the CLI with the given args and returns stdout, stderr, and
// the exit code. HOME is overridden to a temp dir whose .mykb.conf points at
// the e2e API, so commands that accept --host pick up localhost:9092 by default.
//
// If `dir` is non-empty, it becomes the working directory of the invocation —
// useful for `mykb wiki sync` which discovers the vault from cwd.
func runMykb(t *testing.T, dir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	home := fakeHome(t)

	cmd := exec.Command(mykbBin, args...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if dir != "" {
		cmd.Dir = dir
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()
	if err == nil {
		return stdout, stderr, 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout, stderr, exitErr.ExitCode()
	}
	t.Fatalf("runMykb %v: unexpected error: %v\nstdout: %s\nstderr: %s", args, err, stdout, stderr)
	return
}

// connectClient returns a fresh ConnectRPC client pointed at the e2e API.
// Used for direct API assertions (delete, list, query state checks) where
// CLI output parsing is too fragile.
func connectClient() mykbv1connect.KBServiceClient {
	return mykbv1connect.NewKBServiceClient(http.DefaultClient, apiHost)
}

// listAllDocuments returns every document in the e2e store. Pages through
// ListDocuments if there are more than `limit`.
func listAllDocuments(t *testing.T) []*mykbv1.Document {
	t.Helper()
	c := connectClient()
	resp, err := c.ListDocuments(context.Background(), connect.NewRequest(&mykbv1.ListDocumentsRequest{
		Limit:  1000,
		Offset: 0,
	}))
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	return resp.Msg.GetDocuments()
}

// findDocumentByURL returns the document with the given URL, or nil if absent.
func findDocumentByURL(t *testing.T, url string) *mykbv1.Document {
	t.Helper()
	for _, d := range listAllDocuments(t) {
		if d.GetUrl() == url {
			return d
		}
	}
	return nil
}

// queryHasURL runs `mykb query` for `q` and returns true if the URL appears
// in the CLI's stdout. The query CLI prints results with their URLs, so a
// substring match is sufficient.
func queryHasURL(t *testing.T, q, url string) bool {
	t.Helper()
	stdout, stderr, code := runMykb(t, "", "query", q)
	if code != 0 {
		t.Fatalf("query %q failed: exit %d\nstdout: %s\nstderr: %s", q, code, stdout, stderr)
	}
	return strings.Contains(stdout, url)
}

// queryAPIHasURL hits the Query RPC directly and returns true if the URL
// appears as the document_id->URL of any chunk in the result. More precise
// than queryHasURL when the CLI's output formatting could omit URLs.
func queryAPIHasURL(t *testing.T, q, url string) bool {
	t.Helper()
	c := connectClient()
	resp, err := c.Query(context.Background(), connect.NewRequest(&mykbv1.QueryRequest{
		Query: q,
		TopK:  20,
	}))
	if err != nil {
		t.Fatalf("Query RPC: %v", err)
	}
	docIDs := map[string]bool{}
	for _, r := range resp.Msg.GetResults() {
		docIDs[r.GetDocumentId()] = true
	}
	if len(docIDs) == 0 {
		return false
	}
	// Map document_ids to URLs by listing all documents.
	for _, d := range listAllDocuments(t) {
		if docIDs[d.GetId()] && d.GetUrl() == url {
			return true
		}
	}
	return false
}

// deleteDocumentByURL looks up the document by URL and issues a DeleteDocument
// RPC. Used after CLI ingest to remove a doc and verify it's gone from search.
func deleteDocumentByURL(t *testing.T, url string) {
	t.Helper()
	doc := findDocumentByURL(t, url)
	if doc == nil {
		t.Fatalf("delete: document %q not found", url)
	}
	c := connectClient()
	if _, err := c.DeleteDocument(context.Background(), connect.NewRequest(&mykbv1.DeleteDocumentRequest{
		Id: doc.GetId(),
	})); err != nil {
		t.Fatalf("DeleteDocument %s: %v", url, err)
	}
}

// tempVault scaffolds a brand-new wiki vault via `mykb wiki init` and returns
// the path. Caller writes pages into entities/, concepts/, synthesis/ etc.
func tempVault(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	stdout, stderr, code := runMykb(t, "", "wiki", "init", "--vault", dir, "--name", name)
	if code != 0 {
		t.Fatalf("wiki init failed: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	return dir
}

// writeFile writes content to a file relative to the vault root, creating any
// missing parent directories.
func writeFile(t *testing.T, vaultRoot, relPath, content string) {
	t.Helper()
	full := filepath.Join(vaultRoot, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// waitFor polls `predicate` every 200ms up to `timeout` until it returns true.
// Used to give async indexing (e.g. Meilisearch background tasks) time to settle
// before querying.
func waitFor(t *testing.T, timeout time.Duration, predicate func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("waitFor timed out after %s: %s", timeout, msg)
}
