package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const instancePauseKey = "agents_paused"

type postgresRepository struct {
	pool *pgxpool.Pool
	q    *sqlcdb.Queries
}

func NewPostgresRepository(pool *pgxpool.Pool) Repository {
	return &postgresRepository{pool: pool, q: sqlcdb.New(pool)}
}

func uid(s string) pgtype.UUID {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: u, Valid: true}
}

func ustr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return uuid.UUID(u.Bytes).String()
}

func uptr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := uuid.UUID(u.Bytes).String()
	return &s
}

func tptr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func uids(ss []string) []pgtype.UUID {
	out := make([]pgtype.UUID, 0, len(ss))
	for _, s := range ss {
		out = append(out, uid(s))
	}
	return out
}

func ustrs(us []pgtype.UUID) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		out = append(out, ustr(u))
	}
	return out
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func clientOf(c sqlcdb.OauthClient) *Client {
	return &Client{ID: ustr(c.ID), ClientID: c.ClientID, Kind: c.Kind, Name: c.Name, RedirectURIs: c.RedirectUris, FetchedAt: c.FetchedAt.Time}
}

func (r *postgresRepository) GetClient(ctx context.Context, clientID string) (*Client, error) {
	c, err := r.q.GetOAuthClient(ctx, clientID)
	if err != nil {
		return nil, notFound(err)
	}
	return clientOf(c), nil
}

func (r *postgresRepository) GetClientByID(ctx context.Context, id string) (*Client, error) {
	c, err := r.q.GetOAuthClientByID(ctx, uid(id))
	if err != nil {
		return nil, notFound(err)
	}
	return clientOf(c), nil
}

