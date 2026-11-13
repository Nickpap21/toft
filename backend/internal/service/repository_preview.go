package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
)

// CopyForPreview writes the copy, the rows that go with it (a trusted
// copy's config files, compose limits, shared volume mounts and deploy-
// policy exceptions; an untrusted one's compose limits alone, with its
// extra memory) and its addresses, in one transaction.
func (r *PostgresRepository) CopyForPreview(ctx context.Context, in PreviewCopy, memoryLimit string) (string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := r.queries.WithTx(tx)
	orig, env := uuidOf(in.OriginalID), uuidOf(in.EnvironmentID)
	var memory *string
	if memoryLimit != "" {
		memory = &memoryLimit
	}
	id, err := q.CopyServiceForPreview(ctx, sqlcdb.CopyServiceForPreviewParams{EnvironmentID: env, Branch: in.Branch,
		MemoryLimit: memory, Untrusted: in.Untrusted, UntrustedIsolation: in.Isolation, OriginalID: orig})
	if err != nil {
		return "", fmt.Errorf("CopyServiceForPreview: %w", err)
	}
	if err := r.resealCopy(ctx, q, orig, id); err != nil {
		return "", err
	}
	if in.Untrusted {
		err = q.CopyPreviewComposeLimits(ctx, sqlcdb.CopyPreviewComposeLimitsParams{CopyID: id, OriginalID: orig, ExtraMb: int64(in.ExtraMemoryMB)})
	} else {
		err = q.CopyPreviewServiceRows(ctx, sqlcdb.CopyPreviewServiceRowsParams{CopyID: id, OriginalID: orig})
	}
	if err != nil {
		return "", fmt.Errorf("copy the service's rows: %w", err)
	}
	for _, h := range in.Hostnames {
		if err := q.InsertPreviewDomain(ctx, sqlcdb.InsertPreviewDomainParams{ServiceID: id, Hostname: h}); err != nil {
			return "", fmt.Errorf("InsertPreviewDomain: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return uuidString(id), nil
}

// resealCopy seals the copy's registry password, git token and basic auth
// password for the copy: CopyServiceForPreview copied them sealed for the
// original, where they don't open (D-126). A value that doesn't open there
// either (basic auth in plain text from before 0019) is kept as it is.
func (r *PostgresRepository) resealCopy(ctx context.Context, q *sqlcdb.Queries, orig, id pgtype.UUID) error {
	c, err := q.GetServiceCredentials(ctx, id)
	if err != nil {
		return fmt.Errorf("GetServiceCredentials: %w", err)
	}
	reseal := func(column, stored string) (string, error) {
		plain, err := r.keys.Open(credentialAD(column, orig), stored)
		if stored == "" || err != nil {
			return stored, nil
		}
		return r.keys.Seal(credentialAD(column, id), plain)
	}
	p := sqlcdb.SetServiceCredentialsParams{ID: id, RegistryPassword: c.RegistryPassword, GitToken: c.GitToken}
	for _, v := range []struct {
		column string
		value  *string
	}{{"registry_password", p.RegistryPassword}, {"git_token", p.GitToken}} {
		if v.value != nil {
			if *v.value, err = reseal(v.column, *v.value); err != nil {
				return fmt.Errorf("seal the copy's %s: %w", v.column, err)
			}
		}
	}
	if p.BasicAuthPassword, err = reseal("basic_auth_password", c.BasicAuthPassword); err != nil {
		return fmt.Errorf("seal the copy's basic_auth_password: %w", err)
	}
	if err := q.SetServiceCredentials(ctx, p); err != nil {
		return fmt.Errorf("SetServiceCredentials: %w", err)
	}
	return nil
}

func (r *PostgresRepository) ListCopies(ctx context.Context, id string) ([]string, error) {
	rows, err := r.queries.ListServiceCopies(ctx, uuidOf(id))
	if err != nil {
		return nil, fmt.Errorf("ListServiceCopies: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, u := range rows {
		out = append(out, uuidString(u))
	}
	return out, nil
}

func (r *PostgresRepository) AddPreviewAddress(ctx context.Context, id, hostname string) error {
	if err := r.queries.InsertPreviewDomain(ctx, sqlcdb.InsertPreviewDomainParams{ServiceID: uuidOf(id), Hostname: hostname}); err != nil {
		return fmt.Errorf("InsertPreviewDomain: %w", err)
	}
	return nil
}

func (r *PostgresRepository) IsPreviewCopy(ctx context.Context, id string) (bool, error) {
	ok, err := r.queries.IsPreviewCopy(ctx, uuidOf(id))
	if err != nil {
		return false, fmt.Errorf("IsPreviewCopy: %w", err)
	}
	return ok, nil
}

func (r *PostgresRepository) IsPreviewEnvironment(ctx context.Context, environmentID string) (bool, error) {
	ok, err := r.queries.IsPreviewEnvironment(ctx, uuidOf(environmentID))
	if err != nil {
		return false, fmt.Errorf("IsPreviewEnvironment: %w", err)
	}
	return ok, nil
}

// uuidOf is id as a pgtype.UUID; an invalid one is NULL, which matches
// nothing.
func uuidOf(id string) pgtype.UUID {
	u, _ := optUUID(id)
	return u
}

func (r *PostgresRepository) PreviewOfEnvironment(ctx context.Context, environmentID string) (string, bool, bool, error) {
	row, err := r.queries.PreviewOfEnvironment(ctx, uuidOf(environmentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, fmt.Errorf("PreviewOfEnvironment: %w", err)
	}
	return uuidString(row.SourceEnvironmentID), row.Untrusted, true, nil
}

func (r *PostgresRepository) ListPreviewPeers(ctx context.Context, sourceEnvID, serverID, previewEnvID string) ([]NetworkPeer, error) {
	rows, err := r.queries.ListPreviewNetworkPeers(ctx, sqlcdb.ListPreviewNetworkPeersParams{SourceEnvironmentID: uuidOf(sourceEnvID),
		ServerID: uuidOf(serverID), PreviewEnvironmentID: uuidOf(previewEnvID)})
	if err != nil {
		return nil, fmt.Errorf("ListPreviewNetworkPeers: %w", err)
	}
	out := make([]NetworkPeer, 0, len(rows))
	for _, row := range rows {
		out = append(out, NetworkPeer{ID: uuidString(row.ID), Name: row.Name, Alias: row.NetworkAlias,
			SourceType: SourceType(row.SourceType), Scope: ScopeEnvironment})
	}
	return out, nil
}

func (r *PostgresRepository) ListPreviewTargets(ctx context.Context, environmentID, serviceID string) ([]string, error) {
	rows, err := r.queries.ListPreviewNetworkTargets(ctx, sqlcdb.ListPreviewNetworkTargetsParams{EnvironmentID: uuidOf(environmentID),
		ServiceID: uuidOf(serviceID)})
	if err != nil {
		return nil, fmt.Errorf("ListPreviewNetworkTargets: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, u := range rows {
		out = append(out, uuidString(u))
	}
	return out, nil
}

func (r *PostgresRepository) ListUntrustedPreviewEnvironments(ctx context.Context, projectID, orgID string) ([]string, error) {
	rows, err := r.queries.ListUntrustedPreviewEnvironments(ctx, sqlcdb.ListUntrustedPreviewEnvironmentsParams{
		ProjectID: uuidOf(projectID), OrgID: uuidOf(orgID)})
	if err != nil {
		return nil, fmt.Errorf("ListUntrustedPreviewEnvironments: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, u := range rows {
		out = append(out, uuidString(u))
	}
	return out, nil
}

// SumAllocatedOnPanel is what the panel's own server's running services
// reserve, as the servers' ledgers count it (D-97).
func (r *PostgresRepository) SumAllocatedOnPanel(ctx context.Context) (float64, error) {
	rows, err := r.queries.ListPanelAllocations(ctx)
	if err != nil {
		return 0, fmt.Errorf("ListPanelAllocations: %w", err)
	}
	var single []sqlcdb.ListPanelAllocationsRow
	mem := 0.0
	for _, row := range rows {
		if row.SourceType == string(SourceTypeDockerCompose) {
			mem += float64(row.ComposeMemoryMb)
			continue
		}
		single = append(single, row)
	}
	_, m := sumLimitStrings(len(single), func(i int) (*string, *string) { return single[i].CpuLimit, single[i].MemoryLimit })
	return mem + m, nil
}
