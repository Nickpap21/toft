package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type postgresRepository struct {
	q    *sqlcdb.Queries
	db   *pgxpool.Pool
	keys *crypto.Keys
}

// NewPostgresRepository returns a Repository backed by PostgreSQL.
func NewPostgresRepository(db *pgxpool.Pool, keys *crypto.Keys) Repository {
	return &postgresRepository{q: sqlcdb.New(db), db: db, keys: keys}
}

// serverColumns is the column list every raw-SQL read of a server returns,
// in the order scanServer reads it.
const serverColumns = `id, org_id, user_id, name, ip_address, "user", port, ssh_key, status,
		provisioned_at,
		total_cpu_cores, total_memory_mb, cpu_cores_override, memory_mb_override,
		host_key, host_key_pinned_at,
		created_at, updated_at,
		connection, agent_key`

// scanServer reads one row of serverColumns and decrypts its SSH key.
func (r *postgresRepository) scanServer(row pgx.Row) (*Server, error) {
	var s Server
	var storedKey string
	var hostKey, agentKey *string
	if err := row.Scan(
		&s.ID, &s.OrgID, &s.UserID,
		&s.Name, &s.IPAddress, &s.User,
		&s.Port, &storedKey, &s.Status,
		&s.ProvisionedAt,
		&s.TotalCPUCores, &s.TotalMemoryMB, &s.CPUCoresOverride, &s.MemoryMBOverride,
		&hostKey, &s.HostKeyPinnedAt,
		&s.CreatedAt, &s.UpdatedAt,
		&s.Connection, &agentKey,
	); err != nil {
		return nil, err
	}
	s.SSHKey = r.decryptSSHKey(s.ID, storedKey)
	s.HostKey = derefString(hostKey)
	s.AgentKey = derefString(agentKey)
	return &s, nil
}

// sshKeyAD is where a server's SSH key is sealed for (D-126).
func sshKeyAD(serverID string) string {
	if id, err := uuid.Parse(serverID); err == nil {
		serverID = id.String()
	}
	return crypto.AD("servers.ssh_key", serverID)
}

// encryptSSHKey encrypts a plaintext SSH private key for DB storage.
func (r *postgresRepository) encryptSSHKey(serverID, plain string) (string, error) {
	return r.keys.Seal(sshKeyAD(serverID), plain)
}

// decryptSSHKey decrypts a stored SSH key. Falls back to the raw value if decryption
// fails so that servers created before encryption was added continue to work
// until the startup pass encrypts them (EncryptLegacySSHKeys).
func (r *postgresRepository) decryptSSHKey(serverID, raw string) string {
	if plain, err := r.keys.Open(sshKeyAD(serverID), raw); err == nil {
		return plain
	}
	return raw
}

func (r *postgresRepository) EncryptLegacySSHKeys(ctx context.Context) (int64, error) {
	rows, err := r.q.ListServerSSHKeys(ctx)
	if err != nil {
		return 0, fmt.Errorf("ListServerSSHKeys: %w", err)
	}
	params := sqlcdb.ReplaceServerSSHKeysParams{
		Ids:       make([]pgtype.UUID, 0, len(rows)),
		OldValues: make([]string, 0, len(rows)),
		NewValues: make([]string, 0, len(rows)),
	}
	for _, row := range rows {
		// Only a key in plain text: a value that fails to decrypt for
		// another reason (another ENCRYPTION_KEY) is left as it is.
		if !strings.HasPrefix(row.SshKey, "-----BEGIN") {
			continue
		}
		if _, err := r.keys.Open(sshKeyAD(uuidString(row.ID)), row.SshKey); err == nil {
			continue
		}
		enc, err := r.encryptSSHKey(uuidString(row.ID), row.SshKey)
		if err != nil {
			return 0, fmt.Errorf("encrypt the SSH key of server %s: %w", uuidString(row.ID), err)
		}
		params.Ids = append(params.Ids, row.ID)
		params.OldValues = append(params.OldValues, row.SshKey)
		params.NewValues = append(params.NewValues, enc)
	}
	if len(params.Ids) == 0 {
		return 0, nil
	}
	n, err := r.q.ReplaceServerSSHKeys(ctx, params)
	if err != nil {
		return 0, fmt.Errorf("ReplaceServerSSHKeys: %w", err)
	}
	return n, nil
}

