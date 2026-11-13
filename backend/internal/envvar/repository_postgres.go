package envvar

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/dbtx"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// serviceSecretAD and environmentSecretAD are where a secret is sealed for
// (D-126): its owner and its key, which is unique there.
func serviceSecretAD(id pgtype.UUID, key string) string {
	return crypto.AD("env_vars.value", uuidString(id), key)
}

func environmentSecretAD(id pgtype.UUID, key string) string {
	return crypto.AD("environment_env_vars.value", uuidString(id), key)
}

// actorUUID is who made a change: a user ID, or none ("" or not a UUID).
func actorUUID(by string) pgtype.UUID {
	if u, err := uuid.Parse(by); err == nil {
		return pgtype.UUID{Bytes: u, Valid: true}
	}
	return pgtype.UUID{}
}

// agentOf is the agent acting on this request for the person by, if one
// is (D-100). A write with no person (the IMAGE_TAG a deploy saves) names
// no agent either.
func agentOf(ctx context.Context, by string) pgtype.UUID {
	if a, ok := actor.AgentFrom(ctx); ok && actorUUID(by).Valid {
		return actorUUID(a.ConnectionID)
	}
	return pgtype.UUID{}
}

// setBy is who set a value as shown: the person, or the agent acting for
// them ("Claude Code for admin").
func setBy(name, agent string) string {
	if agent == "" {
		return name
	}
	if name == "" {
		return agent
	}
	return agent + " for " + name
}

func serviceUUID(serviceID string) (pgtype.UUID, error) {
	id, err := uuid.Parse(serviceID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid service id %q: %w", serviceID, err)
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

// ─── Env Vars (non-secret) ────────────────────────────────────────────────────

// Upsert sets one plain variable. ErrKeyIsSecret if the service has a
// secret with the key.
func (r *PostgresRepository) Upsert(ctx context.Context, serviceID, by string, input UpsertInput) (*EnvVar, error) {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return nil, err
	}
	row, err := r.queries.UpsertEnvVar(ctx, sqlcdb.UpsertEnvVarParams{
		ServiceID: id, Key: input.Key, Value: input.Value, Masked: input.Masked, UpdatedBy: actorUUID(by), AgentConnectionID: agentOf(ctx, by),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrKeyIsSecret, input.Key)
	}
	if err != nil {
		return nil, fmt.Errorf("UpsertEnvVar: %w", err)
	}
	ev := EnvVar{
		ID: uuidString(row.ID), ServiceID: uuidString(row.ServiceID), Key: row.Key, Value: row.Value,
		Masked: row.Masked, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	return &ev, nil
}

func (r *PostgresRepository) Swap(ctx context.Context, serviceID, key, from, value string) error {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return err
	}
	if err := r.queries.SwapEnvVar(ctx, sqlcdb.SwapEnvVarParams{ServiceID: id, Key: key, FromValue: from, Value: value}); err != nil {
		return fmt.Errorf("SwapEnvVar: %w", err)
	}
	return nil
}

func (r *PostgresRepository) Unset(ctx context.Context, serviceID, key, value string) error {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return err
	}
	if err := r.queries.UnsetEnvVar(ctx, sqlcdb.UnsetEnvVarParams{ServiceID: id, Key: key, Value: value}); err != nil {
		return fmt.Errorf("UnsetEnvVar: %w", err)
	}
	return nil
}

// ListByService returns only non-secret env vars (plaintext values).
func (r *PostgresRepository) ListByService(ctx context.Context, serviceID string) ([]EnvVar, error) {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcdb.New(dbtx.On(ctx, r.db)).ListEnvVarsForService(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ListEnvVarsForService: %w", err)
	}
	result := make([]EnvVar, 0, len(rows))
	for _, row := range rows {
		if !row.IsSecret {
			result = append(result, EnvVar{
				ID: uuidString(row.ID), ServiceID: uuidString(row.ServiceID), Key: row.Key, Value: row.Value,
				Masked: row.Masked, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, UpdatedBy: setBy(row.UpdatedByName, row.UpdatedByAgent),
			})
		}
	}
	return result, nil
}

func (r *PostgresRepository) Delete(ctx context.Context, serviceID, key string) error {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return err
	}
	if err := r.queries.DeleteEnvVar(ctx, sqlcdb.DeleteEnvVarParams{ServiceID: id, Key: key}); err != nil {
		return fmt.Errorf("DeleteEnvVar: %w", err)
	}
	return nil
}

