package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
)

const assetVersionPlaceholder = "{{ASSET_VERSION}}"

func serviceWorkerHandler(assets fs.FS) http.Handler {
	worker, err := fs.ReadFile(assets, "sw.js")
	if err != nil {
		return http.NotFoundHandler()
	}
	version := embeddedAssetVersion(assets)
	worker = bytes.ReplaceAll(worker, []byte(assetVersionPlaceholder), []byte(version))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Service-Worker-Allowed", "/")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(worker)
		}
	})
}

func embeddedAssetVersion(assets fs.FS) string {
	hash := sha256.New()
	_ = fs.WalkDir(assets, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(assets, path)
		if err != nil {
			return nil
		}
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		return nil
	})
	return hex.EncodeToString(hash.Sum(nil)[:8])
}
