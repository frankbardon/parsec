package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/descriptor"
	perr "github.com/frankbardon/parsec/errors"
	"github.com/frankbardon/parsec/internal/rpcclient"
	"github.com/redis/go-redis/v9"
	ucli "github.com/urfave/cli/v3"
)

// KeysCommand groups key rotation subcommands. Most routes hit the
// management RPC — the CLI never edits keyring.json directly. That keeps
// the server the single writer and avoids two-writer file races.
//
// `keys export` and `keys import` are the exception: they operate
// directly against the local KeyRingStore so multi-region operators can
// shuttle a snapshot between regions without a live RPC route between
// them. The store is the single writer for the region; export/import
// touch it from the same host that runs parsec.
func KeysCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "keys",
		Usage: "Manage HMAC signing keys on a remote parsec server",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "server", Value: "http://localhost:8000", Sources: ucli.EnvVars("PARSEC_SERVER")},
			&ucli.StringFlag{Name: "token", Usage: "Mgmt bearer token", Sources: ucli.EnvVars("PARSEC_TOKEN")},
		},
		Commands: []*ucli.Command{
			keysListCommand(),
			keysGenerateCommand(),
			keysPromoteCommand(),
			keysRetireCommand(),
			keysReloadCommand(),
			keysExportCommand(),
			keysImportCommand(),
		},
	}
}

func keysListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List every key in the ring (active + verify-only + retired)",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			res, err := c.ListKeys(ctx)
			if err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.keys.list", res))
		},
	}
}

func keysGenerateCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "generate",
		Usage: "Generate a new signing key (joins as verify-only)",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:  "alg",
				Value: "hs256",
				Usage: "Signing algorithm: hs256 (default), rs256, eddsa, es256, es384",
			},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			res, err := c.GenerateKey(ctx, normalizeAlg(cmd.String("alg")))
			if err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.key.generated", res))
		},
	}
}

// normalizeAlg translates the CLI's lowercase friendly form into the
// canonical wire form expected by the server.
func normalizeAlg(s string) string {
	switch strings.ToLower(s) {
	case "", "hs256":
		return "HS256"
	case "rs256":
		return "RS256"
	case "eddsa", "ed25519":
		return "EdDSA"
	case "es256":
		return "ES256"
	case "es384":
		return "ES384"
	default:
		return s
	}
}

func keysPromoteCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "promote",
		Usage:     "Promote a key to active (signs new tokens). Previous active demotes to verify-only.",
		ArgsUsage: "<key-id>",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("key id required")
			}
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			if err := c.PromoteKey(ctx, cmd.Args().First()); err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.key.promoted", map[string]any{"id": cmd.Args().First()}))
		},
	}
}

func keysRetireCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "retire",
		Usage:     "Retire a key (stop accepting it). Active keys must be demoted first.",
		ArgsUsage: "<key-id>",
		Flags: []ucli.Flag{
			&ucli.StringSliceFlag{
				Name:  "notify",
				Usage: "Peer region URL(s) to POST a ReloadKeys to after local retire succeeds. Repeatable.",
			},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("key id required")
			}
			id := cmd.Args().First()
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			if err := c.RetireKey(ctx, id); err != nil {
				return err
			}
			payload := map[string]any{"id": id}
			notifyURLs := cmd.StringSlice("notify")
			if len(notifyURLs) > 0 {
				results := notifyPeers(ctx, notifyURLs, cmd.String("token"))
				payload["notified"] = results
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.key.retired", payload))
		},
	}
}

