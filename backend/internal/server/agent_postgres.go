package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/ssh"
)

// AgentRecord is what the database holds about a server's agent (D-106).
type AgentRecord struct {
	Connection     string
	Key            string
	NextKey        string // a rotation's new key, admitted beside Key
	KeyPinnedAt    *time.Time
	Version        string
	Arch           string
	BootID         string
	ConnectedAt    *time.Time
	LastSeenAt     *time.Time
	FromIP         string
	RevokedAt      *time.Time
	TokenExpiresAt *time.Time
	// The desired state (D-111): its generations, and the drift the agent
	// last reported (agentproto.Drift JSON) at DriftAt.
	SpecGeneration     int64
	ObservedGeneration int64
	Drift              []byte
	DriftAt            *time.Time
	// Routes is how many services the agent's route files route, nil while
	// Traefik reads labels (D-113). FrozenAt is set while the agent holds
	// another panel epoch's state, and Frozen is then what runs there
	// (agentproto.Running JSON).
	Routes   *int
	FrozenAt *time.Time
	Frozen   []byte
}

// AgentStore persists agents, join tokens and the panel's agent key.
type AgentStore interface {
	CreateAgentServer(ctx context.Context, orgID, userID, name, ip string, automatic bool) (string, error)
	// Add a server (D-153): the next automatic name's n, the caller's own
	// waiting server ("" when none), who made the command an agent joined
	// with ("" when none), the waiting server's SSH key (decrypted, "" when
	// none was made; ErrServerJoined when it isn't waiting), making it once,
	// moving it to SSH, and removing the unused ones.
	NextServerNumber(ctx context.Context, orgID string) (int, error)
	FindWaitingServer(ctx context.Context, orgID, userID string) (string, error)
	LastJoinTokenCreator(ctx context.Context, serverID string) (string, error)
	WaitingSSHKey(ctx context.Context, serverID string) (string, error)
	SetWaitingSSHKey(ctx context.Context, serverID, key string) (bool, error)
	UseSSH(ctx context.Context, serverID, address string, port int, key string) error
	DeleteUnjoinedServers(ctx context.Context) ([]RemovedServer, error)
	// CreateJoinToken stores a token's hash and deletes the server's older
	// unused tokens.
	CreateJoinToken(ctx context.Context, serverID string, hash []byte, replaces bool, createdBy string, expires time.Time) error
	// JoinWithToken consumes a token and pins key in one transaction:
	// ErrUnknownToken when the token is unknown, used or expired. pinned is
	// false for a first join while another key is pinned (the token stays
	// unused then).
	JoinWithToken(ctx context.Context, hash []byte, key string) (serverID string, replaces bool, createdBy string, pinned bool, err error)
	PeekJoinToken(ctx context.Context, hash []byte) (serverID string, err error)
	// PinAgentKey pins key; without replace only while none is pinned.
	PinAgentKey(ctx context.Context, serverID, key string, replace bool) (bool, error)
	// SetAgentNextKey keeps a rotation's new key beside the pinned one;
	// PromoteAgentNextKey pins it once the agent presents it.
	SetAgentNextKey(ctx context.Context, serverID, key string) error
	PromoteAgentNextKey(ctx context.Context, serverID, key string) (bool, error)
	RevokeAgent(ctx context.Context, serverID string) (bool, error)
	AgentKey(ctx context.Context, serverID string) (string, error)
	RecordAgentConnected(ctx context.Context, serverID, version, arch, bootID, fromIP string) error
	TouchAgentSeen(ctx context.Context, serverID string) error
	GetAgent(ctx context.Context, serverID string) (*AgentRecord, error)
	ListAgents(ctx context.Context, orgID string) (map[string]AgentRecord, error)
	SetConnection(ctx context.Context, serverID, mode string) error
	// StrictServices lists the server's services that run Sandboxed with
	// a strict network (D-115).
	StrictServices(ctx context.Context, serverID string) ([]NamedService, error)
	// SandboxStrictPreviews moves the server's strict preview copies to
	// Sandboxed, for a server that no longer offers the strict network.
	SandboxStrictPreviews(ctx context.Context, serverID string) error
	SetSSHCredentials(ctx context.Context, serverID, user string, port int, key string) error
	SetAddressIfEmpty(ctx context.Context, serverID, ip string) error
	ListAgentServerIDs(ctx context.Context) ([]string, error)
	// PanelEpoch returns the panel's epoch (D-111), made on first use.
	PanelEpoch(ctx context.Context) (string, error)
	// AgentGeneration returns the server's spec and observed generations;
	// RecordAgentObserved stores its agent's answer to the desired state.
	AgentGeneration(ctx context.Context, serverID string) (spec, observed int64, err error)
	RecordAgentObserved(ctx context.Context, serverID string, generation int64, drift []byte, routes *int) error
	// RecordAgentFrozen stores a frozen agent's answer (D-113): what runs.
	RecordAgentFrozen(ctx context.Context, serverID string, running []byte, routes *int) error
	// AgentsFrozen lists every agent server and whether its agent is frozen.
	AgentsFrozen(ctx context.Context) (map[string]bool, error)
	// PanelAgentKey returns the panel's ed25519 key (PEM), made on first use.
	PanelAgentKey(ctx context.Context) (string, error)
}

