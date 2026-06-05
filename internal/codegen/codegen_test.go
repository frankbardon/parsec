package codegen

import (
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/frankbardon/parsec/schema"
)

var update = flag.Bool("update", false, "refresh golden files instead of comparing")

func loadFixture(t *testing.T) schema.Snapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "registry.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var snap schema.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return snap
}

func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("golden mismatch for %s\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func TestGoGeneratorGolden(t *testing.T) {
	snap := loadFixture(t)
	out, err := GoGenerator{}.Generate(snap, Options{Package: "parsecgen"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	compareGolden(t, "gen.go", out)
}

func TestTSGeneratorGolden(t *testing.T) {
	snap := loadFixture(t)
	out, err := TSGenerator{}.Generate(snap, Options{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	compareGolden(t, "index.ts", out)
}

func TestSourceHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := os.ReadFile(filepath.Join("testdata", "registry.json"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer srv.Close()
	snap, err := Source{URL: srv.URL}.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(snap.Patterns) != 3 {
		t.Fatalf("expected 3 patterns, got %d", len(snap.Patterns))
	}
}

func TestSourceFile(t *testing.T) {
	snap, err := Source{File: filepath.Join("testdata", "registry.json")}.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(snap.Patterns) != 3 {
		t.Fatalf("expected 3 patterns, got %d", len(snap.Patterns))
	}
}

func TestSourceValidation(t *testing.T) {
	if _, err := (Source{}).Load(); err == nil {
		t.Fatal("expected error for empty Source")
	}
	if _, err := (Source{URL: "x", File: "y"}).Load(); err == nil {
		t.Fatal("expected error when both URL and File are set")
	}
}

func TestGoGeneratorCompiles(t *testing.T) {
	// Smoke test: the generated Go must parse and gofmt successfully.
	// format.Source inside Generate already enforces this; this test
	// fails loud if a future template change drops a token that the
	// formatter rejects.
	snap := loadFixture(t)
	if _, err := (GoGenerator{}).Generate(snap, Options{}); err != nil {
		t.Fatalf("generate (unformatted bytes returned alongside error): %v", err)
	}
}
