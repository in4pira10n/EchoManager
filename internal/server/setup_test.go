package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppRuntimeRequiresSetupUntilKitIsExplicitAndValid(t *testing.T) {
	kitDir := makeValidKitFixture(t)
	for _, test := range []struct {
		name      string
		config    appConfig
		wantSetup bool
	}{
		{
			name:      "implicit path prompts on first run",
			config:    appConfig{EchoKitDir: kitDir},
			wantSetup: true,
		},
		{
			name:      "explicit valid path skips setup",
			config:    appConfig{EchoKitDir: kitDir, EchoKitDirExplicit: true},
			wantSetup: false,
		},
		{
			name:      "explicit invalid path prompts for repair",
			config:    appConfig{EchoKitDir: filepath.Join(t.TempDir(), "missing"), EchoKitDirExplicit: true},
			wantSetup: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := newAppRuntime(test.config)
			if got := runtime.requiresSetup(); got != test.wantSetup {
				t.Fatalf("requiresSetup() = %t, want %t", got, test.wantSetup)
			}
		})
	}
}

func TestSetupPageIsStandaloneAndTransitionsToApp(t *testing.T) {
	kitDir := makeValidKitFixture(t)
	runtime := newAppRuntime(appConfig{
		EchoKitDir: kitDir,
		Port:       "3000",
		DataDir:    filepath.Join(t.TempDir(), "data"),
		ConfigPath: filepath.Join(t.TempDir(), "config.json"),
	})
	auth, err := newAuthManager(filepath.Join(t.TempDir(), "auth"))
	if err != nil {
		t.Fatal(err)
	}
	handler := appOrSetupHandler(embeddedAppAssets, runtime, auth)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "EchoKit 配置") || !strings.Contains(response.Body.String(), "管理员用户名") {
		t.Fatalf("first visit did not serve setup page: status=%d body=%q", response.Code, response.Body.String())
	}

	for _, assetPath := range []string{"/setup.css", "/setup.js", "/settings-config.js", "/pwa.js", "/manifest.webmanifest", "/sw.js", "/offline.html", "/static/assets/echomanager-icon.png", "/static/assets/favicon.ico", "/static/assets/apple-touch-icon.png", "/static/assets/pwa-icon-192.png"} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, assetPath, nil))
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Fatalf("setup asset %s was not served: status=%d", assetPath, response.Code)
		}
	}

	if err := auth.createInitialAdmin("admin", "password123"); err != nil {
		t.Fatal(err)
	}
	runtime.completeSetup(kitDir, appConfigFile{EchoKitDir: kitDir})
	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	session, _, err := auth.login("admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: managerSessionCookie, Value: session})
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "管理器设置") {
		t.Fatalf("configured visit did not serve the application: status=%d", response.Code)
	}
}

func TestSetupAPIValidatesPersistsAndActivatesKit(t *testing.T) {
	kitDir := makeValidKitFixture(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	dataDir := filepath.Join(t.TempDir(), "manager-data")
	auth, err := newAuthManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	config := appConfig{
		EchoKitDir: filepath.Join(t.TempDir(), "old-kit"),
		Port:       "3000",
		DataDir:    dataDir,
		ConfigPath: configPath,
		File:       appConfigFile{Port: "3000", DataDir: "state"},
	}
	runtime := newAppRuntime(config)
	handler := setupAPIHandler(runtime, auth)
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(`{"echokit_dir":"`+t.TempDir()+`","username":"admin","password":"password123","confirm_password":"password123"}`))
	invalidRequest.RemoteAddr = "127.0.0.1:4321"
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusBadRequest || !runtime.requiresSetup() {
		t.Fatalf("invalid Kit path response=%d setupRequired=%t", invalidResponse.Code, runtime.requiresSetup())
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("invalid setup unexpectedly wrote config: stat err=%v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(`{"echokit_dir":"`+kitDir+`","username":"admin","password":"password123","confirm_password":"password123"}`))
	request.RemoteAddr = "127.0.0.1:4321"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("setup returned %d: %s", response.Code, response.Body.String())
	}
	if runtime.requiresSetup() {
		t.Fatal("successful setup still requires setup")
	}
	if !auth.hasUsers() {
		t.Fatal("initial admin user was not persisted")
	}

	var saved appConfigFile
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	resolvedKitDir, err := filepath.EvalSymlinks(kitDir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.EchoKitDir != resolvedKitDir || saved.Port != "3000" || saved.DataDir != "state" {
		t.Fatalf("saved config = %+v, want Kit path persisted and existing fields preserved", saved)
	}

	kitResponse := httptest.NewRecorder()
	runtimeKitAssetHandler(runtime).ServeHTTP(kitResponse, httptest.NewRequest(http.MethodGet, "/kit/css/echokit.css", nil))
	if kitResponse.Code != http.StatusOK || kitResponse.Body.String() != "css/echokit.css" {
		t.Fatalf("new Kit path did not activate: status=%d body=%q", kitResponse.Code, kitResponse.Body.String())
	}
}

func TestSetupAPIUsesSameOriginAndAdminSessionForRepair(t *testing.T) {
	kitDir := makeValidKitFixture(t)
	dataDir := t.TempDir()
	auth, err := newAuthManager(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.createInitialAdmin("admin", "password123"); err != nil {
		t.Fatal(err)
	}
	runtime := newAppRuntime(appConfig{EchoKitDir: t.TempDir(), ConfigPath: filepath.Join(t.TempDir(), "config.json")})
	handler := setupAPIHandler(runtime, auth)
	request := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(`{"echokit_dir":"`+kitDir+`","username":"admin","password":"password123","confirm_password":"password123"}`))
	request.RemoteAddr = "192.168.1.8:4321"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated setup repair returned %d, want %d", response.Code, http.StatusUnauthorized)
	}

	session, _, err := auth.login("admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(`{"echokit_dir":"`+kitDir+`","username":"admin","password":"password123","confirm_password":"password123"}`))
	request.RemoteAddr = "192.168.1.8:4321"
	request.AddCookie(&http.Cookie{Name: managerSessionCookie, Value: session})
	request.Header.Set("Origin", "http://attacker.invalid")
	request.Host = "manager.local"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin remote setup returned %d, want %d", response.Code, http.StatusForbidden)
	}
}

func makeValidKitFixture(t *testing.T) string {
	t.Helper()
	kitDir := t.TempDir()
	for _, asset := range requiredEchoKitAssets {
		writeTestAsset(t, kitDir, asset, asset)
	}
	return kitDir
}