// ErrUnknownToken: the join token is unknown, used or expired.
var ErrUnknownToken = errors.New("the join token is unknown, used or expired")

// NewPostgresAgentStore returns the AgentStore on PostgreSQL.
func NewPostgresAgentStore(db *pgxpool.Pool, keys *crypto.Keys) AgentStore {
	return &postgresRepository{q: sqlcdb.New(db), db: db, keys: keys}
}

func (r *postgresRepository) CreateAgentServer(ctx context.Context, orgID, userID, name, ip string, automatic bool) (string, error) {
	id, err := r.q.CreateAgentServer(ctx, sqlcdb.CreateAgentServerParams{
		OrgID: pgUUID(orgID), UserID: pgUUID(userID), Name: name, IpAddress: ip, NameAutomatic: automatic,
	})
	if isUniqueViolation(err) {
		return "", ErrServerNameTaken
	}
	if err != nil {
		return "", fmt.Errorf("create server: %w", err)
	}
	return uuidString(id), nil
}

// RemovedServer is a waiting server the sweep removed.
type RemovedServer struct{ ID, OrgID, Name string }

func (r *postgresRepository) NextServerNumber(ctx context.Context, orgID string) (int, error) {
	n, err := r.q.NextAutomaticServerNumber(ctx, pgUUID(orgID))
	return int(n), err
}

