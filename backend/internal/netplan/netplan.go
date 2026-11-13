// Package netplan names and describes the Docker networks apps run on when
// app networks are isolated (TOFT_APP_NETWORKS=isolated; D-19, D-26, D-31).
//
// Every environment gets its own bridge network, and so do the services
// shared by a project or an org. Names are built from immutable IDs, never
// from names users choose, and every network the platform creates carries
// paas.managed=true plus the IDs it belongs to, so a network of the same name
// that the platform did not create is refused rather than joined.
//
// It is a leaf package: no database, no Docker, no internal imports.
package netplan

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

// LegacyNetwork is the control plane's own network (TRAEFIK_NETWORK by
// default). Under isolated app networks only the panel's containers and the
// proxy are on it; apps never join it.
const LegacyNetwork = "paas-network"

// Network labels.
const (
	LabelManaged       = "paas.managed"
	LabelKind          = "paas.network.kind"
	LabelEnvironmentID = "paas.environment_id"
	LabelProjectID     = "paas.project_id"
	LabelOrgID         = "paas.org_id"
)

// LabelContainerNetwork is set on the app containers the platform runs: the
// name of the network the container's home network is. Routed containers
// also carry traefik.docker.network with the same value.
const LabelContainerNetwork = "paas.network"

// Name prefixes of the networks the platform creates.
const (
	envPrefix     = "paas-env-"
	projectPrefix = "paas-proj-"
	orgPrefix     = "paas-org-"
)

// Kind says whose network it is.
type Kind string

const (
	KindEnv     Kind = "env"
	KindProject Kind = "project"
	KindOrg     Kind = "org"
)

// EnvNetwork is the network of an environment's own services.
func EnvNetwork(environmentID string) string { return envPrefix + environmentID }

// ProjectNetwork is the network of a project's shared services.
func ProjectNetwork(projectID string) string { return projectPrefix + projectID }

// OrgNetwork is the network of an org's shared services.
func OrgNetwork(orgID string) string { return orgPrefix + orgID }

