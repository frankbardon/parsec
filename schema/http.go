package schema

import (
	"encoding/json"
	"net/http"
)

// Handler returns an http.Handler that serves registry snapshots and
// individual pattern lookups.
//
// Routes:
//
//	GET  <prefix>            — full Snapshot (all current patterns)
//	GET  <prefix>?channel=X  — resolve channel X to its owning pattern
//	GET  <prefix>?pattern=P  — fetch the named pattern (and full history
//	                           when &history=true)
//
// The handler does NOT enforce auth; mount it behind the bearer middleware
// when restricting access (the upgrade spec calls /parsec/schemas a
// public discovery surface, so default deployments expose it unguarded).
func Handler(r Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := req.URL.Query()
		if ch := q.Get("channel"); ch != "" {
			p, bindings, err := r.Resolve(ch)
			if err != nil {
				writeJSONError(w, http.StatusNotFound, err)
				return
			}
			_ = json.NewEncoder(w).Encode(struct {
				Pattern  ChannelPattern    `json:"pattern"`
				Bindings map[string]string `json:"bindings,omitempty"`
			}{p, bindings})
			return
		}
		if pat := q.Get("pattern"); pat != "" {
			if q.Get("history") == "true" {
				mr, ok := r.(*MemoryRegistry)
				if !ok {
					writeJSONError(w, http.StatusNotImplemented, errNoHistory)
					return
				}
				h, err := mr.History(pat)
				if err != nil {
					writeJSONError(w, http.StatusNotFound, err)
					return
				}
				_ = json.NewEncoder(w).Encode(struct {
					Pattern string           `json:"pattern"`
					History []ChannelPattern `json:"history"`
				}{pat, h})
				return
			}
			p, err := r.Get(pat)
			if err != nil {
				writeJSONError(w, http.StatusNotFound, err)
				return
			}
			_ = json.NewEncoder(w).Encode(p)
			return
		}
		mr, ok := r.(*MemoryRegistry)
		if ok {
			_ = json.NewEncoder(w).Encode(mr.Snapshot())
			return
		}
		_ = json.NewEncoder(w).Encode(struct {
			Patterns []ChannelPattern `json:"patterns"`
		}{r.List()})
	})
}

func writeJSONError(w http.ResponseWriter, status int, err error) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

var errNoHistory = &registryError{msg: "history unsupported for this registry backend"}

type registryError struct{ msg string }

func (e *registryError) Error() string { return e.msg }