func (r *postgresRepository) Create(ctx context.Context, orgID, userID string, input CreateServerInput) (*Server, error) {
	// The ID is made here, so the key is sealed for it (D-126).
	id := uuid.NewString()
	encryptedKey, err := r.encryptSSHKey(id, input.SSHKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt ssh key: %w", err)
	}

	s, err := r.scanServer(r.db.QueryRow(ctx,
		`INSERT INTO servers (org_id, user_id, name, ip_address, "user", port, ssh_key, status, id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING `+serverColumns,
		orgID, userID,
		input.Name, input.IPAddress, input.User,
		input.Port, encryptedKey, StatusProvisioning, id,
	))
	if isUniqueViolation(err) {
		return nil, ErrServerNameTaken
	}
	if err != nil {
		return nil, fmt.Errorf("create server: %w", err)
	}
	return s, nil
}

func (r *postgresRepository) Rename(ctx context.Context, id, name string) error {
	n, err := r.q.RenameServer(ctx, sqlcdb.RenameServerParams{ID: pgUUID(id), Name: name})
	if isUniqueViolation(err) {
		return ErrServerNameTaken
	}
	if err != nil {
		return fmt.Errorf("rename server: %w", err)
	}
	if n == 0 {
		return ErrServerNotFound
	}
	return nil
}

func (r *postgresRepository) NameFromHostname(ctx context.Context, id, name string) (bool, error) {
	n, err := r.q.NameServerFromHostname(ctx, sqlcdb.NameServerFromHostnameParams{ID: pgUUID(id), Name: name})
	if isUniqueViolation(err) {
		return false, ErrServerNameTaken
	}
	return n == 1, err
}

func (r *postgresRepository) GetByID(ctx context.Context, id string) (*Server, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid server id format: %w", err)
	}
	res, err := r.q.GetServer(ctx, pgtype.UUID{Bytes: uid, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("failed to get server: %w", err)
	}
	s := fromGetServerRow(res)
	s.SSHKey = r.decryptSSHKey(s.ID, s.SSHKey)
	return s, nil
}

func (r *postgresRepository) ListByOrg(ctx context.Context, orgID string) ([]Server, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+serverColumns+`
		 FROM servers
		 WHERE org_id = $1
		 ORDER BY created_at DESC`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list servers by org: %w", err)
	}
	return r.collectServers(rows)
}

