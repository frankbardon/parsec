// Package admin embeds the parsec administrative single-page application.
//
// The UI is intentionally minimal: vanilla HTML/JS/CSS with no build step,
// no framework, no external assets. It calls the existing Twirp surface
// using the operator's mgmt bearer (pasted into a login form and held in
// sessionStorage). The package exposes a single Handler that the HTTP
// surface mounts at /admin/* when Options.AdminUI.Enabled is true.
//
// Disabled mode returns 404 for every /admin/* path — the bytes never
// enter the response graph. Mount logic lives in internal/server/http.go.
package admin

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Files holds every embedded UI asset. Exposed so tests can introspect
// the bundle without going through the HTTP handler.
//
//go:embed ui/*
var Files embed.FS

// uiFS strips the leading "ui/" prefix so the handler maps URL paths to
// embedded files without each request paying for the strip.
var uiFS = mustSub(Files, "ui")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		// embed.FS is constructed at compile time; a missing subdir is a
		// build error, not a runtime one. Returning the parent FS keeps
		// the package non-panicky if the embed directive ever changes.
		return f
	}
	return sub
}

// IndexPath is the canonical landing-page name inside the embedded FS.
// The handler maps GET /admin and /admin/ to this file.
const IndexPath = "index.html"

// LoginPath is the login form's filename. Routes hitting this name are
// always served; every other route requires the caller to have a bearer
// in sessionStorage (the JS guard enforces it client-side; the actual
// Twirp calls are bearer-gated server-side).
const LoginPath = "login.html"

// knownPaths is the allow-list of files the SPA serves. Keeping it
// explicit (rather than letting http.FileServer auto-enumerate) means
// new files require a code change — operators won't accidentally ship
// stray assets via embed.FS.
var knownPaths = map[string]bool{
	"index.html":    true,
	"channels.html": true,
	"keys.html":     true,
	"dlq.html":      true,
	"limits.html":   true,
	"login.html":    true,
	"app.js":        true,
	"styles.css":    true,
}

// Handler returns an http.Handler that serves the embedded SPA. The
// returned handler expects to be mounted at /admin/ — it strips that
// prefix before doing the file lookup.
//
// Unknown paths return 404. The login page is served without any
// special-casing; the JS code on each protected page redirects to
// login.html when no token is in sessionStorage.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/admin")
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			name = IndexPath
		}
		// Normalise to defend against ".." attempts; embed.FS rejects
		// those but defence-in-depth keeps the handler obvious.
		name = path.Clean(name)
		if strings.HasPrefix(name, "..") || strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}
		if !knownPaths[name] {
			http.NotFound(w, r)
			return
		}
		f, err := uiFS.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		data, err := readAll(f)
		if err != nil {
			http.Error(w, "read asset", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType(name))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	})
}

// contentType returns the MIME type for a known UI asset. We don't use
// mime.TypeByExtension because the OS table is unreliable in containers
// and we own the full file list.
func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// readAll reads the entire file into memory. Assets are small (<5 KB
// each) so loading the whole file is cheaper than streaming.
func readAll(f fs.File) ([]byte, error) {
	return io.ReadAll(f)
}
