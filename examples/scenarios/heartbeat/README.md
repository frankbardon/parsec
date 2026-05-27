# heartbeat

Scenario 4 from the parsec brief. A background service publishes a
periodic heartbeat to `public:system.heartbeat.{service}`. Dashboards,
liveness probes, and anything else that wants to know the service is up
subscribe to the channel. No sink is registered — heartbeats are
intrinsically lossy, you only care about the most recent one.

## How to run

```bash
go run ./examples/scenarios/heartbeat
```

The example self-cancels after about 3 seconds, emitting roughly five
beats at 400 ms intervals before tearing down.

## What the output means

Each `heartbeat seq=N` line is one publication on the channel. A real
dashboard would subscribe over the websocket transport and update its
"last seen" indicator on every beat. The shutdown path is the
interesting bit: when the parent context is canceled, the ticker
goroutine returns cleanly via the `<-ctx.Done()` branch, and the example
exits without leaking goroutines.
