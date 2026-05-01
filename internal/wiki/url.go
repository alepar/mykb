// Package wiki provides primitives for ingesting and managing a markdown
// wiki vault as type-blind documents in mykb. URLs are synthetic and follow
// the scheme wiki://<wiki-name>/<vault-relative-path>.
package wiki

import (
	"fmt"
	"strings"
)

const URLScheme = "wiki://"

// VaultPathToURL builds a wiki:// URL from a wiki name and vault-relative path.
// Backslashes are normalized to forward slashes; leading slashes are stripped.
func VaultPathToURL(wikiName, vaultPath string) (string, error) {
	if wikiName == "" {
		return "", fmt.Errorf("wiki name is empty")
	}
	if vaultPath == "" {
		return "", fmt.Errorf("vault path is empty")
	}
	p := strings.ReplaceAll(vaultPath, "\\", "/")
	p = strings.TrimLeft(p, "/")
	return URLScheme + wikiName + "/" + p, nil
}

// URLToVaultPath parses a wiki:// URL into wiki name and vault-relative path.
func URLToVaultPath(url string) (wikiName, vaultPath string, err error) {
	if !strings.HasPrefix(url, URLScheme) {
		return "", "", fmt.Errorf("not a wiki URL: %q", url)
	}
	rest := strings.TrimPrefix(url, URLScheme)
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 || slash == len(rest)-1 {
		return "", "", fmt.Errorf("malformed wiki URL: %q", url)
	}
	return rest[:slash], rest[slash+1:], nil
}

// IsWikiURL reports whether the URL uses the wiki:// scheme.
func IsWikiURL(url string) bool {
	return strings.HasPrefix(url, URLScheme)
}
