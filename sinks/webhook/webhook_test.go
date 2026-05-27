package webhook

import (
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/sinks"
)

func TestNew_RequiresURL(t *testing.T) {
	s, err := New(Config{})
	if err == nil {
		t.Fatal("expected error")
	}
	if s != nil {
		t.Errorf("Sink should be nil")
	}
	var pe *errors.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != errors.SinkConfig {
		t.Fatalf("code = %s, want SinkConfig", pe.Code)
	}
}

func TestNew_Succeeds(t *testing.T) {
	s, err := New(Config{URL: "https://example.com/x"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "webhook" {
		t.Errorf("Name = %q, want webhook", s.Name())
	}
}

func TestSend_PostsJSONPayload(t *testing.T) {
	var gotMethod, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, _ := New(Config{URL: srv.URL})
	err := s.Send(t.Context(), nil, sinks.Message{Subject: "s", Body: "b", Metadata: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("Method = %q", gotMethod)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	var decoded map[string]any
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["subject"] != "s" || decoded["body"] != "b" {
		t.Errorf("payload mismatch: %#v", decoded)
	}
	if md, _ := decoded["metadata"].(map[string]any); md["k"] != "v" {
		t.Errorf("metadata mismatch: %#v", decoded["metadata"])
	}
}

func TestSend_HeadersAreInjected(t *testing.T) {
	var gotAuth, gotX string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotX = r.Header.Get("X-Trace-Id")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, _ := New(Config{
		URL: srv.URL,
		Headers: map[string]string{
			"Authorization": "Bearer test-bearer",
			"X-Trace-Id":    "abc",
		},
	})
	if err := s.Send(t.Context(), nil, sinks.Message{Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-bearer" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotX != "abc" {
		t.Errorf("X-Trace-Id = %q", gotX)
	}
}

func TestSend_RecipientOverrideURL(t *testing.T) {
	var hits1, hits2 atomic.Int32
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits1.Add(1); w.WriteHeader(http.StatusOK) }))
	defer srv1.Close()
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits2.Add(1); w.WriteHeader(http.StatusOK) }))
	defer srv2.Close()

	s, _ := New(Config{URL: srv1.URL})

	// Empty Recipient.URL → config URL.
	if err := s.Send(t.Context(), Recipient{}, sinks.Message{Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if hits1.Load() != 1 || hits2.Load() != 0 {
		t.Fatalf("expected hits1=1 hits2=0, got %d %d", hits1.Load(), hits2.Load())
	}

	// Non-empty Recipient.URL → override.
	if err := s.Send(t.Context(), Recipient{URL: srv2.URL}, sinks.Message{Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if hits1.Load() != 1 || hits2.Load() != 1 {
		t.Fatalf("expected hits1=1 hits2=1, got %d %d", hits1.Load(), hits2.Load())
	}
}

func TestSend_RecipientTypeOtherThanWebhookIsIgnored(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(http.StatusOK) }))
	defer srv.Close()

	s, _ := New(Config{URL: srv.URL})
	// Passing a non-Recipient does NOT panic; it falls back to the config URL.
	if err := s.Send(t.Context(), "string-recipient", sinks.Message{Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected hit on config URL, got %d", hits.Load())
	}
}

func TestSend_Non2xxReturnsSinkUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	s, _ := New(Config{URL: srv.URL})
	err := s.Send(t.Context(), nil, sinks.Message{Body: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *errors.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != errors.SinkUnavailable {
		t.Errorf("code = %s, want SinkUnavailable", pe.Code)
	}
	if !strings.Contains(pe.Msg, "Bad Gateway") {
		t.Errorf("msg should include status: %q", pe.Msg)
	}
}

func TestSend_NetworkFailureReturnsSinkUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()

	s, _ := New(Config{URL: url})
	err := s.Send(t.Context(), nil, sinks.Message{Body: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *errors.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != errors.SinkUnavailable {
		t.Errorf("code = %s, want SinkUnavailable", pe.Code)
	}
}
