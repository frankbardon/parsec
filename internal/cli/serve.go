package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/frankbardon/parsec"
	"github.com/frankbardon/parsec/auth"
	"github.com/frankbardon/parsec/internal/config"
	"github.com/frankbardon/parsec/internal/server"
	"github.com/frankbardon/parsec/ratelimit"
	"github.com/frankbardon/parsec/service"
	ucli "github.com/urfave/cli/v3"
)

// ServeCommand boots the embedded centrifuge broker and the HTTP surface
// (websocket + twirp + healthz).
func ServeCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "serve",
		Usage: "Boot the parsec broker and HTTP/Twirp service",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "Path to a YAML config file. CLI flags + env vars override file values.",
				Sources: ucli.EnvVars("PARSEC_CONFIG"),
			},
			&ucli.StringFlag{Name: "addr", Aliases: []string{"a"}, Value: ":8000", Usage: "Listen address (host:port)"},
			&ucli.StringFlag{
				Name:    "state-dir",
				Usage:   "Directory holding keyring.json (created with 0700 if missing). If unset, the keyring is ephemeral.",
				Sources: ucli.EnvVars("PARSEC_STATE_DIR"),
			},
			&ucli.StringFlag{
				Name:    "mgmt-subject",
				Value:   "operator",
				Usage:   "Subject (sub claim) for the bootstrap mgmt token printed at boot",
				Sources: ucli.EnvVars("PARSEC_MGMT_SUBJECT"),
			},
			&ucli.DurationFlag{
				Name:    "mgmt-ttl",
				Value:   24 * time.Hour,
				Usage:   "Lifetime of the bootstrap mgmt token printed at boot",
				Sources: ucli.EnvVars("PARSEC_MGMT_TTL"),
			},
			&ucli.DurationFlag{
				Name:    "keyring-poll",
				Value:   5 * time.Second,
				Usage:   "Interval between keyring.json mtime checks. 0 disables polling.",
				Sources: ucli.EnvVars("PARSEC_KEYRING_POLL"),
			},
			&ucli.BoolFlag{
				Name:    "no-auth",
				Usage:   "Disable bearer auth on the management RPC. DANGEROUS — local development only.",
				Sources: ucli.EnvVars("PARSEC_NO_AUTH"),
			},
			&ucli.StringFlag{
				Name:    "publish-rate",
				Usage:   "Publish rate limit per token, formatted N/<window> (e.g. 100/s). Empty disables.",
				Sources: ucli.EnvVars("PARSEC_PUBLISH_RATE"),
			},
			&ucli.IntFlag{
				Name:    "publish-burst",
				Usage:   "Publish burst peak (defaults to publish-rate when 0)",
				Sources: ucli.EnvVars("PARSEC_PUBLISH_BURST"),
			},
			&ucli.StringFlag{
				Name:    "subscribe-rate",
				Usage:   "Subscribe rate limit per token, formatted N/<window> (e.g. 20/s). Empty disables.",
				Sources: ucli.EnvVars("PARSEC_SUBSCRIBE_RATE"),
			},
			&ucli.IntFlag{
				Name:    "subscribe-burst",
				Usage:   "Subscribe burst peak (defaults to subscribe-rate when 0)",
				Sources: ucli.EnvVars("PARSEC_SUBSCRIBE_BURST"),
			},
			&ucli.StringFlag{
				Name:    "token-issue-rate",
				Usage:   "Refresh-token rate limit per IP, formatted N/<window> (e.g. 5/s). Empty disables.",
				Sources: ucli.EnvVars("PARSEC_TOKEN_ISSUE_RATE"),
			},
			&ucli.IntFlag{
				Name:    "token-issue-burst",
				Usage:   "Token-issue burst peak (defaults to token-issue-rate when 0)",
				Sources: ucli.EnvVars("PARSEC_TOKEN_ISSUE_BURST"),
			},
			&ucli.BoolFlag{
				Name:    "admin-ui",
				Usage:   "Mount the embedded admin SPA at /admin. Off by default — operator-grade tool, not for end users.",
				Sources: ucli.EnvVars("PARSEC_ADMIN_UI"),
			},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

			opts := parsec.Options{Logger: logger}
			var resolved *config.Resolved
			if p := cmd.String("config"); p != "" {
				cfg, err := config.Load(p)
				if err != nil {
					return err
				}
				resolved, err = cfg.Resolve()
				if err != nil {
					return err
				}
				resolved.Source = p
				resolved.ApplyTo(&opts)
				opts.ConfigSource = p
				logger.Info("parsec serve: loaded config", "path", p)
			}

			// Layer flag values on top — but only when explicitly set or
			// the config did not provide a value. urfave/cli evaluates
			// IsSet() for explicit-set detection.
			stateDir := cmd.String("state-dir")
			if stateDir != "" || opts.StateDir == "" {
				if stateDir != "" {
					opts.StateDir = stateDir
				}
			}
			if opts.StateDir != "" && !filepath.IsAbs(opts.StateDir) {
				abs, _ := filepath.Abs(opts.StateDir)
				opts.StateDir = abs
			}
			if cmd.IsSet("keyring-poll") || opts.KeyringPollInterval == 0 {
				opts.KeyringPollInterval = cmd.Duration("keyring-poll")
			}

			// Admin UI flag: --admin-ui sets the toggle when explicitly
			// passed; otherwise the resolved YAML setting stands. Flag
			// is bool, so IsSet() distinguishes "absent" from "false".
			if cmd.IsSet("admin-ui") {
				opts.AdminUI = parsec.AdminUIOptions{Enabled: cmd.Bool("admin-ui")}
			}

			// Rate limits: flags override file when ANY rate flag is set;
			// otherwise the file's values stand.
			if cmd.IsSet("publish-rate") || cmd.IsSet("subscribe-rate") || cmd.IsSet("token-issue-rate") {
				rl, err := ParseRateLimits(
					cmd.String("publish-rate"), int(cmd.Int("publish-burst")),
					cmd.String("subscribe-rate"), int(cmd.Int("subscribe-burst")),
					cmd.String("token-issue-rate"), int(cmd.Int("token-issue-burst")),
				)
				if err != nil {
					return err
				}
				opts.RateLimits = rl
			}

			p, err := parsec.New(opts)
			if err != nil {
				return fmt.Errorf("constructing parsec: %w", err)
			}
			svc := service.New(p, version)

			mgmtSubject := cmd.String("mgmt-subject")
			if resolved != nil && !cmd.IsSet("mgmt-subject") && resolved.MgmtSubject != "" {
				mgmtSubject = resolved.MgmtSubject
			}
			mgmtTTL := cmd.Duration("mgmt-ttl")
			if resolved != nil && !cmd.IsSet("mgmt-ttl") && resolved.MgmtTTL > 0 {
				mgmtTTL = resolved.MgmtTTL
			}
			mgmtToken, mgmtExp, err := p.Issuer().IssueMgmt(mgmtSubject, mgmtTTL)
			if err != nil {
				return fmt.Errorf("issue bootstrap mgmt token: %w", err)
			}
			fmt.Fprintf(os.Stderr, "parsec serve: bootstrap mgmt token (expires %s):\n%s\n\n",
				mgmtExp.UTC().Format(time.RFC3339), mgmtToken)

			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			go func() {
				if err := p.Run(runCtx); err != nil {
					logger.Error("broker exited", "err", err)
				}
			}()

			noAuth := cmd.Bool("no-auth")
			if resolved != nil && !cmd.IsSet("no-auth") {
				noAuth = noAuth || resolved.NoAuth
			}
			var validate = server.MgmtValidator(p)
			if noAuth {
				logger.Warn("parsec serve: no-auth set; the management RPC is unauthenticated")
				validate = nil
			}

			addr := cmd.String("addr")
			if resolved != nil && !cmd.IsSet("addr") && resolved.Addr != "" {
				addr = resolved.Addr
			}
			httpSrv := &http.Server{
				Addr:              addr,
				Handler:           server.New(p, svc, logger, validate),
				ReadHeaderTimeout: 5 * time.Second,
			}

			shutdownCh := make(chan os.Signal, 1)
			signal.Notify(shutdownCh, os.Interrupt, syscall.SIGTERM)
			hupCh := make(chan os.Signal, 1)
			signal.Notify(hupCh, syscall.SIGHUP)
			go func() {
				for range hupCh {
					if err := p.ReloadKeys(); err != nil {
						logger.Warn("SIGHUP: keyring reload failed", "err", err)
						continue
					}
					logger.Info("SIGHUP: keyring reloaded", "active_key_id", p.KeyRing().ActiveID())
				}
			}()
			go func() {
				<-shutdownCh
				cancel()
				shutdownCtx, c2 := context.WithTimeout(context.Background(), 5*time.Second)
				defer c2()
				_ = httpSrv.Shutdown(shutdownCtx)
			}()

			logger.Info("parsec serve listening",
				"addr", httpSrv.Addr,
				"state_dir", stateDir,
				"keyring_path", p.KeyringPath(),
				"active_key_id", p.KeyRing().ActiveID(),
			)
			err = httpSrv.ListenAndServe()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
}

