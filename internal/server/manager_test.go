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
