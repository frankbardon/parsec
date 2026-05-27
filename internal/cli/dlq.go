package cli

import (
	"context"
	"fmt"

	"github.com/frankbardon/parsec/descriptor"
	"github.com/frankbardon/parsec/internal/rpcclient"
	ucli "github.com/urfave/cli/v3"
)

// DLQCommand groups dead-letter queue management subcommands. Every route
// goes through the management RPC so the server is the single writer.
func DLQCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "dlq",
		Usage: "Inspect, replay, or discard items in the sink dead-letter queue",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "server", Value: "http://localhost:8000", Sources: ucli.EnvVars("PARSEC_SERVER")},
			&ucli.StringFlag{Name: "token", Usage: "Mgmt bearer token", Sources: ucli.EnvVars("PARSEC_TOKEN")},
		},
		Commands: []*ucli.Command{
			dlqListCommand(),
			dlqCountCommand(),
			dlqDiscardCommand(),
			dlqReplayCommand(),
		},
	}
}

func dlqListCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "list",
		Usage:     "List failed-delivery items for a sink (newest first)",
		ArgsUsage: "<sink-name>",
		Flags: []ucli.Flag{
			&ucli.IntFlag{Name: "limit", Value: 50, Usage: "Maximum items to return"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("sink name required")
			}
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			res, err := c.ListDLQ(ctx, cmd.Args().First(), int(cmd.Int("limit")))
			if err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.dlq.list", res))
		},
	}
}

func dlqCountCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "count",
		Usage:     "Return the number of DLQ items for a sink",
		ArgsUsage: "<sink-name>",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("sink name required")
			}
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			res, err := c.CountDLQ(ctx, cmd.Args().First())
			if err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.dlq.count", res))
		},
	}
}

func dlqDiscardCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "discard",
		Usage:     "Permanently remove a DLQ item by id",
		ArgsUsage: "<id>",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("dlq id required")
			}
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			if err := c.DiscardDLQ(ctx, cmd.Args().First()); err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.dlq.discarded", map[string]any{"id": cmd.Args().First()}))
		},
	}
}

func dlqReplayCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "replay",
		Usage:     "Re-run a DLQ item through its original sink",
		ArgsUsage: "<id>",
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("dlq id required")
			}
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			if err := c.ReplayDLQ(ctx, cmd.Args().First()); err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.dlq.replayed", map[string]any{"id": cmd.Args().First()}))
		},
	}
}
