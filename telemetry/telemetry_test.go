package telemetry

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeSource struct{ snap Snapshot }

func (f fakeSource) Channels(context.Context) ChannelStats   { return f.snap.Channels }
func (f fakeSource) Envelopes(context.Context) EnvelopeStats { return f.snap.Envelopes }
func (f fakeSource) Presence(context.Context) PresenceStats  { return f.snap.Presence }
func (f fakeSource) History(context.Context) HistoryStats    { return f.snap.History }
func (f fakeSource) Tokens(context.Context) TokenStats       { return f.snap.Tokens }
func (f fakeSource) Cache(context.Context) CacheStats        { return f.snap.Cache }

func TestAggregatorSum(t *testing.T) {
	s1 := fakeSource{Snapshot{Channels: ChannelStats{TotalActive: 10, ByPattern: map[string]int64{"a": 10}}}}
	s2 := fakeSource{Snapshot{Channels: ChannelStats{TotalActive: 5, ByPattern: map[string]int64{"a": 5, "b": 1}}}}
	agg := New(s1, s2)
	snap := agg.Snapshot(context.Background())
	if snap.Channels.TotalActive != 15 {
		t.Fatalf("total: %d", snap.Channels.TotalActive)
	}
	if snap.Channels.ByPattern["a"] != 15 || snap.Channels.ByPattern["b"] != 1 {
		t.Fatalf("by pattern: %v", snap.Channels.ByPattern)
	}
}

func TestHandlerJSON(t *testing.T) {
	agg := New(fakeSource{Snapshot{
		Tokens: TokenStats{IssuedLastHour: 42},
	}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/parsec/metrics", nil)
	agg.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"issued_last_hour":42`) {
		t.Fatalf("body: %s", rec.Body.String())
	}
	var out Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
}

type fakeLister struct{ entries []ChannelEntry }

func (f fakeLister) List() []ChannelEntry { return f.entries }

func TestChannelSource(t *testing.T) {
	cs := ChannelSource{Lister: fakeLister{entries: []ChannelEntry{
		{Name: "a", Pattern: "p1"},
		{Name: "b", Pattern: "p1"},
		{Name: "c", Pattern: "p2"},
	}}}
	c := cs.Channels(context.Background())
	if c.TotalActive != 3 || c.ByPattern["p1"] != 2 || c.ByPattern["p2"] != 1 {
		t.Fatalf("channels: %+v", c)
	}
}

func TestTokenCountersHourWindow(t *testing.T) {
	c := NewTokenCounters()
	now := time.Now()
	for range 5 {
		c.Issued(now)
	}
	c.Revoked(now)
	snap := c.Snapshot()
	if snap.IssuedLastHour != 5 {
		t.Fatalf("issued: %d", snap.IssuedLastHour)
	}
	if snap.RevokedLastHour != 1 {
		t.Fatalf("revoked: %d", snap.RevokedLastHour)
	}
	if snap.ActiveCount != 4 {
		t.Fatalf("active: %d", snap.ActiveCount)
	}
}

func TestEnvelopeCounters(t *testing.T) {
	e := NewEnvelopeCounters()
	now := time.Now()
	for range 30 {
		e.Observe(now, "data")
	}
	e.Observe(now, "cursor")
	snap := e.Snapshot()
	if snap.ByAspect["data"] != 30 {
		t.Fatalf("by aspect: %v", snap.ByAspect)
	}
	if snap.RatePerSecond <= 0 {
		t.Fatalf("rate: %v", snap.RatePerSecond)
	}
}

func TestCacheSourceHitRate(t *testing.T) {
	s := NewCacheSource(func() cacheStats {
		return cacheStats{Hits: 80, Misses: 20, SizeEntries: 100}
	})
	cs := s.Cache(context.Background())
	if cs.HitRatePct != 80 {
		t.Fatalf("hit rate: %v", cs.HitRatePct)
	}
	if cs.SizeEntries != 100 {
		t.Fatalf("size: %d", cs.SizeEntries)
	}
}
