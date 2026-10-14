package media

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	unsafeRE    = regexp.MustCompile(`[\x00-\x1f\x7f<>:"/\\|?*{}]`)
	reservedRE  = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$`)
	extensionRE = regexp.MustCompile(`^\.[a-zA-Z0-9]{1,10}$`)
	isoExactRE  = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

func safeComponent(value, label string) (string, error) {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) {
		return "", fmt.Errorf("%s must be nonempty valid text", label)
	}
	value = unsafeRE.ReplaceAllString(value, " - ")
	value = strings.Trim(spaceRE.ReplaceAllString(value, " "), " .-")
	if value == "" {
		return "", fmt.Errorf("%s has no safe filename characters", label)
	}
	if reservedRE.MatchString(value) {
		value = "_" + value
	}
	return value, nil
}

func providerTag(parsed ParseResult, meta Metadata, kind string) (string, error) {
	provider, id := meta.Provider, meta.ID
	// Chosen metadata is authoritative; clearing an old filename ID must not
	// attach it to a corrected match.
	if provider == "" && id == "" {
		return "", nil
	}
	if provider != "tmdb" && provider != "imdb" && !(kind == "tv" && provider == "tvdb") {
		return "", fmt.Errorf("unsupported metadata provider %q for %s", provider, kind)
	}
	if provider == "imdb" && !imdbIDRE.MatchString(id) || provider != "imdb" && !numericIDRE.MatchString(id) {
		return "", fmt.Errorf("invalid %s metadata ID", provider)
	}
	return fmt.Sprintf(" {%s-%s}", provider, id), nil
}

// RelativePath formats resolved metadata, without guessing episode order.
func RelativePath(parsed ParseResult, meta Metadata) (string, error) {
	kind := meta.Kind
	if kind == "" {
		kind = parsed.Kind
	}
	if kind != "movie" && kind != "tv" {
		return "", fmt.Errorf("a resolved movie or tv kind is required")
	}
	name := meta.Title
	if meta.NamingTitle != "" {
		name = meta.NamingTitle
	}
	title, err := safeComponent(name, "title")
	if err != nil {
		return "", err
	}
	year := meta.Year
	if year == 0 {
		year = parsed.Year
	}
	if year == 0 && kind == "movie" {
		return "", fmt.Errorf("movie release year is required")
	}
	base := title
	if year != 0 {
		if year < 1800 || year > 2199 {
			return "", fmt.Errorf("year must be between 1800 and 2199")
		}
		base += fmt.Sprintf(" (%d)", year)
	}
	idTag, err := providerTag(parsed, meta, kind)
	if err != nil {
		return "", err
	}
	editionTag := ""
	if parsed.Edition != "" {
		edition, err := safeComponent(parsed.Edition, "edition")
		if err != nil {
			return "", err
		}
		if utf8.RuneCountInString(edition) > 32 {
			return "", fmt.Errorf("edition must be at most 32 characters")
		}
		editionTag = " {edition-" + edition + "}"
	}
	qualityTag := ""
	if parsed.Quality != "" {
		quality, err := safeComponent(parsed.Quality, "quality")
		if err != nil {
			return "", err
		}
		qualityTag = " [" + quality + "]"
	}
	partTag := ""
	if parsed.Part != 0 {
		if parsed.Part < 1 || parsed.Part > 8 {
			return "", fmt.Errorf("part must be between 1 and 8")
		}
		partTag = fmt.Sprintf(" - pt%d", parsed.Part)
	}
	for _, warning := range parsed.Warnings {
		if strings.HasPrefix(warning, "Invalid part number") {
			return "", fmt.Errorf("invalid source part number")
		}
	}
	extension := filepath.Ext(parsed.Path)
	if !extensionRE.MatchString(extension) {
		return "", fmt.Errorf("a safe source extension is required")
	}
	var result string
	if kind == "movie" {
		movie := base + idTag + editionTag
		result = filepath.Join("Movies", movie, movie+qualityTag+partTag+extension)
	} else {
		season := meta.Season
		if season == nil {
			season = parsed.Season
		}
		if season == nil || *season < 0 || *season > 999 {
			return "", fmt.Errorf("explicit season must be between 0 and 999")
		}
		episodes := meta.Episodes
		if episodes == nil {
			episodes = parsed.Episodes
		}
		episodeTag := ""
		if len(episodes) > 0 {
			for i, episode := range episodes {
				if episode < 1 || episode > 9999 || i > 0 && episode != episodes[i-1]+1 {
					return "", fmt.Errorf("episodes must form a contiguous ascending range from 1 through 9999")
				}
			}
			episodeTag = fmt.Sprintf("s%02de%02d", *season, episodes[0])
			if len(episodes) > 1 {
				episodeTag += fmt.Sprintf("-e%02d", episodes[len(episodes)-1])
			}
		} else {
			airDate := meta.AirDate
			if airDate == "" {
				airDate = parsed.AirDate
			}
			if !isoExactRE.MatchString(airDate) {
				return "", fmt.Errorf("explicit episode numbers or an ISO air date are required")
			}
			if _, err := time.Parse("2006-01-02", airDate); err != nil {
				return "", fmt.Errorf("invalid air date: %w", err)
			}
			episodeTag = airDate
		}
		titleTag := ""
		if len(episodes) == 1 {
			key := strconv.Itoa(episodes[0])
			episodeTitle := meta.EpisodeTitles[key]
			if episodeTitle == "" {
				episodeTitle = meta.Seasons[strconv.Itoa(*season)][key]
			}
			if episodeTitle != "" {
				safeTitle, err := safeComponent(episodeTitle, "episode title")
				if err != nil {
					return "", err
				}
				titleTag = " - " + safeTitle
			}
		}
		result = filepath.Join("TV Shows", base+idTag+editionTag, fmt.Sprintf("Season %02d", *season),
			base+" - "+episodeTag+titleTag+qualityTag+partTag+extension)
	}
	for _, component := range strings.Split(filepath.ToSlash(result), "/") {
		if len(component) > 255 {
			return "", fmt.Errorf("filename component exceeds 255 bytes")
		}
	}
	return filepath.ToSlash(result), nil
}
