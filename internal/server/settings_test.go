package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestManagerSettingsPersistAccentColor(t *testing.T) {
	dir := t.TempDir()
	store, err := newManagerSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	settings, initialized := store.get()
	if initialized || settings.AccentColor != defaultAccentColor {
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

func TestManagerSettingsRejectInvalidAccentColor(t *testing.T) {
	store, err := newManagerSettingsStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.set("red"); err == nil {
		t.Fatal("expected invalid accent color to be rejected")
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
