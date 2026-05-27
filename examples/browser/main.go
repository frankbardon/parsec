// Browser end-to-end demo. Boots a parsec instance, hosts a single
// HTML page that connects via centrifuge-js, and pushes a heartbeat
// publication every second. Open http://localhost:8000 in a browser
// after running:
//
//	go run ./examples/browser
//
// The page renders incoming publications as a live log so you can see
// the broker → websocket → browser path end-to-end.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/frankbardon/parsec"
)

//go:embed index.html
var assets embed.FS

const channelName = "public:demo.system.heartbeat"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	dir, err := os.MkdirTemp("", "parsec-browser-demo-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	p, err := parsec.New(parsec.Options{
		StateDir: dir,
		Logger:   logger,
	})
	if err != nil {
		log.Fatalf("parsec.New: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runErr := make(chan error, 1)
	go func() { runErr <- p.Run(ctx) }()

	// Wait for the centrifuge node to finish booting before publishing.
	for !p.Broker().Started() {
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := p.OpenPublic(channelName, time.Hour); err != nil {
		log.Fatalf("OpenPublic: %v", err)
	}

	// Heartbeat publisher — one tick per second.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				payload, _ := json.Marshal(map[string]any{
					"ts":   time.Now().UTC().Format(time.RFC3339),
					"node": "browser-demo",
				})
				if _, err := p.Publish(ctx, channelName, payload); err != nil {
					logger.Warn("publish failed", "err", err)
				}
			}
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/connection/websocket", centrifuge.NewWebsocketHandler(p.Broker().Node(), centrifuge.WebsocketConfig{}))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		body, err := assets.ReadFile("index.html")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Inject the channel name so the demo HTML stays static.
		out := strings.ReplaceAll(string(body), "{{CHANNEL}}", channelName)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(out))
	})

	srv := &http.Server{Addr: ":8000", Handler: mux}
	go func() {
		fmt.Println("browser demo: open http://localhost:8000")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if err := <-runErr; err != nil && err != context.Canceled {
		log.Printf("parsec.Run: %v", err)
	}
}
