# Testing with `parsectest`

`github.com/frankbardon/parsec/parsectest` ships test helpers that
mirror `net/http/httptest`: one-call constructors return a ready
`*parsec.Parsec`, and teardown is registered via `testing.TB.Cleanup`.

The package lives in this repository — `import
"github.com/frankbardon/parsec/parsectest"` and you have everything.

## Quickstart

```go
package mypkg_test

import (
    "context"
    "testing"
    "time"

    "github.com/frankbardon/parsec/parsectest"
)

func TestMyPublisher(t *testing.T) {
    p := parsectest.New(t) // ephemeral keyring, in-memory everything

    ch, err := p.OpenPublic("public:webapp.system.status", time.Minute)
    if err != nil {
        t.Fatal(err)
    }
    if _, err := p.Publish(context.Background(), ch.Name.String(), []byte(`{"k":"v"}`)); err != nil {
        t.Fatal(err)
    }
}
```

`parsectest.New(t)` returns an `*Instance` that embeds `*parsec.Parsec`
— every library method is directly available. The broker is fully
booted before the constructor returns; the first `Publish` is safe.

## HTTP integration tests

`NewServer` mounts the parsec HTTP surface (Twirp + websocket + manifest
+ healthz) on a `*httptest.Server`:

```go
func TestMyClient(t *testing.T) {
    inst := parsectest.NewServer(t)

    bearer := inst.MintMgmt(t, "ops", time.Hour)
    req, _ := http.NewRequest("GET", inst.BaseURL+"/manifest", nil)
    req.Header.Set("Authorization", "Bearer "+bearer)
    resp, _ := http.DefaultClient.Do(req)
    // ...
}
```

`inst.BaseURL` is the `*httptest.Server.URL`. The bearer middleware is
wired through the parsec verifier, so tokens from `MintMgmt` or
`MintAccess` authenticate normally.

## Multi-node code paths (miniredis)

`NewWithRedis` starts an in-process [miniredis](https://github.com/alicebob/miniredis)
and points parsec at it. The broker, channel registry, keyring, DLQ,
and rate limiter all switch to their Redis backends — same code paths
production runs, no docker required:

```go
func TestClusterScenario(t *testing.T) {
    inst := parsectest.NewWithRedis(t)
    // inst now backs every subsystem with miniredis.
    // Spin up a second instance with the same WithRedis option to
    // simulate two nodes sharing state.
}
```

For tests that also want the HTTP surface:

```go
inst := parsectest.NewServerWithRedis(t)
```

## Minting tokens

| Method | Returns |
|---|---|
| `MintMgmt(t, subject, ttl)` | Mgmt bearer (`Authorization: Bearer`) |
| `MintAccess(t, subject, channel, ttl)` | Access token (websocket connect / private subscribe) |
| `MintRefresh(t, subject, channel, ttl)` | Refresh token for the `RefreshToken` RPC |
| `MintPair(t, subject, channel, ttl)` | `auth.PairResult` with both halves + expiries |

All helpers `t.Fatal` on issuance error — tests do not need to check.

## Options

The constructors take variadic `parsectest.Option` values:

```go
inst := parsectest.NewServer(t,
    parsectest.WithLogger(slog.Default()),
    parsectest.WithSink(myFakeSink),
    parsectest.WithRateLimits(ratelimit.RateLimits{
        Publish: ratelimit.Limit{Rate: 10, Per: time.Second},
    }),
)
```

| Option | Purpose |
|---|---|
| `WithLogger(*slog.Logger)` | Replace the default discard logger |
| `WithStateDir(dir)` | Override the keyring location (rarely useful) |
| `WithSink(sinks.Sink)` | Register a sink before retry/DLQ wrapping |
| `WithRateLimits(rl)` | Attach per-bucket rate limits |
| `WithRedis(client)` | Use a pre-built go-redis client (alternative to `NewWithRedis`) |
| `WithOptions(fn)` | Last-resort hook — receives `*parsec.Options` for any setting not surfaced above |

## What `parsectest.New` does

1. Builds `parsec.Options` with `StateDir: t.TempDir()` and a discard
   logger.
2. Applies your options on top.
3. Calls `parsec.New` and launches `Run` in a goroutine.
4. Polls until `Broker().Started()` reports ready (5 s budget).
5. Registers a `t.Cleanup` that cancels the run context, closes the
   `httptest.Server` if one was created, and waits up to 5 s for Run to
   exit.

The result is a fully-running parsec instance, with no manual teardown
in your test body.
