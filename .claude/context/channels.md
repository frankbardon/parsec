# Channels — Parsec

Load when touching channel naming, lifecycle, or TTL handling.

## Grammar

`<visibility>:<app>.<domain>[.<id>][.<topic>]`

There is exactly one validator: `channels.ParseName`. Every surface
calls it. Do not reimplement it.

| Rule | Where |
|---|---|
| Visibility must be `public` or `private` | `channels/name.go` |
| Private channels MUST have an id segment | `channels/name.go` |
| Private TTL is capped at 1h | `channels/manager.go` |
| Components are lowercase ASCII + digits + `-` + `_` | `channels/name.go` |
| `:` is reserved for the visibility prefix | `channels/name.go` |

## JS client mirror

`clients/js/src/channels.ts` is a port of `channels/name.go` and MUST
stay in sync. Any grammar change updates both files in the same PR and
mirrors the new test case in `clients/js/test/channels.test.ts`.
