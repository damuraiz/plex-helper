package server

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/damuraiz/plex-helper/internal/media"
	"github.com/damuraiz/plex-helper/internal/organizer"
)

//go:embed web/*
var assets embed.FS

type session struct {
	CSRF      string
	Expires   time.Time
	User      string
	AuthError string
}
type attempts struct {
	Count int
	Since time.Time
}
type App struct {
	Store          *Store
	Engine         *organizer.Engine
	Version        string
	mu             sync.Mutex
	sessions       map[string]session
	attempts       map[string]attempts
	inventories    map[string]organizer.Inventory
	plans          map[string]organizer.Plan
	operation      sync.Mutex
	dsm            *dsmAuthenticator
	csrfKey        string
	metadataClient func(Config) *TMDB
}

func New(dataDir, version string) (*App, error) {
	return newApp(dataDir, version, false)
}

func NewDSM(dataDir, version string) (*App, error) {
	return newApp(dataDir, version, true)
}

func newApp(dataDir, version string, dsm bool) (*App, error) {
	store, err := openStore(dataDir, !dsm)
	if err != nil {
		return nil, err
	}
	engine, err := organizer.New(store.dir)
	if err != nil {
		return nil, err
	}
	a := &App{Store: store, Engine: engine, Version: version, sessions: make(map[string]session), attempts: make(map[string]attempts), inventories: make(map[string]organizer.Inventory), plans: make(map[string]organizer.Plan), csrfKey: rand.Text()}
	a.metadataClient = newTMDB
	if dsm {
		a.dsm = newDSMAuthenticator()
	}
	return a, nil
}

func reply(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
func failure(w http.ResponseWriter, status int, err error) {
	reply(w, status, map[string]string{"error": err.Error()})
}
func readJSON(w http.ResponseWriter, r *http.Request, out any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return errors.New("invalid JSON request")
	}
	var tail any
	if err := decoder.Decode(&tail); err != io.EOF {
		return errors.New("request has trailing JSON")
	}
	return nil
}

