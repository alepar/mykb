package tabs

import (
	"encoding/binary"
	"testing"

	"github.com/pierrec/lz4/v4"
)

func makeMozLz4(data []byte) []byte {
	dst := make([]byte, lz4.CompressBlockBound(len(data)))
	n, err := lz4.CompressBlock(data, dst, nil)
	if err != nil || n == 0 {
		panic("compress failed")
	}
	buf := make([]byte, 12+n)
	copy(buf, []byte("mozLz40\x00"))
	binary.LittleEndian.PutUint32(buf[8:], uint32(len(data)))
	copy(buf[12:], dst[:n])
	return buf
}

func TestDecompressMozLz4(t *testing.T) {
	original := []byte(`{"windows":[{"tabs":[{"entries":[{"url":"https://example.com","title":"Example"}]}]}]}`)
	compressed := makeMozLz4(original)
	got, err := decompressMozLz4(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("got %q, want %q", got, original)
	}
}

func TestDecompressMozLz4_BadMagic(t *testing.T) {
	data := []byte("notmozlz4data")
	_, err := decompressMozLz4(data)
	if err == nil {
		t.Error("expected error for bad magic")
	}
}

func TestExtractTabs(t *testing.T) {
	sessionJSON := []byte(`{
		"windows": [
			{
				"tabs": [
					{"entries": [{"url": "https://old.com", "title": "Old"}, {"url": "https://example.com", "title": "Example"}]},
					{"entries": [{"url": "https://other.com", "title": "Other"}]},
					{"entries": []}
				]
			},
			{
				"tabs": [
					{"entries": [{"url": "https://second.com", "title": "Second Window"}]}
				]
			}
		]
	}`)
	tabs, err := extractTabs(sessionJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs) != 3 {
		t.Fatalf("got %d tabs, want 3", len(tabs))
	}
	if tabs[0].URL != "https://example.com" || tabs[0].Title != "Example" {
		t.Errorf("tab 0: got %q %q", tabs[0].URL, tabs[0].Title)
	}
	if tabs[1].URL != "https://other.com" {
		t.Errorf("tab 1: got %q", tabs[1].URL)
	}
	if tabs[2].URL != "https://second.com" {
		t.Errorf("tab 2: got %q", tabs[2].URL)
	}
}

func TestParseProfilesINI(t *testing.T) {
	ini := `[Profile1]
Name=default
IsRelative=1
Path=abc123.default

[InstallCF146F38BCAB2D21]
Default=xyz789.default-release
Locked=1

[Profile0]
Name=default-release
IsRelative=1
Path=xyz789.default-release
`
	profiles := parseProfilesINI([]byte(ini), "/home/user/.mozilla/firefox")
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	if profiles[0].Name != "default" {
		t.Errorf("profile 0 name: got %q", profiles[0].Name)
	}
	if profiles[0].Path != "/home/user/.mozilla/firefox/abc123.default" {
		t.Errorf("profile 0 path: got %q", profiles[0].Path)
	}
	if profiles[1].Name != "default-release" {
		t.Errorf("profile 1 name: got %q", profiles[1].Name)
	}
}

func TestParseProfilesINI_AbsolutePath(t *testing.T) {
	ini := `[Profile0]
Name=custom
IsRelative=0
Path=/opt/firefox-profiles/custom
`
	profiles := parseProfilesINI([]byte(ini), "/home/user/.mozilla/firefox")
	if len(profiles) != 1 {
		t.Fatalf("got %d profiles, want 1", len(profiles))
	}
	if profiles[0].Path != "/opt/firefox-profiles/custom" {
		t.Errorf("got %q, want absolute path", profiles[0].Path)
	}
}
