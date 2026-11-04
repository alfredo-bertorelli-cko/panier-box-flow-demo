package main

import (
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
)

// newRouter wires every route. web is the front-end file system: os.DirFS("web")
// in main, an in-memory fstest.MapFS in tests.
func newRouter(cfg Config, web fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/config", handleConfig(cfg))
	mux.Handle("GET /", staticHandler(web))
	return mux
}

// publicConfig is everything the browser may know. It is its own struct, not
// built from Config, so a field added to Config later can't leak to the browser.
type publicConfig struct {
	PublicKey   string `json:"publicKey"`
	Environment string `json:"environment"`
}

// handleConfig gives Flow its public key. Names match the CheckoutWebComponents
// options, so the front end can pass them straight through.
func handleConfig(cfg Config) http.Handler {
	body := publicConfig{
		PublicKey:   cfg.PublicKey,
		Environment: "sandbox", // sandbox only, never production
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, body)
	})
}

// staticHandler serves files from web, with directory listings switched off:
// any path that is a directory (other than "/" with an index.html) is a 404.
func staticHandler(web fs.FS) http.Handler {
	files := http.FileServerFS(web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		info, err := fs.Stat(web, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent, so all we can do is log it.
		log.Printf("write json response: %v", err)
	}
}
