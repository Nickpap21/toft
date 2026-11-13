package envvar

import (
	"context"

	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"go.uber.org/zap"
)

type Service struct {
	repo    Repository
	log     *zap.Logger
	agents  AgentGate
	copies  PreviewCopies
	reveals Reveals
}

// Reveals deletes every kept copy of a service's secret (D-160) once its
// value is replaced or deleted. Satisfied by reveal.Repository.
type Reveals interface {
	Forget(ctx context.Context, serviceID, key string) error
}

// WithReveals drops a replaced or deleted admin password's kept copies, in
// the write's transaction when it has one (dbtx).
func (s *Service) WithReveals(r Reveals) { s.reveals = r }

func (s *Service) forget(ctx context.Context, serviceID, key string) error {
	if s.reveals == nil {
		return nil
	}
	return s.reveals.Forget(ctx, serviceID, key)
}

// PreviewCopies says whether a service is a preview's copy (D-97), whose
// variables are read from its original at each deploy. Satisfied by
// *service.ServiceManager.
type PreviewCopies interface {
	IsPreviewCopy(ctx context.Context, id string) (bool, error)
}

// WithPreviewCopies refuses variable and secret writes on a preview's
// copies: they would never be read (D-98, D-104).
func (s *Service) WithPreviewCopies(p PreviewCopies) { s.copies = p }

// Writable is ErrPreviewService for a preview's copy.
func (s *Service) Writable(ctx context.Context, serviceID string) error {
	if s.copies == nil {
		return nil
	}
	isCopy, err := s.copies.IsPreviewCopy(ctx, serviceID)
	if err != nil {
		return err
	}
	if isCopy {
		return ErrPreviewService
	}
	return nil
}

func NewService(repo Repository, log *zap.Logger) *Service {
	return &Service{repo: repo, log: log}
}

// AgentGate is the workspace's agent policy (D-100, D-102). Satisfied by
// *agent.Service.
type AgentGate interface {
	Check(ctx context.Context, t actor.Target) error
}

// WithAgentGate puts agents' variable and secret writes under the policy.
func (s *Service) WithAgentGate(g AgentGate) { s.agents = g }

// Agent tools behind each write, for the policy's rate limits.
const (
	toolSetEnvVar = "set_env_var"
	toolSetSecret = "set_secret"
)

// CheckAgent is the agent policy for writing a service's variable
// (secret false) or secret, run by the write use cases before they act.
func (s *Service) CheckAgent(ctx context.Context, serviceID string, secret bool) error {
	if s.agents == nil {
		return nil
	}
	return s.agents.Check(ctx, target(secret, actor.Target{ServiceID: serviceID}))
}

func target(secret bool, t actor.Target) actor.Target {
	t.Action, t.Tool = "variables", toolSetEnvVar
	if secret {
		t.Action, t.Tool = "secrets", toolSetSecret
	}
	return t
}

// Upsert creates or updates one env var. The key must be a valid name
// (ErrInvalidKey otherwise).
func (s *Service) Upsert(ctx context.Context, serviceID, by string, input UpsertInput) (*EnvVar, error) {
	if err := validateKey(input.Key); err != nil {
		return nil, err
	}
	return s.repo.Upsert(ctx, serviceID, by, input)
}

// Set creates or updates one plain env var (deployment.EnvVarSetter), and
// returns the service's own value it replaced; had is false when the
// service had none of its own.
func (s *Service) Set(ctx context.Context, serviceID, key, value string) (prev string, had bool, err error) {
	own, err := s.repo.ListByService(ctx, serviceID)
	if err != nil {
		return "", false, err
	}
	for _, v := range own {
		if v.Key == key {
			prev, had = v.Value, true
		}
	}
	_, err = s.Upsert(ctx, serviceID, "", UpsertInput{Key: key, Value: value})
	return prev, had, err
}

// Swap sets the service's own plain variable key to value only while it is
// still from; it never creates one (deployment.EnvVarSwapper).
func (s *Service) Swap(ctx context.Context, serviceID, key, from, value string) error {
	return s.repo.Swap(ctx, serviceID, key, from, value)
}

