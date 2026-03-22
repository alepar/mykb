package tabs

import (
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pierrec/lz4/v4"
	_ "modernc.org/sqlite"
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

func TestReadSyncedTabs(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "synced-tabs.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE tabs (guid TEXT NOT NULL PRIMARY KEY, record TEXT NOT NULL, last_modified INTEGER NOT NULL)`)
	db.Exec(`INSERT INTO tabs (guid, record, last_modified) VALUES (?, ?, ?)`,
		"device1",
		`{"id":"device1","clientName":"Pixel 6a","tabs":[{"title":"Mobile Page","urlHistory":["https://mobile.example.com"],"lastUsed":1710000000},{"title":"Another","urlHistory":["https://another.com","https://old.com"],"lastUsed":1710000001}]}`,
		1710000000,
	)
	db.Exec(`INSERT INTO tabs (guid, record, last_modified) VALUES (?, ?, ?)`,
		"device2",
		`{"id":"device2","clientName":"MacBook","tabs":[{"title":"Desktop Page","urlHistory":["https://desktop.example.com"],"lastUsed":1710000002}]}`,
		1710000002,
	)
	db.Close()

	tabs, err := readSyncedTabs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs) != 3 {
		t.Fatalf("got %d tabs, want 3", len(tabs))
	}
	if tabs[0].URL != "https://mobile.example.com" || tabs[0].Title != "Mobile Page" {
		t.Errorf("tab 0: got %q %q", tabs[0].URL, tabs[0].Title)
	}
	if tabs[1].URL != "https://another.com" {
		t.Errorf("tab 1: got %q, want first urlHistory entry", tabs[1].URL)
	}
	if tabs[2].URL != "https://desktop.example.com" {
		t.Errorf("tab 2: got %q", tabs[2].URL)
	}
}

func TestReadSyncedTabs_NoDatabase(t *testing.T) {
	tabs, err := readSyncedTabs(t.TempDir())
	if err != nil {
		t.Errorf("expected nil error for missing db, got %v", err)
	}
	if len(tabs) != 0 {
		t.Errorf("expected empty tabs, got %d", len(tabs))
	}
}

func TestReadSyncedTabs_EmptyURLHistory(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "synced-tabs.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE tabs (guid TEXT NOT NULL PRIMARY KEY, record TEXT NOT NULL, last_modified INTEGER NOT NULL)`)
	db.Exec(`INSERT INTO tabs (guid, record, last_modified) VALUES (?, ?, ?)`,
		"device1",
		`{"id":"device1","clientName":"Test","tabs":[{"title":"No URL","urlHistory":[]},{"title":"Has URL","urlHistory":["https://ok.com"]}]}`,
		1710000000,
	)
	db.Close()

	tabs, err := readSyncedTabs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs) != 1 {
		t.Fatalf("got %d tabs, want 1 (empty urlHistory should be skipped)", len(tabs))
	}
	if tabs[0].URL != "https://ok.com" {
		t.Errorf("got %q", tabs[0].URL)
	}
}

func TestDiscoverTabs_DeduplicatesURLs(t *testing.T) {
	// Create a temp profile with both session and synced tabs sharing a URL
	dir := t.TempDir()
	profileDir := filepath.Join(dir, "test.default")
	os.MkdirAll(filepath.Join(profileDir, "sessionstore-backups"), 0755)

	// Write profiles.ini
	ini := "[Profile0]\nName=test\nIsRelative=1\nPath=test.default\n"
	os.WriteFile(filepath.Join(dir, "profiles.ini"), []byte(ini), 0644)

	// Write session data with one tab
	sessionJSON := `{"windows":[{"tabs":[{"entries":[{"url":"https://shared.com","title":"From Session"}]}]}]}`
	compressed := makeMozLz4([]byte(sessionJSON))
	os.WriteFile(filepath.Join(profileDir, "sessionstore-backups", "recovery.jsonlz4"), compressed, 0644)

	// Write synced-tabs.db with overlapping + unique tab
	db, _ := sql.Open("sqlite", filepath.Join(profileDir, "synced-tabs.db"))
	db.Exec(`CREATE TABLE tabs (guid TEXT NOT NULL PRIMARY KEY, record TEXT NOT NULL, last_modified INTEGER NOT NULL)`)
	db.Exec(`INSERT INTO tabs (guid, record, last_modified) VALUES (?, ?, ?)`,
		"device1",
		`{"id":"d1","clientName":"Phone","tabs":[{"title":"From Sync","urlHistory":["https://shared.com"]},{"title":"Unique","urlHistory":["https://unique.com"]}]}`,
		1710000000,
	)
	db.Close()

	// Discover from this specific root
	profiles := discoverProfiles(dir)
	seen := make(map[string]struct{})
	var allTabs []Tab
	for _, p := range profiles {
		if tabs, err := readSessionTabs(p.Path); err == nil {
			for _, t := range tabs {
				if _, ok := seen[t.URL]; !ok {
					seen[t.URL] = struct{}{}
					allTabs = append(allTabs, t)
				}
			}
		}
		if tabs, err := readSyncedTabs(p.Path); err == nil {
			for _, t := range tabs {
				if _, ok := seen[t.URL]; !ok {
					seen[t.URL] = struct{}{}
					allTabs = append(allTabs, t)
				}
			}
		}
	}

	if len(allTabs) != 2 {
		t.Fatalf("got %d tabs, want 2 (shared.com deduped)", len(allTabs))
	}
	// Session tab should win for shared URL
	if allTabs[0].Title != "From Session" {
		t.Errorf("expected session tab to take priority, got title %q", allTabs[0].Title)
	}
	if allTabs[1].URL != "https://unique.com" {
		t.Errorf("expected unique sync tab, got %q", allTabs[1].URL)
	}
}
