package service

import (
	"context"
	"time"

	"github.com/frankbardon/parsec/auth"
)

// KeySummary is the surface-shape of an auth.Key.
type KeySummary struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
	RetiredAt string `json:"retired_at,omitempty"`
}

// KeyListing is the result of ListKeys.
type KeyListing struct {
	ActiveKeyID string       `json:"active_key_id"`
	Keys        []KeySummary `json:"keys"`
}

// ListKeys returns every key currently in the ring along with the active id.
func (s *Service) ListKeys(ctx context.Context) KeyListing {
	ring := s.p.KeyRing()
	out := KeyListing{ActiveKeyID: ring.ActiveID()}
	for _, k := range ring.List() {
		out.Keys = append(out.Keys, keyToSummary(k))
	}
	return out
}

// GenerateKey adds a new key to the ring as verify-only and returns it.
func (s *Service) GenerateKey(ctx context.Context) (KeySummary, error) {
	k, err := s.p.GenerateKey()
	if err != nil {
		return KeySummary{}, err
	}
	return keyToSummary(k), nil
}

// PromoteKey makes id the active signing key.
func (s *Service) PromoteKey(ctx context.Context, id string) error {
	return s.p.PromoteKey(id)
}

// RetireKey removes id from verification.
func (s *Service) RetireKey(ctx context.Context, id string) error {
	return s.p.RetireKey(id)
}

// ReloadKeys re-reads the keyring file (no-op for ephemeral / programmatic
// rings — see parsec.Parsec.ReloadKeys for the error path).
func (s *Service) ReloadKeys(ctx context.Context) error {
	return s.p.ReloadKeys()
}

// MgmtTokenResult is what IssueMgmt returns.
type MgmtTokenResult struct {
	Token         string    `json:"token"`
	Expires       time.Time `json:"expires"`
	SignedByKeyID string    `json:"signed_by_key_id"`
}

// IssueMgmt mints a fresh mgmt token signed by the active key. Used by
// operators during rotation to get a bearer signed by the new key before
// retiring the old one.
func (s *Service) IssueMgmt(ctx context.Context, subject string, ttl time.Duration) (MgmtTokenResult, error) {
	tok, exp, err := s.p.Issuer().IssueMgmt(subject, ttl)
	if err != nil {
		return MgmtTokenResult{}, err
	}
	return MgmtTokenResult{
		Token:         tok,
		Expires:       exp,
		SignedByKeyID: s.p.KeyRing().ActiveID(),
	}, nil
}

func keyToSummary(k auth.Key) KeySummary {
	out := KeySummary{
		ID:        k.ID,
		Role:      string(k.Role),
		CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if k.RetiredAt != nil {
		out.RetiredAt = k.RetiredAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}
