// Package telemetry exposes the aggregated /parsec/metrics view that the
// upgrade spec calls for. It is the operator-facing snapshot of channel
// health, envelope rates, presence, history utilization, token issuance,
// and cache hit rates — pulled from every running Parsec component and
// rendered in one JSON response.
//
// The package is a small aggregator, not a metrics store. Each Source
// is plugged in at construction; the snapshot is computed on demand.
// For Prometheus exposition the existing internal/metrics registry stays
// the source of truth — this surface is a higher-level dashboard view.
package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

// ChannelStats is the per-pattern aggregate.
type ChannelStats struct {
	TotalActive int64            `json:"total_active"`
	ByPattern   map[string]int64 `json:"by_pattern,omitempty"`
}

// EnvelopeStats is the publish-rate aggregate.
type EnvelopeStats struct {
	RatePerSecond float64          `json:"rate_per_second"`
	ByAspect      map[string]int64 `json:"by_aspect,omitempty"`
}

// PresenceStats is the presence aggregate.
type PresenceStats struct {
	TotalUsers        int64   `json:"total_users"`
	TotalAgents       int64   `json:"total_agents"`
	AveragePerChannel float64 `json:"average_per_channel"`
}

// HistoryStats is the history-buffer aggregate.
type HistoryStats struct {
	TotalEnvelopesBuffered int64   `json:"total_envelopes_buffered"`
	BufferUtilizationPct   float64 `json:"buffer_utilization_pct"`
}

// TokenStats is the token issuance aggregate.
type TokenStats struct {
	IssuedLastHour  int64 `json:"issued_last_hour"`
	RevokedLastHour int64 `json:"revoked_last_hour"`
	ActiveCount     int64 `json:"active_count"`
}

// CacheStats is the cache aggregate.
type CacheStats struct {
	HitRatePct        float64 `json:"hit_rate_pct"`
	SizeBytes         int64   `json:"size_bytes"`
	SizeEntries       int64   `json:"size_entries,omitempty"`
	EvictionsLastHour int64   `json:"evictions_last_hour"`
}

// Snapshot is the aggregated /parsec/metrics response shape.
type Snapshot struct {
	Channels  ChannelStats  `json:"channels"`
	Envelopes EnvelopeStats `json:"envelopes"`
	Presence  PresenceStats `json:"presence"`
	History   HistoryStats  `json:"history"`
	Tokens    TokenStats    `json:"tokens"`
	Cache     CacheStats    `json:"cache"`
	At        time.Time     `json:"at"`
	// Alerts lists the AlertRule(s) that evaluated true on this snapshot.
	// Empty when no rules are configured or none fired. Populated by the
	// Aggregator after summing sources so a rule can reference the
	// aggregate (e.g. "total active > N").
	Alerts []FiringAlert `json:"alerts,omitempty"`
}

// Source is the contract every telemetry input satisfies. Each method
// is invoked on every snapshot — if a metric is unavailable, return the
// zero value (the field will simply read as 0 in the JSON).
type Source interface {
	Channels(ctx context.Context) ChannelStats
	Envelopes(ctx context.Context) EnvelopeStats
	Presence(ctx context.Context) PresenceStats
	History(ctx context.Context) HistoryStats
	Tokens(ctx context.Context) TokenStats
	Cache(ctx context.Context) CacheStats
}

// Aggregator composes Source(s) into a single Snapshot.
type Aggregator struct {
	Sources []Source
	// Rules, when non-empty, are evaluated against every Snapshot. The
	// list is validated by Aggregator.WithAlerts; assigning Rules
	// directly skips that check.
	Rules []AlertRule
}

// New constructs an Aggregator over the supplied sources.
func New(sources ...Source) *Aggregator {
	return &Aggregator{Sources: sources}
}

// WithAlerts validates rules and attaches them to the Aggregator. The
// receiver is returned so callers can chain it onto New for fluent
// construction. Returns an error if any rule fails ValidateAlertRules.
func (a *Aggregator) WithAlerts(rules []AlertRule) (*Aggregator, error) {
	if err := ValidateAlertRules(rules); err != nil {
		return nil, err
	}
	a.Rules = rules
	return a, nil
}

