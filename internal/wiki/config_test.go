package wiki

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseVaultConfig(t *testing.T) {
	in := `name = "main"
stale_after_days = 90
`
	cfg, err := ParseVaultConfig([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "main" {
		t.Errorf("name: got %q want main", cfg.Name)
	}
	if cfg.StaleAfterDays != 90 {
		t.Errorf("stale: got %d want 90", cfg.StaleAfterDays)
	}
}

func TestParseVaultConfigDefaults(t *testing.T) {
	in := `name = "personal"`
	cfg, err := ParseVaultConfig([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StaleAfterDays != 180 {
		t.Errorf("stale default: got %d want 180", cfg.StaleAfterDays)
	}
}

func TestParseVaultConfigRequiresName(t *testing.T) {
	_, err := ParseVaultConfig([]byte(`stale_after_days = 30`))
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestDiscoverVaultRoot(t *testing.T) {
	tmp := t.TempDir()
	vault := filepath.Join(tmp, "myvault")
	sub := filepath.Join(vault, "concepts", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "mykb-wiki.toml"), []byte(`name = "main"`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DiscoverVaultRoot(sub)
	if err != nil {
		t.Fatal(err)
	}
	wantAbs, _ := filepath.Abs(vault)
	gotAbs, _ := filepath.Abs(got)
	if gotAbs != wantAbs {
		t.Errorf("got %q want %q", gotAbs, wantAbs)
	}
}

func TestDiscoverVaultRootNotFound(t *testing.T) {
	tmp := t.TempDir()
	_, err := DiscoverVaultRoot(tmp)
	if err == nil {
		t.Error("expected error when no vault found")
	}
}
