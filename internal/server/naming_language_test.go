package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damuraiz/plex-helper/internal/media"
)

func TestNamingLanguageSeparateFromLocalizedMetadata(t *testing.T) {
	requests := []string{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		language := r.URL.Query().Get("language")
		requests = append(requests, r.URL.Path+":"+language)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "season") {
			fmt.Fprint(w, `{"episodes":[{"episode_number":1,"name":"Pop Tarts"}]}`)
			return
		}
		if language == "en-US" {
			fmt.Fprint(w, `{"name":"The Idol","first_air_date":"2023-06-04"}`)
		} else {
			fmt.Fprint(w, `{"name":"Кумир","first_air_date":"2023-06-04"}`)
		}
	}))
	defer mock.Close()
	client := &TMDB{Token: "fixture", Language: "ru-RU", NamingLanguage: "en-US", BaseURL: mock.URL + "/", Client: mock.Client()}
	m, err := client.Metadata(context.Background(), "tv", "135251", []int{1})
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Кумир" || m.NamingTitle != "The Idol" || m.Seasons["1"]["1"] != "Pop Tarts" {
		t.Fatal(m)
	}
	parsed := media.Parse("/fixture/The.Idol.S01E01.mkv", "auto")
	path, err := media.RelativePath(parsed, m)
	if err != nil || !strings.Contains(path, "The Idol (2023)") || strings.Contains(path, "Кумир") {
		t.Fatal(path, err)
	}
	if len(requests) != 3 || requests[2] != "/tv/135251/season/1:en-US" {
		t.Fatal(requests)
	}
}

func TestNamingSettingsPersistAndStopRequiresAuthentication(t *testing.T) {
	a, h := testApp(t)
	cookie, csrf := login(t, h)
	w := call(t, h, "POST", "/api/settings", map[string]any{"language": "ru-RU", "namingLanguage": "en-US"}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s, err := OpenStore(a.Store.dir)
	if err != nil || s.snapshot().NamingLanguage != "en-US" {
		t.Fatal(err)
	}
	for _, test := range []struct {
		cookie *http.Cookie
		csrf   string
		status int
	}{{nil, "", 401}, {cookie, "", 403}, {cookie, csrf, 409}} {
		w = call(t, h, "POST", "/api/stop", map[string]any{}, test.cookie, test.csrf)
		if w.Code != test.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
