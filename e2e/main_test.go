//go:build e2e

// Package e2e contains end-to-end tests that drive the mykb CLI against a
// freshly-spun-up parallel docker-compose stack. Run with:
//
//	go test -tags=e2e -timeout=10m -count=1 ./e2e/...
//
// or via `just e2e`.
//
// TestMain handles stack lifecycle: builds the mykb CLI, brings up
// docker-compose.e2e.yml under project name "mykb-e2e", waits for /healthz,
// runs the suite, then tears the stack down with `down -v` (deletes volumes
// for a clean slate next run).
package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const (
	composeProject = "mykb-e2e"
	composeFile    = "docker-compose.e2e.yml"
	apiHost        = "http://localhost:9092"
	healthURL      = apiHost + "/healthz"
	readyTimeout   = 90 * time.Second
)

// repoRoot is the absolute path to the repo root, set by TestMain.
var repoRoot string

// mykbBin is the absolute path to the freshly-built CLI, set by TestMain.
var mykbBin string

func TestMain(m *testing.M) {
	if os.Getenv("VOYAGE_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "e2e: VOYAGE_API_KEY not set; skipping suite (set it via .env or shell)")
		os.Exit(0)
	}

	root, err := findRepoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: cannot find repo root:", err)
		os.Exit(1)
	}
	repoRoot = root

	bin, err := buildCLI(repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: build CLI failed:", err)
		os.Exit(1)
	}
	mykbBin = bin

	if err := stackUp(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: stack up failed:", err)
		_ = stackDown(repoRoot) // best-effort cleanup
		os.Exit(1)
	}

	if err := waitForHealthz(readyTimeout); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: API not ready in time:", err)
		_ = stackDown(repoRoot)
		os.Exit(1)
	}

	code := m.Run()

	if err := stackDown(repoRoot); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: stack down failed:", err)
		// Don't override test exit code with cleanup failure.
	}

	os.Exit(code)
}

// findRepoRoot returns the absolute path of the repo root. Walks up from the
// test binary's directory looking for a Justfile.
func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "Justfile")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no Justfile found from %s upward", cwd)
		}
		dir = parent
	}
}

// buildCLI builds the mykb CLI from `cmd/mykb` into a temp directory and
// returns the absolute path to the resulting binary.
func buildCLI(root string) (string, error) {
	tmp, err := os.MkdirTemp("", "mykb-e2e-bin-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(tmp, "mykb")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/mykb/")
	cmd.Dir = root
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build: %w", err)
	}
	return bin, nil
}

func stackUp(root string) error {
	fmt.Fprintln(os.Stderr, "e2e: bringing stack up...")
	cmd := exec.Command("docker", "compose", "-p", composeProject, "-f", composeFile, "up", "-d", "--build", "--wait")
	cmd.Dir = root
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func stackDown(root string) error {
	fmt.Fprintln(os.Stderr, "e2e: tearing stack down...")
	cmd := exec.Command("docker", "compose", "-p", composeProject, "-f", composeFile, "down", "-v")
	cmd.Dir = root
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func waitForHealthz(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.NewTicker(500 * time.Millisecond)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("healthz never returned 200: %w", ctx.Err())
		case <-deadline.C:
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}
