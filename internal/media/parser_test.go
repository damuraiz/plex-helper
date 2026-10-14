package media

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseMovieRelease(t *testing.T) {
	got := Parse("Arrival.2016.1080p.BluRay.x265-GROUP.mkv", "auto")
	if got.Kind != "movie" || got.Title != "Arrival" || got.Year != 2016 || got.Quality != "1080p x265" || !filepath.IsAbs(got.Path) {
		t.Fatalf("unexpected movie: %+v", got)
	}
}

func TestParseNumericTitles(t *testing.T) {
	for _, tc := range []struct {
		filename, title string
		year            int
	}{
		{"1917.mkv", "1917", 0}, {"1917 (2019).mkv", "1917", 2019},
		{"Blade.Runner.2049.mkv", "Blade Runner 2049", 0},
		{"Harry.Potter.Deathly.Hallows.Part.2.(2011).mkv", "Harry Potter Deathly Hallows Part 2", 2011},
	} {
		t.Run(tc.filename, func(t *testing.T) {
			got := Parse(tc.filename, "auto")
			if got.Title != tc.title || got.Year != tc.year || got.Part != 0 {
				t.Fatalf("unexpected title/part hints: %+v", got)
			}
		})
	}
}

func TestParseEpisodeNotations(t *testing.T) {
	for _, tc := range []struct {
		filename string
		season   int
		episodes []int
	}{
		{"Show.Name.(2020).S02E03-E05.720p.mkv", 2, []int{3, 4, 5}},
		{"Show.S01E01E02.mkv", 1, []int{1, 2}},
		{"Show.S01E01-02.mkv", 1, []int{1, 2}},
		{"Show.1x02.mkv", 1, []int{2}},
		{"Show.1x02-04.mkv", 1, []int{2, 3, 4}},
		{"Show.1x02-1x04.mkv", 1, []int{2, 3, 4}},
		{"Show.S00E02.mkv", 0, []int{2}},
		{"Show S01E02 1080p.mkv", 1, []int{2}},
	} {
		t.Run(tc.filename, func(t *testing.T) {
			got := Parse(tc.filename, "auto")
			if got.Kind != "tv" || got.Season == nil || *got.Season != tc.season || !reflect.DeepEqual(got.Episodes, tc.episodes) {
				t.Fatalf("unexpected episode: %+v", got)
			}
		})
	}
}

func TestParseBareEpisodeFolder(t *testing.T) {
	got := Parse("/media/TV Shows/Шоу (2020) {tmdb-123}/Season 01/s01e02.mkv", "auto")
	if got.Title != "Шоу" || got.Year != 2020 || got.Provider != "tmdb" || got.ProviderID != "123" {
		t.Fatalf("folder fallback failed: %+v", got)
	}
}

func TestParseDates(t *testing.T) {
	for _, filename := range []string{"Show.2020.02.29.mkv", "Show.29-02-2020.mkv"} {
		got := Parse(filepath.Join("/Show/Season 03", filename), "auto")
		if got.AirDate != "2020-02-29" || got.Season == nil || *got.Season != 3 || got.Kind != "tv" {
			t.Fatalf("bad date hints: %+v", got)
		}
	}
	got := Parse("Show.2021-02-29.mkv", "auto")
	if got.AirDate != "" || len(got.Warnings) == 0 {
		t.Fatalf("invalid date accepted: %+v", got)
	}
}

func TestParseProviderEditionPart(t *testing.T) {
	got := Parse("Blade Runner (1982) {imdb-tt0083658} {edition-Final Cut} - pt2.mkv", "auto")
	if got.Provider != "imdb" || got.ProviderID != "tt0083658" || got.Edition != "Final Cut" || got.Part != 2 || got.Title != "Blade Runner" {
		t.Fatalf("incorrect preservation: %+v", got)
	}
	bad := Parse("Film (2000) {tmdb-invalid}.mkv", "auto")
	if bad.ProviderID != "" || len(bad.Warnings) == 0 {
		t.Fatalf("bad ID accepted: %+v", bad)
	}
}

func TestParseVersion(t *testing.T) {
	got := Parse("Arrival (2016) - Mobile.m4v", "auto")
	if got.Quality != "Mobile" || got.Title != "Arrival" {
		t.Fatalf("version not retained: %+v", got)
	}
}

func TestParseDoesNotGuessAmbiguousEpisodes(t *testing.T) {
	for _, filename := range []string{"Anime - 012.mkv", "Show.S01E123456.mkv", "Show.S01E01-S02E02.mkv", "Show.1x02-2x04.mkv", "Show.S01E01E05-E06.mkv"} {
		got := Parse(filename, "tv")
		if len(got.Episodes) != 0 || len(got.Warnings) == 0 {
			t.Fatalf("ambiguous episode guessed: %+v", got)
		}
	}
	got := Parse("Show.S01E01-E9999.mkv", "auto")
	if len(got.Episodes) > 2 || len(got.Warnings) == 0 {
		t.Fatalf("unbounded range: %+v", got)
	}
}
