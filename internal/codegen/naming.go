package codegen

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/frankbardon/parsec/schema"
)

// patternIdent returns the PascalCase identifier used as the base name
// for everything generated from p (channel helper, constants, payload
// structs). Literal pattern segments contribute; placeholders (*, **,
// {name}) are dropped from the type name and surface only in helper
// signatures.
//
// "sessions:{id}"          -> "Sessions"
// "brands:meridian.reports" -> "BrandsMeridianReports"
// "events.**"               -> "Events"
func patternIdent(pattern string) string {
	parts := splitPatternSegments(pattern)
	var b strings.Builder
	for _, seg := range parts {
		if seg == "" || seg == "*" || seg == "**" {
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			continue
		}
		b.WriteString(toPascal(seg))
	}
	out := b.String()
	if out == "" {
		// All-wildcard pattern; fall back to a content-derived name.
		out = "Pattern" + sanitizeIdent(pattern)
	}
	return out
}

// patternPlaceholders returns the ordered list of placeholder argument
// definitions used by the channel-helper signature.
//
// For "sessions:{id}.{kind}" it returns [{Name:"id"},{Name:"kind"}].
// For "*:foo.{bar}" it returns [{Name:"arg0"},{Name:"bar"}].
// For "events.**" it returns [{Name:"rest",Trailing:true}].
type placeholder struct {
	Name     string
	Trailing bool // true for ** (zero-or-more trailing segments joined by ".")
}

func patternPlaceholders(pattern string) []placeholder {
	parts := splitPatternSegments(pattern)
	var out []placeholder
	anonCount := 0
	for _, seg := range parts {
		switch {
		case seg == "**":
			out = append(out, placeholder{Name: "rest", Trailing: true})
		case seg == "*":
			out = append(out, placeholder{Name: fmt.Sprintf("arg%d", anonCount)})
			anonCount++
		case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && len(seg) > 2:
			out = append(out, placeholder{Name: seg[1 : len(seg)-1]})
		}
	}
	return out
}

// patternHelperBody returns the string-concatenation expression that
// reconstructs a concrete channel name from the placeholder args.
//
// For "sessions:{id}.events" and args=[id], renderer yields
//   "sessions:" + id + ".events"
type patternToken struct {
	Literal     string // non-empty for literal text (including ':' / '.')
	PlaceholderName string // non-empty for a variable substitution
}

func patternTokens(pattern string) []patternToken {
	// Walk the raw pattern character by character so we keep the
	// original separators (':' vs '.') intact.
	var out []patternToken
	var lit strings.Builder
	flushLit := func() {
		if lit.Len() > 0 {
			out = append(out, patternToken{Literal: lit.String()})
			lit.Reset()
		}
	}
	i := 0
	anonCount := 0
	for i < len(pattern) {
		switch c := pattern[i]; c {
		case '{':
			end := strings.IndexByte(pattern[i:], '}')
			if end < 0 {
				lit.WriteByte(c)
				i++
				continue
			}
			flushLit()
			name := pattern[i+1 : i+end]
			out = append(out, patternToken{PlaceholderName: name})
			i += end + 1
		case '*':
			isDouble := i+1 < len(pattern) && pattern[i+1] == '*'
			flushLit()
			if isDouble {
				out = append(out, patternToken{PlaceholderName: "rest"})
				i += 2
			} else {
				out = append(out, patternToken{PlaceholderName: fmt.Sprintf("arg%d", anonCount)})
				anonCount++
				i++
			}
		default:
			lit.WriteByte(c)
			i++
		}
	}
	flushLit()
	return out
}

// aspectIdent is the PascalCase form of an aspect name, used as a
// per-pattern suffix.
func aspectIdent(aspect string) string {
	return toPascal(aspect)
}

// commonInitialisms are uppercased in their entirety inside Go
// identifiers so toPascal honors staticcheck ST1003.
var commonInitialisms = map[string]bool{
	"id": true, "url": true, "uri": true, "api": true, "http": true,
	"https": true, "json": true, "ip": true, "tcp": true, "udp": true,
	"uuid": true, "rpc": true, "sql": true, "html": true, "xml": true,
	"acl": true, "dns": true, "tls": true, "ssh": true, "ttl": true,
	"sse": true, "ws": true, "io": true, "os": true,
}

// toPascal lowercases and uppercase-initializes each ASCII word.
// Non-alphanumeric runes split words; digits are kept verbatim. Words
// matching commonInitialisms are uppercased whole.
func toPascal(s string) string {
	words := splitWords(s)
	var b strings.Builder
	for _, w := range words {
		lower := strings.ToLower(w)
		if commonInitialisms[lower] {
			b.WriteString(strings.ToUpper(w))
			continue
		}
		first := true
		for _, r := range w {
			if first {
				b.WriteRune(unicode.ToUpper(r))
				first = false
				continue
			}
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// splitWords breaks s on non-alphanumeric runes. Digits attach to the
// preceding word.
func splitWords(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// sanitizeIdent replaces every non-identifier rune with '_'. Used as
// the last-resort name suffix for all-wildcard patterns.
func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// splitPatternSegments mirrors schema.splitPatternSegments — we cannot
// import the private helper, but the rules are short.
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

// sortedPatterns returns snap.Patterns in deterministic order so the
// generated output is stable across runs.
func sortedPatterns(snap schema.Snapshot) []schema.ChannelPattern {
	out := make([]schema.ChannelPattern, len(snap.Patterns))
	copy(out, snap.Patterns)
	sort.Slice(out, func(i, j int) bool { return out[i].Pattern < out[j].Pattern })
	return out
}

// sortedAspectNames returns the keys of aspects in deterministic order.
func sortedAspectNames(aspects map[string]schema.Aspect) []string {
	out := make([]string, 0, len(aspects))
	for k := range aspects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
