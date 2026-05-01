package wiki

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// VaultConfigFilename is the well-known config file at the vault root.
const VaultConfigFilename = "mykb-wiki.toml"

// VaultConfig holds the per-vault configuration parsed from mykb-wiki.toml.
type VaultConfig struct {
	Name           string `toml:"name"`
	StaleAfterDays int    `toml:"stale_after_days"`
}

// ParseVaultConfig parses TOML bytes into a VaultConfig and applies defaults.
// Returns an error if `name` is missing.
func ParseVaultConfig(data []byte) (VaultConfig, error) {
	var cfg VaultConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return VaultConfig{}, fmt.Errorf("parse vault config: %w", err)
	}
	if cfg.Name == "" {
		return VaultConfig{}, fmt.Errorf("vault config missing required field: name")
	}
	if cfg.StaleAfterDays == 0 {
		cfg.StaleAfterDays = 180
	}
	return cfg, nil
}

// LoadVaultConfig reads and parses mykb-wiki.toml from the given vault root.
func LoadVaultConfig(vaultRoot string) (VaultConfig, error) {
	path := filepath.Join(vaultRoot, VaultConfigFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return VaultConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	return ParseVaultConfig(data)
}

// DiscoverVaultRoot walks up from `start` looking for a directory containing
// mykb-wiki.toml. Returns an absolute path to the vault root, or an error
// if no vault is found before reaching the filesystem root.
func DiscoverVaultRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, VaultConfigFilename)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in a wiki vault (no %s found from %s upward)", VaultConfigFilename, start)
		}
		dir = parent
	}
}
