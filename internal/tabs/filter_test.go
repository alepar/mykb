package tabs

import "testing"

func TestShouldFilter(t *testing.T) {
	tests := []struct {
		url    string
		filter bool
	}{
		{"about:blank", true},
		{"about:config", true},
		{"chrome://settings", true},
		{"moz-extension://abc/popup.html", true},
		{"resource://gre/modules", true},
		{"http://localhost:3000", true},
		{"http://127.0.0.1:8080/test", true},
		{"http://[::1]:9090", true},
		{"http://0.0.0.0:5000", true},
		{"https://claude.ai/chat/abc-123", true},
		{"https://platform.claude.com/oauth/code/success", true},
		{"https://example.com", false},
		{"https://github.com/user/repo", false},
		{"https://news.ycombinator.com/item?id=123", false},
		{"https://claude.ai/docs/tool-use", false},
		{"http://192.168.1.1/admin", false},
	}
	for _, tt := range tests {
		got := ShouldFilter(tt.url)
		if got != tt.filter {
			t.Errorf("ShouldFilter(%q) = %v, want %v", tt.url, got, tt.filter)
		}
	}
}
