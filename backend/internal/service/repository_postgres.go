package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/observability"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct {
	db      *pgxpool.Pool
	queries *sqlcdb.Queries
	keys    *crypto.Keys
}

func NewPostgresRepository(pool *pgxpool.Pool, keys *crypto.Keys) *PostgresRepository {
	return &PostgresRepository{
		db:      pool,
		queries: sqlcdb.New(pool),
		keys:    keys,
	}
}

// credentialAD is where a service's registry_password, git_token or
// basic_auth_password is sealed for (D-126).
func credentialAD(column string, id pgtype.UUID) string {
	return crypto.AD("services."+column, uuidString(id))
}

// svcJoinSelect is the SELECT+FROM+JOIN clause for all service queries.
// It LEFT JOINs environments so that project-scoped and org-scoped services
// (which have NULL environment_id) are also returned.
// project_id is resolved from environments for env-scoped services, or directly
// from s.project_id for project/org-scoped services via COALESCE.
const svcJoinSelect = `SELECT s.id, COALESCE(e.project_id, s.project_id), s.environment_id, s.server_id,
	s.name, s.type, s.source_type,
	s.git_repo_url, s.git_branch, s.base_directory, s.docker_image,
	s.install_command, s.build_command, s.start_command, s.build_pack,
	s.port, s.replicas, s.cpu_limit, s.memory_limit, s.runtime_status, s.active_deployment_id,
	s.created_at, s.updated_at,
	s.compose_content, s.compose_repo_url, s.compose_file_path,
	s.registry_server, s.registry_username, s.registry_password,
	s.addon_slug, s.network_alias, s.volumes,
	s.git_username, s.git_token,
	s.pre_deploy_commands, s.post_deploy_commands,
	s.health_check_path, s.health_check_timeout, s.health_check_retries,
	s.required_env_vars, s.migrate_command, s.dependencies,
	s.basic_auth_user, s.basic_auth_password, s.ip_allowlist,
	s.scope, s.scoped_org_id,
	s.compose_route_service, s.deploy_strategy,
	s.repository_id, s.deploy_on_push, s.compose_network_service,
	EXISTS (SELECT 1 FROM deployments d WHERE d.service_id = s.id AND d.status IN ('pending', 'queued', 'building')),
	EXISTS (SELECT 1 FROM alerts a WHERE a.resolved_at IS NULL AND a.subject = s.id::text
		AND a.kind IN ('service_down', 'service_restarting')),
	EXISTS (SELECT 1 FROM alerts a WHERE a.resolved_at IS NULL AND a.subject = s.id::text
		AND a.kind IN ('service_unhealthy', 'container_exited')),
	COALESCE((SELECT d.status = 'failed' FROM deployments d WHERE d.service_id = s.id
		ORDER BY d.created_at DESC LIMIT 1), false),
	live.finished_at AS live_since
FROM services s
LEFT JOIN environments e ON s.environment_id = e.id
LEFT JOIN deployments live ON live.id = s.active_deployment_id`

// scanServiceRow scans a pgx row into a Service, decrypting encrypted fields.
func (r *PostgresRepository) scanServiceRow(scanFn func(dest ...any) error) (Service, error) {
	var (
		parsedID, parsedProjectID, parsedEnvID, parsedServerID pgtype.UUID
		name, typ, sourceType                                  string
		gitRepoURL, dockerImage                                *string
		gitBranch, baseDir, buildPack                          string
		installCmd, buildCmd, startCmd                         *string
		port, replicas                                         int32
		cpuLimit, memLimit                                     *string
		runtimeStatus                                          string
		activeDepID                                            pgtype.UUID
		createdAt, updatedAt                                   pgtype.Timestamptz
		composeContent                                         *string
		composeRepoURL, composeFilePath                        string
		registryServer, registryUsername                       *string
		registryPassword                                       *string
		addonSlug, networkAlias                                *string
		volumesJSON                                            string
		gitUsername, gitToken                                  *string
		preDeployCommands, postDeployCommands                  string
		healthCheckPath                                        string
		healthCheckTimeout, healthCheckRetries                 int32
		requiredEnvVars                                        string
		migrateCommand                                         string
		dependenciesJSON                                       string
		basicAuthUser, basicAuthPassword, ipAllowlistJSON      string
		scope                                                  string
		parsedScopedOrgID                                      pgtype.UUID
		composeRouteService, deployStrategy                    string
		parsedRepositoryID                                     pgtype.UUID
		deployOnPush                                           bool
		composeNetworkService                                  *string
		deploying, down, unhealthy, lastDeployFailed           bool
		liveSince                                              *time.Time
	)
	err := scanFn(
		&parsedID, &parsedProjectID, &parsedEnvID, &parsedServerID,
		&name, &typ, &sourceType,
		&gitRepoURL, &gitBranch, &baseDir, &dockerImage,
		&installCmd, &buildCmd, &startCmd, &buildPack,
		&port, &replicas, &cpuLimit, &memLimit,
		&runtimeStatus, &activeDepID,
		&createdAt, &updatedAt,
		&composeContent, &composeRepoURL, &composeFilePath,
		&registryServer, &registryUsername, &registryPassword,
		&addonSlug, &networkAlias, &volumesJSON,
		&gitUsername, &gitToken,
		&preDeployCommands, &postDeployCommands,
		&healthCheckPath, &healthCheckTimeout, &healthCheckRetries,
		&requiredEnvVars, &migrateCommand, &dependenciesJSON,
		&basicAuthUser, &basicAuthPassword, &ipAllowlistJSON,
		&scope, &parsedScopedOrgID,
		&composeRouteService, &deployStrategy,
		&parsedRepositoryID, &deployOnPush, &composeNetworkService,
		&deploying, &down, &unhealthy, &lastDeployFailed,
		&liveSince,
	)
	if err != nil {
		return Service{}, err
	}

	var decryptedPassword string
	if registryPassword != nil && *registryPassword != "" {
		decryptedPassword, _ = r.keys.Open(credentialAD("registry_password", parsedID), *registryPassword)
	}

	var decryptedGitToken string
	if gitToken != nil && *gitToken != "" {
		decryptedGitToken, _ = r.keys.Open(credentialAD("git_token", parsedID), *gitToken)
	}

	svcScope := Scope(scope)
	if svcScope == "" {
		svcScope = ScopeEnvironment
	}

	return Service{
		ID:                  uuidString(parsedID),
		Scope:               svcScope,
		ProjectID:           uuidString(parsedProjectID),
		EnvironmentID:       uuidString(parsedEnvID),
		ScopedOrgID:         uuidString(parsedScopedOrgID),
		ServerID:            uuidString(parsedServerID),
		Name:                name,
		Type:                Type(typ),
		SourceType:          SourceType(sourceType),
		GitRepoURL:          derefString(gitRepoURL),
		GitBranch:           gitBranch,
		RepositoryID:        uuidString(parsedRepositoryID),
		DeployOnPush:        deployOnPush,
		BaseDirectory:       baseDir,
		DockerImage:         derefString(dockerImage),
		InstallCommand:      derefString(installCmd),
		BuildCommand:        derefString(buildCmd),
		StartCommand:        derefString(startCmd),
		BuildPack:           buildPack,
		Port:                int(port),
		Replicas:            int(replicas),
		CPULimit:            derefString(cpuLimit),
		MemoryLimit:         derefString(memLimit),
		RuntimeStatus:       runtimeStatus,
		Deploying:           deploying,
		Down:                down,
		Unhealthy:           unhealthy,
		LastDeployFailed:    lastDeployFailed,
		ActiveDeploymentID:  uuidString(activeDepID),
		LiveSince:           liveSince,
		ComposeContent:      derefString(composeContent),
		ComposeRepoURL:      composeRepoURL,
		ComposeFilePath:     composeFilePath,
		ComposeRouteService: composeRouteService,
		DeployStrategy:      deployStrategy,
		RegistryServer:      derefString(registryServer),
		RegistryUsername:    derefString(registryUsername),
		RegistryPassword:    decryptedPassword,
		HasRegistryPassword: decryptedPassword != "",
		AddonSlug:           derefString(addonSlug),
		NetworkAlias:        derefString(networkAlias),
		Volumes:             parseVolumes(volumesJSON),
		GitUsername:         derefString(gitUsername),
		GitToken:            decryptedGitToken,
		HasGitToken:         decryptedGitToken != "",
		PreDeployCommands:   preDeployCommands,
		PostDeployCommands:  postDeployCommands,
		HealthCheckPath:     healthCheckPath,
		HealthCheckTimeout:  int(healthCheckTimeout),
		HealthCheckRetries:  int(healthCheckRetries),
		RequiredEnvVars:     requiredEnvVars,
		MigrateCommand:      migrateCommand,
		Dependencies:        parseDependencies(dependenciesJSON),
		BasicAuthUser:       basicAuthUser,
		BasicAuthPassword:   r.basicAuthPassword(parsedID, basicAuthPassword),
		IPAllowlist:         parseIPAllowlist(ipAllowlistJSON),
		CreatedAt:           createdAt.Time,
		UpdatedAt:           updatedAt.Time,

		ComposeNetworkService: derefString(composeNetworkService),
	}, nil
}

