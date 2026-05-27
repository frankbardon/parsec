package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/frankbardon/parsec/descriptor"
	"github.com/frankbardon/parsec/internal/rpcclient"
	ucli "github.com/urfave/cli/v3"
)

// TokensCommand groups token operations.
func TokensCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "tokens",
		Usage: "Operate on parsec auth tokens",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "server", Value: "http://localhost:8000", Sources: ucli.EnvVars("PARSEC_SERVER")},
			&ucli.StringFlag{Name: "token", Usage: "Mgmt bearer token (for mgmt issuance)", Sources: ucli.EnvVars("PARSEC_TOKEN")},
		},
		Commands: []*ucli.Command{
			tokensRefreshCommand(),
			tokensMgmtCommand(),
		},
	}
}

func tokensRefreshCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "refresh",
		Usage:     "Exchange a refresh token for a fresh access token",
		ArgsUsage: "<refresh-token>",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("refresh token required")
			}
			// No mgmt bearer for refresh — the refresh token itself authenticates.
			c := rpcclient.New(cmd.String("server"), "")
			res, err := c.RefreshToken(ctx, cmd.Args().First())
			if err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.token.refreshed", res))
		},
	}
}

func tokensMgmtCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "mgmt",
		Usage: "Mint a new mgmt bearer signed by the active key (use during key rotation)",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "subject", Aliases: []string{"s"}, Value: "operator", Usage: "Subject (sub claim)"},
			&ucli.DurationFlag{Name: "ttl", Value: 24 * time.Hour, Usage: "Token lifetime"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			res, err := c.IssueMgmt(ctx, cmd.String("subject"), cmd.Duration("ttl"))
			if err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.token.mgmt", res))
		},
	}
}
