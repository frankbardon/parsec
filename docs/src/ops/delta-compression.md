# Delta compression

Parsec exposes Centrifuge's per-channel fossil-delta encoding as an
opt-in flag. When enabled, the broker ships only the diff between the
previous publication and the current one — the wire payload shrinks
dramatically for high-frequency channels with small mutations
(scoreboards, ticker feeds, partial state snapshots).

## When to enable

Enable delta when **all** of the following hold:

- The channel publishes frequently (multiple times per second).
- Successive payloads share most of their bytes (incremental counters,
  small JSON patches, append-only logs).
- Subscribers can tolerate a one-message-out-of-order tail being
  recomputed from the prior payload.

**Do not** enable delta for:

- Sparse, infrequent publications (no compression headroom; the encoder
  overhead is pure cost).
- Mostly-unique payloads (deltas approach the full payload size — slower
  than no encoding at all).
- Channels where individual messages must remain self-contained on the
  wire (audit feeds, signed payloads).

## Library API

```go
ch, _ := p.OpenPublic("public:scoreboard.counter.global", time.Hour)
_ = p.Manager().SetDelta(ch.Name, true)
```

`SetDelta(name, true)` flips the channel's `DeltaEnabled` flag in the
store. The broker reads the flag on every publish and chooses between
the plain and delta wire format. Toggling is hot — no reconnect
required.

`Channel.DeltaEnabled` is exposed on `parsec channels get` and through
the Twirp `GetChannel` RPC so operators can audit the state.

## CLI

```bash
parsec channels open public:scoreboard.counter.global --delta
parsec channels create private:agent.analysis.{job}.progress --delta --subject job-42
```

For an already-open channel:

```bash
parsec channels set-delta public:scoreboard.counter.global --on
parsec channels set-delta public:scoreboard.counter.global --off
```

## How it works

Parsec sets centrifuge's `WithDelta(true)` publish option and configures
the Node with `AllowedDeltaTypes: []DeltaType{DeltaTypeFossil}`. The
encoder is the fossil-delta algorithm shipped in centrifuge OSS —
parsec adds the channel-level toggle and persistence; the wire encoding
is upstream.

Subscribers negotiate delta support during the connect handshake. A
client that does NOT advertise fossil support receives the full payload
— the broker degrades gracefully, never producing a delta the client
cannot decode.

## Manifest exposure

```json
{
  "delta_compression": "supported"
}
```

The field reports parsec capability, not per-channel state. Operators
inspecting individual channels see the flag on `Channel.DeltaEnabled`.

## Multi-node coherence

When the Redis broker is active, the per-channel delta flag rides on
the channel registry HASH (`parsec:ch:<name>` → `delta=1`). Every node
reads the flag before publishing, so a delta channel stays delta across
the whole cluster without re-issuing tokens.

The `SetDelta` event also rides the channel event bus so cached
`Channel` snapshots stay in sync — clients of `Manager.Subscribe` see a
`updated` event whenever the flag flips.

## Observability

```
parsec_publications_total{visibility="public", delta="true"}
parsec_publications_total{visibility="public", delta="false"}
```

The label is bounded to `true` / `false` so cardinality stays flat.

## Caveats

- Delta is per-channel, not per-subscriber. A channel either ships
  deltas to clients that support fossil OR it does not.
- The encoder holds the previous payload in memory per channel — for
  very wide payloads (>1 MB) the memory overhead can matter; profile
  before enabling.
- History playback still ships full payloads — deltas are a transport
  optimization, not a storage format. Late subscribers receive the
  ground truth.