func (r *postgresRepository) ListAll(ctx context.Context) ([]Server, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+serverColumns+`
		 FROM servers
		 ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("ListAll servers: %w", err)
	}
	return r.collectServers(rows)
}

// collectServers scans every row of serverColumns and closes rows.
func (r *postgresRepository) collectServers(rows pgx.Rows) ([]Server, error) {
	defer rows.Close()
	servers := make([]Server, 0)
	for rows.Next() {
		s, err := r.scanServer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan server: %w", err)
		}
		servers = append(servers, *s)
	}
	return servers, rows.Err()
}

func (r *postgresRepository) UpdateStatus(ctx context.Context, id string, status string) (*Server, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid server id format: %w", err)
	}
	res, err := r.q.UpdateServerStatus(ctx, sqlcdb.UpdateServerStatusParams{
		ID:     pgtype.UUID{Bytes: uid, Valid: true},
		Status: status,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update server status: %w", err)
	}
	s := fromUpdateServerStatusRow(res)
	s.SSHKey = r.decryptSSHKey(s.ID, s.SSHKey)
	return s, nil
}

// MarkProvisioned records that Provision has completed successfully — distinct
// from UpdateStatus, which a bare SSH test-connection also calls with "active".
func (r *postgresRepository) MarkProvisioned(ctx context.Context, id string) (*Server, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid server id format: %w", err)
	}
	res, err := r.q.MarkServerProvisioned(ctx, pgtype.UUID{Bytes: uid, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("failed to mark server provisioned: %w", err)
	}
	s := fromMarkServerProvisionedRow(res)
	s.SSHKey = r.decryptSSHKey(s.ID, s.SSHKey)
	return s, nil
}

func (r *postgresRepository) UpdateCapacity(ctx context.Context, id string, input UpdateCapacityInput) (*Server, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid server id format: %w", err)
	}
	s, err := r.scanServer(r.db.QueryRow(ctx, `
		UPDATE servers SET
			total_cpu_cores    = COALESCE($2, total_cpu_cores),
			total_memory_mb    = COALESCE($3, total_memory_mb),
			cpu_cores_override = $4,
			memory_mb_override = $5,
			updated_at         = NOW()
		WHERE id = $1
		RETURNING `+serverColumns,
		pgtype.UUID{Bytes: uid, Valid: true},
		input.TotalCPUCores,
		input.TotalMemoryMB,
		input.CPUCoresOverride,
		input.MemoryMBOverride,
	))
	if err != nil {
		return nil, fmt.Errorf("update server capacity: %w", err)
	}
	return s, nil
}

func (r *postgresRepository) UpdateDiscoveredCapacity(ctx context.Context, id string, cpuCores float64, memoryMB int64) (*Server, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid server id format: %w", err)
	}
	n, err := r.q.UpdateServerDiscoveredCapacity(ctx, sqlcdb.UpdateServerDiscoveredCapacityParams{
		ID: pgtype.UUID{Bytes: uid, Valid: true}, TotalCpuCores: &cpuCores, TotalMemoryMb: &memoryMB,
	})
	if err != nil {
		return nil, fmt.Errorf("update discovered server capacity: %w", err)
	}
	if n == 0 {
		return nil, ErrServerNotFound
	}
	return r.GetByID(ctx, id)
}

func (r *postgresRepository) Delete(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid server id format: %w", err)
	}
	if err := r.q.DeleteServer(ctx, pgtype.UUID{Bytes: uid, Valid: true}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.TableName == "shared_volumes" {
			return ErrServerHoldsVolumes
		}
		return fmt.Errorf("failed to delete server: %w", err)
	}
	return nil
}

func (r *postgresRepository) CheckOwnership(ctx context.Context, id, orgID string) (bool, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return false, fmt.Errorf("invalid server id format: %w", err)
	}
	var exists bool
	err = r.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM servers WHERE id = $1 AND org_id = $2)`,
		pgtype.UUID{Bytes: uid, Valid: true}, orgID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("CheckOwnership: %w", err)
	}
	return exists, nil
}

// PinHostKey is a compare-and-set: the UPDATE matches only while host_key
// IS NULL, so of two concurrent first connections exactly one writes and the
// other reads the winner's key back.
func (r *postgresRepository) PinHostKey(ctx context.Context, id, key string) (string, bool, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return "", false, fmt.Errorf("invalid server id format: %w", err)
	}
	pgID := pgtype.UUID{Bytes: uid, Valid: true}
	stored, err := r.q.PinServerHostKey(ctx, sqlcdb.PinServerHostKeyParams{ID: pgID, HostKey: &key})
	if err == nil {
		return derefString(stored), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("pin server host key: %w", err)
	}
	// Already pinned, or no such server.
	stored, err = r.q.GetServerHostKey(ctx, pgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrServerNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("read server host key: %w", err)
	}
	return derefString(stored), false, nil
}

func (r *postgresRepository) HostKey(ctx context.Context, id string) (string, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return "", fmt.Errorf("invalid server id format: %w", err)
	}
	stored, err := r.q.GetServerHostKey(ctx, pgtype.UUID{Bytes: uid, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrServerNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read server host key: %w", err)
	}
	return derefString(stored), nil
}