// Snapshot computes the current aggregate. Sources are queried in
// sequence; their stats are summed field-by-field (rates and
// utilization percentages are averaged across sources).
func (a *Aggregator) Snapshot(ctx context.Context) Snapshot {
	out := Snapshot{At: time.Now().UTC()}
	if len(a.Sources) == 0 {
		return out
	}
	rateSamples := 0
	histSamples := 0
	presSamples := 0
	cacheSamples := 0
	for _, s := range a.Sources {
		c := s.Channels(ctx)
		out.Channels.TotalActive += c.TotalActive
		if len(c.ByPattern) > 0 && out.Channels.ByPattern == nil {
			out.Channels.ByPattern = map[string]int64{}
		}
		for k, v := range c.ByPattern {
			out.Channels.ByPattern[k] += v
		}
		e := s.Envelopes(ctx)
		out.Envelopes.RatePerSecond += e.RatePerSecond
		rateSamples++
		if len(e.ByAspect) > 0 && out.Envelopes.ByAspect == nil {
			out.Envelopes.ByAspect = map[string]int64{}
		}
		for k, v := range e.ByAspect {
			out.Envelopes.ByAspect[k] += v
		}
		p := s.Presence(ctx)
		out.Presence.TotalUsers += p.TotalUsers
		out.Presence.TotalAgents += p.TotalAgents
		if p.AveragePerChannel > 0 {
			out.Presence.AveragePerChannel += p.AveragePerChannel
			presSamples++
		}
		h := s.History(ctx)
		out.History.TotalEnvelopesBuffered += h.TotalEnvelopesBuffered
		if h.BufferUtilizationPct > 0 {
			out.History.BufferUtilizationPct += h.BufferUtilizationPct
			histSamples++
		}
		tk := s.Tokens(ctx)
		out.Tokens.IssuedLastHour += tk.IssuedLastHour
		out.Tokens.RevokedLastHour += tk.RevokedLastHour
		out.Tokens.ActiveCount += tk.ActiveCount
		cs := s.Cache(ctx)
		out.Cache.SizeBytes += cs.SizeBytes
		out.Cache.SizeEntries += cs.SizeEntries
		out.Cache.EvictionsLastHour += cs.EvictionsLastHour
		if cs.HitRatePct > 0 {
			out.Cache.HitRatePct += cs.HitRatePct
			cacheSamples++
		}
	}
	if presSamples > 0 {
		out.Presence.AveragePerChannel /= float64(presSamples)
	}
	if histSamples > 0 {
		out.History.BufferUtilizationPct /= float64(histSamples)
	}
	if cacheSamples > 0 {
		out.Cache.HitRatePct /= float64(cacheSamples)
	}
	_ = rateSamples
	out.Alerts = EvaluateAlerts(out, a.Rules)
	return out
}

// Handler returns an http.Handler that serves the JSON snapshot at the
// mount path. Mount it at /parsec/metrics.
func (a *Aggregator) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := a.Snapshot(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})
}

// ---------- Channel source ----------

// ChannelLister returns the current set of channels for ChannelSource.
type ChannelLister interface {
	List() []ChannelEntry
}

// ChannelEntry is the minimal shape ChannelSource consumes.
type ChannelEntry struct {
	Name    string
	Pattern string
}

// ChannelSource is a Source that reports the channel aggregate from a
// ChannelLister. The other source methods return zero values.
type ChannelSource struct{ Lister ChannelLister }

func (c ChannelSource) Channels(_ context.Context) ChannelStats {
	if c.Lister == nil {
		return ChannelStats{}
	}
	entries := c.Lister.List()
	out := ChannelStats{TotalActive: int64(len(entries))}
	if len(entries) > 0 {
		out.ByPattern = map[string]int64{}
	}
	for _, e := range entries {
		key := e.Pattern
		if key == "" {
			key = "unknown"
		}
		out.ByPattern[key]++
	}
	return out
}

