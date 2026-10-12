package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var requiredEchoKitAssets = []string{
	"css/echokit.css",
	"css/base.css",
	"tokens/tokens.css",
	"tokens/ios.css",
	"components/button/index.js",
	"components/button/button.js",
	"components/button/button.css",
	"components/library-card/index.js",
	"components/library-card/library-card.js",
	"components/library-card/library-card.css",
	"components/tab/index.js",
	"components/tab/tab.js",
	"components/tab/tab.css",
	"components/input/index.js",
	"components/input/input.js",
	"components/input/input.css",
	"components/modal/index.js",
	"components/modal/modal.js",
	"components/modal/modal.css",
	"components/metric/index.js",
	"components/metric/metric.js",
	"components/metric/metric.css",
	"components/surface/surface.css",
	"components/segmented/segmented.css",
	"components/setting-row/setting-row.css",
	"SFSymbols/app.badge.svg",
	"SFSymbols/arrow.clockwise.svg",
	"SFSymbols/arrow.counterclockwise.svg",
	"SFSymbols/arrow.turn.up.right.svg",
	"SFSymbols/checkmark.svg",
	"SFSymbols/chevron.down.svg",
	"SFSymbols/clock.svg",
	"SFSymbols/exclamationmark.triangle.svg",
	"SFSymbols/gear.svg",
	"SFSymbols/info.circle.svg",
	"SFSymbols/minus.svg",
	"SFSymbols/network.svg",
	"SFSymbols/number.svg",
	"SFSymbols/pencil.svg",
	"SFSymbols/photo.svg",
	"SFSymbols/play.fill.svg",
	"SFSymbols/plus.svg",
	"SFSymbols/server.rack.svg",
	"SFSymbols/stop.fill.svg",
	"SFSymbols/sun.min.svg",
	"SFSymbols/text.page.badge.magnifyingglass.svg",
	"SFSymbols/trash.svg",
	"SFSymbols/waveform.path.ecg.svg",
	"SFSymbols/xmark.svg",
}

type appRuntime struct {
	mu            sync.RWMutex
	setupMu       sync.Mutex
	config        appConfig
	setupRequired bool
}

func newAppRuntime(config appConfig) *appRuntime {
	_, validationErr := normalizeEchoKitDir(config.EchoKitDir)
	return &appRuntime{
		config:        config,
		setupRequired: !config.EchoKitDirExplicit || validationErr != nil,
	}
}

func (runtime *appRuntime) requiresSetup() bool {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.setupRequired
}

func (runtime *appRuntime) requireSetup() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.setupRequired = true
}

func (runtime *appRuntime) currentConfig() appConfig {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.config
}

func (runtime *appRuntime) completeSetup(kitDir string, fileConfig appConfigFile) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.config.EchoKitDir = kitDir
	runtime.config.EchoKitDirExplicit = true
	runtime.config.ConfigFileExists = true
	runtime.config.File = fileConfig
	runtime.setupRequired = false
}

func normalizeEchoKitDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if len(path) >= 2 && ((path[0] == '"' && path[len(path)-1] == '"') || (path[0] == '\'' && path[len(path)-1] == '\'')) {
		path = strings.TrimSpace(path[1 : len(path)-1])
	}
	if path == "" {
		return "", errors.New("请输入 EchoKit 文件夹位置。")
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("无法解析用户目录：%w", err)
		}
		path = filepath.Join(home, strings.TrimLeft(path[1:], `/\`))
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("无法解析 EchoKit 路径：%w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("无法访问此文件夹：%w", err)
	}
	if !info.IsDir() {
		return "", errors.New("此路径不是文件夹。")
	}
	missing := make([]string, 0, 3)
	for _, relativePath := range requiredEchoKitAssets {
		assetInfo, err := os.Stat(filepath.Join(absPath, filepath.FromSlash(relativePath)))
		if err != nil || !assetInfo.Mode().IsRegular() {
			missing = append(missing, relativePath)
			if len(missing) == 3 {
				break
			}
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("未找到 EchoKit 所需文件，例如：%s。请填写包含 css、components、tokens 和 SFSymbols 文件夹的 EchoKit 根目录。", strings.Join(missing, "、"))
	}
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolved
	}
	return filepath.Clean(absPath), nil
}

func appOrSetupHandler(assets fs.FS, runtime *appRuntime, auth *authManager) http.Handler {
	appHandler := appAssetHandler(assets)
	workerHandler := serviceWorkerHandler(assets)
	setupHandler := setupPageHandler(assets, runtime, auth)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.webmanifest", "/offline.html", "/pwa.js":
			appHandler.ServeHTTP(w, r)
			return
		case "/sw.js":
			workerHandler.ServeHTTP(w, r)
			return
		}
		if runtime.requiresSetup() {
			if r.URL.Path == "/login" || r.URL.Path == "/register" || r.URL.Path == "/index.html" {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			setupHandler.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/login":
			if _, ok := auth.currentUser(r); ok {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			serveFSAsset(w, r, assets, "login.html")
			return
		case "/register":
			if _, ok := auth.currentUser(r); ok {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			serveFSAsset(w, r, assets, "register.html")
			return
		case "/", "/index.html":
			if _, ok := auth.currentUser(r); !ok {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
		}
		appHandler.ServeHTTP(w, r)
	})
}

func setupPageHandler(assets fs.FS, runtime *appRuntime, auth *authManager) http.Handler {
	page := template.Must(template.ParseFS(assets, "setup.html"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			config := runtime.currentConfig()
			message := ""
			if _, err := normalizeEchoKitDir(config.EchoKitDir); err != nil {
				message = err.Error()
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if err := page.Execute(w, map[string]any{
				"HasUsers":   auth.hasUsers(),
				"EchoKitDir": config.EchoKitDir,
				"KitMessage": message,
				"KitFromEnv": config.EchoKitDirFromEnv,
				"Port":       config.Port,
				"DataDir":    config.DataDir,
				"ConfigPath": config.ConfigPath,
			}); err != nil {
				return
			}
			return
		}
		var relativePath string
		switch r.URL.Path {
		case "/setup.css", "/setup.js", "/auth.css", "/settings-config.js", "/pwa.js":
			relativePath = strings.TrimPrefix(r.URL.Path, "/")
		case "/static/assets/echomanager-icon.png",
			"/static/assets/favicon.ico",
			"/static/assets/favicon-32.png",
			"/static/assets/favicon-16.png",
			"/static/assets/pwa-icon-192.png",
			"/static/assets/apple-touch-icon.png":
			relativePath = strings.TrimPrefix(r.URL.Path, "/")
		default:
			http.NotFound(w, r)
			return
		}
		serveFSAsset(w, r, assets, relativePath)
	})
}

func setupAPIHandler(runtime *appRuntime, auth *authManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只支持 POST 请求。"})
			return
		}
		if !sameOriginRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "跨站请求已拒绝。"})
			return
		}
		runtime.setupMu.Lock()
		defer runtime.setupMu.Unlock()
		if !runtime.requiresSetup() {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "初始化已完成，请刷新页面。"})
			return
		}
		if auth.hasUsers() {
			if _, ok := auth.currentUser(r); !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "请先使用管理员账号登录，再修复 EchoKit 路径。"})
				return
			}
		}
		request := struct {
			EchoKitDir      string `json:"echokit_dir"`
			Username        string `json:"username"`
			Password        string `json:"password"`
			ConfirmPassword string `json:"confirm_password"`
		}{}
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "初始化信息无效：" + err.Error()})
			return
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求只能包含一组初始化信息。"})
			return
		}
		kitDir, err := normalizeEchoKitDir(request.EchoKitDir)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if request.Password != request.ConfirmPassword {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "两次输入的密码不一致。"})
			return
		}
		if _, err := validateManagerCredentials(request.Username, request.Password); err != nil && !auth.hasUsers() {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		config := runtime.currentConfig()
		if config.EchoKitDirFromEnv {
			currentEnvDir, envErr := normalizeEchoKitDir(config.EchoKitDir)
			if envErr != nil || currentEnvDir != kitDir {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "ECHOKIT_DIR 环境变量优先于配置文件。请先将环境变量改为有效的 EchoKit 路径并重启服务。"})
				return
			}
		}
		if config.ConfigPath == "" {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法确定配置文件位置。"})
			return
		}
		fileConfig := config.File
		fileConfig.EchoKitDir = kitDir
		if err := writeAppConfig(config.ConfigPath, fileConfig); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法保存配置文件：" + err.Error()})
			return
		}
		if !auth.hasUsers() {
			if err := auth.createInitialAdmin(request.Username, request.Password); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法创建初始管理员账户：" + err.Error()})
				return
			}
		}
		runtime.completeSetup(kitDir, fileConfig)
		writeJSON(w, http.StatusOK, map[string]string{"redirect": "/login"})
	})
}

func runtimeKitAssetHandler(runtime *appRuntime) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if runtime.requiresSetup() {
			http.NotFound(w, r)
			return
		}
		kitAssetHandler(runtime.currentConfig().EchoKitDir).ServeHTTP(w, r)
	})
}
