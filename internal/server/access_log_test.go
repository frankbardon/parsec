package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frankbardon/parsec/internal/server"
)

// TestAccessLog_EmitsOneLinePerRequest verifies the middleware emits a
// single INFO log line per request and that the documented fields are
// populated.
func TestAccessLog_EmitsOneLinePerRequest(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	h := server.AccessLog(server.AccessLogOptions{Logger: logger},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("ok"))
		}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/twirp/parsec.ParsecService/Publish")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d:\n%s", len(lines), buf.String())
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, lines[0])
	}
	for _, key := range []string{"method", "path", "status", "duration_ms", "remote_addr", "request_id", "trace_id", "bearer_subject"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("log line missing field %q: %v", key, entry)
		}
	}
	if entry["msg"] != "http_request" {
		t.Errorf("msg = %v, want http_request", entry["msg"])
	}
	if entry["status"].(float64) != 418 {
		t.Errorf("status = %v, want 418", entry["status"])
	}
	if entry["method"] != "GET" {
		t.Errorf("method = %v, want GET", entry["method"])
	}
	if entry["path"] != "/twirp/parsec.ParsecService/Publish" {
		t.Errorf("path = %v", entry["path"])
	}
	if entry["request_id"].(string) == "" {
		t.Errorf("request_id is empty")
	}
}

// TestAccessLog_RequestIDFromHeaderRoundTrips verifies the middleware
// honors X-Request-ID when present and echoes it back in the response.
func TestAccessLog_RequestIDFromHeaderRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := server.AccessLog(server.AccessLogOptions{Logger: logger},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("X-Request-ID", "client-supplied-id-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-ID"); got != "client-supplied-id-123" {
		t.Errorf("response X-Request-ID = %q, want client-supplied-id-123", got)
	}
	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["request_id"] != "client-supplied-id-123" {
		t.Errorf("log request_id = %v", entry["request_id"])
	}
}

// TestAccessLog_AutoGeneratesRequestID confirms an empty request lands
// with a freshly minted ID rather than blank.
func TestAccessLog_AutoGeneratesRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := server.AccessLog(server.AccessLogOptions{Logger: logger},
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Request-ID"); got == "" {
		t.Error("X-Request-ID header should be auto-set")
	}
}

// TestAccessLog_XFFTrustedProxy confirms X-Forwarded-For is honored
// only when the immediate peer is in the trusted set.
func TestAccessLog_XFFTrustedProxy(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	_, fullCIDR, _ := net.ParseCIDR("127.0.0.0/8")
	h := server.AccessLog(server.AccessLogOptions{
		Logger:         logger,
		TrustedProxies: []net.IPNet{*fullCIDR},
	}, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))

	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.42, 10.0.0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["remote_addr"] != "203.0.113.42" {
		t.Errorf("remote_addr = %v, want 203.0.113.42 (trusted XFF)", entry["remote_addr"])
	}
}

// TestAccessLog_XFFIgnoredWhenUntrusted verifies an unknown peer's XFF
// header is dropped.
func TestAccessLog_XFFIgnoredWhenUntrusted(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	// Trust only RFC1918 10.0.0.0/8; httptest is on 127.0.0.1 so XFF
	// should be ignored.
	_, cidr, _ := net.ParseCIDR("10.0.0.0/8")
	h := server.AccessLog(server.AccessLogOptions{
		Logger:         logger,
		TrustedProxies: []net.IPNet{*cidr},
	}, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.42")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["remote_addr"] == "203.0.113.42" {
		t.Errorf("untrusted XFF should be ignored, got %v", entry["remote_addr"])
	}
}

// TestAccessLog_NilLoggerBypass ensures a nil logger short-circuits the
// middleware.
func TestAccessLog_NilLoggerBypass(t *testing.T) {
	called := false
	h := server.AccessLog(server.AccessLogOptions{},
		http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			called = true
		}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Error("nil logger should pass through to next handler")
	}
}

// TestLogAuthFailure_NoLoggerInContext is a smoke test: when no access
// logger is in context, LogAuthFailure is a no-op (does not panic).
func TestLogAuthFailure_NoLoggerInContext(t *testing.T) {
	server.LogAuthFailure(context.Background(), "PARSEC_AUTH_DENIED")
}

// TestRequestID_RoundsTripThroughContext exercises the context helpers
// so changes to the context-key plumbing get caught by tests.
func TestRequestID_RoundsTripThroughContext(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	captured := ""
	h := server.AccessLog(server.AccessLogOptions{Logger: logger},
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			captured = server.RequestID(r.Context())
		}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if captured == "" {
		t.Error("request handler saw empty RequestID")
	}
}
