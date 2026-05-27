package channels

import (
	"context"
	"sync"
	"time"

	"github.com/frankbardon/parsec/errors"
)

// MaxPrivateTTL is the hard cap on private channel inactivity TTL. Anything
// larger is rejected at creation time.
const MaxPrivateTTL = 1 * time.Hour

// DefaultPublicTTL is applied when the caller does not specify a TTL on
// channel creation for the public namespace.
const DefaultPublicTTL = 30 * time.Minute

// State describes the lifecycle position of a managed channel.
type State string

const (
	StateOpen    State = "open"
	StateClosed  State = "closed" // public only — inactive past TTL, can reopen
	StateDeleted State = "deleted"
)

// Channel is the persisted view of a managed channel. Token material is
// never stored here — the auth package owns secrets; the manager owns
// channel lifecycle.
type Channel struct {
	Name       Name
	State      State
	TTL        time.Duration
	CreatedAt  time.Time
	LastActive time.Time
	// DeltaEnabled toggles centrifuge fossil-delta encoding for this
	// channel. Off by default; enable for high-frequency channels with
	// small mutations (scoreboards, tickers, partial state snapshots).
	DeltaEnabled bool
}

// Manager owns channel lifecycle, TTL enforcement, and the inactivity tick.
// State persistence is delegated to a Store; event fanout to subscribers
// stays in process. Safe for concurrent use.
type Manager struct {
	store Store
	clock func() time.Time

	clockMu sync.RWMutex // protects clock pointer

	subsMu sync.RWMutex
	subs   []chan<- Event

	bus EventBus // optional cross-node bridge (nil for single-node)

	// onSweep, when non-nil, is invoked after every Sweep tick with the
	// current channel list. Used by the metrics layer to publish a
	// gauge of {state, visibility}-grouped counts without leaking
	// channel names.
	onSweepMu sync.RWMutex
	onSweep   func([]Channel)
}

// NewManager constructs a Manager backed by an in-memory store. Equivalent
// to NewManagerWithStore(NewMemoryStore()).
func NewManager() *Manager {
	return NewManagerWithStore(NewMemoryStore())
}

// NewManagerWithStore constructs a Manager backed by the supplied Store.
func NewManagerWithStore(s Store) *Manager {
	return &Manager{store: s, clock: time.Now}
}

// SetEventBus wires a cross-node event bus. When non-nil, every locally
// emitted event is also published to the bus, and remote events are
// fanned out to local subscribers. Used to make manager state coherent
// across multi-node deployments.
func (m *Manager) SetEventBus(b EventBus) { m.bus = b }

// SetOnSweep registers a callback invoked after every Sweep tick with
// the current channel list. Used by the metrics layer to publish gauges
// without leaking channel names. Pass nil to remove.
func (m *Manager) SetOnSweep(cb func([]Channel)) {
	m.onSweepMu.Lock()
	defer m.onSweepMu.Unlock()
	m.onSweep = cb
}

// fireSweepCallback fans the current List() to a registered onSweep
// callback. Cheap when no callback is set.
func (m *Manager) fireSweepCallback() {
	m.onSweepMu.RLock()
	cb := m.onSweep
	m.onSweepMu.RUnlock()
	if cb == nil {
		return
	}
	cb(m.List())
}

// EventBus returns the configured cross-node bus, or nil.
func (m *Manager) EventBus() EventBus { return m.bus }

// Subscribe registers a consumer for lifecycle events. The caller owns the
// channel and must drain it; the manager does a non-blocking send and drops
// events on full buffers. Use a buffered channel (cap >= 16) for production.
func (m *Manager) Subscribe(ch chan<- Event) {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	m.subs = append(m.subs, ch)
}

// emitLocal fans an event out to local subscribers only.
func (m *Manager) emitLocal(ev Event) {
	if ev.Kind == "" {
		return
	}
	m.subsMu.RLock()
	defer m.subsMu.RUnlock()
	for _, ch := range m.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// emit fans an event to local subscribers AND publishes to the cross-node
// bus when configured.
func (m *Manager) emit(ev Event) {
	if ev.Kind == "" {
		return
	}
	m.emitLocal(ev)
	if m.bus != nil {
		_ = m.bus.Publish(context.Background(), ev)
	}
}

// SetClock overrides the time source. Used in tests.
func (m *Manager) SetClock(c func() time.Time) {
	m.clockMu.Lock()
	defer m.clockMu.Unlock()
	m.clock = c
}

func (m *Manager) now() time.Time {
	m.clockMu.RLock()
	defer m.clockMu.RUnlock()
	return m.clock()
}

// OpenPublic creates or re-opens a public channel. Pass 0 for DefaultPublicTTL.
func (m *Manager) OpenPublic(name Name, ttl time.Duration) (*Channel, error) {
	if ttl == 0 {
		ttl = DefaultPublicTTL
	}
	ch, ev, err := m.store.OpenPublic(name, ttl, m.now())
	if err != nil {
		return nil, err
	}
	m.emit(ev)
	return ch, nil
}

// CreatePrivate registers a new private channel. TTL is capped at MaxPrivateTTL.
func (m *Manager) CreatePrivate(name Name, ttl time.Duration) (*Channel, error) {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if ttl > MaxPrivateTTL {
		return nil, errors.New(errors.ChannelTTLExceeds, "private channel TTL must be <= 1h")
	}
	ch, ev, err := m.store.CreatePrivate(name, ttl, m.now())
	if err != nil {
		return nil, err
	}
	m.emit(ev)
	return ch, nil
}

// Touch refreshes the channel's LastActive.
func (m *Manager) Touch(name Name) error {
	return m.store.Touch(name, m.now())
}

// Delete removes the channel. Idempotent on already-deleted channels.
func (m *Manager) Delete(name Name) error {
	ev, err := m.store.Delete(name, m.now())
	if err != nil {
		return err
	}
	m.emit(ev)
	return nil
}

// IsOpen reports whether the channel exists and is in StateOpen.
func (m *Manager) IsOpen(name Name) bool { return m.store.IsOpen(name) }

// SetDelta toggles fossil-delta encoding for the channel. Off by default.
// Enable for high-frequency channels where small mutations dominate the
// publish stream.
func (m *Manager) SetDelta(name Name, enabled bool) error {
	return m.store.SetDelta(name, enabled)
}

// IsDelta reports whether the channel has delta encoding enabled.
func (m *Manager) IsDelta(name Name) bool {
	ch, err := m.store.Get(name)
	if err != nil {
		return false
	}
	return ch.DeltaEnabled
}

// Get returns a snapshot.
func (m *Manager) Get(name Name) (*Channel, error) { return m.store.Get(name) }

// List returns a snapshot of all managed channels.
func (m *Manager) List() []Channel {
	out, err := m.store.List()
	if err != nil {
		return nil
	}
	return out
}

// Sweep runs one expiry pass and returns the count of transitions.
func (m *Manager) Sweep(now time.Time) int {
	events, err := m.store.Sweep(now)
	if err != nil {
		return 0
	}
	for _, ev := range events {
		m.emit(ev)
	}
	m.fireSweepCallback()
	return len(events)
}

// RunSweeper drives Sweep on every tick until ctx is done.
func (m *Manager) RunSweeper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.Sweep(now)
		}
	}
}

// RunEventBus subscribes to the cross-node bus and re-emits remote events
// to local subscribers. Returns when ctx is canceled or the bus exits.
// No-op when no bus is configured.
func (m *Manager) RunEventBus(ctx context.Context) error {
	if m.bus == nil {
		<-ctx.Done()
		return nil
	}
	return m.bus.Run(ctx, m.emitLocal)
}

