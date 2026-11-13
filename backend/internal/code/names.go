package code

import (
	"strconv"
	"strings"
)

// slugify makes a URL name from a display name: lower-case letters, digits
// and single hyphens, at most max characters, fallback when nothing is left.
func slugify(name string, max int, fallback string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '\'' || r == '’':
			// "Ada's workspace" reads as ada-s-workspace otherwise.
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > max {
		s = strings.TrimRight(s[:max], "-")
	}
	if s == "" {
		return fallback
	}
	return s
}

// freeName is base, or base-2, base-3… cut to max, whichever taken lacks;
// it marks the one returned as taken.
func freeName(base string, max int, taken map[string]bool) string {
	name := base
	for n := 2; taken[name]; n++ {
		suffix := "-" + strconv.Itoa(n)
		name = strings.TrimRight(base[:min(len(base), max-len(suffix))], "-") + suffix
	}
	taken[name] = true
	return name
}

// Lengths the database's CHECKs allow.
const (
	maxOrgSlug  = 39
	maxRepoName = 100
)

// SplitPath reads "<workspace>/<repository>[.git]", with or without a
// leading slash, as a git client sends it, lower-cased.
func SplitPath(p string) (workspace, name string, ok bool) {
	p = strings.TrimPrefix(strings.ToLower(p), "/")
	workspace, name, ok = strings.Cut(p, "/")
	name = strings.TrimSuffix(name, ".git")
	if !ok || workspace == "" || name == "" || strings.ContainsAny(name, "/\\") {
		return "", "", false
	}
	return workspace, name, true
}
