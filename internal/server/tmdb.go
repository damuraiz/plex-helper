package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/damuraiz/plex-helper/internal/media"
)

var languagePattern = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
var idPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

type Candidate struct {
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	Year          int    `json:"year"`
	Provider      string `json:"provider"`
	ID            string `json:"id"`
	Overview      string `json:"overview"`
	OriginalTitle string `json:"originalTitle,omitempty"`
}
type tmdbItem struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	Name          string `json:"name"`
	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`
	ReleaseDate   string `json:"release_date"`
	FirstAirDate  string `json:"first_air_date"`
	Overview      string `json:"overview"`
}

type TMDB struct {
	Token, Language, BaseURL string
	Client                   *http.Client
}

func newTMDB(c Config) *TMDB {
	return &TMDB{Token: c.TMDBToken, Language: c.Language, BaseURL: "https://api.themoviedb.org/3/", Client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return errors.New("TMDB redirects are not permitted")
	}}}
}

func (t *TMDB) get(ctx context.Context, path string, params url.Values, out any) error {
	if t.Token == "" {
		return errors.New("добавьте свой TMDB API Read Access Token в настройках")
	}
	if params == nil {
		params = url.Values{}
	}
	params.Set("language", t.Language)
	// Accept the user's v3 API key or bearer Read Access Token. Requests and
	// credentials are never included in response errors or application logs.
	isAPIKey := regexp.MustCompile(`^[a-fA-F0-9]{32}$`).MatchString(t.Token)
	if isAPIKey {
		params.Set("api_key", t.Token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.BaseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return errors.New("invalid metadata request")
	}
	if !isAPIKey {
		req.Header.Set("Authorization", "Bearer "+t.Token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "plex-helper/0.1")
	response, err := t.Client.Do(req)
	if err != nil {
		return errors.New("TMDB недоступен: проверьте соединение и токен")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("TMDB HTTP %d: проверьте токен или повторите позже", response.StatusCode)
	}
	bytes, err := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	if err != nil {
		return errors.New("ошибка чтения TMDB")
	}
	if len(bytes) > 4<<20 {
		return errors.New("TMDB response too large")
	}
	if err = json.Unmarshal(bytes, out); err != nil {
		return errors.New("invalid TMDB response")
	}
	return nil
}

func itemFields(item tmdbItem, kind string) (string, int) {
	title, date := item.Title, item.ReleaseDate
	if kind == "tv" {
		title, date = item.Name, item.FirstAirDate
	}
	year := 0
	if len(date) >= 4 {
		year, _ = strconv.Atoi(date[:4])
	}
	return title, year
}

func (t *TMDB) Search(ctx context.Context, query, kind string, year int) ([]Candidate, error) {
	if kind != "movie" && kind != "tv" {
		return nil, errors.New("choose movie or tv")
	}
	if query == "" || len(query) > 500 {
		return nil, errors.New("укажите название для поиска")
	}
	params := url.Values{"query": {query}, "include_adult": {"false"}}
	if year != 0 {
		if year < 1870 || year > 2100 {
			return nil, errors.New("invalid year")
		}
		if kind == "movie" {
			params.Set("year", strconv.Itoa(year))
		} else {
			params.Set("first_air_date_year", strconv.Itoa(year))
		}
	}
	var payload struct {
		Results []tmdbItem `json:"results"`
	}
	if err := t.get(ctx, "search/"+kind, params, &payload); err != nil {
		return nil, err
	}
	results := make([]Candidate, 0, len(payload.Results))
	for _, item := range payload.Results {
		title, year := itemFields(item, kind)
		original := item.OriginalTitle
		if kind == "tv" {
			original = item.OriginalName
		}
		results = append(results, Candidate{Kind: kind, Title: title, Year: year, Provider: "tmdb", ID: strconv.Itoa(item.ID), Overview: item.Overview, OriginalTitle: original})
	}
	return results, nil
}

func (t *TMDB) Metadata(ctx context.Context, kind, id string, seasons []int) (media.Metadata, error) {
	result := media.Metadata{}
	if (kind != "tv" && kind != "movie") || !idPattern.MatchString(id) || len(id) > 12 {
		return result, errors.New("invalid kind or TMDB ID")
	}
	var item tmdbItem
	if err := t.get(ctx, kind+"/"+id, nil, &item); err != nil {
		return result, err
	}
	title, year := itemFields(item, kind)
	result = media.Metadata{Kind: kind, Title: title, Year: year, Provider: "tmdb", ID: id}
	if kind == "tv" && len(seasons) > 0 {
		if len(seasons) > 50 {
			return result, errors.New("too many seasons in one request")
		}
		result.Seasons = make(map[string]map[string]string)
		for _, season := range seasons {
			if season < 0 || season > 999 {
				return result, errors.New("invalid season")
			}
			key := strconv.Itoa(season)
			if _, ok := result.Seasons[key]; ok {
				continue
			}
			var payload struct {
				Episodes []struct {
					Number int    `json:"episode_number"`
					Name   string `json:"name"`
				} `json:"episodes"`
			}
			if err := t.get(ctx, "tv/"+id+"/season/"+key, nil, &payload); err != nil {
				return result, err
			}
			result.Seasons[key] = make(map[string]string)
			for _, ep := range payload.Episodes {
				result.Seasons[key][strconv.Itoa(ep.Number)] = ep.Name
			}
		}
	}
	return result, nil
}
