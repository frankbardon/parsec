# Errors — Parsec

Load when adding/changing error codes or crossing a package boundary
with a new failure mode.

## Coded errors

All errors that cross a package boundary are typed `*errors.Error` with
a `PARSEC_DOMAIN_CATEGORY` code.

## Mapping points

- Server side (PARSEC_* → twirp codes): `service/twirp_errors.go:toTwirpError`
- Client side (twirp codes → PARSEC_*): `internal/rpcclient/errors.go:mapErr`
- CLI surfaces the code via the descriptor envelope.

## JS client mirror

Coded errors in `clients/js/src/errors.ts` mirror `errors/codes.go`
one-for-one. Twirp JSON → coded error mapping mirrors
`internal/rpcclient/errors.go`. Adding a code requires a row in
`clients/js/test/errors.test.ts`.
