// Package media extracts conservative filename hints and generates Plex paths.
package media

type ParseResult struct {
	Path       string   `json:"path"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Year       int      `json:"year,omitempty"`
	Season     *int     `json:"season,omitempty"`
	Episodes   []int    `json:"episodes"`
	AirDate    string   `json:"air_date,omitempty"`
	Part       int      `json:"part,omitempty"`
	Edition    string   `json:"edition,omitempty"`
	Quality    string   `json:"quality,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	ProviderID string   `json:"provider_id,omitempty"`
	Warnings   []string `json:"warnings"`
}

type Metadata struct {
	Kind          string                       `json:"kind"`
	Title         string                       `json:"title"`
	NamingTitle   string                       `json:"naming_title,omitempty"`
	Year          int                          `json:"year,omitempty"`
	Provider      string                       `json:"provider,omitempty"`
	ID            string                       `json:"id,omitempty"`
	Season        *int                         `json:"season,omitempty"`
	Episodes      []int                        `json:"episodes,omitempty"`
	AirDate       string                       `json:"air_date,omitempty"`
	EpisodeTitles map[string]string            `json:"episode_titles,omitempty"`
	Seasons       map[string]map[string]string `json:"seasons,omitempty"`
}
