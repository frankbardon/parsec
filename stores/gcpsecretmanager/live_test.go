//go:build gcplive

// Package-level note: everything in this file runs against a real Google
// Secret Manager project and is excluded from every normal build. The
// rest of the suite runs against an in-process fake, and a fake can only
// encode what its author believed the API does. These tests are how those
// beliefs get checked — above all that UpdateSecret enforces the secret's
// etag, which is the single primitive the write lease is built on.
//
//	PARSEC_GCP_PROJECT=my-project \
//	PARSEC_GCP_CREDENTIALS=/path/to/sa.json \   # optional; ADC otherwise
//	  go test -tags gcplive -run TestLive -v ./...
//
// Each test creates its own secret and deletes it on the way out. They
// cost a handful of API calls and a few cents at most, but they do write
// to a real project — point them at a scratch one.
package gcpsecretmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/frankbardon/parsec/auth"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// liveSetup skips unless a project is configured, then returns a client
// and a secret id unique to this run.
func liveSetup(t *testing.T) (*secretmanager.Client, string, string) {
	t.Helper()
	project := os.Getenv("PARSEC_GCP_PROJECT")
	if project == "" {
		t.Skip("PARSEC_GCP_PROJECT not set; skipping live Secret Manager tests")
	}

	var opts []option.ClientOption
	if f := os.Getenv("PARSEC_GCP_CREDENTIALS"); f != "" {
		opts = append(opts, option.WithAuthCredentialsFile(option.ServiceAccount, f))
	}
	ctx := context.Background()
	client, err := secretmanager.NewClient(ctx, opts...)
	if err != nil {
		t.Fatalf("secret manager client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	secretID := fmt.Sprintf("parsec-keyring-live-%d", time.Now().UnixNano())
	name := fmt.Sprintf("projects/%s/secrets/%s", project, secretID)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.DeleteSecret(ctx, &secretmanagerpb.DeleteSecretRequest{Name: name}); err != nil {
			t.Logf("cleanup: delete %s: %v (delete it by hand)", name, err)
		}
	})
	return client, project, secretID
}

func liveStore(t *testing.T, client *secretmanager.Client, project, secretID string, tweak func(*Config)) *KeyRingStore {
	t.Helper()
	cfg := Config{
		ProjectID:         project,
		SecretID:          secretID,
		ReconcileInterval: time.Second,
		LockWait:          20 * time.Second,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	s, err := NewWithClient(cfg, client)
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	return s
}

// TestLiveEtagIsEnforcedOnUpdateSecret is the assumption the whole store
// rests on. If Secret Manager ever stops refusing a stale etag, the write
// lease stops excluding anything, the revision check under it becomes
// advisory, and two nodes rotating at once silently lose one of the
// rotations. Nothing in the fake suite can catch that; this can.
func TestLiveEtagIsEnforcedOnUpdateSecret(t *testing.T) {
	client, project, secretID := liveSetup(t)
	ctx := context.Background()
	s := liveStore(t, client, project, secretID, nil)

	if _, _, err := s.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	sec, err := client.GetSecret(ctx, &secretmanagerpb.GetSecretRequest{Name: s.SecretName()})
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	stale := sec.GetEtag()
	if stale == "" {
		t.Fatal("the secret came back with no etag; the lease has nothing to compare against")
	}

	// Move the etag on, the way another node taking the lease would.
	if _, err := s.setLockAnnotation(ctx, sec, "somebody-else:1"); err != nil {
		t.Fatalf("first update: %v", err)
	}

	// Now replay the original etag. This must be refused.
	_, err = client.UpdateSecret(ctx, &secretmanagerpb.UpdateSecretRequest{
		Secret: &secretmanagerpb.Secret{
			Name:        s.SecretName(),
			Etag:        stale,
			Annotations: map[string]string{lockAnnotation: "me:2"},
		},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"annotations"}},
	})
	if err == nil {
		t.Fatal("UpdateSecret accepted a stale etag: the write lease excludes nothing and " +
			"concurrent rotations can silently overwrite each other")
	}
	if !isContention(err) {
		t.Fatalf("stale etag returned %v, which isContention does not recognize; a lost lease race "+
			"would surface as a failed rotation instead of a retry", err)
	}
}

