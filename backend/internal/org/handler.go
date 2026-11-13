package org

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Handler is the thin HTTP adapter for the org domain.
type Handler struct {
	svc *Service
	log *zap.Logger
}

// NewHandler creates a new org Handler.
func NewHandler(svc *Service, log *zap.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// RegisterRoutes mounts all org endpoints on the provided router group.
// The validate/:token endpoint is also registered on the public group (no auth required).
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	org := rg.Group("/org")
	{
		org.POST("", h.createOrg)
		org.GET("", h.getOrg)
		org.PUT("", h.updateOrgName)
		org.DELETE("", h.deleteOrg)
		org.GET("/list", h.listOrgs)
		org.POST("/switch", h.switchOrg)

		org.GET("/members", h.listMembers)
		org.POST("/members", h.addMember)
		org.GET("/members/addable", h.listAddable)
		org.PUT("/members/:userID/role", h.updateMemberRole)
		org.DELETE("/members/:userID", h.removeMember)
		org.POST("/leave", h.leave)

		org.GET("/invites", h.listInvites)
		org.POST("/invites", h.createInvite)
		org.DELETE("/invites/:inviteID", h.revokeInvite)
		org.POST("/invites/accept", h.acceptInvite)

		org.GET("/audit-log", h.listAuditLog)
		org.GET("/audit-log/count", h.countAuditLog)

		org.GET("/settings", h.handleGetSettings)
		org.GET("/isolation", h.handleGetIsolation)
		org.PUT("/settings", h.handleUpdateSettings)
		org.PATCH("/settings", h.handleHideSetup)
	}
}

// RegisterPublicRoutes mounts endpoints that do not require authentication.
// Call this on the unauthenticated router group.
func (h *Handler) RegisterPublicRoutes(rg *gin.RouterGroup) {
	rg.GET("/org/invites/validate/:token", h.validateInvite)
}

