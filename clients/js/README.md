# @frankbardon/parsec-client

Browser + Node client for the [Parsec](https://github.com/frankbardon/parsec)
realtime messaging engine. Thin composition-only wrapper over
[centrifuge-js](https://github.com/centrifugal/centrifuge-js) that adds the
Parsec conventions a real client app needs:

- **Token refresh + rotation** — calls the Twirp `RefreshToken` RPC,
  single-flight, preemptive timer, terminal vs recoverable failure policy.
- **Channel-name validation** — port of the Go `channels.ParseName`
  validator. Bad names fail before centrifuge sees them.
- **Manifest-driven transport selection** — reads `/manifest`, intersects
  the server's advertised transports with the client's preference list.
- **Coded errors** — every error carries a `PARSEC_*` code mirroring the
  server's `errors/codes.go`.
- **Scope inspector** — decodes (does NOT verify) the JWT payload so UIs
  can display granted channels/verbs.
- **Codegen interop** — `client.typedSubscribe<T>(name)` typed via the
  output of `parsec-gen --lang ts`.

## Install

```bash
npm install @frankbardon/parsec-client
```

`centrifuge` ships as a direct dependency — you do not need to install it
separately.

## Quickstart

```ts
import { ParsecClient } from "@frankbardon/parsec-client";

const client = new ParsecClient({
  endpoint: "https://parsec.example.com",
  accessToken: "eyJhbGciOi...",
  refreshToken: "eyJhbGciOi...",
});

await client.connect();

const sub = client.newSubscription("public:webapp.lobby.global");
sub.on("publication", ({ data }) => console.log(data));
await sub.subscribe();
```

## Typed subscriptions (codegen interop)

```ts
import { sessionsChannel, type SessionsCursor } from "./gen/index";

const sub = client.typedSubscribe<SessionsCursor>(sessionsChannel(id));
sub.on("publication", ({ data }) => {
  // data: SessionsCursor
});
await sub.subscribe();
```

## Scope inspection

```ts
import { inspectScopes } from "@frankbardon/parsec-client/scopes";

const decoded = inspectScopes(accessToken);
// decoded.chs, decoded.scopes, decoded.exp, decoded.sub
// NOTE: This decodes only. Never use it to authorize anything client-side.
```

## Versioning

This npm package versions independently from the Go server. Each release
declares a minimum Parsec server version. Pinning the npm package does
not pin the server; upgrade both together when a server release bumps
`descriptor.FormatVersion`.

## License

Apache-2.0
