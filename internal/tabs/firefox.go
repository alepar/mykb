package tabs

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pierrec/lz4/v4"
	_ "modernc.org/sqlite"
)

type Tab struct {
	URL   string
	Title string
}

func decompressMozLz4(data []byte) ([]byte, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("mozLz4: data too short (%d bytes)", len(data))
	}
	if string(data[:8]) != "mozLz40\x00" {
		return nil, fmt.Errorf("mozLz4: bad magic %q", data[:8])
	}
	decompSize := binary.LittleEndian.Uint32(data[8:12])
	dst := make([]byte, decompSize)
	n, err := lz4.UncompressBlock(data[12:], dst)
	if err != nil {
		return nil, fmt.Errorf("mozLz4: decompress: %w", err)
	}
	return dst[:n], nil
}

type sessionData struct {
	Windows []struct {
		Tabs []struct {
			Entries []struct {
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"entries"`
		} `json:"tabs"`
	} `json:"windows"`
}

func extractTabs(data []byte) ([]Tab, error) {
	var session sessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("parse session: %w", err)
	}
	var tabs []Tab
	for _, w := range session.Windows {
		for _, t := range w.Tabs {
			if len(t.Entries) == 0 {
				continue
			}
			last := t.Entries[len(t.Entries)-1]
			tabs = append(tabs, Tab{URL: last.URL, Title: last.Title})
		}
	}
	return tabs, nil
}

func profilePaths() []string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil
	}
	paths := []string{
		filepath.Join(home, ".mozilla", "firefox"),
		filepath.Join(home, ".var", "app", "org.mozilla.firefox", "config", "mozilla", "firefox"),
		filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox"),
	}
	if runtime.GOOS == "darwin" {
		paths = append(paths, filepath.Join(home, "Library", "Application Support", "Firefox"))
	}
	return paths
}

type profileDir struct {
	Name string
	Path string
}

func discoverProfiles(firefoxRoot string) []profileDir {
	iniPath := filepath.Join(firefoxRoot, "profiles.ini")
	data, err := os.ReadFile(iniPath)
	if err != nil {
		return nil
	}
	return parseProfilesINI(data, firefoxRoot)
}

func parseProfilesINI(data []byte, baseDir string) []profileDir {
	var profiles []profileDir
	var name, path string
	var isRelative bool
	inProfile := false

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[Profile") {
			if inProfile && path != "" {
				profiles = append(profiles, makeProfileDir(name, path, isRelative, baseDir))
			}
			inProfile = true
			name = ""
			path = ""
			isRelative = false
			continue
		}
		if strings.HasPrefix(line, "[") {
			if inProfile && path != "" {
				profiles = append(profiles, makeProfileDir(name, path, isRelative, baseDir))
			}
			inProfile = false
			continue
		}
		if !inProfile {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			switch k {
			case "Name":
				name = v
			case "Path":
				path = v
			case "IsRelative":
				isRelative = v == "1"
			}
		}
	}
	if inProfile && path != "" {
		profiles = append(profiles, makeProfileDir(name, path, isRelative, baseDir))
	}
	return profiles
}

func makeProfileDir(name, path string, isRelative bool, baseDir string) profileDir {
	absPath := path
	if isRelative {
		absPath = filepath.Join(baseDir, path)
	}
	return profileDir{Name: name, Path: absPath}
}

func readSessionTabs(profilePath string) ([]Tab, error) {
	backups := filepath.Join(profilePath, "sessionstore-backups")
	for _, name := range []string{"recovery.jsonlz4", "previous.jsonlz4"} {
		data, err := os.ReadFile(filepath.Join(backups, name))
		if err != nil {
			continue
		}
		decompressed, err := decompressMozLz4(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return extractTabs(decompressed)
	}
	return nil, fmt.Errorf("no session file found in %s", backups)
}

// syncedTabsRecord represents one row from synced-tabs.db.
type syncedTabsRecord struct {
	ID         string `json:"id"`
	ClientName string `json:"clientName"`
	Tabs       []struct {
		Title      string   `json:"title"`
		URLHistory []string `json:"urlHistory"`
	} `json:"tabs"`
}

// readSyncedTabs reads tabs from a profile's synced-tabs.db (Firefox Sync).
// Returns nil, nil if the database doesn't exist or can't be opened.
func readSyncedTabs(profilePath string) ([]Tab, error) {
	dbPath := filepath.Join(profilePath, "synced-tabs.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, nil
	}
	defer db.Close()

	rows, err := db.Query("SELECT record FROM tabs")
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	var allTabs []Tab
	for rows.Next() {
		var recordJSON string
		if err := rows.Scan(&recordJSON); err != nil {
			continue
		}
		var record syncedTabsRecord
		if err := json.Unmarshal([]byte(recordJSON), &record); err != nil {
			continue
		}
		for _, t := range record.Tabs {
			if len(t.URLHistory) == 0 {
				continue
			}
			allTabs = append(allTabs, Tab{URL: t.URLHistory[0], Title: t.Title})
		}
	}
	return allTabs, nil
}

func DiscoverTabs() ([]Tab, []string, error) {
	seen := make(map[string]struct{})
	var allTabs []Tab
	var profileNames []string

	addTab := func(t Tab) {
		if _, ok := seen[t.URL]; ok {
			return
		}
		seen[t.URL] = struct{}{}
		allTabs = append(allTabs, t)
	}

	for _, root := range profilePaths() {
		profiles := discoverProfiles(root)
		for _, p := range profiles {
			name := p.Name
			if name == "" {
				name = filepath.Base(p.Path)
			}

			hasData := false

			// Local session tabs take priority (added first, so dedup favors them)
			if tabs, err := readSessionTabs(p.Path); err == nil {
				for _, t := range tabs {
					addTab(t)
				}
				hasData = true
			}

			// Synced tabs from all devices
			if tabs, err := readSyncedTabs(p.Path); err == nil && len(tabs) > 0 {
				for _, t := range tabs {
					addTab(t)
				}
				hasData = true
			}

			if hasData {
				profileNames = append(profileNames, name)
			}
		}
	}
	if len(allTabs) == 0 {
		return nil, nil, fmt.Errorf("no Firefox tabs found (checked %d locations)", len(profilePaths()))
	}
	return allTabs, profileNames, nil
}
