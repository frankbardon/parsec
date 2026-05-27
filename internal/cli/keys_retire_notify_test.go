package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/frankbardon/parsec/internal/cli"
	"github.com/frankbardon/parsec/internal/testutil"
)

// TestKeysRetire_WithNotify_PostsToPeer wires a real parsec server
// (the local region) and one or two httptest peers (the remote
// regions). After local retire, the CLI must POST to each --notify
// URL with the operator's bearer header.
func TestKeysRetire_WithNotify_PostsToPeer(t *testing.T) {
	// Local region: a real parsec test server. We need a key to retire
	// — generate one and (since it joins as verify-only) it is safe to
	// retire without first demoting the active key.
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()

	// Mint a verify-only key on the local server so we have something
	// retire-able.
	genCmd := cli.KeysCommand()
	var genBuf bytes.Buffer
	setWriters(genCmd, &genBuf)
	if err := genCmd.Run(context.Background(), []string{
		"keys", "--server", srv.URL, "generate",
	}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	keyID := extractKeyID(t, genBuf.String())

	// Two peer servers.
	var peerAHits int32
	var peerBHits int32
	peerA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/ReloadKeys") {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&peerAHits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer peerA.Close()
	peerB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&peerBHits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer peerB.Close()

	// Run retire --notify against both peers.
	retireCmd := cli.KeysCommand()
	var retireBuf bytes.Buffer
	setWriters(retireCmd, &retireBuf)
	err := retireCmd.Run(context.Background(), []string{
		"keys", "--server", srv.URL, "--token", "test-operator-token",
		"retire", keyID,
		"--notify", peerA.URL,
		"--notify", peerB.URL,
	})
	if err != nil {
		t.Fatalf("retire --notify: %v", err)
	}
	if got := atomic.LoadInt32(&peerAHits); got != 1 {
		t.Errorf("peerA hits = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&peerBHits); got != 1 {
		t.Errorf("peerB hits = %d, want 1", got)
	}
	if !strings.Contains(retireBuf.String(), "parsec.key.retired") {
		t.Errorf("expected retired envelope, got %s", retireBuf.String())
	}
	if !strings.Contains(retireBuf.String(), "notified") {
		t.Errorf("expected `notified` payload key in output, got %s", retireBuf.String())
	}
}

// TestKeysRetire_NotifyFailureDoesNotAbort exercises the "best-
// effort propagation" contract: an unreachable peer logs in the
// `notified` array as ok=false but never causes the command to fail
// after the local retire already succeeded.
func TestKeysRetire_NotifyFailureDoesNotAbort(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()

	// Generate a key to retire.
	genCmd := cli.KeysCommand()
	var genBuf bytes.Buffer
	setWriters(genCmd, &genBuf)
	if err := genCmd.Run(context.Background(), []string{
		"keys", "--server", srv.URL, "generate",
	}); err != nil {
		t.Fatal(err)
	}
	keyID := extractKeyID(t, genBuf.String())

	// Spin up a peer that 500s every request so we can confirm the
	// retire command treats this as a warning, not a fatal error.
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer peer.Close()

	retireCmd := cli.KeysCommand()
	var out bytes.Buffer
	setWriters(retireCmd, &out)
	err := retireCmd.Run(context.Background(), []string{
		"keys", "--server", srv.URL, "retire", keyID, "--notify", peer.URL,
	})
	if err != nil {
		t.Fatalf("retire should not abort on peer failure: %v", err)
	}
	if !strings.Contains(out.String(), "notified") {
		t.Errorf("expected notified field with failure info: %s", out.String())
	}
}

// extractKeyID pulls the generated kid out of the keys.generated
// envelope JSON. We do not import the rpcclient summary types so the
// test stays decoupled from any future wire-shape change to that
// envelope.
func extractKeyID(t *testing.T, jsonOut string) string {
	t.Helper()
	// Use a minimal struct so test failures don't drag in unrelated
	// fields that future commits might add/rename.
	type wire struct {
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	var w wire
	if err := json.Unmarshal([]byte(jsonOut), &w); err != nil {
		t.Fatalf("decode keys.generated: %v (raw=%s)", err, jsonOut)
	}
	if w.Payload.ID == "" {
		t.Fatalf("no id in keys.generated output: %s", jsonOut)
	}
	return w.Payload.ID
}
