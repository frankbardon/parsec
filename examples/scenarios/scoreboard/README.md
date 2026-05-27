# scoreboard

The simplest scenario in the parsec deck. A public broadcast channel
(`public:scoreboard.counter.ai_users`) receives a tick every time an AI
user completes a turn. Three ticks are published in succession, then the
example exits. No sink is registered: if nobody is subscribed, the ticks
are dropped on the floor — which is the correct behaviour for a counter
feed.

## How to run

```bash
go run ./examples/scenarios/scoreboard
```

The example self-cancels after about 200 ms of work and a 3 s timeout
ceiling. It needs no external services, no SMTP host, no Slack webhook.

## What the output means

Each `tick published` line prints the broker offset and epoch returned by
`parsec.Publish`. Offsets monotonically increase per channel; the epoch
changes if the channel is deleted and re-opened. The structured logs are
the actual primitive — a real consumer would subscribe over the
websocket transport and receive the same JSON payload.
