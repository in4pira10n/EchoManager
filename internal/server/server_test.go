package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppAssetHandlerOnlyServesUIAssets(t *testing.T) {
	root := t.TempDir()
	writeTestAsset(t, root, "index.html", "app shell")
	writeTestAsset(t, root, "app.js", "app script")
	writeTestAsset(t, root, "README.md", "private project file")
	writeTestAsset(t, root, "config.json", "private runtime config")
	writeTestAsset(t, root, ".git/config", "git metadata")
	writeTestAsset(t, root, "static/assets/icon.svg", "icon")
	writeTestAsset(t, root, "static/assets/favicon.ico", "icon")
	writeTestAsset(t, root, "pwa.js", "service worker registration")
	writeTestAsset(t, root, "manifest.webmanifest", "{}")
	writeTestAsset(t, root, "offline.html", "offline page")
	writeTestAsset(t, root, "static/assets/notes.txt", "not a UI asset")

	handler := appAssetHandler(os.DirFS(root))
	for _, test := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/", wantStatus: http.StatusOK},
		{path: "/app.js", wantStatus: http.StatusOK},
		{path: "/static/assets/icon.svg", wantStatus: http.StatusOK},
		{path: "/static/assets/favicon.ico", wantStatus: http.StatusOK},
		{path: "/pwa.js", wantStatus: http.StatusOK},
		{path: "/manifest.webmanifest", wantStatus: http.StatusOK},
		{path: "/offline.html", wantStatus: http.StatusOK},
		{path: "/README.md", wantStatus: http.StatusNotFound},
		{path: "/config.json", wantStatus: http.StatusNotFound},
		{path: "/.git/config", wantStatus: http.StatusNotFound},
		{path: "/static/assets/notes.txt", wantStatus: http.StatusNotFound},
		{path: "/static/assets/../../app.js", wantStatus: http.StatusNotFound},
		{path: "/static/assets/..\\..\\icon.svg", wantStatus: http.StatusNotFound},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("GET %s returned %d, want %d", test.path, response.Code, test.wantStatus)
			}
		})
	}
}

func TestAppAssetHandlerServesEmbeddedAssets(t *testing.T) {
	handler := appAssetHandler(embeddedAppAssets)
	for _, path := range []string{"/", "/app.js", "/app.css", "/settings-config.js", "/pwa.js", "/manifest.webmanifest", "/offline.html", "/static/assets/echomanager-icon.png", "/static/assets/favicon.ico", "/static/assets/apple-touch-icon.png", "/static/assets/pwa-icon-192.png", "/static/assets/task-compass.png"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s returned %d, want %d", path, response.Code, http.StatusOK)
			}
			if response.Body.Len() == 0 {
				t.Fatalf("GET %s returned an empty embedded asset", path)
			}
		})
	}
}

func TestServiceWorkerHandlerInjectsBuildVersionAndRootScope(t *testing.T) {
	root := t.TempDir()
	writeTestAsset(t, root, "sw.js", `const VERSION = "{{ASSET_VERSION}}";`)
	writeTestAsset(t, root, "app.js", "build asset")
	handler := serviceWorkerHandler(os.DirFS(root))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sw.js", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "{{ASSET_VERSION}}") {
		t.Fatalf("service worker version was not injected: status=%d body=%q", response.Code, response.Body.String())
	}
	if response.Header().Get("Service-Worker-Allowed") != "/" {
		t.Fatalf("service worker scope header = %q, want root scope", response.Header().Get("Service-Worker-Allowed"))
	}
}

