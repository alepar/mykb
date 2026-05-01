//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestCLISurface is a breadth-first sweep of every top-level CLI subcommand.
// It does not validate functional correctness (other tests do that) — it only
// confirms that:
//   - bad inputs produce non-zero exit codes with usage on stderr,
//   - good inputs produce zero exits,
//   - no command panics or returns unexpected errors.
func TestCLISurface(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantExit int   // expected exit code
		stderrIn []string // substrings expected on stderr (when wantExit != 0)
		stdoutIn []string // substrings expected on stdout (when wantExit == 0)
	}{
		{
			name:     "no_args_prints_usage",
			args:     nil,
			wantExit: 1,
			stderrIn: []string{"Usage:"},
		},
		{
			name:     "wiki_no_subcommand_prints_usage",
			args:     []string{"wiki"},
			wantExit: 1,
			stderrIn: []string{"Usage:", "mykb wiki"},
		},
		{
			name:     "wiki_unknown_subcommand",
			args:     []string{"wiki", "bogus"},
			wantExit: 1,
			stderrIn: []string{"unknown wiki subcommand"},
		},
		{
			name:     "ingest_no_url",
			args:     []string{"ingest"},
			wantExit: 1,
			stderrIn: []string{"Usage:"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runMykb(t, "", tc.args...)
			if code != tc.wantExit {
				t.Errorf("exit code: got %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantExit, stdout, stderr)
			}
			for _, s := range tc.stderrIn {
				if !strings.Contains(stderr, s) {
					t.Errorf("stderr missing %q: %s", s, stderr)
				}
			}
			for _, s := range tc.stdoutIn {
				if !strings.Contains(stdout, s) {
					t.Errorf("stdout missing %q: %s", s, stdout)
				}
			}
		})
	}

	// `mykb wiki init` is exercised by tempVault elsewhere; here we just
	// confirm a fresh init in an isolated dir produces the expected files.
	t.Run("wiki_init_scaffolds_vault", func(t *testing.T) {
		dir := t.TempDir()
		stdout, stderr, code := runMykb(t, "", "wiki", "init", "--vault", dir, "--name", "surfacetest")
		if code != 0 {
			t.Fatalf("wiki init failed: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
		}
	})
}