func (h *Handler) createOrg(c *gin.Context) {
	var input struct {
		Name string `json:"name" binding:"max=100"`
	}
	// The body is optional; the service defaults the name.
	if err := c.ShouldBindJSON(&input); err != nil && !errors.Is(err, io.EOF) {
		respond.Error(c, http.StatusBadRequest, "name must be at most 100 characters")
		return
	}
	o, err := h.svc.CreateWorkspace(c.Request.Context(), middleware.UserIDFrom(c), input.Name)
	if err != nil {
		if errors.Is(err, ErrOperatorOnly) {
			respond.Error(c, http.StatusForbidden, "only instance operators can create workspaces")
			return
		}
		h.log.Error("create org failed", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to create org")
		return
	}
	respond.JSON(c, http.StatusCreated, o)
}

func (h *Handler) getOrg(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	o, err := h.svc.GetByID(c.Request.Context(), orgID)
	if err != nil {
		respond.Error(c, http.StatusNotFound, "org not found")
		return
	}
	respond.JSON(c, http.StatusOK, o)
}

func (h *Handler) listOrgs(c *gin.Context) {
	userID := middleware.UserIDFrom(c)
	orgs, err := h.svc.ListOrgsForUser(c.Request.Context(), userID)
	if err != nil {
		h.log.Error("list orgs for user", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to list workspaces")
		return
	}
	respond.JSON(c, http.StatusOK, orgs)
}

func (h *Handler) switchOrg(c *gin.Context) {
	userID := middleware.UserIDFrom(c)
	var input struct {
		OrgID string `json:"org_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid request body")
		return
	}
	o, role, err := h.svc.SwitchActiveOrg(c.Request.Context(), userID, input.OrgID)
	if err != nil {
		if errors.Is(err, ErrNotMember) {
			respond.Error(c, http.StatusForbidden, "not a member of this workspace")
			return
		}
		h.log.Error("switch active org", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to switch workspace")
		return
	}
	respond.JSON(c, http.StatusOK, gin.H{"org_id": o.ID, "name": o.Name, "role": role})
}

func (h *Handler) updateOrgName(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role != "owner" {
		respond.Error(c, http.StatusForbidden, "forbidden: only owners can rename the organisation")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	var input struct {
		Name string `json:"name" binding:"required,max=100"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "name is required")
		return
	}
	before, _ := h.svc.GetByID(c.Request.Context(), orgID)
	o, err := h.svc.UpdateName(c.Request.Context(), orgID, input.Name)
	if err != nil {
		respond.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	if before != nil && before.Name != o.Name {
		h.svc.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "org.renamed", "org:"+orgID,
			map[string]any{"from": before.Name, "to": o.Name})
	}
	respond.JSON(c, http.StatusOK, o)
}

// DELETE /org — delete the active workspace and everything in it. Owners
// only; the body's confirm must be the workspace's name.
func (h *Handler) deleteOrg(c *gin.Context) {
	respond.Long(c, respond.LongWork) // stops and removes containers
	if middleware.MemberRoleFrom(c) != RoleOwner {
		respond.Error(c, http.StatusForbidden, "only owners can delete the workspace")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	var input struct {
		Confirm string `json:"confirm"`
	}
	_ = c.ShouldBindJSON(&input) // an empty confirm is refused below
	err := h.svc.DeleteWorkspace(c.Request.Context(), orgID, input.Confirm)
	switch {
	case err == nil:
		h.log.Info("workspace deleted", zap.String("org_id", orgID), zap.String("by", middleware.UserIDFrom(c)))
		respond.NoContent(c)
	case errors.Is(err, ErrConfirmName):
		respond.Error(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrWorkspaceInUse):
		respond.Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, ErrContentsNotRemoved):
		respond.Error(c, http.StatusBadGateway, err.Error())
	default:
		h.log.Error("delete workspace", zap.Error(err), zap.String("org_id", orgID))
		respond.Error(c, http.StatusInternalServerError, "failed to delete the workspace")
	}
}

func (h *Handler) listMembers(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	members, err := h.svc.ListMembers(c.Request.Context(), orgID)
	if err != nil {
		respond.Error(c, http.StatusInternalServerError, "failed to list members")
		return
	}
	respond.JSON(c, http.StatusOK, members)
}

func (h *Handler) listAddable(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role != RoleOwner {
		respond.Error(c, http.StatusForbidden, "forbidden: only owners can add members")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	accounts, err := h.svc.ListAddableAccounts(c.Request.Context(), orgID, middleware.UserIDFrom(c))
	if err != nil {
		h.log.Error("list addable accounts failed", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to list accounts")
		return
	}
	respond.JSON(c, http.StatusOK, accounts)
}

// AddMemberInput is the POST /org/members body: an existing account's email.
type AddMemberInput struct {
	Email string `json:"email" binding:"required,email,max=254"`
	Role  string `json:"role" binding:"required"`
}

func (h *Handler) addMember(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role != RoleOwner {
		respond.Error(c, http.StatusForbidden, "forbidden: only owners can add members")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	var input AddMemberInput
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "email and role are required")
		return
	}
	m, err := h.svc.AddMemberByEmail(c.Request.Context(), orgID, input.Email, input.Role)
	if err != nil {
		h.memberError(c, err, "failed to add the member")
		return
	}
	h.svc.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "member.added", "user:"+m.UserID, map[string]any{"role": m.Role})
	respond.JSON(c, http.StatusCreated, m)
}

func (h *Handler) updateMemberRole(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role != "owner" {
		respond.Error(c, http.StatusForbidden, "forbidden: only owners can change member roles")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	targetUserID := c.Param("userID")
	var input struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "role is required")
		return
	}
	previous, err := h.svc.UpdateMemberRole(c.Request.Context(), orgID, targetUserID, input.Role)
	if err != nil {
		h.memberError(c, err, "failed to change the role")
		return
	}
	if previous != input.Role {
		h.svc.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "member.role_changed", "user:"+targetUserID,
			map[string]any{"from": previous, "to": input.Role})
	}
	respond.NoContent(c)
}

