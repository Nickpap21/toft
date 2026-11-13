package server

import (
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshclient"
)

// Server Status Constants
const (
	StatusActive       = "active"
	StatusOffline      = "offline"
	StatusProvisioning = "provisioning"
)

// Share lets a workspace other than the owner deploy to a server, within a
// CPU and memory share.
type Share struct {
	OrgID     string    `json:"org_id"`
	OrgName   string    `json:"org_name"`
	CPUCores  float64   `json:"cpu_cores"`
	MemoryMB  int64     `json:"memory_mb"`
	CreatedAt time.Time `json:"created_at"`
}

// SharedServer is a server another workspace shares with this one, as that
// workspace sees it: no address, user or port, and its own share.
type SharedServer struct {
	Server
	OwnerOrgName string
	Share        Share
}

// Server represents an external infrastructure node owned by a user/org.
type Server struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	IPAddress string `json:"ip_address"`
	User      string `json:"user"`
	Port      int    `json:"port"`
	SSHKey    string `json:"ssh_key,omitempty"` // Generally omit sending back the raw key
	Status    string `json:"status"`
	// ProvisionedAt is set once Provision completes successfully; nil means
	// this server has never been provisioned, even if Status is "active"
	// (a bare SSH test-connection also sets Status to "active").
	ProvisionedAt *time.Time `json:"provisioned_at"`
	// TotalCPUCores and TotalMemoryMB are nil when capacity has not yet been discovered via SSH.
	TotalCPUCores    *float64 `json:"total_cpu_cores"`
	TotalMemoryMB    *int64   `json:"total_memory_mb"`
	CPUCoresOverride *float64 `json:"cpu_cores_override,omitempty"`
	MemoryMBOverride *int64   `json:"memory_mb_override,omitempty"`
	// HostKey is the pinned SSH host key in authorized_keys form; "" means
	// not pinned yet (the next connection pins it). It is never sent to
	// clients: ServerResponse carries its type and fingerprint.
	HostKey         string     `json:"-"`
	HostKeyPinnedAt *time.Time `json:"-"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	// Connection is how the panel reaches the server (D-106): ConnectionSSH
	// or ConnectionAgent. AgentKey is the agent's pinned key ("" when no agent
	// has joined); with ConnectionAgent it is the SSH host key the agent's
	// connection must present.
	Connection string `json:"-"`
	AgentKey   string `json:"-"`
	// AgentFrozenAt is set while the server's agent holds another panel
	// epoch's state (D-113): nothing deploys there until someone chooses
	// which state to keep. Read by GetByID only.
	AgentFrozenAt *time.Time `json:"-"`
}

// How the panel reaches a server (servers.connection).
const (
	ConnectionSSH   = "ssh"
	ConnectionAgent = "agent"
)

// WaitingForAgent: Add a server made it (D-153) and no agent has joined, so
// it can run nothing: nothing is placed on it (ErrServerNotReady). The
// frontend's waitingForAgent (components/servers/agent.ts) is the same rule.
func (s Server) WaitingForAgent() bool {
	return s.Connection == ConnectionAgent && s.AgentKey == "" && s.ProvisionedAt == nil
}

// EffectiveCPUCores returns the override if set, otherwise the discovered value (0 if undiscovered).
func (s Server) EffectiveCPUCores() float64 {
	if s.CPUCoresOverride != nil {
		return *s.CPUCoresOverride
	}
	if s.TotalCPUCores != nil {
		return *s.TotalCPUCores
	}
	return 0
}

// EffectiveMemoryMB returns the override if set, otherwise the discovered value (0 if undiscovered).
func (s Server) EffectiveMemoryMB() int64 {
	if s.MemoryMBOverride != nil {
		return *s.MemoryMBOverride
	}
	if s.TotalMemoryMB != nil {
		return *s.TotalMemoryMB
	}
	return 0
}

type CreateServerInput struct {
	Name      string `json:"name" binding:"required,max=100"`
	IPAddress string `json:"ip_address" binding:"required,ip|hostname"`
	User      string `json:"user" binding:"required,max=64"`
	Port      int    `json:"port" binding:"required,min=1,max=65535"`
	SSHKey    string `json:"ssh_key" binding:"required,max=16384"`
}

type UpdateServerStatusInput struct {
	Status string `json:"status" binding:"required,oneof=active offline provisioning"`
}

// SharedServerResponse is a server another workspace shares with the
// caller's: no address, SSH user, port or host key (founders, 2026-10-12),
// and the caller's own share as its capacity.
type SharedServerResponse struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Status        string     `json:"status"`
	ProvisionedAt *time.Time `json:"provisioned_at"`
	// Shared marks it, so a client can tell it from the workspace's own.
	Shared   bool   `json:"shared"`
	SharedBy string `json:"shared_by"`
	// SharedByOrgID is the owning workspace, so a member of it can be
	// offered a switch there to see the server's details.
	SharedByOrgID string  `json:"shared_by_org_id"`
	TotalCPUCores float64 `json:"total_cpu_cores"`
	TotalMemoryMB int64   `json:"total_memory_mb"`
}

func toSharedResponse(s SharedServer) SharedServerResponse {
	return SharedServerResponse{
		ID: s.ID, Name: s.Name, Status: s.Status, ProvisionedAt: s.ProvisionedAt,
		Shared: true, SharedBy: s.OwnerOrgName, SharedByOrgID: s.OrgID, TotalCPUCores: s.Share.CPUCores, TotalMemoryMB: s.Share.MemoryMB,
	}
}

// ServerResponse is the API-safe representation of a Server — SSH key is intentionally excluded.
// The pinned host key appears only as its type and SHA256 fingerprint.
type ServerResponse struct {
	ID               string     `json:"id"`
	OrgID            string     `json:"org_id"`
	UserID           string     `json:"user_id"`
	Name             string     `json:"name"`
	IPAddress        string     `json:"ip_address"`
	User             string     `json:"user"`
	Port             int        `json:"port"`
	Status           string     `json:"status"`
	ProvisionedAt    *time.Time `json:"provisioned_at"`
	TotalCPUCores    float64    `json:"total_cpu_cores"`
	TotalMemoryMB    int64      `json:"total_memory_mb"`
	CPUCoresOverride *float64   `json:"cpu_cores_override,omitempty"`
	MemoryMBOverride *int64     `json:"memory_mb_override,omitempty"`
	// HostKeyType and HostKeyFingerprint ("SHA256:...") describe the pinned
	// SSH host key; both are null while none is pinned. HostKeyFingerprint
	// is also null when a stored key cannot be parsed (HostKeyPinnedAt set).
	HostKeyType        *string    `json:"host_key_type"`
	HostKeyFingerprint *string    `json:"host_key_fingerprint"`
	HostKeyPinnedAt    *time.Time `json:"host_key_pinned_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	// What provisioning sets up for app networks (GET /servers/:id only):
	// "isolated" or "legacy".
	AppNetworks string `json:"app_networks,omitempty"`
	// AddressPool is deprecated (v0.36.0, goes in a later release): the
	// first pool the probe read; /capabilities' address_pools replaces it.
	AddressPool string `json:"address_pool,omitempty"`
	// Connection is "ssh" or "agent" (D-106); Agent is null for an SSH
	// server that never had one. SSHKeyStored says whether a key is kept
	// (a fallback for an agent server).
	Connection   string         `json:"connection"`
	Agent        *AgentResponse `json:"agent"`
	SSHKeyStored bool           `json:"ssh_key_stored"`
	// ProvisionWarnings (GET /servers only): what the last provisioning
	// run, which ended ok, left out; absent when nothing.
	ProvisionWarnings []string `json:"provision_warnings,omitempty"`
}

