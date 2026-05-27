# nightly-report

Scenario 5 from the parsec brief. A scheduled cron mints a per-date
private channel (`private:cron.report.{date}`), publishes the rolled-up
report, and expects no live subscriber because the user is asleep.
`PublishOrSink` therefore routes through the email sink every night.

## How to run

```bash
go run ./examples/scenarios/nightly-report
```

The example self-cancels after about 5 seconds. The SMTP target is
`127.0.0.1:1` (closed) so the fallback fails fast and is logged — point
it at a real relay in production.

## What the output means

The channel id segment is today's UTC date in `yyyy-mm-dd` form, which
fits the channel grammar (lowercase ASCII + digits + dashes). Because no
subscriber is attached, `PublishOrSink` immediately escalates to the
email sink. The warning is the sink reporting that the SMTP target
refused the connection; the contract being demonstrated is that the
publisher does not branch on presence — parsec does, once.
