package organizer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/damuraiz/plex-helper/internal/media"
)

func TestSelectedFilesApplyUndoLeavesNeighboursUntouched(t *testing.T) {
	e, root, _ := fixture(t)
	os.Mkdir(filepath.Join(root, "other"), 0755)
	first, second := filepath.Join(root, "First.2024.mkv"), filepath.Join(root, "other", "Second.2025.mp4")
	untouched := filepath.Join(root, "Unselected.2020.mkv")
	sub := filepath.Join(root, "First.2024.ru.srt")
	for path, data := range map[string]string{first: "first", second: "second", untouched: "leave me", sub: "subtitle"} {
		put(t, path, data)
	}
	inv, err := e.ScanFiles([]string{second, first, first}, "movie")
	if err != nil || len(inv.Items) != 2 || inv.Root != root {
		t.Fatal(inv, err)
	}
	selections := map[string]media.Metadata{}
	for _, item := range inv.Items {
		selections[item.ID] = media.Metadata{Kind: "movie", Title: item.Parsed.Title, Year: item.Parsed.Year}
	}
	// Explicit selections may be reorganized under their common ancestor.
	plan, err := e.Plan(inv, filepath.Join(root, "Movies"), selections)
	if err != nil || len(plan.Entries) != 3 {
		t.Fatal(plan, err)
	}
	ids := []string{inv.Items[0].ID, inv.Items[1].ID}
	j, err := e.Apply(plan, ids)
	if err != nil {
		t.Fatal(err)
	}
	if content(t, untouched) != "leave me" {
		t.Fatal("unselected video changed")
	}
	if _, err = os.Stat(sub); !os.IsNotExist(err) {
		t.Fatal("selected subtitle not moved", err)
	}
	if _, err = e.Undo(j.ID); err != nil {
		t.Fatal(err)
	}
	if content(t, first) != "first" || content(t, second) != "second" || content(t, sub) != "subtitle" {
		t.Fatal("undo changed content")
	}
	if _, err = e.Plan(inv, filepath.Join(root, "Movies"), selections); err == nil {
		t.Fatal("stale scan accepted after moves")
	}
}

func TestSelectedContainerDoesNotClaimAmbiguousSubtitle(t *testing.T) {
	e, root, library := fixture(t)
	path := filepath.Join(root, "Example.2024.mkv")
	put(t, path, "mkv")
	put(t, filepath.Join(root, "Example.2024.mp4"), "mp4")
	put(t, filepath.Join(root, "Example.2024.srt"), "shared")
	inv, err := e.ScanFiles([]string{path}, "movie")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := e.Plan(inv, library, map[string]media.Metadata{inv.Items[0].ID: {Kind: "movie", Title: "Example", Year: 2024}})
	if err != nil || len(plan.Entries) != 1 || len(plan.Issues) != 1 {
		t.Fatal(plan, err)
	}
}

func TestSelectedFilesRejectInvalidAndSymlinkPaths(t *testing.T) {
	e, root, _ := fixture(t)
	path := filepath.Join(root, "Example.mkv")
	put(t, path, "video")
	link := filepath.Join(root, "Alias.mkv")
	os.Symlink(path, link)
	parentLink := filepath.Join(filepath.Dir(root), "alias")
	os.Symlink(root, parentLink)
	put(t, filepath.Join(root, "note.txt"), "not video")
	for _, paths := range [][]string{nil, {}, {"relative.mkv"}, {root}, {link}, {filepath.Join(parentLink, "Example.mkv")}, {filepath.Join(root, "missing.mkv")}, {filepath.Join(root, "note.txt")}, {path, filepath.Join(root, "missing.mkv")}} {
		if _, err := e.ScanFiles(paths, "auto"); err == nil {
			t.Fatal("invalid selection accepted", paths)
		}
	}
	if content(t, path) != "video" {
		t.Fatal("scan changed file")
	}
}
