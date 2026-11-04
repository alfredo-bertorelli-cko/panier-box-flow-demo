package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// testRouter builds the router with a fake config and an in-memory web folder,
// so tests don't depend on the real web/ files or the real env.
func testRouter(t *testing.T) (http.Handler, map[string]string) {
	t.Helper()
	env := validEnv()
	cfg, err := LoadConfig(fakeGetenv(env))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	web := fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html><title>Panier Box</title>")},
		"lib.js":          {Data: []byte("export const x = 1;")},
		"assets/logo.svg": {Data: []byte("<svg></svg>")},
		"assets/.gitkeep": {Data: nil},
	}
	return newRouter(cfg, web), env
}

// serve runs one request through the router and returns the recorded response.
func serve(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// Proves /api/config returns the public key and sandbox, as JSON, not cached.
func TestConfigHandlerReturnsPublicKey(t *testing.T) {
	h, env := testRouter(t)
	rec := serve(h, http.MethodGet, "/api/config")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q, want application/json", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", cc)
	}
	var got publicConfig
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.PublicKey != env[envPublicKey] || got.Environment != "sandbox" {
		t.Errorf("got publicKey match=%v environment=%q", got.PublicKey == env[envPublicKey], got.Environment)
	}
}

// Proves the body carries nothing server-only: no secret key, webhook key,
// subdomain or processing channel.
func TestConfigHandlerNeverLeaksServerValues(t *testing.T) {
	h, env := testRouter(t)
	body := serve(h, http.MethodGet, "/api/config").Body.String()
	serverOnly := map[string]string{
		envSecretKey:           env[envSecretKey],
		envWebhookSignatureKey: env[envWebhookSignatureKey],
		envSubdomain:           env[envSubdomain],
		envProcessingChannelID: env[envProcessingChannelID],
	}
	assertNoValues(t, body, serverOnly)
}

// Proves the response has exactly two keys. Adding a field must be a deliberate
// choice that also updates this test.
func TestConfigHandlerExactKeys(t *testing.T) {
	h, _ := testRouter(t)
	var got map[string]any
	if err := json.NewDecoder(serve(h, http.MethodGet, "/api/config").Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d keys, want 2 (publicKey, environment)", len(got))
	}
	for _, k := range []string{"publicKey", "environment"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
}

// Proves method routing is live: a POST to a GET route is 405, not served.
// Below "go 1.22" in go.mod this pattern would be a literal path (D-018).
func TestConfigHandlerRejectsOtherMethods(t *testing.T) {
	h, _ := testRouter(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if rec := serve(h, m, "/api/config"); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/config: status %d, want 405", m, rec.Code)
		}
	}
}

// Proves the static server serves real files and refuses unknown paths and
// directory listings.
func TestStaticFiles(t *testing.T) {
	h, _ := testRouter(t)
	tests := []struct {
		name     string
		target   string
		want     int
		wantType string
	}{
		{"root serves index.html", "/", http.StatusOK, "text/html"},
		{"a js file", "/lib.js", http.StatusOK, "javascript"},
		{"a nested file", "/assets/logo.svg", http.StatusOK, "image/svg+xml"},
		{"unknown file", "/nope.html", http.StatusNotFound, ""},
		{"directory with slash", "/assets/", http.StatusNotFound, ""},
		{"directory without slash", "/assets", http.StatusNotFound, ""},
		{"Go source is not served", "/config.go", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, http.MethodGet, tt.target)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d", rec.Code, tt.want)
			}
			if tt.wantType != "" && !strings.Contains(rec.Header().Get("Content-Type"), tt.wantType) {
				t.Errorf("Content-Type %q, want it to contain %q", rec.Header().Get("Content-Type"), tt.wantType)
			}
		})
	}
}

// Proves a path that climbs out with ".." never reaches a handler: ServeMux
// cleans it first and redirects into the web root, where it is a 404.
func TestStaticPathClimbingOut(t *testing.T) {
	h, _ := testRouter(t)
	rec := serve(h, http.MethodGet, "/../config.go")
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/config.go" {
		t.Fatalf("got %d to %q, want 307 to /config.go", rec.Code, rec.Header().Get("Location"))
	}
	if rec := serve(h, http.MethodGet, "/config.go"); rec.Code != http.StatusNotFound {
		t.Errorf("redirect target: status %d, want 404", rec.Code)
	}
}

// Proves "/" is a 404, not a directory listing, when there is no index.html.
func TestStaticRootWithoutIndex(t *testing.T) {
	cfg, err := LoadConfig(fakeGetenv(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	h := newRouter(cfg, fstest.MapFS{"lib.js": {Data: []byte("x")}})
	if rec := serve(h, http.MethodGet, "/"); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}
