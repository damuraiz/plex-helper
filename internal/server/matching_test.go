package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damuraiz/plex-helper/internal/media"
	"github.com/damuraiz/plex-helper/internal/organizer"
)

func TestAutomaticChoiceRequiresUniqueTitleAndYear(t *testing.T) {
	g := MatchGroup{Kind: "tv", Title: "The Idol"}
	exact := Candidate{Kind: "tv", Title: "Кумир", OriginalTitle: "The Idol", Year: 2023, ID: "135251"}
	other := Candidate{Kind: "tv", Title: "Айдол: Переворот", OriginalTitle: "Idol: The Coup", Year: 2021, ID: "137215"}
	if c, ok := automaticCandidate(g, []Candidate{other, exact}); !ok || c.ID != exact.ID {
		t.Fatal(c, ok)
	}
	remake := exact
	remake.Year = 1990
	remake.ID = "456"
	if _, ok := automaticCandidate(g, []Candidate{exact, remake}); ok {
		t.Fatal("ambiguous same-title shows were accepted")
	}
	g.Year = 2023
	if _, ok := automaticCandidate(g, []Candidate{remake, exact}); !ok {
		t.Fatal("year did not disambiguate")
	}
	g.Title = "Idol"
	if _, ok := automaticCandidate(g, []Candidate{exact}); ok {
		t.Fatal("partial-title guess accepted")
	}
}

func TestSeriesGroupsPreserveSeasonsAndSeparateIdentity(t *testing.T) {
	paths := []string{"/a/The.Idol.S01E01.mkv", "/b/The.Idol.S02E02.mkv", "/b/The.Idol.1990.S01E01.mkv", "/b/Dark.S01E01.mkv", "/b/Dark.S01E02.{tmdb-1}.mkv", "/b/The.Idol.2023.mkv"}
	inv := organizer.Inventory{}
	for i, path := range paths {
		inv.Items = append(inv.Items, organizer.Item{ID: fmt.Sprint(i), Parsed: media.Parse(path, "auto")})
	}
	groups := matchGroups(inv)
	if len(groups) != 5 || len(groups[0].ItemIDs) != 2 || fmt.Sprint(groups[0].Seasons) != "[1 2]" {
		t.Fatal(groups)
	}
	season := 1
	identity := media.Metadata{Kind: "tv", Title: "Кумир", Year: 2023, Provider: "tmdb", ID: "135251", Season: &season, Episodes: []int{1}}
	result := distributeMatch(inv, groups[0], identity, "0", nil, false)
	if len(result.Selections) != 2 || *result.Selections["1"].Season != 2 || result.Selections["1"].Episodes[0] != 2 {
		t.Fatal(result)
	}
	if _, ok := result.Selections["2"]; ok {
		t.Fatal("another show was changed")
	}
}

