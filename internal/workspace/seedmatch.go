package workspace

import (
	"fmt"
	"path"
	"strings"

	"github.com/daveagill/boxt/internal/config"
)

// seedPattern is one compiled root-relative seed rule.
type seedPattern struct {
	segments []string
	dirOnly  bool
	plain    bool
}

// seedMatcher evaluates the include/exclude rules for a seed configuration.
// Include patterns select candidates; exclude patterns are applied afterwards
// and always win. An empty include list means every candidate is included, which
// is the weaker historical behavior, so excludes still apply.
type seedMatcher struct {
	includes []seedPattern
	excludes []seedPattern
}

// newSeedMatcher compiles seed rules. Config validation normally catches invalid
// patterns, but programmatic callers get the same clear errors here.
func newSeedMatcher(rules config.SeedConfig) (*seedMatcher, error) {
	includes, err := compileSeedPatterns(rules.Include)
	if err != nil {
		return nil, fmt.Errorf("seed.include: %w", err)
	}
	excludes, err := compileSeedPatterns(rules.Exclude)
	if err != nil {
		return nil, fmt.Errorf("seed.exclude: %w", err)
	}
	return &seedMatcher{includes: includes, excludes: excludes}, nil
}

func compileSeedPatterns(patterns []string) ([]seedPattern, error) {
	compiled := make([]seedPattern, 0, len(patterns))
	for _, raw := range patterns {
		normalized, err := config.NormalizeSeedPattern(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", raw, err)
		}
		dirOnly := strings.HasSuffix(normalized, "/")
		trimmed := strings.TrimSuffix(normalized, "/")
		segments := strings.Split(trimmed, "/")
		plain := true
		for _, segment := range segments {
			if strings.ContainsAny(segment, "*?[") {
				plain = false
				break
			}
		}
		compiled = append(compiled, seedPattern{segments: segments, dirOnly: dirOnly, plain: plain})
	}
	return compiled, nil
}

// Match reports whether rel is selected by the rules.
func (m *seedMatcher) Match(rel string) bool {
	parts := splitSeedPath(rel)
	if parts == nil {
		return false
	}
	if len(m.includes) > 0 && !matchesAnySeedPattern(m.includes, parts) {
		return false
	}
	return !matchesAnySeedPattern(m.excludes, parts)
}

// Unmatched returns the paths that the current rules would not allow. Order is
// preserved so errors list paths in Git's stable order.
func (m *seedMatcher) Unmatched(paths []string) []string {
	var unmatched []string
	for _, rel := range paths {
		if !m.Match(rel) {
			unmatched = append(unmatched, rel)
		}
	}
	return unmatched
}

func matchesAnySeedPattern(patterns []seedPattern, parts []string) bool {
	for _, pattern := range patterns {
		if pattern.matches(parts) {
			return true
		}
	}
	return false
}

func (p seedPattern) matches(parts []string) bool {
	if matchSeedSegments(p.segments, parts) {
		return true
	}
	// A trailing slash or a plain path names a directory. Include descendants
	// so node_modules/ and node_modules both cover the whole tree.
	if p.dirOnly || p.plain {
		for i := 0; i <= len(parts); i++ {
			if matchSeedSegments(p.segments, parts[:i]) {
				return true
			}
		}
	}
	return false
}

// matchSeedSegments matches slash-separated pattern segments against path
// segments. A "**" segment matches zero or more complete path segments, while
// "*", "?" and character classes are evaluated per segment with path.Match.
func matchSeedSegments(pattern, parts []string) bool {
	if len(pattern) == 0 {
		return len(parts) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(parts); i++ {
			if matchSeedSegments(pattern[1:], parts[i:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], parts[0])
	if err != nil || !ok {
		return false
	}
	return matchSeedSegments(pattern[1:], parts[1:])
}

// splitSeedPath normalizes a relative slash path. Unsafe or absolute paths never
// match, so a malformed candidate cannot select itself via a pattern.
func splitSeedPath(rel string) []string {
	if rel == "" || strings.HasPrefix(rel, "/") {
		return nil
	}
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil
		}
	}
	return parts
}
