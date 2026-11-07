package platform

import (
	"sort"
	"strings"
)

// NormalizeScopes validates OAuth scope tokens and returns a canonical set.
func NormalizeScopes(scopes []string) ([]string, bool) {
	if len(scopes) == 0 || len(scopes) > 64 {
		return nil, false
	}
	seen := map[string]bool{}
	for _, s := range scopes {
		if len(s) == 0 || len(s) > 128 {
			return nil, false
		}
		for _, c := range s {
			if c < 33 || c > 126 || c == '"' || c == '\\' {
				return nil, false
			}
		}
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, true
}

func ScopeString(scopes []string) (string, bool) {
	s, ok := NormalizeScopes(scopes)
	return strings.Join(s, " "), ok
}
