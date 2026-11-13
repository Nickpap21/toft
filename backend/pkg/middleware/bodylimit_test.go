package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(100, map[string]int64{"PUT /big/:id": 1000}))
	echo := func(c *gin.Context) {
		var in struct {
			V string `json:"v"`
		}
		if err := c.ShouldBindJSON(&in); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusOK)
	}
	r.POST("/small", echo)
	r.PUT("/big/:id", echo)

	post := func(method, path string, size int) int {
		body := `{"v":"` + strings.Repeat("x", size) + `"}`
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code
	}
	assert.Equal(t, http.StatusOK, post(http.MethodPost, "/small", 50))
	assert.Equal(t, http.StatusBadRequest, post(http.MethodPost, "/small", 500))
	assert.Equal(t, http.StatusOK, post(http.MethodPut, "/big/1", 500), "the route with the larger cap")
	assert.Equal(t, http.StatusBadRequest, post(http.MethodPut, "/big/1", 5000))
}
