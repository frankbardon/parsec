package auth

import "context"

// KeyRingStore is the persistence interface for the KeyRing. Two
// implementations ship: a file-backed store (single-node) and a Redis-
// backed store that lets multiple Parsec nodes share the same keys and
// observe rotation events without manual SIGHUPs.
//
// A store shared across nodes carries two obligations beyond the method
// signatures, both documented below: Save must not overwrite a concurrent
// rotation (see ErrKeyRingConflict), and Watch must survive transport
// failures. See .claude/context/auth.md for the full contract.
type KeyRingStore interface {
	// Load returns the persisted ring, or (nil, os.ErrNotExist) when the
	// store is empty so the caller can decide whether to bootstrap.
	//
	// A store that versions its snapshots MUST record which revision the
	// returned ring belongs to, so the next Save can tell whether it is
	// still current. Pairing a snapshot with the wrong revision defeats
	// that check.
	Load(ctx context.Context) (*KeyRing, error)

	// Save replaces the persisted snapshot with the supplied ring's.
	//
	// The write is whole-snapshot, so a ring derived from a stale read
	// would erase any rotation that landed in between. A store shared
	// across nodes MUST therefore make Save a compare-and-set against the
	// revision it last loaded, writing nothing and returning
	// ErrKeyRingConflict on a mismatch. The caller reloads, re-applies its
	// mutation, and saves again — Parsec.mutateKeys does this.
	//
	// The same applies to bootstrap: writing an initial ring is a
	// compare-and-set against "still empty", or two nodes starting at once
	// each mint a ring and the loser signs with keys nobody else accepts.
	Save(ctx context.Context, r *KeyRing) error

	// Watch subscribes to remote modifications. onChange fires with each
	// freshly loaded ring snapshot after a remote write, and only when the
	// snapshot is actually newer than the one the caller already has.
	//
	// Implementations MUST block until ctx is canceled and recover from
	// transport failures themselves rather than returning. A Watch that
	// exits leaves its node serving keys that can never be updated again,
	// with one log line as the only symptom.
	//
	// A store shared across nodes MUST also re-read the ring periodically:
	// change notifications can be lost (Redis pub/sub is at-most-once), and
	// the periodic read is what bounds how long a node can serve a stale
	// ring. File-backed implementations poll mtime for the same reason.
	Watch(ctx context.Context, onChange func(*KeyRing)) error
}
