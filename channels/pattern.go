package channels

import (
	"strings"

	"github.com/frankbardon/parsec/errors"
)

// MaxPatternDepth caps the segment count of a pattern. Patterns that try
// to address more than this many dot-separated segments are rejected at
// parse time so a pathological token like `**.**.**...` cannot become a
// denial-of-service vector. The cap mirrors the channel grammar's
// practical depth (app.domain.id.topic) plus headroom.
const MaxPatternDepth = 8

// patternSegmentKind discriminates the three forms a pattern segment can
// take. The matcher branches on this in its hot path so the field stays
// a small enum, not a string.
type patternSegmentKind uint8

const (
	segLiteral    patternSegmentKind = iota // exact, e.g. "foo"
	segSingleStar                           // "*" — matches exactly one segment
	segDoubleStar                           // "**" — matches one-or-more segments; trailing only
)

// patternSegment is one parsed segment of a Pattern. Only literals carry
// the raw text so wildcards add no string allocation when matching.
type patternSegment struct {
	kind patternSegmentKind
	lit  string
}

// Pattern is a parsed channel-name pattern. It carries a fixed visibility
// (cross-visibility patterns are rejected at parse time) and a slice of
// segments with optional `*` and trailing `**` wildcards.
//
// The zero value is invalid; build one with ParsePattern.
type Pattern struct {
	raw        string
	visibility Visibility
	segs       []patternSegment
}

// String returns the raw pattern text. Round-trips through ParsePattern
// for any valid Pattern.
func (p Pattern) String() string { return p.raw }

// Visibility returns the visibility the pattern is bound to. Patterns
// always carry a concrete visibility (no cross-visibility wildcard).
func (p Pattern) Visibility() Visibility { return p.visibility }

// IsValid reports whether the pattern has been parsed and is usable.
// A zero Pattern returns false.
func (p Pattern) IsValid() bool { return len(p.segs) > 0 }

// ParsePattern validates s and returns a structured Pattern. The grammar
// matches channels.ParseName with two additions:
//
//   - `*` (single segment wildcard) is allowed in any position
//   - `**` (multi-segment wildcard) is allowed only as the last segment
//
// The visibility prefix is mandatory and concrete: `*:foo.*` or any other
// cross-visibility form is rejected.
func ParsePattern(s string) (Pattern, error) {
	colon := strings.IndexByte(s, ':')
	if colon <= 0 || colon == len(s)-1 {
		return Pattern{}, errors.New(errors.InvalidArgument, "pattern must start with public: or private:")
	}
	visRaw := s[:colon]
	vis := Visibility(visRaw)
	if vis != VisibilityPublic && vis != VisibilityPrivate {
		// Reject wildcards or any other value across the visibility axis.
		return Pattern{}, errors.New(errors.InvalidArgument, "pattern visibility must be public or private (no cross-visibility wildcards)")
	}
	rest := s[colon+1:]
	if strings.ContainsRune(rest, ':') {
		return Pattern{}, errors.New(errors.InvalidArgument, "the : character is reserved for the visibility prefix")
	}
	parts := strings.Split(rest, ".")
	if len(parts) < 1 || (len(parts) == 1 && parts[0] == "") {
		return Pattern{}, errors.New(errors.InvalidArgument, "pattern must have at least one segment after the visibility prefix")
	}
	if len(parts) > MaxPatternDepth {
		return Pattern{}, errors.New(errors.InvalidArgument, "pattern exceeds maximum segment depth")
	}
	segs := make([]patternSegment, len(parts))
	for i, p := range parts {
		switch p {
		case "":
			return Pattern{}, errors.New(errors.InvalidArgument, "pattern segment must not be empty")
		case "*":
			segs[i] = patternSegment{kind: segSingleStar}
		case "**":
			if i != len(parts)-1 {
				return Pattern{}, errors.New(errors.InvalidArgument, "** is only allowed as the trailing segment")
			}
			segs[i] = patternSegment{kind: segDoubleStar}
		default:
			if err := validateComponent(p); err != nil {
				return Pattern{}, errors.New(errors.InvalidArgument, "invalid pattern segment: "+err.Error())
			}
			segs[i] = patternSegment{kind: segLiteral, lit: p}
		}
	}
	return Pattern{raw: s, visibility: vis, segs: segs}, nil
}

// Matches reports whether n satisfies p. Visibility must match exactly;
// each segment is then walked left-to-right with the wildcard semantics
// described on ParsePattern. The matcher is hand-rolled (no regex) and
// performs no allocations in the hot path.
func (p Pattern) Matches(n Name) bool {
	if !p.IsValid() {
		return false
	}
	if p.visibility != n.Visibility {
		return false
	}
	// Materialize the channel segments via local fixed-size scratch so we
	// avoid any allocation. A Name has up to 4 segments. Only present
	// (non-empty) segments are pushed so synthesized partial Names match
	// the visible wire shape.
	var nameSegs [4]string
	nCount := 0
	if n.App != "" {
		nameSegs[nCount] = n.App
		nCount++
	}
	if n.Domain != "" {
		nameSegs[nCount] = n.Domain
		nCount++
	}
	if n.ID != "" {
		nameSegs[nCount] = n.ID
		nCount++
	}
	if n.Topic != "" {
		nameSegs[nCount] = n.Topic
		nCount++
	}
	return matchSegments(p.segs, nameSegs[:nCount])
}

// matchSegments compares a slice of pattern segments against the channel
// name segments. The loop is straight — wildcards only carry meaning at
// fixed positions (single-star matches exactly one, double-star matches
// the remainder and is only legal trailing), so no backtracking is needed.
func matchSegments(pat []patternSegment, name []string) bool {
	for i, ps := range pat {
		switch ps.kind {
		case segDoubleStar:
			// Greedy trailing match: needs at least one remaining segment.
			return len(name) > i
		case segSingleStar:
			if i >= len(name) {
				return false
			}
			// Any single segment passes; do not enforce internal grammar
			// of the channel name itself here — Name was already validated
			// by ParseName before reaching the matcher.
		case segLiteral:
			if i >= len(name) {
				return false
			}
			if name[i] != ps.lit {
				return false
			}
		}
	}
	// If we walked the whole pattern, the name must have exactly the same
	// number of segments (no trailing extras allowed without `**`).
	return len(name) == len(pat)
}
