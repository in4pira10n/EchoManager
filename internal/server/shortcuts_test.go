package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShortcutStorePersistsAndOrders(t *testing.T) {
	dir := t.TempDir()
	manager, err := newInstanceManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	manager.instances = []Instance{{ID: strings.Repeat("a", 32), Name: "Gallery"}}
	first := shortcut{ID: strings.Repeat("b", 32), Name: "Open", Icon: "play.fill", Color: "#a0efe3", Steps: []shortcutStep{{InstanceID: manager.instances[0].ID, Action: "open", Rule: "always"}}}
	second := shortcut{ID: strings.Repeat("c", 32), Name: "Stop", Icon: "stop.fill", Color: "#f5c9e2", Steps: []shortcutStep{{InstanceID: manager.instances[0].ID, Action: "stop", Rule: "running"}}}
	store, err := newShortcutStore(dir, manager)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.saveLocked([]shortcut{first, second}); err != nil {
		t.Fatal(err)
	}
	ordered := []shortcut{second, first}
	if err := store.saveLocked(ordered); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newShortcutStore(dir, manager)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.items) != 2 || reloaded.items[0].ID != second.ID || reloaded.items[1].ID != first.ID {
		t.Fatalf("unexpected restored order: %#v", reloaded.items)
	}
	if _, err := os.Stat(filepath.Join(dir, "shortcuts.json")); err != nil {
		t.Fatal(err)
	}
}

func TestShortcutHandlerRequiresAuthentication(t *testing.T) {
	manager, err := newInstanceManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := newShortcutStore(t.TempDir(), manager)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	store.handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/shortcuts", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("GET without auth returned %d", response.Code)
	}
}

func TestShortcutRunSkipsWhenRuleDoesNotMatch(t *testing.T) {
	manager, err := newInstanceManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instanceID := strings.Repeat("a", 32)
	manager.instances = []Instance{{ID: instanceID, Name: "Gallery", Status: statusRunning}}
	store, err := newShortcutStore(t.TempDir(), manager)
	if err != nil {
		t.Fatal(err)
	}
	item := shortcut{ID: strings.Repeat("b", 32), Name: "Only when stopped", Icon: "play.fill", Color: "#a0efe3", Steps: []shortcutStep{{InstanceID: instanceID, Action: "stop", Rule: "stopped"}}}
	store.runs[item.ID] = shortcutRun{Status: "running", Total: 1}
	store.execute(item)
	got := store.runs[item.ID]
	if got.Status != "completed" || got.Completed != 1 || len(got.Messages) != 1 || !strings.Contains(got.Messages[0], "已跳过") {
		t.Fatalf("unexpected skipped run: %#v", got)
	}
	if manager.instances[0].Status != statusRunning {
		t.Fatal("conditional skip changed the program state")
	}
}

func TestShortcutToggleChoosesOppositeRunningState(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{status: statusRunning, want: "stop"},
		{status: statusStopped, want: "start"},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			got, err := shortcutActionForStatus("toggle", test.status)
			if err != nil {
				t.Fatalf("shortcutActionForStatus() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("shortcutActionForStatus() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestShortcutToggleRejectsUnconfirmedStates(t *testing.T) {
	for _, status := range []string{statusStarting, statusStopping, statusException, statusUnavailable, "unknown"} {
		t.Run(status, func(t *testing.T) {
			if action, err := shortcutActionForStatus("toggle", status); err == nil || action != "" {
				t.Fatalf("shortcutActionForStatus(toggle, %q) = %q, %v; want an error", status, action, err)
			}
		})
	}
}

func TestShortcutNonToggleActionIsUnchanged(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart", "open"} {
		got, err := shortcutActionForStatus(action, statusRunning)
		if err != nil || got != action {
			t.Fatalf("shortcutActionForStatus(%q) = %q, %v", action, got, err)
		}
	}
}

func TestValidateShortcutAcceptsToggleAction(t *testing.T) {
	item := shortcut{
		Name:  "Toggle service",
		Icon:  "power",
		Color: "#a0efe3",
		Steps: []shortcutStep{{InstanceID: strings.Repeat("a", 32), Action: "toggle", Rule: "always"}},
	}
	if err := validateShortcut(item); err != nil {
		t.Fatalf("validateShortcut() rejected toggle action: %v", err)
	}
}

func TestShortcutHandlerCreatesAndReordersWithSession(t *testing.T) {
	dir := t.TempDir()
	manager, err := newInstanceManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	instanceID := strings.Repeat("a", 32)
	manager.instances = []Instance{{ID: instanceID, Name: "Gallery"}}
	auth, err := newAuthManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = auth.createInitialAdmin("admin", "password123"); err != nil {
		t.Fatal(err)
	}
	session, _, err := auth.login("admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	store, err := newShortcutStore(dir, manager)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(shortcut{Name: "Launch", Icon: "play.fill", Color: "#a0efe3", Steps: []shortcutStep{{InstanceID: instanceID, Action: "start", Rule: "always"}}})
	request := httptest.NewRequest(http.MethodPost, "/api/shortcuts", bytes.NewReader(body))
	request.AddCookie(&http.Cookie{Name: managerSessionCookie, Value: session})
	response := httptest.NewRecorder()
	store.handler(auth).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("create returned %d: %s", response.Code, response.Body.String())
	}
	var created shortcut
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !idPattern.MatchString(created.ID) {
		t.Fatalf("create returned invalid id %q", created.ID)
	}
	orderBody, _ := json.Marshal(map[string][]string{"ids": {created.ID}})
	orderRequest := httptest.NewRequest(http.MethodPut, "/api/shortcuts/order", bytes.NewReader(orderBody))
	orderRequest.AddCookie(&http.Cookie{Name: managerSessionCookie, Value: session})
	orderResponse := httptest.NewRecorder()
	store.handler(auth).ServeHTTP(orderResponse, orderRequest)
	if orderResponse.Code != http.StatusOK {
		t.Fatalf("order returned %d: %s", orderResponse.Code, orderResponse.Body.String())
	}
}
