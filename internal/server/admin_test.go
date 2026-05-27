package server_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/internal/server"
	"github.com/frankbardon/parsec/rpc"
	"github.com/frankbardon/parsec/service"
)

// adminServer boots a Parsec with the admin UI enabled (or not) and
// returns the composed HTTP handler wrapped in httptest.Server.
func adminServer(t *testing.T, enabled bool, validate server.BearerValidator) (*httptest.Server, *parsec.Parsec, func()) {
	t.Helper()
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := auth.NewKeyRingFromSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	p, err := parsec.New(parsec.Options{
		KeyRing:       ring,
		SweepInterval: 50 * time.Millisecond,
		Logger:        slog.New(slog.DiscardHandler),
		AdminUI:       parsec.AdminUIOptions{Enabled: enabled},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	go func() {
		_ = p.Run(ctx)
		close(doneCh)
	}()
	time.Sleep(75 * time.Millisecond)
	svc := service.New(p, "test")
	srv := httptest.NewServer(server.New(p, svc, slog.New(slog.DiscardHandler), validate))
	cleanup := func() {
		srv.Close()
		cancel()
		select {
		case <-doneCh:
		case <-time.After(2 * time.Second):
			t.Logf("warn: parsec.Run did not exit within 2s")
		}
	}
	return srv, p, cleanup
}

// TestAdminUI_DisabledReturns404 confirms /admin/* is fully absent when
// the toggle is off. No bytes from the embedded SPA reach the wire.
func TestAdminUI_DisabledReturns404(t *testing.T) {
	srv, _, stop := adminServer(t, false, nil)
	defer stop()
	cases := []string{
		"/admin",
		"/admin/",
		"/admin/index.html",
		"/admin/login.html",
		"/admin/app.js",
		"/admin/styles.css",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			resp, err := http.Get(srv.URL + p)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("%s: status=%d body=%s", p, resp.StatusCode, body)
			}
		})
	}
}

// TestAdminUI_EnabledServesAssets confirms /admin/* returns 200 when
// the toggle is on. The asset routes themselves don't require a bearer
// — only the Twirp calls the client makes do.
func TestAdminUI_EnabledServesAssets(t *testing.T) {
	srv, _, stop := adminServer(t, true, nil)
	defer stop()
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
			resp, err := http.Get(srv.URL + p)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("%s: status=%d body=%s", p, resp.StatusCode, body)
			}
			body, _ := io.ReadAll(resp.Body)
			if len(body) == 0 {
				t.Errorf("%s: empty body", p)
			}
		})
	}
}

// TestAdminUI_BearerStillEnforcedOnAPI confirms that enabling the admin
// UI does not weaken the Twirp bearer gate — every protected RPC
// continues to return 401 without a token.
func TestAdminUI_BearerStillEnforcedOnAPI(t *testing.T) {
	srv, p, stop := adminServer(t, true, nil)
	stop()
	_ = p
	// Re-boot with a real validator so the bearer middleware is active.
	srv, _, stop = adminServer(t, true, func(token string) error {
		// Reject every token — we want to confirm the 401 path.
		return auth.ErrMalformedToken
	})
	defer stop()
	resp, err := http.Post(srv.URL+rpc.ParsecServicePathPrefix+"ListChannels", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ListChannels status=%d, want 401", resp.StatusCode)
	}
}

// TestAdminUI_ManifestReportsToggle confirms admin_ui_enabled in the
// manifest tracks the option.
func TestAdminUI_ManifestReportsToggle(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
			srv, _, stop := adminServer(t, enabled, nil)
			defer stop()
			resp, err := http.Get(srv.URL + "/manifest")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			// Look for the literal "admin_ui_enabled":<bool> token in the
			// JSON. The manifest is nested in an Envelope but the field
			// name is unique.
			needle := `"admin_ui_enabled":true`
			if !enabled {
				needle = `"admin_ui_enabled":false`
			}
			if !strings.Contains(string(body), needle) {
				t.Errorf("expected %q in manifest, got: %s", needle, body)
			}
		})
	}
}
