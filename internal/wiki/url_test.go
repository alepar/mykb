package wiki

import "testing"

func TestVaultPathToURL(t *testing.T) {
	tests := []struct {
		name, wikiName, vaultPath, want string
	}{
		{"basic", "main", "concepts/foo.md", "wiki://main/concepts/foo.md"},
		{"nested", "main", "synthesis/2026/q2/x.md", "wiki://main/synthesis/2026/q2/x.md"},
		{"backslashes_normalized", "main", "concepts\\foo.md", "wiki://main/concepts/foo.md"},
		{"leading_slash_stripped", "main", "/concepts/foo.md", "wiki://main/concepts/foo.md"},
		{"different_wiki", "personal", "concepts/foo.md", "wiki://personal/concepts/foo.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VaultPathToURL(tt.wikiName, tt.vaultPath)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestURLToVaultPath(t *testing.T) {
	tests := []struct {
		name, url, wantWiki, wantPath string
		wantErr                       bool
	}{
		{"basic", "wiki://main/concepts/foo.md", "main", "concepts/foo.md", false},
		{"nested", "wiki://main/synthesis/2026/q2/x.md", "main", "synthesis/2026/q2/x.md", false},
		{"non_wiki_scheme", "https://example.com/x", "", "", true},
		{"missing_path", "wiki://main", "", "", true},
		{"empty", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotWiki, gotPath, err := URLToVaultPath(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if gotWiki != tt.wantWiki || gotPath != tt.wantPath {
					t.Errorf("got (%q, %q), want (%q, %q)", gotWiki, gotPath, tt.wantWiki, tt.wantPath)
				}
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	wikiName := "main"
	vaultPaths := []string{"concepts/foo.md", "entities/bar/baz.md", "synthesis/answer.md"}
	for _, vp := range vaultPaths {
		t.Run(vp, func(t *testing.T) {
			url, err := VaultPathToURL(wikiName, vp)
			if err != nil {
				t.Fatal(err)
			}
			gotWiki, gotPath, err := URLToVaultPath(url)
			if err != nil {
				t.Fatal(err)
			}
			if gotWiki != wikiName || gotPath != vp {
				t.Errorf("round-trip mismatch: %q -> %q -> (%q,%q)", vp, url, gotWiki, gotPath)
			}
		})
	}
}

func TestIsWikiURL(t *testing.T) {
	if !IsWikiURL("wiki://main/x.md") {
		t.Error("expected true for wiki URL")
	}
	if IsWikiURL("https://example.com") {
		t.Error("expected false for https URL")
	}
}
