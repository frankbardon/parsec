# Examples

Runnable parsec examples. Every example is a standalone `main` package
with its own `README.md`. Compile and run them with `go run`:

```bash
go run ./examples/scenarios/scoreboard
```

The fastest read is `scenarios/scoreboard` — one public channel, one
publish loop, no sinks. Start there and walk down the table.

## Scenarios

The seven scenarios from the original brief, each in its own directory:

| # | Directory                                  | Primitive                                              | Channel(s)                                                                 | Sink   |
|---|--------------------------------------------|--------------------------------------------------------|----------------------------------------------------------------------------|--------|
| 7 | [scoreboard](./scenarios/scoreboard)       | `Publish` on a public channel                          | `public:scoreboard.counter.ai_users`                                       | none   |
| 4 | [heartbeat](./scenarios/heartbeat)         | Periodic `Publish` driven by a ticker                  | `public:system.heartbeat.{service}`                                        | none   |
| 3 | [feedback-flag](./scenarios/feedback-flag) | `PublishOrSink` to a public ops channel                | `public:agent.feedback.flagged`                                            | slack  |
| 1 | [download-notify](./scenarios/download-notify) | `PublishOrSink` to a per-user private channel      | `private:webapp.user.{uid}.downloads`                                      | email  |
| 2 | [agent-analysis](./scenarios/agent-analysis) | Progress `Publish` + terminal `PublishOrSink`        | `private:agent.analysis.{job}.progress`                                    | email  |
| 5 | [nightly-report](./scenarios/nightly-report) | Date-scoped private channel, always-sink delivery    | `private:cron.report.{date}`                                               | email  |

## Integration smoke tests

The two original examples remain — they are smoke tests for the library
shape rather than scenario walkthroughs:

| Directory                                  | Purpose                                                            |
|--------------------------------------------|--------------------------------------------------------------------|
| [embedded](./embedded)                     | Minimal `parsec.New` + `OpenPublic` + `Publish`.                   |
| [notify-or-email](./notify-or-email)       | Original `PublishOrSink` + email sink + refresh token demonstration. |

## Browser end-to-end

| Directory | Purpose |
|---|---|
| [browser](./browser) | Full broker → websocket → browser path. Boots parsec, embeds an HTML page using `centrifuge-js`, and renders a 1 Hz heartbeat live. Open <http://localhost:8000> after `go run ./examples/browser`. The narrative walkthrough (public + private + WebTransport variants) is at [docs/src/getting-started/browser-client.md](../docs/src/getting-started/browser-client.md). |

## Conventions

Every scenario example:

- Uses `parsec.New` directly (no CLI shelling).
- Builds channel names with `channels.BuildName`, not `fmt.Sprintf`.
- Logs through `log/slog`, not `fmt.Println`.
- Self-cancels via a `context.WithTimeout` so `go run` exits 0 without
  Ctrl-C.
- Configures sinks against unreachable endpoints (closed SMTP port,
  in-process HTTP stub) so nothing is actually emailed or posted.
