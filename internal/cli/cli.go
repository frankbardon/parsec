// Package cli holds the urfave/cli/v3 command definitions for the parsec
// binary. cmd/parsec/main.go is a thin assembler; the bodies live here so
// they are testable without spawning processes.
package cli

import (
	"context"
	"io"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/descriptor"
	"github.com/frankbardon/parsec/service"
)

// version is set by main.SetVersion.
var version = "dev"

// SetVersion is called from main.
func SetVersion(v string) { version = v }

// Version returns the build-time version string.
func Version() string { return version }

// WriteManifest prints the manifest envelope to w. Used by both the root
// `--json` flag and the dedicated `manifest` resource.
func WriteManifest(ctx context.Context, w io.Writer) error {
	p, err := parsec.New(parsec.Options{})
	if err != nil {
		return err
	}
	svc := service.New(p, version)
	env := svc.Manifest(ctx)
	return descriptor.WriteEnvelope(w, env)
}

