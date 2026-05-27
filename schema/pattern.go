package schema

import (
	"fmt"
	"strings"
)

// Pattern is a compiled channel-name pattern. The source grammar mixes
// literal segments with two kinds of wildcards:
//
//   - {name} — matches exactly one segment; the matched value is bound to
//             the placeholder name.
//   - *     — matches exactly one segment, anonymous.
//   - **    — matches one or more trailing segments greedily. Only valid
//             as the final token. Anonymous.
//
// Segments are split on the channel-name delimiter set: '.' AND ':'. This
// lets a pattern address both Parsec's canonical wire form
// ("public:sessions.app.id") and the upgrade-spec's shorthand
// ("sessions:{id}") with one matcher.
//
// Pattern is the schema registry's matching primitive. It does not depend
// on the channels grammar — names that fail channels.ParseName can still
// match a pattern (the schema registry is intentionally more permissive
// than the channel grammar).
type Pattern struct {
	tokens []patternToken
	raw    string
}

type patternToken struct {
	kind tokenKind
	text string // literal text or placeholder name (without braces)
}

type tokenKind int

const (
	tokLiteral tokenKind = iota
	tokSingle            // '*' wildcard (anonymous, single segment)
	tokDouble            // '**' wildcard (anonymous, trailing multi-segment)
	tokVar               // '{name}' single-segment placeholder
)

// ParsePattern compiles src.
func ParsePattern(src string) (Pattern, error) {
	if src == "" {
		return Pattern{}, fmt.Errorf("schema: empty pattern")
	}
	segs := splitPatternSegments(src)
	tokens := make([]patternToken, 0, len(segs))
	for i, s := range segs {
		switch {
		case s == "**":
			if i != len(segs)-1 {
				return Pattern{}, fmt.Errorf("schema: ** must be the trailing segment in %q", src)
			}
			tokens = append(tokens, patternToken{kind: tokDouble})
		case s == "*":
			tokens = append(tokens, patternToken{kind: tokSingle})
		case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") && len(s) > 2:
			tokens = append(tokens, patternToken{kind: tokVar, text: s[1 : len(s)-1]})
		case strings.ContainsAny(s, "{}*"):
			return Pattern{}, fmt.Errorf("schema: segment %q mixes literal and wildcard in %q", s, src)
		default:
			tokens = append(tokens, patternToken{kind: tokLiteral, text: s})
		}
	}
	return Pattern{tokens: tokens, raw: src}, nil
}

// Raw returns the source string the pattern was compiled from.
func (p Pattern) Raw() string { return p.raw }

// Match tests channel against p. On a match, the returned map binds any
// {name} placeholders to the segments they consumed. Returns (nil, false)
// when the pattern does not match.
func (p Pattern) Match(channel string) (map[string]string, bool) {
	segs := splitPatternSegments(channel)
	bindings := map[string]string{}
	si := 0
	for ti, tok := range p.tokens {
		if tok.kind == tokDouble {
			// Greedy tail match — consume the remainder.
			if si >= len(segs) {
				return nil, false
			}
			return bindings, true
		}
		if si >= len(segs) {
			return nil, false
		}
		switch tok.kind {
		case tokLiteral:
			if segs[si] != tok.text {
				return nil, false
			}
		case tokSingle:
			// anonymous single — accept anything
		case tokVar:
			bindings[tok.text] = segs[si]
		}
		si++
		_ = ti
	}
	if si != len(segs) {
		return nil, false
	}
	return bindings, true
}

// splitPatternSegments splits on '.' AND ':' so the matcher handles both
// the canonical channel form and shorthand forms uniformly.
func splitPatternSegments(s string) []string {
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.', ':':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
