package channels

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	perr "github.com/frankbardon/parsec/errors"
)

func formatNanos(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
}

// RedisStore persists channel records in a Redis hash so that every Parsec
// node sharing the same Redis sees the same channel registry. Sweep
// transitions run inside a Lua script for atomicity.
//
// Storage layout:
//   parsec:channels        — HASH, field = channel name, value = JSON record
//
// The store is safe for concurrent use across processes; Redis is the
// single source of truth.
type RedisStore struct {
	client    redis.UniversalClient
	keyPrefix string // default "parsec"
	timeout   time.Duration
}

// NewRedisStore constructs a RedisStore against the given client.
func NewRedisStore(client redis.UniversalClient) *RedisStore {
	return &RedisStore{client: client, keyPrefix: "parsec", timeout: 5 * time.Second}
}

// WithKeyPrefix sets a custom Redis key namespace. Useful when multiple
// Parsec deployments share a Redis instance.
func (s *RedisStore) WithKeyPrefix(p string) *RedisStore {
	if p == "" {
		p = "parsec"
	}
	s.keyPrefix = p
	return s
}

func (s *RedisStore) hashKey() string { return s.keyPrefix + ":channels" }

// channelRecord is the JSON shape stored in Redis. Visibility lives outside
// Name so we can index without re-parsing. Timestamps are encoded as
// strings of nanoseconds to survive a cjson decode/encode round-trip in
// the Lua scripts (Lua numbers are float64 and lose precision on int64).
type channelRecord struct {
	Name         string `json:"name"`
	Visibility   string `json:"visibility"`
	State        State  `json:"state"`
	TTLSeconds   int64  `json:"ttl_seconds,string"`
	CreatedAt    int64  `json:"created_at,string"`
	LastActive   int64  `json:"last_active,string"`
	DeltaEnabled bool   `json:"delta,omitempty"`
}

func fromRecord(r channelRecord) (*Channel, error) {
	n, err := ParseName(r.Name)
	if err != nil {
		return nil, err
	}
	return &Channel{
		Name:         n,
		State:        r.State,
		TTL:          time.Duration(r.TTLSeconds) * time.Second,
		CreatedAt:    time.Unix(0, r.CreatedAt),
		LastActive:   time.Unix(0, r.LastActive),
		DeltaEnabled: r.DeltaEnabled,
	}, nil
}

func (s *RedisStore) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.timeout)
}

// Get returns a snapshot or ChannelNotFound.
func (s *RedisStore) Get(name Name) (*Channel, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	raw, err := s.client.HGet(ctx, s.hashKey(), name.String()).Result()
	if errors.Is(err, redis.Nil) {
		return nil, perr.New(perr.ChannelNotFound, "no such channel")
	}
	if err != nil {
		return nil, perr.Wrap(perr.Internal, "redis HGet", err)
	}
	var rec channelRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, perr.Wrap(perr.Internal, "decode channel record", err)
	}
	if rec.State == StateDeleted {
		return nil, perr.New(perr.ChannelNotFound, "no such channel")
	}
	return fromRecord(rec)
}

// List returns every non-deleted channel.
func (s *RedisStore) List() ([]Channel, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	all, err := s.client.HGetAll(ctx, s.hashKey()).Result()
	if err != nil {
		return nil, perr.Wrap(perr.Internal, "redis HGetAll", err)
	}
	out := make([]Channel, 0, len(all))
	for _, raw := range all {
		var rec channelRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			continue
		}
		if rec.State == StateDeleted {
			continue
		}
		ch, err := fromRecord(rec)
		if err != nil {
			continue
		}
		out = append(out, *ch)
	}
	return out, nil
}

// IsOpen reports whether the channel exists and is StateOpen.
func (s *RedisStore) IsOpen(name Name) bool {
	ch, err := s.Get(name)
	if err != nil {
		return false
	}
	return ch.State == StateOpen
}