// UpdateCapacityInput carries the fields for the UpdateCapacity repository method.
type UpdateCapacityInput struct {
	TotalCPUCores    *float64 `json:"total_cpu_cores"`
	TotalMemoryMB    *int64   `json:"total_memory_mb"`
	CPUCoresOverride *float64 `json:"cpu_cores_override"`
	MemoryMBOverride *int64   `json:"memory_mb_override"`
}

func toResponse(s *Server) ServerResponse {
	r := ServerResponse{
		ID:               s.ID,
		OrgID:            s.OrgID,
		UserID:           s.UserID,
		Name:             s.Name,
		IPAddress:        s.IPAddress,
		User:             s.User,
		Port:             s.Port,
		Status:           s.Status,
		ProvisionedAt:    s.ProvisionedAt,
		TotalCPUCores:    s.EffectiveCPUCores(),
		TotalMemoryMB:    s.EffectiveMemoryMB(),
		CPUCoresOverride: s.CPUCoresOverride,
		MemoryMBOverride: s.MemoryMBOverride,
		HostKeyPinnedAt:  s.HostKeyPinnedAt,
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        s.UpdatedAt,
		Connection:       s.Connection,
		SSHKeyStored:     s.SSHKey != "",
	}
	if r.Connection == "" {
		r.Connection = ConnectionSSH
	}
	if s.HostKey != "" {
		if fp, keyType, err := sshclient.Fingerprint(s.HostKey); err == nil {
			r.HostKeyType, r.HostKeyFingerprint = &keyType, &fp
		}
	}
	return r
}

