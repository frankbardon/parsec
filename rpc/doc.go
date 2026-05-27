// Package rpc is the parsec management RPC surface.
//
// The wire format is the Twirp v8 JSON protocol: POST to
//
//	/twirp/parsec.ParsecService/<Method>
//
// with Content-Type application/json and a JSON body matching the
// corresponding generated request message. Responses are JSON-encoded
// generated response messages, or a Twirp-shaped error envelope on
// failure.
//
// The code in this package is generated from service.proto by
//
//	make proto
//
// (requires protoc + protoc-gen-go + protoc-gen-twirp on PATH). The
// generated files — service.pb.go and service.twirp.go — are the source
// of truth for the wire format. Any change to the proto must regenerate
// both files in the same commit.
//
// Bearer enforcement (Authorization: Bearer <mgmt-token>) is layered on
// top of the generated handler by internal/server: every method except
// Manifest and RefreshToken requires a valid non-retired mgmt token.
package rpc
