package media

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	yearRE            = regexp.MustCompile(`18[0-9]{2}|19[0-9]{2}|20[0-9]{2}`)
	seasonEpisodeRE   = regexp.MustCompile(`(?i)s([0-9]{1,4})e([0-9]{1,4})((?:e[0-9]{1,4})*)(?:[ -]e([0-9]{1,4})|-([0-9]{1,4}))?`)
	xEpisodeRE        = regexp.MustCompile(`(?i)([0-9]{1,4})x([0-9]{1,4})(?:-(?:([0-9]{1,4})x)?([0-9]{1,4}))?`)
	repeatedEpisodeRE = regexp.MustCompile(`(?i)e([0-9]+)`)
	isoDateRE         = regexp.MustCompile(`([0-9]{4})[-. ]([0-9]{2})[-. ]([0-9]{2})`)
	dayDateRE         = regexp.MustCompile(`([0-9]{2})[-. ]([0-9]{2})[-. ]([0-9]{4})`)
	partRE            = regexp.MustCompile(`(?i)(?:cd|disc|disk|dvd|part|pt)[ ._-]?([0-9]+)`)
	qualityRE         = regexp.MustCompile(`(?i)2160p|1080[pi]|720p|480p|576p|4k|uhd|hdr10\+?|hdr|dolby[ ._-]?vision|hevc|[xh][ ._-]?26[45]|av1|remux`)
	noiseRE           = regexp.MustCompile(`(?i)2160p|1080[pi]|720p|480p|576p|4k|uhd|hdr10\+?|hdr|hevc|[xh][ ._-]?26[45]|av1|remux|blu[ ._-]?ray|b[dr]rip|web[ ._-]?(?:dl|rip)|hdtv|dvdrip|aac|dts|ddp`)
	providerRE        = regexp.MustCompile(`(?i)\{(tmdb|tvdb|imdb)-([^{}]+)\}`)
	editionRE         = regexp.MustCompile(`(?i)\{edition-([^{}]+)\}`)
	seasonFolderRE    = regexp.MustCompile(`(?i)^season[ ._-]*([0-9]{1,4})$`)
	spaceRE           = regexp.MustCompile(`\s+`)
	numericIDRE       = regexp.MustCompile(`^[1-9][0-9]*$`)
	imdbIDRE          = regexp.MustCompile(`^tt[0-9]+$`)
	versionRE         = regexp.MustCompile(`(?:18[0-9]{2}|19[0-9]{2}|20[0-9]{2})\)?\s+-\s+(.+)$`)
)

func alphaNumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// RE2 has no lookarounds. Explicit boundaries prevent partial numeric matches.
func boundedMatches(re *regexp.Regexp, text string) [][]int {
	var result [][]int
	for _, match := range re.FindAllStringSubmatchIndex(text, -1) {
		if match[0] > 0 && alphaNumeric(text[match[0]-1]) {
			continue
		}
		if match[1] < len(text) && alphaNumeric(text[match[1]]) {
			continue
		}
		result = append(result, match)
	}
	return result
}

func group(text string, match []int, number int) string {
	start, end := match[number*2], match[number*2+1]
	if start < 0 {
		return ""
	}
	return text[start:end]
}

func number(text string) int {
	n, _ := strconv.Atoi(text)
	return n
}

func cleanHint(text string) string {
	text = strings.NewReplacer(".", " ", "_", " ").Replace(text)
	return strings.Trim(spaceRE.ReplaceAllString(text, " "), " -[](){}")
}

func titleYear(text string) (string, int) {
	if noise := boundedMatches(noiseRE, text); len(noise) > 0 {
		text = text[:noise[0][0]]
	}
	year := 0
	if matches := boundedMatches(yearRE, text); len(matches) > 0 {
		match := matches[len(matches)-1]
		candidate := number(text[match[0]:match[1]])
		parenthesized := match[0] > 0 && text[match[0]-1] == '('
		if cleanHint(text[:match[0]]) != "" && (parenthesized || candidate <= time.Now().Year()+1) {
			year = candidate
			text = text[:match[0]]
		}
	}
	return cleanHint(text), year
}

func providerHints(text string, result *ParseResult) string {
	var providers, ids []string
	for _, match := range providerRE.FindAllStringSubmatch(text, -1) {
		provider, id := strings.ToLower(match[1]), match[2]
		if provider == "imdb" && imdbIDRE.MatchString(id) || provider != "imdb" && numericIDRE.MatchString(id) {
			providers, ids = append(providers, provider), append(ids, id)
		} else {
			result.Warnings = append(result.Warnings, "Invalid provider ID in filename")
		}
	}
	if len(ids) == 1 && result.Provider == "" {
		result.Provider, result.ProviderID = providers[0], ids[0]
	} else if len(ids) > 1 {
		result.Warnings = append(result.Warnings, "Multiple provider IDs require review")
	}
	return providerRE.ReplaceAllString(text, "")
}

func seasonFolder(name string) *int {
	if strings.EqualFold(name, "Specials") {
		season := 0
		return &season
	}
	if match := seasonFolderRE.FindStringSubmatch(name); match != nil {
		season := number(match[1])
		return &season
	}
	return nil
}

