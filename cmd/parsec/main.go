// Package main is the entry point for the parsec CLI binary.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/frankbardon/parsec/internal/cli"

	// Register built-in sink factories so `parsec serve --config` can
	// declare sinks by kind name. Importing for side effects only.
	_ "github.com/frankbardon/parsec/sinks/email"
	_ "github.com/frankbardon/parsec/sinks/slack"
	_ "github.com/frankbardon/parsec/sinks/webhook"

	ucli "github.com/urfave/cli/v3"
)

// version is set by the build system.
var version = "dev"

func main() {
	cli.SetVersion(version)
	app := buildApp()
	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func buildApp() *ucli.Command {
	return &ucli.Command{
		Name:    "parsec",
		Usage:   "Scalable realtime messaging engine",
		Version: version,
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "json", Usage: "Output the parsec manifest as a descriptor envelope"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.Bool("json") {
				return cli.WriteManifest(ctx, cmd.Writer)
			}
			return ucli.ShowAppHelp(cmd)
		},
		Commands: []*ucli.Command{
			cli.ServeCommand(),
			cli.ChannelsCommand(),
			cli.TokensCommand(),
			cli.KeysCommand(),
			cli.PublishCommand(),
			cli.SubscribeCommand(),
			cli.DLQCommand(),
			cli.LoginCommand(),
			cli.LogoutCommand(),
		},
	}
}