func TestDefaultKitDirCanResolveFromExecutableOrSourceDirectory(t *testing.T) {
	root := t.TempDir()
	executableDir := filepath.Join(root, "app")
	kitDir := filepath.Join(root, "EchoKit")
	if err := os.MkdirAll(executableDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(kitDir, 0o700); err != nil {
		t.Fatal(err)
	}

	if got := defaultKitDir(filepath.Join(executableDir, "EchoManager"), ""); got != kitDir {
		t.Fatalf("defaultKitDir from executable = %q, want %q", got, kitDir)
	}

	sourceDir := filepath.Join(root, "source", "EchoManager")
	sourceKitDir := filepath.Join(root, "source", "EchoKit")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceKitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := defaultKitDir(filepath.Join(t.TempDir(), "go-build", "EchoManager"), sourceDir); got != sourceKitDir {
		t.Fatalf("defaultKitDir from source directory = %q, want %q", got, sourceKitDir)
	}
}

func TestLoadAppConfigReadsFileAndAppliesEnvironmentOverrides(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "app")
	workingDir := filepath.Join(root, "launch")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, appConfigFileName)
	if err := os.WriteFile(configPath, []byte(`{"port":"3100","echokit_dir":"../Kit","data_dir":"state"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		envManagerPort:    "3200",
		envEchoKitDir:     "./CustomKit",
		envManagerDataDir: "",
	}
	lookup := func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
	config, err := loadAppConfigAt(configPath, filepath.Join(configDir, "EchoManager"), workingDir, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if config.Port != "3200" {
		t.Fatalf("port = %q, want environment override 3200", config.Port)
	}
	if config.EchoKitDir != filepath.Join(workingDir, "CustomKit") {
		t.Fatalf("EchoKit path = %q, want env-relative path %q", config.EchoKitDir, filepath.Join(workingDir, "CustomKit"))
	}
	if config.DataDir != filepath.Join(configDir, "state") {
		t.Fatalf("data path = %q, want config-relative path %q", config.DataDir, filepath.Join(configDir, "state"))
	}
	if info, err := os.Stat(config.DataDir); err != nil || !info.IsDir() {
		t.Fatalf("configured data directory was not created: info=%v err=%v", info, err)
	}
}

func TestLoadAppConfigResolvesRelativePathsFromConfigFile(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "app")
	workingDir := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, appConfigFileName)
	if err := os.WriteFile(configPath, []byte(`{"port":"3100","echokit_dir":"../Kit","data_dir":"state"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := func(string) (string, bool) { return "", false }
	config, err := loadAppConfigAt(configPath, filepath.Join(configDir, "EchoManager"), workingDir, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if config.Port != "3100" {
		t.Fatalf("port = %q, want config value 3100", config.Port)
	}
	if config.EchoKitDir != filepath.Join(root, "Kit") {
		t.Fatalf("EchoKit path = %q, want config-relative path %q", config.EchoKitDir, filepath.Join(root, "Kit"))
	}
	if config.DataDir != filepath.Join(configDir, "state") {
		t.Fatalf("data path = %q, want config-relative path %q", config.DataDir, filepath.Join(configDir, "state"))
	}
}

func TestFindAppConfigPathPrefersExecutableDirectory(t *testing.T) {
	root := t.TempDir()
	executableDir := filepath.Join(root, "bin")
	workingDir := filepath.Join(root, "work")
	if err := os.MkdirAll(executableDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	executableConfig := filepath.Join(executableDir, appConfigFileName)
	workingConfig := filepath.Join(workingDir, appConfigFileName)
	if err := os.WriteFile(executableConfig, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workingConfig, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := findAppConfigPath(filepath.Join(executableDir, "EchoManager"), workingDir)
	if got != executableConfig {
		t.Fatalf("config path = %q, want executable-side file %q", got, executableConfig)
	}
}

func TestPortableManagerDataDirectoryCopiesLegacyStateWithoutDeletingIt(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy", "EchoManager")
	portable := filepath.Join(root, "app", managerDataDirectoryName)
	if err := os.MkdirAll(filepath.Dir(portable), 0o700); err != nil {
		t.Fatal(err)
	}
	for relative, content := range map[string]string{
		"instances.json":     `{"instances":[]}`,
		"device-secrets.key": "secret-key",
		"logs/example.log":   "task log",
		"icons/example.png":  "icon bytes",
	} {
		path := filepath.Join(legacy, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateLegacyManagerDataDir(legacy, portable); err != nil {
		t.Fatal(err)
	}
	for relative, want := range map[string]string{
		"instances.json":     `{"instances":[]}`,
		"device-secrets.key": "secret-key",
		"logs/example.log":   "task log",
		"icons/example.png":  "icon bytes",
	} {
		for _, dir := range []string{legacy, portable} {
			got, err := os.ReadFile(filepath.Join(dir, relative))
			if err != nil || string(got) != want {
				t.Fatalf("%s contents = %q, err=%v; want %q", filepath.Join(dir, relative), got, err, want)
			}
		}
	}
}

func TestLegacyManagerDataMigrationDoesNotOverwriteExistingPortableData(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	portable := filepath.Join(root, managerDataDirectoryName)
	for path, value := range map[string]string{
		filepath.Join(legacy, "instances.json"):   "legacy",
		filepath.Join(portable, "instances.json"): "current",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateLegacyManagerDataDir(legacy, portable); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(portable, "instances.json"))
	if err != nil || string(got) != "current" {
		t.Fatalf("portable data = %q, err=%v; want current data to remain unchanged", got, err)
	}
}

func TestDefaultManagerDataDirectoryIsBesideProgram(t *testing.T) {
	root := t.TempDir()
	programDir := filepath.Join(root, "EchoManager")
	if err := os.MkdirAll(programDir, 0o700); err != nil {
		t.Fatal(err)
	}
	workingDir := filepath.Join(root, "launcher")
	if err := os.MkdirAll(workingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := defaultManagerDataDir(filepath.Join(programDir, "EchoManager"), filepath.Join(programDir, "config.json"), workingDir)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(programDir, managerDataDirectoryName)
	if got != want {
		t.Fatalf("default data directory = %q, want %q", got, want)
	}
}

func TestKitAssetHandlerOnlyServesRuntimeAssets(t *testing.T) {
	root := t.TempDir()
	writeTestAsset(t, root, "components/button/index.js", "component")
	writeTestAsset(t, root, "components/button/README.md", "component docs")
	writeTestAsset(t, root, "css/echokit.css", "kit styles")
	writeTestAsset(t, root, "tokens/tokens.css", "design tokens")
	writeTestAsset(t, root, "SFSymbols/checkmark.svg", "symbol")
	writeTestAsset(t, root, ".git/config", "git metadata")

	handler := kitAssetHandler(root)
	for _, test := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/kit/components/button/index.js", wantStatus: http.StatusOK},
		{path: "/kit/css/echokit.css", wantStatus: http.StatusOK},
		{path: "/kit/tokens/tokens.css", wantStatus: http.StatusOK},
		{path: "/kit/SFSymbols/checkmark.svg", wantStatus: http.StatusOK},
		{path: "/kit/components/button/README.md", wantStatus: http.StatusNotFound},
		{path: "/kit/.git/config", wantStatus: http.StatusNotFound},
		{path: "/kit/components/../README.md", wantStatus: http.StatusNotFound},
		{path: "/kit/components/..\\..\\README.js", wantStatus: http.StatusNotFound},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("GET %s returned %d, want %d", test.path, response.Code, test.wantStatus)
			}
		})
	}
}

func writeTestAsset(t *testing.T, root, relativePath, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
