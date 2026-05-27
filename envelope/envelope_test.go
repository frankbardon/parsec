package envelope

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func newEnv(t *testing.T) Envelope {
	t.Helper()
	return Envelope{
		Channel:    "public:app.domain.id.topic",
		Sequence:   1,
		ProducedAt: time.Date(2026, 5, 27, 14, 22, 9, 331_000_000, time.UTC),
		Producer:   Producer{ID: "u-1", Kind: ProducerUser},
		Aspect:     "data",
		SchemaRef:  "sessions:s_19:envelope/v1",
		Payload:    json.RawMessage(`{"hello":"world"}`),
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	in := newEnv(t)
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"produced_at":"2026-05-27T14:22:09.331Z"`) {
		t.Fatalf("produced_at not ms-precision: %s", b)
	}
	var out Envelope
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.ProducedAt.Equal(in.ProducedAt) {
		t.Fatalf("time mismatch: got %v want %v", out.ProducedAt, in.ProducedAt)
	}
	if out.Aspect != in.Aspect || out.Channel != in.Channel || out.Sequence != in.Sequence {
		t.Fatalf("envelope mismatch: %+v vs %+v", out, in)
	}
}

func TestEnvelopeValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Envelope)
		err  error
	}{
		{"channel", func(e *Envelope) { e.Channel = "" }, ErrChannelRequired},
		{"aspect", func(e *Envelope) { e.Aspect = "" }, ErrAspectRequired},
		{"producer id", func(e *Envelope) { e.Producer.ID = "" }, ErrProducerRequired},
		{"producer kind", func(e *Envelope) { e.Producer.Kind = "" }, ErrProducerKindUnknown},
		{"sequence", func(e *Envelope) { e.Sequence = 0 }, ErrSequenceNonPositive},
		{"produced_at", func(e *Envelope) { e.ProducedAt = time.Time{} }, ErrProducedAtZero},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			c.mut(&e)
			if err := e.Validate(); err != c.err {
				t.Fatalf("want %v got %v", c.err, err)
			}
		})
	}
	e := newEnv(t)
	if err := e.Validate(); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}
}

func TestEnvelopeEncodeSizeLimit(t *testing.T) {
	e := newEnv(t)
	big := make([]byte, MaxEnvelopeSize+1024)
	for i := range big {
		big[i] = 'a'
	}
	e.Payload = json.RawMessage(`"` + string(big) + `"`)
	_, err := e.Encode()
	if err == nil {
		t.Fatal("expected size error")
	}
	if !strings.Contains(err.Error(), "exceeds MaxEnvelopeSize") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestSequenceTracker(t *testing.T) {
	tr := NewSequenceTracker()
	if got := tr.Next("a"); got != 1 {
		t.Fatalf("first next: got %d want 1", got)
	}
	if got := tr.Next("a"); got != 2 {
		t.Fatalf("second next: got %d want 2", got)
	}
	if got := tr.Next("b"); got != 1 {
		t.Fatalf("b next: got %d want 1", got)
	}
	if got := tr.Peek("a"); got != 2 {
		t.Fatalf("peek: got %d want 2", got)
	}
	snap := tr.Snapshot()
	if snap["a"] != 2 || snap["b"] != 1 {
		t.Fatalf("snapshot: %v", snap)
	}
	tr2 := NewSequenceTracker()
	tr2.Restore(snap)
	if got := tr2.Next("a"); got != 3 {
		t.Fatalf("restored next: got %d want 3", got)
	}
}

func TestSequenceTrackerConcurrent(t *testing.T) {
	tr := NewSequenceTracker()
	const N = 1000
	var wg sync.WaitGroup
	wg.Add(N)
	seen := sync.Map{}
	for range N {
		go func() {
			defer wg.Done()
			s := tr.Next("c")
			if _, dup := seen.LoadOrStore(s, true); dup {
				t.Errorf("duplicate sequence %d", s)
			}
		}()
	}
	wg.Wait()
	if tr.Peek("c") != N {
		t.Fatalf("final peek: %d", tr.Peek("c"))
	}
}

func TestGapDetector(t *testing.T) {
	d := NewGapDetector()
	r := d.Observe("ch", "p1", 5)
	if !r.First {
		t.Fatal("first observation not flagged First")
	}
	r = d.Observe("ch", "p1", 6)
	if r.Gap != 0 || r.Duplicate || r.First {
		t.Fatalf("in-order: %+v", r)
	}
	r = d.Observe("ch", "p1", 9)
	if r.Gap != 2 {
		t.Fatalf("gap: got %d want 2", r.Gap)
	}
	r = d.Observe("ch", "p1", 9)
	if !r.Duplicate {
		t.Fatal("expected duplicate")
	}
	r = d.Observe("ch", "p2", 1)
	if !r.First {
		t.Fatal("second producer should be First")
	}
	if d.HighWater("ch", "p1") != 9 {
		t.Fatalf("highwater p1: %d", d.HighWater("ch", "p1"))
	}
}

func TestDataRefShape(t *testing.T) {
	p := DataRefPayload{URL: "https://x", SizeBytes: 100, ContentHash: "abc"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"url":"https://x"`) {
		t.Fatalf("bad shape: %s", b)
	}
}
