package parsec

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/frankbardon/parsec/auth"
)

// bufLogger returns a logger writing into buf so boot-time warnings can
// be asserted.
func bufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestRedisAddrActivatesRedisKeyring is the regression guard for the
// ordering bug: New resolved the keyring store before RedisAddr had been
// turned into a client, so every deployment configured through the YAML
// redis.addr field (the only path `parsec serve` offers) silently got a
// file-backed or ephemeral ring while the manifest reported "redis". On
// multi-node that means each node bootstraps its own keys and tokens
// minted by one node fail verification on the next.
func TestRedisAddrActivatesRedisKeyring(t *testing.T) {
	mr := miniredis.RunT(t)
	var buf bytes.Buffer

	p, err := New(Options{RedisAddr: mr.Addr(), Logger: bufLogger(&buf)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := p.KeyringStore().(*auth.RedisKeyRingStore); !ok {
		t.Fatalf("KeyringStore = %T, want *auth.RedisKeyRingStore", p.KeyringStore())
	}
	if got := p.KeyringPath(); got != "" {
		t.Errorf("KeyringPath = %q, want empty for a redis-backed ring", got)
	}
	if !mr.Exists("parsec:keyring") {
		t.Error("parsec:keyring absent from redis; the ring was not persisted there")
	}
	if p.Persistence() != "redis" {
		t.Errorf("Persistence = %q, want redis", p.Persistence())
	}
}

// A ring already in Redis must be adopted, not replaced — the failure
// this guards against is a second node bootstrapping its own keys.
func TestRedisAddrAdoptsExistingRing(t *testing.T) {
	mr := miniredis.RunT(t)

	first, err := New(Options{RedisAddr: mr.Addr(), Logger: bufLogger(&bytes.Buffer{})})
	if err != nil {
		t.Fatalf("New (first node): %v", err)
	}
	wantKID := first.KeyRing().ActiveID()

	second, err := New(Options{RedisAddr: mr.Addr(), Logger: bufLogger(&bytes.Buffer{})})
	if err != nil {
		t.Fatalf("New (second node): %v", err)
	}
	if got := second.KeyRing().ActiveID(); got != wantKID {
		t.Errorf("second node active key = %q, want %q — nodes do not share a ring", got, wantKID)
	}
}

// Redis wins the keyring precedence over StateDir, which makes a StateDir
// in the config misleading: it looks like a durable backup and is never
// written. Warn rather than quietly ignore it.
func TestRedisKeyringWarnsWhenStateDirAlsoSet(t *testing.T) {
	mr := miniredis.RunT(t)
	var buf bytes.Buffer
	dir := t.TempDir()

	p, err := New(Options{RedisAddr: mr.Addr(), StateDir: dir, Logger: bufLogger(&buf)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := p.KeyringStore().(*auth.RedisKeyRingStore); !ok {
		t.Fatalf("KeyringStore = %T, want redis to win over StateDir", p.KeyringStore())
	}
	if !strings.Contains(buf.String(), "redis is the only copy of the signing keys") {
		t.Errorf("no StateDir-ignored warning logged; got:\n%s", buf.String())
	}
}

// The URL form is what examples/config/parsec.docker.yaml ships. It used
// to build a client dialing a host named "redis://…": New returned no
// error and the first real command failed with "too many colons".
func TestRedisURLFormWorksEndToEnd(t *testing.T) {
	mr := miniredis.RunT(t)

	p, err := New(Options{RedisAddr: "redis://" + mr.Addr(), Logger: bufLogger(&bytes.Buffer{})})
	if err != nil {
		t.Fatalf("New with redis:// address: %v", err)
	}
	if _, ok := p.KeyringStore().(*auth.RedisKeyRingStore); !ok {
		t.Fatalf("KeyringStore = %T, want *auth.RedisKeyRingStore", p.KeyringStore())
	}
	if _, err := p.OpenPublic("public:app.room.general", time.Minute); err != nil {
		t.Fatalf("OpenPublic: %v", err)
	}
}

func TestMalformedRedisAddrFailsNew(t *testing.T) {
	_, err := New(Options{RedisAddr: "localhost", Logger: bufLogger(&bytes.Buffer{})})
	if err == nil {
		t.Fatal("New with a portless address = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "redis address") {
		t.Errorf("error %q does not name the redis address", err)
	}
}

// An unreachable Redis must fail boot, not defer the failure to whichever
// command runs first. go-redis dials lazily, so this needs an explicit
// ping.
func TestUnreachableRedisFailsNew(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()

	_, err := New(Options{RedisAddr: addr, Logger: bufLogger(&bytes.Buffer{})})
	if err == nil {
		t.Fatal("New against a closed redis = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "redis unreachable") {
		t.Errorf("error %q does not say redis is unreachable", err)
	}
}

// A negative RedisPingTimeout opts out of parsec's own reachability
// check. It cannot make boot tolerate an absent Redis on its own — the
// centrifuge shard connects eagerly — so the contract under test is
// narrow: parsec's check does not run.
func TestNegativePingTimeoutSkipsCheck(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()

	ring := auth.NewKeyRing()
	if _, err := ring.Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, err := New(Options{
		RedisAddr:        addr,
		RedisPingTimeout: -1,
		KeyRing:          ring,
		Logger:           bufLogger(&bytes.Buffer{}),
	})
	if err != nil && strings.Contains(err.Error(), "redis unreachable") {
		t.Errorf("parsec's ping ran despite RedisPingTimeout=-1: %v", err)
	}
}

// A pre-built client still gets pinged — the check is about reachability,
// not about which field supplied the client.
func TestSuppliedRedisClientIsPinged(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	mr.Close()

	_, err := New(Options{RedisClient: client, Logger: bufLogger(&bytes.Buffer{})})
	if err == nil {
		t.Fatal("New with a dead supplied client = nil error, want failure")
	}
	if !strings.Contains(err.Error(), "redis unreachable") {
		t.Errorf("error %q does not say redis is unreachable", err)
	}
}

// RedisAuth overrides must reach the client parsec builds.
func TestRedisAuthAppliedToBuiltClient(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.RequireUserAuth("parsec", "s3cret")

	p, err := New(Options{
		RedisAddr: mr.Addr(),
		RedisAuth: RedisAuth{Username: "parsec", Password: "s3cret"},
		Logger:    bufLogger(&bytes.Buffer{}),
	})
	if err != nil {
		t.Fatalf("New with RedisAuth: %v", err)
	}
	if err := p.opts.RedisClient.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("ping with credentials: %v", err)
	}
	// The broker shard authenticates separately; New succeeding at all
	// means the credentials reached it too.
	if len(p.opts.RedisShards) == 0 {
		t.Error("no redis shard built for the broker")
	}
}
