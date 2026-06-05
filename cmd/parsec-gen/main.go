// Package main is the parsec-gen command: it reads a Parsec schema
// registry snapshot (either from a running registry over HTTP or from a
// JSON file on disk) and emits typed Go and TypeScript bindings for
// every registered channel pattern.
//
// Typical use:
//
//	# Go bindings from a running registry into ./gen/parsecgen.
//	parsec-gen --registry http://localhost:8000/parsec/schemas \
//	           --lang go --out ./gen/parsecgen \
//	           --package parsecgen
//
//	# Both languages from a committed snapshot.
//	parsec-gen --registry-file ./schemas.json \
//	           --lang go,ts --out ./gen
//
// Filenames are fixed: Go output writes to <out>/parsec_gen.go,
// TypeScript output writes to <out>/index.ts. The directory is created
// if absent.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/frankbardon/parsec/internal/codegen"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "parsec-gen:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("parsec-gen", flag.ContinueOnError)
	var (
		registry     = fs.String("registry", "", "Schema registry URL (mutually exclusive with --registry-file)")
		registryFile = fs.String("registry-file", "", "Path to a JSON snapshot file (mutually exclusive with --registry)")
		out          = fs.String("out", "./gen", "Output directory")
		lang         = fs.String("lang", "go", "Comma-separated list of target languages (go, ts)")
		pkg          = fs.String("package", "parsecgen", "Go package name (ignored for non-Go targets)")
		header       = fs.String("header", "", "Optional header prepended to every emitted file")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *registry == "" && *registryFile == "" {
		return fmt.Errorf("set --registry <url> or --registry-file <path>")
	}
	if *registry != "" && *registryFile != "" {
		return fmt.Errorf("--registry and --registry-file are mutually exclusive")
	}

	snap, err := codegen.Source{URL: *registry, File: *registryFile}.Load()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}

	gens := codegen.Generators()
	langs := splitCSV(*lang)
	if len(langs) == 0 {
		return fmt.Errorf("--lang must list at least one target")
	}
	opts := codegen.Options{Package: *pkg, Header: *header}
	for _, l := range langs {
		g, ok := gens[l]
		if !ok {
			return fmt.Errorf("unsupported --lang %q (known: go, ts)", l)
		}
		buf, err := g.Generate(snap, opts)
		if err != nil {
			return fmt.Errorf("%s codegen: %w", l, err)
		}
		path := filepath.Join(*out, outputFilename(l))
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d patterns)\n", path, len(snap.Patterns))
	}
	return nil
}

func outputFilename(lang string) string {
	switch lang {
	case "go":
		return "parsec_gen.go"
	case "ts":
		return "index.ts"
	default:
		return lang + ".out"
	}
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}