func (r *postgresRepository) UpsertCIMDClient(ctx context.Context, clientID, name string, redirectURIs []string) (*Client, error) {
	c, err := r.q.UpsertCIMDClient(ctx, sqlcdb.UpsertCIMDClientParams{ClientID: clientID, Name: name, RedirectUris: redirectURIs})
	if errors.Is(err, pgx.ErrNoRows) {
		// A DCR client with that ID: never turned into a CIMD one.
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return clientOf(c), nil
}

func (r *postgresRepository) InsertDCRClient(ctx context.Context, clientID, name string, redirectURIs []string, maxUnused int) (*Client, error) {
	c, err := r.q.InsertDCRClient(ctx, sqlcdb.InsertDCRClientParams{ClientID: clientID, Name: name, RedirectUris: redirectURIs, MaxUnused: int64(maxUnused)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrFull
	}
	if err != nil {
		return nil, err
	}
	return clientOf(c), nil
}

func (r *postgresRepository) InsertCode(ctx context.Context, c Code) error {
	return r.q.InsertOAuthCode(ctx, sqlcdb.InsertOAuthCodeParams{
		CodeHash: c.Hash, ClientID: uid(c.ClientID), UserID: uid(c.UserID), OrgID: uid(c.OrgID),
		AllProjects: c.AllProjects, ProjectIds: uids(c.ProjectIDs), Scopes: c.Scopes, RedirectUri: c.RedirectURI,
		CodeChallenge: c.CodeChallenge, ExpiresAt: ts(c.ExpiresAt),
	})
}

func (r *postgresRepository) UseCode(ctx context.Context, hash string) (*Code, error) {
	row, err := r.q.UseOAuthCode(ctx, hash)
	if err != nil {
		return nil, notFound(err)
	}
	return &Code{Hash: hash, ClientID: ustr(row.ClientID), UserID: ustr(row.UserID), OrgID: ustr(row.OrgID),
		AllProjects: row.AllProjects, ProjectIDs: ustrs(row.ProjectIds), Scopes: row.Scopes,
		RedirectURI: row.RedirectUri, CodeChallenge: row.CodeChallenge}, nil
}

func (r *postgresRepository) CreateConnection(ctx context.Context, n NewConnection) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.q.WithTx(tx)
	row, err := q.InsertAgentConnection(ctx, sqlcdb.InsertAgentConnectionParams{
		OrgID: uid(n.Code.OrgID), UserID: uid(n.Code.UserID), ClientID: uid(n.Code.ClientID), Name: n.Name,
		AllProjects: n.Code.AllProjects, Scopes: n.Code.Scopes, ExpiresAt: ts(n.ExpiresAt),
	})
	if err != nil {
		return "", fmt.Errorf("insert connection: %w", err)
	}
	if !n.Code.AllProjects {
		added, err := q.InsertAgentConnectionProjects(ctx, sqlcdb.InsertAgentConnectionProjectsParams{
			ConnectionID: row.ID, ProjectIds: uids(n.Code.ProjectIDs), OrgID: uid(n.Code.OrgID),
		})
		if err != nil {
			return "", fmt.Errorf("insert connection projects: %w", err)
		}
		if int(added) != len(n.Code.ProjectIDs) {
			return "", ErrForbidden
		}
	}
	for _, t := range []struct {
		hash, kind string
		exp        time.Time
	}{{n.AccessHash, "access", n.AccessExp}, {n.RefreshHash, "refresh", n.ExpiresAt}} {
		if err := q.InsertAgentToken(ctx, sqlcdb.InsertAgentTokenParams{TokenHash: t.hash, ConnectionID: row.ID, Kind: t.kind, ExpiresAt: ts(t.exp)}); err != nil {
			return "", fmt.Errorf("insert token: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return ustr(row.ID), nil
}

func (r *postgresRepository) RotateRefresh(ctx context.Context, hash, accessHash string, accessExp time.Time, refreshHash string) (string, []string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.q.WithTx(tx)
	row, err := q.UseRefreshToken(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		tok, gerr := r.q.GetAgentToken(ctx, hash)
		if gerr == nil && tok.Kind == "refresh" && tok.UsedAt.Valid {
			// Used before: someone else holds a copy. End the connection.
			if _, rerr := r.RevokeConnection(ctx, ustr(tok.ConnectionID), "", "A refresh token was used twice, so the connection was revoked."); rerr != nil && !errors.Is(rerr, ErrNotFound) {
				return "", nil, rerr
			}
			return "", nil, ErrConflict
		}
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	for _, t := range []struct {
		hash, kind string
		exp        time.Time
	}{{accessHash, "access", accessExp}, {refreshHash, "refresh", row.ExpiresAt.Time}} {
		if err := q.InsertAgentToken(ctx, sqlcdb.InsertAgentTokenParams{TokenHash: t.hash, ConnectionID: row.ConnectionID, Kind: t.kind, ExpiresAt: ts(t.exp)}); err != nil {
			return "", nil, fmt.Errorf("insert token: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, fmt.Errorf("commit: %w", err)
	}
	return ustr(row.ConnectionID), row.Scopes, nil
}

func (r *postgresRepository) ValidateAccess(ctx context.Context, hash string) (*Live, error) {
	row, err := r.q.ValidateAgentAccessToken(ctx, hash)
	if err != nil {
		return nil, notFound(err)
	}
	return &Live{ConnectionID: ustr(row.ID), OrgID: ustr(row.OrgID), UserID: ustr(row.UserID), Name: row.Name,
		AllProjects: row.AllProjects, ProjectIDs: ustrs(row.ProjectIds), Scopes: row.Scopes, OrgPaused: row.AgentsPausedAt.Valid}, nil
}

func (r *postgresRepository) InsertToken(ctx context.Context, hash, connectionID, kind string, expires time.Time) error {
	return r.q.InsertAgentToken(ctx, sqlcdb.InsertAgentTokenParams{TokenHash: hash, ConnectionID: uid(connectionID), Kind: kind, ExpiresAt: ts(expires)})
}

func (r *postgresRepository) DeleteToken(ctx context.Context, hash string) error {
	return r.q.DeleteAgentToken(ctx, hash)
}

func (r *postgresRepository) TokenConnection(ctx context.Context, hash string) (string, string, error) {
	t, err := r.q.GetAgentToken(ctx, hash)
	if err != nil {
		return "", "", notFound(err)
	}
	return ustr(t.ConnectionID), t.Kind, nil
}

func (r *postgresRepository) GetConnection(ctx context.Context, id string) (*ConnectionRow, error) {
	c, err := r.q.GetAgentConnection(ctx, uid(id))
	if err != nil {
		return nil, notFound(err)
	}
	return &ConnectionRow{ID: ustr(c.ID), OrgID: ustr(c.OrgID), UserID: ustr(c.UserID), Name: c.Name, Scopes: c.Scopes,
		ExpiresAt: c.ExpiresAt.Time, RevokedAt: tptr(c.RevokedAt)}, nil
}

func (r *postgresRepository) ListConnections(ctx context.Context, orgID, userID string) ([]Connection, error) {
	rows, err := r.q.ListAgentConnections(ctx, sqlcdb.ListAgentConnectionsParams{OrgID: uid(orgID), UserID: uid(userID)})
	if err != nil {
		return nil, err
	}
	out := make([]Connection, 0, len(rows))
	for _, c := range rows {
		out = append(out, Connection{ID: ustr(c.ID), Name: c.Name, ClientKind: c.ClientKind, ClientRef: c.ClientRef,
			UserID: ustr(c.UserID), UserName: c.UserName, UserEmail: c.UserEmail, UserRole: c.UserRole,
			AllProjects: c.AllProjects, Projects: c.ProjectNames, Scopes: c.Scopes, CreatedAt: c.CreatedAt.Time,
			ExpiresAt: c.ExpiresAt.Time, LastUsedAt: tptr(c.LastUsedAt), LastTool: c.LastTool})
	}
	return out, nil
}

func (r *postgresRepository) RevokeConnection(ctx context.Context, id, by, note string) (*ConnectionRow, error) {
	row, err := r.q.RevokeAgentConnection(ctx, sqlcdb.RevokeAgentConnectionParams{ID: uid(id), RevokedBy: uid(by)})
	if err != nil {
		return nil, notFound(err)
	}
	if _, err := r.q.CancelConnectionApprovals(ctx, sqlcdb.CancelConnectionApprovalsParams{ConnectionID: row.ID, Note: note}); err != nil {
		return nil, fmt.Errorf("cancel approvals: %w", err)
	}
	return &ConnectionRow{ID: ustr(row.ID), OrgID: ustr(row.OrgID), UserID: ustr(row.UserID), Name: row.Name}, nil
}

func (r *postgresRepository) Touch(ctx context.Context, connectionID, tool string) error {
	var t *string
	if tool != "" {
		t = &tool
	}
	return r.q.TouchAgentConnection(ctx, sqlcdb.TouchAgentConnectionParams{ID: uid(connectionID), Tool: t})
}

func (r *postgresRepository) ListPolicy(ctx context.Context, orgID string) (map[string]map[string]string, error) {
	rows, err := r.q.ListAgentPolicies(ctx, uid(orgID))
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, row := range rows {
		if out[row.Action] == nil {
			out[row.Action] = map[string]string{}
		}
		out[row.Action][row.EnvClass] = row.Decision
	}
	return out, nil
}

func (r *postgresRepository) SetPolicy(ctx context.Context, orgID, by string, cells []PolicyCell) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.q.WithTx(tx)
	for _, c := range cells {
		if err := q.UpsertAgentPolicy(ctx, sqlcdb.UpsertAgentPolicyParams{OrgID: uid(orgID), Action: c.Action,
			EnvClass: c.Class, Decision: c.Decision, UpdatedBy: uid(by)}); err != nil {
			return fmt.Errorf("set policy: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (r *postgresRepository) ServiceInfo(ctx context.Context, serviceID string) (*ServiceInfo, error) {
	row, err := r.q.ServiceEnvironmentClass(ctx, uid(serviceID))
	if err != nil {
		return nil, notFound(err)
	}
	label := row.Name
	if row.ProjectName != "" {
		label = row.ProjectName + " / " + row.Name
	}
	if row.EnvironmentName != "" {
		label += " in " + row.EnvironmentName
	}
	return &ServiceInfo{ID: ustr(row.ID), EnvironmentID: ustr(row.EnvironmentID), ProjectID: ustr(row.ProjectID), Class: row.EnvClass,
		Label: label, Data: dataService(ctx, row.AddonSlug, row.DockerImage, row.SourceType, row.ComposeRepoUrl, row.ComposeContent)}, nil
}

func (r *postgresRepository) EnvironmentInfo(ctx context.Context, environmentID string) (*ServiceInfo, error) {
	row, err := r.q.EnvironmentAgentClass(ctx, uid(environmentID))
	if err != nil {
		return nil, notFound(err)
	}
	return &ServiceInfo{EnvironmentID: ustr(row.ID), ProjectID: ustr(row.ProjectID), Class: row.EnvClass,
		Label: row.ProjectName + " / " + row.Name}, nil
}

func (r *postgresRepository) ProjectInfo(ctx context.Context, projectID string) (string, string, error) {
	row, err := r.q.ProjectAgentInfo(ctx, uid(projectID))
	if err != nil {
		return "", "", notFound(err)
	}
	return row.Name, ustr(row.OrgID), nil
}

func (r *postgresRepository) ListProjectPolicy(ctx context.Context, projectID string) (map[string]map[string]string, error) {
	rows, err := r.q.ListAgentProjectPolicies(ctx, uid(projectID))
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, row := range rows {
		if out[row.Action] == nil {
			out[row.Action] = map[string]string{}
		}
		out[row.Action][row.EnvClass] = row.Decision
	}
	return out, nil
}

func (r *postgresRepository) ListOrgProjectPolicies(ctx context.Context, orgID string) ([]ProjectPolicy, error) {
	rows, err := r.q.ListOrgAgentProjectPolicies(ctx, uid(orgID))
	if err != nil {
		return nil, err
	}
	out := make([]ProjectPolicy, 0)
	for _, row := range rows {
		id := ustr(row.ProjectID)
		if len(out) == 0 || out[len(out)-1].ProjectID != id {
			out = append(out, ProjectPolicy{ProjectID: id, Name: row.ProjectName, Cells: []PolicyCell{}})
		}
		last := &out[len(out)-1]
		last.Cells = append(last.Cells, PolicyCell{Action: row.Action, Class: row.EnvClass, Decision: row.Decision})
	}
	return out, nil
}

func (r *postgresRepository) SetProjectPolicy(ctx context.Context, projectID, by string, cells []PolicyCell) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := r.q.WithTx(tx)
	for _, c := range cells {
		if c.Decision == "" {
			err = q.DeleteAgentProjectPolicy(ctx, sqlcdb.DeleteAgentProjectPolicyParams{ProjectID: uid(projectID), Action: c.Action, EnvClass: c.Class})
		} else {
			err = q.UpsertAgentProjectPolicy(ctx, sqlcdb.UpsertAgentProjectPolicyParams{ProjectID: uid(projectID), Action: c.Action,
				EnvClass: c.Class, Decision: c.Decision, UpdatedBy: uid(by)})
		}
		if err != nil {
			return fmt.Errorf("set project policy: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (r *postgresRepository) InsertApproval(ctx context.Context, a NewApproval) (string, error) {
	req, err := json.Marshal(a.Request)
	if err != nil {
		return "", err
	}
	steps := a.PlanSteps
	if steps == nil {
		steps = []actor.PlanStep{}
	}
	rawSteps, err := json.Marshal(steps)
	if err != nil {
		return "", err
	}
	row, err := r.q.InsertAgentApproval(ctx, sqlcdb.InsertAgentApprovalParams{
		ID: uid(a.ID), OrgID: uid(a.OrgID), ConnectionID: uid(a.ConnectionID), UserID: uid(a.UserID), Action: a.Action, Tool: a.Tool,
		ServiceID: uid(a.ServiceID), EnvironmentID: uid(a.EnvironmentID), ProjectID: uid(a.ProjectID), Target: a.Target, Request: req,
		Sealed: sptr(a.Sealed), RequestHash: a.RequestHash, Reason: a.Reason, Plan: a.Plan, ExpiresAt: ts(a.ExpiresAt), PlanSteps: rawSteps,
	})
	if err != nil {
		return "", err
	}
	return ustr(row.ID), nil
}

func (r *postgresRepository) FindOpenApproval(ctx context.Context, connectionID, requestHash string) (string, error) {
	id, err := r.q.FindOpenAgentApproval(ctx, sqlcdb.FindOpenAgentApprovalParams{ConnectionID: uid(connectionID), RequestHash: requestHash})
	if err != nil {
		return "", notFound(err)
	}
	return ustr(id), nil
}

func (r *postgresRepository) GetApproval(ctx context.Context, id string) (*Approval, string, error) {
	row, err := r.q.GetAgentApproval(ctx, uid(id))
	if err != nil {
		return nil, "", notFound(err)
	}
	a := &Approval{ID: ustr(row.ID), ConnectionID: ustr(row.ConnectionID), AgentName: row.AgentName,
		UserID: ustr(row.UserID), UserName: row.UserName, UserRole: row.UserRole, Action: row.Action, Tool: row.Tool,
		ServiceID: uptr(row.ServiceID), EnvironmentID: uptr(row.EnvironmentID), ProjectID: uptr(row.ProjectID), Target: row.Target,
		Reason: row.Reason, Plan: row.Plan, State: row.State, ExpiresAt: row.ExpiresAt.Time,
		DecidedBy: row.DecidedByName, DecidedAt: tptr(row.DecidedAt), Note: row.Note, ExecutedAt: tptr(row.ExecutedAt),
		CreatedAt: row.CreatedAt.Time,
		facts: rollbackFacts{to: ustr(row.RollbackTo), commit: row.RollbackCommit, usedAt: tptr(row.RollbackUsedAt),
			depStatus: row.DeploymentStatus, depCommit: row.DeploymentCommit, depFinished: tptr(row.DeploymentFinishedAt), depLive: row.DeploymentLive,
			deciderRole: row.DecidedByRole}}
	_ = json.Unmarshal(row.Request, &a.Request)
	var steps []actor.PlanStep
	_ = json.Unmarshal(row.PlanSteps, &steps)
	for _, p := range steps {
		step := PlanStep{Text: p.Text, Tool: p.Tool}
		if p.ServiceID != "" {
			id := p.ServiceID
			step.ServiceID = &id
		}
		a.PlanSteps = append(a.PlanSteps, step)
	}
	if len(row.Result) > 0 {
		var res Result
		if json.Unmarshal(row.Result, &res) == nil {
			a.Result = &res
		}
	}
	return a, ustr(row.OrgID), nil
}

func (r *postgresRepository) ListWaiting(ctx context.Context, orgID string) ([]Waiting, error) {
	rows, err := r.q.ListWaitingAgentApprovals(ctx, uid(orgID))
	if err != nil {
		return nil, err
	}
	out := make([]Waiting, 0, len(rows))
	for _, w := range rows {
		out = append(out, Waiting{ID: ustr(w.ID), AgentName: w.AgentName, UserID: ustr(w.UserID), UserName: w.UserName,
			Action: w.Action, Tool: w.Tool, ServiceID: uptr(w.ServiceID), Target: w.Target, Reason: w.Reason,
			ExpiresAt: w.ExpiresAt.Time, CreatedAt: w.CreatedAt.Time})
	}
	return out, nil
}

func (r *postgresRepository) DecideApproval(ctx context.Context, id, orgID, state, by, note string) error {
	_, err := r.q.DecideAgentApproval(ctx, sqlcdb.DecideAgentApprovalParams{ID: uid(id), OrgID: uid(orgID), State: state, DecidedBy: uid(by), Note: note})
	return notFound(err)
}

func (r *postgresRepository) ClaimRun(ctx context.Context, id, connectionID string) (bool, error) {
	_, err := r.q.ClaimApprovalRun(ctx, sqlcdb.ClaimApprovalRunParams{ID: uid(id), ConnectionID: uid(connectionID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *postgresRepository) SetResult(ctx context.Context, id string, res Result, deploymentID string) error {
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return r.q.SetAgentApprovalResult(ctx, sqlcdb.SetAgentApprovalResultParams{ID: uid(id), Result: raw, DeploymentID: uid(deploymentID)})
}

func (r *postgresRepository) ReleaseRollback(ctx context.Context, approvalID string) error {
	return r.q.ReleaseApprovalRollback(ctx, uid(approvalID))
}

func (r *postgresRepository) ClaimRollback(ctx context.Context, connectionID, serviceID, rollbackTo string) (string, string, bool, error) {
	row, err := r.q.ClaimApprovalRollback(ctx, sqlcdb.ClaimApprovalRollbackParams{ConnectionID: uid(connectionID), ServiceID: uid(serviceID), RollbackTo: uid(rollbackTo)})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("claim the rollback an approval covers: %w", err)
	}
	return ustr(row.ID), row.ApproverName, true, nil
}

func (r *postgresRepository) Sealed(ctx context.Context, id string) (string, error) {
	v, err := r.q.GetAgentApprovalSealed(ctx, uid(id))
	if err != nil {
		return "", notFound(err)
	}
	if v == nil {
		return "", ErrNotFound
	}
	return *v, nil
}

func (r *postgresRepository) ClearSealed(ctx context.Context, id string) error {
	return r.q.ClearAgentApprovalSealed(ctx, uid(id))
}

func sptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *postgresRepository) SetOrgPaused(ctx context.Context, orgID, by string, paused bool, note string) error {
	p := sqlcdb.SetOrgAgentsPausedParams{ID: uid(orgID)}
	if paused {
		p.PausedAt, p.PausedBy = ts(time.Now()), uid(by)
	}
	if err := r.q.SetOrgAgentsPaused(ctx, p); err != nil {
		return err
	}
	if paused {
		_, err := r.q.CancelOrgApprovals(ctx, sqlcdb.CancelOrgApprovalsParams{OrgID: uid(orgID), Note: note})
		return err
	}
	return nil
}

func (r *postgresRepository) OrgPaused(ctx context.Context, orgID string) (*PauseState, error) {
	row, err := r.q.GetOrgAgentsPaused(ctx, uid(orgID))
	if err != nil {
		return nil, notFound(err)
	}
	if !row.AgentsPausedAt.Valid {
		return nil, nil
	}
	return &PauseState{At: row.AgentsPausedAt.Time, By: row.PausedByName}, nil
}

func (r *postgresRepository) SetInstancePaused(ctx context.Context, byID, byName string, paused bool, note string) error {
	if !paused {
		return r.q.DeleteInstanceSetting(ctx, instancePauseKey)
	}
	raw, err := json.Marshal(PauseState{At: time.Now().UTC(), By: byName})
	if err != nil {
		return err
	}
	if err := r.q.SetInstanceSetting(ctx, sqlcdb.SetInstanceSettingParams{Key: instancePauseKey, Value: raw, UpdatedBy: uid(byID)}); err != nil {
		return err
	}
	_, err = r.q.CancelAllApprovals(ctx, note)
	return err
}

func (r *postgresRepository) InstancePaused(ctx context.Context) (*PauseState, error) {
	raw, err := r.q.GetInstanceSetting(ctx, instancePauseKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p PauseState
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *postgresRepository) Clean(ctx context.Context, now time.Time) error {
	if _, err := r.q.DeleteExpiredAgentTokens(ctx, ts(now)); err != nil {
		return fmt.Errorf("delete expired tokens: %w", err)
	}
	if _, err := r.q.DeleteOldOAuthCodes(ctx, ts(now)); err != nil {
		return fmt.Errorf("delete old codes: %w", err)
	}
	if _, err := r.q.DeleteUnusedDCRClients(ctx, ts(now.Add(-24*time.Hour))); err != nil {
		return fmt.Errorf("delete unused registrations: %w", err)
	}
	if _, err := r.q.ClearStaleAgentApprovalSealed(ctx, ts(now)); err != nil {
		return fmt.Errorf("delete stale secret values: %w", err)
	}
	return nil
}
