package rollout

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// On a server whose agent writes Traefik's routes (D-113), Traefik reads a
// file per service from a directory instead of the containers' labels. The
// panel still gives each routed container its traefik.* labels
// (buildTraefikLabels), the one source of a route; the agent turns the
// serving container's labels into the service's file, so the labels and the
// file never say different things.

// RouteFile is one service's routes in Traefik's file provider.
type RouteFile struct {
	HTTP RouteHTTP `yaml:"http"`
}

// RouteHTTP is the http section of a route file.
type RouteHTTP struct {
	Routers     map[string]*FileRouter     `yaml:"routers,omitempty"`
	Services    map[string]*FileService    `yaml:"services,omitempty"`
	Middlewares map[string]*FileMiddleware `yaml:"middlewares,omitempty"`
}

// FileRouter is a router.
type FileRouter struct {
	Rule        string   `yaml:"rule"`
	EntryPoints []string `yaml:"entryPoints,omitempty"`
	Middlewares []string `yaml:"middlewares,omitempty"`
	Service     string   `yaml:"service"`
	Priority    int      `yaml:"priority,omitempty"`
	TLS         *FileTLS `yaml:"tls,omitempty"`
}

// FileTLS is a router's TLS.
type FileTLS struct {
	CertResolver string       `yaml:"certResolver,omitempty"`
	Domains      []FileDomain `yaml:"domains,omitempty"`
}

// FileDomain is a certificate's names.
type FileDomain struct {
	Main string   `yaml:"main"`
	SANs []string `yaml:"sans,omitempty"`
}

// FileService is a load balancer over the service's containers.
type FileService struct {
	LoadBalancer FileLoadBalancer `yaml:"loadBalancer"`
}

// FileLoadBalancer lists the servers.
type FileLoadBalancer struct {
	Servers []FileServer `yaml:"servers"`
}

// FileServer is one container.
type FileServer struct {
	URL string `yaml:"url"`
}

// FileMiddleware is a basic-auth or IP allowlist middleware.
type FileMiddleware struct {
	BasicAuth   *FileBasicAuth   `yaml:"basicAuth,omitempty"`
	IPAllowList *FileIPAllowList `yaml:"ipAllowList,omitempty"`
}

// FileBasicAuth holds htpasswd entries.
type FileBasicAuth struct {
	Users []string `yaml:"users"`
}

// FileIPAllowList holds the allowed ranges.
type FileIPAllowList struct {
	SourceRange []string `yaml:"sourceRange"`
}

// sharedMiddlewares are the middlewares every server defines in toft.yml
// (server.traefikDynamicConfig), which any service may use.
var sharedMiddlewares = map[string]bool{"secure-headers@file": true, "redirect-to-https@file": true}

// appEntryPoints are the entry points a service's routers may use.
var appEntryPoints = map[string]bool{"web": true, "websecure": true}

var domainKey = regexp.MustCompile(`^tls\.domains\[(\d)\]\.(main|sans)$`)

// RouteFileName is the file in the routes directory that holds the routes
// of the service whose ID starts with id8.
func RouteFileName(id8 string) string { return "paas-" + id8 + ".yml" }