func toResponses(servers []Server) []ServerResponse {
	out := make([]ServerResponse, len(servers))
	for i := range servers {
		out[i] = toResponse(&servers[i])
	}
	return out
}

// ServiceOnServer is the API-safe projection of a service assigned to this server.
type ServiceOnServer struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ProjectName string `json:"project_name"`
	// ProjectID and EnvironmentName are "" for a workspace service; a
	// project service has no environment.
	ProjectID       string `json:"project_id"`
	EnvironmentName string `json:"environment_name"`
	Scope           string `json:"scope"`
	CPULimit        string `json:"cpu_limit"`
	MemoryLimit     string `json:"memory_limit"`
	RuntimeStatus   string `json:"runtime_status"`
	// EffectiveIsolation is its own level or its workspace's default (D-115).
	EffectiveIsolation string `json:"effective_isolation"`
}

// ProvisionRun is a server's last provisioning run (D-48). FinishedAt and
// OK are nil while it runs.
type ProvisionRun struct {
	StartedAt  time.Time  `json:"started_at"`
	StartedBy  string     `json:"started_by"`
	FinishedAt *time.Time `json:"finished_at"`
	OK         *bool      `json:"ok"`
	Error      string     `json:"error"`
	Log        string     `json:"log"`
	// Warnings name what a run that ended ok left out ("the firewall (ufw,
	// fail2ban)"); empty otherwise.
	Warnings []string `json:"warnings"`
	// AddressPoolFix says how to give Docker the app network address pool
	// by hand, when the run left it out (addressPoolFix).
	AddressPoolFix string `json:"address_pool_fix,omitempty"`
}

// ConnectionResult holds the outcome of a test connection attempt.
type ConnectionResult struct {
	Host      string `json:"host"`
	LatencyMS int64  `json:"latency_ms"`
	// The SSH host key the server presented, and whether this connection
	// pinned it (the first connection to a server trusts its key).
	HostKeyType        string `json:"host_key_type"`
	HostKeyFingerprint string `json:"host_key_fingerprint"`
	HostKeyNewlyPinned bool   `json:"host_key_newly_pinned"`
}

// HostKeyScanResponse is what a credential-free handshake with the server
// found, next to the pinned key.
type HostKeyScanResponse struct {
	KeyType              string `json:"key_type"`
	PresentedFingerprint string `json:"presented_fingerprint"`
	// PinnedFingerprint is null while no key is pinned.
	PinnedFingerprint *string `json:"pinned_fingerprint"`
	PinnedKeyType     *string `json:"pinned_key_type"`
	MatchesPinned     bool    `json:"matches_pinned"`
}

// ResetHostKeyInput confirms the fingerprint ("SHA256:...") of the key the
// server presents, as compared on the server's console.
type ResetHostKeyInput struct {
	Fingerprint string `json:"fingerprint" binding:"required,max=128"`
}
