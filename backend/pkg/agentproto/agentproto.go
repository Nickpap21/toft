// Package agentproto is what toft-agent and the panel agree on (D-106): the
// WebSocket the agent opens, the headers it sends, and the typed SSH
// subsystems the panel runs on it. Inside the WebSocket the agent is the SSH
// server and the panel the SSH client.
package agentproto

// ConnectPath is the WebSocket the agent dials on the panel's origin.
const ConnectPath = "/api/v1/agent/connect"

// Headers on the WebSocket upgrade.
const (
	// HeaderServer names the server the agent runs on (its ID).
	HeaderServer = "X-Toft-Server"
	// HeaderVersion, HeaderArch and HeaderBoot describe the agent: its
	// release, GOARCH and the server's boot ID (a new one re-runs the probe).
	HeaderVersion = "X-Toft-Agent-Version"
	HeaderArch    = "X-Toft-Agent-Arch"
	HeaderBoot    = "X-Toft-Agent-Boot"
	// HeaderKey carries the agent's public key, in authorized_keys form, on
	// every connection (with the join token as a bearer token on a join).
	// The SSH handshake that follows proves the agent holds it.
	HeaderKey = "X-Toft-Agent-Key"
	// HeaderCaps lists what the agent can do beyond the SSH tunnel,
	// space-separated (CapRollout). An older agent sends none.
	HeaderCaps = "X-Toft-Agent-Caps"
)

// CapRollout: the agent runs rollouts itself (SubsystemRollout, D-111).
// CapDesired: it keeps the desired state (SubsystemDesired, D-111).
// CapRoutes: it writes Traefik's routes (SubsystemRoutes, D-113).
// CapMetrics: it samples the server itself and keeps the samples while the
// panel is away (SubsystemMetrics, D-113).
// CapDNS: it answers the name lookups of strict-network containers on
// DNSRelayAddr (D-115).
// CapDeadline: it ends a rollout at the deploy's time limit
// (Request.TimeLeft, D-125), across its own restarts.
// CapHostUpdate: it installs updates, updates Docker and reboots
// (SubsystemHostUpdate, D-134).
// CapContainerCheck: its rollouts create a slot with the service's own
// container check in place of the image's (rollout.Slot.Check); an older
// agent would drop it and keep the image's.
const (
	CapRollout        = "rollout"
	CapDesired        = "desired"
	CapRoutes         = "routes"
	CapMetrics        = "metrics"
	CapDNS            = "dns"
	CapDeadline       = "deadline"
	CapHostUpdate     = "host-update"
	CapContainerCheck = "container-check"
)

// The DNS relay (D-115): gVisor's own network stack can't reach Docker's DNS
// at 127.0.0.11, so a container that runs Sandboxed with a strict network
// gets StrictResolvConf, which the agent writes, mounted read-only over its
// /etc/resolv.conf, and asks the agent on DNSRelayAddr (port 53, UDP and
// TCP, on the dummy interface DNSRelayInterface). The agent answers
// DNSRelayCheckName with DNSRelayAddr to any source, for the probe.
const (
	DNSRelayAddr      = "169.254.53.53"
	DNSRelayInterface = "toft-dns0"
	StrictResolvConf  = "/etc/toft-agent/resolv.conf"
	DNSRelayCheckName = "toft-relay.check."
)

// SSHUser is the user the panel signs in as. The agent accepts only the
// pinned panel key; the name carries nothing.
const SSHUser = "toft"

// Typed requests the agent serves itself, sent as an exec of exactly this
// name (never run by a shell). Each starts with a byte no command does.
const (
	// SubsystemUpgrade: the panel writes one JSON line (UpgradeHeader) and
	// then the binary; the agent verifies, installs, answers "ok" and
	// restarts into it.
	SubsystemUpgrade = "\x00toft-upgrade"
	// SubsystemRotate: the agent makes a new key and answers its public
	// half, keeping it aside. SubsystemRotateCommit (once the panel pinned
	// it) switches to it from the next connection.
	SubsystemRotate       = "\x00toft-rotate"
	SubsystemRotateCommit = "\x00toft-rotate-commit"
	// SubsystemUninstall: the agent removes itself and stops for good.
	SubsystemUninstall = "\x00toft-uninstall"
	// SubsystemRollout: the panel writes one JSON line (rollout.Request).
	// The agent records it, runs it once per deployment ID and one at a
	// time per service, and answers as SubsystemRolloutAttach from the
	// start. The rollout goes on if the panel goes away.
	SubsystemRollout = "\x00toft-rollout"
	// SubsystemRolloutAttach: the panel writes one JSON line
	// (rollout.Attach); the agent answers rollout.Event lines: the
	// rollout's log from that line on, then its result.
	SubsystemRolloutAttach = "\x00toft-rollout-attach"
	// SubsystemRolloutList: the agent answers one JSON line, the rollouts
	// it holds ([]rollout.Summary).
	SubsystemRolloutList = "\x00toft-rollout-list"
	// SubsystemDesired: the panel writes one JSON line (Desired); the
	// agent keeps the newest and answers one JSON line (DesiredAck).
	SubsystemDesired = "\x00toft-desired"
	// SubsystemRoutes: the agent writes every service's route file from
	// its running containers (D-113) and answers one JSON line
	// (RoutesAck). The panel asks before Traefik switches to the files.
	SubsystemRoutes = "\x00toft-routes"
	// SubsystemMetrics: the panel writes one JSON line (MetricsRequest);
	// the agent answers one JSON line per sample it keeps from after
	// Since (MetricsSample), oldest first, a few MiB at most: the panel
	// asks again for the rest (D-113).
	SubsystemMetrics = "\x00toft-metrics"
	// SubsystemHostUpdate: the panel writes one JSON line
	// (HostUpdateRequest); the agent starts the run (internal/hostupdate)
	// as a transient unit that outlives it and answers "started", or
	// "busy" while one runs (D-134).
	SubsystemHostUpdate = "\x00toft-host-update"
	// SubsystemHostUpdateStatus: the agent answers hostupdate.StatusScript's
	// output.
	SubsystemHostUpdateStatus = "\x00toft-host-update-status"
)