func episodeNumbers(text string, match []int, x bool, result *ParseResult) {
	season, first := number(group(text, match, 1)), number(group(text, match, 2))
	result.Season = &season
	result.Episodes = []int{first}
	endText := group(text, match, 3)
	if x {
		if endText != "" && number(endText) != season {
			result.Episodes = nil
			result.Warnings = append(result.Warnings, "Episode range crosses seasons; explicit episode metadata required")
			return
		}
		endText = group(text, match, 4)
	} else {
		for _, repeated := range repeatedEpisodeRE.FindAllStringSubmatch(endText, -1) {
			result.Episodes = append(result.Episodes, number(repeated[1]))
		}
		endText = group(text, match, 4)
		if endText == "" {
			endText = group(text, match, 5)
		}
	}
	if endText != "" {
		end := number(endText)
		for i, episode := range result.Episodes {
			if episode != first+i || end < episode {
				result.Episodes = nil
				result.Warnings = append(result.Warnings, "Ambiguous mixed episode list/range requires explicit metadata")
				return
			}
		}
		if end < first || end-first > 1000 {
			result.Episodes = []int{first, end}
			result.Warnings = append(result.Warnings, "Invalid or excessive episode range")
		} else {
			result.Episodes = nil
			for episode := first; episode <= end; episode++ {
				result.Episodes = append(result.Episodes, episode)
			}
		}
	}
}

// Parse provides hints only; a filename is never a confirmed metadata match.
func Parse(path, kind string) ParseResult {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	result := ParseResult{Path: abs, Episodes: []int{}, Warnings: []string{}}
	if kind != "" && kind != "auto" && kind != "movie" && kind != "tv" {
		result.Kind = "unknown"
		result.Warnings = append(result.Warnings, "kind must be auto, movie, or tv")
		return result
	}
	base := filepath.Base(path)
	raw := strings.TrimSuffix(base, filepath.Ext(base))
	raw = providerHints(raw, &result)
	if edition := editionRE.FindStringSubmatch(raw); edition != nil {
		result.Edition = strings.TrimSpace(edition[1])
	}
	raw = editionRE.ReplaceAllString(raw, "")
	seen := map[string]bool{}
	var qualities []string
	for _, match := range boundedMatches(qualityRE, raw) {
		quality := cleanHint(raw[match[0]:match[1]])
		if !seen[strings.ToLower(quality)] {
			qualities = append(qualities, quality)
			seen[strings.ToLower(quality)] = true
		}
	}
	result.Quality = strings.Join(qualities, " ")
	if parts := boundedMatches(partRE, raw); len(parts) > 0 {
		part := parts[0]
		// A numbered sequel before its release year is part of the title.
		if len(boundedMatches(yearRE, raw[part[1]:])) == 0 {
			result.Part = number(group(raw, part, 1))
			if result.Part < 1 || result.Part > 8 {
				result.Warnings = append(result.Warnings, "Invalid part number; Plex supports 1 through 8")
			}
			raw = raw[:part[0]] + raw[part[1]:]
		}
	}
	titleText := raw
	se := boundedMatches(seasonEpisodeRE, raw)
	x := false
	if len(se) == 0 {
		se = boundedMatches(xEpisodeRE, raw)
		x = true
	}
	if len(se) > 0 {
		result.Kind = "tv"
		titleText = raw[:se[0][0]]
		if len(se) > 1 {
			result.Warnings = append(result.Warnings, "Multiple separate episode markers require explicit episode metadata")
		} else {
			episodeNumbers(raw, se[0], x, &result)
		}
	} else {
		result.Kind = "movie"
		if result.Quality == "" {
			if version := versionRE.FindStringSubmatch(raw); version != nil {
				result.Quality = cleanHint(version[1])
			}
		}
		dates := boundedMatches(isoDateRE, raw)
		dayFirst := false
		if len(dates) == 0 {
			dates = boundedMatches(dayDateRE, raw)
			dayFirst = true
		}
		if len(dates) > 0 {
			result.Kind = "tv"
			match := dates[0]
			values := []string{group(raw, match, 1), group(raw, match, 2), group(raw, match, 3)}
			if dayFirst {
				values[0], values[2] = values[2], values[0]
			}
			iso := strings.Join(values, "-")
			if _, err := time.Parse("2006-01-02", iso); err != nil {
				result.Warnings = append(result.Warnings, "Invalid air date in filename")
			} else {
				result.AirDate = iso
			}
			titleText = raw[:match[0]]
		}
	}
	if kind == "movie" || kind == "tv" {
		if kind == "movie" && result.Kind == "tv" {
			result.Warnings = append(result.Warnings, "Episode/date hints conflict with requested movie type")
		}
		result.Kind = kind
	}
	result.Title, result.Year = titleYear(titleText)
	if result.Kind == "tv" {
		parent := filepath.Dir(abs)
		folderSeason := seasonFolder(filepath.Base(parent))
		if result.Season == nil {
			result.Season = folderSeason
		}
		if result.Title == "" {
			if folderSeason != nil {
				parent = filepath.Dir(parent)
			}
			parentText := providerHints(filepath.Base(parent), &result)
			if edition := editionRE.FindStringSubmatch(parentText); edition != nil && result.Edition == "" {
				result.Edition = strings.TrimSpace(edition[1])
			}
			result.Title, result.Year = titleYear(editionRE.ReplaceAllString(parentText, ""))
		}
		if len(result.Episodes) == 0 && result.AirDate == "" {
			result.Warnings = append(result.Warnings, "No explicit episodes or valid air date; absolute ordering requires review")
		}
	}
	if result.Title == "" {
		result.Kind = "unknown"
		result.Warnings = append(result.Warnings, "No title hint found")
	}
	return result
}
