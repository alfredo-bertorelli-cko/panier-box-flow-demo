package main

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

const flowScriptURL = "https://checkout-web-components.checkout.com/index.js"

// Proves the hard rule on the real page: Flow loads only from Checkout.com's
// origin, and every other script is our own file. No CDN, no copy of Flow.
func TestIndexLoadsFlowOnlyFromCheckout(t *testing.T) {
	html, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	srcs := regexp.MustCompile(`<script[^>]*\ssrc="([^"]+)"`).FindAllStringSubmatch(string(html), -1)
	flow := 0
	for _, m := range srcs {
		switch src := m[1]; {
		case src == flowScriptURL:
			flow++
		case strings.HasPrefix(src, "/") && !strings.HasPrefix(src, "//"):
			// same-origin file of ours
		default:
			t.Errorf("script from an unexpected origin: %q", src)
		}
	}
	if flow != 1 {
		t.Errorf("Flow script tag found %d times, want exactly 1", flow)
	}
	// A file named like the Flow bundle in web/ would mean someone self-hosted it.
	if _, err := os.Stat("web/index.js"); err == nil {
		t.Error("web/index.js exists: Flow must never be self-hosted")
	}
}

// Proves the real web/ folder serves the page and its scripts, with a
// JavaScript MIME type. Browsers refuse a module script served as anything else.
func TestRealWebFilesServe(t *testing.T) {
	cfg, err := LoadConfig(fakeGetenv(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	h := newRouter(&app{cfg: cfg}, os.DirFS("web"))
	for path, wantType := range map[string]string{
		"/":            "text/html",
		"/checkout.js": "javascript",
		"/lib.js":      "javascript",
		"/styles.css":  "text/css",
	} {
		rec := serve(h, http.MethodGet, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), wantType) {
			t.Errorf("%s: status %d, type %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}
