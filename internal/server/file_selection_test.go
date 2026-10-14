package server

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/damuraiz/plex-helper/internal/organizer"
)

func TestFilePickerAndExplicitScan(t *testing.T) {
	_, h := testApp(t)
	cookie, csrf := login(t, h)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	first, second := filepath.Join(root, "First.2024.mkv"), filepath.Join(root, "Second.2025.mp4")
	os.WriteFile(first, []byte("first"), 0600)
	os.WriteFile(second, []byte("second"), 0600)
	os.WriteFile(filepath.Join(root, "note.txt"), []byte("note"), 0600)
	os.Symlink(first, filepath.Join(root, "alias.mkv"))
	os.Mkdir(filepath.Join(root, "nested"), 0700)
	query := "/api/browse?path=" + url.QueryEscape(root)
	w := call(t, h, "GET", query+"&files=1", nil, nil, "")
	if w.Code != 401 {
		t.Fatal("unauthenticated browser accepted", w.Code)
	}
	w = call(t, h, "GET", query+"&files=1", nil, cookie, "")
	var listing struct {
		Files []struct{ Path string }
		Dirs  []struct{ Path string }
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listing) != nil || len(listing.Files) != 2 || len(listing.Dirs) != 1 {
		t.Fatal(w.Body.String())
	}
	w = call(t, h, "GET", query, nil, cookie, "")
	json.Unmarshal(w.Body.Bytes(), &listing)
	if len(listing.Files) != 0 {
		t.Fatal("folder picker included files")
	}
	input := map[string]any{"kind": "movie", "files": []string{second}}
	w = call(t, h, "POST", "/api/scan", input, cookie, "")
	if w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	w = call(t, h, "POST", "/api/scan", input, cookie, csrf)
	var inv struct {
		Items    []organizer.Item
		Explicit bool
		Groups   []MatchGroup
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &inv) != nil || !inv.Explicit || len(inv.Items) != 1 || inv.Items[0].Parsed.Path != second || len(inv.Groups) != 1 {
		t.Fatal(w.Body.String())
	}
	for _, files := range []any{[]string{}, nil, "not a list", []string{second, filepath.Join(root, "missing.mkv")}} {
		w = call(t, h, "POST", "/api/scan", map[string]any{"source": root, "kind": "auto", "files": files}, cookie, csrf)
		if w.Code != 400 {
			t.Fatal("invalid files fell back to whole-folder scan", w.Code, w.Body.String())
		}
	}
}
