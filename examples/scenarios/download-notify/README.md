# download-notify

Scenario 1 from the parsec brief. A user starts an asynchronous export
(a long-running download), walks away from the browser, and a worker
finishes the job several minutes later. The worker calls
`PublishOrSink`: if the browser session is still subscribed to
`private:webapp.user.42.downloads`, the JSON payload is delivered over
the websocket; if presence is zero, parsec falls back to the registered
`email` sink.

## How to run

```bash
go run ./examples/scenarios/download-notify
```

The example self-cancels after about 5 seconds. The SMTP endpoint is set
to `127.0.0.1:1` (a closed port) on purpose, so the email fallback
attempt fails fast and is logged — no mail is actually sent.

## What the output means

The first log line shows the private channel and access-token expiry
returned by `CreatePrivate`. With no subscribers attached, presence is
zero, so `PublishOrSink` chooses the `email` sink. The
`email fallback attempted but failed (expected in demo)` warning is the
sink reporting that the SMTP target refused the connection. In
production you would point the sink at a real relay and the same code
path would deliver the message.
