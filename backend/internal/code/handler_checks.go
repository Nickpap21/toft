package code

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// Commit checks and pipelines over HTTP (D-77).

func (h *Handler) registerChecks(repo *gin.RouterGroup) {
	// 120 reports a minute per repository: CI reports a few per run.
	reportRL := middleware.KeyedRateLimiter(rate.Every(time.Minute/120), 120,
		func(c *gin.Context) string { return "checks:" + c.Param("repositoryID") })
	repo.GET("/commits/:sha/checks", h.getChecks)
	repo.POST("/commits/:sha/checks", reportRL, h.reportCheck)
	repo.GET("/pipelines", h.pipelines)
}

func (h *Handler) getChecks(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	cc, err := h.svc.Checks(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("sha"))
	if err != nil {
		h.fail(c, "checks", err)
		return
	}
	respond.JSON(c, http.StatusOK, cc)
}

// reportCheck is what an external CI calls: owners and developers, and API
// keys (a project key needs the checks scope, which apikey enforces).
func (h *Handler) reportCheck(c *gin.Context) {
	if !h.inProject(c) || !canWrite(c) {
		return
	}
	var in CheckInput
	if err := c.ShouldBindJSON(&in); err != nil {
		respond.ErrorData(c, http.StatusBadRequest, "name and state are required", gin.H{"code": "bad_check_name"})
		return
	}
	check, created, err := h.svc.ReportCheck(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Param("sha"), middleware.UserIDFrom(c), in)
	if err != nil {
		h.fail(c, "report check", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	respond.JSON(c, status, check)
}

func (h *Handler) pipelines(c *gin.Context) {
	if !h.inProject(c) {
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 {
		respond.ErrorData(c, http.StatusBadRequest, "limit must be a whole number", gin.H{"code": "bad_limit"})
		return
	}
	out, next, err := h.svc.Pipelines(c.Request.Context(), c.Param("projectID"), c.Param("repositoryID"), c.Query("branch"), c.Query("cursor"), min(limit, 200))
	if err != nil {
		h.fail(c, "pipelines", err)
		return
	}
	respond.JSONPage(c, http.StatusOK, out, next)
}

// repoInUse answers a repository services build from, naming them.
func repoInUse(c *gin.Context, err error) bool {
	var inUse *RepoInUseError
	if errors.As(err, &inUse) {
		respond.ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": "repository_in_use", "services": inUse.Services})
		return true
	}
	return false
}