// Exercise the user journey with real file operations on disposable fixtures:
// prepare once for five episodes, replace the show once, preview, apply, undo.
func TestPrepareSeriesThenGroupCorrectionApplyUndo(t *testing.T) {
	a, h := testApp(t)
	cookie, csrf := login(t, h)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	incoming, library := filepath.Join(root, "incoming"), filepath.Join(root, "library")
	os.Mkdir(incoming, 0700)
	os.Mkdir(library, 0700)
	searches, details, seasons := 0, 0, 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/tv":
			searches++
			if r.URL.Query().Get("query") != "The Idol" {
				t.Error(r.URL.Query().Get("query"))
			}
			fmt.Fprint(w, `{"results":[{"id":135251,"name":"Кумир","original_name":"The Idol","first_air_date":"2023-06-04"},{"id":46599,"name":"Кии металлический идол","original_name":"Key the Metal Idol","first_air_date":"1994-12-16"}]}`)
		case "/tv/135251", "/tv/777":
			details++
			fmt.Fprint(w, `{"id":135251,"name":"Кумир","first_air_date":"2023-06-04"}`)
		case "/tv/135251/season/1", "/tv/777/season/1":
			seasons++
			fmt.Fprint(w, `{"episodes":[{"episode_number":1,"name":"Первая"},{"episode_number":2,"name":"Вторая"},{"episode_number":3,"name":"Третья"},{"episode_number":4,"name":"Четвёртая"},{"episode_number":5,"name":"Пятая"}]}`)
		default:
			t.Error(r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer mock.Close()
	a.metadataClient = func(c Config) *TMDB {
		return &TMDB{Token: "fixture", Language: "ru-RU", BaseURL: mock.URL + "/", Client: mock.Client()}
	}
	content := []byte("disposable media fixture")
	for ep := 1; ep <= 5; ep++ {
		os.WriteFile(filepath.Join(incoming, fmt.Sprintf("The.Idol.S01E%02d.2160p.mkv", ep)), content, 0600)
	}
	os.WriteFile(filepath.Join(incoming, "The.Idol.S01E01.2160p.ru.srt"), []byte("fixture subtitle"), 0600)
	w := call(t, h, "POST", "/api/scan", map[string]string{"source": incoming, "kind": "auto"}, cookie, csrf)
	var inv struct {
		InventoryID string           `json:"inventoryId"`
		Items       []organizer.Item `json:"items"`
		Groups      []MatchGroup     `json:"groups"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &inv); w.Code != 200 || err != nil || len(inv.Groups) != 1 {
		t.Fatal(w.Body.String(), err)
	}
	prepare := map[string]string{"inventoryId": inv.InventoryID, "itemId": inv.Items[0].ID}
	if w = call(t, h, "POST", "/api/prepare", prepare, nil, ""); w.Code != 401 {
		t.Fatal("unauthenticated prepare", w.Code)
	}
	if w = call(t, h, "POST", "/api/prepare", prepare, cookie, ""); w.Code != 403 {
		t.Fatal("prepare without CSRF", w.Code)
	}
	w = call(t, h, "POST", "/api/prepare", prepare, cookie, csrf)
	var result MatchResult
	if err = json.Unmarshal(w.Body.Bytes(), &result); w.Code != 200 || err != nil || len(result.Selections) != 5 {
		t.Fatal(w.Body.String(), err)
	}
	if searches != 1 || details != 1 || seasons != 1 {
		t.Fatal("TMDB was not deduplicated", searches, details, seasons)
	}
	for i, item := range inv.Items {
		m := result.Selections[item.ID]
		if m.ID != "135251" || *m.Season != 1 || m.Episodes[0] != i+1 {
			t.Fatal(item, m)
		}
	}
	entries, _ := os.ReadDir(library)
	if len(entries) != 0 {
		t.Fatal("prepare wrote files")
	}
	if w = call(t, h, "POST", "/api/prepare", map[string]string{"inventoryId": "stale", "itemId": inv.Items[0].ID}, cookie, csrf); w.Code != 400 {
		t.Fatal("stale inventory accepted")
	}
	// Fix one episode's coordinates and propagate only the series identity.
	identity := result.Selections[inv.Items[0].ID]
	identity.Title = "Другое название"
	identity.ID = "777"
	w = call(t, h, "POST", "/api/match", map[string]any{"inventoryId": inv.InventoryID, "itemId": inv.Items[0].ID, "scope": "group", "metadata": identity, "selections": result.Selections}, cookie, csrf)
	if err = json.Unmarshal(w.Body.Bytes(), &result); w.Code != 200 || err != nil || len(result.Selections) != 5 {
		t.Fatal(w.Body.String(), err)
	}
	for i, item := range inv.Items {
		m := result.Selections[item.ID]
		if m.Title != "Другое название" || m.ID != "777" || m.Episodes[0] != i+1 {
			t.Fatal(item, m)
		}
	}
	w = call(t, h, "POST", "/api/plan", map[string]any{"inventoryId": inv.InventoryID, "library": library, "selections": result.Selections}, cookie, csrf)
	var plan organizer.Plan
	if err = json.Unmarshal(w.Body.Bytes(), &plan); w.Code != 200 || err != nil || len(plan.Entries) != 6 {
		t.Fatal(w.Body.String(), err)
	}
	ids := []string{}
	for _, entry := range plan.Entries {
		if !strings.Contains(entry.Destination, "Другое название (2023)") {
			t.Fatal(entry.Destination)
		}
		if _, err = os.Stat(entry.Source); err != nil {
			t.Fatal("preview changed source", err)
		}
		if entry.Role == "video" {
			ids = append(ids, entry.MediaID)
		}
	}
	w = call(t, h, "POST", "/api/apply", map[string]any{"planId": plan.ID, "confirmed": ids}, cookie, csrf)
	var journal organizer.Journal
	if err = json.Unmarshal(w.Body.Bytes(), &journal); w.Code != 200 || err != nil {
		t.Fatal(w.Body.String(), err)
	}
	for _, entry := range plan.Entries {
		if _, err = os.Stat(entry.Destination); err != nil {
			t.Fatal(err)
		}
	}
	w = call(t, h, "POST", "/api/undo", map[string]string{"journalId": journal.ID}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, entry := range plan.Entries {
		if _, err = os.Stat(entry.Source); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGroupMatchLeavesUnknownEpisodesAndPreservesCorrections(t *testing.T) {
	inv := organizer.Inventory{}
	for i, path := range []string{"/fixture/Dark.S01E01.mkv", "/fixture/Dark.S01E02.mkv", "/fixture/Dark.mkv"} {
		inv.Items = append(inv.Items, organizer.Item{ID: fmt.Sprint(i), Parsed: media.Parse(path, "tv")})
	}
	g := matchGroups(inv)[0]
	season := 2
	m := media.Metadata{Kind: "tv", Title: "Тьма", Year: 2017, Season: &season, Episodes: []int{1}}
	prior := map[string]media.Metadata{"1": {Kind: "tv", Season: &season, Episodes: []int{3}}}
	result := distributeMatch(inv, g, m, "0", prior, false)
	if len(result.Selections) != 2 || len(result.Issues) != 1 || *result.Selections["1"].Season != 2 || result.Selections["1"].Episodes[0] != 3 {
		t.Fatal(result)
	}
	if _, ok := result.Selections["2"]; ok {
		t.Fatal("unknown episode copied from first file")
	}
}
