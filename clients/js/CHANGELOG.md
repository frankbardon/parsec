# Changelog

## 0.1.0 - unreleased

Initial release.

- `ParsecClient` composition wrapper over `centrifuge` v5.6.x.
- Token refresh via Twirp `RefreshToken`, with single-flight de-duplication,
  preemptive timer, and terminal/recoverable failure policy.
- `parseName` / `buildName` channel-grammar port (parity gate against
  `channels/name.go`).
- `fetchManifest` + `pickTransports` driven by the server's
  `/manifest` response.
- `ParsecError` + `ParsecErrorCode` mirroring `errors/codes.go`; Twirp
  JSON → coded error mapping mirrors `internal/rpcclient/errors.go`.
- `inspectScopes` + `decodeJwtPayload` — decode-only JWT introspection
  for UI display. Does NOT verify.
- `MemoryTokenStore`; pluggable `TokenStore` interface for cookie /
  IndexedDB adapters in later releases.
- `typedSubscribe<T>` ergonomic for use with `parsec-gen --lang ts`
  output.
