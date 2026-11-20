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

const (
	defaultAccentColor         = "#34c759"
	defaultStartTimeoutSeconds = 25
	minStartTimeoutSeconds     = 5
	maxStartTimeoutSeconds     = 120
)

var accentColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type managerSettings struct {
	AccentColor         string `json:"accentColor"`
	ConfirmShortcut     *bool  `json:"confirmShortcut,omitempty"`
	StartTimeoutSeconds int    `json:"startTimeoutSeconds"`
}

type managerSettingsUpdate struct {
	AccentColor         *string `json:"accentColor"`
	ConfirmShortcut     *bool   `json:"confirmShortcut"`
	StartTimeoutSeconds *int    `json:"startTimeoutSeconds"`
}

type persistedManagerSettings struct {
	AccentColor         string `json:"accent_color"`
	ConfirmShortcut     *bool  `json:"confirm_shortcut,omitempty"`
	StartTimeoutSeconds int    `json:"start_timeout_seconds,omitempty"`
}

type managerSettingsStore struct {
	mu                  sync.RWMutex
	path                string
	accentColor         string
	initialized         bool
	confirmShortcut     bool
	startTimeoutSeconds int
}

func newManagerSettingsStore(dataDir string) (*managerSettingsStore, error) {
	store := &managerSettingsStore{
		path:                filepath.Join(dataDir, "settings.json"),
		accentColor:         defaultAccentColor,
		confirmShortcut:     true,
		startTimeoutSeconds: defaultStartTimeoutSeconds,
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
	if saved.ConfirmShortcut != nil {
		store.confirmShortcut = *saved.ConfirmShortcut
	}
	if saved.StartTimeoutSeconds != 0 {
		if saved.StartTimeoutSeconds < minStartTimeoutSeconds || saved.StartTimeoutSeconds > maxStartTimeoutSeconds {
			return nil, errors.New("saved manager start timeout is outside the supported range")
		}
		store.startTimeoutSeconds = saved.StartTimeoutSeconds
	}
	return store, nil
}

func (s *managerSettingsStore) get() (managerSettings, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	confirm := s.confirmShortcut
	return managerSettings{
		AccentColor:         s.accentColor,
		ConfirmShortcut:     &confirm,
		StartTimeoutSeconds: s.startTimeoutSeconds,
	}, s.initialized
}

func (s *managerSettingsStore) set(accentColor string, warning ...*bool) error {
	color := accentColor
	var confirm *bool
	if len(warning) > 0 {
		confirm = warning[0]
	}
	return s.update(&color, confirm, nil)
}

func (s *managerSettingsStore) update(accentColor *string, warning *bool, startTimeoutSeconds *int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	color := s.accentColor
	if accentColor != nil {
		color = strings.ToLower(strings.TrimSpace(*accentColor))
		if !accentColorPattern.MatchString(color) {
			return apiError{status: http.StatusBadRequest, message: uiText("backend.settings.message001")}
		}
	}
	confirm := s.confirmShortcut
	if warning != nil {
		confirm = *warning
	}
	timeoutSeconds := s.startTimeoutSeconds
	if startTimeoutSeconds != nil {
		timeoutSeconds = *startTimeoutSeconds
		if timeoutSeconds < minStartTimeoutSeconds || timeoutSeconds > maxStartTimeoutSeconds {
			return apiError{status: http.StatusBadRequest, message: uiText("backend.settings.message002")}
		}
	}
	if s.initialized && s.accentColor == color && s.confirmShortcut == confirm && s.startTimeoutSeconds == timeoutSeconds {
		return nil
	}
	data, err := json.MarshalIndent(persistedManagerSettings{
		AccentColor: color, ConfirmShortcut: &confirm, StartTimeoutSeconds: timeoutSeconds,
	}, "", "  ")
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
	s.accentColor = color
	s.initialized = true
	s.confirmShortcut = confirm
	s.startTimeoutSeconds = timeoutSeconds
	return nil
}

func (s *managerSettingsStore) handler(auth *authManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !sameOriginRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": uiText("backend.auth.message011")})
			return
		}
		if auth == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": uiText("backend.auth.message010")})
			return
		}
		if _, ok := auth.currentUser(r); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": uiText("backend.manager.message066")})
			return
		}
		switch r.Method {
		case http.MethodGet:
			settings, initialized := s.get()
			writeJSON(w, http.StatusOK, map[string]any{"settings": settings, "initialized": initialized})
		case http.MethodPut:
			var input managerSettingsUpdate
			if err := decodeJSON(w, r, &input); err != nil {
				writeAPIError(w, err)
				return
			}
			if err := s.update(input.AccentColor, input.ConfirmShortcut, input.StartTimeoutSeconds); err != nil {
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
