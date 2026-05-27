package slack

import (
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/sinks"
)

func TestNew_RequiresWebhookURL(t *testing.T) {
	s, err := New(Config{})
	if err == nil {
		t.Fatal("expected error for missing WebhookURL")
	}
	if s != nil {
		t.Errorf("Sink should be nil on error")
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
	s, err := New(Config{WebhookURL: "https://hooks.slack/test"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "slack" {
		t.Errorf("Name = %q, want slack", s.Name())
	}
}

func TestSend_PostsToWebhookURL(t *testing.T) {
	var gotURL string
	var gotBody []byte
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	s, err := New(Config{WebhookURL: srv.URL + "/hook"})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(t.Context(), nil, sinks.Message{Subject: "title", Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != "/hook" {
		t.Errorf("URL = %q", gotURL)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	var decoded map[string]any
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("unmarshal body %q: %v", gotBody, err)
	}
	text, ok := decoded["text"].(string)
	if !ok || !strings.Contains(text, "*title*") {
		t.Errorf("expected subject formatted with asterisks; got %q", text)
	}
	if !strings.Contains(text, "hello") {
		t.Errorf("expected body in text; got %q", text)
	}
}

func TestSend_OmitsSubjectWhenEmpty(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := New(Config{WebhookURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(t.Context(), nil, sinks.Message{Body: "plain"}); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	if decoded["text"] != "plain" {
		t.Errorf("text = %v, want plain", decoded["text"])
	}
}

func TestSend_IncludesMetadata(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
	}))
	defer srv.Close()

	s, err := New(Config{WebhookURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(t.Context(), nil, sinks.Message{
		Body:     "b",
		Metadata: map[string]string{"channel": "C123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "C123") {
		t.Errorf("metadata should be in body: %s", body)
	}
}

func TestSend_MapsNon2xxToSinkUnavailable(t *testing.T) {
	cases := []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusNotFound, http.StatusForbidden}
	for _, status := range cases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			defer srv.Close()

			s, err := New(Config{WebhookURL: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			err = s.Send(t.Context(), nil, sinks.Message{Body: "b"})
			if err == nil {
				t.Fatal("expected error for non-2xx response")
			}
			var pe *errors.Error
			if !stderrors.As(err, &pe) {
				t.Fatalf("expected coded error, got %T", err)
			}
			if pe.Code != errors.SinkUnavailable {
				t.Errorf("code = %s, want SinkUnavailable", pe.Code)
			}
		})
	}
}

func TestSend_NetworkFailureReturnsSinkUnavailable(t *testing.T) {
	// Use a URL pointing at a definitely-closed server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()

	s, err := New(Config{WebhookURL: url})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Send(t.Context(), nil, sinks.Message{Body: "b"})
	if err == nil {
		t.Fatal("expected dial failure")
	}
	var pe *errors.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != errors.SinkUnavailable {
		t.Errorf("code = %s, want SinkUnavailable", pe.Code)
	}
}
