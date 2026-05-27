package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/frankbardon/parsec/descriptor"
	"github.com/frankbardon/parsec/internal/rpcclient"
	ucli "github.com/urfave/cli/v3"
)

// PublishCommand sends a message to a channel via the remote server. Body is
// read from --data, --file, or stdin (in that order of preference).
func PublishCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "publish",
		Usage:     "Publish a message to a channel",
		ArgsUsage: "<channel-name>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "server", Value: "http://localhost:8000", Sources: ucli.EnvVars("PARSEC_SERVER")},
			&ucli.StringFlag{Name: "token", Sources: ucli.EnvVars("PARSEC_TOKEN")},
			&ucli.StringFlag{Name: "data", Aliases: []string{"d"}, Usage: "Inline message body"},
			&ucli.StringFlag{Name: "file", Aliases: []string{"f"}, Usage: "Read message body from file"},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("channel name required")
			}
			data, err := readPayload(cmd.String("data"), cmd.String("file"))
			if err != nil {
				return err
			}
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			ack, err := c.Publish(ctx, cmd.Args().First(), data)
			if err != nil {
				return err
			}
			return descriptor.WriteEnvelope(cmd.Writer, descriptor.NewEnvelope("parsec.publish.ack", ack))
		},
	}
}

// SubscribeCommand is intentionally lightweight: it subscribes to the
// configured channel and streams every received Publication as a one-line
// JSON envelope to stdout. It is useful as a probe; production consumers
// should embed the library or use a websocket client directly.
func SubscribeCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "subscribe",
		Usage:     "Subscribe to a channel (probe; prints publications as NDJSON envelopes)",
		ArgsUsage: "<channel-name>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "server", Value: "http://localhost:8000", Sources: ucli.EnvVars("PARSEC_SERVER")},
			&ucli.StringFlag{Name: "token", Sources: ucli.EnvVars("PARSEC_TOKEN")},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.NArg() < 1 {
				return fmt.Errorf("channel name required")
			}
			c := rpcclient.New(cmd.String("server"), cmd.String("token"))
			return c.SubscribeNDJSON(ctx, cmd.Args().First(), cmd.Writer)
		},
	}
}

func readPayload(inline, file string) ([]byte, error) {
	if inline != "" {
		return []byte(inline), nil
	}
	if file != "" {
		return os.ReadFile(file)
	}
	return io.ReadAll(os.Stdin)
}