// BulkUpsert makes vars the service's plain variables in one transaction, if
// version is still what is stored (listversion.ErrChanged and no write if not):
// each is set (a row whose value and mask are unchanged keeps when and by
// whom it was last set), the others are deleted, and secrets are kept.
// ErrKeyIsSecret if a variable has a secret's key (nothing changes then).
//
// READ COMMITTED, so each statement sees a new snapshot. The service's lock
// conflicts with the FOR KEY SHARE that adding a variable takes through its
// foreign key, so no key can appear between the check and the delete; the
// rows' locks hold the ones the check saw.
//
// ponytail: a POST adding the same new key at the same instant deadlocks with
// this (Postgres aborts one: a 500 to retry, nothing half-written); answer
// 40P01 as changed_since_read if that is ever seen.
func (r *PostgresRepository) BulkUpsert(ctx context.Context, serviceID, by, version string, vars []UpsertInput) ([]EnvVar, error) {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return nil, err
	}
	tx, err := dbtx.On(ctx, r.db).Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	qtx := r.queries.WithTx(tx)
	if _, err := qtx.LockServiceForReplace(ctx, id); err != nil {
		return nil, fmt.Errorf("LockServiceForReplace: %w", err)
	}
	stored, err := qtx.ListPlainEnvVarsForReplace(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ListPlainEnvVarsForReplace: %w", err)
	}
	current := make([]EnvVar, 0, len(stored))
	for _, row := range stored {
		current = append(current, EnvVar{Key: row.Key, UpdatedAt: row.UpdatedAt.Time})
	}
	if versionOf(current) != version {
		return nil, listversion.ErrChanged
	}
	keys := make([]string, 0, len(vars))
	for _, v := range vars {
		keys = append(keys, v.Key)
	}
	if err := qtx.DeleteEnvVarsExcept(ctx, sqlcdb.DeleteEnvVarsExceptParams{ServiceID: id, Keys: keys}); err != nil {
		return nil, fmt.Errorf("DeleteEnvVarsExcept: %w", err)
	}
	for _, v := range vars {
		_, err := qtx.UpsertEnvVar(ctx, sqlcdb.UpsertEnvVarParams{
			ServiceID: id, Key: v.Key, Value: v.Value, Masked: v.Masked, UpdatedBy: actorUUID(by), AgentConnectionID: agentOf(ctx, by),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrKeyIsSecret, v.Key)
		}
		if err != nil {
			return nil, fmt.Errorf("UpsertEnvVar %q: %w", v.Key, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return r.ListByService(ctx, serviceID)
}

// Seed sets vars on a new service, in the caller's transaction (dbtx). It
// deletes nothing: only a replace, with the version it read, does.
func (r *PostgresRepository) Seed(ctx context.Context, serviceID, by string, vars []UpsertInput) error {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return err
	}
	q := sqlcdb.New(dbtx.On(ctx, r.db))
	for _, v := range vars {
		_, err := q.UpsertEnvVar(ctx, sqlcdb.UpsertEnvVarParams{
			ServiceID: id, Key: v.Key, Value: v.Value, Masked: v.Masked, UpdatedBy: actorUUID(by), AgentConnectionID: agentOf(ctx, by),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrKeyIsSecret, v.Key)
		}
		if err != nil {
			return fmt.Errorf("UpsertEnvVar %q: %w", v.Key, err)
		}
	}
	return nil
}

func (r *PostgresRepository) DeleteAll(ctx context.Context, serviceID string) error {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return err
	}
	if err := r.queries.DeleteAllEnvVarsForService(ctx, id); err != nil {
		return fmt.Errorf("DeleteAllEnvVarsForService: %w", err)
	}
	return nil
}

// ─── Secrets ──────────────────────────────────────────────────────────────────

// ListSecrets returns all secrets for a service with values masked (never returned).
func (r *PostgresRepository) ListSecrets(ctx context.Context, serviceID string) ([]SecretDTO, error) {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListEnvVarsForService(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ListEnvVarsForService: %w", err)
	}
	result := make([]SecretDTO, 0, len(rows))
	for _, row := range rows {
		if row.IsSecret {
			result = append(result, SecretDTO{
				ID: uuidString(row.ID), ServiceID: uuidString(row.ServiceID), Key: row.Key,
				CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, UpdatedBy: setBy(row.UpdatedByName, row.UpdatedByAgent),
			})
		}
	}
	return result, nil
}