var _ = auth.TypeMgmt

// ParseRateLimits assembles a ratelimit.RateLimits from the operator-
// friendly "N/<window>" shorthand. window is one of "s", "m", "h" (or
// any duration go parses) — "100/s" is "100 events per second". An
// empty rate string disables that bucket. burst <= 0 means "default
// to rate" (no burst above the steady-state).
//
// Returns an error when a spec is malformed so the CLI fails loud
// rather than booting with a silently-misconfigured limiter.
func ParseRateLimits(pubRate string, pubBurst int, subRate string, subBurst int, tokRate string, tokBurst int) (ratelimit.RateLimits, error) {
	out := ratelimit.RateLimits{}
	var err error
	out.Publish, err = ParseRateSpec(pubRate, pubBurst)
	if err != nil {
		return out, fmt.Errorf("--publish-rate: %w", err)
	}
	out.Subscribe, err = ParseRateSpec(subRate, subBurst)
	if err != nil {
		return out, fmt.Errorf("--subscribe-rate: %w", err)
	}
	out.TokenIssue, err = ParseRateSpec(tokRate, tokBurst)
	if err != nil {
		return out, fmt.Errorf("--token-issue-rate: %w", err)
	}
	return out, nil
}

// ParseRateSpec parses a single rate spec "N/<window>" into a
// ratelimit.Limit. Empty input returns the zero Limit (unlimited).
// Burst <= 0 falls back to the rate.
//
// Accepted windows: any duration form parseable by time.ParseDuration,
// plus the bare aliases "s" (=1s), "m" (=1m), "h" (=1h).
func ParseRateSpec(spec string, burst int) (ratelimit.Limit, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ratelimit.Limit{}, nil
	}
	idx := strings.Index(spec, "/")
	if idx < 0 {
		return ratelimit.Limit{}, fmt.Errorf("missing /<window> in %q", spec)
	}
	rateStr := strings.TrimSpace(spec[:idx])
	windowStr := strings.TrimSpace(spec[idx+1:])
	rate, err := strconv.Atoi(rateStr)
	if err != nil {
		return ratelimit.Limit{}, fmt.Errorf("rate %q is not an integer: %w", rateStr, err)
	}
	if rate <= 0 {
		return ratelimit.Limit{}, nil
	}
	// Allow bare unit aliases for the operator-friendly common case.
	switch windowStr {
	case "s":
		windowStr = "1s"
	case "m":
		windowStr = "1m"
	case "h":
		windowStr = "1h"
	}
	window, err := time.ParseDuration(windowStr)
	if err != nil {
		return ratelimit.Limit{}, fmt.Errorf("window %q: %w", windowStr, err)
	}
	if window <= 0 {
		return ratelimit.Limit{}, fmt.Errorf("window must be positive (got %v)", window)
	}
	return ratelimit.Limit{Rate: rate, Per: window, Burst: burst}, nil
}
