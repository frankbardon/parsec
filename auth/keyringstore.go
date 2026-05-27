package auth

import "context"

// KeyRingStore is the persistence interface for the HMAC KeyRing. Two
// implementations ship: a file-backed store (single-node) and a Redis-
// backed store that lets multiple Parsec nodes share the same keys and
// observe rotation events without manual SIGHUPs.
type KeyRingStore interface {
	// Load returns the persisted ring, or (nil, os.ErrNotExist) when the
	// store is empty so the caller can decide whether to bootstrap.
	Load(ctx context.Context) (*KeyRing, error)

	// Save replaces the persisted snapshot with the supplied ring's.
	Save(ctx context.Context, r *KeyRing) error

	// Watch subscribes to remote modifications. onChange fires with each
	// freshly loaded ring snapshot after a remote write. Blocks until ctx
	// is canceled or the underlying transport errors fatally. File-backed
	// implementations may emit periodically via mtime polling; Redis
	// implementations use pub/sub.
	Watch(ctx context.Context, onChange func(*KeyRing)) error
}