// RoutesFor builds the route file of the service whose ID starts with id8
// from a serving container's labels, with a server for each of hosts (the
// containers' names: Traefik reaches them on a network they share). It
// returns nil when the labels route nothing.
//
// Only the routers, middlewares and service the platform names after the
// service (paas-<id8>-…, paas-svc-<id8>) are accepted, using only those and
// the shared middlewares: a service's file can't take another's host or
// reach Traefik's own API. Any label the platform doesn't write is refused,
// so nothing is dropped without notice.
func RoutesFor(id8 string, labels map[string]string, hosts []string) (*RouteFile, error) {
	if !strings.EqualFold(labels["traefik.enable"], "true") {
		return nil, nil
	}
	own := "paas-" + id8 + "-"
	svcName := "paas-svc-" + id8
	f := &RouteFile{HTTP: RouteHTTP{
		Routers:     map[string]*FileRouter{},
		Services:    map[string]*FileService{},
		Middlewares: map[string]*FileMiddleware{},
	}}
	port := 0
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys) // domains[0].main before .sans; deterministic errors
	for _, k := range keys {
		v := labels[k]
		key, ok := strings.CutPrefix(strings.ToLower(k), "traefik.")
		if !ok || key == "enable" || strings.HasPrefix(key, "docker.") {
			continue
		}
		parts := strings.SplitN(key, ".", 4)
		if len(parts) < 4 || parts[0] != "http" {
			return nil, fmt.Errorf("label %s: not one the platform writes", k)
		}
		kind, name, rest := parts[1], parts[2], parts[3]
		switch kind {
		case "routers":
			if !strings.HasPrefix(name, own) {
				return nil, fmt.Errorf("label %s: router %s isn't this service's", k, name)
			}
			r := f.HTTP.Routers[name]
			if r == nil {
				r = &FileRouter{}
				f.HTTP.Routers[name] = r
			}
			if err := setRouter(r, rest, v); err != nil {
				return nil, fmt.Errorf("label %s: %w", k, err)
			}
		case "services":
			if name != svcName || rest != "loadbalancer.server.port" {
				return nil, fmt.Errorf("label %s: not one the platform writes", k)
			}
			p, err := strconv.Atoi(v)
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("label %s: port %q", k, v)
			}
			port = p
		case "middlewares":
			if !strings.HasPrefix(name, own) {
				return nil, fmt.Errorf("label %s: middleware %s isn't this service's", k, name)
			}
			m := f.HTTP.Middlewares[name]
			if m == nil {
				m = &FileMiddleware{}
				f.HTTP.Middlewares[name] = m
			}
			switch rest {
			case "basicauth.users":
				m.BasicAuth = &FileBasicAuth{Users: splitList(v)}
			case "ipallowlist.sourcerange":
				m.IPAllowList = &FileIPAllowList{SourceRange: splitList(v)}
			default:
				return nil, fmt.Errorf("label %s: not one the platform writes", k)
			}
		default:
			return nil, fmt.Errorf("label %s: not one the platform writes", k)
		}
	}
	if len(f.HTTP.Routers) == 0 {
		return nil, nil
	}
	if port == 0 || len(hosts) == 0 {
		return nil, fmt.Errorf("the routes of %s have no port or no container", svcName)
	}
	lb := FileLoadBalancer{Servers: make([]FileServer, 0, len(hosts))}
	for _, h := range hosts {
		lb.Servers = append(lb.Servers, FileServer{URL: fmt.Sprintf("http://%s:%d", h, port)})
	}
	f.HTTP.Services[svcName] = &FileService{LoadBalancer: lb}
	for name, r := range f.HTTP.Routers {
		if r.Rule == "" {
			return nil, fmt.Errorf("router %s has no rule", name)
		}
		// A router without entry points takes every one, Traefik's API's too.
		if len(r.EntryPoints) == 0 || slices.ContainsFunc(r.EntryPoints, func(e string) bool { return !appEntryPoints[e] }) {
			return nil, fmt.Errorf("router %s: entry points %v, not web or websecure", name, r.EntryPoints)
		}
		// The Docker provider gives a router without one the container's
		// only service; the file provider needs it named.
		if r.Service == "" || r.Service == svcName+"@file" {
			r.Service = svcName
		}
		if r.Service != svcName {
			return nil, fmt.Errorf("router %s uses service %s, not this service's", name, r.Service)
		}
		for _, m := range r.Middlewares {
			bare := strings.TrimSuffix(m, "@file")
			if !sharedMiddlewares[m] && (f.HTTP.Middlewares[bare] == nil) {
				return nil, fmt.Errorf("router %s uses middleware %s, which is neither shared nor this service's", name, m)
			}
		}
	}
	return f, nil
}

func setRouter(r *FileRouter, rest, v string) error {
	switch rest {
	case "rule":
		r.Rule = v
	case "entrypoints":
		r.EntryPoints = splitList(v)
	case "middlewares":
		r.Middlewares = splitList(v)
	case "service":
		r.Service = v
	case "priority":
		p, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("priority %q", v)
		}
		r.Priority = p
	case "tls":
		if !strings.EqualFold(v, "true") {
			return fmt.Errorf("tls %q", v)
		}
		if r.TLS == nil {
			r.TLS = &FileTLS{}
		}
	case "tls.certresolver":
		if r.TLS == nil {
			r.TLS = &FileTLS{}
		}
		r.TLS.CertResolver = v
	default:
		m := domainKey.FindStringSubmatch(rest)
		if m == nil {
			return fmt.Errorf("not one the platform writes")
		}
		if r.TLS == nil {
			r.TLS = &FileTLS{}
		}
		i, _ := strconv.Atoi(m[1])
		for len(r.TLS.Domains) <= i {
			r.TLS.Domains = append(r.TLS.Domains, FileDomain{})
		}
		if m[2] == "main" {
			r.TLS.Domains[i].Main = v
		} else {
			r.TLS.Domains[i].SANs = splitList(v)
		}
	}
	return nil
}

func splitList(v string) []string {
	out := []string{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Marshal renders the file. The result parses back (ParseRouteFile): a file
// Traefik can't parse stops it from loading any change to the directory, and
// one present when it starts leaves it with no file routes at all.
func (f *RouteFile) Marshal() ([]byte, error) {
	raw, err := yaml.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("render the routes: %w", err)
	}
	if _, err := ParseRouteFile(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// ParseRouteFile reads a route file, strictly.
func ParseRouteFile(raw []byte) (*RouteFile, error) {
	var f RouteFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse the routes: %w", err)
	}
	return &f, nil
}