// Unset deletes the service's own plain variable key only while it is still
// value (deployment.EnvVarSetter); never a secret, nor the environment's.
func (s *Service) Unset(ctx context.Context, serviceID, key, value string) error {
	return s.repo.Unset(ctx, serviceID, key, value)
}

func (s *Service) List(ctx context.Context, serviceID string) ([]EnvVar, error) {
	return s.repo.ListByService(ctx, serviceID)
}

// ListVersioned is List and the version a replace of this list must send.
func (s *Service) ListVersioned(ctx context.Context, serviceID string) ([]EnvVar, string, error) {
	vars, err := s.repo.ListByService(ctx, serviceID)
	if err != nil {
		return nil, "", err
	}
	return vars, versionOf(vars), nil
}

func (s *Service) Delete(ctx context.Context, serviceID, key string) error {
	return s.repo.Delete(ctx, serviceID, key)
}

// BulkUpsert replaces the service's env vars if version is still what is
// stored (listversion.ErrRequired, listversion.ErrChanged), and returns the new list
// and its version. Every key must be a valid name (ErrInvalidKey names the
// first one that is not). Keys stored before the rule existed are not touched
// until the service's vars are saved again.
func (s *Service) BulkUpsert(ctx context.Context, serviceID, by, version string, vars []UpsertInput) ([]EnvVar, string, error) {
	if version == "" {
		return nil, "", listversion.ErrRequired
	}
	for _, v := range vars {
		if err := validateKey(v.Key); err != nil {
			return nil, "", err
		}
	}
	saved, err := s.repo.BulkUpsert(ctx, serviceID, by, version, vars)
	if err != nil {
		return nil, "", err
	}
	return saved, versionOf(saved), nil
}

// Seed sets a new service's first variables, in its creation's transaction.
// It deletes nothing, so it needs no version; no route reaches it.
func (s *Service) Seed(ctx context.Context, serviceID, by string, vars []UpsertInput) error {
	for _, v := range vars {
		if err := validateKey(v.Key); err != nil {
			return err
		}
	}
	return s.repo.Seed(ctx, serviceID, by, vars)
}

func (s *Service) ListSecrets(ctx context.Context, serviceID string) ([]SecretDTO, error) {
	return s.repo.ListSecrets(ctx, serviceID)
}

// CreateSecret creates a write-once secret. The key must be a valid name
// (ErrInvalidKey otherwise).
func (s *Service) CreateSecret(ctx context.Context, serviceID, by string, input CreateSecretInput) (*SecretDTO, error) {
	if err := validateKey(input.Key); err != nil {
		return nil, err
	}
	return s.repo.CreateSecret(ctx, serviceID, by, input)
}

// InheritEnvironment gives the service its own copy of what its
// environment shares and it doesn't set, before it leaves the environment
// (D-162).
func (s *Service) InheritEnvironment(ctx context.Context, environmentID, serviceID string) error {
	return s.repo.InheritEnvironment(ctx, environmentID, serviceID)
}

// ReplaceSecret replaces a secret's value (ErrNotFound).
func (s *Service) ReplaceSecret(ctx context.Context, serviceID, by, key, plainValue string) error {
	if err := s.repo.UpdateSecret(ctx, serviceID, by, key, plainValue); err != nil {
		return err
	}
	return s.forget(ctx, serviceID, key)
}

func (s *Service) DeleteSecret(ctx context.Context, serviceID, key string) error {
	if err := s.repo.DeleteSecret(ctx, serviceID, key); err != nil {
		return err
	}
	return s.forget(ctx, serviceID, key)
}

// ListAllDecrypted returns every env var and secret of a service with its
// decrypted value and IsSecret set, for checks that must interpolate with the
// real values and then keep the secret ones out of anything they show.
func (s *Service) ListAllDecrypted(ctx context.Context, serviceID string) ([]EnvVar, error) {
	return s.repo.ListAllForDeployment(ctx, serviceID)
}

// ListAllForDeployment returns all vars + decrypted secrets as KEY=VALUE strings for Docker injection.
func (s *Service) ListAllForDeployment(ctx context.Context, serviceID string) ([]string, error) {
	vars, err := s.repo.ListAllForDeployment(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(vars))
	for i, v := range vars {
		result[i] = v.Key + "=" + v.Value
	}
	return result, nil
}

