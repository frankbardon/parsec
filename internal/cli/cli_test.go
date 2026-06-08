package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/frankbardon/parsec/internal/cli"
	"github.com/frankbardon/parsec/internal/testutil"
	ucli "github.com/urfave/cli/v3"
)

// runCmd invokes a urfave cli.Command with args and returns its stdout buffer
// and the error from Run. urfave/cli/v3 sets a default os.Stdout writer on
// every command at setup-time, so we propagate the buffer down the tree
// before invoking.
func runCmd(t *testing.T, root *ucli.Command, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	setWriters(root, &buf)
	err := root.Run(context.Background(), args)
	return buf.String(), err
}

// setWriters recursively assigns out as the Writer/ErrWriter on cmd and every
// subcommand. urfave/cli/v3's setupDefaults only initialises the root,
// leaving subcommands with nil writers that default to os.Stdout.
func setWriters(cmd *ucli.Command, out io.Writer) {
	cmd.Writer = out
	cmd.ErrWriter = out
	for _, sub := range cmd.Commands {
		setWriters(sub, out)
	}
}

func TestWriteManifest_ProducesEnvelope(t *testing.T) {
	var buf bytes.Buffer
	if err := cli.WriteManifest(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	var env struct {
		FormatVersion string `json:"format_version"`
		Kind          string `json:"kind"`
		Payload       struct {
			Service string `json:"service"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, buf.String())
	}
	if env.Kind != "parsec.manifest" {
		t.Errorf("kind = %q", env.Kind)
	}
	if env.Payload.Service != "parsec" {
		t.Errorf("service = %q", env.Payload.Service)
	}
	if env.FormatVersion == "" {
		t.Error("format_version empty")
	}
}

func TestSetVersion(t *testing.T) {
	before := cli.Version()
	cli.SetVersion("v1.2.3")
	if cli.Version() != "v1.2.3" {
		t.Errorf("Version = %q", cli.Version())
	}
	cli.SetVersion(before) // restore
}

func TestChannelsList_AgainstFakeServer(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()

	root := cli.ChannelsCommand()
	out, err := runCmd(t, root, "channels", "--server", srv.URL, "list")
	if err != nil {
		t.Fatal(err)
	}
	// The envelope wraps a parsec.channels.list response.
	var env struct {
		Kind    string `json:"kind"`
		Payload []any  `json:"payload"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, out)
	}
	if env.Kind != "parsec.channels.list" {
		t.Errorf("kind = %q", env.Kind)
	}
}

func TestChannelsOpen_AgainstFakeServer(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	root := cli.ChannelsCommand()
	out, err := runCmd(t, root, "channels", "--server", srv.URL, "open", "public:test.x.y", "--ttl", "1m")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.channel.open") {
		t.Errorf("expected envelope kind, got %s", out)
	}
}

func TestChannelsOpen_RequiresName(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	root := cli.ChannelsCommand()
	_, err := runCmd(t, root, "channels", "--server", srv.URL, "open")
	if err == nil {
		t.Fatal("expected error when name missing")
	}
}

func TestChannelsCreate_AgainstFakeServer(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	root := cli.ChannelsCommand()
	out, err := runCmd(t, root, "channels", "--server", srv.URL,
		"create", "private:test.user.1.notif", "--ttl", "10m", "--subject", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.channel.credentials") {
		t.Errorf("expected credentials kind, got %s", out)
	}
	if !strings.Contains(out, "access_token") {
		t.Errorf("expected access_token in output, got %s", out)
	}
}

func TestChannelsCreate_RequiresName(t *testing.T) {
	root := cli.ChannelsCommand()
	_, err := runCmd(t, root, "channels", "create")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestChannelsGet_AgainstFakeServer(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	// Pre-open a channel so Get finds it.
	openRoot := cli.ChannelsCommand()
	if _, err := runCmd(t, openRoot, "channels", "--server", srv.URL, "open", "public:test.x.y"); err != nil {
		t.Fatal(err)
	}
	out, err := runCmd(t, cli.ChannelsCommand(), "channels", "--server", srv.URL, "get", "public:test.x.y")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.channel.get") {
		t.Errorf("expected get envelope, got %s", out)
	}
}

func TestChannelsGet_RequiresName(t *testing.T) {
	_, err := runCmd(t, cli.ChannelsCommand(), "channels", "get")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestChannelsDelete_AgainstFakeServer(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	if _, err := runCmd(t, cli.ChannelsCommand(), "channels", "--server", srv.URL, "open", "public:test.x.y"); err != nil {
		t.Fatal(err)
	}
	out, err := runCmd(t, cli.ChannelsCommand(), "channels", "--server", srv.URL, "delete", "public:test.x.y")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.channel.deleted") {
		t.Errorf("expected delete envelope, got %s", out)
	}
}