func (ChannelSource) Envelopes(context.Context) EnvelopeStats { return EnvelopeStats{} }
func (ChannelSource) Presence(context.Context) PresenceStats  { return PresenceStats{} }
func (ChannelSource) History(context.Context) HistoryStats    { return HistoryStats{} }
func (ChannelSource) Tokens(context.Context) TokenStats       { return TokenStats{} }
func (ChannelSource) Cache(context.Context) CacheStats        { return CacheStats{} }

// ---------- Cache source ----------

// CacheStatter is satisfied by both cache.MemoryCache and cache.RedisCache.
type CacheStatter interface {
	Stats() cacheStats
}

// cacheStats is the shape duplicated from cache.Stats so this package
// can stay zero-dep on the cache package. Callers wrap their cache via
// CacheSourceFor which adapts.
type cacheStats = struct {
	Hits        int64 `json:"hits"`
	Misses      int64 `json:"misses"`
	Puts        int64 `json:"puts"`
	Evictions   int64 `json:"evictions"`
	SizeEntries int64 `json:"size_entries"`
}

// CacheSource implements Source against a CacheStatter. The window for
// "evictions last hour" is approximate: it diffs the eviction counter
// against the value observed an hour ago.
type CacheSource struct {
	Stats func() cacheStats

	lastWindow atomic.Int64
	lastAt     atomic.Int64
}

// NewCacheSource constructs a CacheSource over the supplied stat func.
func NewCacheSource(stats func() cacheStats) *CacheSource {
	return &CacheSource{Stats: stats}
}

func (s *CacheSource) Channels(context.Context) ChannelStats   { return ChannelStats{} }
func (s *CacheSource) Envelopes(context.Context) EnvelopeStats { return EnvelopeStats{} }
func (s *CacheSource) Presence(context.Context) PresenceStats  { return PresenceStats{} }
func (s *CacheSource) History(context.Context) HistoryStats    { return HistoryStats{} }
func (s *CacheSource) Tokens(context.Context) TokenStats       { return TokenStats{} }

func (s *CacheSource) Cache(context.Context) CacheStats {
	if s.Stats == nil {
		return CacheStats{}
	}
	st := s.Stats()
	out := CacheStats{SizeEntries: st.SizeEntries}
	tot := st.Hits + st.Misses
	if tot > 0 {
		out.HitRatePct = float64(st.Hits) * 100.0 / float64(tot)
	}
	now := time.Now().UnixNano()
	lastAt := s.lastAt.Load()
	lastWin := s.lastWindow.Load()
	if lastAt == 0 || now-lastAt > int64(time.Hour) {
		s.lastWindow.Store(st.Evictions)
		s.lastAt.Store(now)
		out.EvictionsLastHour = 0
	} else {
		out.EvictionsLastHour = st.Evictions - lastWin
		if out.EvictionsLastHour < 0 {
			out.EvictionsLastHour = 0
		}
	}
	return out
}

// ---------- Token source ----------

// TokenCounters tracks token issuance over a sliding window. The token
// broker calls Issued / Revoked on each event; the source reads back
// hour-window aggregates.
type TokenCounters struct {
	issued  ringWindow
	revoked ringWindow
	active  atomic.Int64
}

// NewTokenCounters constructs a counter with a one-hour window.
func NewTokenCounters() *TokenCounters {
	return &TokenCounters{
		issued:  newRingWindow(60),
		revoked: newRingWindow(60),
	}
}

// Issued records one issued token at time t.
func (c *TokenCounters) Issued(t time.Time) {
	c.issued.add(t)
	c.active.Add(1)
}

// Revoked records one revoked token at time t.
func (c *TokenCounters) Revoked(t time.Time) {
	c.revoked.add(t)
	c.active.Add(-1)
}

// Expired drops one from the active set without recording a revocation.
func (c *TokenCounters) Expired() {
	c.active.Add(-1)
}

// Snapshot returns the current TokenStats.
func (c *TokenCounters) Snapshot() TokenStats {
	return TokenStats{
		IssuedLastHour:  c.issued.sum(time.Now()),
		RevokedLastHour: c.revoked.sum(time.Now()),
		ActiveCount:     c.active.Load(),
	}
}