// setSource stores a new service's repository source and its deploy on
// push (D-77), in the create's transaction.
func setSource(ctx context.Context, q *sqlcdb.Queries, id pgtype.UUID, input CreateInput) error {
	if input.RepositoryID == "" && input.DeployOnPush == nil {
		return nil
	}
	repo, _ := uuid.Parse(input.RepositoryID)
	return q.SetServiceSource(ctx, sqlcdb.SetServiceSourceParams{
		ID: id, SetRepository: input.RepositoryID != "", RepositoryID: pgtype.UUID{Bytes: repo, Valid: input.RepositoryID != ""},
		DeployOnPush: input.DeployOnPush,
	})
}

// SetSource changes a service's repository source (repositoryID nil keeps
// it, "" clears it) and its deploy on push (nil keeps it).
func (r *PostgresRepository) SetSource(ctx context.Context, id string, repositoryID *string, deployOnPush *bool) error {
	sid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}
	p := sqlcdb.SetServiceSourceParams{ID: pgtype.UUID{Bytes: sid, Valid: true}, DeployOnPush: deployOnPush}
	if repositoryID != nil {
		p.SetRepository = true
		if rid, err := uuid.Parse(*repositoryID); err == nil {
			p.RepositoryID = pgtype.UUID{Bytes: rid, Valid: true}
		}
	}
	if err := r.queries.SetServiceSource(ctx, p); err != nil {
		return fmt.Errorf("SetServiceSource: %w", err)
	}
	return nil
}

// RepositoryInScope says whether repositoryID is the project's, the
// environment's project when environmentID is set.
func (r *PostgresRepository) RepositoryInScope(ctx context.Context, repositoryID, environmentID, projectID string) (bool, error) {
	opt := func(s string) pgtype.UUID {
		u, err := uuid.Parse(s)
		return pgtype.UUID{Bytes: u, Valid: err == nil}
	}
	ok, err := r.queries.RepositoryInScope(ctx, sqlcdb.RepositoryInScopeParams{
		RepositoryID: opt(repositoryID), EnvironmentID: opt(environmentID), ProjectID: opt(projectID)})
	if err != nil {
		return false, fmt.Errorf("RepositoryInScope: %w", err)
	}
	return ok, nil
}

// encryptPassword encrypts a plaintext credential for the service's column;
// returns nil pointer for empty input.
func (r *PostgresRepository) encryptPassword(column string, id pgtype.UUID, plain string) (*string, error) {
	if plain == "" {
		return nil, nil
	}
	enc, err := r.keys.Seal(credentialAD(column, id), plain)
	if err != nil {
		return nil, err
	}
	return &enc, nil
}

// basicAuthPassword reads a stored basic auth password. Stored encrypted
// since 0019; a value that does not decrypt was written in plain text (before
// the upgrade's startup pass, or by an older backend after a rollback) and is
// used as it is. Read as "", it would turn basic auth off at the next deploy.
func (r *PostgresRepository) basicAuthPassword(id pgtype.UUID, stored string) string {
	if stored == "" {
		return ""
	}
	if plain, err := r.keys.Open(credentialAD("basic_auth_password", id), stored); err == nil {
		return plain
	}
	return stored
}

// setAccessControl stores the basic auth and IP allowlist a service is
// created with, the password encrypted.
func (r *PostgresRepository) setAccessControl(ctx context.Context, q *sqlcdb.Queries, id pgtype.UUID, input CreateInput) error {
	if input.BasicAuthUser == "" && input.BasicAuthPassword == "" && len(input.IPAllowlist) == 0 {
		return nil
	}
	encPass, err := r.encryptPassword("basic_auth_password", id, input.BasicAuthPassword)
	if err != nil {
		return fmt.Errorf("encrypt basic auth password: %w", err)
	}
	if err := q.SetServiceAccessControl(ctx, sqlcdb.SetServiceAccessControlParams{
		ID:                id,
		BasicAuthUser:     input.BasicAuthUser,
		BasicAuthPassword: derefString(encPass),
		IpAllowlist:       marshalIPAllowlist(input.IPAllowlist),
	}); err != nil {
		return fmt.Errorf("access control: %w", err)
	}
	return nil
}

// StoreBasicAuthPasswords rewrites the stored basic auth passwords encrypted
// (the startup pass) or, with encrypted false, in plain text (the rollback
// command, for a backend from before 0019). It returns how many rows changed.
// One statement; a row changes only while it still holds the value read.
func (r *PostgresRepository) StoreBasicAuthPasswords(ctx context.Context, encrypted bool) (int64, error) {
	rows, err := r.queries.ListBasicAuthPasswords(ctx)
	if err != nil {
		return 0, fmt.Errorf("ListBasicAuthPasswords: %w", err)
	}
	params := sqlcdb.ReplaceBasicAuthPasswordsParams{
		Ids:       make([]pgtype.UUID, 0, len(rows)),
		OldValues: make([]string, 0, len(rows)),
		NewValues: make([]string, 0, len(rows)),
	}
	for _, row := range rows {
		ad := credentialAD("basic_auth_password", row.ID)
		// Either format counts as sealed: the reseal pass, after this one,
		// converts an old one.
		plain, decErr := r.keys.OpenLegacy(ad, row.BasicAuthPassword)
		if (decErr == nil) == encrypted {
			continue // already stored the wanted way
		}
		next := plain
		if encrypted {
			if next, err = r.keys.Seal(ad, row.BasicAuthPassword); err != nil {
				return 0, fmt.Errorf("encrypt the basic auth password of service %s: %w", uuidString(row.ID), err)
			}
		}
		params.Ids = append(params.Ids, row.ID)
		params.OldValues = append(params.OldValues, row.BasicAuthPassword)
		params.NewValues = append(params.NewValues, next)
	}
	if len(params.Ids) == 0 {
		return 0, nil
	}
	n, err := r.queries.ReplaceBasicAuthPasswords(ctx, params)
	if err != nil {
		return 0, fmt.Errorf("ReplaceBasicAuthPasswords: %w", err)
	}
	return n, nil
}

// ─── CRUD ─────────────────────────────────────────────────────────────────────

