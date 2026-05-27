package schema

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frankbardon/parsec/envelope"
)

func sessionsPattern() ChannelPattern {
	return ChannelPattern{
		Pattern:     "sessions:{id}",
		Description: "session channels",
		Aspects: map[string]Aspect{
			"data": {
				Name: "data",
				PayloadSchema: &JSONSchema{
					Type:                 "object",
					Required:             []string{"text"},
					AdditionalProperties: new(bool),
					Properties: map[string]*JSONSchema{
						"text": {Type: "string"},
					},
				},
			},
			"cursor": {Name: "cursor"},
		},
	}
}

func TestPatternMatch(t *testing.T) {
	p, err := ParsePattern("sessions:{id}.events")
	if err != nil {
		t.Fatal(err)
	}
	b, ok := p.Match("sessions:s_19.events")
	if !ok {
		t.Fatal("expected match")
	}
	if b["id"] != "s_19" {
		t.Fatalf("binding: %v", b)
	}
	if _, ok := p.Match("sessions:s_19.foo"); ok {
		t.Fatal("should not match foo")
	}
}

func TestPatternDoubleStar(t *testing.T) {
	p, err := ParsePattern("brands:{slug}.**")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Match("brands:meridian.reports.daily"); !ok {
		t.Fatal("expected ** match")
	}
	if _, ok := p.Match("brands:meridian"); ok {
		t.Fatal("**) requires at least one segment after prefix")
	}
}

func TestPatternBadDoubleStarMid(t *testing.T) {
	if _, err := ParsePattern("a.**.b"); err == nil {
		t.Fatal("expected ** mid-pattern to fail")
	}
}

func TestRegistryRegisterResolve(t *testing.T) {
	r := NewMemoryRegistry()
	if err := r.Register(sessionsPattern()); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(sessionsPattern()); err == nil {
		t.Fatal("expected conflict")
	}
	p, b, err := r.Resolve("sessions:s_19")
	if err != nil {
		t.Fatal(err)
	}
	if b["id"] != "s_19" || p.Pattern != "sessions:{id}" {
		t.Fatalf("resolve: %v %v", p, b)
	}
}

func TestResolveSpecificity(t *testing.T) {
	r := NewMemoryRegistry()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(r.Register(ChannelPattern{Pattern: "brands:{slug}.**", Aspects: map[string]Aspect{"a": {Name: "a"}}}))
	must(r.Register(ChannelPattern{Pattern: "brands:meridian.reports", Aspects: map[string]Aspect{"b": {Name: "b"}}}))
	p, _, err := r.Resolve("brands:meridian.reports")
	if err != nil {
		t.Fatal(err)
	}
	if p.Pattern != "brands:meridian.reports" {
		t.Fatalf("most specific: %v", p)
	}
}

func TestRegistryUpdateHistory(t *testing.T) {
	r := NewMemoryRegistry()
	if err := r.Register(sessionsPattern()); err != nil {
		t.Fatal(err)
	}
	upd := sessionsPattern()
	upd.Description = "v2"
	if err := r.Update(upd); err != nil {
		t.Fatal(err)
	}
	h, err := r.History("sessions:{id}")
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[1].Version != 2 {
		t.Fatalf("history: %+v", h)
	}
}

func TestRegistrySubscribe(t *testing.T) {
	r := NewMemoryRegistry()
	ch, cancel := r.Subscribe()
	defer cancel()
	if err := r.Register(sessionsPattern()); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-ch:
		if c.Kind != ChangeRegistered || c.Pattern != "sessions:{id}" {
			t.Fatalf("change: %+v", c)
		}
	default:
		t.Fatal("no change broadcast")
	}
}

func TestValidatorStrict(t *testing.T) {
	r := NewMemoryRegistry()
	if err := r.Register(sessionsPattern()); err != nil {
		t.Fatal(err)
	}
	v := &Validator{Registry: r, Mode: ModeStrict}
	good := envelope.Envelope{
		Channel: "sessions:s_19", Aspect: "data",
		Payload: json.RawMessage(`{"text":"hi"}`),
	}
	if err := v.Check(good); err != nil {
		t.Fatalf("good rejected: %v", err)
	}
	bad := good
	bad.Payload = json.RawMessage(`{"text":42}`)
	if err := v.Check(bad); err == nil {
		t.Fatal("strict should reject")
	}
	missing := good
	missing.Aspect = "data"
	missing.Payload = json.RawMessage(`{}`)
	if err := v.Check(missing); err == nil {
		t.Fatal("missing required field should reject")
	}
}

func TestValidatorOffWarn(t *testing.T) {
	r := NewMemoryRegistry()
	if err := r.Register(sessionsPattern()); err != nil {
		t.Fatal(err)
	}
	bad := envelope.Envelope{
		Channel: "sessions:s_19", Aspect: "data",
		Payload: json.RawMessage(`{"text":42}`),
	}
	off := &Validator{Registry: r, Mode: ModeOff}
	if err := off.Check(bad); err != nil {
		t.Fatal("off mode rejected")
	}
	warn := &Validator{Registry: r, Mode: ModeWarn}
	if err := warn.Check(bad); err != nil {
		t.Fatal("warn mode rejected")
	}
}

func TestHTTPHandler(t *testing.T) {
	r := NewMemoryRegistry()
	if err := r.Register(sessionsPattern()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(r))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := mustBody(t, resp)
	if !strings.Contains(body, `"sessions:{id}"`) {
		t.Fatalf("snapshot missing pattern: %s", body)
	}

	resp, err = http.Get(srv.URL + "/?channel=sessions:s_19")
	if err != nil {
		t.Fatal(err)
	}
	body = mustBody(t, resp)
	if !strings.Contains(body, `"s_19"`) {
		t.Fatalf("resolve missing binding: %s", body)
	}

	resp, err = http.Get(srv.URL + "/?channel=unknown")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func mustBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := readAll(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readAll(resp *http.Response) ([]byte, error) {
	const max = 1 << 20
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, err
		}
		if len(buf) > max {
			return buf, nil
		}
	}
}
