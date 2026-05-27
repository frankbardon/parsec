package channels

import (
	"sync"
	"time"

	"github.com/frankbardon/parsec/errors"
)

// MemoryStore is the in-process Store. Single-node Parsec deployments use
// it by default; multi-node deployments swap it for a RedisStore.
type MemoryStore struct {
	mu       sync.RWMutex
	channels map[string]*Channel
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{channels: make(map[string]*Channel)}
}

// Get returns a snapshot or ChannelNotFound.
func (s *MemoryStore) Get(name Name) (*Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.channels[name.String()]
	if !ok || ch.State == StateDeleted {
		return nil, errors.New(errors.ChannelNotFound, "no such channel")
	}
	cp := *ch
	return &cp, nil
}

// List returns a snapshot.
func (s *MemoryStore) List() ([]Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Channel, 0, len(s.channels))
	for _, ch := range s.channels {
		out = append(out, *ch)
	}
	return out, nil
}

// IsOpen reports whether the channel is in StateOpen.
func (s *MemoryStore) IsOpen(name Name) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.channels[name.String()]
	if !ok {
		return false
	}
	return ch.State == StateOpen
}

// OpenPublic creates or re-opens a public channel.
func (s *MemoryStore) OpenPublic(name Name, ttl time.Duration, now time.Time) (*Channel, Event, error) {
	if name.Visibility != VisibilityPublic {
		return nil, Event{}, errors.New(errors.ChannelInvalid, "OpenPublic requires a public channel name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := name.String()
	if existing, ok := s.channels[key]; ok {
		if existing.State == StateDeleted {
			return nil, Event{}, errors.New(errors.ChannelNotFound, "channel was deleted; create a new one")
		}
		wasClosed := existing.State == StateClosed
		existing.State = StateOpen
		existing.LastActive = now
		existing.TTL = ttl
		out := *existing
		var ev Event
		if wasClosed {
			ev = Event{Kind: EventOpened, Name: name, At: now}
		}
		return &out, ev, nil
	}
	ch := &Channel{Name: name, State: StateOpen, TTL: ttl, CreatedAt: now, LastActive: now}
	s.channels[key] = ch
	out := *ch
	return &out, Event{Kind: EventOpened, Name: name, At: now}, nil
}

// CreatePrivate registers a new private channel.
func (s *MemoryStore) CreatePrivate(name Name, ttl time.Duration, now time.Time) (*Channel, Event, error) {
	if name.Visibility != VisibilityPrivate {
		return nil, Event{}, errors.New(errors.ChannelInvalid, "CreatePrivate requires a private channel name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := name.String()
	if _, ok := s.channels[key]; ok {
		return nil, Event{}, errors.New(errors.ChannelExists, "channel already exists")
	}
	ch := &Channel{Name: name, State: StateOpen, TTL: ttl, CreatedAt: now, LastActive: now}
	s.channels[key] = ch
	out := *ch
	return &out, Event{Kind: EventOpened, Name: name, At: now}, nil
}

// Touch refreshes LastActive.
func (s *MemoryStore) Touch(name Name, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.channels[name.String()]
	if !ok {
		return errors.New(errors.ChannelNotFound, "no such channel")
	}
	if ch.State == StateDeleted {
		return errors.New(errors.ChannelNotFound, "channel deleted")
	}
	if ch.State == StateClosed {
		return errors.New(errors.ChannelClosed, "channel closed; reopen first")
	}
	ch.LastActive = now
	return nil
}

// Delete marks the channel deleted.
func (s *MemoryStore) Delete(name Name, now time.Time) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.channels[name.String()]
	if !ok {
		return Event{}, errors.New(errors.ChannelNotFound, "no such channel")
	}
	alreadyDeleted := ch.State == StateDeleted
	ch.State = StateDeleted
	if alreadyDeleted {
		return Event{}, nil
	}
	return Event{Kind: EventDeleted, Name: name, At: now}, nil
}

// SetDelta toggles fossil-delta encoding on the channel.
func (s *MemoryStore) SetDelta(name Name, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.channels[name.String()]
	if !ok || ch.State == StateDeleted {
		return errors.New(errors.ChannelNotFound, "no such channel")
	}
	ch.DeltaEnabled = enabled
	return nil
}

// Sweep runs one expiry pass.
func (s *MemoryStore) Sweep(now time.Time) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var events []Event
	for key, ch := range s.channels {
		if ch.State != StateOpen {
			continue
		}
		if now.Sub(ch.LastActive) < ch.TTL {
			continue
		}
		if ch.Name.IsPrivate() {
			delete(s.channels, key)
			events = append(events, Event{Kind: EventDeleted, Name: ch.Name, At: now})
		} else {
			ch.State = StateClosed
			events = append(events, Event{Kind: EventClosed, Name: ch.Name, At: now})
		}
	}
	return events, nil
}
