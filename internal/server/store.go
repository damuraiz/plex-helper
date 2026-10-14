package server

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Config struct {
	SourceRoot   string `json:"sourceRoot"`
	LibraryRoot  string `json:"libraryRoot"`
	Language     string `json:"language"`
	TMDBToken    string `json:"tmdbToken,omitempty"`
	PasswordSalt string `json:"passwordSalt,omitempty"`
	PasswordHash string `json:"passwordHash,omitempty"`
}

type Store struct {
	mu         sync.Mutex
	dir        string
	config     Config
	setupToken string
}

func OpenStore(dir string) (*Store, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	// DSM's pkgvar and macOS /var may be system-managed links. Bind the trusted
	// data root once; organizer rejects links beneath each selected media root.
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, config: Config{Language: "ru-RU"}}
	path := filepath.Join(dir, "config.json")
	if info, e := os.Lstat(path); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("config must not be a symbolic link")
	}
	bytes, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(bytes, &s.config); err != nil {
			return nil, fmt.Errorf("invalid config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.config.Language == "" {
		s.config.Language = "ru-RU"
	}
	if s.config.PasswordHash == "" {
		path = filepath.Join(dir, "setup-token")
		if info, e := os.Lstat(path); e == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("setup token must not be a symbolic link")
		}
		data, e := os.ReadFile(path)
		if e == nil {
			s.setupToken = string(data)
		} else if os.IsNotExist(e) {
			s.setupToken = rand.Text()
			if e = atomicWrite(path, []byte(s.setupToken)); e != nil {
				return nil, e
			}
		} else {
			return nil, e
		}
	}
	return s, nil
}

func atomicWrite(path string, bytes []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".plex-helper-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(bytes)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err == nil {
		dir, e := os.Open(filepath.Dir(path))
		if e != nil {
			return e
		}
		defer dir.Close()
		err = dir.Sync()
	}
	return err
}

func (s *Store) snapshot() Config { s.mu.Lock(); defer s.mu.Unlock(); return s.config }

func passwordKey(password, salt string) (string, error) {
	key, err := pbkdf2.Key(sha256.New, password, []byte(salt), 210000, 32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

func (s *Store) InitPassword(password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.config.PasswordHash != "" {
		return errors.New("password already initialized; refusing to overwrite")
	}
	if len(password) < 12 || len(password) > 1024 {
		return errors.New("password must contain between 12 and 1024 bytes")
	}
	updated := s.config
	updated.PasswordSalt = rand.Text()
	key, err := passwordKey(password, updated.PasswordSalt)
	if err != nil {
		return err
	}
	updated.PasswordHash = key
	bytes, err := json.MarshalIndent(updated, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(s.dir, "config.json"), bytes); err != nil {
		return err
	}
	s.config = updated
	s.setupToken = ""
	if err = os.Remove(filepath.Join(s.dir, "setup-token")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Store) VerifyPassword(password string) bool {
	c := s.snapshot()
	if c.PasswordHash == "" || len(password) > 1024 {
		return false
	}
	key, err := passwordKey(password, c.PasswordSalt)
	return err == nil && subtle.ConstantTimeCompare([]byte(key), []byte(c.PasswordHash)) == 1
}

func (s *Store) CheckSetupToken(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setupToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.setupToken)) == 1
}

func (s *Store) SaveSettings(source, library, language string, token *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := s.config
	updated.SourceRoot = source
	updated.LibraryRoot = library
	updated.Language = language
	if token != nil {
		updated.TMDBToken = *token
	}
	bytes, err := json.MarshalIndent(updated, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(s.dir, "config.json"), bytes); err != nil {
		return err
	}
	s.config = updated
	return nil
}
