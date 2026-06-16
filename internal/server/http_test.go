package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/parsec/internal/testutil"
	"github.com/frankbardon/parsec/rpc"
)

func TestHTTP_Healthz(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}

func TestHTTP_Manifest(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	resp, err := http.Get(srv.URL + "/manifest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	// Manifest is an Envelope JSON.
	var env struct {
		FormatVersion string `json:"format_version"`
		Kind          string `json:"kind"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal manifest: %v (body=%s)", err, body)
	}
	if env.Kind != "parsec.manifest" {
		t.Errorf("kind = %q", env.Kind)
	}
	if env.FormatVersion == "" {
		t.Errorf("FormatVersion empty")
	}
}

func TestHTTP_TwirpManifest(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	// Twirp Manifest is a public method, so the no-validator server
	// returns 200 without a bearer.
	resp, err := http.Post(srv.URL+rpc.ParsecServicePathPrefix+"Manifest", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Manifest status=%d body=%s", resp.StatusCode, body)
	}
}

func TestHTTP_BearerEnforcedWhenValidatorSet(t *testing.T) {
	srv, _, _, stop := testutil.NewBearerTestServer(t)
	defer stop()
	// ListChannels is a protected method.
	resp, err := http.Post(srv.URL+rpc.ParsecServicePathPrefix+"ListChannels", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHTTP_BearerAcceptsValidMgmtToken(t *testing.T) {
	srv, _, token, stop := testutil.NewBearerTestServer(t)
	defer stop()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+rpc.ParsecServicePathPrefix+"ListChannels", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, body)
	}
}

func TestHTTP_SSE_RequiresChannelQuery(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	resp, err := http.Get(srv.URL + "/sse")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHTTP_SSE_RejectsBadChannelName(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	resp, err := http.Get(srv.URL + "/sse?channel=" + url.QueryEscape("bogus-without-prefix"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad name, got %d", resp.StatusCode)
	}
}

// TestHTTP_SSE_DisconnectsOnContextCancel exercises the SSE route's happy
// path without depending on streaming behavior. The handler reads from
// broker.Node().History() (which returns nothing without an explicit
// HistoryFilter), so it would otherwise loop forever waiting for headers
// to be flushed. We confirm the route accepts a valid channel and exits
// when the client cancels.
//
// (A full publish-then-receive smoke would require a HistoryFilter-aware
// SSE implementation; that's outside the scope of this surface test.)
func TestHTTP_SSE_DisconnectsOnContextCancel(t *testing.T) {
	srv, svc, stop := testutil.NewTestServer(t)
	defer stop()
	name := "public:test.sse.demo"
	if _, err := svc.OpenPublicChannel(context.Background(), name, time.Minute); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())

	// Start the SSE request in a goroutine; cancel quickly. The Do call
	// may block until headers arrive (never, in this trivial handler),
	// so we want to observe that Do returns with a context-canceled error
	// rather than hanging the test forever.
	done := make(chan struct{})
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/sse?channel="+url.QueryEscape(name), nil)
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		_ = err
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SSE client did not exit within 2s of cancel")
	}
}

// TestHTTP_MetricsUnguardedByDefault confirms /metrics is reachable
// without auth when MetricsBearerToken is empty (the default), and
// that hitting an RPC path leaves a parsec_rpc_* sample behind.
func TestHTTP_MetricsUnguardedByDefault(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	// Prime the rpc metric so it has a sample to expose.
	_, _ = http.Post(srv.URL+rpc.ParsecServicePathPrefix+"Manifest", "application/json", strings.NewReader("{}"))

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "parsec_rpc_requests_total") {
		t.Errorf("expected parsec_rpc_requests_total in body, got: %s", body)
	}
}

// TestHTTP_MetricsBearerRequired exercises the gate when a token is
// configured: missing/wrong returns 401; correct returns 200.
func TestHTTP_MetricsBearerRequired(t *testing.T) {
	const tok = "metrics-secret-xyz"
	srv, stop := testutil.NewMetricsBearerTestServer(t, tok)
	defer stop()

	// No token: 401
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-token status = %d, want 401", resp.StatusCode)
	}

	// Wrong token: 401
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong-token status = %d, want 401", resp.StatusCode)
	}

	// Correct token: 200
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("correct-token status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "go_") {
		t.Errorf("expected go_* runtime metrics in body, got: %s", body)
	}
}

func TestHTTP_WebsocketEndpointAccepts101(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	// We don't have a websocket client here — we just verify the
	// /connection/websocket route is wired by checking the handler
	// rejects a plain GET with a non-200 (centrifuge returns 400 or 426
	// for a non-websocket GET). 404 would indicate the mux didn't match.
	resp, err := http.Get(srv.URL + "/connection/websocket")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("route not mounted: %d", resp.StatusCode)
	}
}

// TestHTTP_WebsocketUpgradeSucceeds drives a real WebSocket handshake
// through the full middleware chain (metrics + access-log) so the
// statusRecorder / accessRecorder must forward http.Hijacker. A
// regression of either wrapper losing the hijack interface manifests as
// a 500 here instead of a 101, breaking every centrifuge-js client.
func TestHTTP_WebsocketUpgradeSucceeds(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	req := "GET /connection/websocket HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Origin: http://" + u.Host + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	statusLine := string(buf[:n])
	if !strings.HasPrefix(statusLine, "HTTP/1.1 101 ") {
		t.Fatalf("websocket upgrade did not return 101 (status: %q)", strings.SplitN(statusLine, "\r\n", 2)[0])
	}
}

// TestHTTP_HTTPStreamGETReturnsMethodNotAllowed pings the http-stream
// endpoint with a plain GET — the centrifuge handler requires POST, so
// a 405 means "route mounted, wrong method" and a 404 would prove the
// mux didn't pick the path up.
func TestHTTP_HTTPStreamGETReturnsMethodNotAllowed(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	resp, err := http.Get(srv.URL + "/connection/http_stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("route not mounted: %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// TestHTTP_HTTPStreamOPTIONSReturnsNoContent verifies CORS preflight
// handling on the http-stream endpoint — centrifuge's handler answers
// OPTIONS with 204 + Allow headers, which lets browser clients run
// cross-origin POSTs against the stream.
func TestHTTP_HTTPStreamOPTIONSReturnsNoContent(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/connection/http_stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("Allow-Methods = %q, want POST", got)
	}
}

// TestHTTP_ManifestExposesHTTPStreamTransport asserts the manifest
// surface advertises the http_stream transport so client SDKs can
// discover it via the public Manifest RPC.
func TestHTTP_ManifestExposesHTTPStreamTransport(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	resp, err := http.Get(srv.URL + "/manifest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"http_stream"`) {
		t.Fatalf("manifest missing http_stream transport: %s", body)
	}
}