// OpenPublic creates or re-opens a public channel using a Lua script for
// atomicity.
func (s *RedisStore) OpenPublic(name Name, ttl time.Duration, now time.Time) (*Channel, Event, error) {
	if name.Visibility != VisibilityPublic {
		return nil, Event{}, perr.New(perr.ChannelInvalid, "OpenPublic requires a public channel name")
	}
	ctx, cancel := s.ctx()
	defer cancel()

	// Read existing, decide transition, write back. Use WATCH/MULTI so two
	// nodes racing for the same name don't both win.
	for range 3 {
		tx := s.client.Watch(ctx, func(tx *redis.Tx) error {
			raw, err := tx.HGet(ctx, s.hashKey(), name.String()).Result()
			var rec channelRecord
			existed := false
			if err == nil {
				if err := json.Unmarshal([]byte(raw), &rec); err != nil {
					return err
				}
				existed = true
			} else if !errors.Is(err, redis.Nil) {
				return err
			}

			if existed && rec.State == StateDeleted {
				return perr.New(perr.ChannelNotFound, "channel was deleted; create a new one")
			}

			wasClosed := existed && rec.State == StateClosed
			wasNew := !existed
			if existed {
				rec.State = StateOpen
				rec.LastActive = now.UnixNano()
				rec.TTLSeconds = int64(ttl.Seconds())
			} else {
				rec = channelRecord{
					Name:       name.String(),
					Visibility: string(name.Visibility),
					State:      StateOpen,
					TTLSeconds: int64(ttl.Seconds()),
					CreatedAt:  now.UnixNano(),
					LastActive: now.UnixNano(),
				}
			}
			payload, _ := json.Marshal(rec)
			_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
				p.HSet(ctx, s.hashKey(), name.String(), payload)
				return nil
			})
			if err != nil {
				return err
			}
			// Decorate err with a sentinel for the outer logic.
			if wasNew || wasClosed {
				return errOpenedEvent{}
			}
			return nil
		}, s.hashKey())

		switch {
		case tx == nil:
			// success, no transition event
			ch, _ := s.Get(name)
			return ch, Event{}, nil
		case errors.Is(tx, redis.TxFailedErr):
			continue // retry
		case errors.As(tx, new(errOpenedEvent)):
			ch, _ := s.Get(name)
			return ch, Event{Kind: EventOpened, Name: name, At: now}, nil
		default:
			return nil, Event{}, tx
		}
	}
	return nil, Event{}, perr.New(perr.Internal, "redis OpenPublic contention")
}

type errOpenedEvent struct{}

func (errOpenedEvent) Error() string { return "channel opened" }

// CreatePrivate registers a new private channel.
func (s *RedisStore) CreatePrivate(name Name, ttl time.Duration, now time.Time) (*Channel, Event, error) {
	if name.Visibility != VisibilityPrivate {
		return nil, Event{}, perr.New(perr.ChannelInvalid, "CreatePrivate requires a private channel name")
	}
	ctx, cancel := s.ctx()
	defer cancel()
	rec := channelRecord{
		Name:       name.String(),
		Visibility: string(name.Visibility),
		State:      StateOpen,
		TTLSeconds: int64(ttl.Seconds()),
		CreatedAt:  now.UnixNano(),
		LastActive: now.UnixNano(),
	}
	payload, _ := json.Marshal(rec)
	// HSETNX returns 0 if the field already existed.
	ok, err := s.client.HSetNX(ctx, s.hashKey(), name.String(), payload).Result()
	if err != nil {
		return nil, Event{}, perr.Wrap(perr.Internal, "redis HSetNX", err)
	}
	if !ok {
		return nil, Event{}, perr.New(perr.ChannelExists, "channel already exists")
	}
	ch, _ := fromRecord(rec)
	return ch, Event{Kind: EventOpened, Name: name, At: now}, nil
}

// touchScript updates LastActive on the existing record without overwriting
// other fields, refusing to touch deleted/closed channels. Timestamps are
// passed as strings to avoid Lua float precision loss on int64.
const touchScript = `
local key = KEYS[1]
local field = KEYS[2]
local now = ARGV[1]
local raw = redis.call("HGET", key, field)
if not raw then return "not_found" end
local rec = cjson.decode(raw)
if rec.state == "deleted" then return "not_found" end
if rec.state == "closed" then return "closed" end
rec.last_active = now
redis.call("HSET", key, field, cjson.encode(rec))
return "ok"
`

// Touch refreshes LastActive via a Lua script.
func (s *RedisStore) Touch(name Name, now time.Time) error {
	ctx, cancel := s.ctx()
	defer cancel()
	res, err := s.client.Eval(ctx, touchScript, []string{s.hashKey(), name.String()}, formatNanos(now)).Result()
	if err != nil {
		return perr.Wrap(perr.Internal, "redis Touch", err)
	}
	switch res {
	case "ok":
		return nil
	case "closed":
		return perr.New(perr.ChannelClosed, "channel closed; reopen first")
	default:
		return perr.New(perr.ChannelNotFound, "no such channel")
	}
}

