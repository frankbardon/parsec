package service_test

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/channels"
	perr "github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/internal/testutil"
)

func TestService_Manifest_ContainsExpectedFields(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()

	env := svc.Manifest(context.Background())
	if env.Kind != "parsec.manifest" {
		t.Errorf("Kind = %q", env.Kind)
	}
	if env.FormatVersion == "" {
		t.Errorf("FormatVersion empty")
	}
	if env.GeneratedAt.IsZero() {
		t.Errorf("GeneratedAt zero")
	}
	// The payload is a descriptor.Manifest by value.
	if env.Payload == nil {
		t.Fatalf("payload nil: %T", env.Payload)
	}
	// We cannot type-assert across packages without an import cycle; reach
	// through reflection by re-serialising. The shape check is done in
	// descriptor_test.go — here we just confirm key fields are non-empty.
	got := svc.Manifest(context.Background())
	type minimal struct {
		Service       string   `json:"service"`
		Version       string   `json:"version"`
		Surfaces      []string `json:"surfaces"`
		Visibilities  []string `json:"visibilities"`
		MaxPrivateTTL string   `json:"max_private_ttl"`
		KeyRotation   string   `json:"key_rotation"`
		ActiveKeyID   string   `json:"active_key_id"`
		Persistence   string   `json:"persistence"`
	}
	// We can rely on Manifest's payload exposing the field tags via JSON.
	_ = got
	// Direct shape probe: marshal envelope then unmarshal manifest payload.
	t.Run("payload-shape", func(t *testing.T) {
		raw := struct {
			Payload minimal `json:"payload"`
		}{}
		b, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		if raw.Payload.Service != "parsec" {
			t.Errorf("service = %q", raw.Payload.Service)
		}
		if raw.Payload.Version != testutil.Version {
			t.Errorf("version = %q, want %q", raw.Payload.Version, testutil.Version)
		}
		if len(raw.Payload.Surfaces) == 0 {
			t.Errorf("surfaces empty")
		}
		if len(raw.Payload.Visibilities) != 2 {
			t.Errorf("visibilities = %v", raw.Payload.Visibilities)
		}
		if raw.Payload.MaxPrivateTTL == "" {
			t.Errorf("MaxPrivateTTL empty")
		}
		if raw.Payload.KeyRotation != "supported" {
			t.Errorf("KeyRotation = %q", raw.Payload.KeyRotation)
		}
		if raw.Payload.ActiveKeyID == "" {
			t.Errorf("ActiveKeyID empty")
		}
		if raw.Payload.Persistence != "in-memory" {
			t.Errorf("Persistence = %q", raw.Payload.Persistence)
		}
	})
}

