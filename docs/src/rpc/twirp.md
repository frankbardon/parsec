# Twirp service

Parsec speaks Twirp v8 JSON over HTTP. The CLI is a thin wrapper around
this RPC; anything you can do from `parsec channels` you can also do
with a `curl`.

```bash
curl -X POST https://parsec.example.com/twirp/parsec.ParsecService/ListChannels \
  -H "Authorization: Bearer $PARSEC_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{}'
```

## Wire format

- Encoding: Twirp v8 **JSON** (not protobuf binary). Every request and
  response body is JSON.
- Path prefix: `/twirp/parsec.ParsecService/`.
- Method names follow the `service.proto` definition; the full RPC URL
  is the prefix plus the method name (e.g.
  `/twirp/parsec.ParsecService/Publish`).
- Headers: `Content-Type: application/json`,
  `Authorization: Bearer <mgmt-token>` (for non-public methods).
- Errors are coded — see [Errors](#errors) below.

## Auth

Every method except `Manifest` and `RefreshToken` requires a valid
mgmt bearer in the `Authorization` header. The bearer is verified by
`auth.Verifier` against the live keyring; tokens signed by a retired
key fail as if the signature were bad.

The public methods are:

```go
var PublicMethods = map[string]bool{
    MethodManifest:     true,
    MethodRefreshToken: true,
}
```

`Manifest` is public because it is the discovery endpoint.
`RefreshToken` is public because it validates its own (in-body)
refresh token instead of a bearer.

## Methods

The full method list is in `rpc/service.proto` and mirrored in
`rpc/types.go`. Grouped by intent:

### Discovery

| Method | Request | Response |
|---|---|---|
| `Manifest` | `Empty` | `JSONResponse` (descriptor envelope) |

### Channels

| Method | Request | Response |
|---|---|---|
| `OpenPublic` | `OpenPublicRequest` | `ChannelResponse` |
| `CreatePrivate` | `CreatePrivateRequest` | `Credentials` |
| `ListChannels` | `Empty` | `ListChannelsResponse` |
| `GetChannel` | `ChannelRef` | `ChannelResponse` |
| `DeleteChannel` | `ChannelRef` | `Empty` |

### Tokens

| Method | Request | Response |
|---|---|---|
| `RefreshToken` | `RefreshTokenRequest` | `RefreshTokenResponse` |
| `IssueMgmt` | `IssueMgmtRequest` | `IssueMgmtResponse` |

### Keys

| Method | Request | Response |
|---|---|---|
| `ListKeys` | `Empty` | `ListKeysResponse` |
| `GenerateKey` | `Empty` | `KeySummary` |
| `PromoteKey` | `KeyRef` | `Empty` |
| `RetireKey` | `KeyRef` | `Empty` |
| `ReloadKeys` | `Empty` | `Empty` |

### Publish / presence

| Method | Request | Response |
|---|---|---|
| `Publish` | `PublishRequest` | `PublishResponse` |
| `Presence` | `ChannelRef` | `PresenceResponse` |

The request and response shapes are declared in `rpc/types.go`. The
proto definition lives in `rpc/service.proto`; running `make proto`
will (eventually) regenerate `service.pb.go` and `service.twirp.go`
from the proto. Until that runs, the hand-rolled types are the
contract.

## Errors

Coded errors cross the boundary as Twirp errors. The mapping is one
function — `rpc/server.go:writeServiceError`. The Twirp code carries
the HTTP status; the response body carries the Parsec code as a
`meta.code` entry. The full code list:

- `PARSEC_CHANNEL_INVALID`
- `PARSEC_CHANNEL_NOT_FOUND`
- `PARSEC_CHANNEL_EXISTS`
- `PARSEC_CHANNEL_CLOSED`
- `PARSEC_CHANNEL_TTL_EXCEEDS_MAX`
- `PARSEC_AUTH_DENIED`
- `PARSEC_AUTH_EXPIRED`
- `PARSEC_BROKER_NOT_READY`
- `PARSEC_BROKER_INTERNAL`
- `PARSEC_SINK_UNAVAILABLE`
- `PARSEC_SINK_CONFIG_INVALID`
- `PARSEC_INVALID_ARGUMENT`
- `PARSEC_INTERNAL`

See [Troubleshooting](../ops/troubleshooting.md) for what to do when
they fire.

## Client

The reference Go client is in `rpc/client.go`. The CLI uses it
through `internal/rpcclient/`. Both are non-stable packages — if you
build an external Go client, generate one from the proto with
`make proto` once that target is wired.

## See also

- [`parsec channels`](../cli/channels.md) — the CLI surface of the
  same calls.
- [Architecture](../internals/architecture.md) — where the RPC server
  sits in the request flow.
