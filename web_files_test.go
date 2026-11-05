package main

import (
	"net/http"
	"os"
	"path/filepath"
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
		"/page.js":     "javascript",
		"/theme.js":    "javascript",
		"/styles.css":  "text/css",
		"/success":     "text/html",
		"/success?cko-payment-id=pay_fakeaaaaaaaaaaaaaaaaaaaaaa": "text/html",
		"/success.js": "javascript",
	} {
		rec := serve(h, http.MethodGet, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), wantType) {
			t.Errorf("%s: status %d, type %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
		// Every file is revalidated, so the browser never runs a new page
		// script against a stale lib.js (the 5.4b bug).
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control %q, want no-cache", path, cc)
		}
	}
}

// Proves the success page is the success page (not index.html by accident)
// and loads only our own scripts. It never needs Flow: no card entry there.
func TestSuccessPageScripts(t *testing.T) {
	html, err := os.ReadFile("web/success.html")
	if err != nil {
		t.Fatal(err)
	}
	srcs := regexp.MustCompile(`<script[^>]*\ssrc="([^"]+)"`).FindAllStringSubmatch(string(html), -1)
	if len(srcs) != 1 || srcs[0][1] != "/success.js" {
		t.Errorf("success.html scripts %v, want only /success.js", srcs)
	}
}

// Proves no page script writes server data as HTML. The one innerHTML is a
// fixed string (the Delivery: Free line). Everything else uses textContent,
// so a name or reference from the server can't inject markup.
func TestNoInnerHTMLWithData(t *testing.T) {
	files, _ := filepath.Glob("web/*.js")
	assign := regexp.MustCompile(`\.innerHTML\s*=\s*(.*)`)
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range assign.FindAllStringSubmatch(string(src), -1) {
			if !strings.HasPrefix(m[1], `"<span>Delivery</span><span>Free</span>";`) {
				t.Errorf("%s: innerHTML set to %s", f, m[1])
			}
		}
	}
}

// Proves every element a page script looks up exists in its page. A typo in
// an ID only fails in the browser, as a null on first use, so check it here.
func TestScriptIDsExistInPages(t *testing.T) {
	pages := map[string][]string{
		"web/checkout.js": {"web/index.html"},
		"web/success.js":  {"web/success.html"},
		"web/page.js":     {"web/index.html", "web/success.html"}, // shared: both pages need every ID
	}
	lookup := regexp.MustCompile(`\$\("([A-Za-z0-9-]+)"\)`)
	for script, htmls := range pages {
		src, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		ids := lookup.FindAllStringSubmatch(string(src), -1)
		if len(ids) == 0 {
			t.Errorf("%s: no $(\"id\") lookups found, the pattern is out of date", script)
		}
		for _, h := range htmls {
			html, err := os.ReadFile(h)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range ids {
				if !strings.Contains(string(html), `id="`+m[1]+`"`) {
					t.Errorf("%s looks up #%s, missing in %s", script, m[1], h)
				}
			}
		}
	}
}