func TestService_OpenPublicChannel(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	ctx := context.Background()
	name := testutil.MustParseName(t, "public:test.x.y").String()
	ch, err := svc.OpenPublicChannel(ctx, name, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ch.State != channels.StateOpen {
		t.Errorf("State = %s", ch.State)
	}
	if ch.TTL != time.Minute {
		t.Errorf("TTL = %v", ch.TTL)
	}
}

func TestService_OpenPublicChannel_InvalidName(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	_, err := svc.OpenPublicChannel(context.Background(), "private:test.x.1", time.Minute)
	if err == nil {
		t.Fatal("expected error for private name on OpenPublic")
	}
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != perr.ChannelInvalid {
		t.Errorf("code = %s, want ChannelInvalid", pe.Code)
	}
}

func TestService_CreatePrivateChannel(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	creds, err := svc.CreatePrivateChannel(context.Background(), "user-1", "private:test.user.1.notif", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessToken == "" || creds.RefreshToken == "" {
		t.Fatal("missing tokens")
	}
}

func TestService_CreatePrivateChannel_TTLExceedsCap(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	_, err := svc.CreatePrivateChannel(context.Background(), "user-1", "private:test.user.1.notif", 2*time.Hour, nil)
	if err == nil {
		t.Fatal("expected TTL-exceeded error")
	}
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != perr.ChannelTTLExceeds {
		t.Errorf("code = %s, want ChannelTTLExceeds", pe.Code)
	}
}

func TestService_RefreshAccess(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	creds, err := svc.CreatePrivateChannel(context.Background(), "user-1", "private:test.user.1.notif", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.RefreshAccess(context.Background(), creds.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if res.AccessToken == "" {
		t.Fatal("AccessToken empty")
	}
}

func TestService_RefreshAccess_BadToken(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	_, err := svc.RefreshAccess(context.Background(), "garbage")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestService_ListGetDeleteChannels(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	ctx := context.Background()
	name := "public:test.x.y"
	if _, err := svc.OpenPublicChannel(ctx, name, time.Minute); err != nil {
		t.Fatal(err)
	}
	list := svc.ListChannels(ctx)
	if len(list) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(list))
	}
	ch, err := svc.GetChannel(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Name.String() != name {
		t.Errorf("name = %s", ch.Name)
	}
	if err := svc.DeleteChannel(ctx, name); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetChannel(ctx, name); err == nil {
		t.Fatal("expected GetChannel to fail after delete")
	}
}

func TestService_GetChannel_InvalidName(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	_, err := svc.GetChannel(context.Background(), "not-a-channel")
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != perr.ChannelInvalid {
		t.Errorf("code = %s, want ChannelInvalid", pe.Code)
	}
}

func TestService_DeleteChannel_InvalidName(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	err := svc.DeleteChannel(context.Background(), "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestService_Publish(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	ctx := context.Background()
	name := "public:test.x.y"
	if _, err := svc.OpenPublicChannel(ctx, name, time.Minute); err != nil {
		t.Fatal(err)
	}
	ack, err := svc.Publish(ctx, name, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if ack.Offset == 0 {
		t.Errorf("expected non-zero offset")
	}
	if ack.Epoch == "" {
		t.Errorf("epoch empty")
	}
}

func TestService_Publish_UnknownChannel(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	_, err := svc.Publish(context.Background(), "public:test.x.y", []byte("x"))
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != perr.ChannelNotFound {
		t.Errorf("code = %s, want ChannelNotFound", pe.Code)
	}
}

func TestService_Presence(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	ctx := context.Background()
	name := "public:test.x.y"
	if _, err := svc.OpenPublicChannel(ctx, name, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Presence(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("presence = %d, want 0", got)
	}
}

func TestService_Presence_InvalidName(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	_, err := svc.Presence(context.Background(), "not-a-channel")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestService_KeyManagement(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	ctx := context.Background()

	// initial listing has exactly one active key.
	initial := svc.ListKeys(ctx)
	if initial.ActiveKeyID == "" {
		t.Fatal("ActiveKeyID empty at boot")
	}
	if len(initial.Keys) != 1 {
		t.Fatalf("expected 1 key initially, got %d", len(initial.Keys))
	}
	if initial.Keys[0].Role != string(auth.RoleActive) {
		t.Errorf("initial role = %s", initial.Keys[0].Role)
	}

	// generate a new key.
	k2, err := svc.GenerateKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if k2.Role != string(auth.RoleVerifyOnly) {
		t.Errorf("new key role = %s, want verify-only", k2.Role)
	}
	if !strings.HasPrefix(k2.ID, "k-") {
		t.Errorf("new key id = %q", k2.ID)
	}

	// promote the new key.
	if err := svc.PromoteKey(ctx, k2.ID); err != nil {
		t.Fatal(err)
	}
	listing := svc.ListKeys(ctx)
	if listing.ActiveKeyID != k2.ID {
		t.Errorf("active = %s, want %s", listing.ActiveKeyID, k2.ID)
	}

	// retire the originally-active key.
	if err := svc.RetireKey(ctx, initial.Keys[0].ID); err != nil {
		t.Fatal(err)
	}

	// reload should fail without a state dir.
	err = svc.ReloadKeys(ctx)
	if err == nil {
		t.Fatal("expected ReloadKeys error without StateDir")
	}
	var pe *perr.Error
	if !stderrors.As(err, &pe) {
		t.Fatalf("expected coded error, got %T", err)
	}
	if pe.Code != perr.InvalidArgument {
		t.Errorf("code = %s, want InvalidArgument", pe.Code)
	}
}

func TestService_IssueMgmt(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	ctx := context.Background()
	res, err := svc.IssueMgmt(ctx, "operator-1", 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" {
		t.Fatal("token empty")
	}
	if res.SignedByKeyID == "" {
		t.Fatal("SignedByKeyID empty")
	}
	if !res.Expires.After(time.Now()) {
		t.Errorf("Expires not in future: %v", res.Expires)
	}
	// Verify via the parsec verifier.
	if _, err := svc.Parsec().Verifier().Verify(res.Token, auth.TypeMgmt); err != nil {
		t.Fatalf("mgmt verify: %v", err)
	}
}

func TestService_Parsec_Accessor(t *testing.T) {
	svc, stop := testutil.NewTestService(t)
	defer stop()
	if svc.Parsec() == nil {
		t.Fatal("Parsec() returned nil")
	}
}
