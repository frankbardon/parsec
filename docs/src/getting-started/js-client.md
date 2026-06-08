# JavaScript Client (`@frankbardon/parsec-client`)

`@frankbardon/parsec-client` is a thin composition wrapper around
[`centrifuge-js`](https://github.com/centrifugal/centrifuge-js) that
adds the Parsec conventions a real browser app needs: token refresh
with rotation, channel-name validation, manifest-driven transport
selection, coded errors that mirror `errors/codes.go`, and a (decode-
only) scope inspector for UIs that need to display token grants.

The package lives in-tree at `clients/js/` and is published to npm
under `@frankbardon/parsec-client`. The Centrifugo wire protocol is
unchanged — anything you can do with raw `centrifuge-js` (see
[Browser client](./browser-client.md)) you can still do.

## Install

```bash
npm install @frankbardon/parsec-client
```

`centrifuge` ships as a direct dependency — no extra install needed.

## Quickstart

```ts
import { ParsecClient } from "@frankbardon/parsec-client";

const client = new ParsecClient({
  endpoint: "https://parsec.example.com",
  accessToken: initialAccess,
  refreshToken: initialRefresh,
  accessExp: initialAccessExp, // unix seconds; 0 forces immediate refresh
});

client.on("terminal", (err) => console.error("re-authenticate:", err.code));

await client.connect();

const sub = client.newSubscription("public:webapp.lobby.global");
sub.on("publication", ({ data }) => console.log(data));
await sub.subscribe();

await client.publish("public:webapp.lobby.global", { kind: "hello" });
```

## Manifest auto-discovery

`connect()` GETs `/manifest`, intersects the advertised `transports`
with the client's preference list, and builds the endpoint array
centrifuge-js expects. The default client preference is
`["websocket", "http_stream"]`; override with `preferTransports` and
listen for `manifestUnavailable` to detect manifest fetch failures:

```ts
const client = new ParsecClient({
  endpoint: "https://parsec.example.com",
  preferTransports: ["webtransport", "websocket", "http_stream"],
});
client.on("manifestUnavailable", (err) => console.warn("manifest fetch failed", err));
```

To skip the manifest fetch entirely (e.g. tightly-locked-down
deployments behind a CDN that does not expose `/manifest`):

```ts
new ParsecClient({ endpoint: "...", manifest: false });
```

## Token refresh and rotation

The wrapper plumbs centrifuge-js's `getToken` callback into a
single-flight refresh state machine. A preemptive timer fires at
`accessExp − skew − jitter` so refresh almost always happens before
centrifuge has to ask. The refresh hits the Twirp `RefreshToken` RPC
at `/twirp/parsec.ParsecService/RefreshToken`.

Failure policy:

| Class | Examples | Action |
|---|---|---|
| **Terminal** | `PARSEC_AUTH_DENIED`, `PARSEC_AUTH_EXPIRED` | Clear store, emit `terminal`, return null to centrifuge (disconnects). Host app re-authenticates. |
| **Recoverable** | Network, `PARSEC_BROKER_NOT_READY`, `PARSEC_RATE_LIMITED` | Throw — centrifuge handles its own backoff and will retry. |

The wrapper always persists the response's `refresh_token` (even when
`rotated: false`), and it persists BEFORE resolving the promise to
centrifuge. Reusing a stale refresh after rotation triggers
family-revoke — the wrapper avoids that path by design. See the
[Refresh-Token Rotation](../ops/refresh-rotation.md) runbook for the
server side.

### Plugging in custom storage

`MemoryTokenStore` is the default. For session continuity across page
reloads, implement the `TokenStore` interface against your storage of
choice (cookies, IndexedDB, a session-storage adapter):

```ts
import type { TokenStore, TokenPair } from "@frankbardon/parsec-client";

class IndexedDbTokenStore implements TokenStore {
  async get(): Promise<TokenPair | null> { /* ... */ return null; }
  async set(pair: TokenPair): Promise<void> { /* ... */ }
  async clear(): Promise<void> { /* ... */ }
}

new ParsecClient({ endpoint: "...", tokenStore: new IndexedDbTokenStore() });
```

## Coded errors

Every error thrown by the wrapper is a `ParsecError` carrying a
`PARSEC_*` code that mirrors `errors/codes.go`. Branch on `.code`
instead of string-matching messages:

```ts
import { ParsecError, ParsecErrorCode } from "@frankbardon/parsec-client";

try {
  await client.publish(channel, payload);
} catch (err) {
  if (err instanceof ParsecError && err.code === ParsecErrorCode.RateLimited) {
    // back off
  }
}
```

The Twirp wire mapping mirrors `internal/rpcclient/errors.go`:
`unauthenticated → PARSEC_AUTH_EXPIRED`, `permission_denied →
PARSEC_AUTH_DENIED`, `resource_exhausted → PARSEC_RATE_LIMITED`, etc.

## Channel name validation

`parseName` and `buildName` mirror `channels.ParseName` in Go.
Subscribe / publish call `parseName` before delegating, so malformed
channels fail with `PARSEC_CHANNEL_INVALID` before centrifuge ever
sees them. You can call the helpers directly to validate
operator-typed input:

```ts
import { parseName, buildName, Visibility } from "@frankbardon/parsec-client/channels";

const name = buildName(Visibility.Private, "webapp", "user", userId, "inbox");
// → "private:webapp.user.<id>.inbox", validated end-to-end
```

## Scope inspector — decode only

`inspectScopes` decodes the JWT payload and returns a typed view a UI
can render. It does **NOT** verify the signature. The server is the
only authority on token validity; treat the result as display data,
not as an authorization decision.

```ts
import { inspectScopes } from "@frankbardon/parsec-client/scopes";

const view = inspectScopes(accessToken);
// view.channels, view.scopes (pattern/verbs/deny), view.expiresAt, view.expired
```

## Codegen interop (`parsec-gen --lang ts`)

`parsec-gen --lang ts` emits pattern constants and per-aspect payload
interfaces from your schema registry. Pair them with `typedSubscribe`
for end-to-end types:

```ts
import { sessionsChannel, type SessionsCursor } from "./gen/index";

const sub = client.typedSubscribe<SessionsCursor>(sessionsChannel(userId));
sub.on("publication", ({ data }) => {
  // data: SessionsCursor
});
await sub.subscribe();
```

The `T` parameter is a phantom type — runtime is the same
`Subscription` centrifuge-js returns. The type is a *promise*, not a
guarantee: the schema registry is the contract enforcer. Wire your
emitter, regenerate after schema changes, and the types will track.

## Versioning

The npm package versions independently from the Go server. Each
release declares a minimum Parsec server version. A server release
that bumps `descriptor.FormatVersion` is a wire change and requires a
matching client release.

## Reference

| Export | Module | Purpose |
|---|---|---|
| `ParsecClient` | `@frankbardon/parsec-client` | Composition wrapper |
| `ParsecError`, `ParsecErrorCode` | `@frankbardon/parsec-client` | Coded errors |
| `parseName`, `buildName`, `Visibility` | `@frankbardon/parsec-client/channels` | Channel grammar |
| `inspectScopes`, `decodeJwtPayload` | `@frankbardon/parsec-client/scopes` | Unverified JWT introspection |
| `MemoryTokenStore`, `TokenStore` | `@frankbardon/parsec-client` | Token persistence boundary |
| `fetchManifest`, `pickTransports` | `@frankbardon/parsec-client` | Manifest + transport ranking |
