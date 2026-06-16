// Command playground boots an in-memory parsec instance and serves an
// interactive browser UI that exercises publish, subscribe, token
// issuance, sinks, DLQ, rate limits, and manifest inspection. It is a
// developer aid — not a deployment artifact. State is ephemeral, bearer
// auth is disabled, and the keyring is freshly bootstrapped on every
// boot.
//
// Run:
//
//	go run ./examples/playground
//
// Then open http://localhost:8000/playground in a browser.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/internal/server"
	"github.com/frankbardon/parsec/ratelimit"
	"github.com/frankbardon/parsec/service"
	"github.com/frankbardon/parsec/sinks"
)

//go:embed index.html
var assets embed.FS

const (
	addr    = ":8000"
	version = "playground"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	dir, err := os.MkdirTemp("", "parsec-playground-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	mem := newMemSink(200)
	flaky := &flakySink{}

	registry := sinks.NewRegistry()
	registry.Register(mem)
	registry.Register(flaky)

	p, err := parsec.New(parsec.Options{
		StateDir: dir,
		Logger:   logger,
		Sinks:    registry,
		RateLimits: ratelimit.RateLimits{
			Publish:   ratelimit.Limit{Rate: 5, Per: 10 * time.Second, Burst: 5},
			Subscribe: ratelimit.Limit{Rate: 10, Per: 10 * time.Second, Burst: 10},
		},
	})
	if err != nil {
		log.Fatalf("parsec.New: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runErr := make(chan error, 1)
	go func() { runErr <- p.Run(ctx) }()

	for !p.Broker().Started() {
		time.Sleep(5 * time.Millisecond)
	}

	mgmt, mgmtExp, err := p.Issuer().IssueMgmt("playground", 24*time.Hour)
	if err != nil {
		log.Fatalf("issue mgmt: %v", err)
	}

	svc := service.New(p, version)
	core := server.New(p, svc, logger, nil)

	mux := http.NewServeMux()
	mux.Handle("/playground", playgroundIndex(assets))
	mux.Handle("/playground/", playgroundAPI(p, mem, mgmt, mgmtExp))
	mux.Handle("/", core)

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		banner(addr, mgmt, mgmtExp)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if err := <-runErr; err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("parsec.Run: %v", err)
	}
}

func banner(addr, mgmt string, exp time.Time) {
	fmt.Println("──────────────────────────────────────────────────────────────")
	fmt.Println(" parsec playground")
	fmt.Println("──────────────────────────────────────────────────────────────")
	fmt.Printf(" UI:        http://localhost%s/playground\n", addr)
	fmt.Printf(" Twirp:     http://localhost%s/twirp/parsec.ParsecService/...\n", addr)
	fmt.Printf(" WebSocket: ws://localhost%s/connection/websocket\n", addr)
	fmt.Printf(" Manifest:  http://localhost%s/manifest\n", addr)
	fmt.Println()
	fmt.Println(" Bootstrap mgmt token (auth is disabled — token is for inspection):")
	fmt.Printf("   %s\n", mgmt)
	fmt.Printf("   exp = %s\n", exp.UTC().Format(time.RFC3339))
	fmt.Println(" Sample channels:")
	fmt.Println("   public:demo.heartbeat")
	fmt.Println("   private:demo.user.alice")
	fmt.Println("──────────────────────────────────────────────────────────────")
}

func playgroundIndex(fs embed.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body, err := fs.ReadFile("index.html")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body)
	})
}

