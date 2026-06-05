// Package codegen reads a schema-registry snapshot and emits typed
// Go and TypeScript bindings for each registered ChannelPattern.
//
// The output supports two use cases:
//
//   - Go publishers and subscribers that want strongly-typed payloads
//     via client.OnAspectTyped[T any] without writing the structs by
//     hand.
//   - TypeScript browser clients that want compile-time guarantees on
//     envelope payloads and channel names.
//
// The generator is deliberately small: a hand-written walker over
// schema.JSONSchema (the subset already shipped in the schema package)
// driven by a tiny template. It does not pull a full JSON-Schema
// compiler — anything richer than the supported subset is emitted as
// json.RawMessage / unknown so the consumer can extend later without
// fork.
package codegen

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/frankbardon/parsec/schema"
)

// Source is where the generator loads a snapshot from.
//
// Exactly one of URL or File must be set. The URL form fetches the
// JSON returned by schema.Handler at the configured prefix; the File
// form reads a snapshot previously written to disk (typically by
// piping the HTTP response through `jq` and committing it).
type Source struct {
	URL    string
	File   string
	Client *http.Client // optional override; defaults to http.DefaultClient with a 30s timeout
}

// Load returns the snapshot described by s.
func (s Source) Load() (schema.Snapshot, error) {
	switch {
	case s.URL != "" && s.File != "":
		return schema.Snapshot{}, fmt.Errorf("codegen: set only one of URL or File")
	case s.URL != "":
		return loadHTTP(s.URL, s.Client)
	case s.File != "":
		return loadFile(s.File)
	default:
		return schema.Snapshot{}, fmt.Errorf("codegen: empty Source")
	}
}

func loadHTTP(url string, client *http.Client) (schema.Snapshot, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return schema.Snapshot{}, fmt.Errorf("codegen: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return schema.Snapshot{}, fmt.Errorf("codegen: fetch registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return schema.Snapshot{}, fmt.Errorf("codegen: registry returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var snap schema.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return schema.Snapshot{}, fmt.Errorf("codegen: decode snapshot: %w", err)
	}
	return snap, nil
}

func loadFile(path string) (schema.Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return schema.Snapshot{}, fmt.Errorf("codegen: read snapshot file: %w", err)
	}
	var snap schema.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return schema.Snapshot{}, fmt.Errorf("codegen: decode snapshot file: %w", err)
	}
	return snap, nil
}

// Options controls a generation run.
type Options struct {
	// Package is the Go package name to emit. Defaults to "parsecgen".
	// Ignored when the target language is TypeScript.
	Package string

	// Header is prepended verbatim above the generator's own header.
	// Use for project-specific build tags or license stanzas.
	Header string
}

func (o Options) pkg() string {
	if o.Package == "" {
		return "parsecgen"
	}
	return o.Package
}

// Generator is the language-specific code emitter. Implementations
// produce a single self-contained file.
type Generator interface {
	Name() string
	Generate(snap schema.Snapshot, opts Options) ([]byte, error)
}

// Generators returns the registered generators by name. Adding a new
// language means registering a new Generator implementation here.
func Generators() map[string]Generator {
	return map[string]Generator{
		"go": GoGenerator{},
		"ts": TSGenerator{},
	}
}
