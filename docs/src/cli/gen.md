# parsec-gen

`parsec-gen` reads a schema-registry snapshot and emits typed Go and
TypeScript bindings for every registered channel pattern.

It is a separate binary from `parsec`; build it with `go build
./cmd/parsec-gen` or vendor the published release artifact.

## Usage

```bash
# Go bindings from a running registry into ./gen/parsecgen.
parsec-gen --registry http://localhost:8000/parsec/schemas \
           --lang go --out ./gen/parsecgen \
           --package parsecgen

# Both languages from a committed snapshot file.
parsec-gen --registry-file ./schemas.json \
           --lang go,ts --out ./gen
```

## Flags

| Flag | Default | Purpose |
|---|---|---|
| `--registry <url>` | _required if no `--registry-file`_ | Schema-registry HTTP endpoint |
| `--registry-file <path>` | _required if no `--registry`_ | JSON snapshot on disk |
| `--out <dir>` | `./gen` | Output directory (created if missing) |
| `--lang <list>` | `go` | Comma-separated targets: `go`, `ts` |
| `--package <name>` | `parsecgen` | Go package name (ignored for non-Go) |
| `--header <text>` | _empty_ | Optional header prepended to every emitted file |

`--registry` and `--registry-file` are mutually exclusive.

## Output

| Language | File | Format |
|---|---|---|
| `go` | `<out>/parsec_gen.go` | gofmt-formatted package |
| `ts` | `<out>/index.ts` | No external imports |

For each `ChannelPattern` the generator emits:

- A `<Name>Pattern` constant carrying the raw registry pattern.
- A `<Name>Channel(...)` helper (only if the pattern has placeholders)
  that reconstructs a concrete channel name from typed arguments.
  Trailing `**` placeholders become `rest ...string` in Go and
  `rest: string[]` in TypeScript.
- One `<Name><Aspect>` type per declared aspect. The type follows the
  declared `payload_schema`: `object` becomes a struct/interface,
  `array` becomes an alias, scalars become a typed alias.

## Naming

Pattern segments contribute to the type name in PascalCase; placeholder
segments are dropped from the name and surface only in helper
signatures. Aspect names are PascalCased and appended.

| Pattern | Aspect | Generated type |
|---|---|---|
| `sessions:{id}` | `data` | `SessionsData` |
| `brands:meridian.reports` | `summary` | `BrandsMeridianReportsSummary` |
| `events.**` | `raw` | `EventsRaw` |

Common initialisms (`id`, `url`, `api`, `http`, ...) are uppercased in
their entirety so the output passes `staticcheck` ST1003.

## Wiring with the client

The Go output drops straight into `client.OnAspectTyped[T any]`:

```go
sub, _ := pc.Subscribe(channel)
client.OnAspectTyped[parsecgen.SessionsData](sub, "data",
    func(d parsecgen.SessionsData, env envelope.Envelope) {
        fmt.Println(d.Text)
    })
```

The TypeScript output drops straight into `parsec-client`'s
`typedSubscribe<T>` ergonomic:

```ts
import { sessionsChannel, type SessionsData } from "./gen/index";

const sub = client.typedSubscribe<SessionsData>(sessionsChannel(userId));
sub.on("publication", ({ data }) => {
  // data: SessionsData
});
await sub.subscribe();
```

See [JavaScript Client (parsec-client)](../getting-started/js-client.md)
for the full integration.

## Regeneration

The generator is deterministic — patterns and aspects are emitted in
lexicographic order. Re-running with the same registry yields a
byte-identical file. Commit the output and re-run in CI to detect
unintended schema drift.