func (h *Handler) removeMember(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role != "owner" {
		respond.Error(c, http.StatusForbidden, "forbidden: only owners can remove members")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	actorID := middleware.UserIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	targetUserID := c.Param("userID")
	previous, err := h.svc.RemoveMember(c.Request.Context(), orgID, actorID, targetUserID)
	if err != nil {
		h.memberError(c, err, "failed to remove the member")
		return
	}
	h.svc.WriteAuditLogDetail(c.Request.Context(), orgID, actorID, "member.removed", "user:"+targetUserID, map[string]any{"role": previous})
	respond.NoContent(c)
}

// POST /org/leave — any member leaves the active workspace, unless they are
// its last owner (409).
func (h *Handler) leave(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	userID := middleware.UserIDFrom(c)
	previous, err := h.svc.Leave(c.Request.Context(), orgID, userID)
	if err != nil {
		h.memberError(c, err, "failed to leave the workspace")
		return
	}
	h.svc.WriteAuditLogDetail(c.Request.Context(), orgID, userID, "member.left", "user:"+userID, map[string]any{"role": previous})
	respond.NoContent(c)
}

// memberError answers a refused or failed membership change.
func (h *Handler) memberError(c *gin.Context, err error, internalMsg string) {
	switch {
	case errors.Is(err, ErrInvalidRole), errors.Is(err, ErrCannotRemoveSelf):
		respond.Error(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotMember), errors.Is(err, ErrNotFound):
		respond.Error(c, http.StatusNotFound, ErrNotMember.Error())
	case errors.Is(err, ErrNoSuchAccount):
		respond.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrLastOwner), errors.Is(err, ErrAlreadyMember), errors.Is(err, ErrAccountInactive):
		respond.Error(c, http.StatusConflict, err.Error())
	default:
		h.log.Error(internalMsg, zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, internalMsg)
	}
}

