# agent-analysis

Scenario 2 from the parsec brief. A long-running AI analysis publishes
incremental progress to `private:agent.analysis.{job}.progress` and a
final terminal event when the model finishes. Progress chatter uses
plain `Publish` (drop on the floor if nobody is watching). The terminal
event uses `PublishOrSink` so a user who walked away gets an email.

## How to run

```bash
go run ./examples/scenarios/agent-analysis
```

The example self-cancels after about 5 seconds. The email sink points
at a closed port so the fallback attempt fails quickly and is logged.

## What the output means

The four `progress` lines show intermediate publishes that never escape
the broker — if a websocket client is attached it sees them, otherwise
they vanish. The final event is the interesting one: with no live
subscriber, `PublishOrSink` invokes the email sink, and the warning is
the sink reporting that the SMTP target refused the connection. The
contract being demonstrated is *which* events deserve sink escalation,
not how to send mail.
