package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damuraiz/plex-helper/internal/media"
	"github.com/damuraiz/plex-helper/internal/organizer"
)

func testApp(t *testing.T) (*App, http.Handler) {
	t.Helper()
	a, err := New(filepath.Join(t.TempDir(), "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Store.InitPassword("correct-test-password"); err != nil {
		t.Fatal(err)
	}
	return a, a.Handler()
}
func call(t *testing.T, h http.Handler, method, path string, data any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	body := new(bytes.Buffer)
	if data != nil {
		if err := json.NewEncoder(body).Encode(data); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	w := call(t, h, "POST", "/api/login", map[string]string{"password": "correct-test-password"}, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(w.Body.Bytes(), &data)
	return w.Result().Cookies()[0], data.CSRF
}

func TestAuthenticationAndCSRF(t *testing.T) {
	_, h := testApp(t)
	w := call(t, h, "POST", "/api/scan", map[string]string{"source": "/", "kind": "auto"}, nil, "")
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie, csrf := login(t, h)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("insecure session cookie")
	}
	w = call(t, h, "POST", "/api/scan", map[string]string{"source": "/", "kind": "auto"}, cookie, "")
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r := httptest.NewRequest("POST", "/api/logout", strings.NewReader("{}"))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "http://evil.example")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestConfigPersistsWithoutLeakingSecrets(t *testing.T) {
	a, h := testApp(t)
	cookie, csrf := login(t, h)
	w := call(t, h, "POST", "/api/settings", map[string]any{"sourceRoot": "", "libraryRoot": "", "language": "ru-RU", "tmdbToken": "private-test-token"}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = call(t, h, "GET", "/api/state", nil, cookie, "")
	if strings.Contains(w.Body.String(), "private-test-token") || strings.Contains(w.Body.String(), "passwordHash") {
		t.Fatal("secret leaked in state")
	}
	store, err := OpenStore(a.Store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if store.snapshot().TMDBToken != "private-test-token" || !store.VerifyPassword("correct-test-password") {
		t.Fatal("state was not retained")
	}
	if err = store.InitPassword("another-test-password"); err == nil {
		t.Fatal("overwrote existing auth")
	}
	info, err := os.Stat(filepath.Join(a.Store.dir, "config.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("config mode", err)
	}
}

func TestSetupRequiresOneTimeToken(t *testing.T) {
	a, err := New(filepath.Join(t.TempDir(), "data"), "test")
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	w := call(t, h, "POST", "/api/setup", map[string]string{"token": "bad", "password": "correct-test-password"}, nil, "")
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	token, err := os.ReadFile(filepath.Join(a.Store.dir, "setup-token"))
	if err != nil {
		t.Fatal(err)
	}
	w = call(t, h, "POST", "/api/setup", map[string]string{"token": string(token), "password": "correct-test-password"}, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err = os.Stat(filepath.Join(a.Store.dir, "setup-token")); !os.IsNotExist(err) {
		t.Fatal("setup token was not removed")
	}
	w = call(t, h, "POST", "/api/setup", map[string]string{"token": string(token), "password": "another-test-password"}, nil, "")
	if w.Code != 403 {
		t.Fatal("setup repeated")
	}
}

func TestHTTPScanPlanApplyUndo(t *testing.T) {
	_, h := testApp(t)
	cookie, csrf := login(t, h)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	incoming, library := filepath.Join(root, "incoming"), filepath.Join(root, "library")
	os.Mkdir(incoming, 0700)
	os.Mkdir(library, 0700)
	source := filepath.Join(incoming, "Example.Movie.2024.1080p.mkv")
	content := []byte("not real media; integration fixture")
	os.WriteFile(source, content, 0600)
	w := call(t, h, "POST", "/api/scan", map[string]string{"source": incoming, "kind": "movie"}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var inv struct {
		InventoryID string           `json:"inventoryId"`
		Items       []organizer.Item `json:"items"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	if len(inv.Items) != 1 {
		t.Fatal(w.Body.String())
	}
	id := inv.Items[0].ID
	meta := media.Metadata{Kind: "movie", Title: "Example Movie", Year: 2024, Provider: "tmdb", ID: "123"}
	w = call(t, h, "POST", "/api/plan", map[string]any{"inventoryId": inv.InventoryID, "library": library, "selections": map[string]media.Metadata{id: meta}}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var plan organizer.Plan
	if err = json.Unmarshal(w.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 1 {
		t.Fatal(w.Body.String())
	}
	w = call(t, h, "POST", "/api/apply", map[string]any{"planId": plan.ID, "confirmed": []string{}}, cookie, csrf)
	if w.Code < 400 {
		t.Fatal("empty confirmation moved media")
	}
	if _, err = os.Stat(source); err != nil {
		t.Fatal("unconfirmed source changed")
	}
	w = call(t, h, "POST", "/api/apply", map[string]any{"planId": plan.ID, "confirmed": []string{id}}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var journal organizer.Journal
	json.Unmarshal(w.Body.Bytes(), &journal)
	dest := plan.Entries[0].Destination
	if data, err := os.ReadFile(dest); err != nil || !bytes.Equal(data, content) {
		t.Fatal("destination bytes", err)
	}
	w = call(t, h, "POST", "/api/undo", map[string]string{"journalId": journal.ID}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if data, err := os.ReadFile(source); err != nil || !bytes.Equal(data, content) {
		t.Fatal("undo bytes", err)
	}
}

func TestTMDBSearchAndSeasonThroughHTTP(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer own-token" {
			t.Error("missing own bearer token")
		}
		if r.URL.Query().Get("language") != "ru-RU" {
			t.Error("missing language")
		}
		switch r.URL.Path {
		case "/search/movie":
			io.WriteString(w, `{"results":[{"id":123,"title":"Фильм","release_date":"2024-01-01"}]}`)
		case "/tv/123":
			io.WriteString(w, `{"id":123,"name":"Сериал","first_air_date":"2020-01-01"}`)
		case "/tv/123/season/1":
			io.WriteString(w, `{"episodes":[{"episode_number":1,"name":"Пилот"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	client := &TMDB{Token: "own-token", Language: "ru-RU", BaseURL: remote.URL + "/", Client: remote.Client()}
	candidates, err := client.Search(context.Background(), "Movie", "movie", 2024)
	if err != nil || len(candidates) != 1 || candidates[0].Year != 2024 {
		t.Fatal(candidates, err)
	}
	meta, err := client.Metadata(context.Background(), "tv", "123", []int{1})
	if err != nil || meta.Seasons["1"]["1"] != "Пилот" {
		t.Fatal(meta, err)
	}
}

func TestTMDBV3KeyAndBearerCredentials(t *testing.T) {
	for _, apiKey := range []bool{false, true} {
		t.Run(map[bool]string{false: "bearer", true: "v3-key"}[apiKey], func(t *testing.T) {
			token := "own-test-bearer-token"
			if apiKey {
				token = strings.Repeat("a", 32)
			}
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if apiKey {
					if r.URL.Query().Get("api_key") != token || r.Header.Get("Authorization") != "" {
						t.Error("wrong API key auth")
					}
				} else if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Query().Get("api_key") != "" {
					t.Error("wrong bearer auth")
				}
				io.WriteString(w, `{"results":[]}`)
			}))
			defer remote.Close()
			client := &TMDB{Token: token, Language: "en-US", BaseURL: remote.URL + "/", Client: remote.Client()}
			if _, err := client.Search(context.Background(), "Film", "movie", 2020); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExternalUIAssetsAndHealth(t *testing.T) {
	_, h := testApp(t)
	for _, path := range []string{"/", "/app.js", "/style.css", "/health"} {
		w := call(t, h, "GET", path, nil, nil, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}
