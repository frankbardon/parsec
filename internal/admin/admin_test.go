package admin

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEmbedFSPopulated confirms every advertised asset is actually
// embedded. Catches the case where a file is renamed in ui/ but the
// embed directive (or the knownPaths table) wasn't updated.
func TestEmbedFSPopulated(t *testing.T) {
	for _, name := range KnownAssets() {
		_, err := fs.ReadFile(uiFS, name)
		if err != nil {
			t.Errorf("asset %q missing from embed.FS: %v", name, err)
		}
	}
}

// TestHandlerServesKnownPaths walks every path the SPA exposes and
// confirms the handler returns 200 with non-empty bytes.
func TestHandlerServesKnownPaths(t *testing.T) {
	h := Handler()
	cases := []string{
		"/admin",
		"/admin/",
		"/admin/index.html",
		"/admin/channels.html",
		"/admin/keys.html",
		"/admin/dlq.html",
		"/admin/limits.html",
		"/admin/login.html",
		"/admin/app.js",
		"/admin/styles.css",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: status=%d body=%s", p, rr.Code, rr.Body.String())
			}
			if rr.Body.Len() == 0 {
				t.Fatalf("%s: empty body", p)
			}
		})
	}
}

// TestHandler404OnUnknown confirms the handler rejects paths that
// aren't on the allow-list.
func TestHandler404OnUnknown(t *testing.T) {
	h := Handler()
	cases := []string{
		"/admin/bogus.html",
		"/admin/../etc/passwd",
		"/admin/subdir/file.js",
		"/admin/index.htmlx",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("%s: status=%d (want 404)", p, rr.Code)
			}
		})
	}
}

// TestHandlerContentTypes confirms the handler stamps the right Content-Type
// on each asset class — operators serving the UI behind a reverse proxy
// that mis-detects MIME would otherwise see broken pages.
func TestHandlerContentTypes(t *testing.T) {
	h := Handler()
	cases := map[string]string{
		"/admin/index.html":  "text/html",
		"/admin/app.js":      "application/javascript",
		"/admin/styles.css":  "text/css",
	}
	for p, want := range cases {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		ct := rr.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, want) {
			t.Errorf("%s: Content-Type=%q, want prefix %q", p, ct, want)
		}
	}
}

// TestBundleSize asserts the total embedded payload stays under the 20 KB
// budget set in the phase 22 plan. If you add a new asset and trip this,
// trim something else — the UI is a diagnostic, not a product.
func TestBundleSize(t *testing.T) {
	const max = 20 * 1024
	total := 0
	for _, name := range KnownAssets() {
		b, err := fs.ReadFile(uiFS, name)
		if err != nil {
			t.Fatalf("read %q: %v", name, err)
		}
		total += len(b)
	}
	if total > max {
		t.Errorf("embedded UI bundle is %d bytes; budget is %d", total, max)
	}
	t.Logf("admin UI bundle: %d bytes", total)
}
