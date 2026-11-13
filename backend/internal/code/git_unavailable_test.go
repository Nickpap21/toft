package code

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// A read while the git server's socket is missing answers 503
// git_unavailable, not a bare 500, so the panel can say git is down
// rather than skeleton forever (QA #13).
func TestReadWithoutTheGitServerIsGitUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	err := gitstore.NewClient(filepath.Join(t.TempDir(), "git.sock")).Browse(context.Background(), "r1", "summary", nil, &struct{}{})
	require.Error(t, err)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	(&Handler{log: zap.NewNop()}).browseError(c, "repository", err)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	var body struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
		Data   struct {
			Code string `json:"code"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "git_unavailable", body.Code)
	assert.Equal(t, "git_unavailable", body.Data.Code)
	assert.NotContains(t, body.Detail, "git.sock", "the socket path is the log's, not the client's")
}