func (r *postgresRepository) FindWaitingServer(ctx context.Context, orgID, userID string) (string, error) {
	id, err := r.q.FindWaitingAgentServer(ctx, sqlcdb.FindWaitingAgentServerParams{OrgID: pgUUID(orgID), UserID: pgUUID(userID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return uuidString(id), err
}

func (r *postgresRepository) LastJoinTokenCreator(ctx context.Context, serverID string) (string, error) {
	by, err := r.q.LastJoinTokenCreator(ctx, pgUUID(serverID))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return uuidString(by), err
}

func (r *postgresRepository) WaitingSSHKey(ctx context.Context, serverID string) (string, error) {
	raw, err := r.q.GetWaitingServerSSHKey(ctx, pgUUID(serverID))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrServerJoined
	}
	if err != nil || raw == "" {
		return "", err
	}
	return r.keys.Open(sshKeyAD(serverID), raw)
}

func (r *postgresRepository) SetWaitingSSHKey(ctx context.Context, serverID, key string) (bool, error) {
	sealed, err := r.encryptSSHKey(serverID, key)
	if err != nil {
		return false, fmt.Errorf("encrypt ssh key: %w", err)
	}
	n, err := r.q.SetWaitingServerSSHKey(ctx, sqlcdb.SetWaitingServerSSHKeyParams{ID: pgUUID(serverID), SshKey: sealed})
	return n == 1, err
}

// UseSSH moves the waiting server to SSH and expires its join tokens, in
// one transaction, so no command joins a server reached over SSH.
func (r *postgresRepository) UseSSH(ctx context.Context, serverID, address string, port int, key string) error {
	sealed, err := r.encryptSSHKey(serverID, key)
	if err != nil {
		return fmt.Errorf("encrypt ssh key: %w", err)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)
	n, err := q.UseSSHForWaitingServer(ctx, sqlcdb.UseSSHForWaitingServerParams{ID: pgUUID(serverID), IpAddress: address, Port: int32(port), SshKey: sealed})
	if err != nil {
		return fmt.Errorf("use SSH: %w", err)
	}
	if n == 0 {
		return ErrServerJoined
	}
	if err := q.ExpireServerJoinTokens(ctx, pgUUID(serverID)); err != nil {
		return fmt.Errorf("expire join tokens: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *postgresRepository) DeleteUnjoinedServers(ctx context.Context) ([]RemovedServer, error) {
	rows, err := r.q.DeleteUnjoinedServers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RemovedServer, 0, len(rows))
	for _, g := range rows {
		out = append(out, RemovedServer{ID: uuidString(g.ID), OrgID: uuidString(g.OrgID), Name: g.Name})
	}
	return out, nil
}

// isUniqueViolation: servers' one unique constraint besides its key is
// (org_id, name).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (r *postgresRepository) CreateJoinToken(ctx context.Context, serverID string, hash []byte, replaces bool, createdBy string, expires time.Time) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)
	if err := q.DeleteUnusedServerJoinTokens(ctx, pgUUID(serverID)); err != nil {
		return fmt.Errorf("delete older tokens: %w", err)
	}
	if err := q.CreateServerJoinToken(ctx, sqlcdb.CreateServerJoinTokenParams{
		ServerID: pgUUID(serverID), TokenHash: hash, ReplacesKey: replaces,
		CreatedBy: pgUUID(createdBy), ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
	}); err != nil {
		return fmt.Errorf("store the join token: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *postgresRepository) JoinWithToken(ctx context.Context, hash []byte, key string) (string, bool, string, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", false, "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := r.q.WithTx(tx)
	row, err := q.UseServerJoinToken(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, "", false, ErrUnknownToken
	}
	if err != nil {
		return "", false, "", false, err
	}
	serverID, createdBy := uuidString(row.ServerID), uuidString(row.CreatedBy)
	n, err := q.PinServerAgentKey(ctx, sqlcdb.PinServerAgentKeyParams{ID: row.ServerID, AgentKey: &key, Replace: row.ReplacesKey})
	if err != nil {
		return "", false, "", false, fmt.Errorf("pin the agent's key: %w", err)
	}
	if n == 0 {
		return serverID, row.ReplacesKey, createdBy, false, nil
	}
	return serverID, row.ReplacesKey, createdBy, true, tx.Commit(ctx)
}

func (r *postgresRepository) PeekJoinToken(ctx context.Context, hash []byte) (string, error) {
	id, err := r.q.PeekServerJoinToken(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUnknownToken
	}
	if err != nil {
		return "", err
	}
	return uuidString(id), nil
}

func (r *postgresRepository) PinAgentKey(ctx context.Context, serverID, key string, replace bool) (bool, error) {
	n, err := r.q.PinServerAgentKey(ctx, sqlcdb.PinServerAgentKeyParams{ID: pgUUID(serverID), AgentKey: &key, Replace: replace})
	return n > 0, err
}

func (r *postgresRepository) SetAgentNextKey(ctx context.Context, serverID, key string) error {
	n, err := r.q.SetServerAgentNextKey(ctx, sqlcdb.SetServerAgentNextKeyParams{ID: pgUUID(serverID), AgentNextKey: &key})
	if err == nil && n == 0 {
		return ErrNoAgent
	}
	return err
}

func (r *postgresRepository) PromoteAgentNextKey(ctx context.Context, serverID, key string) (bool, error) {
	n, err := r.q.PromoteServerAgentNextKey(ctx, sqlcdb.PromoteServerAgentNextKeyParams{ID: pgUUID(serverID), Key: key})
	return n > 0, err
}

func (r *postgresRepository) RevokeAgent(ctx context.Context, serverID string) (bool, error) {
	n, err := r.q.RevokeServerAgent(ctx, pgUUID(serverID))
	return n > 0, err
}

func (r *postgresRepository) AgentKey(ctx context.Context, serverID string) (string, error) {
	k, err := r.q.GetServerAgentKey(ctx, pgUUID(serverID))
	if err != nil {
		return "", err
	}
	return derefString(k), nil
}

func (r *postgresRepository) RecordAgentConnected(ctx context.Context, serverID, version, arch, bootID, fromIP string) error {
	return r.q.RecordAgentConnected(ctx, sqlcdb.RecordAgentConnectedParams{
		ID: pgUUID(serverID), AgentVersion: &version, AgentArch: &arch, AgentBootID: &bootID, AgentFromIp: &fromIP,
	})
}

func (r *postgresRepository) TouchAgentSeen(ctx context.Context, serverID string) error {
	return r.q.TouchAgentSeen(ctx, pgUUID(serverID))
}

func (r *postgresRepository) GetAgent(ctx context.Context, serverID string) (*AgentRecord, error) {
	row, err := r.q.GetServerAgent(ctx, pgUUID(serverID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrServerNotFound
	}
	if err != nil {
		return nil, err
	}
	rec := agentRecord(sqlcdb.ListServerAgentsByOrgRow(row))
	return &rec, nil
}

func (r *postgresRepository) ListAgents(ctx context.Context, orgID string) (map[string]AgentRecord, error) {
	rows, err := r.q.ListServerAgentsByOrg(ctx, pgUUID(orgID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]AgentRecord, len(rows))
	for _, row := range rows {
		out[uuidString(row.ID)] = agentRecord(row)
	}
	return out, nil
}

func agentRecord(row sqlcdb.ListServerAgentsByOrgRow) AgentRecord {
	return AgentRecord{
		Connection: row.Connection, Key: derefString(row.AgentKey), NextKey: derefString(row.AgentNextKey), KeyPinnedAt: pgTimeToPtr(row.AgentKeyPinnedAt),
		Version: derefString(row.AgentVersion), Arch: derefString(row.AgentArch), BootID: derefString(row.AgentBootID),
		ConnectedAt: pgTimeToPtr(row.AgentConnectedAt), LastSeenAt: pgTimeToPtr(row.AgentLastSeenAt),
		FromIP: derefString(row.AgentFromIp), RevokedAt: pgTimeToPtr(row.AgentRevokedAt), TokenExpiresAt: pgTimeToPtr(row.TokenExpiresAt),
		SpecGeneration: row.SpecGeneration, ObservedGeneration: row.ObservedGeneration, Drift: row.AgentDrift, DriftAt: pgTimeToPtr(row.AgentDriftAt),
		Routes: intFromInt32(row.AgentRoutes), FrozenAt: pgTimeToPtr(row.AgentFrozenAt), Frozen: row.AgentFrozen,
	}
}

func (r *postgresRepository) SetConnection(ctx context.Context, serverID, mode string) error {
	n, err := r.q.SetServerConnection(ctx, sqlcdb.SetServerConnectionParams{ID: pgUUID(serverID), Connection: mode})
	if err == nil && n == 0 {
		return ErrServerNotFound
	}
	return err
}

func (r *postgresRepository) StrictServices(ctx context.Context, serverID string) ([]NamedService, error) {
	rows, err := r.q.ListStrictServicesOnServer(ctx, pgUUID(serverID))
	if err != nil {
		return nil, fmt.Errorf("ListStrictServicesOnServer: %w", err)
	}
	out := make([]NamedService, 0, len(rows))
	for _, row := range rows {
		out = append(out, NamedService{ID: uuidString(row.ID), Name: row.Name})
	}
	return out, nil
}

func (r *postgresRepository) SandboxStrictPreviews(ctx context.Context, serverID string) error {
	if err := r.q.SandboxStrictPreviewCopies(ctx, pgUUID(serverID)); err != nil {
		return fmt.Errorf("SandboxStrictPreviewCopies: %w", err)
	}
	return nil
}

func (r *postgresRepository) SetSSHCredentials(ctx context.Context, serverID, user string, port int, key string) error {
	enc, err := r.encryptSSHKey(serverID, key)
	if err != nil {
		return fmt.Errorf("encrypt ssh key: %w", err)
	}
	n, err := r.q.SetServerSSHCredentials(ctx, sqlcdb.SetServerSSHCredentialsParams{ID: pgUUID(serverID), User: user, Port: int32(port), SshKey: enc})
	if err == nil && n == 0 {
		return ErrServerNotFound
	}
	return err
}

func (r *postgresRepository) SetAddressIfEmpty(ctx context.Context, serverID, ip string) error {
	return r.q.SetServerAddressIfEmpty(ctx, sqlcdb.SetServerAddressIfEmptyParams{ID: pgUUID(serverID), IpAddress: ip})
}

// panelKeyValue is instance_settings 'agent_panel_key': the PEM, encrypted
// with ENCRYPTION_KEY, so it travels in the database backup.
type panelKeyValue struct {
	Key string `json:"key"`
}

// panelKeyAD is where the panel's agent key is sealed for (D-126).
var panelKeyAD = crypto.AD("instance_settings.value", "agent_panel_key")

func (r *postgresRepository) PanelAgentKey(ctx context.Context) (string, error) {
	for range 2 {
		raw, err := r.q.GetAgentPanelKey(ctx)
		if err == nil {
			var v panelKeyValue
			if err := json.Unmarshal(raw, &v); err != nil {
				return "", fmt.Errorf("read the panel's agent key: %w", err)
			}
			return r.keys.Open(panelKeyAD, v.Key)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return "", err
		}
		block, err := ssh.MarshalPrivateKey(priv, "toft-panel")
		if err != nil {
			return "", err
		}
		enc, err := r.keys.Seal(panelKeyAD, string(pem.EncodeToMemory(block)))
		if err != nil {
			return "", err
		}
		val, _ := json.Marshal(panelKeyValue{Key: enc})
		if err := r.q.InsertAgentPanelKey(ctx, val); err != nil {
			return "", err
		}
		// Read back: a concurrent start may have won.
	}
	return "", errors.New("could not store the panel's agent key")
}

func (r *postgresRepository) ListAgentServerIDs(ctx context.Context) ([]string, error) {
	rows, err := r.q.ListAgentServerIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, id := range rows {
		out = append(out, uuidString(id))
	}
	return out, nil
}

func (r *postgresRepository) PanelEpoch(ctx context.Context) (string, error) {
	return r.q.EnsurePanelEpoch(ctx)
}

func (r *postgresRepository) AgentGeneration(ctx context.Context, serverID string) (int64, int64, error) {
	row, err := r.q.GetAgentGeneration(ctx, pgUUID(serverID))
	return row.SpecGeneration, row.ObservedGeneration, err
}

func (r *postgresRepository) RecordAgentObserved(ctx context.Context, serverID string, generation int64, drift []byte, routes *int) error {
	return r.q.RecordAgentObserved(ctx, sqlcdb.RecordAgentObservedParams{ID: pgUUID(serverID), Generation: generation, Drift: drift, Routes: routesParam(routes)})
}

func (r *postgresRepository) RecordAgentFrozen(ctx context.Context, serverID string, running []byte, routes *int) error {
	return r.q.RecordAgentFrozen(ctx, sqlcdb.RecordAgentFrozenParams{ID: pgUUID(serverID), Running: running, Routes: routesParam(routes)})
}

func (r *postgresRepository) AgentsFrozen(ctx context.Context) (map[string]bool, error) {
	rows, err := r.q.ListAgentServersFrozen(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[uuidString(row.ID)] = row.Frozen
	}
	return out, nil
}

func routesParam(n *int) *int32 {
	if n == nil {
		return nil
	}
	v := int32(min(*n, math.MaxInt32))
	return &v
}

func intFromInt32(n *int32) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}
