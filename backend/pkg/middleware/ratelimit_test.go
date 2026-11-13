package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

func limitedRouter(t *testing.T, trusted []string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	require.NoError(t, r.SetTrustedProxies(trusted))
	r.GET("/x", RateLimiter(rate.Every(time.Hour), 1), func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func get(r *gin.Engine, remoteAddr, forwardedFor string) int {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// A peer outside the trusted ranges cannot pick its own address: every
// request from it counts against its real one, whatever header it sends.
func TestForwardedForIgnoredFromUntrustedPeer(t *testing.T) {
	r := limitedRouter(t, []string{"10.0.0.0/8"})
	assert.Equal(t, http.StatusOK, get(r, "203.0.113.9:1234", "1.1.1.1"))
	assert.Equal(t, http.StatusTooManyRequests, get(r, "203.0.113.9:1234", "2.2.2.2"))
}

// The proxy's header is believed, so two clients behind it have two limits.
func TestForwardedForHonouredFromTrustedProxy(t *testing.T) {
	r := limitedRouter(t, []string{"10.0.0.0/8"})
	assert.Equal(t, http.StatusOK, get(r, "10.0.0.2:1234", "1.1.1.1"))
	assert.Equal(t, http.StatusOK, get(r, "10.0.0.2:1234", "2.2.2.2"))
	assert.Equal(t, http.StatusTooManyRequests, get(r, "10.0.0.2:1234", "1.1.1.1"))
}

func TestKeyedLimiterSkipsEmptyKeysAndStaysBounded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	seen := map[string]*ipLimiter{}
	for i := 0; i < maxVisitors; i++ {
		seen[string(rune(i))+"k"] = &ipLimiter{}
	}
	evictOldest(seen, maxVisitors/10)
	assert.Len(t, seen, maxVisitors-maxVisitors/10)

	r := gin.New()
	r.GET("/x", KeyedRateLimiter(rate.Every(time.Hour), 1, func(*gin.Context) string { return "" }), func(c *gin.Context) { c.Status(http.StatusOK) })
	for i := 0; i < 3; i++ {
		assert.Equal(t, http.StatusOK, get(r, "1.2.3.4:1", ""), "an empty key is not limited")
	}
}

// A refused request says when to come back, and waiting is not reset by the
// refusal itself (it takes no token).
func TestRefusalSaysWhenToRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", RateLimiter(rate.Every(90*time.Second), 1), func(c *gin.Context) { c.Status(http.StatusOK) })
	assert.Equal(t, http.StatusOK, get(r, "1.2.3.4:1", ""))
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.RemoteAddr = "1.2.3.4:1"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusTooManyRequests, w.Code)
		secs, err := strconv.Atoi(w.Header().Get("Retry-After"))
		require.NoError(t, err)
		assert.InDelta(t, 90, secs, 1)
		assert.Contains(t, w.Body.String(), `"retry_after":`)
	}
}

// One IPv6 host holds a /64: every address in it shares one limit, and the
// next /64 has its own.
func TestIPv6SharesALimitPer64(t *testing.T) {
	r := limitedRouter(t, nil)
	assert.Equal(t, http.StatusOK, get(r, "[2001:db8:1:2::1]:1234", ""))
	assert.Equal(t, http.StatusTooManyRequests, get(r, "[2001:db8:1:2:ffff::9]:1234", ""))
	assert.Equal(t, http.StatusOK, get(r, "[2001:db8:1:3::1]:1234", ""))
	assert.Equal(t, http.StatusOK, get(r, "203.0.113.7:1", ""))
	assert.Equal(t, http.StatusTooManyRequests, get(r, "[::ffff:203.0.113.7]:1", ""), "a mapped IPv4 address is the IPv4 address")
}
