# feedback-flag

Scenario 3 from the parsec brief. A user clicks thumbs-down on an AI
answer. The event publishes to `public:agent.feedback.flagged`; if no
internal dashboard is subscribed, parsec falls back to the registered
Slack sink so on-call humans see the alert.

## How to run

```bash
go run ./examples/scenarios/feedback-flag
```

The example self-cancels after about 5 seconds. It spins up an
in-process HTTP stub that impersonates a Slack incoming webhook, so the
example needs no network access — point the real `WebhookURL` at
`hooks.slack.com/services/...` in production.

## What the output means

`ops channel open` confirms the public channel is alive. With no
subscribers attached, `PublishOrSink` calls the Slack sink, the stub
server logs the inbound POST, and the example prints `flag escalated to
slack (no live ops subscriber)`. The primitive being demonstrated is
that the same line of code (`PublishOrSink`) handles both the "humans
are watching" and "humans walked away" cases — the publisher does not
need to know which.