func (r *PostgresRepository) Create(ctx context.Context, environmentID string, input CreateInput) (*Service, error) {
	parsedEnvID, err := uuid.Parse(environmentID)
	if err != nil {
		return nil, fmt.Errorf("invalid environment_id %q: %w", environmentID, err)
	}

	// The ID is made here, so the credentials are sealed for it (D-126).
	newID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	encPass, err := r.encryptPassword("registry_password", newID, input.RegistryPassword)
	if err != nil {
		return nil, fmt.Errorf("encrypt registry password: %w", err)
	}

	encGitToken, err := r.encryptPassword("git_token", newID, input.GitToken)
	if err != nil {
		return nil, fmt.Errorf("encrypt git token: %w", err)
	}

	// Insert and retrieve the new ID, then fetch the full row via svcJoinSelect
	// so that project_id is populated through the environments JOIN.
	const insertQ = `
		INSERT INTO services (
			environment_id, server_id, name, type, source_type,
			git_repo_url, git_branch, base_directory,
			docker_image, install_command, build_command, start_command, build_pack,
			port, replicas, cpu_limit, memory_limit,
			compose_content, compose_repo_url, compose_file_path,
			registry_server, registry_username, registry_password,
			addon_slug, network_alias, volumes,
			git_username, git_token,
			pre_deploy_commands, post_deploy_commands,
			health_check_path, health_check_timeout, health_check_retries,
			required_env_vars, migrate_command, dependencies,
			compose_route_service, id
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18, $19, $20, $21, $22, $23,
			$24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36,
			$37, $38
		) RETURNING id`

	healthCheckPath := input.HealthCheckPath
	if healthCheckPath == "" {
		healthCheckPath = "/health"
	}
	healthCheckTimeout := input.HealthCheckTimeout
	if healthCheckTimeout == 0 {
		healthCheckTimeout = 5
	}
	healthCheckRetries := input.HealthCheckRetries

	var parsedServerID pgtype.UUID
	if input.ServerID != "" {
		sid, parseErr := uuid.Parse(input.ServerID)
		if parseErr == nil {
			parsedServerID = pgtype.UUID{Bytes: sid, Valid: true}
		}
	}

	// One transaction: a service created with basic auth or an allowlist is
	// never stored without them. A savepoint inside a project's create (7g).
	tx, err := dbtx.On(ctx, r.db).Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("Create service: begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck — a no-op after Commit

	err = tx.QueryRow(ctx, insertQ,
		pgtype.UUID{Bytes: parsedEnvID, Valid: true},
		parsedServerID,
		input.Name, string(input.Type), string(input.SourceType),
		optString(input.GitRepoURL), input.GitBranch, input.BaseDirectory,
		optString(input.DockerImage),
		optString(input.InstallCommand), optString(input.BuildCommand), optString(input.StartCommand),
		input.BuildPack,
		int32(input.Port), int32(1),
		optString(input.CPULimit), optString(input.MemoryLimit),
		optString(input.ComposeContent),
		input.ComposeRepoURL, input.ComposeFilePath,
		optString(input.RegistryServer), optString(input.RegistryUsername),
		encPass,
		optString(input.AddonSlug), optString(input.NetworkAlias),
		marshalVolumes(input.Volumes),
		optString(input.GitUsername), encGitToken,
		input.PreDeployCommands, input.PostDeployCommands,
		healthCheckPath, int32(healthCheckTimeout), int32(healthCheckRetries),
		input.RequiredEnvVars, input.MigrateCommand, marshalDependencies(input.Dependencies),
		input.ComposeRouteService, newID,
	).Scan(&newID)
	if err != nil {
		if isDuplicateNameError(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("Create service: %w", err)
	}
	if err := r.setAccessControl(ctx, sqlcdb.New(tx), newID, input); err != nil {
		return nil, fmt.Errorf("Create service: %w", err)
	}
	if err := setSource(ctx, sqlcdb.New(tx), newID, input); err != nil {
		return nil, fmt.Errorf("Create service: %w", err)
	}
	if input.ContainerHealth != "" {
		if err := setContainerHealth(ctx, sqlcdb.New(tx), newID, input.ContainerHealth, input.ContainerHealthCommand); err != nil {
			return nil, fmt.Errorf("Create service: %w", err)
		}
	}
	if input.AddressHostname != "" {
		// Its address from creation (D-150), with the service or not at all.
		if _, err := sqlcdb.New(tx).SaveNewDomain(ctx, sqlcdb.SaveNewDomainParams{ServiceID: newID, Hostname: input.AddressHostname, TlsEnabled: true}); err != nil {
			if isDuplicateNameError(err) {
				return nil, ErrAddressTaken
			}
			return nil, fmt.Errorf("Create service: its address: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("Create service: commit: %w", err)
	}

	return r.GetByID(ctx, uuidString(newID))
}

func (r *PostgresRepository) RuntimeStatuses(ctx context.Context, serviceID string, ids []string) (map[string]ServiceStatus, error) {
	out := make(map[string]ServiceStatus, len(ids))
	sid, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}
	uuids := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		if parsed, err := uuid.Parse(id); err == nil {
			uuids = append(uuids, pgtype.UUID{Bytes: parsed, Valid: true})
		}
	}
	if len(uuids) == 0 {
		return out, nil
	}
	rows, err := r.queries.DependencyStatuses(ctx, sqlcdb.DependencyStatusesParams{Ids: uuids, ServiceID: pgtype.UUID{Bytes: sid, Valid: true}})
	if err != nil {
		return nil, fmt.Errorf("DependencyStatuses: %w", err)
	}
	for _, row := range rows {
		out[uuidString(row.ID)] = ServiceStatus{Name: row.Name, RuntimeStatus: row.RuntimeStatus}
	}
	return out, nil
}

func (r *PostgresRepository) GetByID(ctx context.Context, id string) (*Service, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid service id %q: %w", id, err)
	}

	svc, err := r.scanServiceRow(dbtx.On(ctx, r.db).QueryRow(ctx,
		svcJoinSelect+` WHERE s.id = $1 LIMIT 1`,
		pgtype.UUID{Bytes: parsedID, Valid: true},
	).Scan)
	if err != nil {
		return nil, fmt.Errorf("GetByID: %w", err)
	}
	return &svc, nil
}

func (r *PostgresRepository) ListByProject(ctx context.Context, projectID string) ([]Service, error) {
	parsedProjectID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("invalid project id %q: %w", projectID, err)
	}

	rows, err := r.db.Query(ctx,
		svcJoinSelect+` JOIN projects p ON e.project_id = p.id WHERE p.id = $1 ORDER BY s.created_at ASC`,
		pgtype.UUID{Bytes: parsedProjectID, Valid: true},
	)
	if err != nil {
		return nil, fmt.Errorf("ListByProject: %w", err)
	}
	defer rows.Close()

	result := make([]Service, 0)
	for rows.Next() {
		svc, err := r.scanServiceRow(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("ListByProject scan: %w", err)
		}
		result = append(result, svc)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) ListByEnvironment(ctx context.Context, environmentID string) ([]Service, error) {
	parsedEnvID, err := uuid.Parse(environmentID)
	if err != nil {
		return nil, fmt.Errorf("invalid environment_id %q: %w", environmentID, err)
	}

	// Returns:
	//  1. env-scoped services directly in this environment
	//  2. project-scoped services belonging to the same project
	//  3. org-scoped services belonging to the same org
	const q = `
		WITH env_ctx AS (
			SELECT e.project_id, p.org_id
			FROM environments e
			JOIN projects p ON e.id = e.id AND e.project_id = p.id
			WHERE e.id = $1
		)
		` + svcJoinSelect + `
		WHERE s.environment_id = $1
		   OR (s.scope = 'project' AND s.project_id = (SELECT project_id FROM env_ctx))
		   OR (s.scope = 'org'     AND s.scoped_org_id = (SELECT org_id FROM env_ctx))
		ORDER BY s.scope DESC, s.created_at ASC`

	rows, err := r.db.Query(ctx, q, pgtype.UUID{Bytes: parsedEnvID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListByEnvironment: %w", err)
	}
	defer rows.Close()

	result := make([]Service, 0)
	for rows.Next() {
		svc, err := r.scanServiceRow(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("ListByEnvironment scan: %w", err)
		}
		result = append(result, svc)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) ListProjectScoped(ctx context.Context, projectID string) ([]Service, error) {
	parsedID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("invalid project_id %q: %w", projectID, err)
	}

	rows, err := r.db.Query(ctx,
		svcJoinSelect+` WHERE s.scope = 'project' AND s.project_id = $1 ORDER BY s.created_at ASC`,
		pgtype.UUID{Bytes: parsedID, Valid: true},
	)
	if err != nil {
		return nil, fmt.Errorf("ListProjectScoped: %w", err)
	}
	defer rows.Close()

	result := make([]Service, 0)
	for rows.Next() {
		svc, err := r.scanServiceRow(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("ListProjectScoped scan: %w", err)
		}
		result = append(result, svc)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) ListOrgScoped(ctx context.Context, orgID string) ([]Service, error) {
	parsedID, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org_id %q: %w", orgID, err)
	}

	rows, err := r.db.Query(ctx,
		svcJoinSelect+` WHERE s.scope = 'org' AND s.scoped_org_id = $1 ORDER BY s.created_at ASC`,
		pgtype.UUID{Bytes: parsedID, Valid: true},
	)
	if err != nil {
		return nil, fmt.Errorf("ListOrgScoped: %w", err)
	}
	defer rows.Close()

	result := make([]Service, 0)
	for rows.Next() {
		svc, err := r.scanServiceRow(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("ListOrgScoped scan: %w", err)
		}
		result = append(result, svc)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) CreateProjectScoped(ctx context.Context, projectID string, input CreateInput) (*Service, error) {
	parsedProjectID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("invalid project_id %q: %w", projectID, err)
	}
	return r.createScoped(ctx, ScopeProject, pgtype.UUID{Bytes: parsedProjectID, Valid: true}, pgtype.UUID{}, input)
}

func (r *PostgresRepository) CreateOrgScoped(ctx context.Context, orgID string, input CreateInput) (*Service, error) {
	parsedOrgID, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org_id %q: %w", orgID, err)
	}
	return r.createScoped(ctx, ScopeOrg, pgtype.UUID{}, pgtype.UUID{Bytes: parsedOrgID, Valid: true}, input)
}

// createScoped inserts a project- or org-scoped service (no environment_id).
func (r *PostgresRepository) createScoped(ctx context.Context, scope Scope, projectID, orgID pgtype.UUID, input CreateInput) (*Service, error) {
	// The ID is made here, so the credentials are sealed for it (D-126).
	newID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	encPass, err := r.encryptPassword("registry_password", newID, input.RegistryPassword)
	if err != nil {
		return nil, fmt.Errorf("encrypt registry password: %w", err)
	}
	encGitToken, err := r.encryptPassword("git_token", newID, input.GitToken)
	if err != nil {
		return nil, fmt.Errorf("encrypt git token: %w", err)
	}

	const insertQ = `
		INSERT INTO services (
			scope, project_id, scoped_org_id,
			server_id, name, type, source_type,
			git_repo_url, git_branch, base_directory,
			docker_image, install_command, build_command, start_command, build_pack,
			port, replicas, cpu_limit, memory_limit,
			compose_content, compose_repo_url, compose_file_path,
			registry_server, registry_username, registry_password,
			addon_slug, network_alias, volumes,
			git_username, git_token,
			pre_deploy_commands, post_deploy_commands,
			health_check_path, health_check_timeout, health_check_retries,
			required_env_vars, migrate_command, dependencies,
			compose_route_service, id
		) VALUES (
			$1, $2, $3,
			$4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			$16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28,
			$29, $30, $31, $32, $33, $34, $35, $36, $37, $38,
			$39, $40
		) RETURNING id`

	healthCheckPath := input.HealthCheckPath
	if healthCheckPath == "" {
		healthCheckPath = "/health"
	}
	healthCheckTimeout := input.HealthCheckTimeout
	if healthCheckTimeout == 0 {
		healthCheckTimeout = 5
	}

	var parsedServerID pgtype.UUID
	if input.ServerID != "" {
		if sid, parseErr := uuid.Parse(input.ServerID); parseErr == nil {
			parsedServerID = pgtype.UUID{Bytes: sid, Valid: true}
		}
	}

	// One transaction: a service created with basic auth or an allowlist is
	// never stored without them. A savepoint inside a create with secrets.
	tx, err := dbtx.On(ctx, r.db).Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("CreateScoped service: begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck — a no-op after Commit

	err = tx.QueryRow(ctx, insertQ,
		string(scope), projectID, orgID,
		parsedServerID,
		input.Name, string(input.Type), string(input.SourceType),
		optString(input.GitRepoURL), input.GitBranch, input.BaseDirectory,
		optString(input.DockerImage),
		optString(input.InstallCommand), optString(input.BuildCommand), optString(input.StartCommand),
		input.BuildPack,
		int32(input.Port), int32(1),
		optString(input.CPULimit), optString(input.MemoryLimit),
		optString(input.ComposeContent),
		input.ComposeRepoURL, input.ComposeFilePath,
		optString(input.RegistryServer), optString(input.RegistryUsername),
		encPass,
		optString(input.AddonSlug), optString(input.NetworkAlias),
		marshalVolumes(input.Volumes),
		optString(input.GitUsername), encGitToken,
		input.PreDeployCommands, input.PostDeployCommands,
		healthCheckPath, int32(healthCheckTimeout), int32(input.HealthCheckRetries),
		input.RequiredEnvVars, input.MigrateCommand, marshalDependencies(input.Dependencies),
		input.ComposeRouteService, newID,
	).Scan(&newID)
	if err != nil {
		if isDuplicateNameError(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("CreateScoped service: %w", err)
	}
	if err := r.setAccessControl(ctx, sqlcdb.New(tx), newID, input); err != nil {
		return nil, fmt.Errorf("CreateScoped service: %w", err)
	}
	if err := setSource(ctx, sqlcdb.New(tx), newID, input); err != nil {
		return nil, fmt.Errorf("CreateScoped service: %w", err)
	}
	if input.ContainerHealth != "" {
		if err := setContainerHealth(ctx, sqlcdb.New(tx), newID, input.ContainerHealth, input.ContainerHealthCommand); err != nil {
			return nil, fmt.Errorf("CreateScoped service: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("CreateScoped service: commit: %w", err)
	}

	return r.GetByID(ctx, uuidString(newID))
}

func (r *PostgresRepository) UpdateConfig(ctx context.Context, id string, input UpdateConfigInput) (*Service, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid service id %q: %w", id, err)
	}
	// Before the URLs move: a failure leaves them where the credential was
	// saved for.
	if input.ClearGitToken || input.ClearRegistryPassword {
		if err := r.queries.ClearServiceCredentials(ctx, sqlcdb.ClearServiceCredentialsParams{
			GitToken: input.ClearGitToken, RegistryPassword: input.ClearRegistryPassword, ID: pgtype.UUID{Bytes: parsedID, Valid: true},
		}); err != nil {
			return nil, fmt.Errorf("clear credentials: %w", err)
		}
	}

	// compose_route_service: a nil input.ComposeRouteService (not sent) is
	// NULL and keeps the stored choice; "" switches back to automatic.
	// addon_slug is not here: it is the catalog item the service was created
	// from, and no settings form sends it (a save used to clear it).
	const q = `
		UPDATE services SET
			name                 = CASE WHEN $2::text = '' THEN name ELSE $2::text END,
			git_repo_url         = $3,
			git_branch           = $4,
			base_directory       = $5,
			docker_image         = $6,
			install_command      = $7,
			build_command        = $8,
			start_command        = $9,
			build_pack           = $10,
			port                 = $11,
			replicas             = $12,
			cpu_limit            = $13,
			memory_limit         = $14,
			compose_content      = $15,
			compose_repo_url     = $16,
			compose_file_path    = $17,
			registry_server      = $18,
			registry_username    = $19,
			network_alias        = $20,
			volumes              = $21,
			git_username         = $22,
			pre_deploy_commands  = $23,
			post_deploy_commands = $24,
			health_check_path    = $25,
			health_check_timeout = $26,
			health_check_retries = $27,
			required_env_vars    = $28,
			migrate_command      = $29,
			dependencies         = $30,
			basic_auth_user      = $31,
			-- No user turns basic auth off: its password goes with it.
			basic_auth_password  = CASE WHEN $31::text = '' THEN '' ELSE basic_auth_password END,
			ip_allowlist         = $32,
			compose_route_service = COALESCE($33::text, compose_route_service),
			deploy_strategy      = COALESCE(NULLIF($34::text, ''), deploy_strategy),
			updated_at           = NOW()
		WHERE id = $1`

	if _, err := r.db.Exec(ctx, q,
		pgtype.UUID{Bytes: parsedID, Valid: true},
		input.Name,
		optString(input.GitRepoURL), input.GitBranch, input.BaseDirectory,
		optString(input.DockerImage),
		optString(input.InstallCommand), optString(input.BuildCommand), optString(input.StartCommand),
		input.BuildPack,
		int32(input.Port), int32(1),
		optString(input.CPULimit), optString(input.MemoryLimit),
		optString(input.ComposeContent),
		input.ComposeRepoURL, input.ComposeFilePath,
		optString(input.RegistryServer), optString(input.RegistryUsername),
		optString(input.NetworkAlias),
		marshalVolumes(input.Volumes),
		optString(input.GitUsername),
		input.PreDeployCommands, input.PostDeployCommands,
		input.HealthCheckPath, int32(input.HealthCheckTimeout), int32(input.HealthCheckRetries),
		input.RequiredEnvVars, input.MigrateCommand, marshalDependencies(input.Dependencies),
		input.BasicAuthUser, marshalIPAllowlist(input.IPAllowlist),
		input.ComposeRouteService, input.DeployStrategy,
	); err != nil {
		if isDuplicateNameError(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("UpdateConfig: %w", err)
	}

	// Update passwords separately only when new values are provided.
	if input.RegistryPassword != "" {
		encPass, err := r.encryptPassword("registry_password", pgtype.UUID{Bytes: parsedID, Valid: true}, input.RegistryPassword)
		if err != nil {
			return nil, fmt.Errorf("encrypt registry password: %w", err)
		}
		if _, err := r.db.Exec(ctx,
			`UPDATE services SET registry_password = $2, updated_at = NOW() WHERE id = $1`,
			pgtype.UUID{Bytes: parsedID, Valid: true}, encPass,
		); err != nil {
			return nil, fmt.Errorf("update registry password: %w", err)
		}
	}

	if input.GitToken != "" {
		encToken, err := r.encryptPassword("git_token", pgtype.UUID{Bytes: parsedID, Valid: true}, input.GitToken)
		if err != nil {
			return nil, fmt.Errorf("encrypt git token: %w", err)
		}
		if _, err := r.db.Exec(ctx,
			`UPDATE services SET git_token = $2, updated_at = NOW() WHERE id = $1`,
			pgtype.UUID{Bytes: parsedID, Valid: true}, encToken,
		); err != nil {
			return nil, fmt.Errorf("update git token: %w", err)
		}
	}

	if input.BasicAuthPassword != "" {
		encPass, err := r.encryptPassword("basic_auth_password", pgtype.UUID{Bytes: parsedID, Valid: true}, input.BasicAuthPassword)
		if err != nil {
			return nil, fmt.Errorf("encrypt basic auth password: %w", err)
		}
		if _, err := r.db.Exec(ctx,
			`UPDATE services SET basic_auth_password = $2, updated_at = NOW() WHERE id = $1`,
			pgtype.UUID{Bytes: parsedID, Valid: true}, encPass,
		); err != nil {
			return nil, fmt.Errorf("update basic auth password: %w", err)
		}
	}

	return r.GetByID(ctx, id)
}

func (r *PostgresRepository) Delete(ctx context.Context, id string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}
	if err := r.queries.DeleteService(ctx, pgtype.UUID{Bytes: parsedID, Valid: true}); err != nil {
		return fmt.Errorf("DeleteService: %w", err)
	}
	return nil
}

// ListIDsIn returns the services of an environment (ScopeEnvironment), a
// project (ScopeProject: every environment's and its shared services) or a
// workspace (ScopeOrg: its projects' and its own shared services).
func (r *PostgresRepository) ListIDsIn(ctx context.Context, scope Scope, id string) ([]string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid %s id %q: %w", scope, id, err)
	}
	pid := pgtype.UUID{Bytes: parsed, Valid: true}
	var rows []pgtype.UUID
	switch scope {
	case ScopeEnvironment:
		rows, err = r.queries.ListServiceIDsInEnvironment(ctx, pid)
	case ScopeProject:
		rows, err = r.queries.ListServiceIDsInProject(ctx, pid)
	case ScopeOrg:
		rows, err = r.queries.ListServiceIDsInOrg(ctx, pid)
	default:
		return nil, fmt.Errorf("ListIDsIn: unknown scope %q", scope)
	}
	if err != nil {
		return nil, fmt.Errorf("ListIDsIn %s: %w", scope, err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, uuidString(row))
	}
	return ids, nil
}

// ListData returns the services given and their previews' copies, with
// what names their data on their servers.
func (r *PostgresRepository) ListData(ctx context.Context, ids []string) ([]DataRow, error) {
	parsed := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		u, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("invalid service id %q: %w", id, err)
		}
		parsed = append(parsed, pgtype.UUID{Bytes: u, Valid: true})
	}
	rows, err := r.queries.ListServiceData(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("ListData: %w", err)
	}
	out := make([]DataRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, DataRow{
			ID:         uuidString(row.ID),
			Name:       row.Name,
			ServerID:   uuidString(row.ServerID),
			ServerName: row.ServerName,
			Volumes:    parseVolumes(row.Volumes),
		})
	}
	return out, nil
}