func TestChannelsDelete_RequiresName(t *testing.T) {
	_, err := runCmd(t, cli.ChannelsCommand(), "channels", "delete")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPublishCommand_AgainstFakeServer(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	if _, err := runCmd(t, cli.ChannelsCommand(), "channels", "--server", srv.URL, "open", "public:test.x.y"); err != nil {
		t.Fatal(err)
	}
	root := cli.PublishCommand()
	out, err := runCmd(t, root, "publish", "--server", srv.URL,
		"public:test.x.y", "--data", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.publish.ack") {
		t.Errorf("expected publish ack, got %s", out)
	}
}

func TestPublishCommand_RequiresName(t *testing.T) {
	_, err := runCmd(t, cli.PublishCommand(), "publish", "--data", "hi")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSubscribeCommand_RequiresName(t *testing.T) {
	_, err := runCmd(t, cli.SubscribeCommand(), "subscribe")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestKeysList_AgainstFakeServer(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	out, err := runCmd(t, cli.KeysCommand(), "keys", "--server", srv.URL, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.keys.list") {
		t.Errorf("expected list envelope, got %s", out)
	}
	if !strings.Contains(out, "active_key_id") {
		t.Errorf("expected active_key_id, got %s", out)
	}
}

func TestKeysGenerate_AgainstFakeServer(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	out, err := runCmd(t, cli.KeysCommand(), "keys", "--server", srv.URL, "generate")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.key.generated") {
		t.Errorf("expected generate envelope, got %s", out)
	}
}

func TestKeysPromote_RequiresID(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	_, err := runCmd(t, cli.KeysCommand(), "keys", "--server", srv.URL, "promote")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestKeysRetire_RequiresID(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	_, err := runCmd(t, cli.KeysCommand(), "keys", "--server", srv.URL, "retire")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestKeysReload_FailsWithoutStateDir(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	// The reload command makes an RPC call; since the test server has no
	// state dir, the underlying ReloadKeys returns an error.
	_, err := runCmd(t, cli.KeysCommand(), "keys", "--server", srv.URL, "reload")
	if err == nil {
		t.Fatal("expected error when reloading ephemeral keyring")
	}
}

func TestTokensRefresh_RequiresArg(t *testing.T) {
	_, err := runCmd(t, cli.TokensCommand(), "tokens", "refresh")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTokensMgmt_AgainstFakeServer(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	out, err := runCmd(t, cli.TokensCommand(), "tokens", "--server", srv.URL, "mgmt", "--subject", "op", "--ttl", "1h")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.token.mgmt") {
		t.Errorf("expected mgmt token envelope, got %s", out)
	}
	if !strings.Contains(out, "mgmt_token") {
		t.Errorf("expected mgmt_token field, got %s", out)
	}
}

func TestTokensRefresh_AgainstFakeServer(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	// First create a private channel to get a refresh token.
	createOut, err := runCmd(t, cli.ChannelsCommand(), "channels", "--server", srv.URL,
		"create", "private:test.user.1.notif", "--ttl", "10m", "--subject", "u1")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Payload struct {
			RefreshToken string `json:"refresh_token"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(createOut), &env); err != nil {
		t.Fatalf("parse create output: %v (%s)", err, createOut)
	}
	if env.Payload.RefreshToken == "" {
		t.Fatal("no refresh token in create output")
	}
	out, err := runCmd(t, cli.TokensCommand(), "tokens", "--server", srv.URL, "refresh", env.Payload.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.token.refreshed") {
		t.Errorf("expected refreshed envelope, got %s", out)
	}
}

func TestTokensRevoke_RequiresArg(t *testing.T) {
	_, err := runCmd(t, cli.TokensCommand(), "tokens", "revoke")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTokensRevokeUser_RequiresArg(t *testing.T) {
	_, err := runCmd(t, cli.TokensCommand(), "tokens", "revoke-user")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTokensRevoke_NoStoreReturnsError(t *testing.T) {
	srv, _, stop := testutil.NewTestServer(t)
	defer stop()
	_, err := runCmd(t, cli.TokensCommand(), "tokens", "--server", srv.URL,
		"revoke", "jti-abc", "--user", "u1", "--reason", "leak")
	if err == nil {
		t.Fatal("expected error from server with no RevocationStore")
	}
}

func TestTokensRevoke_AgainstStoreWritesEntry(t *testing.T) {
	srv, _, store, stop := testutil.NewTestServerWithRevocations(t)
	defer stop()
	out, err := runCmd(t, cli.TokensCommand(), "tokens", "--server", srv.URL,
		"revoke", "jti-abc", "--user", "u1", "--reason", "leak")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.token.revoked") {
		t.Errorf("expected revoked envelope, got %s", out)
	}
	revoked, err := store.IsRevoked(context.Background(), "jti-abc")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Error("store did not record the revocation")
	}
}

func TestTokensRevokeUser_AgainstStoreWritesEntry(t *testing.T) {
	srv, _, store, stop := testutil.NewTestServerWithRevocations(t)
	defer stop()
	out, err := runCmd(t, cli.TokensCommand(), "tokens", "--server", srv.URL,
		"revoke-user", "u1", "--reason", "session reset")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "parsec.token.user_revoked") {
		t.Errorf("expected user_revoked envelope, got %s", out)
	}
	// Token issued before the revoke call is considered revoked.
	preCutoff := time.Now().UTC().Add(-time.Minute)
	revoked, err := store.IsUserRevoked(context.Background(), "u1", preCutoff)
	if err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Error("store did not record the user-blanket revocation")
	}
}

func TestServeCommand_Exists(t *testing.T) {
	cmd := cli.ServeCommand()
	if cmd.Name != "serve" {
		t.Errorf("Name = %q", cmd.Name)
	}
	if cmd.Action == nil {
		t.Fatal("Action nil")
	}
}
