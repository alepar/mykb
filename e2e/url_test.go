//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// TestURLIngestSearchDelete drives the URL ingest path end-to-end:
//
//  1. Ingest three small public URLs via `mykb ingest`.
//  2. Verify all three documents are in the API document list.
//  3. Run `mykb query` for keywords that appear in each, assert each URL is found.
//  4. Delete one URL via the API, query the same keyword, assert the deleted URL is absent.
//
// Uses real public URLs (per design): example.com, iana.org, httpbin.org/html.
// These are small, stable, and have well-known content.
func TestURLIngestSearchDelete(t *testing.T) {
	urls := []string{
		"https://example.com/",
		"https://www.iana.org/help/example-domains",
		"https://httpbin.org/html",
	}

	// Step 1: ingest each URL via the CLI. The CLI streams progress and
	// exits when the worker reaches DONE/ERROR, so when these calls return
	// the documents are fully indexed.
	for _, url := range urls {
		stdout, stderr, code := runMykb(t, "", "ingest", url)
		if code != 0 {
			t.Fatalf("ingest %q failed: exit %d\nstdout: %s\nstderr: %s", url, code, stdout, stderr)
		}
	}

	// Step 2: confirm each URL appears in ListDocuments.
	for _, url := range urls {
		if findDocumentByURL(t, url) == nil {
			t.Errorf("after ingest, document %q not found in ListDocuments", url)
		}
	}

	// Step 3: search for keywords specific to each URL and confirm presence.
	// example.com and iana.org/help/example-domains both contain the phrase
	// "example domain" prominently. httpbin.org/html serves a Moby Dick
	// excerpt.
	waitFor(t, 5*time.Second, func() bool {
		return queryAPIHasURL(t, "example domain", "https://example.com/")
	}, `query "example domain" should return https://example.com/`)

	waitFor(t, 5*time.Second, func() bool {
		return queryAPIHasURL(t, "example domain", "https://www.iana.org/help/example-domains")
	}, `query "example domain" should return https://www.iana.org/help/example-domains`)

	waitFor(t, 5*time.Second, func() bool {
		return queryAPIHasURL(t, "Moby Dick whale", "https://httpbin.org/html")
	}, `query "Moby Dick whale" should return https://httpbin.org/html`)

	// CLI query smoke check: the user-facing path should also show one of these.
	if !queryHasURL(t, "example domain", "https://example.com/") {
		t.Errorf("CLI `mykb query` did not contain https://example.com/ in stdout")
	}

	// Step 4: delete one URL via API, confirm it's no longer searchable.
	target := "https://httpbin.org/html"
	deleteDocumentByURL(t, target)

	waitFor(t, 5*time.Second, func() bool {
		return !queryAPIHasURL(t, "Moby Dick whale", target)
	}, `after delete, query "Moby Dick whale" should not return https://httpbin.org/html`)

	// And the other URLs should still be searchable.
	if !queryAPIHasURL(t, "example domain", "https://example.com/") {
		t.Errorf("after deleting unrelated URL, https://example.com/ unexpectedly disappeared")
	}
}
