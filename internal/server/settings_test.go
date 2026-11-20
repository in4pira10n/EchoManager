package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagerSettingsPersistAccentColor(t *testing.T) {
	dir := t.TempDir()
	store, err := newManagerSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, initialized := store.get()
	if initialized || settings.AccentColor != defaultAccentColor || settings.StartTimeoutSeconds != defaultStartTimeoutSeconds {
		t.Fatalf("initial settings = %+v, initialized=%v", settings, initialized)
	}
	if err := store.set("#4A90E2"); err != nil {
		t.Fatal(err)
	}
	loaded, err := newManagerSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, initialized = loaded.get()
	if !initialized || settings.AccentColor != "#4a90e2" {
		t.Fatalf("loaded settings = %+v, initialized=%v", settings, initialized)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil {
		t.Fatal(err)
	}
}

func TestManagerSettingsPersistStartTimeout(t *testing.T) {
	dir := t.TempDir()
	store, err := newManagerSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	color := "#4A90E2"
	confirmShortcut := false
	if err := store.update(&color, &confirmShortcut, nil); err != nil {
		t.Fatal(err)
	}
	seconds := 47
	if err := store.update(nil, nil, &seconds); err != nil {
		t.Fatal(err)
	}
	loaded, err := newManagerSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, initialized := loaded.get()
	if !initialized || settings.StartTimeoutSeconds != seconds || settings.AccentColor != "#4a90e2" || settings.ConfirmShortcut == nil || *settings.ConfirmShortcut {
		t.Fatalf("loaded settings = %+v, initialized=%v", settings, initialized)
	}
}

func TestManagerSettingsLegacyFileDefaultsStartTimeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"accent_color":"#123456"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newManagerSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, initialized := store.get()
	if !initialized || settings.StartTimeoutSeconds != defaultStartTimeoutSeconds {
		t.Fatalf("legacy settings = %+v, initialized=%v", settings, initialized)
	}
}

func TestManagerSettingsPersistShortcutWarning(t *testing.T) {
	dir := t.TempDir()
	store, err := newManagerSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	confirm := false
	if err := store.set(defaultAccentColor, &confirm); err != nil {
		t.Fatal(err)
	}
	loaded, err := newManagerSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, initialized := loaded.get()
	if !initialized || settings.ConfirmShortcut == nil || *settings.ConfirmShortcut {
		t.Fatalf("shortcut warning preference did not persist: %+v", settings)
	}
}

func TestManagerSettingsRejectInvalidAccentColor(t *testing.T) {
	store, err := newManagerSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.set("red"); err == nil {
		t.Fatal("expected invalid accent color to be rejected")
	}
}

func TestManagerSettingsRejectOutOfRangeStartTimeout(t *testing.T) {
	store, err := newManagerSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, seconds := range []int{0, 4, 121} {
		if err := store.update(nil, nil, &seconds); err == nil {
			t.Errorf("start timeout %d unexpectedly succeeded", seconds)
		}
	}
}

func TestInstanceManagerReadinessTimeoutUsesSettings(t *testing.T) {
	store, err := newManagerSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := &instanceManager{settings: store}
	if got := manager.readinessTimeout(); got != startTimeout {
		t.Fatalf("default readiness timeout = %s, want %s", got, startTimeout)
	}
	seconds := 38
	if err := store.update(nil, nil, &seconds); err != nil {
		t.Fatal(err)
	}
	if got, want := manager.readinessTimeout(), 38*time.Second; got != want {
		t.Fatalf("configured readiness timeout = %s, want %s", got, want)
	}
}

func TestManagerSettingsHandlerRequiresAuthentication(t *testing.T) {
	store, err := newManagerSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	store.handler(nil).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("GET without auth returned %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestManagerSettingsResponseShape(t *testing.T) {
	store, err := newManagerSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings, initialized := store.get()
	data, err := json.Marshal(map[string]any{"settings": settings, "initialized": initialized})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Settings    managerSettings `json:"settings"`
		Initialized bool            `json:"initialized"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Settings.AccentColor != defaultAccentColor || decoded.Initialized {
		t.Fatalf("unexpected settings response: %+v", decoded)
	}
}
