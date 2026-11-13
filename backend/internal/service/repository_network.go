package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Read queries for app network planning (package C). Raw pgx, like the rest
// of this repository: db/sqlcdb is stale against schema.sql. No schema
// change: network names derive from existing IDs.

// optUUID parses id, or returns a NULL UUID when it is empty.
func optUUID(id string) (pgtype.UUID, error) {
	if id == "" {
		return pgtype.UUID{}, nil
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid id %q: %w", id, err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

// NetworkScope returns where a service sits: its scope, environment,
// project (direct, or through its environment), org (through its project, or
// direct for org scope) and server.
func (r *PostgresRepository) NetworkScope(ctx context.Context, serviceID string) (*NetworkScope, error) {
	id, err := optUUID(serviceID)
	if err != nil || !id.Valid {
		return nil, fmt.Errorf("NetworkScope: invalid service id %q", serviceID)
	}
	var (
		scope, name, alias, sourceType string
		envID, projectID, orgID, srvID pgtype.UUID
	)
	err = r.db.QueryRow(ctx, `
		SELECT s.scope, s.environment_id, COALESCE(e.project_id, s.project_id),
		       CASE WHEN s.scope = 'org' THEN s.scoped_org_id ELSE p.org_id END,
		       s.server_id, s.name, COALESCE(s.network_alias, ''), s.source_type
		FROM services s
		LEFT JOIN environments e ON e.id = s.environment_id
		LEFT JOIN projects p ON p.id = COALESCE(e.project_id, s.project_id)
		WHERE s.id = $1`, id,
	).Scan(&scope, &envID, &projectID, &orgID, &srvID, &name, &alias, &sourceType)
	if err != nil {
		return nil, fmt.Errorf("NetworkScope: %w", err)
	}
	return &NetworkScope{
		Scope:         Scope(scope),
		ServiceID:     serviceID,
		Name:          name,
		Alias:         alias,
		SourceType:    SourceType(sourceType),
		EnvironmentID: uuidString(envID),
		ProjectID:     uuidString(projectID),
		OrgID:         uuidString(orgID),
		ServerID:      uuidString(srvID),
	}, nil
}

// ListNetworkPeers returns the services on the same server as sc (other
// than sc itself) whose containers join sc's home network: for environment
// scope its environment's services and the shared services of its project
// and org; for project scope the other services shared by its project and
// its org's shared services; for org scope the other services its org
// shares. Ordered by ID.
func (r *PostgresRepository) ListNetworkPeers(ctx context.Context, sc NetworkScope) ([]NetworkPeer, error) {
	self, err := optUUID(sc.ServiceID)
	if err != nil {
		return nil, err
	}
	srv, err := optUUID(sc.ServerID)
	if err != nil {
		return nil, err
	}
	var envID, projectID, orgID string
	switch sc.Scope {
	case ScopeEnvironment:
		envID, projectID, orgID = sc.EnvironmentID, sc.ProjectID, sc.OrgID
	case ScopeProject:
		projectID, orgID = sc.ProjectID, sc.OrgID
	case ScopeOrg:
		orgID = sc.OrgID
	default:
		return nil, fmt.Errorf("ListNetworkPeers: unknown scope %q", sc.Scope)
	}
	env, err := optUUID(envID)
	if err != nil {
		return nil, err
	}
	proj, err := optUUID(projectID)
	if err != nil {
		return nil, err
	}
	org, err := optUUID(orgID)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `
		SELECT s.id, s.name, COALESCE(s.network_alias, ''), s.source_type, s.scope
		FROM services s
		WHERE s.server_id IS NOT DISTINCT FROM $4::uuid
		  AND s.id <> $5
		  AND (   ($1::uuid IS NOT NULL AND s.scope = 'environment' AND s.environment_id = $1)
		       OR ($2::uuid IS NOT NULL AND s.scope = 'project'     AND s.project_id = $2)
		       OR ($3::uuid IS NOT NULL AND s.scope = 'org'         AND s.scoped_org_id = $3))
		ORDER BY s.id`, env, proj, org, srv, self)
	if err != nil {
		return nil, fmt.Errorf("ListNetworkPeers: %w", err)
	}
	defer rows.Close()
	out := make([]NetworkPeer, 0)
	for rows.Next() {
		var (
			id                            pgtype.UUID
			name, alias, sourceType, scop string
		)
		if err := rows.Scan(&id, &name, &alias, &sourceType, &scop); err != nil {
			return nil, fmt.Errorf("ListNetworkPeers scan: %w", err)
		}
		out = append(out, NetworkPeer{ID: uuidString(id), Name: name, Alias: alias, SourceType: SourceType(sourceType), Scope: Scope(scop)})
	}
	return out, rows.Err()
}

// ListNetworkTargets returns the consumer networks of a shared service:
// for a project (orgID empty) its environments; for an org its projects'
// environments and its projects. Ordered by ID.
func (r *PostgresRepository) ListNetworkTargets(ctx context.Context, projectID, orgID string) ([]NetworkTarget, []string, error) {
	proj, err := optUUID(projectID)
	if err != nil {
		return nil, nil, err
	}
	org, err := optUUID(orgID)
	if err != nil {
		return nil, nil, err
	}
	if proj.Valid == org.Valid {
		return nil, nil, fmt.Errorf("ListNetworkTargets: want a project or an org")
	}
	rows, err := r.db.Query(ctx, `
		SELECT e.id, e.project_id
		FROM environments e
		JOIN projects p ON p.id = e.project_id
		WHERE ($1::uuid IS NOT NULL AND e.project_id = $1)
		   OR ($2::uuid IS NOT NULL AND p.org_id = $2)
		ORDER BY e.id`, proj, org)
	if err != nil {
		return nil, nil, fmt.Errorf("ListNetworkTargets: %w", err)
	}
	var targets []NetworkTarget
	for rows.Next() {
		var envID, pID pgtype.UUID
		if err := rows.Scan(&envID, &pID); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("ListNetworkTargets scan: %w", err)
		}
		targets = append(targets, NetworkTarget{EnvironmentID: uuidString(envID), ProjectID: uuidString(pID)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("ListNetworkTargets: %w", err)
	}
	if !org.Valid {
		return targets, nil, nil
	}
	prow, err := r.db.Query(ctx, `SELECT id FROM projects WHERE org_id = $1 ORDER BY id`, org)
	if err != nil {
		return nil, nil, fmt.Errorf("ListNetworkTargets projects: %w", err)
	}
	defer prow.Close()
	var projects []string
	for prow.Next() {
		var id pgtype.UUID
		if err := prow.Scan(&id); err != nil {
			return nil, nil, fmt.Errorf("ListNetworkTargets projects scan: %w", err)
		}
		projects = append(projects, uuidString(id))
	}
	return targets, projects, prow.Err()
}

// ListIDsOnServer returns the IDs of the services assigned to serverID ("":
// the control-plane server, server_id NULL), ordered.
func (r *PostgresRepository) ListIDsOnServer(ctx context.Context, serverID string) ([]string, error) {
	srv, err := optUUID(serverID)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `SELECT id FROM services WHERE server_id IS NOT DISTINCT FROM $1::uuid ORDER BY id`, srv)
	if err != nil {
		return nil, fmt.Errorf("ListIDsOnServer: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ListIDsOnServer scan: %w", err)
		}
		ids = append(ids, uuidString(id))
	}
	return ids, rows.Err()
}

// ListAllIDs returns the IDs of every service, ordered: a container of any of
// them is not an orphan, whichever server entry it is assigned to.
func (r *PostgresRepository) ListAllIDs(ctx context.Context) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT id FROM services ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("ListAllIDs: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ListAllIDs scan: %w", err)
		}
		ids = append(ids, uuidString(id))
	}
	return ids, rows.Err()
}