// deleteScript marks the channel deleted and returns "deleted" if the
// transition happened, "noop" if it was already deleted, "not_found"
// otherwise.
const deleteScript = `
local key = KEYS[1]
local field = KEYS[2]
local raw = redis.call("HGET", key, field)
if not raw then return "not_found" end
local rec = cjson.decode(raw)
if rec.state == "deleted" then return "noop" end
rec.state = "deleted"
redis.call("HSET", key, field, cjson.encode(rec))
return "deleted"
`

// Delete marks the channel deleted.
func (s *RedisStore) Delete(name Name, now time.Time) (Event, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	res, err := s.client.Eval(ctx, deleteScript, []string{s.hashKey(), name.String()}).Result()
	if err != nil {
		return Event{}, perr.Wrap(perr.Internal, "redis Delete", err)
	}
	switch res {
	case "deleted":
		return Event{Kind: EventDeleted, Name: name, At: now}, nil
	case "noop":
		return Event{}, nil
	default:
		return Event{}, perr.New(perr.ChannelNotFound, "no such channel")
	}
}

// setDeltaScript flips the delta flag on the record.
const setDeltaScript = `
local key = KEYS[1]
local field = KEYS[2]
local enabled = tonumber(ARGV[1])
local raw = redis.call("HGET", key, field)
if not raw then return "not_found" end
local rec = cjson.decode(raw)
if rec.state == "deleted" then return "not_found" end
if enabled == 1 then
  rec.delta = true
else
  rec.delta = nil
end
redis.call("HSET", key, field, cjson.encode(rec))
return "ok"
`

// SetDelta toggles fossil-delta encoding on the channel.
func (s *RedisStore) SetDelta(name Name, enabled bool) error {
	ctx, cancel := s.ctx()
	defer cancel()
	flag := "0"
	if enabled {
		flag = "1"
	}
	res, err := s.client.Eval(ctx, setDeltaScript, []string{s.hashKey(), name.String()}, flag).Result()
	if err != nil {
		return perr.Wrap(perr.Internal, "redis SetDelta", err)
	}
	if res == "not_found" {
		return perr.New(perr.ChannelNotFound, "no such channel")
	}
	return nil
}

// sweepScript runs an expiry pass server-side. Returns a flat Lua table
// of {name, kind, name, kind, ...}. Private channels past TTL get deleted
// from the hash; public channels transition to closed. Timestamps are
// strings of nanoseconds — Lua arithmetic on strings auto-coerces to
// float64, but we keep the comparison in millisecond resolution to dodge
// the cjson precision trap (sub-ms slack on TTL is fine).
const sweepScript = `
local key = KEYS[1]
local now_ns = ARGV[1]
local now_ms = math.floor(tonumber(string.sub(now_ns, 1, -7)))
local fields = redis.call("HGETALL", key)
local out = {}
for i = 1, #fields, 2 do
  local name = fields[i]
  local raw = fields[i+1]
  local rec = cjson.decode(raw)
  if rec.state == "open" then
    local last_str = tostring(rec.last_active)
    local last_ms = math.floor(tonumber(string.sub(last_str, 1, -7)))
    local age_ms = now_ms - last_ms
    local ttl_ms = tonumber(rec.ttl_seconds) * 1000
    if age_ms >= ttl_ms then
      if rec.visibility == "private" then
        redis.call("HDEL", key, name)
        out[#out+1] = name
        out[#out+1] = "deleted"
      else
        rec.state = "closed"
        redis.call("HSET", key, name, cjson.encode(rec))
        out[#out+1] = name
        out[#out+1] = "closed"
      end
    end
  end
end
return out
`

// Sweep runs one expiry pass via Lua.
func (s *RedisStore) Sweep(now time.Time) ([]Event, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	raw, err := s.client.Eval(ctx, sweepScript, []string{s.hashKey()}, formatNanos(now)).Result()
	if err != nil {
		return nil, perr.Wrap(perr.Internal, "redis Sweep", err)
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	events := make([]Event, 0, len(rows)/2)
	for i := 0; i+1 < len(rows); i += 2 {
		name, _ := rows[i].(string)
		kind, _ := rows[i+1].(string)
		n, err := ParseName(name)
		if err != nil {
			continue
		}
		switch kind {
		case "deleted":
			events = append(events, Event{Kind: EventDeleted, Name: n, At: now})
		case "closed":
			events = append(events, Event{Kind: EventClosed, Name: n, At: now})
		}
	}
	return events, nil
}