// CreateSecret encrypts the value and inserts a new secret. Fails if the key already exists.
func (r *PostgresRepository) CreateSecret(ctx context.Context, serviceID, by string, input CreateSecretInput) (*SecretDTO, error) {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return nil, err
	}
	encrypted, err := r.keys.Seal(serviceSecretAD(id, input.Key), input.Value)
	if err != nil {
		return nil, fmt.Errorf("encrypt secret: %w", err)
	}
	row, err := sqlcdb.New(dbtx.On(ctx, r.db)).CreateEnvVarSecret(ctx, sqlcdb.CreateEnvVarSecretParams{
		ServiceID: id, Key: input.Key, Value: encrypted, UpdatedBy: actorUUID(by), AgentConnectionID: agentOf(ctx, by),
	})
	if err != nil {
		return nil, fmt.Errorf("insert secret: %w", err)
	}
	return &SecretDTO{
		ID: uuidString(row.ID), ServiceID: uuidString(row.ServiceID), Key: row.Key,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

// UpdateSecret encrypts the new value and overwrites the existing secret.
// ErrNotFound if the service has no secret with that key. In the caller's
// transaction (dbtx).
func (r *PostgresRepository) UpdateSecret(ctx context.Context, serviceID, by, key, plainValue string) error {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return err
	}
	encrypted, err := r.keys.Seal(serviceSecretAD(id, key), plainValue)
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}
	n, err := sqlcdb.New(dbtx.On(ctx, r.db)).UpdateEnvVarSecret(ctx, sqlcdb.UpdateEnvVarSecretParams{
		ServiceID: id, Key: key, Value: encrypted, UpdatedBy: actorUUID(by), AgentConnectionID: agentOf(ctx, by),
	})
	if err != nil {
		return fmt.Errorf("update secret: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSecret deletes a secret by key (only if is_secret=true); a missing
// key is not an error.
func (r *PostgresRepository) DeleteSecret(ctx context.Context, serviceID, key string) error {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return err
	}
	if _, err := r.queries.DeleteEnvVarSecret(ctx, sqlcdb.DeleteEnvVarSecretParams{ServiceID: id, Key: key}); err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	return nil
}

// ─── Deployment injection ─────────────────────────────────────────────────────

// ListAllForDeployment returns the service's env vars and secrets over its
// environment's shared ones (the service's row wins for a key set on both),
// with secrets decrypted, for Docker injection.
func (r *PostgresRepository) ListAllForDeployment(ctx context.Context, serviceID string) ([]EnvVar, error) {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListEnvVarsForDeployment(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ListEnvVarsForDeployment: %w", err)
	}
	result := make([]EnvVar, 0, len(rows))
	for _, row := range rows {
		ev := EnvVar{Key: row.Key, Value: row.Value, IsSecret: row.IsSecret}
		if row.IsSecret {
			ad := serviceSecretAD(id, row.Key)
			if row.EnvironmentID.Valid {
				ad = environmentSecretAD(row.EnvironmentID, row.Key)
			}
			decrypted, err := r.keys.Open(ad, row.Value)
			if err != nil {
				return nil, fmt.Errorf("decrypt secret %q: %w", row.Key, err)
			}
			ev.Value = decrypted
		}
		result = append(result, ev)
	}
	return result, nil
}

// ListPlainForDeployment is ListAllForDeployment without secrets or masked
// variables: what an untrusted preview gets (D-97).
func (r *PostgresRepository) ListPlainForDeployment(ctx context.Context, serviceID string) ([]EnvVar, error) {
	id, err := serviceUUID(serviceID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListPlainEnvVarsForDeployment(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ListPlainEnvVarsForDeployment: %w", err)
	}
	result := make([]EnvVar, 0, len(rows))
	for _, row := range rows {
		result = append(result, EnvVar{Key: row.Key, Value: row.Value})
	}
	return result, nil
}

// ─── Environment (shared) variables ──────────────────────────────────────────

func environmentUUID(environmentID string) (pgtype.UUID, error) {
	id, err := uuid.Parse(environmentID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid environment id %q: %w", environmentID, err)
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

// ListEnvironmentVars returns the environment's plain variables.
func (r *PostgresRepository) ListEnvironmentVars(ctx context.Context, environmentID string) ([]EnvVar, error) {
	id, err := environmentUUID(environmentID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListEnvironmentEnvVars(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ListEnvironmentEnvVars: %w", err)
	}
	result := make([]EnvVar, 0, len(rows))
	for _, row := range rows {
		if !row.IsSecret {
			result = append(result, EnvVar{
				ID: uuidString(row.ID), EnvironmentID: uuidString(row.EnvironmentID), Key: row.Key, Value: row.Value,
				CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, UpdatedBy: setBy(row.UpdatedByName, row.UpdatedByAgent),
			})
		}
	}
	return result, nil
}

// UpsertEnvironmentVar sets a plain variable. ErrKeyIsSecret if a secret has
// the key.
func (r *PostgresRepository) UpsertEnvironmentVar(ctx context.Context, environmentID, by string, input UpsertInput) (*EnvVar, error) {
	id, err := environmentUUID(environmentID)
	if err != nil {
		return nil, err
	}
	row, err := r.queries.UpsertEnvironmentEnvVar(ctx, sqlcdb.UpsertEnvironmentEnvVarParams{
		EnvironmentID: id, Key: input.Key, Value: input.Value, UpdatedBy: actorUUID(by), AgentConnectionID: agentOf(ctx, by),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrKeyIsSecret, input.Key)
	}
	if err != nil {
		return nil, fmt.Errorf("UpsertEnvironmentEnvVar: %w", err)
	}
	return &EnvVar{
		ID: uuidString(row.ID), EnvironmentID: uuidString(row.EnvironmentID), Key: row.Key, Value: row.Value,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

// ReplaceEnvironmentVars makes vars the environment's plain variables in
// one transaction, as BulkUpsert does for a service, with the same version
// check and locks; its secrets are kept. ErrKeyIsSecret if a variable has a
// secret's key (nothing is changed then).
func (r *PostgresRepository) ReplaceEnvironmentVars(ctx context.Context, environmentID, by, version string, vars []UpsertInput) ([]EnvVar, error) {
	id, err := environmentUUID(environmentID)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	qtx := r.queries.WithTx(tx)
	if _, err := qtx.LockEnvironmentForReplace(ctx, id); err != nil {
		return nil, fmt.Errorf("LockEnvironmentForReplace: %w", err)
	}
	stored, err := qtx.ListPlainEnvironmentEnvVarsForReplace(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ListPlainEnvironmentEnvVarsForReplace: %w", err)
	}
	current := make([]EnvVar, 0, len(stored))
	for _, row := range stored {
		current = append(current, EnvVar{Key: row.Key, UpdatedAt: row.UpdatedAt.Time})
	}
	if versionOf(current) != version {
		return nil, listversion.ErrChanged
	}
	keys := make([]string, 0, len(vars))
	for _, v := range vars {
		keys = append(keys, v.Key)
	}
	if err := qtx.DeleteEnvironmentEnvVarsExcept(ctx, sqlcdb.DeleteEnvironmentEnvVarsExceptParams{EnvironmentID: id, Keys: keys}); err != nil {
		return nil, fmt.Errorf("DeleteEnvironmentEnvVarsExcept: %w", err)
	}
	for _, v := range vars {
		_, err := qtx.UpsertEnvironmentEnvVar(ctx, sqlcdb.UpsertEnvironmentEnvVarParams{
			EnvironmentID: id, Key: v.Key, Value: v.Value, UpdatedBy: actorUUID(by), AgentConnectionID: agentOf(ctx, by),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrKeyIsSecret, v.Key)
		}
		if err != nil {
			return nil, fmt.Errorf("UpsertEnvironmentEnvVar %q: %w", v.Key, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return r.ListEnvironmentVars(ctx, environmentID)
}

// InheritEnvironment gives the service its own copy of each variable and
// secret its environment shares that it doesn't set itself, a secret
// sealed again for the service: what a service moved out of its
// environment keeps (D-162). In the caller's transaction (dbtx).
func (r *PostgresRepository) InheritEnvironment(ctx context.Context, environmentID, serviceID string) error {
	envID, err := environmentUUID(environmentID)
	if err != nil {
		return err
	}
	svcID, err := serviceUUID(serviceID)
	if err != nil {
		return err
	}
	q := sqlcdb.New(dbtx.On(ctx, r.db))
	rows, err := q.ListEnvironmentVarsNotSetBy(ctx, sqlcdb.ListEnvironmentVarsNotSetByParams{EnvironmentID: envID, ServiceID: svcID})
	if err != nil {
		return fmt.Errorf("ListEnvironmentVarsNotSetBy: %w", err)
	}
	in := sqlcdb.InsertInheritedEnvVarsParams{ServiceID: svcID}
	for _, row := range rows {
		value := row.Value
		if row.IsSecret {
			plain, err := r.keys.Open(environmentSecretAD(envID, row.Key), row.Value)
			if err != nil {
				return fmt.Errorf("decrypt the environment's secret %q: %w", row.Key, err)
			}
			if value, err = r.keys.Seal(serviceSecretAD(svcID, row.Key), plain); err != nil {
				return fmt.Errorf("encrypt secret %q: %w", row.Key, err)
			}
		}
		in.Keys = append(in.Keys, row.Key)
		in.Vals = append(in.Vals, value)
		in.Secrets = append(in.Secrets, row.IsSecret)
		in.UpdatedBy = append(in.UpdatedBy, row.UpdatedBy)
		in.Agents = append(in.Agents, row.AgentConnectionID)
	}
	if len(rows) == 0 {
		return nil
	}
	if err := q.InsertInheritedEnvVars(ctx, in); err != nil {
		return fmt.Errorf("InsertInheritedEnvVars: %w", err)
	}
	return nil
}

// DeleteEnvironmentVar deletes a plain variable; a missing key is not an error.
func (r *PostgresRepository) DeleteEnvironmentVar(ctx context.Context, environmentID, key string) error {
	id, err := environmentUUID(environmentID)
	if err != nil {
		return err
	}
	if _, err := r.queries.DeleteEnvironmentEnvVar(ctx, sqlcdb.DeleteEnvironmentEnvVarParams{EnvironmentID: id, Key: key}); err != nil {
		return fmt.Errorf("DeleteEnvironmentEnvVar: %w", err)
	}
	return nil
}

// ListEnvironmentSecrets returns the environment's secrets, values never included.
func (r *PostgresRepository) ListEnvironmentSecrets(ctx context.Context, environmentID string) ([]SecretDTO, error) {
	id, err := environmentUUID(environmentID)
	if err != nil {
		return nil, err
	}
	rows, err := r.queries.ListEnvironmentEnvVars(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ListEnvironmentEnvVars: %w", err)
	}
	result := make([]SecretDTO, 0, len(rows))
	for _, row := range rows {
		if row.IsSecret {
			result = append(result, SecretDTO{
				ID: uuidString(row.ID), EnvironmentID: uuidString(row.EnvironmentID), Key: row.Key,
				CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time, UpdatedBy: setBy(row.UpdatedByName, row.UpdatedByAgent),
			})
		}
	}
	return result, nil
}

// CreateEnvironmentSecret encrypts and inserts a write-once secret. An
// existing key (secret or plain) fails.
func (r *PostgresRepository) CreateEnvironmentSecret(ctx context.Context, environmentID, by string, input CreateSecretInput) (*SecretDTO, error) {
	id, err := environmentUUID(environmentID)
	if err != nil {
		return nil, err
	}
	encrypted, err := r.keys.Seal(environmentSecretAD(id, input.Key), input.Value)
	if err != nil {
		return nil, fmt.Errorf("encrypt secret: %w", err)
	}
	row, err := r.queries.CreateEnvironmentSecret(ctx, sqlcdb.CreateEnvironmentSecretParams{
		EnvironmentID: id, Key: input.Key, Value: encrypted, UpdatedBy: actorUUID(by), AgentConnectionID: agentOf(ctx, by),
	})
	if err != nil {
		return nil, fmt.Errorf("insert environment secret: %w", err)
	}
	return &SecretDTO{
		ID: uuidString(row.ID), EnvironmentID: uuidString(row.EnvironmentID), Key: row.Key,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

// UpdateEnvironmentSecret encrypts and replaces a secret's value. ErrNotFound
// if the environment has no secret with that key.
func (r *PostgresRepository) UpdateEnvironmentSecret(ctx context.Context, environmentID, by, key, plainValue string) error {
	id, err := environmentUUID(environmentID)
	if err != nil {
		return err
	}
	encrypted, err := r.keys.Seal(environmentSecretAD(id, key), plainValue)
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}
	n, err := r.queries.UpdateEnvironmentSecret(ctx, sqlcdb.UpdateEnvironmentSecretParams{
		EnvironmentID: id, Key: key, Value: encrypted, UpdatedBy: actorUUID(by), AgentConnectionID: agentOf(ctx, by),
	})
	if err != nil {
		return fmt.Errorf("update environment secret: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteEnvironmentSecret deletes a secret; a missing key is not an error.
func (r *PostgresRepository) DeleteEnvironmentSecret(ctx context.Context, environmentID, key string) error {
	id, err := environmentUUID(environmentID)
	if err != nil {
		return err
	}
	if _, err := r.queries.DeleteEnvironmentSecret(ctx, sqlcdb.DeleteEnvironmentSecretParams{EnvironmentID: id, Key: key}); err != nil {
		return fmt.Errorf("delete environment secret: %w", err)
	}
	return nil
}

func uuidString(p pgtype.UUID) string {
	val, _ := p.Value()
	if val == nil {
		return ""
	}
	return val.(string)
}