func (h *Handler) listInvites(c *gin.Context) {
	// Owner-only: the list carries live tokens, and anyone holding one can join.
	if role := middleware.MemberRoleFrom(c); role != RoleOwner {
		respond.Error(c, http.StatusForbidden, "forbidden: only owners can view invites")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	invites, err := h.svc.ListInvites(c.Request.Context(), orgID)
	if err != nil {
		respond.Error(c, http.StatusInternalServerError, "failed to list invites")
		return
	}
	respond.JSON(c, http.StatusOK, invites)
}

func (h *Handler) createInvite(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role != "owner" {
		respond.Error(c, http.StatusForbidden, "forbidden: only owners can invite members")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	userID := middleware.UserIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	var input struct {
		Role string `json:"role"`
	}
	_ = c.ShouldBindJSON(&input)
	if input.Role == "" {
		input.Role = RoleDeveloper
	}
	inv, err := h.svc.CreateInvite(c.Request.Context(), orgID, userID, input.Role)
	if err != nil {
		if errors.Is(err, ErrInvalidRole) {
			respond.Error(c, http.StatusBadRequest, ErrInvalidRole.Error())
			return
		}
		h.log.Error("create invite failed", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to create invite")
		return
	}
	h.svc.WriteAuditLogDetail(c.Request.Context(), orgID, userID, "member.invited", "invite:"+inv.ID,
		map[string]any{"role": inv.Role, "expires_at": inv.ExpiresAt})
	respond.JSON(c, http.StatusCreated, inv)
}

func (h *Handler) revokeInvite(c *gin.Context) {
	if role := middleware.MemberRoleFrom(c); role != "owner" {
		respond.Error(c, http.StatusForbidden, "forbidden: only owners can revoke invites")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	inviteID := c.Param("inviteID")
	if err := h.svc.RevokeInvite(c.Request.Context(), orgID, inviteID); err != nil {
		respond.Error(c, http.StatusInternalServerError, "failed to revoke invite")
		return
	}
	h.svc.WriteAuditLog(c.Request.Context(), orgID, middleware.UserIDFrom(c), "invite.revoked", "invite:"+inviteID)
	respond.NoContent(c)
}

func (h *Handler) acceptInvite(c *gin.Context) {
	userID := middleware.UserIDFrom(c)
	var input struct {
		Token string `json:"token" binding:"required,max=512"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "token is required")
		return
	}
	o, err := h.svc.AcceptInvite(c.Request.Context(), input.Token, userID)
	if err != nil {
		if msg, ok := InviteErrorMessage(err); ok {
			respond.ErrorData(c, http.StatusBadRequest, msg, gin.H{"code": InviteErrorCode(err)})
			return
		}
		if errors.Is(err, ErrAlreadyMember) {
			respond.Error(c, http.StatusConflict, "you're already a member of this workspace")
			return
		}
		h.log.Error("accept invite failed", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to accept invite")
		return
	}
	respond.JSON(c, http.StatusOK, o)
}

// validateInvite is a public endpoint — no auth required, just reads invite metadata.
func (h *Handler) validateInvite(c *gin.Context) {
	token := c.Param("token")
	inv, err := h.svc.ValidateInvite(c.Request.Context(), token)
	if err != nil {
		if msg, ok := InviteErrorMessage(err); ok {
			respond.ErrorData(c, http.StatusBadRequest, msg, gin.H{"code": InviteErrorCode(err)})
			return
		}
		h.log.Error("validate invite failed", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to validate invite")
		return
	}
	o, err := h.svc.GetByID(c.Request.Context(), inv.OrgID)
	if err != nil {
		respond.Error(c, http.StatusInternalServerError, "failed to load org info")
		return
	}
	// org_id lets a signed-in member see they already belong; invited_by
	// names who sent the link. Both are for whoever holds the token.
	respond.JSON(c, http.StatusOK, gin.H{
		"invite_id":  inv.ID,
		"org_id":     inv.OrgID,
		"org_name":   o.Name,
		"role":       inv.Role,
		"expires_at": inv.ExpiresAt,
		"invited_by": inv.CreatorName,
	})
}

func (h *Handler) listAuditLog(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	filter, ok := auditFilter(c)
	if !ok {
		return
	}
	entries, err := h.svc.ListAuditLog(c.Request.Context(), orgID, filter)
	if errors.Is(err, ErrInvalidFilter) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		h.log.Error("list audit log failed", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to load audit log")
		return
	}
	var next string
	if n := len(entries); n > 0 && n == clampLimit(filter.Limit) {
		next = respond.CursorAt(entries[n-1].CreatedAt, entries[n-1].ID)
	}
	respond.JSONPage(c, http.StatusOK, entries, next)
}

// GET /org/audit-log/count — how many entries the same filters match, on
// every page.
func (h *Handler) countAuditLog(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	filter, ok := auditFilter(c)
	if !ok {
		return
	}
	total, err := h.svc.CountAuditLog(c.Request.Context(), orgID, filter)
	if errors.Is(err, ErrInvalidFilter) {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		h.log.Error("count audit log failed", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to count the audit log")
		return
	}
	respond.JSON(c, http.StatusOK, gin.H{"total": total})
}

// auditFilter reads the audit log's query: action (one), actions (several,
// comma-separated), actor (a user ID), agent (any, none or a connection's
// ID), api_key (any or none), q, resource, since and before
// (RFC 3339) with before_id, and limit. It writes the 400 itself.
func auditFilter(c *gin.Context) (AuditLogFilter, bool) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	f := AuditLogFilter{
		Action:   c.Query("action"),
		ActorID:  c.Query("actor"),
		Search:   c.Query("q"),
		Resource: c.Query("resource"),
		Agent:    c.Query("agent"),
		APIKey:   c.Query("api_key"),
		BeforeID: c.Query("before_id"),
		Limit:    limit,
	}
	if f.Agent != "" && f.Agent != "any" && f.Agent != "none" {
		id, err := uuid.Parse(f.Agent)
		if err != nil {
			respond.Error(c, http.StatusBadRequest, "agent must be any, none or an agent connection's ID")
			return f, false
		}
		f.Agent = id.String()
	}
	if f.APIKey != "" && f.APIKey != "any" && f.APIKey != "none" {
		respond.Error(c, http.StatusBadRequest, "api_key must be any or none")
		return f, false
	}
	if a := c.Query("actions"); a != "" {
		f.Actions = strings.Split(a, ",")
	}
	// ?cursor= is the page convention (D-59); before and before_id are the
	// older spelling of the same cursor and stay for one release.
	if at, id, err := respond.ParseCursor(c.Query("cursor")); err != nil {
		respond.Error(c, http.StatusBadRequest, err.Error())
		return f, false
	} else if id != "" {
		f.Before, f.BeforeID = &at, id
	}
	for _, p := range []struct {
		name string
		to   **time.Time
	}{{"since", &f.Since}, {"before", &f.Before}} {
		if v := c.Query(p.name); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				respond.Error(c, http.StatusBadRequest, p.name+" must be an RFC 3339 time")
				return f, false
			}
			*p.to = &t
		}
	}
	return f, true
}

// GET /org/settings
func (h *Handler) handleGetSettings(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	settings, err := h.svc.GetSettings(c.Request.Context(), orgID)
	if err != nil {
		respond.Error(c, http.StatusInternalServerError, "failed to get settings")
		return
	}
	respond.JSON(c, http.StatusOK, settings)
}

// PUT /org/settings
func (h *Handler) handleUpdateSettings(c *gin.Context) {
	if middleware.MemberRoleFrom(c) != RoleOwner {
		respond.Error(c, http.StatusForbidden, "only owners can update org settings")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	var input OrgSettings
	if err := c.ShouldBindJSON(&input); err != nil {
		respond.Error(c, http.StatusBadRequest, "invalid input")
		return
	}
	before, _ := h.svc.GetSettings(c.Request.Context(), orgID)
	// An API key can't change isolation (D-95): agents must not lower
	// their own sandbox.
	if input.DefaultIsolation != "" && (before == nil || before.DefaultIsolation != input.DefaultIsolation) && middleware.ViaAPIKey(c) {
		respond.ErrorData(c, http.StatusForbidden, "changing the workspace's isolation needs a signed-in session, not an API key",
			gin.H{"code": "session_required"})
		return
	}
	settings, err := h.svc.UpdateSettings(c.Request.Context(), orgID, input)
	if err != nil {
		if errors.Is(err, ErrInvalidSettings) {
			respond.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		respond.Error(c, http.StatusInternalServerError, "failed to update settings")
		return
	}
	if before == nil || before.ResourceBypassMinRole != settings.ResourceBypassMinRole {
		h.svc.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "org.capacity_rule_changed", "org:"+orgID,
			map[string]any{"to": settings.ResourceBypassMinRole})
	}
	if before != nil && before.DefaultIsolation != settings.DefaultIsolation {
		h.svc.WriteAuditLogDetail(c.Request.Context(), orgID, middleware.UserIDFrom(c), "org.isolation_changed", "org:"+orgID,
			map[string]any{"from": before.DefaultIsolation, "to": settings.DefaultIsolation})
	}
	respond.JSON(c, http.StatusOK, settings)
}

// PATCH /org/settings: hides Home's Finish setting up checklist for the
// workspace (7g); owners only.
func (h *Handler) handleHideSetup(c *gin.Context) {
	if middleware.MemberRoleFrom(c) != RoleOwner {
		respond.Error(c, http.StatusForbidden, "only owners can hide the setup checklist")
		return
	}
	var input struct {
		SetupHidden *bool `json:"setup_hidden" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || !*input.SetupHidden {
		respond.Error(c, http.StatusBadRequest, "setup_hidden must be true: a hidden checklist stays hidden")
		return
	}
	orgID := middleware.OrgIDFrom(c)
	settings, hidden, err := h.svc.HideSetup(c.Request.Context(), orgID)
	if err != nil {
		h.log.Error("hide the setup checklist", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to hide the setup checklist")
		return
	}
	if hidden {
		h.svc.WriteAuditLog(c.Request.Context(), orgID, middleware.UserIDFrom(c), "org.setup_hidden", "org:"+orgID)
	}
	respond.JSON(c, http.StatusOK, settings)
}

// GET /org/isolation: where the workspace's services can run Sandboxed
// (D-95). Every member reads it.
func (h *Handler) handleGetIsolation(c *gin.Context) {
	out, err := h.svc.Isolation(c.Request.Context(), middleware.OrgIDFrom(c))
	if err != nil {
		h.log.Error("read workspace isolation", zap.Error(err))
		respond.Error(c, http.StatusInternalServerError, "failed to read where Sandboxed services can run")
		return
	}
	respond.JSON(c, http.StatusOK, out)
}
