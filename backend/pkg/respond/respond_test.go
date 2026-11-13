package respond_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A stream outlives the server's WriteTimeout only with respond.Stream.
func TestStreamOutlivesWriteTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, lifted := range []bool{true, false} {
		r := gin.New()
		r.GET("/", func(c *gin.Context) {
			if lifted {
				respond.Stream(c)
			}
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(c.Writer, "data: first\n\n")
			c.Writer.Flush()
			time.Sleep(300 * time.Millisecond) // past the WriteTimeout
			_, _ = io.WriteString(c.Writer, "data: late\n\n")
			c.Writer.Flush()
		})
		srv := httptest.NewUnstartedServer(r)
		srv.Config.WriteTimeout = 100 * time.Millisecond
		srv.Start()

		res, err := http.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body) // a cut stream ends in an error; the body read so far counts
		res.Body.Close()
		srv.Close()

		if got := strings.Contains(string(body), "data: late"); got != lifted {
			t.Errorf("with Stream %v: late event delivered = %v, body %q", lifted, got, body)
		}
	}
}

// A slow request answers past the server's WriteTimeout only with Long.
func TestLongAnswersPastWriteTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, lifted := range []bool{true, false} {
		r := gin.New()
		r.DELETE("/", func(c *gin.Context) {
			if lifted {
				respond.Long(c, time.Second)
			}
			time.Sleep(300 * time.Millisecond) // the work, past the WriteTimeout
			respond.NoContent(c)
		})
		srv := httptest.NewUnstartedServer(r)
		srv.Config.WriteTimeout = 100 * time.Millisecond
		srv.Start()

		req, _ := http.NewRequest(http.MethodDelete, srv.URL, nil)
		res, err := http.DefaultClient.Do(req)
		if lifted {
			require.NoError(t, err)
			assert.Equal(t, http.StatusNoContent, res.StatusCode)
			res.Body.Close()
		} else {
			assert.Error(t, err, "the answer is cut off")
		}
		srv.Close()
	}
}

// An error carries the RFC 9457 fields beside the older shape (D-59); the
// code, when there is one, names the problem type.
func TestErrorIsAProblem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/plain", func(c *gin.Context) { respond.Error(c, http.StatusNotFound, "no such thing") })
	r.GET("/coded", func(c *gin.Context) {
		respond.ErrorData(c, http.StatusForbidden, "confirm your password", gin.H{"code": "reauth_required"})
	})
	r.GET("/field", func(c *gin.Context) {
		respond.ErrorData(c, http.StatusBadRequest, "too long", gin.H{"code": "password_too_long", "field": "password"})
	})
	get := func(path string) map[string]any {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	plain := get("/plain")
	for k, want := range map[string]any{"error": "no such thing", "detail": "no such thing", "title": "Not Found", "status": float64(404), "type": "about:blank"} {
		if plain[k] != want {
			t.Errorf("plain[%s] = %v, want %v", k, plain[k], want)
		}
	}
	if _, has := plain["code"]; has {
		t.Errorf("plain error has a code: %v", plain["code"])
	}
	if field := get("/field"); field["field"] != "password" {
		t.Errorf("field = %v", field)
	}
	if _, has := get("/coded")["field"]; has {
		t.Error("a problem about no field has one")
	}
	coded := get("/coded")
	if coded["code"] != "reauth_required" || coded["type"] != respond.ProblemTypeBase+"reauth_required" {
		t.Errorf("coded = %v", coded)
	}
	if coded["data"].(map[string]any)["code"] != "reauth_required" {
		t.Errorf("data.code kept for older clients: %v", coded["data"])
	}
}
