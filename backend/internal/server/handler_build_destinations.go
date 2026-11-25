package server

import (
	"errors"
	"net/http"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The build destinations of a server (/servers/:serverID/build-destinations)
// and of the panel's own (/instance/build-destinations, no server ID).
// Instance operators only; a change needs a session and the password. The
// audit rows are the service's, saved with the change.

// buildDestinationsWork is how long a change may take to answer: the write
// to its server, and the saved list written back when that went wrong.
const buildDestinationsWork = 3 * buildDestinationsTimeout

func (h *Handler) listBuildDestinations(c *gin.Context) {
	list, err := h.service.ListBuildDestinations(c.Request.Context(), middleware.UserIDFrom(c), c.Param("serverID"))
	if err != nil {
		h.writeBuildDestinationError(c, err)
		return
	}
	out := make([]BuildDestinationResponse, 0, len(list))
	for _, d := range list {
		out = append(out, toBuildDestinationResponse(d))
	}
	respond.JSON(c, http.StatusOK, out)
}

func (h *Handler) addBuildDestination(c *gin.Context) {
	ctx, userID := c.Request.Context(), middleware.UserIDFrom(c)
	// Refuse before reading the body so non-operators always get a 403.
	if err := h.service.AuthorizeBuildDestinations(ctx, userID); err != nil {
		h.writeBuildDestinationError(c, err)
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	var in BuildDestinationInput
	if err := c.ShouldBindJSON(&in); err != nil {
		h.writeBuildDestinationError(c, &BuildDestinationError{Code: codeBuildDestinationInvalid, Field: "address", Message: "an address is required"})
		return
	}
	respond.Long(c, buildDestinationsWork)
	d, err := h.service.AddBuildDestination(ctx, userID, c.Param("serverID"), in)
	if err != nil {
		h.writeBuildDestinationError(c, err)
		return
	}
	respond.JSON(c, http.StatusCreated, toBuildDestinationResponse(d))
}

func (h *Handler) removeBuildDestination(c *gin.Context) {
	ctx, userID := c.Request.Context(), middleware.UserIDFrom(c)
	if err := h.service.AuthorizeBuildDestinations(ctx, userID); err != nil {
		h.writeBuildDestinationError(c, err)
		return
	}
	if !middleware.RequireReauth(c) {
		return
	}
	respond.Long(c, buildDestinationsWork)
	if err := h.service.RemoveBuildDestination(ctx, userID, c.Param("serverID"), c.Param("destinationID")); err != nil {
		h.writeBuildDestinationError(c, err)
		return
	}
	respond.NoContent(c)
}

// writeBuildDestinationError answers a refused list or change: 403 for
// non-operators, 400 for a malformed entry (with its field), 409 for one
// the list or the server doesn't take, 404 for an entry that isn't there,
// 502 when the server didn't take the list (why is in the log, not the
// answer), 500 otherwise.
func (h *Handler) writeBuildDestinationError(c *gin.Context, err error) {
	var refused *BuildDestinationError
	switch {
	case errors.Is(err, ErrBuildDestinationsOperatorOnly):
		respond.Error(c, http.StatusForbidden, err.Error())
	case errors.As(err, &refused) && refused.Code == codeBuildDestinationInvalid:
		respond.ErrorData(c, http.StatusBadRequest, refused.Message, gin.H{"code": refused.Code, "field": refused.Field})
	case errors.As(err, &refused):
		respond.ErrorData(c, http.StatusConflict, refused.Message, gin.H{"code": refused.Code})
	case errors.Is(err, ErrBuildDestinationNotFound):
		respond.Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrBuildDestinationsNotApplied):
		h.log.Warn("build destinations not applied", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.ErrorData(c, http.StatusBadGateway, ErrBuildDestinationsNotApplied.Error(), gin.H{"code": codeBuildDestinationsUnset})
	default:
		h.log.Error("build destinations request failed", zap.Error(err), zap.String("server_id", c.Param("serverID")))
		respond.Error(c, http.StatusInternalServerError, "failed to change the build destinations")
	}
}
