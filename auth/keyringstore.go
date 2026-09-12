package auth

import (
	"context"
	"errors"
	"os"
)

// KeyRingStore is the persistence interface for the KeyRing. Two
// implementations ship in this module: a file-backed store (single-node)
// and a Redis-backed store that lets multiple Parsec nodes share the same
// keys and observe rotation events without manual SIGHUPs. A third lives
// in the stores/gcpsecretmanager submodule, which keeps the Google Cloud
// SDK out of this module's dependency graph; embedders plug it in via
// parsec.Options.KeyRingStore.
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

// KeyRingVersionUnknown is the revision a store reports before it has
// read one. Surfaced on the parsec_keyring_version gauge as -1, which is
// how "this node cannot tell you which revision it serves" is
// distinguished from revision 0.
const KeyRingVersionUnknown = versionUnknown

// KeyRingVersioner is the optional half of KeyRingStore that a store
// shared across nodes implements: the revision of the snapshot this node
// last loaded or saved. Nodes serving the same ring report the same
// number, so a divergence is visible on parsec_keyring_version without
// exporting key ids as labels.
//
// Stores with no shared revision (file, ephemeral) simply do not
// implement it.
type KeyRingVersioner interface {
	Version() int64
}

// KeyRingErrorReporter is the optional hook for non-fatal Watch errors —
// a failed reconcile read, a dropped subscription. Watch keeps running
// either way; the hook is how those get counted
// (parsec_keyring_reconcile_errors_total) instead of vanishing.
type KeyRingErrorReporter interface {
	SetErrorHook(func(error))
}

// KeyRingBootstrapper is the optional bootstrap half of KeyRingStore. A
// store shared across nodes implements it so that minting the first ring
// is a compare-and-set against "still empty" in the backend's own terms,
// rather than the read-then-write that EnsureKeyRingStore falls back to.
type KeyRingBootstrapper interface {
	Ensure(ctx context.Context) (*KeyRing, bool, error)
}

// EnsureKeyRingStore returns the ring held by store, generating and
// persisting a fresh one when the store is empty. The bool reports
// whether bootstrap happened.
//
// A store implementing KeyRingBootstrapper is asked to do it itself: only
// the store knows how to make the initial write conditional on the
// backend still being empty. For the rest this is load-or-create, with
// one concession to racing nodes — an ErrKeyRingConflict on the bootstrap
// write means somebody else got there first, so the ring they wrote is
// adopted instead of overwritten. Both nodes must sign with the same
// keys.
func EnsureKeyRingStore(ctx context.Context, store KeyRingStore) (*KeyRing, bool, error) {
	if b, ok := store.(KeyRingBootstrapper); ok {
		return b.Ensure(ctx)
	}
	r, err := store.Load(ctx)
	if err == nil {
		return r, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	r = NewKeyRing()
	if _, err := r.Generate(); err != nil {
		return nil, false, err
	}
	if err := store.Save(ctx, r); err != nil {
		if !errors.Is(err, ErrKeyRingConflict) {
			return nil, false, err
		}
		r, err = store.Load(ctx)
		if err != nil {
			return nil, false, err
		}
		return r, false, nil
	}
	return r, true, nil
}
