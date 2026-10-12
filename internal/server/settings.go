package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const defaultAccentColor = "#34c759"

var accentColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type managerSettings struct {
	AccentColor string `json:"accentColor"`
}

type persistedManagerSettings struct {
	AccentColor string `json:"accent_color"`
}

type managerSettingsStore struct {
	mu          sync.RWMutex
	path        string
	accentColor string
	initialized bool
}

func newManagerSettingsStore(dataDir string) (*managerSettingsStore, error) {
	store := &managerSettingsStore{
		path:        filepath.Join(dataDir, "settings.json"),
		accentColor: defaultAccentColor,
	}
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	var saved persistedManagerSettings
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}
	if !accentColorPattern.MatchString(saved.AccentColor) {
		return nil, errors.New("saved manager accent color is invalid")
	}
	store.accentColor = strings.ToLower(saved.AccentColor)
	store.initialized = true
	return store, nil
}

func (s *managerSettingsStore) get() (managerSettings, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return managerSettings{AccentColor: s.accentColor}, s.initialized
}

func (s *managerSettingsStore) set(accentColor string) error {
	accentColor = strings.ToLower(strings.TrimSpace(accentColor))
	if !accentColorPattern.MatchString(accentColor) {
		return apiError{status: http.StatusBadRequest, message: "主色需要是完整的六位十六进制颜色值。"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initialized && s.accentColor == accentColor {
		return nil
	}
	data, err := json.MarshalIndent(persistedManagerSettings{AccentColor: accentColor}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, s.path); err != nil {
		return err
	}
	s.accentColor = accentColor
	s.initialized = true
	return nil
}

func (s *managerSettingsStore) handler(auth *authManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !sameOriginRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "跨站请求已拒绝。"})
			return
		}
		if auth == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "请先登录。"})
			return
		}
		if _, ok := auth.currentUser(r); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "登录状态已失效，请重新登录。"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			settings, initialized := s.get()
			writeJSON(w, http.StatusOK, map[string]any{"settings": settings, "initialized": initialized})
		case http.MethodPut:
			var input managerSettings
			if err := decodeJSON(w, r, &input); err != nil {
				writeAPIError(w, err)
				return
			}
			if err := s.set(input.AccentColor); err != nil {
				writeAPIError(w, err)
				return
			}
			settings, _ := s.get()
			writeJSON(w, http.StatusOK, map[string]any{"settings": settings, "initialized": true})
		default:
			methodNotAllowed(w)
		}
	})
}
