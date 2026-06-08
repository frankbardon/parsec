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
			tokensRevokeCommand(),
			tokensRevokeUserCommand(),
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

// tokensRevokeCommand marks a single token-id revoked. The token-id is
// the `jti` claim that ships in every access token minted by the
// library (and the token-broker /parsec/token response's `token_id`
// field). The mgmt bearer (--token / PARSEC_TOKEN) gates the call.
func tokensRevokeCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "revoke",
		Usage:     "Revoke a single access token by jti",
		ArgsUsage: "<token-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "user", Aliases: []string{"u"}, Usage: "Optional user-id to record with the revocation (audit only)"},
			&ucli.StringFlag{Name: "reason", Aliases: []string{"r"}, Usage: "Optional revocation reason (audit only)"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("token id required")
			}
			tokenID := cmd.Args().First()
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			if err := c.RevokeToken(ctx, tokenID, cmd.String("user"), cmd.String("reason")); err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.token.revoked", RevokeSummary{
				TokenID: tokenID,
				UserID:  cmd.String("user"),
				Reason:  cmd.String("reason"),
			}))
		},
	}
}

// tokensRevokeUserCommand invalidates every token previously issued to
// a user-id. Tokens minted after the call remain valid (the store uses
// a cutoff-timestamp comparison vs the token's iat claim).
func tokensRevokeUserCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "revoke-user",
		Usage:     "Revoke every token previously issued to a user",
		ArgsUsage: "<user-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "reason", Aliases: []string{"r"}, Usage: "Optional revocation reason (audit only)"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("user id required")
			}
			userID := cmd.Args().First()
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			if err := c.RevokeUser(ctx, userID, cmd.String("reason")); err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.token.user_revoked", RevokeSummary{
				UserID: userID,
				Reason: cmd.String("reason"),
			}))
		},
	}
}

// RevokeSummary is the descriptor payload for a successful revoke.
type RevokeSummary struct {
	TokenID string `json:"token_id,omitempty"`
	UserID  string `json:"user_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}
