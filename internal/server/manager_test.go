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

func TestManagerRestartWarningOnlyAppliesToActiveInstances(t *testing.T) {
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	instanceID := strings.Repeat("a", 32)
	content, err := json.Marshal(persistedState{Instances: []persistedInstance{{Instance: Instance{
		ID: instanceID, Name: "Stopped", ExecutablePath: executable, ConfiguredPort: 1, Status: statusStopped,
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "instances.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	manager, err := newInstanceManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	instance, ok := manager.instanceByID(instanceID)
	if !ok {
		t.Fatal("stopped instance was not restored")
	}
	if instance.Error != "" {
		t.Fatalf("stopped instance received restart warning: %q", instance.Error)
	}
}

func TestClearManagerRestartWarningForStoppedInstance(t *testing.T) {
	instance := &Instance{Status: statusStopped, Error: managerRestartWarning}
	if !clearManagerRestartWarning(instance) {
		t.Fatal("restart warning was not cleared")
	}
	if instance.Error != "" || instance.Status != statusStopped || instance.ActualPort != 0 || instance.StartedAt != "" || instance.ProcessStartToken != "" {
		t.Fatalf("stale restart state remains: %+v", instance)
	}
}

func TestClearManagerRestartWarningDoesNotTouchTrackedProcess(t *testing.T) {
	instance := &Instance{Status: statusException, PID: 1234, Error: managerRestartWarning}
	if clearManagerRestartWarning(instance) {
		t.Fatal("restart warning cleared while a process was still tracked")
	}
	if instance.Error != managerRestartWarning || instance.PID != 1234 {
		t.Fatalf("tracked process state changed: %+v", instance)
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

func TestGetInstancesReturnsSavedRemoteStateWithoutProbing(t *testing.T) {
	manager, err := newInstanceManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instanceID := strings.Repeat("a", 32)
	deviceID := strings.Repeat("b", 32)
	manager.instances = []Instance{{
		ID: instanceID, Name: "Remote service", DeviceID: deviceID, Status: statusStopped,
		ExecutablePath: `C:\Apps\service.exe`, ConfiguredPort: 8080,
	}}
	manager.devices = []Device{{
		ID: deviceID, Name: "Remote Windows", Address: "192.0.2.10", Platform: "windows",
		Transport: "winrm", Port: 5986, Username: `host\user`, Password: "not-used-by-GET",
	}}

	response := httptest.NewRecorder()
	manager.handleInstances(response, httptest.NewRequest(http.MethodGet, "/api/instances", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/instances returned %d: %s", response.Code, response.Body.String())
	}
	if got := manager.instances[0].Status; got != statusStopped {
		t.Fatalf("GET /api/instances changed saved remote status to %q", got)
	}
	if !strings.Contains(response.Body.String(), `"name":"Remote service"`) {
		t.Fatalf("GET /api/instances did not return the saved task: %s", response.Body.String())
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
