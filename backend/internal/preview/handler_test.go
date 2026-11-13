package preview

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// A limit outside 1 to 20 is the caller's mistake: 400 bad_max.
func TestBadMaxIsABadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	(&Handler{log: zap.NewNop()}).fail(c, "save the previews settings", fmt.Errorf("set: %w", ErrBadMax))
	var out struct {
		Data struct{ Code string } `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusBadRequest || out.Data.Code != "bad_max" {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