func (r *postgresRepository) ReplaceHostKey(ctx context.Context, id, key string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid server id format: %w", err)
	}
	n, err := r.q.ReplaceServerHostKey(ctx, sqlcdb.ReplaceServerHostKeyParams{
		ID:      pgtype.UUID{Bytes: uid, Valid: true},
		HostKey: &key,
	})
	if err != nil {
		return fmt.Errorf("replace server host key: %w", err)
	}
	if n == 0 {
		return ErrServerNotFound
	}
	return nil
}

func (r *postgresRepository) ListServicesByServer(ctx context.Context, serverID, orgID string) ([]ServiceOnServer, error) {
	sid, oid, err := pgUUIDs(serverID, orgID)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListOrgServicesOnServer(ctx, sqlcdb.ListOrgServicesOnServerParams{ServerID: sid, OrgID: oid})
	if err != nil {
		return nil, fmt.Errorf("list services by server: %w", err)
	}
	result := make([]ServiceOnServer, 0, len(rows))
	for _, row := range rows {
		result = append(result, ServiceOnServer{
			ID: uuidString(row.ID), Name: row.Name, ProjectName: row.ProjectName, ProjectID: uuidString(row.ProjectID),
			EnvironmentName: row.EnvironmentName, Scope: row.Scope,
			CPULimit: row.CpuLimit, MemoryLimit: row.MemoryLimit, RuntimeStatus: row.RuntimeStatus,
			EffectiveIsolation: row.EffectiveIsolation,
		})
	}
	return result, nil
}

// StartProvisionRun records a new run of the server's provisioning,
// replacing the last one; startedBy "" when not known.
func (r *postgresRepository) StartProvisionRun(ctx context.Context, serverID, startedBy string) error {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return fmt.Errorf("invalid server id: %w", err)
	}
	by := pgtype.UUID{}
	if u, err := uuid.Parse(startedBy); err == nil {
		by = pgtype.UUID{Bytes: u, Valid: true}
	}
	if err := r.q.StartProvisionRun(ctx, sqlcdb.StartProvisionRunParams{ServerID: pgtype.UUID{Bytes: sid, Valid: true}, StartedBy: by}); err != nil {
		return fmt.Errorf("start provision run: %w", err)
	}
	return nil
}

func (r *postgresRepository) AppendProvisionLog(ctx context.Context, serverID, line string) error {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return fmt.Errorf("invalid server id: %w", err)
	}
	if err := r.q.AppendProvisionLog(ctx, sqlcdb.AppendProvisionLogParams{ServerID: pgtype.UUID{Bytes: sid, Valid: true}, Line: line}); err != nil {
		return fmt.Errorf("append provision log: %w", err)
	}
	return nil
}

func (r *postgresRepository) FinishProvisionRun(ctx context.Context, serverID string, ok bool, errMsg string, warnings []string) error {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return fmt.Errorf("invalid server id: %w", err)
	}
	if warnings == nil {
		warnings = []string{} // nil would be NULL
	}
	if err := r.q.FinishProvisionRun(ctx, sqlcdb.FinishProvisionRunParams{ServerID: pgtype.UUID{Bytes: sid, Valid: true}, Ok: &ok, Error: errMsg, Warnings: warnings}); err != nil {
		return fmt.Errorf("finish provision run: %w", err)
	}
	return nil
}

func (r *postgresRepository) CloseUnfinishedProvisionRuns(ctx context.Context) (int64, error) {
	n, err := r.q.CloseUnfinishedProvisionRuns(ctx)
	if err != nil {
		return 0, fmt.Errorf("close unfinished provision runs: %w", err)
	}
	return n, nil
}

