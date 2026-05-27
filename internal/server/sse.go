package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/channels"
)

// sseHandler implements a tiny Server-Sent Events fallback used by the CLI
// `subscribe` probe. It polls broker.History (via centrifuge.Node.History)
// every 200ms and emits any new publication as one `data: <json>\n\n` block.
//
// This is intentionally trivial — production consumers should connect via
// /connection/websocket and rely on Centrifuge's protocol. Subscribe-probe
// users tolerate a 200ms polling lag.
type sseHandler struct {
	p      *parsec.Parsec
	logger *slog.Logger
}

func (h *sseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("channel")
	if name == "" {
		http.Error(w, "channel query param required", http.StatusBadRequest)
		return
	}
	parsed, err := channels.ParseName(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var lastOffset uint64
	enc := json.NewEncoder(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			hist, err := h.p.Broker().Node().History(parsed.String())
			if err != nil {
				continue
			}
			for _, pub := range hist.Publications {
				if pub.Offset <= lastOffset {
					continue
				}
				lastOffset = pub.Offset
				_, _ = w.Write([]byte("data: "))
				_ = enc.Encode(map[string]any{
					"channel": parsed.String(),
					"offset":  pub.Offset,
					"data":    string(pub.Data),
				})
				_, _ = w.Write([]byte("\n"))
				flusher.Flush()
			}
		}
	}
}