// AsSource adapts the counter to a Source whose Tokens method returns
// the current Snapshot.
func (c *TokenCounters) AsSource() Source { return tokenSource{c: c} }

type tokenSource struct{ c *TokenCounters }

func (tokenSource) Channels(context.Context) ChannelStats   { return ChannelStats{} }
func (tokenSource) Envelopes(context.Context) EnvelopeStats { return EnvelopeStats{} }
func (tokenSource) Presence(context.Context) PresenceStats  { return PresenceStats{} }
func (tokenSource) History(context.Context) HistoryStats    { return HistoryStats{} }
func (t tokenSource) Tokens(context.Context) TokenStats     { return t.c.Snapshot() }
func (tokenSource) Cache(context.Context) CacheStats        { return CacheStats{} }

// ringWindow is a one-minute-resolution sliding sum over the most
// recent N minutes. Sufficient for hourly token counters where each
// event is small.
type ringWindow struct {
	mu    chan struct{}
	cells []int64
	stamp []int64 // unix-minute of each cell
}

func newRingWindow(n int) ringWindow {
	r := ringWindow{
		mu:    make(chan struct{}, 1),
		cells: make([]int64, n),
		stamp: make([]int64, n),
	}
	return r
}

func (r *ringWindow) lock()   { r.mu <- struct{}{} }
func (r *ringWindow) unlock() { <-r.mu }

func (r *ringWindow) add(t time.Time) {
	r.lock()
	defer r.unlock()
	min := t.Unix() / 60
	idx := int(min % int64(len(r.cells)))
	if r.stamp[idx] != min {
		r.cells[idx] = 0
		r.stamp[idx] = min
	}
	r.cells[idx]++
}

func (r *ringWindow) sum(now time.Time) int64 {
	r.lock()
	defer r.unlock()
	cutoff := now.Unix()/60 - int64(len(r.cells)) + 1
	var sum int64
	for i, c := range r.cells {
		if r.stamp[i] >= cutoff {
			sum += c
		}
	}
	return sum
}

// ---------- Envelope source ----------

// EnvelopeCounters tracks publish rate + per-aspect counts.
type EnvelopeCounters struct {
	mu      chan struct{}
	byAspect map[string]int64
	window   ringWindow
}

// NewEnvelopeCounters constructs a counter with a 60-second rate window.
func NewEnvelopeCounters() *EnvelopeCounters {
	return &EnvelopeCounters{
		mu:       make(chan struct{}, 1),
		byAspect: map[string]int64{},
		window:   newRingWindow(60),
	}
}

// Observe records one publish at time t for the named aspect.
func (e *EnvelopeCounters) Observe(t time.Time, aspect string) {
	e.window.add(t)
	e.mu <- struct{}{}
	e.byAspect[aspect]++
	<-e.mu
}

// Snapshot returns the current EnvelopeStats.
func (e *EnvelopeCounters) Snapshot() EnvelopeStats {
	rate := float64(e.window.sum(time.Now())) / 60.0
	e.mu <- struct{}{}
	by := make(map[string]int64, len(e.byAspect))
	for k, v := range e.byAspect {
		by[k] = v
	}
	<-e.mu
	return EnvelopeStats{RatePerSecond: rate, ByAspect: by}
}

// AsSource adapts the counter to a Source.
func (e *EnvelopeCounters) AsSource() Source { return envSource{e: e} }

type envSource struct{ e *EnvelopeCounters }

func (envSource) Channels(context.Context) ChannelStats     { return ChannelStats{} }
func (s envSource) Envelopes(context.Context) EnvelopeStats { return s.e.Snapshot() }
func (envSource) Presence(context.Context) PresenceStats    { return PresenceStats{} }
func (envSource) History(context.Context) HistoryStats      { return HistoryStats{} }
func (envSource) Tokens(context.Context) TokenStats         { return TokenStats{} }
func (envSource) Cache(context.Context) CacheStats          { return CacheStats{} }