// GetProvisionRun returns the server's last provisioning run, or
// ErrNoProvisionRun.
func (r *postgresRepository) GetProvisionRun(ctx context.Context, serverID string) (*ProvisionRun, error) {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return nil, fmt.Errorf("invalid server id: %w", err)
	}
	row, err := r.q.GetProvisionRun(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoProvisionRun
	}
	if err != nil {
		return nil, fmt.Errorf("get provision run: %w", err)
	}
	run := &ProvisionRun{StartedAt: row.StartedAt.Time, StartedBy: row.StartedByName, OK: row.Ok, Error: row.Error, Log: row.Log, Warnings: row.Warnings}
	if run.Warnings == nil {
		run.Warnings = []string{}
	}
	if row.FinishedAt.Valid {
		t := row.FinishedAt.Time
		run.FinishedAt = &t
	}
	return run, nil
}

func (r *postgresRepository) ListProvisionWarnings(ctx context.Context, orgID string) (map[string][]string, error) {
	rows, err := r.q.ListProvisionWarningsByOrg(ctx, pgUUID(orgID))
	if err != nil {
		return nil, fmt.Errorf("list provision warnings: %w", err)
	}
	out := make(map[string][]string, len(rows))
	for _, row := range rows {
		out[uuidString(row.ServerID)] = row.Warnings
	}
	return out, nil
}

// ─── Mappers ──────────────────────────────────────────────────────────────────

