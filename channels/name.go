// Package channels owns the channel naming contract and the channel manager
// (public / private namespaces, TTL bookkeeping, expiry).
//
// The grammar is:
//
//	<visibility>:<app>.<domain>[.<id>][.<topic>]
//
// where visibility is "public" or "private". Anything else is rejected by
// ParseName so the CLI and Twirp surfaces share one validator.
package channels

import (
	"fmt"
	"strings"

	"github.com/frankbardon/parsec/errors"
)

// Visibility is the public/private discriminator that prefixes every channel
// name. It maps 1:1 to a Centrifuge namespace at broker boot.
type Visibility string

const (
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

// String returns the canonical lowercase form used on the wire.
func (v Visibility) String() string { return string(v) }

// Name is a parsed, validated channel name. The zero value is invalid; build
// one with ParseName or BuildName.
type Name struct {
	Visibility Visibility
	App        string
	Domain     string
	ID         string // optional for public, required for private
	Topic      string // optional
}

// String renders the canonical wire form. The output round-trips through
// ParseName.
func (n Name) String() string {
	parts := []string{n.App, n.Domain}
	if n.ID != "" {
		parts = append(parts, n.ID)
	}
	if n.Topic != "" {
		parts = append(parts, n.Topic)
	}
	return string(n.Visibility) + ":" + strings.Join(parts, ".")
}

// IsPrivate reports whether the channel is in the private namespace.
func (n Name) IsPrivate() bool { return n.Visibility == VisibilityPrivate }

// ParseName validates s and returns the structured Name. Errors carry
// PARSEC_CHANNEL_* codes so callers can map them to the right HTTP/Twirp
// status without string matching.
func ParseName(s string) (Name, error) {
	colon := strings.IndexByte(s, ':')
	if colon <= 0 || colon == len(s)-1 {
		return Name{}, errors.New(errors.ChannelInvalid, "channel name must start with public: or private:")
	}
	vis := Visibility(s[:colon])
	if vis != VisibilityPublic && vis != VisibilityPrivate {
		return Name{}, errors.New(errors.ChannelInvalid, fmt.Sprintf("unknown visibility %q; want public or private", vis))
	}
	rest := s[colon+1:]
	if strings.ContainsRune(rest, ':') {
		return Name{}, errors.New(errors.ChannelInvalid, "the : character is reserved for the visibility prefix")
	}
	parts := strings.Split(rest, ".")
	if len(parts) < 2 {
		return Name{}, errors.New(errors.ChannelInvalid, "channel name must include at least <app>.<domain>")
	}
	if len(parts) > 4 {
		return Name{}, errors.New(errors.ChannelInvalid, "channel name has too many components; max is <app>.<domain>.<id>.<topic>")
	}
	for _, p := range parts {
		if err := validateComponent(p); err != nil {
			return Name{}, err
		}
	}
	n := Name{Visibility: vis, App: parts[0], Domain: parts[1]}
	if len(parts) >= 3 {
		n.ID = parts[2]
	}
	if len(parts) == 4 {
		n.Topic = parts[3]
	}
	if vis == VisibilityPrivate && n.ID == "" {
		return Name{}, errors.New(errors.ChannelInvalid, "private channels must include an id segment")
	}
	return n, nil
}

// BuildName composes and validates a Name from its components. The same rules
// as ParseName apply.
func BuildName(vis Visibility, app, domain, id, topic string) (Name, error) {
	n := Name{Visibility: vis, App: app, Domain: domain, ID: id, Topic: topic}
	if _, err := ParseName(n.String()); err != nil {
		return Name{}, err
	}
	return n, nil
}

// validateComponent enforces the lowercase ASCII letters / digits / hyphen
// rule. UUIDs and other id-like values fit because they only use those
// characters once normalized.
func validateComponent(s string) error {
	if s == "" {
		return errors.New(errors.ChannelInvalid, "channel component must not be empty")
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return errors.New(errors.ChannelInvalid, fmt.Sprintf("invalid character %q in channel component %q", r, s))
		}
	}
	return nil
}
