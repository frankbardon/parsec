package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/frankbardon/parsec/descriptor"
	"github.com/frankbardon/parsec/internal/rpcclient"
	ucli "github.com/urfave/cli/v3"
)

// ChannelsCommand groups channel-management subcommands. Each one is a thin
// wrapper over the Twirp client; the CLI cannot do anything the bearer
// account cannot do.
func ChannelsCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "channels",
		Usage: "Manage channels on a remote parsec server",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "server", Value: "http://localhost:8000", Usage: "Parsec server base URL", Sources: ucli.EnvVars("PARSEC_SERVER")},
			&ucli.StringFlag{Name: "token", Usage: "Mgmt bearer token for the management API", Sources: ucli.EnvVars("PARSEC_TOKEN")},
		},
		Commands: []*ucli.Command{
			channelsListCommand(),
			channelsOpenCommand(),
			channelsCreateCommand(),
			channelsGetCommand(),
			channelsDeleteCommand(),
		},
	}
}

func channelsListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List all managed channels",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			res, err := c.ListChannels(ctx)
			if err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.channels.list", res))
		},
	}
}

func channelsOpenCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "open",
		Usage:     "Open (or re-open) a public channel",
		ArgsUsage: "<channel-name>",
		Flags: []ucli.Flag{
			&ucli.DurationFlag{Name: "ttl", Value: 30 * time.Minute, Usage: "Inactivity TTL before close"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("channel name required")
			}
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			res, err := c.OpenPublic(ctx, cmd.Args().First(), cmd.Duration("ttl"))
			if err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.channel.open", res))
		},
	}
}

func channelsCreateCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "create",
		Usage:     "Create a private channel and print its one-shot access + refresh tokens",
		ArgsUsage: "<channel-name>",
		Flags: []ucli.Flag{
			&ucli.DurationFlag{Name: "ttl", Value: 15 * time.Minute, Usage: "Inactivity TTL (max 1h)"},
			&ucli.StringFlag{Name: "subject", Aliases: []string{"s"}, Usage: "Subject (sub claim) embedded in the tokens; typically a user id"},
			&ucli.StringSliceFlag{Name: "scope", Usage: "Channel-name pattern grant (repeatable), e.g. private:webapp.user.42.*"},
			&ucli.StringSliceFlag{Name: "verb", Usage: "Verb granted on the allow scopes (repeatable). Allowed: subscribe, publish, manage. Default: subscribe"},
			&ucli.StringSliceFlag{Name: "deny-scope", Usage: "Channel-name pattern denied (repeatable), e.g. private:webapp.user.42.financials. Deny wins on overlap with --scope."},
			&ucli.StringSliceFlag{Name: "deny-verb", Usage: "Verb the deny scopes apply to (repeatable). Default: same as --verb (or subscribe)."},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("channel name required")
			}
			scopes, err := buildScopeArgs(
				cmd.StringSlice("scope"),
				cmd.StringSlice("verb"),
				cmd.StringSlice("deny-scope"),
				cmd.StringSlice("deny-verb"),
			)
			if err != nil {
				return err
			}
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			res, err := c.CreatePrivate(ctx, cmd.String("subject"), cmd.Args().First(), cmd.Duration("ttl"), scopes)
			if err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.channel.credentials", res))
		},
	}
}

// buildScopeArgs combines --scope, --verb, --deny-scope, and
// --deny-verb flag values into the rpcclient shape. Behavior:
//
//   - The allow-verb list applies to every --scope (so the operator can
//     write `--scope a --scope b --verb subscribe --verb publish` to
//     grant both verbs on both patterns). If no --verb is given, the
//     default is `subscribe` — the most common case.
//   - --deny-scope is each its own ScopeArg with Deny=true. The
//     deny-verb list applies to every --deny-scope; when unset it
//     mirrors the allow-verb list (the common operator intent is
//     "block these patterns from the verbs I just granted").
//   - --verb without --scope OR --deny-scope is an error (no scopes for
//     it to attach to). --deny-verb without --deny-scope is an error
//     for the same reason.
//   - A request with no --scope and no --deny-scope produces a nil
//     slice (exact-match-only grant on the channel name).
func buildScopeArgs(allowPatterns, allowVerbs, denyPatterns, denyVerbs []string) ([]rpcclient.ScopeArg, error) {
	if len(allowPatterns) == 0 && len(denyPatterns) == 0 {
		if len(allowVerbs) > 0 {
			return nil, fmt.Errorf("--verb requires at least one --scope")
		}
		if len(denyVerbs) > 0 {
			return nil, fmt.Errorf("--deny-verb requires at least one --deny-scope")
		}
		return nil, nil
	}
	if len(denyVerbs) > 0 && len(denyPatterns) == 0 {
		return nil, fmt.Errorf("--deny-verb requires at least one --deny-scope")
	}
	allow := allowVerbs
	if len(allow) == 0 {
		allow = []string{"subscribe"}
	}
	deny := denyVerbs
	if len(deny) == 0 {
		// Default to the allow-verb list so the common case
		// "everyone in the prefix subscribes EXCEPT this sub-prefix"
		// works without restating the verb.
		deny = allow
	}
	out := make([]rpcclient.ScopeArg, 0, len(allowPatterns)+len(denyPatterns))
	for _, p := range allowPatterns {
		out = append(out, rpcclient.ScopeArg{Pattern: p, Verbs: append([]string(nil), allow...)})
	}
	for _, p := range denyPatterns {
		out = append(out, rpcclient.ScopeArg{Pattern: p, Verbs: append([]string(nil), deny...), Deny: true})
	}
	return out, nil
}

func channelsGetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Show a single channel's metadata",
		ArgsUsage: "<channel-name>",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("channel name required")
			}
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			res, err := c.GetChannel(ctx, cmd.Args().First())
			if err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.channel.get", res))
		},
	}
}

func channelsDeleteCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "delete",
		Usage:     "Delete a channel (public channels must be deleted explicitly; private auto-expire)",
		ArgsUsage: "<channel-name>",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("channel name required")
			}
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			if err := c.DeleteChannel(ctx, cmd.Args().First()); err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.channel.deleted", map[string]any{"channel": cmd.Args().First()}))
		},
	}
}
