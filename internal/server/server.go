package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/echo/echomanager/web"
)

var embeddedAppAssets = web.Files()
var managerLogFile *os.File

func Run() {
	configureManagerLog("")
	config, err := loadAppConfig()
	if err != nil {
		log.Fatal(err)
	}
	configureManagerLog(config.DataDir)
	if managerLogFile != nil {
		defer managerLogFile.Close()
	}
	fmt.Printf("✅ Config loaded. Server will run on port %s\n", config.Port)
	fmt.Printf("🧩 EchoKit path: %s\n", config.EchoKitDir)
	fmt.Printf("📂 Manager data: %s\n", config.DataDir)
	fmt.Printf("⚙️ Configuration: %s\n", config.ConfigPath)
	settings, err := newManagerSettingsStore(config.DataDir)
	if err != nil {
		log.Fatal(err)
	}
	manager, err := newInstanceManager(config.DataDir)
	if err != nil {
		log.Fatal(err)
	}
	manager.settings = settings
	runtime := newAppRuntime(config)
	shortcuts, err := newShortcutStore(config.DataDir, manager)
	if err != nil {
		log.Fatal(err)
	}
	auth, err := newAuthManager(config.DataDir)
	if err != nil {
		log.Fatal(err)
	}
	if !auth.hasUsers() {
		runtime.requireSetup()
	}

	shutdownRequests := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.Handle("/api/setup", setupAPIHandler(runtime, auth))
	mux.Handle("/api/auth/", auth.handler())
	mux.Handle("/api/settings", settings.handler(auth))
	mux.Handle("/api/shortcuts", shortcuts.handler(auth))
	mux.Handle("/api/shortcuts/", shortcuts.handler(auth))
	mux.Handle("/api/manager/shutdown", managerShutdownAPIHandler(auth, func() {
		select {
		case shutdownRequests <- struct{}{}:
		default:
		}
	}))
	mux.Handle("/api/", manager.apiHandler(auth))
	mux.Handle("/kit/", runtimeKitAssetHandler(runtime))
	mux.Handle("/manifest.webmanifest", appAssetHandler(embeddedAppAssets))
	mux.Handle("/sw.js", serviceWorkerHandler(embeddedAppAssets))
	mux.Handle("/offline.html", appAssetHandler(embeddedAppAssets))
	mux.Handle("/", appOrSetupHandler(embeddedAppAssets, runtime, auth))

	server := &http.Server{
		Addr:              ":" + config.Port,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Could not start HTTP server: %v\n", err)
		log.Fatal(err)
	}
	localURL := fmt.Sprintf("http://127.0.0.1:%s", config.Port)
	log.Printf("EchoManager listening on port %s", config.Port)
	log.Printf("Local URL: %s", localURL)
	fmt.Printf("🌐 Local URL: %s\n", localURL)
	if lanIP := firstLANIPv4(); lanIP != "" {
		lanURL := fmt.Sprintf("http://%s:%s", lanIP, config.Port)
		log.Printf("LAN URL: %s", lanURL)
		fmt.Printf("🌐 LAN URL: %s\n", lanURL)
	}
	log.Printf("Manager data: %s", config.DataDir)
	log.Printf("Configuration: %s", config.ConfigPath)
	if runtime.requiresSetup() {
		log.Printf("Initialization required: open a printed URL to configure EchoKit")
		fmt.Println("ℹ️ Initialization required. Open one of the URLs above to configure EchoKit.")
	}
	fmt.Printf("✅ HTTP server started: %s\n", localURL)
	ctx, stop := signal.NotifyContext(context.Background(), managerSignals()...)
	defer stop()
	go func() {
		select {
		case <-ctx.Done():
		case <-shutdownRequests:
			stop()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Print("EchoManager stopped; managed processes were left running")
}

func configureManagerLog(dataDir string) {
	if dataDir == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			return
		}
		dataDir = filepath.Join(cacheDir, "EchoManager")
	}
	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(logDir, "manager.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	if managerLogFile != nil {
		_ = managerLogFile.Close()
	}
	managerLogFile = file
	log.SetOutput(file)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

func appAssetHandler(assets fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var relativePath string
		switch r.URL.Path {
		case "/", "/index.html":
			relativePath = "index.html"
		case "/app.js", "/app.css", "/settings-config.js", "/auth.js", "/auth.css", "/setup.js", "/setup.css", "/static/i18n.js", "/pwa.js", "/manifest.webmanifest", "/offline.html":
			relativePath = strings.TrimPrefix(r.URL.Path, "/")
		default:
			const assetPrefix = "/static/assets/"
			const stringsPrefix = "/static/strings/"
			if strings.HasPrefix(r.URL.Path, stringsPrefix) {
				relativePath = strings.TrimPrefix(r.URL.Path, "/")
				if filepath.Ext(relativePath) != ".json" || strings.Contains(relativePath, "..") {
					http.NotFound(w, r)
					return
				}
			} else if !strings.HasPrefix(r.URL.Path, assetPrefix) {
				http.NotFound(w, r)
				return
			} else {
				relativePath = strings.TrimPrefix(r.URL.Path, "/")
				extension := strings.ToLower(filepath.Ext(relativePath))
				if extension != ".svg" && extension != ".png" && extension != ".ico" {
					http.NotFound(w, r)
					return
				}
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		if relativePath == "manifest.webmanifest" {
			w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
		}
		serveFSAsset(w, r, assets, relativePath)
	})
}

func kitAssetHandler(root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const assetPrefix = "/kit/"
		if !strings.HasPrefix(r.URL.Path, assetPrefix) {
			http.NotFound(w, r)
			return
		}
		relativePath := strings.TrimPrefix(r.URL.Path, assetPrefix)
		extension := strings.ToLower(filepath.Ext(relativePath))
		allowed := (strings.HasPrefix(relativePath, "components/") && (extension == ".js" || extension == ".css")) ||
			(strings.HasPrefix(relativePath, "css/") && extension == ".css") ||
			(strings.HasPrefix(relativePath, "tokens/") && extension == ".css") ||
			(strings.HasPrefix(relativePath, "SFSymbols/") && extension == ".svg")
		if !allowed {
			http.NotFound(w, r)
			return
		}
		serveLocalAsset(w, r, root, relativePath)
	})
}

func serveLocalAsset(w http.ResponseWriter, r *http.Request, root, relativePath string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !fs.ValidPath(relativePath) || strings.Contains(relativePath, `\`) {
		http.NotFound(w, r)
		return
	}
	// EchoKit is loaded directly from the configured folder. Revalidate it so
	// component updates cannot leave a running browser on an older module.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Join(root, filepath.FromSlash(relativePath)))
}

func serveFSAsset(w http.ResponseWriter, r *http.Request, assets fs.FS, relativePath string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !fs.ValidPath(relativePath) || strings.Contains(relativePath, `\`) {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, assets, relativePath)
}

func firstLANIPv4() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	return ""
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
