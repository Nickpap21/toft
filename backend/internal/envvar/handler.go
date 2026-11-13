package envvar

import (
	"context"
	"errors"
	"net/http"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// OwnershipChecker verifies that a service, or an environment, belongs to the
// requesting org. Satisfied by *service.ServiceManager and
// *environment.Service — defined here to avoid a circular import.
type OwnershipChecker interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
}

// vars is one scope's variables and secrets: a service's own (*Service), or
// an environment's, which its services inherit (EnvironmentVars).
type vars interface {
	ListVersioned(ctx context.Context, id string) ([]EnvVar, string, error)
	Upsert(ctx context.Context, id, by string, input UpsertInput) (*EnvVar, error)
	BulkUpsert(ctx context.Context, id, by, version string, vars []UpsertInput) ([]EnvVar, string, error)
	Delete(ctx context.Context, id, key string) error
	ListSecrets(ctx context.Context, id string) ([]SecretDTO, error)
	CreateSecret(ctx context.Context, id, by string, input CreateSecretInput) (*SecretDTO, error)
	ReplaceSecret(ctx context.Context, id, by, key, plainValue string) error
	DeleteSecret(ctx context.Context, id, key string) error
	CheckAgent(ctx context.Context, id string, secret bool) error
	Writable(ctx context.Context, id string) error
}

// AuditWriter records who changed which variables and secrets. Satisfied
// by *org.Service.
type AuditWriter interface {
	WriteAuditLog(ctx context.Context, orgID, actorID, action, resource string)
}

// ─── Handler ──────────────────────────────────────────────────────────────────

type Handler struct {
	services     scope
	environments scope
}

func NewHandler(svc *Service, serviceOwner, environmentOwner OwnershipChecker, audit AuditWriter, log *zap.Logger) *Handler {
	return &Handler{
		services:     scope{param: "serviceID", kind: "service", logKey: "service_id", owner: serviceOwner, vars: svc, audit: audit, log: log},
		environments: scope{param: "environmentID", kind: "environment", logKey: "environment_id", owner: environmentOwner, vars: svc.Environment(), audit: audit, log: log},
	}
}

func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	h.services.register(r.Group("/services/:serviceID"))
	h.environments.register(r.Group("/environments/:environmentID"))
}

// scope serves one owner's variables: routes under /services/:serviceID or
// /environments/:environmentID, with the same bodies and responses.
type scope struct {
	param  string // the route parameter holding the owner's ID
	kind   string // "service" or "environment": the audit log's resource
	logKey string
	owner  OwnershipChecker
	vars   vars
	audit  AuditWriter
	log    *zap.Logger
}

// record writes action on the owner to the audit log.
func (sc scope) record(c *gin.Context, id, action string) {
	if sc.audit != nil {
		sc.audit.WriteAuditLog(c.Request.Context(), middleware.OrgIDFrom(c), middleware.UserIDFrom(c), action, sc.kind+":"+id)
	}
}

func (sc scope) register(g *gin.RouterGroup) {
	// Env vars (non-secret, plaintext)
	envVars := g.Group("/env-vars")
	{
		envVars.GET("", sc.handleList)
		envVars.POST("", sc.handleUpsert)
		// PUT replaces the whole set of plain variables: any plain variable not in
		// the body is deleted (secrets are untouched), so it must send the version
		// it read. To add or change one variable without touching the rest, POST
		// it instead.
		envVars.PUT("", sc.handleBulkUpsert)
		envVars.DELETE("/:key", sc.handleDelete)
	}

	// Secrets (encrypted at rest, values never returned)
	secrets := g.Group("/secrets")
	{
		secrets.GET("", sc.handleListSecrets)
		secrets.POST("", sc.handleCreateSecret)
		secrets.PUT("/:key", sc.handleReplaceSecret)
		secrets.DELETE("/:key", sc.handleDeleteSecret)
	}
}

