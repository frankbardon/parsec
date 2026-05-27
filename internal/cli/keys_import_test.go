package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/internal/cli"
	ucli "github.com/urfave/cli/v3"
)

// TestKeysImport_RoundTripsViaFileStore exercises the export+import
// pair end-to-end against the file-backed store: an exported snapshot
// from region A is imported into region B (a fresh state dir) and
// must materialize the same active key + ring.
func TestKeysImport_RoundTripsViaFileStore(t *testing.T) {
	// Build region-A ring.
	regionA := t.TempDir()
	regionAPath := filepath.Join(regionA, auth.KeyringFileName)
	ringA := auth.NewKeyRing()
	if _, err := ringA.Generate(); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveKeyRing(regionAPath, ringA); err != nil {
		t.Fatal(err)
	}
	// Export it.
	exportCmd := cli.KeysCommand()
	var exportBuf bytes.Buffer
	setWriters(exportCmd, &exportBuf)
	if err := exportCmd.Run(context.Background(), []string{
		"keys", "export", "--state-dir", regionA,
	}); err != nil {
		t.Fatalf("export: %v", err)
	}

	// Import into region B.
	regionB := t.TempDir()
	regionBPath := filepath.Join(regionB, auth.KeyringFileName)
	importCmd := cli.KeysCommand()
	var importBuf bytes.Buffer
	setWriters(importCmd, &importBuf)
	importCmd = setReaderForImport(importCmd, &exportBuf)
	if err := importCmd.Run(context.Background(), []string{
		"keys", "import", "--state-dir", regionB,
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(importBuf.String(), "parsec.keys.imported") {
		t.Errorf("expected imported envelope kind, got %q", importBuf.String())
	}

	// Region B's file must round-trip to the same Snapshot.
	ringB, err := auth.LoadKeyRing(regionBPath)
	if err != nil {
		t.Fatal(err)
	}
	if ringB.ActiveID() != ringA.ActiveID() {
		t.Fatalf("active key mismatch: A=%s B=%s", ringA.ActiveID(), ringB.ActiveID())
	}
	listA := ringA.List()
	listB := ringB.List()
	if len(listA) != len(listB) {
		t.Fatalf("key count mismatch: A=%d B=%d", len(listA), len(listB))
	}
}

// TestKeysImport_RefusesCollidingSecret asserts the safety net: if a
// kid already exists locally with a different secret, the import
// aborts unless --force is given.
func TestKeysImport_RefusesCollidingSecret(t *testing.T) {
	// Region A has its own ring.
	regionA := t.TempDir()
	regionAPath := filepath.Join(regionA, auth.KeyringFileName)
	ringA := auth.NewKeyRing()
	if _, err := ringA.Generate(); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveKeyRing(regionAPath, ringA); err != nil {
		t.Fatal(err)
	}
	exportBuf := mustExport(t, regionA)

	// Region B has a different ring with the SAME kid (we hand-craft the
	// snapshot to force the collision by reading + replacing the secret).
	regionB := t.TempDir()
	regionBPath := filepath.Join(regionB, auth.KeyringFileName)
	var importedSnap auth.Snapshot
	if err := json.Unmarshal(exportBuf.Bytes(), &importedSnap); err != nil {
		t.Fatal(err)
	}
	collisionSnap := importedSnap
	collisionSnap.Keys = append([]auth.SnapshotKey(nil), importedSnap.Keys...)
	// Use a different (still valid 32+ byte hex) secret for the same kid.
	collisionSnap.Keys[0].SecretHex = strings.Repeat("aa", 32)
	ringB := auth.NewKeyRing()
	if err := ringB.LoadSnapshot(collisionSnap); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveKeyRing(regionBPath, ringB); err != nil {
		t.Fatal(err)
	}

	// Import region A's snapshot into region B WITHOUT --force.
	importCmd := setReaderForImport(cli.KeysCommand(), bytes.NewReader(exportBuf.Bytes()))
	var stdout bytes.Buffer
	setWriters(importCmd, &stdout)
	err := importCmd.Run(context.Background(), []string{
		"keys", "import", "--state-dir", regionB,
	})
	if err == nil {
		t.Fatal("expected collision error without --force")
	}
	if !strings.Contains(err.Error(), "different secret") {
		t.Errorf("error not the collision case: %v", err)
	}

	// With --force the import succeeds.
	importCmd2 := setReaderForImport(cli.KeysCommand(), bytes.NewReader(exportBuf.Bytes()))
	var stdout2 bytes.Buffer
	setWriters(importCmd2, &stdout2)
	if err := importCmd2.Run(context.Background(), []string{
		"keys", "import", "--state-dir", regionB, "--force",
	}); err != nil {
		t.Fatalf("import --force: %v", err)
	}
}

// TestKeysImport_RejectsMalformedJSON guards the validation path. The
// store must never see a non-Snapshot blob; the import surface emits
// PARSEC_INVALID_ARGUMENT in that case.
func TestKeysImport_RejectsMalformedJSON(t *testing.T) {
	regionB := t.TempDir()
	importCmd := setReaderForImport(cli.KeysCommand(), strings.NewReader("not json"))
	var stdout bytes.Buffer
	setWriters(importCmd, &stdout)
	err := importCmd.Run(context.Background(), []string{
		"keys", "import", "--state-dir", regionB,
	})
	if err == nil {
		t.Fatal("expected decode error")
	}
}

// mustExport runs `keys export --state-dir <dir>` and returns the
// captured stdout buffer. Helper for tests that need a snapshot as
// input to subsequent commands.
func mustExport(t *testing.T, dir string) *bytes.Buffer {
	t.Helper()
	root := cli.KeysCommand()
	var out bytes.Buffer
	setWriters(root, &out)
	if err := root.Run(context.Background(), []string{
		"keys", "export", "--state-dir", dir,
	}); err != nil {
		t.Fatal(err)
	}
	return &out
}

// setReaderForImport wires r as the stdin reader for cmd. urfave/cli
// v3 reads --no-flag stdin via cmd.Reader.
func setReaderForImport(cmd *ucli.Command, r interface{ Read([]byte) (int, error) }) *ucli.Command {
	walkSetReader(cmd, r)
	return cmd
}

func walkSetReader(cmd *ucli.Command, r interface{ Read([]byte) (int, error) }) {
	cmd.Reader = r
	for _, sub := range cmd.Commands {
		walkSetReader(sub, r)
	}
}
