package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/damuraiz/plex-helper/internal/media"
	"github.com/damuraiz/plex-helper/internal/organizer"
)

type MatchGroup struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Title   string   `json:"title"`
	Year    int      `json:"year,omitempty"`
	ItemIDs []string `json:"itemIds"`
	Seasons []int    `json:"seasons"`
}

// Group by filename identity, never by a user-selected translated title or
// season number. Distinct years and explicit provider IDs stay separate.
func matchGroups(inv organizer.Inventory) []MatchGroup {
	groups := []MatchGroup{}
	keys := map[string]int{}
	for _, item := range inv.Items {
		p := item.Parsed
		key := item.ID // Films and unknown titles stay independent.
		if p.Kind == "tv" && normalizedTitle(p.Title) != "" {
			key = fmt.Sprintf("tv:%s:%d:%s:%s", normalizedTitle(p.Title), p.Year, p.Provider, p.ProviderID)
		}
		index, ok := keys[key]
		if !ok {
			index = len(groups)
			keys[key] = index
			groups = append(groups, MatchGroup{ID: item.ID, Kind: p.Kind, Title: p.Title, Year: p.Year, ItemIDs: []string{}, Seasons: []int{}})
		}
		g := &groups[index]
		g.ItemIDs = append(g.ItemIDs, item.ID)
		if p.Season != nil {
			found := false
			for _, season := range g.Seasons {
				if season == *p.Season {
					found = true
				}
			}
			if !found {
				g.Seasons = append(g.Seasons, *p.Season)
			}
		}
	}
	for i := range groups {
		sort.Ints(groups[i].Seasons)
	}
	return groups
}