// managedNamePattern is the shape of the names this package builds: a
// prefix and a UUID. A compose stack's own networks
// (paas-<name>-<id8>_<key>) never match, whatever the stack is called.
var managedNamePattern = regexp.MustCompile(`^paas-(env|proj|org)-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsManagedName reports whether name has the shape of a network this
// package names (a prefix and a UUID). The labels, not the name, decide
// whether the platform created it (Network.Matches).
func IsManagedName(name string) bool {
	return managedNamePattern.MatchString(name)
}

// IsReservedName reports whether a network name belongs to the platform:
// any paas- or toft- prefix (toft- after the D-24 rename) or one of legacy,
// the control plane's network names.
func IsReservedName(name string, legacy ...string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(n, "paas-") || strings.HasPrefix(n, "toft-") {
		return true
	}
	for _, l := range legacy {
		if l != "" && n == strings.ToLower(l) {
			return true
		}
	}
	return false
}

// Network is one app network.
type Network struct {
	Name          string
	Kind          Kind
	EnvironmentID string
	ProjectID     string
	OrgID         string
	// Untrusted: an untrusted preview's own network (D-97). Its bridge is
	// named toft-u…, which the host firewall keeps off private addresses
	// and SMTP.
	Untrusted bool
}

// UntrustedBridgePrefix starts the bridge name of an untrusted preview's
// network; firewall.nft matches it.
const UntrustedBridgePrefix = "toft-u"

// BridgeName is the Linux bridge an untrusted network gets (at most 15
// characters): toft-u and the first 8 hex digits of its environment's ID.
// "" lets Docker name it.
func (n Network) BridgeName() string {
	if !n.Untrusted {
		return ""
	}
	id := strings.ReplaceAll(n.EnvironmentID, "-", "")
	return UntrustedBridgePrefix + id[:min(8, len(id))]
}

// Env returns the network of environment envID in project projectID of org
// orgID (orgID may be empty: projects without an org).
func Env(envID, projectID, orgID string) Network {
	return Network{Name: EnvNetwork(envID), Kind: KindEnv, EnvironmentID: envID, ProjectID: projectID, OrgID: orgID}
}

// Project returns the network of project projectID's shared services.
func Project(projectID, orgID string) Network {
	return Network{Name: ProjectNetwork(projectID), Kind: KindProject, ProjectID: projectID, OrgID: orgID}
}

// Org returns the network of org orgID's shared services.
func Org(orgID string) Network {
	return Network{Name: OrgNetwork(orgID), Kind: KindOrg, OrgID: orgID}
}

// Labels returns the labels the platform creates n with. Empty IDs are
// omitted.
func (n Network) Labels() map[string]string {
	l := map[string]string{LabelManaged: "true", LabelKind: string(n.Kind)}
	for k, v := range map[string]string{LabelEnvironmentID: n.EnvironmentID, LabelProjectID: n.ProjectID, LabelOrgID: n.OrgID} {
		if v != "" {
			l[k] = v
		}
	}
	return l
}

// Matches reports whether labels are the labels of a network the platform
// created as n: managed, the same kind and the same ID of what it belongs
// to (the environment, project or org it is named after). The other IDs are
// informational: a project can change orgs (projects.org_id is set by a
// backfill or cleared when an org is deleted), and its environments'
// networks stay theirs. IDs are UUIDs, so the own ID alone identifies the
// owner.
func (n Network) Matches(labels map[string]string) bool {
	if labels[LabelManaged] != "true" || labels[LabelKind] != string(n.Kind) {
		return false
	}
	var key, want string
	switch n.Kind {
	case KindEnv:
		key, want = LabelEnvironmentID, n.EnvironmentID
	case KindProject:
		key, want = LabelProjectID, n.ProjectID
	case KindOrg:
		key, want = LabelOrgID, n.OrgID
	default:
		return false
	}
	return want != "" && labels[key] == want
}

// Valid reports whether n can be created: a managed name that agrees with
// its kind and IDs.
func (n Network) Valid() error {
	var want string
	switch n.Kind {
	case KindEnv:
		if n.EnvironmentID == "" {
			return errors.New("environment network without an environment")
		}
		want = EnvNetwork(n.EnvironmentID)
	case KindProject:
		if n.ProjectID == "" {
			return errors.New("project network without a project")
		}
		want = ProjectNetwork(n.ProjectID)
	case KindOrg:
		if n.OrgID == "" {
			return errors.New("org network without an org")
		}
		want = OrgNetwork(n.OrgID)
	default:
		return fmt.Errorf("unknown network kind %q", n.Kind)
	}
	if n.Name != want {
		return fmt.Errorf("network %q should be named %q", n.Name, want)
	}
	return nil
}

// Endpoint is a service whose running containers are connected to a
// network: single containers with the service's alias, compose containers
// with their compose service name.
type Endpoint struct {
	ServiceID   string
	ServiceName string
	Alias       string
	Compose     bool
}

// Plan is where one service's containers go.
type Plan struct {
	ServiceID   string
	ServiceName string
	// Alias is the service's network alias (services.network_alias).
	Alias   string
	Compose bool
	// Legacy is set under TOFT_APP_NETWORKS=legacy: Home is the control
	// plane's network and nothing is created, attached or published. The
	// deploy then behaves exactly as before app networks existed.
	Legacy bool
	// Home is the service's own network.
	Home Network
	// Publish (shared services only) are the consumer networks the
	// service's containers also join, when they exist on the host with
	// matching labels.
	Publish []Network
	// Attach are the services whose running containers on the same server
	// are connected into Home.
	Attach []Endpoint
}

// LegacyPlan is the plan of every deploy under TOFT_APP_NETWORKS=legacy:
// the service joins network (the control plane's) with its alias, as before.
func LegacyPlan(serviceID, serviceName, alias, network string) Plan {
	if network == "" {
		network = LegacyNetwork
	}
	return Plan{ServiceID: serviceID, ServiceName: serviceName, Alias: alias, Legacy: true, Home: Network{Name: network}}
}

// Valid reports whether p can be used. An isolated plan must have a valid
// managed Home: nothing ever falls back to the control plane's network.
func (p Plan) Valid() error {
	if p.ServiceID == "" {
		return errors.New("network plan without a service")
	}
	if p.Home.Name == "" {
		return errors.New("network plan without a home network")
	}
	if p.Legacy {
		return nil
	}
	if err := p.Home.Valid(); err != nil {
		return fmt.Errorf("network plan: %w", err)
	}
	for _, n := range p.Publish {
		if err := n.Valid(); err != nil {
			return fmt.Errorf("network plan: publish: %w", err)
		}
	}
	return nil
}

// Networks returns the names of every network p puts the service's own
// containers on: Home, then Publish.
func (p Plan) Networks() []string {
	out := []string{p.Home.Name}
	for _, n := range p.Publish {
		out = append(out, n.Name)
	}
	return out
}

// MigrationMode is APP_NETWORK_MIGRATION: what the startup reconcile does
// under TOFT_APP_NETWORKS=isolated. Under legacy it is ignored.
type MigrationMode string

const (
	// MigrationIsolate (the default) creates and attaches every app
	// network, logs the report, then detaches app containers from the
	// control plane's network.
	MigrationIsolate MigrationMode = "isolate"
	// MigrationAttach does everything isolate does except detaching: a
	// staged rollout, nothing breaks.
	MigrationAttach MigrationMode = "attach"
	// MigrationReport changes nothing: it logs what isolate would do (a
	// dry run).
	MigrationReport MigrationMode = "report"
	// MigrationOff skips the startup reconcile. New deploys still use app
	// networks.
	MigrationOff MigrationMode = "off"
)

// ParseMigrationMode parses APP_NETWORK_MIGRATION. Empty means isolate.
func ParseMigrationMode(s string) (MigrationMode, error) {
	switch m := MigrationMode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return MigrationIsolate, nil
	case MigrationIsolate, MigrationAttach, MigrationReport, MigrationOff:
		return m, nil
	}
	return "", fmt.Errorf("APP_NETWORK_MIGRATION=%q: want isolate, attach, report or off", s)
}

// Changes reports whether m changes anything on the hosts.
func (m MigrationMode) Changes() bool { return m == MigrationIsolate || m == MigrationAttach }

// Detaches reports whether m detaches app containers from the control
// plane's network.
func (m MigrationMode) Detaches() bool { return m == MigrationIsolate }

// DefaultAddressPool is the address pool written to daemon.json on fresh
// servers when none is set: /24 networks out of it (D-31).
const DefaultAddressPool = "10.203.0.0/16"

// AddressPoolSize is the prefix length of each network taken from the pool.
const AddressPoolSize = 24

// ParseAddressPool checks an address pool: an IPv4 CIDR no smaller than one
// /24, returned in canonical form.
func ParseAddressPool(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultAddressPool, nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return "", fmt.Errorf("address pool %q: %w", s, err)
	}
	if !p.Addr().Is4() {
		return "", fmt.Errorf("address pool %q: must be IPv4", s)
	}
	if p.Bits() > AddressPoolSize {
		return "", fmt.Errorf("address pool %q: must hold at least one /%d", s, AddressPoolSize)
	}
	return p.Masked().String(), nil
}

// Report is what a reconcile did (or, in report mode, would do) on one host.
type Report struct {
	Created   []string // networks created
	Attached  []string // "<container> → <network>"
	Detached  []string // "<container> ✕ <legacy network>"
	Orphans   []string // containers labelled as a service that has no row (detached in isolate mode)
	Unknown   []string // other containers on the legacy network, never touched
	Removed   []string // empty managed networks no service uses
	CrossRefs []CrossRef
	Errors    []string
}

// Sort sorts every list so reports are stable.
func (r *Report) Sort() {
	for _, l := range []*[]string{&r.Created, &r.Attached, &r.Detached, &r.Orphans, &r.Unknown, &r.Removed, &r.Errors} {
		sort.Strings(*l)
	}
	sort.Slice(r.CrossRefs, func(i, j int) bool {
		a, b := r.CrossRefs[i], r.CrossRefs[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Variable < b.Variable
	})
}