// authorize checks that the owner belongs to the caller's org and, for a
// write, that the caller is not a viewer. On failure it writes the 403 and
// returns false.
func (sc scope) authorize(c *gin.Context, write bool) (string, bool) {
	id := c.Param(sc.param)
	if owned, err := sc.owner.CheckOwnership(c.Request.Context(), id, middleware.OrgIDFrom(c)); err != nil || !owned {
		respond.Error(c, http.StatusNotFound, sc.kind+" not found")
		return "", false
	}
	if write && middleware.MemberRoleFrom(c) == "viewer" {
		respond.Error(c, http.StatusForbidden, "forbidden")
		return "", false
	}
	if write {
		if err := sc.vars.Writable(c.Request.Context(), id); errors.Is(err, ErrPreviewService) {
			respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "preview_service"})
			return "", false
		} else if err != nil {
			sc.log.Error("preview copy check failed", zap.Error(err), zap.String(sc.logKey, id))
			respond.Error(c, http.StatusInternalServerError, "failed to load the service")
			return "", false
		}
	}
	return id, true
}

// agentPolicy runs the agent policy (D-102) for a write and writes its
// refusal, or the 202 of a call waiting for a person.
func (sc scope) agentPolicy(c *gin.Context, id string, secret bool) bool {
	err := sc.vars.CheckAgent(c.Request.Context(), id, secret)
	if err == nil {
		return true
	}
	if !respond.Gate(c, err) {
		sc.log.Error("agent policy check failed", zap.Error(err), zap.String(sc.logKey, id))
		respond.Error(c, http.StatusInternalServerError, "failed to check the agent policy")
	}
	return false
}

// ─── Env Var Handlers ─────────────────────────────────────────────────────────

func (sc scope) handleList(c *gin.Context) {
	id, ok := sc.authorize(c, false)
	if !ok {
		return
	}

	envVars, version, err := sc.vars.ListVersioned(c.Request.Context(), id)
	if err != nil {
		sc.log.Error("failed to list env vars", zap.Error(err), zap.String(sc.logKey, id))
		respond.Error(c, http.StatusInternalServerError, "Failed to list environment variables")
		return
	}

	respond.JSONVersioned(c, http.StatusOK, envVars, version)
}

func (sc scope) handleUpsert(c *gin.Context) {
	id, ok := sc.authorize(c, true)
	if !ok {
		return
	}

	var input UpsertInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if !sc.agentPolicy(c, id, false) {
		return
	}

	ev, err := sc.vars.Upsert(c.Request.Context(), id, middleware.UserIDFrom(c), input)
	if errors.Is(err, ErrInvalidKey) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, ErrKeyIsSecret) {
		respond.Error(c, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		sc.log.Error("failed to upsert env var", zap.Error(err), zap.String(sc.logKey, id))
		respond.Error(c, http.StatusInternalServerError, "Failed to save environment variable")
		return
	}

	sc.record(c, id, "variables.saved")
	respond.JSON(c, http.StatusOK, ev)
}

func (sc scope) handleBulkUpsert(c *gin.Context) {
	id, ok := sc.authorize(c, true)
	if !ok {
		return
	}

	var input BulkUpsertInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "Invalid request body")
		return
	}

	envVars, version, err := sc.vars.BulkUpsert(c.Request.Context(), id, middleware.UserIDFrom(c), input.Version, input.Vars)
	if respond.VersionRefused(c, err) {
		return
	}
	if errors.Is(err, ErrInvalidKey) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, ErrKeyIsSecret) {
		respond.Error(c, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		sc.log.Error("failed to bulk upsert env vars", zap.Error(err), zap.String(sc.logKey, id))
		respond.Error(c, http.StatusInternalServerError, "Failed to save environment variables")
		return
	}

	sc.record(c, id, "variables.saved")
	respond.JSONVersioned(c, http.StatusOK, envVars, version)
}