func (r *PostgresRepository) CheckOwnership(ctx context.Context, id, orgID string) (bool, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return false, fmt.Errorf("invalid service id %q: %w", id, err)
	}
	parsedOrgID, err := uuid.Parse(orgID)
	if err != nil {
		return false, fmt.Errorf("invalid org id %q: %w", orgID, err)
	}

	var exists bool
	err = r.db.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM services s
			LEFT JOIN environments e ON s.environment_id = e.id
			LEFT JOIN projects p_env ON e.project_id = p_env.id
			WHERE s.id = $1 AND (
				(s.scope = 'environment' AND p_env.org_id = $2)
				OR (s.scope = 'project' AND s.project_id IN (
					SELECT id FROM projects WHERE org_id = $2
				))
				OR (s.scope = 'org' AND s.scoped_org_id = $2)
			)
		)`,
		pgtype.UUID{Bytes: parsedID, Valid: true},
		pgtype.UUID{Bytes: parsedOrgID, Valid: true},
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("CheckOwnership: %w", err)
	}
	return exists, nil
}

func (r *PostgresRepository) UpdateRuntimeStatus(ctx context.Context, id, status, activeDeploymentID string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}

	var activeDepID pgtype.UUID
	if activeDeploymentID != "" {
		parsed, err := uuid.Parse(activeDeploymentID)
		if err != nil {
			return fmt.Errorf("invalid active_deployment_id %q: %w", activeDeploymentID, err)
		}
		activeDepID = pgtype.UUID{Bytes: parsed, Valid: true}
	}

	if err := r.queries.UpdateServiceRuntimeStatus(ctx, sqlcdb.UpdateServiceRuntimeStatusParams{
		ID:                 pgtype.UUID{Bytes: parsedID, Valid: true},
		RuntimeStatus:      status,
		ActiveDeploymentID: activeDepID,
	}); err != nil {
		return fmt.Errorf("UpdateServiceRuntimeStatus: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateDockerImage(ctx context.Context, id, image string, clearRegistryPassword bool) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}
	return r.queries.SetServiceDockerImage(ctx, sqlcdb.SetServiceDockerImageParams{
		DockerImage: &image, ClearRegistryPassword: clearRegistryPassword, ID: pgtype.UUID{Bytes: parsedID, Valid: true},
	})
}

func (r *PostgresRepository) SetComposeNetworkService(ctx context.Context, id, name string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}
	if err := r.queries.SetServiceComposeNetworkService(ctx, sqlcdb.SetServiceComposeNetworkServiceParams{
		ComposeNetworkService: name, ID: pgtype.UUID{Bytes: parsedID, Valid: true},
	}); err != nil {
		return fmt.Errorf("SetServiceComposeNetworkService: %w", err)
	}
	return nil
}

func (r *PostgresRepository) SwapDockerImage(ctx context.Context, id, from, image, registryPassword string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}
	pid := pgtype.UUID{Bytes: parsedID, Valid: true}
	encPass, err := r.encryptPassword("registry_password", pid, registryPassword)
	if err != nil {
		return fmt.Errorf("encrypt registry password: %w", err)
	}
	if err := r.queries.SwapServiceDockerImage(ctx, sqlcdb.SwapServiceDockerImageParams{
		DockerImage: &image, RegistryPassword: encPass, ID: pid, FromImage: from,
	}); err != nil {
		return fmt.Errorf("SwapServiceDockerImage: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateGitBranch(ctx context.Context, id, branch string) error {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}
	_, err = r.db.Exec(ctx,
		"UPDATE services SET git_branch = $1, updated_at = NOW() WHERE id = $2",
		branch, pgtype.UUID{Bytes: parsedID, Valid: true},
	)
	return err
}

func (r *PostgresRepository) AssignServer(ctx context.Context, serviceID, serverID string) (*Service, error) {
	parsedServiceID, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}

	var serverUUID pgtype.UUID
	if serverID != "" {
		parsed, err := uuid.Parse(serverID)
		if err != nil {
			return nil, fmt.Errorf("invalid server id %q: %w", serverID, err)
		}
		serverUUID = pgtype.UUID{Bytes: parsed, Valid: true}
	}

	if _, err := r.db.Exec(ctx,
		// The servers it leaves and joins raise their spec_generation (D-111).
		`WITH prev AS (
			SELECT server_id FROM services WHERE id = $1 AND server_id IS DISTINCT FROM $2::uuid
		), moved AS (
			UPDATE services SET server_id = $2, updated_at = NOW() WHERE id = $1
		)
		UPDATE servers SET spec_generation = spec_generation + 1
		WHERE id IN (SELECT server_id FROM prev) OR (id = $2 AND EXISTS (SELECT 1 FROM prev))`,
		pgtype.UUID{Bytes: parsedServiceID, Valid: true}, serverUUID,
	); err != nil {
		return nil, fmt.Errorf("AssignServer: %w", err)
	}
	return r.GetByID(ctx, serviceID)
}

// FindByProjectRepoAndBranch returns projectID's services that deploy on
// push from repoURL's branch, or from the project's repository repositoryID
// (D-77): its environments' services and its project-shared ones.
// Workspace-shared services belong to no project, so a push never reaches them.
func (r *PostgresRepository) FindByProjectRepoAndBranch(ctx context.Context, projectID, repoURL, repositoryID, branch string) ([]Service, error) {
	parsedProjectID, err := uuid.Parse(projectID)
	if err != nil {
		return nil, fmt.Errorf("invalid project id %q: %w", projectID, err)
	}
	rows, err := r.db.Query(ctx,
		svcJoinSelect+` WHERE COALESCE(e.project_id, s.project_id) = $1 AND s.scope <> 'org'
		  AND (s.git_repo_url = $2 OR s.repository_id = $4) AND s.git_branch = $3 AND s.deploy_on_push`,
		pgtype.UUID{Bytes: parsedProjectID, Valid: true}, optString(repoURL), branch, optUUIDString(repositoryID),
	)
	if err != nil {
		return nil, fmt.Errorf("FindByProjectRepoAndBranch: %w", err)
	}
	defer rows.Close()

	result := make([]Service, 0)
	for rows.Next() {
		svc, err := r.scanServiceRow(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("FindByProjectRepoAndBranch scan: %w", err)
		}
		result = append(result, svc)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) MoveToWorkspace(ctx context.Context, id, orgID string) (bool, error) {
	sid, err := uuid.Parse(id)
	if err != nil {
		return false, fmt.Errorf("invalid service id: %w", err)
	}
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return false, fmt.Errorf("invalid org id: %w", err)
	}
	n, err := sqlcdb.New(dbtx.On(ctx, r.db)).MoveServiceToWorkspace(ctx, sqlcdb.MoveServiceToWorkspaceParams{
		ID: pgtype.UUID{Bytes: sid, Valid: true}, OrgID: pgtype.UUID{Bytes: oid, Valid: true},
	})
	if isDuplicateNameError(err) {
		return false, ErrDuplicateName
	}
	if err != nil {
		return false, fmt.Errorf("MoveServiceToWorkspace: %w", err)
	}
	return n == 1, nil
}

func (r *PostgresRepository) AliasUserInWorkspace(ctx context.Context, orgID, id, alias string) (string, error) {
	sid, err := uuid.Parse(id)
	if err != nil {
		return "", fmt.Errorf("invalid service id: %w", err)
	}
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return "", fmt.Errorf("invalid org id: %w", err)
	}
	names, err := r.queries.WorkspaceAliasUser(ctx, sqlcdb.WorkspaceAliasUserParams{
		Alias: &alias, ID: pgtype.UUID{Bytes: sid, Valid: true}, OrgID: pgtype.UUID{Bytes: oid, Valid: true},
	})
	if err != nil {
		return "", fmt.Errorf("WorkspaceAliasUser: %w", err)
	}
	if len(names) == 0 {
		return "", nil
	}
	return names[0], nil
}

func isDuplicateNameError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ListWorkspaceServices returns every service of the workspace orgID.
func (r *PostgresRepository) ListWorkspaceServices(ctx context.Context, orgID string) ([]WorkspaceService, error) {
	id, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace id %q: %w", orgID, err)
	}
	rows, err := r.queries.ListWorkspaceServices(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListWorkspaceServices: %w", err)
	}
	out := make([]WorkspaceService, 0, len(rows))
	for _, row := range rows {
		out = append(out, WorkspaceService{
			ID: uuidString(row.ID), Name: row.Name, Scope: Scope(row.Scope),
			ProjectID: uuidString(row.ProjectID), EnvironmentID: uuidString(row.EnvironmentID),
			ServerID:      uuidString(row.ServerID),
			Port:          int(row.Port),
			RuntimeStatus: PresentedStatus(row.RuntimeStatus, row.Deploying, row.Down, row.Unhealthy),
			SourceType:    row.SourceType,
			AddonSlug:     row.AddonSlug, DockerImage: row.DockerImage, GitRepoURL: row.GitRepoUrl,
			GitBranch: row.GitBranch, EnvironmentName: row.EnvironmentName,
		})
	}
	return out, nil
}

// RenameTemporaryAddresses moves the service's sslip.io names from fromIP
// to toIP (D-150): a moved service's temporary address follows it.
func (r *PostgresRepository) RenameTemporaryAddresses(ctx context.Context, serviceID, fromIP, toIP string) (int64, error) {
	id, err := uuid.Parse(serviceID)
	if err != nil {
		return 0, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}
	n, err := r.queries.RenameTemporaryDomains(ctx, sqlcdb.RenameTemporaryDomainsParams{
		ServiceID: pgtype.UUID{Bytes: id, Valid: true}, OldSuffix: domain.TemporarySuffix(fromIP), NewSuffix: domain.TemporarySuffix(toIP)})
	if err != nil {
		return 0, fmt.Errorf("RenameTemporaryDomains: %w", err)
	}
	return n, nil
}

// AssignLoopbackPort gives serviceID, when it is the catalog Loki, its
// 127.0.0.1 port (D-148): the one it has, else the lowest free one on the
// instance; 0 for any other service. ErrNoLoopbackPort when 39100-39199 are
// all taken. Two Lokis deploying at once can pick the same port: the loser
// picks again.
func (r *PostgresRepository) AssignLoopbackPort(ctx context.Context, serviceID string) (int, error) {
	id, err := uuid.Parse(serviceID)
	if err != nil {
		return 0, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}
	var port *int32
	for range 3 {
		if port, err = r.queries.AssignLoopbackPort(ctx, pgtype.UUID{Bytes: id, Valid: true}); !isDuplicateNameError(err) {
			break
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil // not the catalog Loki
	}
	if err != nil {
		return 0, fmt.Errorf("AssignLoopbackPort: %w", err)
	}
	if port == nil {
		return 0, ErrNoLoopbackPort
	}
	return int(*port), nil
}

// DesiredOn lists the services on serverID as its agent's desired state
// (D-111; server.DesiredServices).
func (r *PostgresRepository) DesiredOn(ctx context.Context, serverID string) ([]agentproto.DesiredService, error) {
	id, err := uuid.Parse(serverID)
	if err != nil {
		return nil, fmt.Errorf("invalid server id %q: %w", serverID, err)
	}
	rows, err := r.queries.ListDesiredServices(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListDesiredServices: %w", err)
	}
	out := make([]agentproto.DesiredService, 0, len(rows))
	for _, row := range rows {
		target := observability.LokiTarget{URL: row.LokiUrl, ServiceID: uuidString(row.LokiServiceID), ServerID: uuidString(row.LokiServerID)}
		if row.LoopbackPort != nil {
			target.Port = int(*row.LoopbackPort)
		}
		loki := target.ShipTo(serverID, uuidString(row.ID))
		out = append(out, agentproto.DesiredService{
			ID:         uuidString(row.ID),
			Compose:    SourceType(row.SourceType) == SourceTypeDockerCompose,
			Running:    row.RuntimeStatus == "running",
			Deployment: uuidString(row.ActiveDeploymentID),
			Loki:       loki,
		})
	}
	return out, nil
}

func uuidString(p pgtype.UUID) string {
	val, _ := p.Value()
	if val == nil {
		return ""
	}
	return val.(string)
}

// marshalVolumes encodes a []VolumeMount slice to JSON text; returns "[]" on nil/empty.
func marshalVolumes(vols []VolumeMount) string {
	if len(vols) == 0 {
		return "[]"
	}
	b, err := json.Marshal(vols)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// parseVolumes decodes a JSON text column into a []VolumeMount; always returns a non-nil slice.
func parseVolumes(s string) []VolumeMount {
	result := make([]VolumeMount, 0)
	if s == "" || s == "[]" {
		return result
	}
	_ = json.Unmarshal([]byte(s), &result)
	return result
}

// marshalDependencies encodes a []string slice to JSON text; returns "[]" on nil/empty.
func marshalDependencies(deps []string) string {
	if len(deps) == 0 {
		return "[]"
	}
	b, err := json.Marshal(deps)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// marshalIPAllowlist encodes a []string slice to JSON text; returns "[]" on nil/empty.
func marshalIPAllowlist(ips []string) string {
	if len(ips) == 0 {
		return "[]"
	}
	b, err := json.Marshal(ips)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// parseIPAllowlist decodes a JSON text column into a []string; always returns a non-nil slice.
func parseIPAllowlist(s string) []string {
	result := make([]string, 0)
	if s == "" || s == "[]" {
		return result
	}
	_ = json.Unmarshal([]byte(s), &result)
	return result
}

// parseDependencies decodes a JSON text column into a []string; always returns a non-nil slice.
func parseDependencies(s string) []string {
	result := make([]string, 0)
	if s == "" || s == "[]" {
		return result
	}
	_ = json.Unmarshal([]byte(s), &result)
	return result
}

// ListRunningMetricsTargets returns every service with runtime_status =
// 'running': its ID, name and server ("" when it runs on the local host).
func (r *PostgresRepository) ListRunningMetricsTargets(ctx context.Context) ([]MetricsTarget, error) {
	rows, err := r.queries.ListRunningServicesForMetrics(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListRunningServicesForMetrics: %w", err)
	}
	out := make([]MetricsTarget, 0, len(rows))
	for _, row := range rows {
		t := MetricsTarget{ID: uuidString(row.ID), Name: row.Name, ServerID: uuidString(row.ServerID), Preview: row.Preview, Routed: row.Routed}
		if row.SourceType != string(SourceTypeDockerCompose) {
			t.ContainerHealth = row.ContainerHealth
		}
		if row.HealthCheckRetries > 0 && row.Port > 0 && row.SourceType != string(SourceTypeDockerCompose) && row.HealthCheckPath != "" {
			t.HealthPath, t.Port, t.HealthTimeout = row.HealthCheckPath, int(row.Port), int(row.HealthCheckTimeout)
		}
		out = append(out, t)
	}
	return out, nil
}

func (r *PostgresRepository) ListResourceFieldsOnServer(ctx context.Context, orgID, serverID string) ([]Service, error) {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return nil, fmt.Errorf("invalid server id %q: %w", serverID, err)
	}
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return nil, fmt.Errorf("invalid org id %q: %w", orgID, err)
	}
	rows, err := r.queries.ListServicesOnServerForResources(ctx, sqlcdb.ListServicesOnServerForResourcesParams{
		ServerID: pgtype.UUID{Bytes: sid, Valid: true}, OrgID: pgtype.UUID{Bytes: oid, Valid: true}})
	if err != nil {
		return nil, fmt.Errorf("ListServicesOnServerForResources: %w", err)
	}
	out := make([]Service, 0, len(rows))
	for _, row := range rows {
		out = append(out, Service{
			ID: uuidString(row.ID), Name: row.Name, SourceType: SourceType(row.SourceType),
			MemoryLimit: row.MemoryLimit, CPULimit: row.CpuLimit, AddonSlug: row.AddonSlug, DockerImage: row.DockerImage,
			ComposeContent: row.ComposeContent, ComposeRepoURL: row.ComposeRepoUrl,
		})
	}
	return out, nil
}

// InsertSample records one compose service's sample, collected at at.
func (r *PostgresRepository) InsertSample(ctx context.Context, serviceID, composeService string, at time.Time, m MetricSample) error {
	sid, err := uuid.Parse(serviceID)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}
	var restarts *int32
	if m.Restarts != nil {
		n := int32(*m.Restarts)
		restarts = &n
	}
	if err := r.queries.InsertServiceMetric(ctx, sqlcdb.InsertServiceMetricParams{
		ServiceID:      pgtype.UUID{Bytes: sid, Valid: true},
		ComposeService: composeService,
		CpuPercent:     numeric(&m.CPUPercent),
		MemoryMb:       numeric(&m.MemoryMB),
		MemoryPeakMb:   numeric(m.MemoryPeakMB),
		ThrottledPct:   numeric(m.ThrottledPct),
		CpuWaitPct:     numeric(m.CPUWaitPct),
		CollectedAt:    pgtype.Timestamptz{Time: at, Valid: true},
		Restarts:       restarts,
	}); err != nil {
		return fmt.Errorf("InsertServiceMetric: %w", err)
	}
	return nil
}

// numeric is f as a NUMERIC parameter, rounded to the columns' two
// decimals; NULL when f is nil.
func numeric(f *float64) pgtype.Numeric {
	var n pgtype.Numeric
	if f != nil {
		_ = n.Scan(strconv.FormatFloat(*f, 'f', 2, 64)) // always a decimal string
	}
	return n
}

// ListMetrics returns a service's chart points collected from since on,
// oldest first: one per collector cycle, its compose services summed, or
// only composeService's when it is set.
func (r *PostgresRepository) ListMetrics(ctx context.Context, serviceID, composeService string, since time.Time) ([]MetricPoint, error) {
	sid, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}
	rows, err := r.queries.ListServiceMetrics(ctx, sqlcdb.ListServiceMetricsParams{
		ServiceID:      pgtype.UUID{Bytes: sid, Valid: true},
		Since:          pgtype.Timestamptz{Time: since, Valid: true},
		ComposeService: composeService,
	})
	if err != nil {
		return nil, fmt.Errorf("ListServiceMetrics: %w", err)
	}
	result := make([]MetricPoint, 0, len(rows))
	for _, row := range rows {
		result = append(result, MetricPoint{
			Timestamp: row.CollectedAt.Time, CPUPercent: row.CpuPercent, MemoryMB: row.MemoryMb, ThrottledPct: row.ThrottledPct,
		})
	}
	return result, nil
}

// ListDailyMetrics returns a service's week, one point per UTC day: the 6
// days before today summarised, then today from its samples; its compose
// services summed, or only composeService's when it is set.
func (r *PostgresRepository) ListDailyMetrics(ctx context.Context, serviceID, composeService string, today time.Time) ([]MetricPoint, error) {
	sid, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}
	rows, err := r.queries.ListServiceMetricsDaily(ctx, sqlcdb.ListServiceMetricsDailyParams{
		ServiceID:      pgtype.UUID{Bytes: sid, Valid: true},
		Today:          pgtype.Date{Time: today, Valid: true},
		ComposeService: composeService,
	})
	if err != nil {
		return nil, fmt.Errorf("ListServiceMetricsDaily: %w", err)
	}
	result := make([]MetricPoint, 0, len(rows))
	for _, row := range rows {
		result = append(result, MetricPoint{
			Timestamp: row.DayStart.Time, CPUPercent: row.CpuPercent, MemoryMB: row.MemoryMb, ThrottledPct: row.ThrottledPct,
			LargestContainer: row.LargestContainer,
		})
	}
	return result, nil
}

// DeleteOldMetrics removes all metric rows collected before the given cutoff.
func (r *PostgresRepository) DeleteOldMetrics(ctx context.Context, before time.Time) error {
	if err := r.queries.DeleteServiceMetricsBefore(ctx, pgtype.Timestamptz{Time: before, Valid: true}); err != nil {
		return fmt.Errorf("DeleteServiceMetricsBefore: %w", err)
	}
	return nil
}

// NewestMetricOn returns when the newest sample of a server's services was
// collected; zero for none.
func (r *PostgresRepository) NewestMetricOn(ctx context.Context, serverID string) (time.Time, error) {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid server id %q: %w", serverID, err)
	}
	newest, err := r.queries.NewestServerMetricAt(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		return time.Time{}, fmt.Errorf("NewestServerMetricAt: %w", err)
	}
	if !newest.Valid {
		return time.Time{}, nil
	}
	return newest.Time, nil
}

// allocationQueries is the slice of sqlcdb.Queries the capacity totals use,
// so the totals can be tested with a fake.
type allocationQueries interface {
	SumAllocatedForServer(ctx context.Context, serverID pgtype.UUID) (sqlcdb.SumAllocatedForServerRow, error)
	SumAllocatedForServerOrg(ctx context.Context, arg sqlcdb.SumAllocatedForServerOrgParams) (sqlcdb.SumAllocatedForServerOrgRow, error)
	ListLimitStringsForServer(ctx context.Context, serverID pgtype.UUID) ([]sqlcdb.ListLimitStringsForServerRow, error)
	ListLimitStringsForServerOrg(ctx context.Context, arg sqlcdb.ListLimitStringsForServerOrgParams) ([]sqlcdb.ListLimitStringsForServerOrgRow, error)
}

// SumAllocatedForServer returns the total cpu (cores) and memory (MB) committed
// by all non-stopped services assigned to the given server: single-container
// services' limit strings (parsed here) plus compose stacks' panel limits
// (summed in SQL).
//
// Counted as 0: a single-container service with a memory_limit exception and
// no limit (the deploy policy lets it run unlimited), and compose limits set
// only in the compose file (they are not stored; reading every stack's file
// per check is not affordable).
func (r *PostgresRepository) SumAllocatedForServer(ctx context.Context, serverID string) (float64, float64, error) {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid server id format: %w", err)
	}
	return sumAllocatedForServer(ctx, r.queries, pgtype.UUID{Bytes: sid, Valid: true})
}

// WorkspaceAllocations splits what SumAllocatedForServer adds up by
// workspace, with each workspace's projects and service count. Same
// zero-count rules.
func (r *PostgresRepository) WorkspaceAllocations(ctx context.Context, serverID string) ([]server.WorkspaceAllocation, error) {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return nil, fmt.Errorf("invalid server id format: %w", err)
	}
	rows, err := r.queries.ListServerAllocations(ctx, pgtype.UUID{Bytes: sid, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("ListServerAllocations: %w", err)
	}
	return groupAllocations(rows), nil
}

// groupAllocations sums the rows (ordered by workspace) per workspace.
func groupAllocations(rows []sqlcdb.ListServerAllocationsRow) []server.WorkspaceAllocation {
	out := make([]server.WorkspaceAllocation, 0, 2)
	for _, row := range rows {
		org := uuidString(row.OrgID)
		if len(out) == 0 || out[len(out)-1].OrgID != org {
			out = append(out, server.WorkspaceAllocation{OrgID: org, OrgName: row.OrgName, Projects: make([]string, 0, 1)})
		}
		w := &out[len(out)-1]
		w.Services++
		if row.ProjectName != "" && (len(w.Projects) == 0 || w.Projects[len(w.Projects)-1] != row.ProjectName) {
			w.Projects = append(w.Projects, row.ProjectName) // rows are ordered by project too
		}
		if row.SourceType == string(SourceTypeDockerCompose) {
			w.CPUCores += row.ComposeCpuCores
			w.MemoryMB += float64(row.ComposeMemoryMb)
			continue
		}
		cpu, mem := sumLimitStrings(1, func(int) (*string, *string) { return row.CpuLimit, row.MemoryLimit })
		w.CPUCores += cpu
		w.MemoryMB += mem
	}
	return out
}

// SumAllocatedForServerOrg is SumAllocatedForServer for one workspace's
// services (by their project's workspace, or their own when shared by the
// workspace): its use of a shared server's share. Same zero-count rules.
func (r *PostgresRepository) SumAllocatedForServerOrg(ctx context.Context, serverID, orgID string) (float64, float64, error) {
	sid, err := uuid.Parse(serverID)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid server id format: %w", err)
	}
	oid, err := uuid.Parse(orgID)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid org id format: %w", err)
	}
	return sumAllocatedForServerOrg(ctx, r.queries,
		pgtype.UUID{Bytes: sid, Valid: true}, pgtype.UUID{Bytes: oid, Valid: true})
}

func sumAllocatedForServer(ctx context.Context, q allocationQueries, sid pgtype.UUID) (float64, float64, error) {
	rows, err := q.ListLimitStringsForServer(ctx, sid)
	if err != nil {
		return 0, 0, fmt.Errorf("ListLimitStringsForServer: %w", err)
	}
	cpu, mem := sumLimitStrings(len(rows), func(i int) (*string, *string) { return rows[i].CpuLimit, rows[i].MemoryLimit })
	compose, err := q.SumAllocatedForServer(ctx, sid)
	if err != nil {
		return 0, 0, fmt.Errorf("SumAllocatedForServer: %w", err)
	}
	return cpu + compose.ComposeCpuCores, mem + float64(compose.ComposeMemoryMb), nil
}

func sumAllocatedForServerOrg(ctx context.Context, q allocationQueries, sid, oid pgtype.UUID) (float64, float64, error) {
	rows, err := q.ListLimitStringsForServerOrg(ctx, sqlcdb.ListLimitStringsForServerOrgParams{ServerID: sid, OrgID: oid})
	if err != nil {
		return 0, 0, fmt.Errorf("ListLimitStringsForServerOrg: %w", err)
	}
	cpu, mem := sumLimitStrings(len(rows), func(i int) (*string, *string) { return rows[i].CpuLimit, rows[i].MemoryLimit })
	compose, err := q.SumAllocatedForServerOrg(ctx, sqlcdb.SumAllocatedForServerOrgParams{ServerID: sid, OrgID: oid})
	if err != nil {
		return 0, 0, fmt.Errorf("SumAllocatedForServerOrg: %w", err)
	}
	return cpu + compose.ComposeCpuCores, mem + float64(compose.ComposeMemoryMb), nil
}

// sumLimitStrings parses n services' cpu and memory limit strings; an empty
// or unparseable string adds 0.
func sumLimitStrings(n int, at func(i int) (cpu, mem *string)) (cpuCores, memMB float64) {
	for i := 0; i < n; i++ {
		c, m := at(i)
		if c != nil {
			if v, err := strconv.ParseFloat(*c, 64); err == nil {
				cpuCores += v
			}
		}
		if m != nil {
			memMB += ParseMemoryMB(*m)
		}
	}
	return cpuCores, memMB
}

// optUUIDString is id as a UUID, NULL when it isn't one.
func optUUIDString(id string) pgtype.UUID {
	u, err := uuid.Parse(id)
	return pgtype.UUID{Bytes: u, Valid: err == nil}
}

func (r *PostgresRepository) PidsLimit(ctx context.Context, id string) (int, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return 0, fmt.Errorf("invalid service id %q: %w", id, err)
	}
	n, err := r.queries.GetServicePidsLimit(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("GetServicePidsLimit: %w", err)
	}
	return int(n), nil
}

func (r *PostgresRepository) SetPidsLimit(ctx context.Context, id string, n int) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}
	var v *int32
	if n > 0 {
		x := int32(n)
		v = &x
	}
	if err := r.queries.SetServicePidsLimit(ctx, sqlcdb.SetServicePidsLimitParams{PidsLimit: v, ID: pgtype.UUID{Bytes: parsed, Valid: true}}); err != nil {
		return fmt.Errorf("SetServicePidsLimit: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ContainerHealth(ctx context.Context, id string) (string, string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", "", fmt.Errorf("invalid service id %q: %w", id, err)
	}
	row, err := r.queries.GetServiceContainerHealth(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if err != nil {
		return "", "", fmt.Errorf("GetServiceContainerHealth: %w", err)
	}
	return row.ContainerHealth, row.ContainerHealthCommand, nil
}

func (r *PostgresRepository) SetContainerHealth(ctx context.Context, id, mode, command string) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}
	return setContainerHealth(ctx, r.queries, pgtype.UUID{Bytes: parsed, Valid: true}, mode, command)
}

// setContainerHealth stores a service's container check; a new service's
// in its create's transaction.
func setContainerHealth(ctx context.Context, q *sqlcdb.Queries, id pgtype.UUID, mode, command string) error {
	if err := q.SetServiceContainerHealth(ctx, sqlcdb.SetServiceContainerHealthParams{
		ID: id, ContainerHealth: mode, ContainerHealthCommand: command}); err != nil {
		return fmt.Errorf("SetServiceContainerHealth: %w", err)
	}
	return nil
}

func (r *PostgresRepository) RollbackImagesKept(ctx context.Context, id string) (int, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return 0, fmt.Errorf("invalid service id %q: %w", id, err)
	}
	n, err := r.queries.GetServiceRollbackImagesKept(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("GetServiceRollbackImagesKept: %w", err)
	}
	return int(n), nil
}

func (r *PostgresRepository) SetRollbackImagesKept(ctx context.Context, id string, n int) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}
	var v *int16
	if n > 0 {
		x := int16(n)
		v = &x
	}
	if err := r.queries.SetServiceRollbackImagesKept(ctx, sqlcdb.SetServiceRollbackImagesKeptParams{RollbackImagesKept: v, ID: pgtype.UUID{Bytes: parsed, Valid: true}}); err != nil {
		return fmt.Errorf("SetServiceRollbackImagesKept: %w", err)
	}
	return nil
}

func (r *PostgresRepository) DeployTimeout(ctx context.Context, id string) (int, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return 0, fmt.Errorf("invalid service id %q: %w", id, err)
	}
	n, err := r.queries.GetServiceDeployTimeout(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("GetServiceDeployTimeout: %w", err)
	}
	return int(n), nil
}

func (r *PostgresRepository) SetDeployTimeout(ctx context.Context, id string, minutes int) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}
	var v *int32
	if minutes > 0 {
		x := int32(minutes)
		v = &x
	}
	if err := r.queries.SetServiceDeployTimeout(ctx, sqlcdb.SetServiceDeployTimeoutParams{DeployTimeoutMinutes: v, ID: pgtype.UUID{Bytes: parsed, Valid: true}}); err != nil {
		return fmt.Errorf("SetServiceDeployTimeout: %w", err)
	}
	return nil
}

func (r *PostgresRepository) Isolation(ctx context.Context, id string) (string, string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", "", fmt.Errorf("invalid service id %q: %w", id, err)
	}
	row, err := r.queries.GetServiceIsolation(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if err != nil {
		return "", "", fmt.Errorf("GetServiceIsolation: %w", err)
	}
	return row.Isolation, row.WorkspaceDefault, nil
}

func (r *PostgresRepository) SetIsolation(ctx context.Context, id, level string) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid service id %q: %w", id, err)
	}
	var v *string
	if level != "" {
		v = &level
	}
	if err := r.queries.SetServiceIsolation(ctx, sqlcdb.SetServiceIsolationParams{Isolation: v, ID: pgtype.UUID{Bytes: parsed, Valid: true}}); err != nil {
		return fmt.Errorf("SetServiceIsolation: %w", err)
	}
	return nil
}
