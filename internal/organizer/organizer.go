// Package organizer builds reviewable media plans and executes recoverable,
// exclusive moves. Downloaders must finish writing before a plan is applied.
package organizer

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/damuraiz/plex-helper/internal/media"
)

type Identity struct {
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	Size      int64  `json:"size"`
	ModTimeNS int64  `json:"modTimeNs"`
}
type Item struct {
	ID       string            `json:"id"`
	Parsed   media.ParseResult `json:"parsed"`
	Identity Identity          `json:"identity"`
}
type Issue struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}
type Inventory struct {
	Root   string  `json:"root"`
	Items  []Item  `json:"items"`
	Issues []Issue `json:"issues"`
}
type Entry struct {
	ID          string   `json:"id"`
	Source      string   `json:"source"`
	Destination string   `json:"destination"`
	Identity    Identity `json:"identity"`
	Role        string   `json:"role"`
	MediaID     string   `json:"mediaId"`
}
type Plan struct {
	ID        string    `json:"id"`
	Root      string    `json:"root"`
	Library   string    `json:"library"`
	CreatedAt time.Time `json:"createdAt"`
	Entries   []Entry   `json:"entries"`
	Issues    []Issue   `json:"issues"`
}
type JournalEntry struct {
	Entry
	Digest      string `json:"sha256,omitempty"`
	SourceStage string `json:"sourceStage,omitempty"`
	UndoStage   string `json:"undoStage,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}
type Journal struct {
	ID        string         `json:"id"`
	PlanID    string         `json:"planId"`
	Root      string         `json:"root"`
	Library   string         `json:"library"`
	CreatedAt time.Time      `json:"createdAt"`
	Status    string         `json:"status"`
	Entries   []JournalEntry `json:"entries"`
	Error     string         `json:"error,omitempty"`
}
type Engine struct {
	dataDir string
	mu      sync.Mutex
}

func New(dataDir string) (*Engine, error) {
	path, err := absolute(dataDir)
	if err != nil {
		return nil, err
	}
	if err = checkPath(path); err != nil {
		return nil, err
	}
	if err = durableMkdir(path, 0700); err != nil {
		return nil, err
	}
	return &Engine{dataDir: path}, nil
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func absolute(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	return filepath.Abs(path)
}
func inside(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func checkPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("absolute path required: %s", path)
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symbolic links are not supported: %s", current)
			}
			if current != path && !info.IsDir() {
				return fmt.Errorf("parent is not a directory: %s", current)
			}
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}
func identityInfo(info os.FileInfo) (Identity, error) {
	if !info.Mode().IsRegular() {
		return Identity{}, fmt.Errorf("not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, fmt.Errorf("unsupported filesystem identity")
	}
	return Identity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Size: info.Size(), ModTimeNS: info.ModTime().UnixNano()}, nil
}
func snapshot(path string) (Identity, error) {
	if err := checkPath(path); err != nil {
		return Identity{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Identity{}, err
	}
	return identityInfo(info)
}
func video(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m4v", ".ts", ".m2ts", ".wmv", ".mpg", ".mpeg", ".webm":
		return true
	}
	return false
}
func subtitle(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".srt", ".ass", ".ssa", ".vtt", ".smi":
		return true
	}
	return false
}

func (e *Engine) Scan(root, kind string) (Inventory, error) {
	var result Inventory
	result.Items = []Item{}
	result.Issues = []Issue{}
	var err error
	root, err = absolute(root)
	if err != nil {
		return result, err
	}
	result.Root = root
	if err = checkPath(root); err != nil {
		return result, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, fmt.Errorf("input must be a directory")
	}
	if kind != "auto" && kind != "movie" && kind != "tv" {
		return result, fmt.Errorf("kind must be auto, movie or tv")
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if len(result.Issues) < 1000 {
				result.Issues = append(result.Issues, Issue{path, walkErr.Error()})
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if len(result.Issues) < 1000 {
				result.Issues = append(result.Issues, Issue{path, "symbolic link skipped"})
			}
			return nil
		}
		if d.IsDir() || !video(path) {
			return nil
		}
		if len(result.Items) >= 10000 {
			result.Issues = append(result.Issues, Issue{root, "scan limited to 10,000 videos; select a smaller input directory"})
			return fs.SkipAll
		}
		id, err := snapshot(path)
		if err != nil {
			if len(result.Issues) < 1000 {
				result.Issues = append(result.Issues, Issue{path, err.Error()})
			}
			return nil
		}
		result.Items = append(result.Items, Item{ID: newID(), Parsed: media.Parse(path, kind), Identity: id})
		return nil
	})
	return result, err
}

var sidecarSuffix = regexp.MustCompile(`^(?:\.[a-zA-Z]{2,3}(?:-[a-zA-Z]{2})?)?(?:\.(?:forced|sdh|cc))*$`)

func (e *Engine) Plan(inv Inventory, library string, selections map[string]media.Metadata) (Plan, error) {
	plan := Plan{ID: newID(), Root: inv.Root, CreatedAt: time.Now().UTC(), Entries: []Entry{}, Issues: []Issue{}}
	var err error
	library, err = absolute(library)
	if err != nil {
		return plan, err
	}
	plan.Library = library
	if !filepath.IsAbs(inv.Root) || filepath.Clean(inv.Root) != inv.Root {
		return plan, fmt.Errorf("invalid inventory root")
	}
	if inside(library, inv.Root) {
		return plan, fmt.Errorf("library must be outside scanned input")
	}
	for _, path := range []string{inv.Root, library} {
		if err = checkPath(path); err != nil {
			return plan, err
		}
	}
	// A sidecar matching multiple video containers is ambiguous; leave it untouched.
	stems := map[string]int{}
	for _, item := range inv.Items {
		stems[strings.TrimSuffix(item.Parsed.Path, filepath.Ext(item.Parsed.Path))]++
	}
	for _, item := range inv.Items {
		selected, ok := selections[item.ID]
		if !ok {
			plan.Issues = append(plan.Issues, Issue{item.Parsed.Path, "choose movie or series metadata"})
			continue
		}
		source := item.Parsed.Path
		if !filepath.IsAbs(source) || filepath.Clean(source) != source || !inside(source, inv.Root) {
			return plan, fmt.Errorf("source outside scanned root: %s", source)
		}
		id, err := snapshot(source)
		if err != nil {
			return plan, err
		}
		if id != item.Identity {
			return plan, fmt.Errorf("file changed since scan: %s", source)
		}
		season := selected.Season
		if season == nil {
			season = item.Parsed.Season
		}
		if len(selected.Seasons) > 0 && season != nil {
			titles, ok := selected.Seasons[fmt.Sprint(*season)]
			if !ok {
				plan.Issues = append(plan.Issues, Issue{source, "requested season missing from selected metadata"})
				continue
			}
			requested := selected.Episodes
			if requested == nil {
				requested = item.Parsed.Episodes
			}
			missing := false
			for _, ep := range requested {
				if _, ok = titles[fmt.Sprint(ep)]; !ok {
					missing = true
				}
			}
			if missing {
				plan.Issues = append(plan.Issues, Issue{source, "episode missing from selected season"})
				continue
			}
			selected.EpisodeTitles = titles
		}
		rel, err := media.RelativePath(item.Parsed, selected)
		if err != nil {
			plan.Issues = append(plan.Issues, Issue{source, err.Error()})
			continue
		}
		if filepath.IsAbs(rel) || !inside(filepath.Join(library, rel), library) || filepath.Clean(rel) != rel {
			return plan, fmt.Errorf("unsafe naming result")
		}
		for _, r := range rel {
			if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
				return plan, fmt.Errorf("use composed Unicode characters in names; combining marks are not supported")
			}
		}
		destination := filepath.Join(library, rel)
		plan.Entries = append(plan.Entries, Entry{ID: item.ID, Source: source, Destination: destination, Identity: id, Role: "video", MediaID: item.ID})
		stem := strings.TrimSuffix(source, filepath.Ext(source))
		if stems[stem] > 1 {
			plan.Issues = append(plan.Issues, Issue{source, "same-stem video containers: ambiguous subtitles left untouched"})
			continue
		}
		neighbors, err := os.ReadDir(filepath.Dir(source))
		if err != nil {
			return plan, err
		}
		for _, neighbor := range neighbors {
			path := filepath.Join(filepath.Dir(source), neighbor.Name())
			if !subtitle(path) {
				continue
			}
			subStem := strings.TrimSuffix(path, filepath.Ext(path))
			if !strings.HasPrefix(subStem, stem) {
				continue
			}
			suffix := strings.TrimPrefix(subStem, stem)
			if !sidecarSuffix.MatchString(suffix) {
				continue
			}
			subID, err := snapshot(path)
			if err != nil {
				plan.Issues = append(plan.Issues, Issue{path, err.Error()})
				continue
			}
			subDest := strings.TrimSuffix(destination, filepath.Ext(destination)) + suffix + filepath.Ext(path)
			plan.Entries = append(plan.Entries, Entry{ID: newID(), Source: path, Destination: subDest, Identity: subID, Role: "subtitle", MediaID: item.ID})
		}
	}
	if err = validatePlan(plan); err != nil {
		return plan, err
	}
	return plan, nil
}

func pathKey(path string) string { return strings.ToLower(filepath.Clean(path)) }
func checkAliases(path string) error {
	for candidate := path; filepath.Dir(candidate) != candidate; candidate = filepath.Dir(candidate) {
		siblings, err := os.ReadDir(filepath.Dir(candidate))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, sibling := range siblings {
			if strings.EqualFold(sibling.Name(), filepath.Base(candidate)) && sibling.Name() != filepath.Base(candidate) {
				return fmt.Errorf("case alias collision: %s", candidate)
			}
		}
	}
	return nil
}
func validatePlan(plan Plan) error {
	if !filepath.IsAbs(plan.Root) || !filepath.IsAbs(plan.Library) || filepath.Clean(plan.Root) != plan.Root || filepath.Clean(plan.Library) != plan.Library {
		return fmt.Errorf("invalid plan roots")
	}
	sources, destinations, ancestors := map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, entry := range plan.Entries {
		if entry.Role != "video" && entry.Role != "subtitle" {
			return fmt.Errorf("invalid entry role")
		}
		if !filepath.IsAbs(entry.Source) || filepath.Clean(entry.Source) != entry.Source || !inside(entry.Source, plan.Root) {
			return fmt.Errorf("invalid source: %s", entry.Source)
		}
		if !filepath.IsAbs(entry.Destination) || filepath.Clean(entry.Destination) != entry.Destination || !inside(entry.Destination, plan.Library) {
			return fmt.Errorf("destination outside library")
		}
		for _, path := range []string{entry.Source, entry.Destination} {
			if err := checkPath(path); err != nil {
				return err
			}
		}
		id, err := snapshot(entry.Source)
		if err != nil {
			return err
		}
		if id != entry.Identity {
			return fmt.Errorf("source changed: %s", entry.Source)
		}
		if sources[entry.Source] {
			return fmt.Errorf("source appears twice: %s", entry.Source)
		}
		sources[entry.Source] = true
		key := pathKey(entry.Destination)
		if destinations[key] {
			return fmt.Errorf("duplicate destination: %s", entry.Destination)
		}
		destinations[key] = true
		if _, err = os.Lstat(entry.Destination); err == nil {
			return fmt.Errorf("destination already exists: %s", entry.Destination)
		} else if !os.IsNotExist(err) {
			return err
		}
		if err = checkAliases(entry.Destination); err != nil {
			return err
		}
		for path := entry.Destination; ; path = filepath.Dir(path) {
			key = pathKey(path)
			if previous, ok := ancestors[key]; ok && previous != path {
				return fmt.Errorf("planned case alias: %s and %s", previous, path)
			}
			ancestors[key] = path
			if path == plan.Library {
				break
			}
		}
	}
	for path := range destinations {
		for parent := filepath.Dir(path); parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
			if destinations[parent] {
				return fmt.Errorf("planned destination is also a directory: %s", parent)
			}
		}
	}
	return nil
}

func (e *Engine) Journals() ([]Journal, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	names, err := os.ReadDir(e.dataDir)
	if err != nil {
		return nil, err
	}
	result := []Journal{}
	for _, name := range names {
		if !strings.HasSuffix(name.Name(), ".jsonl") {
			continue
		}
		j, _, err := readJournal(filepath.Join(e.dataDir, name.Name()))
		if err != nil {
			return result, fmt.Errorf("journal %s: %w", name.Name(), err)
		}
		result = append(result, j)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}
