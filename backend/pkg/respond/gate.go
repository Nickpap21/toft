package respond

import (
	"errors"

	"github.com/Nickpap21/toft/backend/pkg/actor"
	"github.com/gin-gonic/gin"
)

// Gate writes the agent policy's answer when err is one (D-100, D-102): a
// refusal with its code, or the 202 of a call waiting for a person with
// its approval. It reports whether it wrote one.
func Gate(c *gin.Context, err error) bool {
	var ge *actor.GateError
	if !errors.As(err, &ge) {
		return false
	}
	data := gin.H{"code": ge.Code}
	if ge.ApprovalID != "" {
		data["approval_id"] = ge.ApprovalID
	}
	if ge.Status < 400 {
		JSON(c, ge.Status, data)
	} else {
		ErrorData(c, ge.Status, ge.Message, data)
	}
	c.Abort()
	return true
}
