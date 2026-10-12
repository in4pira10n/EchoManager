package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReorderPersistsCompleteInstanceOrder(t *testing.T) {
	manager, err := newInstanceManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstID := strings.Repeat("a", 32)
	secondID := strings.Repeat("b", 32)
	manager.instances = []Instance{
		{ID: firstID, Name: "First", Status: statusStopped},
		{ID: secondID, Name: "Second", Status: statusStopped},
	}
	if err := manager.saveLocked(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reorder([]string{secondID, firstID}); err != nil {
		t.Fatal(err)
	}
	if got := manager.list(); len(got) != 2 || got[0].ID != secondID || got[1].ID != firstID {
		t.Fatalf("reorder returned unexpected order: %#v", got)
	}

	reloaded, err := newInstanceManager(filepath.Dir(manager.statePath))
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.list(); len(got) != 2 || got[0].ID != secondID || got[1].ID != firstID {
		t.Fatalf("persisted order was not restored: %#v", got)
	}
}

func TestReorderRejectsIncompleteOrDuplicateIDs(t *testing.T) {
	firstID := strings.Repeat("a", 32)
	secondID := strings.Repeat("b", 32)
	manager := &instanceManager{
		statePath: filepath.Join(t.TempDir(), "instances.json"),
		instances: []Instance{{ID: firstID}, {ID: secondID}},
	}
	for _, ids := range [][]string{{firstID}, {firstID, firstID}, {firstID, strings.Repeat("c", 32)}} {
		if err := manager.reorder(ids); err == nil {
			t.Fatalf("reorder(%v) unexpectedly succeeded", ids)
		}
	}
	if manager.instances[0].ID != firstID || manager.instances[1].ID != secondID {
		t.Fatalf("invalid order mutated the current list: %#v", manager.instances)
	}
}

func TestUpdateInstanceIconAcceptsPostAndPut(t *testing.T) {
	dataDir := t.TempDir()
	iconDir := filepath.Join(dataDir, "icons")
	if err := os.MkdirAll(iconDir, 0o700); err != nil {
		t.Fatal(err)
	}
	instanceID := strings.Repeat("a", 32)
	manager := &instanceManager{
		statePath: filepath.Join(dataDir, "instances.json"),
		iconDir:   iconDir,
		instances: []Instance{{ID: instanceID, Name: "Test", Status: statusStopped}},
	}
	auth, err := newAuthManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.createInitialAdmin("admin", "password123"); err != nil {
		t.Fatal(err)
	}
	session, _, err := auth.login("admin", "password123")
	if err != nil {
		t.Fatal(err)
	}

	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 512, 512))); err != nil {
		t.Fatal(err)
	}
	requestBody, err := json.Marshal(map[string]string{
		"iconData": "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData.Bytes()),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			request := httptest.NewRequest(method, "/api/instances/"+instanceID+"/icon", bytes.NewReader(requestBody))
			request.RemoteAddr = "127.0.0.1:12345"
			request.AddCookie(&http.Cookie{Name: managerSessionCookie, Value: session})
			response := httptest.NewRecorder()
			manager.apiHandler(auth).ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("%s icon returned %d: %s", method, response.Code, response.Body.String())
			}
			if manager.instances[0].IconFile == "" {
				t.Fatalf("%s icon returned success without persisting an icon file", method)
			}
		})
	}
}
