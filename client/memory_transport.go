package client

import (
	"context"
	"errors"
	"sync"
)

// MemoryTransport is an in-process Transport useful for tests, examples,
// and same-process publisher/subscriber wiring. Publish hands the bytes
// to every subscriber on the same channel; history is unbounded; presence
// is empty.
//
// MemoryTransport is NOT a production transport — it does not survive a
// process restart, has no auth, and broadcasts inside one process. Use
// it for envelope-level tests; swap in a WebSocketTransport in
// production.
type MemoryTransport struct {
	mu     sync.Mutex
	subs   map[string]map[int]chan []byte
	nextID int
	hist   map[string][][]byte
}

// NewMemoryTransport constructs an empty transport.
func NewMemoryTransport() *MemoryTransport {
	return &MemoryTransport{
		subs: map[string]map[int]chan []byte{},
		hist: map[string][][]byte{},
	}
}

// Connect is a no-op.
func (m *MemoryTransport) Connect(context.Context, string) error { return nil }

// Close cancels every active subscription.
func (m *MemoryTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, chs := range m.subs {
		for id, c := range chs {
			close(c)
			delete(chs, id)
		}
	}
	return nil
}

// Publish broadcasts payload to every subscriber on channel.
func (m *MemoryTransport) Publish(_ context.Context, channel string, payload []byte) error {
	m.mu.Lock()
	m.hist[channel] = append(m.hist[channel], payload)
	subs := m.subs[channel]
	pending := make([]chan []byte, 0, len(subs))
	for _, c := range subs {
		pending = append(pending, c)
	}
	m.mu.Unlock()
	for _, c := range pending {
		select {
		case c <- payload:
		default:
			// drop on slow subscriber
		}
	}
	return nil
}

// Subscribe opens a stream of bytes. FromSequence replays history above
// the given sequence number by decoding+filtering — useful for resume
// after disconnect.
func (m *MemoryTransport) Subscribe(_ context.Context, channel string, opts SubscribeOptions) (<-chan []byte, func(), error) {
	m.mu.Lock()
	if _, ok := m.subs[channel]; !ok {
		m.subs[channel] = map[int]chan []byte{}
	}
	id := m.nextID
	m.nextID++
	out := make(chan []byte, 64)
	m.subs[channel][id] = out
	// Replay history above FromSequence — bytes are envelope JSON, so
	// the test can supply opts.FromSequence to drive a resume path.
	if opts.FromSequence > 0 {
		for _, raw := range m.hist[channel] {
			if seq := peekSequence(raw); seq > opts.FromSequence {
				select {
				case out <- raw:
				default:
				}
			}
		}
	}
	m.mu.Unlock()
	cancel := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if chs, ok := m.subs[channel]; ok {
			if c, ok := chs[id]; ok {
				delete(chs, id)
				close(c)
			}
		}
	}
	return out, cancel, nil
}

// History returns every envelope ever published to channel, optionally
// bounded by opts.Since (exclusive) and opts.Limit.
func (m *MemoryTransport) History(_ context.Context, channel string, opts HistoryOptions) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	raws := m.hist[channel]
	out := make([][]byte, 0, len(raws))
	for _, r := range raws {
		seq := peekSequence(r)
		if opts.Since > 0 && seq <= opts.Since {
			continue
		}
		if opts.Until > 0 && seq > opts.Until {
			continue
		}
		out = append(out, r)
	}
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[len(out)-opts.Limit:]
	}
	return out, nil
}

// Presence returns the count of subscribers as a single anonymous entry.
// Memory transport has no auth and therefore no user identities to report.
func (m *MemoryTransport) Presence(_ context.Context, channel string) ([]PresenceEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PresenceEntry, 0, len(m.subs[channel]))
	for id := range m.subs[channel] {
		out = append(out, PresenceEntry{UserID: "anonymous", ClientID: itoa(id)})
	}
	return out, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	buf := [16]byte{}
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// peekSequence does a cheap scan for `"sequence":` in raw JSON. Returns
// 0 if not found. Avoids a full json.Unmarshal hot-path cost on each
// history walk.
func peekSequence(raw []byte) int64 {
	key := []byte(`"sequence":`)
	i := indexOf(raw, key)
	if i < 0 {
		return 0
	}
	j := i + len(key)
	for j < len(raw) && (raw[j] == ' ' || raw[j] == '\t') {
		j++
	}
	var n int64
	for j < len(raw) && raw[j] >= '0' && raw[j] <= '9' {
		n = n*10 + int64(raw[j]-'0')
		j++
	}
	return n
}

func indexOf(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
outer:
	for i := 0; i+len(needle) <= len(haystack); i++ {
		for k := range needle {
			if haystack[i+k] != needle[k] {
				continue outer
			}
		}
		return i
	}
	return -1
}

// ErrNotImplemented is returned by transports whose feature surface is
// incomplete (e.g. a publish-only transport's Subscribe).
var ErrNotImplemented = errors.New("transport: not implemented")