// HostUpdateRequest is SubsystemHostUpdate's one JSON line: the run's ID
// and its action (hostupdate.Valid).
type HostUpdateRequest struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

// RoutesRequest is SubsystemRoutes' one JSON line.
type RoutesRequest struct {
	Epoch string `json:"epoch"`
}

// MetricsRequest asks for the samples taken after Since, unix seconds.
type MetricsRequest struct {
	Since int64 `json:"since"`
}

// MetricsSample is one run of the panel's sampling script on the server
// (hoststats.Cmd): its time, unix seconds on the server's clock, and its
// output, which the panel parses as one taken over SSH.
type MetricsSample struct {
	At  int64  `json:"at"`
	Out string `json:"out"`
}

// RoutesAck says how many services the agent's files route.
type RoutesAck struct {
	Services int `json:"services"`
}

// ErrEpochFrozen starts the refusal of an agent that holds another panel
// epoch's state (D-113): the panel was restored from a backup older than
// what runs. It takes no order from the new epoch until the panel sends
// one with Desired.Unfreeze.
const ErrEpochFrozen = "epoch_frozen"

// Desired is what should run on the server (D-111): its services as the
// panel last changed them, numbered by the server's spec_generation.
type Desired struct {
	// Epoch is the panel's (a restored panel has a new one).
	Epoch      string `json:"epoch"`
	Generation int64  `json:"generation"`
	// Services is nil when the panel only asks for the drift against the
	// state the agent holds.
	Services []DesiredService `json:"services"`
	// Unfreeze, with Services, has an agent frozen under another epoch
	// take this one (the panel's adopt or use-the-panel's-state choice).
	Unfreeze bool `json:"unfreeze,omitempty"`
}

// DesiredService is one service on the server. Running with a Deployment
// expects its slot of that deployment, or its compose project, to run.
type DesiredService struct {
	ID         string `json:"id"`
	Compose    bool   `json:"compose,omitempty"`
	Running    bool   `json:"running,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	// Loki is the Loki the agent ships the service's container logs to
	// (D-113): its project's, else its workspace's; "" keeps them on the
	// server.
	Loki string `json:"loki,omitempty"`
}

// DesiredAck is the agent's answer: the generation it holds (0 for none of
// this epoch) and what runs otherwise than it says.
type DesiredAck struct {
	Generation int64   `json:"generation"`
	Drift      []Drift `json:"drift"`
	// Routes is how many services the agent's route files route, nil
	// while Traefik reads the containers' labels (D-113).
	Routes *int `json:"routes,omitempty"`
	// Frozen is set while the agent holds another epoch's state: what
	// runs, for the panel to adopt or not (D-113), [] when nothing does
	// (so no omitempty: null is "not frozen"). Generation and Drift are
	// then empty.
	Frozen []Running `json:"frozen"`
}

// Running is a platform container on the server: a service's slot, or a
// container of a service's compose project (Service is then the service
// ID's first 8 characters and Compose is set).
type Running struct {
	Service    string `json:"service"`
	Compose    bool   `json:"compose,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	Container  string `json:"container"`
	Image      string `json:"image"`
	State      string `json:"state"`
	// Created is when the container was made (Unix seconds).
	Created int64 `json:"created"`
}

// Drift is one difference between the desired state and what runs.
type Drift struct {
	ServiceID string `json:"serviceId,omitempty"`
	Kind      string `json:"kind"`
	Container string `json:"container,omitempty"`
}

// Drift kinds: a running service's containers are missing, stopped or
// restarting over and over; or a platform container runs for a service the
// server doesn't have.
const (
	DriftMissing   = "missing"
	DriftStopped   = "stopped"
	DriftCrashLoop = "crash_loop"
	DriftOrphan    = "orphan"
)

// UpgradeHeader precedes the binary on SubsystemUpgrade.
type UpgradeHeader struct {
	Version string `json:"version"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	// Signature is the release key's ed25519 signature of the binary,
	// base64, which agents before v0.24.0 check. Empty for a development
	// build.
	Signature string `json:"signature"`
	// ManifestSignature is the release key's ed25519 signature, base64, of
	// the manifest line "toft-agent <version> linux <arch> <sha256 hex>\n"
	// (release.yml writes it), so the version is signed with the binary.
	// The agent rebuilds the line from Version, its own architecture and
	// the SHA-256 it computed, and checks that (D-123).
	ManifestSignature string `json:"manifest_signature,omitempty"`
}

// ExitRevoked is the agent's exit status when the panel refuses its key
// (revoked or replaced): systemd's RestartPreventExitStatus keeps it down.
const ExitRevoked = 78

// DockerSocket is the one Unix socket the panel may open on the server.
const DockerSocket = "/var/run/docker.sock"

// Typed reports whether an exec command is one of the typed requests.
func Typed(cmd string) bool {
	switch cmd {
	case SubsystemUpgrade, SubsystemRotate, SubsystemRotateCommit, SubsystemUninstall,
		SubsystemRollout, SubsystemRolloutAttach, SubsystemRolloutList, SubsystemDesired, SubsystemRoutes, SubsystemMetrics,
		SubsystemHostUpdate, SubsystemHostUpdateStatus:
		return true
	}
	return false
}