func (sc scope) handleDelete(c *gin.Context) {
	id, ok := sc.authorize(c, true)
	if !ok {
		return
	}

	if err := sc.vars.Delete(c.Request.Context(), id, c.Param("key")); err != nil {
		sc.log.Error("failed to delete env var", zap.Error(err),
			zap.String(sc.logKey, id),
			zap.String("key", c.Param("key")),
		)
		respond.Error(c, http.StatusInternalServerError, "Failed to delete environment variable")
		return
	}

	sc.record(c, id, "variables.saved")
	respond.NoContent(c)
}

// ─── Secret Handlers ──────────────────────────────────────────────────────────

func (sc scope) handleListSecrets(c *gin.Context) {
	id, ok := sc.authorize(c, false)
	if !ok {
		return
	}

	secrets, err := sc.vars.ListSecrets(c.Request.Context(), id)
	if err != nil {
		sc.log.Error("failed to list secrets", zap.Error(err), zap.String(sc.logKey, id))
		respond.Error(c, http.StatusInternalServerError, "Failed to list secrets")
		return
	}

	respond.JSON(c, http.StatusOK, secrets)
}

func (sc scope) handleCreateSecret(c *gin.Context) {
	id, ok := sc.authorize(c, true)
	if !ok {
		return
	}

	var input CreateSecretInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if !sc.agentPolicy(c, id, true) {
		return
	}

	secret, err := sc.vars.CreateSecret(c.Request.Context(), id, middleware.UserIDFrom(c), input)
	if errors.Is(err, ErrInvalidKey) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		sc.log.Error("failed to create secret", zap.Error(err), zap.String(sc.logKey, id))
		respond.Error(c, http.StatusConflict, "Secret with this key already exists or could not be created")
		return
	}

	sc.record(c, id, "secret.created")
	respond.JSON(c, http.StatusCreated, secret)
}

func (sc scope) handleReplaceSecret(c *gin.Context) {
	id, ok := sc.authorize(c, true)
	if !ok {
		return
	}
	key := c.Param("key")

	var body struct {
		Value string `json:"value" binding:"required,max=32768"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		respond.Error(c, http.StatusBadRequest, "Invalid request body")
		return
	}
	if !sc.agentPolicy(c, id, true) {
		return
	}

	if err := sc.vars.ReplaceSecret(c.Request.Context(), id, middleware.UserIDFrom(c), key, body.Value); err != nil {
		if errors.Is(err, ErrNotFound) {
			respond.Error(c, http.StatusNotFound, err.Error())
			return
		}
		sc.log.Error("failed to replace secret", zap.Error(err),
			zap.String(sc.logKey, id),
			zap.String("key", key),
		)
		respond.Error(c, http.StatusInternalServerError, "Failed to replace secret")
		return
	}

	sc.record(c, id, "secret.replaced")
	// Return updated SecretDTO (key + timestamps, no value)
	secrets, err := sc.vars.ListSecrets(c.Request.Context(), id)
	if err != nil {
		respond.JSON(c, http.StatusOK, gin.H{"key": key})
		return
	}
	for _, s := range secrets {
		if s.Key == key {
			respond.JSON(c, http.StatusOK, s)
			return
		}
	}
	respond.JSON(c, http.StatusOK, gin.H{"key": key})
}

func (sc scope) handleDeleteSecret(c *gin.Context) {
	id, ok := sc.authorize(c, true)
	if !ok {
		return
	}

	if err := sc.vars.DeleteSecret(c.Request.Context(), id, c.Param("key")); err != nil {
		sc.log.Error("failed to delete secret", zap.Error(err),
			zap.String(sc.logKey, id),
			zap.String("key", c.Param("key")),
		)
		respond.Error(c, http.StatusInternalServerError, "Failed to delete secret")
		return
	}

	sc.record(c, id, "secret.deleted")
	respond.NoContent(c)
}
