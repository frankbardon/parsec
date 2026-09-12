// Package redisutil centralizes Redis address parsing for every parsec
// surface that takes an address as a string: the library's
// Options.RedisAddr, the `parsec keys` subcommands, and parsec-keys-sync.
//
// It exists because go-redis' Options.Addr is a bare host:port while the
// broker (centrifuge.RedisShardConfig.Address) accepts full URLs. Parsec
// documents the URL forms, so every string-to-client conversion has to
// parse them the same way — previously each caller stripped the scheme by
// hand, which quietly discarded credentials, the database number, and
// (for rediss://) TLS.
package redisutil

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Auth carries the credential and transport overrides an operator can
// supply alongside the address. Every field is optional; a set field wins
// over whatever the address encoded, so a password in the config file
// overrides one embedded in the URL.
type Auth struct {
	Username string
	Password string
	// DB selects the logical database. Nil means "not set", so a
	// deliberate 0 still overrides a URL-supplied database.
	DB *int
	// TLS, when non-nil, forces a TLS connection with this config. Use it
	// for private CAs; rediss:// addresses get a default config without it.
	TLS *tls.Config
}

// unsupportedSchemes are URL forms the plain go-redis client cannot serve.
// Centrifuge's broker understands them, so an operator can legitimately
// have one in their config for the broker — we refuse rather than dial a
// host literally named "redis+sentinel", which is what silently stripping
// the scheme used to do.
var unsupportedSchemes = []string{
	"redis+sentinel://",
	"rediss+sentinel://",
	"redis+cluster://",
	"rediss+cluster://",
}

// ClientOptions converts addr plus auth into go-redis options.
//
// Accepted address forms:
//
//	host:port
//	redis://[[user][:password]@]host:port[/db][?opt=val]
//	rediss://...  (same, TLS)
//	tcp://...     (alias for redis://)
//	unix://[[user][:password]@]/path/to/socket[?db=n]
//
// Sentinel and cluster URLs are rejected with a message pointing at
// Options.RedisClient, which takes a fully-configured client.
func ClientOptions(addr string, auth Auth) (*redis.Options, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("redis address is empty")
	}
	for _, s := range unsupportedSchemes {
		if strings.HasPrefix(addr, s) {
			return nil, fmt.Errorf(
				"redis address %q uses %s, which the parsec redis client cannot build; "+
					"construct the client yourself and pass it as Options.RedisClient", addr, strings.TrimSuffix(s, "://"))
		}
	}

	var opts *redis.Options
	if hasScheme(addr) {
		parsed, err := redis.ParseURL(normalizeScheme(addr))
		if err != nil {
			return nil, fmt.Errorf("parse redis address %q: %w", addr, err)
		}
		opts = parsed
	} else {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return nil, fmt.Errorf(
				"redis address %q is neither host:port nor a redis URL: %w", addr, err)
		}
		opts = &redis.Options{Addr: addr}
	}

	if auth.Username != "" {
		opts.Username = auth.Username
	}
	if auth.Password != "" {
		opts.Password = auth.Password
	}
	if auth.DB != nil {
		opts.DB = *auth.DB
	}
	if auth.TLS != nil {
		opts.TLSConfig = auth.TLS
	}
	return opts, nil
}

// NewClient builds a client from addr plus auth. The client is lazy —
// nothing is dialed until the first command — so callers that need
// fail-fast behavior should Ping it.
func NewClient(addr string, auth Auth) (*redis.Client, error) {
	opts, err := ClientOptions(addr, auth)
	if err != nil {
		return nil, err
	}
	return redis.NewClient(opts), nil
}

// hasScheme reports whether addr looks like a URL rather than host:port.
// An IPv6 host:port ("[::1]:6379") has no "//" so it stays on the
// SplitHostPort path.
func hasScheme(addr string) bool {
	i := strings.Index(addr, "://")
	return i > 0
}

// normalizeScheme maps the aliases redis.ParseURL does not know onto the
// ones it does. centrifuge accepts tcp:// as a synonym for redis://, so
// parsec does too.
func normalizeScheme(addr string) string {
	if rest, ok := strings.CutPrefix(addr, "tcp://"); ok {
		return "redis://" + rest
	}
	return addr
}