func (a *App) currentSession(r *http.Request) (session, bool) {
	if a.dsm != nil {
		return a.dsmSession(r)
	}
	cookie, err := r.Cookie("plex_helper_session")
	if err != nil {
		return session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[cookie.Value]
	if ok && time.Now().Before(s.Expires) {
		return s, true
	}
	delete(a.sessions, cookie.Value)
	return session{}, false
}
func (a *App) loginSession(w http.ResponseWriter, r *http.Request) {
	token := rand.Text()
	s := session{CSRF: rand.Text(), Expires: time.Now().Add(12 * time.Hour)}
	a.mu.Lock()
	for key, value := range a.sessions {
		if time.Now().After(value.Expires) {
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= 32 {
		a.sessions = make(map[string]session)
	}
	a.sessions[token] = s
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "plex_helper_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600})
	reply(w, 200, map[string]any{"authenticated": true, "csrf": s.CSRF})
}

func (a *App) allowLogin(r *http.Request) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for key, value := range a.attempts {
		if now.Sub(value.Since) > time.Minute {
			delete(a.attempts, key)
		}
	}
	if len(a.attempts) > 1000 {
		return false
	}
	value := a.attempts[ip]
	if value.Since.IsZero() {
		value.Since = now
	}
	value.Count++
	a.attempts[ip] = value
	return value.Count <= 5
}

func (a *App) Handler() http.Handler {
	static, _ := fs.Sub(assets, "web")
	files := http.FileServer(http.FS(static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		if r.URL.Path == "/health" {
			reply(w, 200, map[string]string{"status": "ok", "version": a.Version})
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Method != "GET" {
				failure(w, 405, errors.New("method not allowed"))
				return
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" && r.Method != "POST" {
			failure(w, 405, errors.New("method not allowed"))
			return
		}
		// Reject cross-origin writes even before login. DNS rebinding cannot supply this origin.
		if origin := r.Header.Get("Origin"); origin != "" {
			allowed := origin == "http://"+r.Host || origin == "https://"+r.Host
			if a.dsm != nil {
				allowed = origin == r.Header.Get("X-Plex-Helper-Protocol")+"://"+r.Host
			}
			if !allowed {
				failure(w, 403, errors.New("cross-origin request rejected"))
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			failure(w, 403, errors.New("cross-site request rejected"))
			return
		}
		if r.URL.Path == "/api/state" && r.Method == "GET" {
			a.state(w, r)
			return
		}
		if (r.URL.Path == "/api/login" || r.URL.Path == "/api/setup") && r.Method == "POST" {
			if a.dsm != nil {
				failure(w, 404, errors.New("вход выполняется через DSM"))
				return
			}
			a.authenticate(w, r)
			return
		}
		s, ok := a.currentSession(r)
		if !ok {
			failure(w, 401, errors.New("войдите в приложение"))
			return
		}
		if r.Method == "POST" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(s.CSRF)) != 1 {
			failure(w, 403, errors.New("invalid CSRF token"))
			return
		}
		if r.Method == "GET" {
			switch r.URL.Path {
			case "/api/browse":
				a.browse(w, r)
			case "/api/journals":
				journals, err := a.Engine.Journals()
				if err != nil {
					failure(w, 500, err)
				} else {
					reply(w, 200, map[string]any{"journals": journals})
				}
			default:
				failure(w, 404, errors.New("not found"))
			}
			return
		}
		a.post(w, r)
	})
}

func (a *App) state(w http.ResponseWriter, r *http.Request) {
	c := a.Store.snapshot()
	s, ok := a.currentSession(r)
	result := map[string]any{"authenticated": ok, "setupRequired": a.dsm == nil && c.PasswordHash == "", "version": a.Version, "authMode": "local"}
	if a.dsm != nil {
		result["authMode"] = "dsm"
		if !ok {
			result["authError"] = s.AuthError
		}
	}
	if ok {
		result["user"] = s.User
		result["csrf"] = s.CSRF
		result["settings"] = map[string]any{"sourceRoot": c.SourceRoot, "libraryRoot": c.LibraryRoot, "language": c.Language, "tmdbConfigured": c.TMDBToken != ""}
	}
	reply(w, 200, result)
}

func (a *App) authenticate(w http.ResponseWriter, r *http.Request) {
	if !a.allowLogin(r) {
		failure(w, 429, errors.New("слишком много попыток; повторите через минуту"))
		return
	}
	var input struct {
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if err := readJSON(w, r, &input); err != nil {
		failure(w, 400, err)
		return
	}
	if r.URL.Path == "/api/setup" {
		if !a.Store.CheckSetupToken(input.Token) {
			failure(w, 403, errors.New("неверный код первоначальной настройки"))
			return
		}
		if err := a.Store.InitPassword(input.Password); err != nil {
			failure(w, 400, err)
			return
		}
	} else if !a.Store.VerifyPassword(input.Password) {
		failure(w, 401, errors.New("неверный пароль"))
		return
	}
	a.loginSession(w, r)
}

func canonicalDirectory(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("укажите абсолютный путь к папке")
	}
	path, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return path, nil
}

func (a *App) browse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	path, err := canonicalDirectory(path)
	if err != nil {
		failure(w, 400, err)
		return
	}
	files, err := os.ReadDir(path)
	if err != nil {
		failure(w, 403, errors.New("нет доступа к папке; настройте права пользователя plex-helper в DSM"))
		return
	}
	dirs := make([]map[string]string, 0)
	for _, file := range files {
		if file.IsDir() && !strings.HasPrefix(file.Name(), ".") {
			dirs = append(dirs, map[string]string{"name": file.Name(), "path": filepath.Join(path, file.Name())})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i]["name"] < dirs[j]["name"] })
	reply(w, 200, map[string]any{"path": path, "parent": filepath.Dir(path), "dirs": dirs})
}

func (a *App) post(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/logout":
		if a.dsm != nil {
			failure(w, 409, errors.New("для завершения сессии выйдите из DSM"))
			return
		}
		cookie, _ := r.Cookie("plex_helper_session")
		a.mu.Lock()
		if cookie != nil {
			delete(a.sessions, cookie.Value)
		}
		a.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "plex_helper_session", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		reply(w, 200, map[string]bool{"ok": true})
	case "/api/settings":
		var input struct {
			SourceRoot, LibraryRoot, Language string
			TMDBToken                         *string `json:"tmdbToken"`
			ClearTMDBToken                    bool    `json:"clearTmdbToken"`
		}
		if err := readJSON(w, r, &input); err != nil {
			failure(w, 400, err)
			return
		}
		if !languagePattern.MatchString(input.Language) {
			failure(w, 400, errors.New("language must be ru-RU or en-US, etc."))
			return
		}
		for _, path := range []string{input.SourceRoot, input.LibraryRoot} {
			if path != "" {
				if _, err := canonicalDirectory(path); err != nil {
					failure(w, 400, err)
					return
				}
			}
		}
		if input.TMDBToken != nil && *input.TMDBToken == "" {
			input.TMDBToken = nil
		}
		if input.ClearTMDBToken {
			empty := ""
			input.TMDBToken = &empty
		}
		if input.TMDBToken != nil && (len(*input.TMDBToken) > 4096 || strings.ContainsAny(*input.TMDBToken, "\r\n")) {
			failure(w, 400, errors.New("invalid token"))
			return
		}
		if err := a.Store.SaveSettings(input.SourceRoot, input.LibraryRoot, input.Language, input.TMDBToken); err != nil {
			failure(w, 500, err)
			return
		}
		a.mu.Lock()
		a.inventories = make(map[string]organizer.Inventory)
		a.plans = make(map[string]organizer.Plan)
		a.mu.Unlock()
		reply(w, 200, map[string]bool{"ok": true})
	case "/api/scan":
		var input struct{ Source, Kind string }
		if err := readJSON(w, r, &input); err != nil {
			failure(w, 400, err)
			return
		}
		if input.Kind != "auto" && input.Kind != "movie" && input.Kind != "tv" {
			failure(w, 400, errors.New("invalid media kind"))
			return
		}
		root, err := canonicalDirectory(input.Source)
		if err != nil {
			failure(w, 400, err)
			return
		}
		if !a.operation.TryLock() {
			failure(w, 409, errors.New("другая операция ещё выполняется"))
			return
		}
		defer a.operation.Unlock()
		inventory, err := a.Engine.Scan(root, input.Kind)
		if err != nil {
			failure(w, 400, err)
			return
		}
		id := rand.Text()
		a.mu.Lock()
		if len(a.inventories) >= 4 {
			a.inventories = make(map[string]organizer.Inventory)
			a.plans = make(map[string]organizer.Plan)
		}
		a.inventories[id] = inventory
		a.mu.Unlock()
		reply(w, 200, map[string]any{"inventoryId": id, "root": inventory.Root, "items": inventory.Items, "issues": inventory.Issues, "groups": matchGroups(inventory)})
	case "/api/prepare", "/api/match":
		a.matchRequest(w, r)
	case "/api/search":
		var input struct {
			Query, Kind string
			Year        int
		}
		if err := readJSON(w, r, &input); err != nil {
			failure(w, 400, err)
			return
		}
		candidates, err := a.metadataClient(a.Store.snapshot()).Search(r.Context(), input.Query, input.Kind, input.Year)
		if err != nil {
			failure(w, 400, err)
		} else {
			reply(w, 200, map[string]any{"candidates": candidates})
		}
	case "/api/metadata":
		var input struct {
			Kind, ID string
			Seasons  []int
		}
		if err := readJSON(w, r, &input); err != nil {
			failure(w, 400, err)
			return
		}
		metadata, err := a.metadataClient(a.Store.snapshot()).Metadata(r.Context(), input.Kind, input.ID, input.Seasons)
		if err != nil {
			failure(w, 400, err)
		} else {
			reply(w, 200, metadata)
		}
	case "/api/plan":
		var input struct {
			InventoryID string                    `json:"inventoryId"`
			Library     string                    `json:"library"`
			Selections  map[string]media.Metadata `json:"selections"`
		}
		if err := readJSON(w, r, &input); err != nil {
			failure(w, 400, err)
			return
		}
		a.mu.Lock()
		inventory, ok := a.inventories[input.InventoryID]
		a.mu.Unlock()
		if !ok {
			failure(w, 400, errors.New("скан устарел; повторите сканирование"))
			return
		}
		library, err := canonicalDirectory(input.Library)
		if err != nil {
			failure(w, 400, err)
			return
		}
		plan, err := a.Engine.Plan(inventory, library, input.Selections)
		if err != nil {
			failure(w, 400, err)
			return
		}
		a.mu.Lock()
		if len(a.plans) >= 8 {
			a.plans = make(map[string]organizer.Plan)
		}
		a.plans[plan.ID] = plan
		a.mu.Unlock()
		reply(w, 200, plan)
	case "/api/apply":
		var input struct {
			PlanID    string   `json:"planId"`
			Confirmed []string `json:"confirmed"`
		}
		if err := readJSON(w, r, &input); err != nil {
			failure(w, 400, err)
			return
		}
		if !a.operation.TryLock() {
			failure(w, 409, errors.New("другая операция ещё выполняется"))
			return
		}
		defer a.operation.Unlock()
		a.mu.Lock()
		plan, ok := a.plans[input.PlanID]
		a.mu.Unlock()
		if !ok {
			failure(w, 400, errors.New("план устарел; создайте новый предварительный просмотр"))
			return
		}
		journal, err := a.Engine.Apply(plan, input.Confirmed)
		if journal.ID != "" {
			a.mu.Lock()
			delete(a.plans, input.PlanID)
			a.mu.Unlock()
		}
		if err != nil {
			reply(w, 409, map[string]any{"error": err.Error(), "journal": journal})
		} else {
			reply(w, 200, journal)
		}
	case "/api/undo":
		var input struct {
			JournalID string `json:"journalId"`
		}
		if err := readJSON(w, r, &input); err != nil {
			failure(w, 400, err)
			return
		}
		if !a.operation.TryLock() {
			failure(w, 409, errors.New("другая операция ещё выполняется"))
			return
		}
		defer a.operation.Unlock()
		journal, err := a.Engine.Undo(input.JournalID)
		if err != nil {
			reply(w, 409, map[string]any{"error": err.Error(), "journal": journal})
		} else {
			reply(w, 200, journal)
		}
	default:
		failure(w, 404, errors.New("not found"))
	}
}
