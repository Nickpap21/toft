package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/pkg/config"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/reauth"
	"github.com/gin-gonic/gin"
)

// The panel's browser sends these headers; a preflight that does not allow
// one blocks the request before it leaves the browser.
func TestCORSAllowsThePanelsHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(corsMiddleware(allowedOrigins(&config.Config{}, func() string { return "" })))
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/servers/s/exec", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight: %d", w.Code)
	}
	allowed := w.Header().Get("Access-Control-Allow-Headers")
	for _, h := range []string{"Authorization", "Content-Type", middleware.OrgHeader, reauth.Header} {
		if !strings.Contains(allowed, h) {
			t.Errorf("Access-Control-Allow-Headers %q lacks %s", allowed, h)
		}
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
}
