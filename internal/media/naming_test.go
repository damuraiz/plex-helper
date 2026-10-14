package media

import (
	"strings"
	"testing"
)

func ptr(n int) *int { return &n }

func TestClearedProviderDoesNotReuseStaleFilenameID(t *testing.T) {
	parsed := Parse("Old.Movie.2020.{tmdb-123}.mkv", "movie")
	got, err := RelativePath(parsed, Metadata{Kind: "movie", Title: "Corrected Movie", Year: 2021})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "tmdb") {
		t.Fatal("stale filename ID reused", got)
	}
}

func TestRelativeMovieProvider(t *testing.T) {
	got, err := RelativePath(Parse("source.mkv", "auto"), Metadata{Kind: "movie", Title: "Arrival", Year: 2016, Provider: "tmdb", ID: "329865"})
	want := "Movies/Arrival (2016) {tmdb-329865}/Arrival (2016) {tmdb-329865}.mkv"
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestRelativeVersionEditionPart(t *testing.T) {
	got, err := RelativePath(Parse("Blade.Runner.1982.1080p.x265 {edition-Final Cut} - pt2.mkv", "auto"), Metadata{Kind: "movie", Title: "Blade Runner", Year: 1982})
	want := "Movies/Blade Runner (1982) {edition-Final Cut}/Blade Runner (1982) {edition-Final Cut} [1080p x265] - pt2.mkv"
	if err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestRelativeTVProviderAndTitles(t *testing.T) {
	got, err := RelativePath(Parse("Show.S01E02.mkv", "auto"), Metadata{Kind: "tv", Title: "Show", Year: 2020, Provider: "tvdb", ID: "123", EpisodeTitles: map[string]string{"2": "Pilot"}})
	want := "TV Shows/Show (2020) {tvdb-123}/Season 01/Show (2020) - s01e02 - Pilot.mkv"
	if err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = RelativePath(Parse("Show.S01E02.mkv", "auto"), Metadata{Kind: "tv", Title: "Show", Seasons: map[string]map[string]string{"1": {"2": "Pilot"}}})
	if err != nil || !strings.Contains(got, " - Pilot.mkv") {
		t.Fatalf("season title map failed: %q %v", got, err)
	}
}

func TestRelativeEpisodes(t *testing.T) {
	for _, tc := range []struct{ filename, suffix string }{
		{"Show.S01E01E02.mkv", "Season 01/Show (2020) - s01e01-e02.mkv"},
		{"Show.S00E02.mkv", "Season 00/Show (2020) - s00e02.mkv"},
	} {
		got, err := RelativePath(Parse(tc.filename, "auto"), Metadata{Kind: "tv", Title: "Show", Year: 2020})
		if err != nil || !strings.HasSuffix(got, tc.suffix) {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	if _, err := RelativePath(Parse("Show.S01E01E03.mkv", "auto"), Metadata{Kind: "tv", Title: "Show"}); err == nil {
		t.Fatal("noncontiguous episodes accepted")
	}
}

func TestRelativeExplicitEpisodeOverride(t *testing.T) {
	got, err := RelativePath(Parse("Anime - 012.mkv", "tv"), Metadata{Kind: "tv", Title: "Show", Year: 2020, Season: ptr(2), Episodes: []int{1}})
	if err != nil || !strings.HasSuffix(got, "Show (2020) - s02e01.mkv") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestRelativeDoesNotGuessSeason(t *testing.T) {
	for _, parsed := range []ParseResult{Parse("Anime - 012.mkv", "tv"), Parse("Show.2020-02-29.mkv", "tv")} {
		if _, err := RelativePath(parsed, Metadata{Kind: "tv", Title: "Show", Episodes: []int{12}}); err == nil {
			t.Fatal("missing season guessed")
		}
	}
}

func TestRelativeDates(t *testing.T) {
	got, err := RelativePath(Parse("Show.2020-02-29.mkv", "auto"), Metadata{Kind: "tv", Title: "Show", Season: ptr(1)})
	if err != nil || !strings.HasSuffix(got, "Show - 2020-02-29.mkv") {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := RelativePath(Parse("Show.S01E02.mkv", "auto"), Metadata{Kind: "tv", Title: "Show", Episodes: []int{}, AirDate: "2021-02-29"}); err == nil {
		t.Fatal("invalid air date accepted")
	}
}

func TestRelativeSafeComponents(t *testing.T) {
	parsed := Parse("source.mkv", "auto")
	parsed.Edition = "../Cut"
	got, err := RelativePath(parsed, Metadata{Kind: "movie", Title: "../A:/B\\C", Year: 2016})
	if err != nil || strings.HasPrefix(got, "/") || !strings.HasPrefix(got, "Movies/") || strings.Contains(got, "/../") {
		t.Fatalf("unsafe path: %q %v", got, err)
	}
	_, err = RelativePath(parsed, Metadata{Kind: "movie", Title: strings.Repeat("Ш", 150), Year: 2016})
	if err == nil {
		t.Fatal("oversized filename accepted")
	}
}

func TestRelativeInvalidMetadata(t *testing.T) {
	for _, meta := range []Metadata{
		{Kind: "movie", Title: "Film", Year: 2000, Provider: "tmdb", ID: "../../bad"},
		{Kind: "movie", Title: "Film", Year: 2000, Provider: "imdb", ID: "123"},
		{Kind: "movie", Title: "Film", Year: 2000, Provider: "tvdb", ID: "123"},
		{Kind: "movie", Title: "Film", Year: -1}, {Kind: "movie", Title: "Film"},
		{Kind: "tv", Title: "Show", Season: ptr(-1), Episodes: []int{1}},
		{Kind: "tv", Title: "Show", Season: ptr(1000), Episodes: []int{1}},
		{Kind: "tv", Title: "Show", Season: ptr(1), Episodes: []int{0}},
		{Kind: "tv", Title: "Show", Season: ptr(1), Episodes: []int{2, 1}},
		{Kind: "tv", Title: "Show", Season: ptr(1), Episodes: []int{1, 1}},
	} {
		if got, err := RelativePath(Parse("source.mkv", "auto"), meta); err == nil {
			t.Fatalf("invalid metadata accepted: %+v -> %q", meta, got)
		}
	}
}

func TestRelativeInvalidPartEdition(t *testing.T) {
	for _, parsed := range []ParseResult{
		Parse("Film (2000) - pt0.mkv", "auto"), Parse("Film (2000) - pt9.mkv", "auto"),
		{Path: "source.mkv", Kind: "movie", Year: 2000, Edition: strings.Repeat("x", 33)},
	} {
		if _, err := RelativePath(parsed, Metadata{Kind: "movie", Title: "Film", Year: 2000}); err == nil {
			t.Fatalf("invalid part/edition accepted: %+v", parsed)
		}
	}
}
