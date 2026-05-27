package auth

import (
	"context"
	"errors"
	"os"
	"time"
)

// FileKeyRingStore reads and writes the KeyRing as a JSON file at
// <state-dir>/keyring.json. Single-node deployments use this; multi-node
// deployments swap for RedisKeyRingStore.
type FileKeyRingStore struct {
	path     string
	pollEvery time.Duration
}

// NewFileKeyRingStore constructs a store at path. pollEvery controls how
// frequently Watch checks the file's mtime. Pass 0 to disable polling
// (Watch becomes a no-op).
func NewFileKeyRingStore(path string, pollEvery time.Duration) *FileKeyRingStore {
	return &FileKeyRingStore{path: path, pollEvery: pollEvery}
}

// Path returns the on-disk path.
func (s *FileKeyRingStore) Path() string { return s.path }

// Load reads the file.
func (s *FileKeyRingStore) Load(_ context.Context) (*KeyRing, error) {
	return LoadKeyRing(s.path)
}

// Save writes the ring atomically.
func (s *FileKeyRingStore) Save(_ context.Context, r *KeyRing) error {
	return SaveKeyRing(s.path, r)
}

// Watch polls the file's mtime and re-loads on change.
func (s *FileKeyRingStore) Watch(ctx context.Context, onChange func(*KeyRing)) error {
	if s.pollEvery <= 0 {
		<-ctx.Done()
		return nil
	}
	t := time.NewTicker(s.pollEvery)
	defer t.Stop()
	var lastMod time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			info, err := os.Stat(s.path)
			if err != nil {
				continue
			}
			if !lastMod.IsZero() && !info.ModTime().After(lastMod) {
				continue
			}
			lastMod = info.ModTime()
			r, err := LoadKeyRing(s.path)
			if err != nil {
				continue
			}
			onChange(r)
		}
	}
}

// Ensure satisfies the bootstrap pattern: load or generate a fresh ring,
// persist, return.
func (s *FileKeyRingStore) Ensure(ctx context.Context) (*KeyRing, bool, error) {
	r, err := s.Load(ctx)
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
	if err := s.Save(ctx, r); err != nil {
		return nil, false, err
	}
	return r, true, nil
}