func normalizedTitle(title string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// Exact localized/original title and a compatible year are necessary for an
// automatic choice. More than one such result is deliberately left for review;
// TMDB ranking/popularity alone is not evidence of the right show.
func automaticCandidate(g MatchGroup, candidates []Candidate) (Candidate, bool) {
	var selected Candidate
	ids := map[string]bool{}
	query := normalizedTitle(g.Title)
	for _, c := range candidates {
		if c.Kind != g.Kind || query == "" || c.Year == 0 || (g.Year != 0 && c.Year != g.Year) {
			continue
		}
		if normalizedTitle(c.Title) != query && normalizedTitle(c.OriginalTitle) != query {
			continue
		}
		if !ids[c.ID] {
			selected = c
			ids[c.ID] = true
		}
	}
	return selected, len(ids) == 1
}

type MatchResult struct {
	Group      MatchGroup                `json:"group"`
	Selections map[string]media.Metadata `json:"selections"`
	Candidates []Candidate               `json:"candidates"`
	Issues     []organizer.Issue         `json:"issues"`
	Reason     string                    `json:"reason,omitempty"`
}

func groupItems(inv organizer.Inventory, g MatchGroup) []organizer.Item {
	ids := map[string]bool{}
	for _, id := range g.ItemIDs {
		ids[id] = true
	}
	items := []organizer.Item{}
	for _, item := range inv.Items {
		if ids[item.ID] {
			items = append(items, item)
		}
	}
	return items
}

func distributeMatch(inv organizer.Inventory, g MatchGroup, identity media.Metadata, focusID string, previous map[string]media.Metadata, automatic bool) MatchResult {
	result := MatchResult{Group: g, Selections: map[string]media.Metadata{}, Candidates: []Candidate{}, Issues: []organizer.Issue{}}
	for _, item := range groupItems(inv, g) {
		p := item.Parsed
		m := identity
		// Only the focused file's episode fields can be edited by this action.
		// Every other file keeps its own coordinates, including prior corrections.
		if item.ID != focusID || automatic {
			m.Season, m.Episodes, m.AirDate = p.Season, p.Episodes, p.AirDate
			if prior, ok := previous[item.ID]; ok && !automatic && prior.Kind == "tv" {
				m.Season, m.Episodes, m.AirDate = prior.Season, prior.Episodes, prior.AirDate
			}
		}
		// Never reuse the focused episode's title for a different episode/season.
		if m.Kind == "tv" {
			m.EpisodeTitles = nil
		}
		if automatic && len(p.Warnings) > 0 {
			result.Issues = append(result.Issues, organizer.Issue{Source: p.Path, Message: "Номер серии или имя файла требует проверки: " + strings.Join(p.Warnings, "; ")})
			continue
		}
		if m.Kind == "tv" && len(m.Seasons) > 0 && m.Season != nil {
			titles, ok := m.Seasons[strconv.Itoa(*m.Season)]
			if !ok {
				result.Issues = append(result.Issues, organizer.Issue{Source: p.Path, Message: "Сезон отсутствует в метаданных; исправьте номер сезона"})
				continue
			}
			missing := false
			for _, ep := range m.Episodes {
				if _, ok := titles[strconv.Itoa(ep)]; !ok {
					missing = true
				}
			}
			if missing {
				result.Issues = append(result.Issues, organizer.Issue{Source: p.Path, Message: "Серия отсутствует в метаданных; исправьте номер серии"})
				continue
			}
		}
		if _, err := media.RelativePath(p, m); err != nil {
			result.Issues = append(result.Issues, organizer.Issue{Source: p.Path, Message: err.Error()})
			continue
		}
		result.Selections[item.ID] = m
	}
	return result
}

func prepareMatch(ctx context.Context, tmdb *TMDB, inv organizer.Inventory, g MatchGroup) MatchResult {
	result := MatchResult{Group: g, Selections: map[string]media.Metadata{}, Candidates: []Candidate{}, Issues: []organizer.Issue{}}
	if g.Kind != "tv" && g.Kind != "movie" {
		result.Reason = "Тип файла требует проверки"
		return result
	}
	items := groupItems(inv, g)
	if len(items) == 0 {
		result.Reason = "Группа пуста"
		return result
	}
	id := ""
	if p := items[0].Parsed; p.Provider == "tmdb" {
		id = p.ProviderID
	}
	if id == "" {
		candidates, err := tmdb.Search(ctx, g.Title, g.Kind, g.Year)
		if err != nil {
			result.Reason = err.Error()
			return result
		}
		result.Candidates = candidates
		candidate, ok := automaticCandidate(g, candidates)
		if !ok {
			result.Reason = "Нет однозначного совпадения — выберите название для всей группы"
			return result
		}
		id = candidate.ID
	}
	metadata, err := tmdb.Metadata(ctx, g.Kind, id, g.Seasons)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	resolved := distributeMatch(inv, g, metadata, "", nil, true)
	resolved.Candidates = result.Candidates
	if len(resolved.Issues) > 0 {
		resolved.Reason = "Название найдено; некоторые номера серий требуют проверки"
	}
	return resolved
}

func (a *App) matchRequest(w http.ResponseWriter, r *http.Request) {
	var input struct {
		InventoryID string                    `json:"inventoryId"`
		ItemID      string                    `json:"itemId"`
		Scope       string                    `json:"scope"`
		Metadata    media.Metadata            `json:"metadata"`
		Selections  map[string]media.Metadata `json:"selections"`
	}
	if err := readJSON(w, r, &input); err != nil {
		failure(w, 400, err)
		return
	}
	a.mu.Lock()
	inv, ok := a.inventories[input.InventoryID]
	a.mu.Unlock()
	if !ok {
		failure(w, 400, errors.New("скан устарел; повторите подготовку"))
		return
	}
	var group MatchGroup
	for _, g := range matchGroups(inv) {
		for _, id := range g.ItemIDs {
			if id == input.ItemID {
				group = g
			}
		}
	}
	if group.ID == "" {
		failure(w, 400, errors.New("файл отсутствует в скане"))
		return
	}
	if r.URL.Path == "/api/prepare" {
		reply(w, 200, prepareMatch(r.Context(), a.metadataClient(a.Store.snapshot()), inv, group))
		return
	}
	if input.Scope != "group" && input.Scope != "file" {
		failure(w, 400, errors.New("выберите файл или группу"))
		return
	}
	if input.Scope == "group" && (group.Kind != "tv" || input.Metadata.Kind != "tv") {
		failure(w, 400, errors.New("групповой выбор доступен только для сериалов"))
		return
	}
	if input.Scope == "file" {
		group.ItemIDs = []string{input.ItemID}
	}
	reply(w, 200, distributeMatch(inv, group, input.Metadata, input.ItemID, input.Selections, false))
}
