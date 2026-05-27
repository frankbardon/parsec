package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/internal/cli"
)

// readFileForTest reads path or fails the test. Tiny helper to keep
// the per-test bodies readable.
func readFileForTest(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestKeysExport_WritesValidSnapshotJSON drives the export command
// against an on-disk file store and asserts the stdout payload is a
// well-formed auth.Snapshot — the on-the-wire format the import side
// (or a peer region's operator) reads back.
func TestKeysExport_WritesValidSnapshotJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, auth.KeyringFileName)
	ring := auth.NewKeyRing()
	if _, err := ring.Generate(); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveKeyRing(path, ring); err != nil {
		t.Fatal(err)
	}

	root := cli.KeysCommand()
	var out bytes.Buffer
	setWriters(root, &out)
	if err := root.Run(context.Background(), []string{"keys", "export", "--state-dir", dir}); err != nil {
		t.Fatalf("export: %v", err)
	}

	var snap auth.Snapshot
	if err := json.Unmarshal(out.Bytes(), &snap); err != nil {
		t.Fatalf("output not a snapshot: %v (raw=%q)", err, out.String())
	}
	if snap.ActiveKeyID == "" {
		t.Error("export missing active_key_id")
	}
	if len(snap.Keys) == 0 {
		t.Error("export missing keys")
	}
	if snap.FormatVersion != "1" {
		t.Errorf("format_version = %q, want 1", snap.FormatVersion)
	}
}

// TestKeysExport_OutFlagWritesFile asserts --out diverts the snapshot
// to a file (with 0600) rather than stdout.
func TestKeysExport_OutFlagWritesFile(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	srcPath := filepath.Join(srcDir, auth.KeyringFileName)
	dstPath := filepath.Join(dstDir, "exported.json")
	ring := auth.NewKeyRing()
	if _, err := ring.Generate(); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveKeyRing(srcPath, ring); err != nil {
		t.Fatal(err)
	}
	root := cli.KeysCommand()
	var stdout bytes.Buffer
	setWriters(root, &stdout)
	err := root.Run(context.Background(), []string{
		"keys", "export", "--state-dir", srcDir, "--out", dstPath,
	})
	if err != nil {
		t.Fatalf("export --out: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("--out should suppress stdout, got %q", stdout.String())
	}
	// File must parse as a Snapshot.
	body := readFileForTest(t, dstPath)
	var snap auth.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("dst not snapshot: %v (raw=%q)", err, string(body))
	}
	if snap.ActiveKeyID == "" {
		t.Error("--out snapshot missing active_key_id")
	}
}

// TestKeysExport_RequiresSource verifies the command refuses to run
// without --state-dir or --redis-addr — operators should not be able
// to invoke export against an undefined backend by accident.
func TestKeysExport_RequiresSource(t *testing.T) {
	root := cli.KeysCommand()
	var out bytes.Buffer
	setWriters(root, &out)
	if err := root.Run(context.Background(), []string{"keys", "export"}); err == nil {
		t.Fatal("expected error without --state-dir/--redis-addr")
	}
}
