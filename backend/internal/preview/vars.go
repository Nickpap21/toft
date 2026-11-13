package preview

import (
	"regexp"
	"sort"
	"strings"
)

// Variables is what a preview service's container gets (D-97): the
// original's variables (all of them for a writer's preview, plain ones for
// an untrusted one), where each address of a rebuilt service in the copied
// environment becomes its preview address, and extra, which wins.
func Variables(base []string, addresses map[string]string, extra map[string]string) []string {
	hosts := make([]string, 0, len(addresses))
	for h := range addresses {
		hosts = append(hosts, h)
	}
	// Longest first, so a name that contains another is replaced whole.
	sort.Slice(hosts, func(i, j int) bool { return len(hosts[i]) > len(hosts[j]) })
	res := make([]*regexp.Regexp, len(hosts))
	for i, h := range hosts {
		res[i] = regexp.MustCompile(`(^|[^A-Za-z0-9.-])` + regexp.QuoteMeta(h) + `($|[^A-Za-z0-9-])`)
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, over := extra[k]; over {
			continue
		}
		for i, re := range res {
			// Twice: two addresses with one character between them share
			// it, and a match consumes it.
			for range 2 {
				v = re.ReplaceAllString(v, "${1}"+addresses[hosts[i]]+"${2}")
			}
		}
		out = append(out, k+"="+v)
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+extra[k])
	}
	return out
}
