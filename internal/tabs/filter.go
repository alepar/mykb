package tabs

import (
	"net/url"
	"strings"
)

var internalSchemes = []string{"about:", "chrome://", "moz-extension://", "resource://"}
var localHosts = []string{"localhost", "127.0.0.1", "::1", "0.0.0.0"}
var filteredPrefixes = []string{
	"https://claude.ai/chat/",
	"https://platform.claude.com",
}

func ShouldFilter(rawURL string) bool {
	for _, scheme := range internalSchemes {
		if strings.HasPrefix(rawURL, scheme) {
			return true
		}
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	host := parsed.Hostname()
	for _, h := range localHosts {
		if host == h {
			return true
		}
	}
	for _, prefix := range filteredPrefixes {
		if strings.HasPrefix(rawURL, prefix) {
			return true
		}
	}
	return false
}