func playgroundAPI(p *parsec.Parsec, mem *memSink, mgmt string, mgmtExp time.Time) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/playground/bootstrap.json", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"mgmt_token":       mgmt,
			"mgmt_token_exp":   mgmtExp.UTC().Format(time.RFC3339),
			"twirp_prefix":     "/twirp/parsec.ParsecService/",
			"websocket_url":    "/connection/websocket",
			"manifest_url":     "/manifest",
			"sample_channels":  []string{"public:demo.heartbeat", "private:demo.user.alice"},
			"sinks_registered": []string{"memsink", "flakysink"},
		})
	})

	mux.HandleFunc("/playground/sinks/memsink", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"captures": mem.Snapshot(),
		})
	})

	mux.HandleFunc("/playground/issue-access", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Sub     string         `json:"sub"`
			Channel string         `json:"channel"`
			TTLSecs int            `json:"ttl_secs"`
			Scopes  []scopeRequest `json:"scopes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Sub == "" || req.Channel == "" {
			http.Error(w, "sub and channel required", http.StatusBadRequest)
			return
		}
		ttl := time.Duration(req.TTLSecs) * time.Second
		if ttl <= 0 {
			ttl = 5 * time.Minute
		}
		exp := time.Now().Add(ttl)
		scopes := make([]auth.Scope, 0, len(req.Scopes))
		for _, s := range req.Scopes {
			verbs := make([]auth.Verb, 0, len(s.Verbs))
			for _, v := range s.Verbs {
				verbs = append(verbs, auth.Verb(v))
			}
			scopes = append(scopes, auth.Scope{Pattern: s.Pattern, Verbs: verbs})
		}
		tok, tokExp, err := p.Issuer().IssueAccess(req.Sub, req.Channel, exp, scopes)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": tok,
			"exp":          tokExp.UTC().Format(time.RFC3339),
		})
	})

	mux.HandleFunc("/playground/publish-or-sink", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Channel   string `json:"channel"`
			Payload   string `json:"payload"`
			SinkName  string `json:"sink_name"`
			Recipient string `json:"recipient"`
			Subject   string `json:"subject"`
			Body      string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Channel == "" || req.SinkName == "" {
			http.Error(w, "channel and sink_name required", http.StatusBadRequest)
			return
		}
		msg := sinks.Message{Subject: req.Subject, Body: req.Body}
		sunk, err := p.PublishOrSink(r.Context(), req.Channel, []byte(req.Payload), req.SinkName, demoRecipient{To: req.Recipient}, msg)
		resp := map[string]any{"sunk": sunk}
		if err != nil {
			resp["error"] = err.Error()
			writeJSON(w, http.StatusBadRequest, resp)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("/playground/check-ratelimit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Bucket  string `json:"bucket"`
			Subject string `json:"subject"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Bucket == "" || req.Subject == "" {
			http.Error(w, "bucket and subject required", http.StatusBadRequest)
			return
		}
		decision, err := p.CheckRateLimit(r.Context(), req.Bucket, req.Subject, nil)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"allowed":     decision.Allowed,
			"remaining":   decision.Remaining,
			"reset_ms":    decision.Reset.Milliseconds(),
			"limits_json": p.RateLimits(),
		})
	})

	return mux
}

type scopeRequest struct {
	Pattern string   `json:"pattern"`
	Verbs   []string `json:"verbs"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// demoRecipient is the playground's stand-in for a per-call sink address.
// Both demo sinks ignore the To field except for showing it in the
// capture buffer.
type demoRecipient struct {
	To string `json:"to"`
}

// memSink captures every payload in a bounded ring buffer so the UI can
// show what the sink would have delivered. Always succeeds.
type memSink struct {
	mu   sync.Mutex
	cap  int
	ring []memEntry
}

type memEntry struct {
	At        string `json:"at"`
	Recipient any    `json:"recipient"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
}

func newMemSink(capacity int) *memSink {
	return &memSink{cap: capacity, ring: make([]memEntry, 0, capacity)}
}

func (m *memSink) Name() string { return "memsink" }

func (m *memSink) Send(_ context.Context, to sinks.Recipient, msg sinks.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := memEntry{
		At:        time.Now().UTC().Format(time.RFC3339Nano),
		Recipient: to,
		Subject:   msg.Subject,
		Body:      msg.Body,
	}
	if len(m.ring) >= m.cap {
		copy(m.ring, m.ring[1:])
		m.ring[len(m.ring)-1] = entry
		return nil
	}
	m.ring = append(m.ring, entry)
	return nil
}

func (m *memSink) Snapshot() []memEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]memEntry, len(m.ring))
	copy(out, m.ring)
	return out
}

// flakySink always fails transiently — every call routes to DLQ once the
// retry budget is exhausted. Useful for exercising the DLQ tab.
type flakySink struct{}

func (*flakySink) Name() string { return "flakysink" }
func (*flakySink) Send(_ context.Context, _ sinks.Recipient, _ sinks.Message) error {
	return sinks.Transient(errors.New("simulated transient failure"))
}

