package server

import "context"

// Repository defines the data access contract for the server domain.
type Repository interface {
	// EncryptLegacySSHKeys encrypts the SSH private keys stored in plain text
	// before encryption was added, and returns how many there were.
	EncryptLegacySSHKeys(ctx context.Context) (int64, error)
	// DNS provider (wildcard certificates). GetDNSProvider returns it
	// decrypted, or ErrNoDNSProvider.
	GetDNSProvider(ctx context.Context, serverID string) (*DNSConfig, error)
	SetDNSProvider(ctx context.Context, serverID, provider string, creds map[string]string) (*DNSConfig, error)
	DeleteDNSProvider(ctx context.Context, serverID string) error

	// Backup destinations (D-43); serverID "" is the panel host.
	// GetBackupDestination returns it decrypted, or ErrNoBackupDestination.
	GetBackupDestination(ctx context.Context, serverID string) (*BackupDestination, error)
	ListBackupDestinations(ctx context.Context) ([]BackupDestination, error)
	CreateBackupDestination(ctx context.Context, serverID string, in BackupInput, repoPassword string) (*BackupDestination, error)
	// UpdateBackupDestination keeps the secret key when in.SecretKey is "".
	UpdateBackupDestination(ctx context.Context, id string, in BackupInput) (*BackupDestination, error)
	DeleteBackupDestination(ctx context.Context, id string) error
	RecordBackupRun(ctx context.Context, id string, run BackupRun) error

	Create(ctx context.Context, orgID, userID string, input CreateServerInput) (*Server, error)
	GetByID(ctx context.Context, id string) (*Server, error)
	ListByOrg(ctx context.Context, orgID string) ([]Server, error)
	ListAll(ctx context.Context) ([]Server, error)
	UpdateStatus(ctx context.Context, id string, status string) (*Server, error)
	MarkProvisioned(ctx context.Context, id string) (*Server, error)
	UpdateCapacity(ctx context.Context, id string, input UpdateCapacityInput) (*Server, error)
	// UpdateDiscoveredCapacity stores the capacity read from the server,
	// leaving the manual overrides as they are.
	UpdateDiscoveredCapacity(ctx context.Context, id string, cpuCores float64, memoryMB int64) (*Server, error)
	Delete(ctx context.Context, id string) error
	// Rename sets a new name (ErrServerNameTaken when the workspace has it);
	// NameFromHostname sets it only while the name is the automatic one
	// (D-153), reporting whether it did.
	Rename(ctx context.Context, id, name string) error
	NameFromHostname(ctx context.Context, id, name string) (bool, error)
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
	// ListServicesByServer lists orgID's services on the server.
	ListServicesByServer(ctx context.Context, serverID, orgID string) ([]ServiceOnServer, error)
	// The last provisioning run (D-48). GetProvisionRun returns
	// ErrNoProvisionRun when the server was never provisioned since 0029.
	StartProvisionRun(ctx context.Context, serverID, startedBy string) error
	AppendProvisionLog(ctx context.Context, serverID, line string) error
	FinishProvisionRun(ctx context.Context, serverID string, ok bool, errMsg string, warnings []string) error
	CloseUnfinishedProvisionRuns(ctx context.Context) (int64, error)
	GetProvisionRun(ctx context.Context, serverID string) (*ProvisionRun, error)
	// ListProvisionWarnings maps each of orgID's servers whose last run
	// ended ok with warnings to them.
	ListProvisionWarnings(ctx context.Context, orgID string) (map[string][]string, error)
	// PinHostKey stores key (authorized_keys form) as the server's SSH host
	// key only if none is pinned yet (compare-and-set). It returns the key
	// pinned afterwards and whether this call wrote it; when another key was
	// pinned first, stored is that key and wrote is false.
	PinHostKey(ctx context.Context, id, key string) (stored string, wrote bool, err error)
	// HostKey returns the server's pinned SSH host key ("" when none is
	// pinned). ErrServerNotFound if no such server.
	HostKey(ctx context.Context, id string) (string, error)
	// ReplaceHostKey sets the server's pinned SSH host key unconditionally
	// (an instance operator confirmed it). ErrServerNotFound if no such server.
	ReplaceHostKey(ctx context.Context, id, key string) error
	// Shares (shared servers).
	ListShares(ctx context.Context, serverID string) ([]Share, error)
	// GetShare returns a workspace's share of a server, or ErrNoShare.
	GetShare(ctx context.Context, serverID, orgID string) (*Share, error)
	UpsertShare(ctx context.Context, serverID, orgID string, cpuCores float64, memoryMB int64) error
	DeleteShare(ctx context.Context, serverID, orgID string) (bool, error)
	// ListSharedWithOrg returns the servers other workspaces share with orgID.
	ListSharedWithOrg(ctx context.Context, orgID string) ([]SharedServer, error)
	// CountOrgServicesOnServer counts a workspace's services on a server.
	CountOrgServicesOnServer(ctx context.Context, serverID, orgID string) (int64, error)
	// CountServicesOnServer counts every service assigned to a server.
	CountServicesOnServer(ctx context.Context, serverID string) (int64, error)
	// CountSharedServicesOnServer counts the services on a server of the
	// workspaces it is shared with.
	CountSharedServicesOnServer(ctx context.Context, serverID string) (int64, error)
}