// TestLiveStaleSaveConflicts is the store's own contract, end to end
// against the real API.
func TestLiveStaleSaveConflicts(t *testing.T) {
	client, project, secretID := liveSetup(t)
	ctx := context.Background()

	a := liveStore(t, client, project, secretID, nil)
	b := liveStore(t, client, project, secretID, nil)

	ringA, _, err := a.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ringB, err := b.Load(ctx)
	if err != nil {
		t.Fatalf("b Load: %v", err)
	}

	keyA, err := ringA.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := a.Save(ctx, ringA); err != nil {
		t.Fatalf("a Save: %v", err)
	}
	if _, err := ringB.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := b.Save(ctx, ringB); !errors.Is(err, auth.ErrKeyRingConflict) {
		t.Fatalf("b Save from a stale ring: %v, want auth.ErrKeyRingConflict", err)
	}

	latest, err := a.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := latest.Get(keyA.ID); err != nil {
		t.Fatalf("a's rotation was erased: %v", err)
	}
}

// TestLiveConcurrentBootstrapConverges races real nodes against an empty
// secret.
func TestLiveConcurrentBootstrapConverges(t *testing.T) {
	client, project, secretID := liveSetup(t)
	ctx := context.Background()

	const nodes = 3
	rings := make([]*auth.KeyRing, nodes)
	errs := make([]error, nodes)
	done := make(chan int, nodes)
	start := make(chan struct{})
	for i := range nodes {
		s := liveStore(t, client, project, secretID, nil)
		go func() {
			<-start
			rings[i], _, errs[i] = s.Ensure(ctx)
			done <- i
		}()
	}
	close(start)
	for range nodes {
		<-done
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("node %d Ensure: %v", i, err)
		}
	}
	for i, r := range rings {
		if r.ActiveID() != rings[0].ActiveID() {
			t.Fatalf("node %d signs with %q, node 0 with %q — the fleet disagrees",
				i, r.ActiveID(), rings[0].ActiveID())
		}
	}
}

// TestLiveWatchSeesARemoteRotation checks that polling the "latest" alias
// actually observes another node's write, and that the alias resolves to
// a numeric version the way the revision logic assumes.
func TestLiveWatchSeesARemoteRotation(t *testing.T) {
	client, project, secretID := liveSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := liveStore(t, client, project, secretID, nil)
	writer := liveStore(t, client, project, secretID, nil)
	if _, _, err := watcher.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	changes := make(chan *auth.KeyRing, 4)
	go func() { _ = watcher.Watch(ctx, func(r *auth.KeyRing) { changes <- r }) }()

	remote, err := writer.Load(ctx)
	if err != nil {
		t.Fatalf("writer Load: %v", err)
	}
	rotated, err := remote.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := remote.Promote(rotated.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := writer.Save(ctx, remote); err != nil {
		t.Fatalf("writer Save: %v", err)
	}

	select {
	case got := <-changes:
		if got.ActiveID() != rotated.ID {
			t.Fatalf("watch delivered %q, want %q", got.ActiveID(), rotated.ID)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the rotation never reached the watching node")
	}
}

// TestLiveChecksumAndPayloadRoundTrip checks the two things the store
// asks of a payload: that the CRC32C it sends is accepted, and that the
// one it gets back describes the bytes it gets back.
func TestLiveChecksumAndPayloadRoundTrip(t *testing.T) {
	client, project, secretID := liveSetup(t)
	ctx := context.Background()
	s := liveStore(t, client, project, secretID, nil)

	ring, _, err := s.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// A ring with one of each algorithm, so the PEM-carrying entries make
	// the round trip too.
	if _, err := ring.GenerateEd25519(); err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	if _, err := ring.GenerateRSA(2048); err != nil {
		t.Fatalf("generate rsa: %v", err)
	}
	if err := s.Save(ctx, ring); err != nil {
		t.Fatalf("Save: %v", err)
	}

	back, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(back.List()) != len(ring.List()) {
		t.Fatalf("round trip returned %d keys, want %d", len(back.List()), len(ring.List()))
	}
	for _, k := range ring.List() {
		got, err := back.Get(k.ID)
		if err != nil {
			t.Fatalf("key %q (%s) missing after the round trip: %v", k.ID, k.Alg, err)
		}
		if got.Alg != k.Alg {
			t.Fatalf("key %q came back as %s, want %s", k.ID, got.Alg, k.Alg)
		}
	}
}