// derefFloat64 safely dereferences a nullable float64 pointer to a value type.
func derefFloat64(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// derefString dereferences a nullable string, "" for NULL.
func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// derefInt64 safely dereferences a nullable int64 pointer to a value type.
func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func fromGetServerRow(db sqlcdb.GetServerRow) *Server {
	return &Server{
		ID:               uuidString(db.ID),
		OrgID:            uuidString(db.OrgID),
		UserID:           uuidString(db.UserID),
		Name:             db.Name,
		IPAddress:        db.IpAddress,
		User:             db.User,
		Port:             int(db.Port),
		SSHKey:           db.SshKey,
		Status:           db.Status,
		ProvisionedAt:    pgTimeToPtr(db.ProvisionedAt),
		TotalCPUCores:    db.TotalCpuCores,
		TotalMemoryMB:    db.TotalMemoryMb,
		CPUCoresOverride: db.CpuCoresOverride,
		MemoryMBOverride: db.MemoryMbOverride,
		HostKey:          derefString(db.HostKey),
		HostKeyPinnedAt:  pgTimeToPtr(db.HostKeyPinnedAt),
		CreatedAt:        db.CreatedAt.Time,
		UpdatedAt:        db.UpdatedAt.Time,
		Connection:       db.Connection,
		AgentKey:         derefString(db.AgentKey),
		AgentFrozenAt:    pgTimeToPtr(db.AgentFrozenAt),
	}
}

func fromUpdateServerStatusRow(db sqlcdb.UpdateServerStatusRow) *Server {
	return &Server{
		ID:               uuidString(db.ID),
		OrgID:            uuidString(db.OrgID),
		UserID:           uuidString(db.UserID),
		Name:             db.Name,
		IPAddress:        db.IpAddress,
		User:             db.User,
		Port:             int(db.Port),
		SSHKey:           db.SshKey,
		Status:           db.Status,
		ProvisionedAt:    pgTimeToPtr(db.ProvisionedAt),
		TotalCPUCores:    db.TotalCpuCores,
		TotalMemoryMB:    db.TotalMemoryMb,
		CPUCoresOverride: db.CpuCoresOverride,
		MemoryMBOverride: db.MemoryMbOverride,
		HostKey:          derefString(db.HostKey),
		HostKeyPinnedAt:  pgTimeToPtr(db.HostKeyPinnedAt),
		CreatedAt:        db.CreatedAt.Time,
		UpdatedAt:        db.UpdatedAt.Time,
		Connection:       db.Connection,
		AgentKey:         derefString(db.AgentKey),
	}
}

func fromMarkServerProvisionedRow(db sqlcdb.MarkServerProvisionedRow) *Server {
	return &Server{
		ID:               uuidString(db.ID),
		OrgID:            uuidString(db.OrgID),
		UserID:           uuidString(db.UserID),
		Name:             db.Name,
		IPAddress:        db.IpAddress,
		User:             db.User,
		Port:             int(db.Port),
		SSHKey:           db.SshKey,
		Status:           db.Status,
		ProvisionedAt:    pgTimeToPtr(db.ProvisionedAt),
		TotalCPUCores:    db.TotalCpuCores,
		TotalMemoryMB:    db.TotalMemoryMb,
		CPUCoresOverride: db.CpuCoresOverride,
		MemoryMBOverride: db.MemoryMbOverride,
		HostKey:          derefString(db.HostKey),
		HostKeyPinnedAt:  pgTimeToPtr(db.HostKeyPinnedAt),
		CreatedAt:        db.CreatedAt.Time,
		UpdatedAt:        db.UpdatedAt.Time,
		Connection:       db.Connection,
		AgentKey:         derefString(db.AgentKey),
	}
}

func uuidString(p pgtype.UUID) string {
	val, _ := p.Value()
	if val == nil {
		return ""
	}
	return val.(string)
}

// pgTimeToPtr converts a nullable pgtype.Timestamptz to *time.Time (nil when not valid/set).
func pgTimeToPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

// ─── DNS provider ─────────────────────────────────────────────────────────────

// dnsAD is where a server's DNS credentials are sealed for (D-126).
func dnsAD(serverID pgtype.UUID) string {
	return crypto.AD("server_dns_providers.credentials", uuidString(serverID))
}

func (r *postgresRepository) dnsConfig(row sqlcdb.ServerDnsProvider) (*DNSConfig, error) {
	plain, err := r.keys.Open(dnsAD(row.ServerID), row.Credentials)
	if err != nil {
		return nil, fmt.Errorf("decrypt DNS credentials: %w", err)
	}
	var creds map[string]string
	if err := json.Unmarshal([]byte(plain), &creds); err != nil {
		return nil, fmt.Errorf("decode DNS credentials: %w", err)
	}
	return &DNSConfig{Provider: row.Provider, Credentials: creds, UpdatedAt: row.UpdatedAt.Time}, nil
}

func (r *postgresRepository) GetDNSProvider(ctx context.Context, serverID string) (*DNSConfig, error) {
	id, err := uuid.Parse(serverID)
	if err != nil {
		return nil, fmt.Errorf("invalid server id %q: %w", serverID, err)
	}
	row, err := r.q.GetServerDNSProvider(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoDNSProvider
	}
	if err != nil {
		return nil, fmt.Errorf("GetServerDNSProvider: %w", err)
	}
	return r.dnsConfig(row)
}

func (r *postgresRepository) SetDNSProvider(ctx context.Context, serverID, provider string, creds map[string]string) (*DNSConfig, error) {
	id, err := uuid.Parse(serverID)
	if err != nil {
		return nil, fmt.Errorf("invalid server id %q: %w", serverID, err)
	}
	raw, err := json.Marshal(creds)
	if err != nil {
		return nil, err
	}
	enc, err := r.keys.Seal(dnsAD(pgtype.UUID{Bytes: id, Valid: true}), string(raw))
	if err != nil {
		return nil, fmt.Errorf("encrypt DNS credentials: %w", err)
	}
	row, err := r.q.UpsertServerDNSProvider(ctx, sqlcdb.UpsertServerDNSProviderParams{
		ServerID: pgtype.UUID{Bytes: id, Valid: true}, Provider: provider, Credentials: enc,
	})
	if err != nil {
		return nil, fmt.Errorf("UpsertServerDNSProvider: %w", err)
	}
	return r.dnsConfig(row)
}

func (r *postgresRepository) DeleteDNSProvider(ctx context.Context, serverID string) error {
	id, err := uuid.Parse(serverID)
	if err != nil {
		return fmt.Errorf("invalid server id %q: %w", serverID, err)
	}
	if err := r.q.DeleteServerDNSProvider(ctx, pgtype.UUID{Bytes: id, Valid: true}); err != nil {
		return fmt.Errorf("DeleteServerDNSProvider: %w", err)
	}
	return nil
}
