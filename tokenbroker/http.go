package tokenbroker

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Handler returns an http.Handler that mounts the broker's three routes.
// The handler is path-prefix agnostic — mount it under whatever prefix
// the deployment prefers (the canonical mount is "/parsec").
//
// Routes (relative to the mount):
//
//	POST /token            — IssueRequest -> IssueResponse
//	POST /token/delegate   — DelegateRequest -> IssueResponse
//	POST /revoke           — RevokeRequest -> { "ok": true }
//
// All routes require Authorization: Bearer <user-token>. The bearer is
// passed verbatim to the Authenticator; the broker itself does no
// signature verification.
func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", b.handleIssue)
	mux.HandleFunc("POST /token/delegate", b.handleDelegate)
	mux.HandleFunc("POST /revoke", b.handleRevoke)
	return mux
}

func (b *Broker) handleIssue(w http.ResponseWriter, r *http.Request) {
	bearer := bearerOf(r)
	if bearer == "" {
		writeAuthRequired(w)
		return
	}
	var req IssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	resp, err := b.Issue(r.Context(), bearer, req)
	if err != nil {
		writeIssueError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (b *Broker) handleDelegate(w http.ResponseWriter, r *http.Request) {
	bearer := bearerOf(r)
	if bearer == "" {
		writeAuthRequired(w)
		return
	}
	var req DelegateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	resp, err := b.Delegate(r.Context(), bearer, req)
	if err != nil {
		writeIssueError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (b *Broker) handleRevoke(w http.ResponseWriter, r *http.Request) {
	bearer := bearerOf(r)
	if bearer == "" {
		writeAuthRequired(w)
		return
	}
	var req RevokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := b.Revoke(r.Context(), bearer, req); err != nil {
		writeIssueError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func bearerOf(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func writeIssueError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnauthenticated) {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	writeErr(w, http.StatusBadRequest, err)
}

func writeAuthRequired(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="parsec"`)
	writeErr(w, http.StatusUnauthorized, errors.New("authorization required"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