// ListPlainForDeployment is ListAllForDeployment's plain variables only,
// neither secret nor masked (an untrusted preview's; D-97).
func (s *Service) ListPlainForDeployment(ctx context.Context, serviceID string) ([]string, error) {
	vars, err := s.repo.ListPlainForDeployment(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(vars))
	for i, v := range vars {
		result[i] = v.Key + "=" + v.Value
	}
	return result, nil
}

// EnvironmentVars is the environment scope: variables and secrets every
// service in an environment inherits on its next deploy (a service's own
// variable with the same key wins). Its methods mirror the service scope's
// on *Service, keyed by environment ID.
type EnvironmentVars struct {
	repo Repository
	svc  *Service
}

// Environment returns the environment scope.
func (s *Service) Environment() EnvironmentVars { return EnvironmentVars{repo: s.repo, svc: s} }

// Writable: an environment's variables are always written.
func (e EnvironmentVars) Writable(context.Context, string) error { return nil }

// CheckAgent is the agent policy for writing an environment's variable or
// secret.
func (e EnvironmentVars) CheckAgent(ctx context.Context, environmentID string, secret bool) error {
	if e.svc == nil || e.svc.agents == nil {
		return nil
	}
	return e.svc.agents.Check(ctx, target(secret, actor.Target{EnvironmentID: environmentID}))
}

// ListVersioned is the plain variables and the version a replace must send.
func (e EnvironmentVars) ListVersioned(ctx context.Context, environmentID string) ([]EnvVar, string, error) {
	vars, err := e.repo.ListEnvironmentVars(ctx, environmentID)
	if err != nil {
		return nil, "", err
	}
	return vars, versionOf(vars), nil
}

// Upsert sets one plain variable (ErrInvalidKey, ErrKeyIsSecret); an
// environment's variables are never masked.
func (e EnvironmentVars) Upsert(ctx context.Context, environmentID, by string, input UpsertInput) (*EnvVar, error) {
	if err := validateKey(input.Key); err != nil {
		return nil, err
	}
	return e.repo.UpsertEnvironmentVar(ctx, environmentID, by, input)
}

// BulkUpsert replaces the plain variables if version is still what is stored
// (listversion.ErrRequired, listversion.ErrChanged); secrets are kept (ErrInvalidKey,
// ErrKeyIsSecret).
func (e EnvironmentVars) BulkUpsert(ctx context.Context, environmentID, by, version string, vars []UpsertInput) ([]EnvVar, string, error) {
	if version == "" {
		return nil, "", listversion.ErrRequired
	}
	for _, v := range vars {
		if err := validateKey(v.Key); err != nil {
			return nil, "", err
		}
	}
	saved, err := e.repo.ReplaceEnvironmentVars(ctx, environmentID, by, version, vars)
	if err != nil {
		return nil, "", err
	}
	return saved, versionOf(saved), nil
}

func (e EnvironmentVars) Delete(ctx context.Context, environmentID, key string) error {
	return e.repo.DeleteEnvironmentVar(ctx, environmentID, key)
}

func (e EnvironmentVars) ListSecrets(ctx context.Context, environmentID string) ([]SecretDTO, error) {
	return e.repo.ListEnvironmentSecrets(ctx, environmentID)
}

// CreateSecret creates a write-once secret (ErrInvalidKey).
func (e EnvironmentVars) CreateSecret(ctx context.Context, environmentID, by string, input CreateSecretInput) (*SecretDTO, error) {
	if err := validateKey(input.Key); err != nil {
		return nil, err
	}
	return e.repo.CreateEnvironmentSecret(ctx, environmentID, by, input)
}

// ReplaceSecret replaces a secret's value (ErrNotFound).
func (e EnvironmentVars) ReplaceSecret(ctx context.Context, environmentID, by, key, plainValue string) error {
	return e.repo.UpdateEnvironmentSecret(ctx, environmentID, by, key, plainValue)
}

func (e EnvironmentVars) DeleteSecret(ctx context.Context, environmentID, key string) error {
	return e.repo.DeleteEnvironmentSecret(ctx, environmentID, key)
}