// PeerNotifyResult records the outcome of a single peer ReloadKeys POST.
// Surfaced in the `keys retire --notify` envelope so operators can spot
// the region that drifted.
type PeerNotifyResult struct {
	URL    string `json:"url"`
	Status int    `json:"status,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// notifyPeers fires one POST per URL to its /twirp/parsec.ParsecService/
// ReloadKeys endpoint. The operator's bearer is forwarded so the peer
// can authenticate the call (peers must trust the same kid via shared
// keyring or shared OIDC issuer). Individual peer failures are logged
// in the returned slice but never roll back the local retire — the
// purpose of the notify hook is best-effort propagation.
func notifyPeers(ctx context.Context, urls []string, token string) []PeerNotifyResult {
	out := make([]PeerNotifyResult, 0, len(urls))
	client := &http.Client{Timeout: 10 * time.Second}
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}
		full := normalizePeerURL(u)
		res := PeerNotifyResult{URL: full}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, full, strings.NewReader("{}"))
		if err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			res.Error = err.Error()
			out = append(out, res)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		res.Status = resp.StatusCode
		res.OK = resp.StatusCode >= 200 && resp.StatusCode < 300
		if !res.OK {
			res.Error = resp.Status
		}
		out = append(out, res)
	}
	return out
}

// normalizePeerURL appends the canonical ReloadKeys path to a base URL
// when the operator passed a bare host. Operators who pass a full path
// (e.g. "https://eu-west:8000/twirp/parsec.ParsecService/ReloadKeys")
// see the URL untouched.
func normalizePeerURL(u string) string {
	const path = "/twirp/parsec.ParsecService/ReloadKeys"
	if strings.Contains(u, "/twirp/") {
		return u
	}
	return strings.TrimRight(u, "/") + path
}

func keysReloadCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "reload",
		Usage: "Force the server to re-read its keyring file (alternative to SIGHUP)",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			if err := c.ReloadKeys(ctx); err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.keys.reloaded", map[string]any{"ok": true}))
		},
	}
}

// keysExportCommand emits the current keyring snapshot as JSON. The
// snapshot is the wire-stable on-disk format (auth.Snapshot) so the
// output of `keys export` can be fed straight into `keys import` on
// another region.
//
// Source resolution: --state-dir (file-backed) or --redis-addr
// (Redis-backed). Mutually exclusive; exactly one must be set.
func keysExportCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "export",
		Usage: "Emit the local keyring snapshot as JSON (for cross-region sharing)",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "state-dir", Usage: "File-backed source: directory holding keyring.json"},
			&ucli.StringFlag{Name: "redis-addr", Usage: "Redis-backed source: host:port or redis://... URL"},
			&ucli.StringFlag{Name: "redis-key-prefix", Value: "parsec", Usage: "Namespace prefix for the Redis store"},
			&ucli.StringFlag{Name: "out", Aliases: []string{"o"}, Usage: "Write snapshot to file instead of stdout"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			store, closer, err := openKeyRingStore(cmd)
			if err != nil {
				return err
			}
			if closer != nil {
				defer closer()
			}
			ring, err := store.Load(ctx)
			if err != nil {
				return perr.Wrap(perr.Internal, "load keyring", err)
			}
			snap := ring.Snapshot()
			if snap.FormatVersion == "" {
				snap.FormatVersion = "1"
			}
			body, err := json.MarshalIndent(snap, "", "  ")
			if err != nil {
				return perr.Wrap(perr.Internal, "encode snapshot", err)
			}
			body = append(body, '\n')
			if out := cmd.String("out"); out != "" {
				if err := os.WriteFile(out, body, 0o600); err != nil {
					return perr.Wrap(perr.Internal, "write snapshot file", err)
				}
				return nil
			}
			if _, err := cmd.Writer.Write(body); err != nil {
				return perr.Wrap(perr.Internal, "write snapshot", err)
			}
			return nil
		},
	}
}

// keysImportCommand replaces the local keyring with an externally-
// produced snapshot. The snapshot is validated structurally; any kid
// already present in the local ring with a different secret causes the
// import to abort unless --force is set.
//
// The import calls KeyRingStore.Save which (for Redis) bumps the
// version key and publishes the rotation event so every running parsec
// node in the region picks up the new ring through its Watch goroutine.
func keysImportCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "import",
		Usage: "Load a keyring snapshot from JSON (stdin or --in) into the local store",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "state-dir", Usage: "File-backed target: directory holding keyring.json"},
			&ucli.StringFlag{Name: "redis-addr", Usage: "Redis-backed target: host:port or redis://... URL"},
			&ucli.StringFlag{Name: "redis-key-prefix", Value: "parsec", Usage: "Namespace prefix for the Redis store"},
			&ucli.StringFlag{Name: "in", Aliases: []string{"i"}, Usage: "Read snapshot from file instead of stdin"},
			&ucli.BoolFlag{Name: "force", Usage: "Overwrite local keys whose kid matches but secret differs"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			store, closer, err := openKeyRingStore(cmd)
			if err != nil {
				return err
			}
			if closer != nil {
				defer closer()
			}
			body, err := readImportInput(cmd)
			if err != nil {
				return err
			}
			var snap auth.Snapshot
			dec := json.NewDecoder(strings.NewReader(string(body)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&snap); err != nil {
				return perr.Wrap(perr.InvalidArgument, "decode snapshot", err)
			}
			fresh := auth.NewKeyRing()
			if err := fresh.LoadSnapshot(snap); err != nil {
				return perr.Wrap(perr.InvalidArgument, "validate snapshot", err)
			}
			if !cmd.Bool("force") {
				existing, lerr := store.Load(ctx)
				if lerr == nil && existing != nil {
					if err := assertNoSecretCollision(existing.Snapshot(), snap); err != nil {
						return err
					}
				}
			}
			if err := store.Save(ctx, fresh); err != nil {
				return perr.Wrap(perr.Internal, "save snapshot to store", err)
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.keys.imported", map[string]any{
				"active_key_id": snap.ActiveKeyID,
				"keys":          len(snap.Keys),
			}))
		},
	}
}

// readImportInput returns the JSON bytes from --in or stdin. The CLI
// fails when stdin is a terminal AND no --in is given so the operator
// gets a clear error rather than a silent hang.
func readImportInput(cmd *ucli.Command) ([]byte, error) {
	if in := cmd.String("in"); in != "" {
		body, err := os.ReadFile(in)
		if err != nil {
			return nil, perr.Wrap(perr.InvalidArgument, "read snapshot file", err)
		}
		return body, nil
	}
	body, err := io.ReadAll(cmd.Reader)
	if err != nil {
		return nil, perr.Wrap(perr.InvalidArgument, "read snapshot from stdin", err)
	}
	if len(body) == 0 {
		return nil, perr.New(perr.InvalidArgument, "no snapshot input: pipe JSON or pass --in <path>")
	}
	return body, nil
}

// assertNoSecretCollision walks the import's keys and refuses when the
// local store has a key with the same id but a different secret_hex.
// This is the safety net for the "two regions each generated their own
// kid X" pathological case — without it the import would silently
// overwrite local secrets.
func assertNoSecretCollision(local, incoming auth.Snapshot) error {
	idx := make(map[string]string, len(local.Keys))
	for _, k := range local.Keys {
		idx[k.ID] = k.SecretHex
	}
	for _, k := range incoming.Keys {
		existing, ok := idx[k.ID]
		if !ok {
			continue
		}
		if existing != k.SecretHex {
			return perr.New(perr.InvalidArgument,
				fmt.Sprintf("key id %q present locally with a different secret; pass --force to overwrite", k.ID))
		}
	}
	return nil
}

// openKeyRingStore resolves --state-dir vs --redis-addr into a
// KeyRingStore plus an optional cleanup. Exactly one flag must be set;
// having both is an operator error and we fail loud.
func openKeyRingStore(cmd *ucli.Command) (auth.KeyRingStore, func(), error) {
	stateDir := strings.TrimSpace(cmd.String("state-dir"))
	redisAddr := strings.TrimSpace(cmd.String("redis-addr"))
	prefix := strings.TrimSpace(cmd.String("redis-key-prefix"))
	if prefix == "" {
		prefix = "parsec"
	}
	switch {
	case stateDir != "" && redisAddr != "":
		return nil, nil, perr.New(perr.InvalidArgument, "--state-dir and --redis-addr are mutually exclusive")
	case stateDir != "":
		path := stateDir
		if !strings.HasSuffix(path, auth.KeyringFileName) {
			path = strings.TrimRight(path, "/") + "/" + auth.KeyringFileName
		}
		return auth.NewFileKeyRingStore(path, 0), nil, nil
	case redisAddr != "":
		client := redis.NewClient(&redis.Options{Addr: stripRedisScheme(redisAddr)})
		store := auth.NewRedisKeyRingStore(client).WithKeyPrefix(prefix)
		return store, func() { _ = client.Close() }, nil
	default:
		return nil, nil, perr.New(perr.InvalidArgument, "one of --state-dir or --redis-addr is required")
	}
}

// stripRedisScheme accepts both "host:port" and "redis://host:port" so
// the CLI can take whichever form the operator's deploy template emits.
// The go-redis client's Options.Addr is a bare host:port.
func stripRedisScheme(s string) string {
	for _, p := range []string{"redis://", "rediss://"} {
		if strings.HasPrefix(s, p) {
			return strings.TrimPrefix(s, p)
		}
	}
	return s
}

