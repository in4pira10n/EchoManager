package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type shortcutStep struct {
	InstanceID string `json:"instanceId"`
	Action     string `json:"action"`
	Rule       string `json:"rule"`
}

type shortcut struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Icon  string         `json:"icon"`
	Color string         `json:"color"`
	Steps []shortcutStep `json:"steps"`
}

type shortcutRun struct {
	Status    string   `json:"status"`
	Completed int      `json:"completed"`
	Total     int      `json:"total"`
	Messages  []string `json:"messages"`
	OpenIDs   []string `json:"openIds"`
}

type shortcutStore struct {
	mu      sync.Mutex
	path    string
	items   []shortcut
	runs    map[string]shortcutRun
	manager *instanceManager
}

var symbolNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,119}$`)

func newShortcutStore(dataDir string, manager *instanceManager) (*shortcutStore, error) {
	s := &shortcutStore{path: filepath.Join(dataDir, "shortcuts.json"), items: []shortcut{}, runs: make(map[string]shortcutRun), manager: manager}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &s.items); err != nil {
		return nil, fmt.Errorf("read shortcuts: %w", err)
	}
	seen := map[string]bool{}
	for _, item := range s.items {
		if !idPattern.MatchString(item.ID) || seen[item.ID] {
			return nil, errors.New("invalid shortcut IDs")
		}
		if err := validateShortcut(item); err != nil {
			return nil, err
		}
		seen[item.ID] = true
	}
	return s, nil
}

func validateShortcut(item shortcut) error {
	if strings.TrimSpace(item.Name) == "" || len(item.Name) > 200 || !symbolNamePattern.MatchString(item.Icon) || !accentColorPattern.MatchString(item.Color) {
		return apiError{status: 400, message: uiText("backend.shortcuts.message001")}
	}
	if len(item.Steps) < 1 || len(item.Steps) > 50 {
		return apiError{status: 400, message: uiText("backend.shortcuts.message002")}
	}
	for _, step := range item.Steps {
		if !idPattern.MatchString(step.InstanceID) {
			return apiError{status: 400, message: uiText("backend.shortcuts.message003")}
		}
		switch step.Action {
		case "start", "stop", "restart", "open", "toggle":
		default:
			return apiError{status: 400, message: uiText("backend.shortcuts.message004")}
		}
		switch step.Rule {
		case "always", "running", "stopped":
		default:
			return apiError{status: 400, message: uiText("backend.shortcuts.message005")}
		}
	}
	return nil
}

// Commit the entire ordered list atomically before publishing it in memory.
func (s *shortcutStore) saveLocked(items []shortcut) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".shortcuts-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = replaceFile(f.Name(), s.path); err != nil {
		return err
	}
	s.items = items
	return nil
}

func (s *shortcutStore) handler(auth *authManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !sameOriginRequest(r) {
			writeAPIError(w, apiError{status: 403, message: uiText("backend.auth.message011")})
			return
		}
		if auth == nil {
			writeAPIError(w, apiError{status: 401, message: uiText("backend.auth.message010")})
			return
		}
		if _, ok := auth.currentUser(r); !ok {
			writeAPIError(w, apiError{status: 401, message: uiText("backend.auth.message010")})
			return
		}
		s.handle(w, r)
	})
}

func (s *shortcutStore) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/shortcuts")
	if path == "" && r.Method == http.MethodGet {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"shortcuts": s.items, "runs": s.runs})
		return
	}
	if path == "/order" && r.Method == http.MethodPut {
		var input struct {
			IDs []string `json:"ids"`
		}
		if err := decodeJSON(w, r, &input); err != nil {
			writeAPIError(w, err)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		byID := map[string]shortcut{}
		for _, item := range s.items {
			byID[item.ID] = item
		}
		ordered := []shortcut{}
		for _, id := range input.IDs {
			item, ok := byID[id]
			if !ok {
				writeAPIError(w, apiError{status: 409, message: uiText("backend.shortcuts.message006")})
				return
			}
			ordered = append(ordered, item)
			delete(byID, id)
		}
		if len(byID) > 0 {
			writeAPIError(w, apiError{status: 409, message: uiText("backend.shortcuts.message007")})
			return
		}
		if err := s.saveLocked(ordered); err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"shortcuts": ordered})
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	id := parts[0]
	if len(parts) == 2 && parts[1] == "run" && r.Method == http.MethodPost {
		s.mu.Lock()
		if s.runs[id].Status == "running" {
			s.mu.Unlock()
			writeAPIError(w, apiError{status: 409, message: uiText("backend.shortcuts.message008")})
			return
		}
		var item shortcut
		for _, candidate := range s.items {
			if candidate.ID == id {
				item = candidate
				break
			}
		}
		if item.ID == "" {
			s.mu.Unlock()
			writeAPIError(w, apiError{status: 404, message: uiText("backend.shortcuts.message009")})
			return
		}
		s.runs[id] = shortcutRun{Status: "running", Total: len(item.Steps), Messages: []string{}, OpenIDs: []string{}}
		s.mu.Unlock()
		go s.execute(item)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "running"})
		return
	}
	if len(parts) != 1 {
		writeAPIError(w, apiError{status: 404, message: uiText("backend.shortcuts.message010")})
		return
	}
	if (path == "" && r.Method == http.MethodPost) || (idPattern.MatchString(id) && r.Method == http.MethodPut) {
		var item shortcut
		if err := decodeJSON(w, r, &item); err != nil {
			writeAPIError(w, err)
			return
		}
		item.Name = strings.TrimSpace(item.Name)
		item.Icon = strings.TrimSuffix(strings.TrimSpace(item.Icon), ".svg")
		if err := validateShortcut(item); err != nil {
			writeAPIError(w, err)
			return
		}
		for _, step := range item.Steps {
			if _, ok := s.manager.instanceByID(step.InstanceID); !ok {
				writeAPIError(w, apiError{status: 400, message: uiText("backend.shortcuts.message011")})
				return
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		items := append([]shortcut{}, s.items...)
		if r.Method == http.MethodPost {
			var err error
			item.ID, err = randomID()
			if err != nil {
				writeAPIError(w, err)
				return
			}
			items = append(items, item)
		} else {
			item.ID = id
			found := false
			for i := range items {
				if items[i].ID == id {
					items[i] = item
					found = true
					break
				}
			}
			if !found {
				writeAPIError(w, apiError{status: 404, message: uiText("backend.shortcuts.message009")})
				return
			}
			if s.runs[id].Status == "running" {
				writeAPIError(w, apiError{status: 409, message: uiText("backend.shortcuts.message012")})
				return
			}
		}
		if err := s.saveLocked(items); err != nil {
			writeAPIError(w, err)
			return
		}
		writeJSON(w, 200, item)
		return
	}
	if idPattern.MatchString(id) && r.Method == http.MethodDelete {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.runs[id].Status == "running" {
			writeAPIError(w, apiError{status: 409, message: uiText("backend.shortcuts.message013")})
			return
		}
		items := []shortcut{}
		found := false
		for _, item := range s.items {
			if item.ID == id {
				found = true
			} else {
				items = append(items, item)
			}
		}
		if !found {
			writeAPIError(w, apiError{status: 404, message: uiText("backend.shortcuts.message009")})
			return
		}
		if err := s.saveLocked(items); err != nil {
			writeAPIError(w, err)
			return
		}
		delete(s.runs, id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	methodNotAllowed(w)
}

func (s *shortcutStore) awaitState(id, desired string) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		instance, ok := s.manager.instanceByID(id)
		if !ok {
			return errors.New(uiText("backend.shortcuts.message014"))
		}
		if instance.Status == desired {
			return nil
		}
		if instance.Status == statusUnavailable || (instance.Status == statusException && instance.PID <= 0) {
			return fmt.Errorf("%s", instance.Error)
		}
		if instance.DeviceID != "" && strings.HasPrefix(instance.Error, uiText("backend.shortcuts.message015")) {
			return errors.New(instance.Error)
		}
		if desired == statusRunning && instance.Status == statusStopped {
			return errors.New(uiText("backend.shortcuts.message016"))
		}
		if instance.DeviceID != "" {
			refreshCtx, cancel := context.WithTimeout(context.Background(), 13*time.Second)
			_ = s.manager.refreshRemoteStates(refreshCtx, id)
			cancel()
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New(uiText("backend.shortcuts.message017"))
}

func shortcutActionForStatus(action, status string) (string, error) {
	if action != "toggle" {
		return action, nil
	}
	switch status {
	case statusRunning:
		return "stop", nil
	case statusStopped:
		return "start", nil
	default:
		return "", fmt.Errorf(uiText("backend.shortcuts.message018"), status)
	}
}

func (s *shortcutStore) execute(item shortcut) {
	failed := false
	for i, step := range item.Steps {
		snapshot, exists := s.manager.instanceByID(step.InstanceID)
		var refreshErr error
		if exists && snapshot.DeviceID == "" {
			s.manager.refreshLocalStates()
		} else if exists {
			refreshCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			refreshErr = s.manager.refreshRemoteStates(refreshCtx, step.InstanceID)
			cancel()
		}
		if refreshErr != nil {
			s.mu.Lock()
			run := s.runs[item.ID]
			run.Completed = i
			run.Messages = append(run.Messages, fmt.Sprintf(uiText("backend.shortcuts.message019"), i+1, refreshErr))
			s.runs[item.ID] = run
			s.mu.Unlock()
			failed = true
			break
		}
		instance, ok := s.manager.instanceByID(step.InstanceID)
		message := ""
		openID := ""
		var err error
		if !ok {
			err = errors.New(uiText("backend.shortcuts.message014"))
		} else if instance.Error != "" {
			err = fmt.Errorf(uiText("backend.shortcuts.message020"), instance.Error)
		} else if (step.Rule == "running" && instance.Status != statusRunning) || (step.Rule == "stopped" && instance.Status != statusStopped) {
			message = instance.Name + uiText("backend.shortcuts.message021")
		} else {
			action, actionErr := shortcutActionForStatus(step.Action, instance.Status)
			if actionErr != nil {
				err = actionErr
			} else {
				switch action {
				case "start":
					if instance.Status != statusRunning {
						err = s.manager.start(instance.ID)
						if err == nil {
							err = s.awaitState(instance.ID, statusRunning)
						}
					}
				case "stop":
					if instance.Status != statusStopped {
						err = s.manager.stop(instance.ID)
						if err == nil {
							err = s.awaitState(instance.ID, statusStopped)
						}
					}
				case "restart":
					if instance.Status != statusStopped {
						err = s.manager.stop(instance.ID)
						if err == nil {
							err = s.awaitState(instance.ID, statusStopped)
						}
					}
					if err == nil {
						err = s.manager.start(instance.ID)
						if err == nil {
							err = s.awaitState(instance.ID, statusRunning)
						}
					}
				case "open":
					if instance.Status != statusRunning {
						err = errors.New(uiText("backend.shortcuts.message022"))
					} else {
						openID = instance.ID
					}
				}
			}
			if err == nil {
				if step.Action == "toggle" {
					if instance.Status == statusRunning {
						message = instance.Name + uiText("backend.shortcuts.message023")
					} else {
						message = instance.Name + uiText("backend.shortcuts.message024")
					}
				} else {
					message = instance.Name + uiText("backend.shortcuts.message025")
				}
			}
		}
		if err != nil {
			message = fmt.Sprintf(uiText("backend.shortcuts.message026"), i+1, err)
			failed = true
		}
		s.mu.Lock()
		run := s.runs[item.ID]
		run.Completed = i + 1
		run.Messages = append(run.Messages, message)
		if openID != "" {
			run.OpenIDs = append(run.OpenIDs, openID)
		}
		s.runs[item.ID] = run
		s.mu.Unlock()
		// Stop on an error rather than executing dependent actions blindly.
		if failed {
			break
		}
	}
	s.mu.Lock()
	run := s.runs[item.ID]
	run.Status = "completed"
	if failed {
		run.Status = "failed"
	}
	s.runs[item.ID] = run
	s.mu.Unlock()
}
