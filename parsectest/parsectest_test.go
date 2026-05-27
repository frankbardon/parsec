package parsectest_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/parsectest"
)

func TestNew_OpenPublicAndPublish(t *testing.T) {
	inst := parsectest.New(t)

	ch, err := inst.OpenPublic("public:webapp.system.status", time.Minute)
	if err != nil {
		t.Fatalf("OpenPublic: %v", err)
	}
	if ch == nil || ch.Name.String() != "public:webapp.system.status" {
		t.Fatalf("unexpected channel: %#v", ch)
	}

	res, err := inst.Publish(context.Background(), ch.Name.String(), []byte(`{"k":"v"}`))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.Offset == 0 && res.Epoch == "" {
		t.Fatalf("publish result empty: %#v", res)
	}
}

func TestNew_MintAccessVerifiesAgainstChannel(t *testing.T) {
	inst := parsectest.New(t)

	creds, err := inst.CreatePrivate("user-42", "private:webapp.user.42.downloads", 30*time.Minute, nil)
	if err != nil {
		t.Fatalf("CreatePrivate: %v", err)
	}

	claims, err := inst.Verifier().Verify(creds.AccessToken, auth.TypeAccess)
	if err != nil {
		t.Fatalf("Verify access: %v", err)
	}
	if claims.Sub != "user-42" {
		t.Fatalf("subject mismatch: %q", claims.Sub)
	}
	if len(claims.Chs) != 1 || claims.Chs[0] != "private:webapp.user.42.downloads" {
		t.Fatalf("channels mismatch: %v", claims.Chs)
	}
}

func TestNewServer_ManifestReachable(t *testing.T) {
	inst := parsectest.NewServer(t)

	resp, err := http.Get(inst.BaseURL + "/manifest")
	if err != nil {
		t.Fatalf("GET /manifest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"parsec"`) {
		t.Fatalf("manifest missing service identifier: %s", body)
	}
}

func TestNewServer_BearerGatePublicMethods(t *testing.T) {
	inst := parsectest.NewServer(t)

	// /healthz needs no bearer.
	resp, err := http.Get(inst.BaseURL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
}

func TestNewServer_MintMgmtAuthenticates(t *testing.T) {
	inst := parsectest.NewServer(t)
	bearer := inst.MintMgmt(t, "ops", time.Hour)
	if bearer == "" {
		t.Fatal("empty bearer")
	}
	claims, err := inst.Verifier().Verify(bearer, auth.TypeMgmt)
	if err != nil {
		t.Fatalf("verify mgmt: %v", err)
	}
	if claims.Sub != "ops" {
		t.Fatalf("sub mismatch: %q", claims.Sub)
	}
}

func TestNewWithRedis_OpenAndPublishViaRedisBroker(t *testing.T) {
	inst := parsectest.NewWithRedis(t)

	if _, err := inst.OpenPublic("public:test.cluster.events", time.Minute); err != nil {
		t.Fatalf("OpenPublic: %v", err)
	}
	if _, err := inst.Publish(context.Background(), "public:test.cluster.events", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

func TestNew_CleanupReturnsBeforeTimeout(t *testing.T) {
	// Ensures the t.Cleanup path actually halts Run within the 5s budget.
	// We construct an instance inside a subtest so its cleanup runs when
	// the subtest exits, then verify the parent test sees no orphaned
	// goroutine warnings (best-effort — Go's test runtime would flag a
	// hung Cleanup as a test timeout, so this is a smoke check).
	t.Run("inner", func(t *testing.T) {
		_ = parsectest.New(t)
	})
	// If we reach here the inner cleanup completed.
}
