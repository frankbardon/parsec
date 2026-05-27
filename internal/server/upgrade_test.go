package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/parsectest"
	"github.com/frankbardon/parsec/schema"
	"github.com/frankbardon/parsec/telemetry"
	"github.com/frankbardon/parsec/tokenbroker"
)

func TestSchemaRegistryMounted(t *testing.T) {
	reg := schema.NewMemoryRegistry()
	additionalFalse := false
	_ = reg.Register(schema.ChannelPattern{
		Pattern: "sessions:{id}",
		Aspects: map[string]schema.Aspect{
			"data": {Name: "data", PayloadSchema: &schema.JSONSchema{
				Type:                 "object",
				Required:             []string{"text"},
				AdditionalProperties: &additionalFalse,
				Properties:           map[string]*schema.JSONSchema{"text": {Type: "string"}},
			}},
		},
	})
	inst := parsectest.NewServer(t, parsectest.WithOptions(func(o *parsec.Options) {
		o.SchemaHandler = schema.Handler(reg)
	}))
	resp, err := http.Get(inst.BaseURL + "/parsec/schemas")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"sessions:{id}"`) {
		t.Fatalf("body missing pattern: %s", body)
	}

	resp, err = http.Get(inst.BaseURL + "/parsec/schemas?channel=sessions:s_19")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"s_19"`) {
		t.Fatalf("resolve missing binding: %s", body)
	}
}

func TestTelemetryMounted(t *testing.T) {
	src := fakeTelemetrySource{}
	agg := telemetry.New(src)
	inst := parsectest.NewServer(t, parsectest.WithOptions(func(o *parsec.Options) {
		o.TelemetryHandler = agg.Handler()
	}))
	resp, err := http.Get(inst.BaseURL + "/parsec/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var snap telemetry.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Tokens.IssuedLastHour != 7 {
		t.Fatalf("expected 7, got %d", snap.Tokens.IssuedLastHour)
	}
}

type fakeTelemetrySource struct{}

func (fakeTelemetrySource) Channels(context.Context) telemetry.ChannelStats {
	return telemetry.ChannelStats{TotalActive: 3}
}
func (fakeTelemetrySource) Envelopes(context.Context) telemetry.EnvelopeStats {
	return telemetry.EnvelopeStats{}
}
func (fakeTelemetrySource) Presence(context.Context) telemetry.PresenceStats {
	return telemetry.PresenceStats{}
}
func (fakeTelemetrySource) History(context.Context) telemetry.HistoryStats {
	return telemetry.HistoryStats{}
}
func (fakeTelemetrySource) Tokens(context.Context) telemetry.TokenStats {
	return telemetry.TokenStats{IssuedLastHour: 7}
}
func (fakeTelemetrySource) Cache(context.Context) telemetry.CacheStats {
	return telemetry.CacheStats{}
}

func TestTokenBrokerMounted(t *testing.T) {
	ring, err := auth.NewKeyRingFromSecret([]byte("test-secret-1234-abcdefgh-test-1"))
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := auth.NewSigner(ring)
	issuer := auth.NewIssuer(signer)
	br, err := tokenbroker.New(tokenbroker.Options{
		Issuer:     issuer,
		Authorizer: tokenbroker.AllowAll,
		Authenticator: tokenbroker.AuthenticatorFunc(func(_ context.Context, bearer string) (tokenbroker.UserID, error) {
			if bearer == "u-tok" {
				return "u1", nil
			}
			return "", tokenbroker.ErrUnauthenticated
		}),
		DefaultTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	inst := parsectest.NewServer(t, parsectest.WithOptions(func(o *parsec.Options) {
		o.TokenBrokerHandler = br.Handler()
	}))

	body := strings.NewReader(`{"channels":["public:a.b.c"]}`)
	req, _ := http.NewRequest("POST", inst.BaseURL+"/parsec/token", body)
	req.Header.Set("Authorization", "Bearer u-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bb, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, bb)
	}
	var out tokenbroker.IssueResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Token == "" {
		t.Fatal("empty token")
	}
}
