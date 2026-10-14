package organizer

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/damuraiz/plex-helper/internal/media"
)

// ScanFiles inspects exactly the requested videos. It never walks their parent
// directories. Root is the common ancestor used by the existing plan/journal
// guards; Explicit allows a destination below that ancestor without scanning it.
func (e *Engine) ScanFiles(paths []string, kind string) (Inventory, error) {
	inv := Inventory{Items: []Item{}, Issues: []Issue{}, Explicit: true}
	if kind != "auto" && kind != "movie" && kind != "tv" {
		return inv, fmt.Errorf("kind must be auto, movie or tv")
	}
	if len(paths) == 0 || len(paths) > 10000 {
		return inv, fmt.Errorf("выберите от 1 до 10 000 видеофайлов")
	}
	unique := make(map[string]bool)
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || !video(path) {
			return inv, fmt.Errorf("укажите абсолютный путь к видеофайлу: %s", path)
		}
		if unique[path] {
			continue
		}
		unique[path] = true
		identity, err := snapshot(path)
		if err != nil {
			return inv, fmt.Errorf("выбранный файл недоступен: %s: %w", path, err)
		}
		inv.Items = append(inv.Items, Item{ID: newID(), Parsed: media.Parse(path, kind), Identity: identity})
		parent := filepath.Dir(path)
		if inv.Root == "" {
			inv.Root = parent
		}
		for !inside(parent, inv.Root) {
			inv.Root = filepath.Dir(inv.Root)
		}
	}
	sort.Slice(inv.Items, func(i, j int) bool { return inv.Items[i].Parsed.Path < inv.Items[j].Parsed.Path })
	return inv, nil
}
