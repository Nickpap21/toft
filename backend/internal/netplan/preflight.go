package netplan

import (
	"regexp"
	"strings"
)

// CrossRef is an env var of one service that names another service's alias
// which will not resolve from it once app networks are isolated: the two
// share no network. Only the variable's name is kept, never its value
// (values hold secrets).
type CrossRef struct {
	From     string // service ID
	FromName string
	To       string // service ID
	ToName   string
	Alias    string
	Variable string
}

// FindCrossRefs returns, for the plans of one host, every env var of a
// service whose value names, as a host, the alias of another service that
// it will not reach under isolation, and that no service it does reach
// answers to. envVars maps a service ID to its KEY=VALUE pairs.
//
// A service reaches every service that shares one of its networks, where
// nets(X) is X.Home, X.Publish and the Home of every plan that attaches X.
func FindCrossRefs(plans []Plan, envVars map[string][]string) []CrossRef {
	nets := make(map[string]map[string]bool, len(plans))
	for _, p := range plans {
		set := map[string]bool{p.Home.Name: true}
		for _, n := range p.Publish {
			set[n.Name] = true
		}
		nets[p.ServiceID] = set
	}
	for _, p := range plans {
		for _, ep := range p.Attach {
			if set, ok := nets[ep.ServiceID]; ok {
				set[p.Home.Name] = true
			}
		}
	}
	reachable := func(a, b string) bool {
		if a == b {
			return true
		}
		for n := range nets[a] {
			if nets[b][n] {
				return true
			}
		}
		return false
	}

	var out []CrossRef
	for _, a := range plans {
		vars := envVars[a.ServiceID]
		if len(vars) == 0 {
			continue
		}
		// Aliases a resolves after isolation.
		resolved := map[string]bool{}
		for _, b := range plans {
			if b.Alias != "" && reachable(a.ServiceID, b.ServiceID) {
				resolved[b.Alias] = true
			}
		}
		reported := map[string]bool{}
		for _, b := range plans {
			if b.Alias == "" || resolved[b.Alias] || reachable(a.ServiceID, b.ServiceID) {
				continue
			}
			re := hostTokenPattern(b.Alias)
			for _, kv := range vars {
				name, value, ok := strings.Cut(kv, "=")
				if !ok || !re.MatchString(value) {
					continue
				}
				key := b.ServiceID + "\x00" + name
				if reported[key] {
					continue
				}
				reported[key] = true
				out = append(out, CrossRef{
					From: a.ServiceID, FromName: a.ServiceName,
					To: b.ServiceID, ToName: b.ServiceName,
					Alias: b.Alias, Variable: name,
				})
			}
		}
	}
	return out
}

// hostTokenPattern matches alias where a URL, a host:port or a list puts a
// host name: after the start, '@', '/', '=', ':', ',' or space, and before
// ':', '/', ',', space or the end. "db" does not match "mydb.example.com".
func hostTokenPattern(alias string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[@/=:,\s])` + regexp.QuoteMeta(alias) + `(:|/|$|[,\s])`)
}
