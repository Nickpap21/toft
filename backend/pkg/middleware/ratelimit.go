package middleware

import (
	"math"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// ipLimiter holds a rate limiter per IP with a last-seen timestamp for cleanup.
type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// maxVisitors bounds each limiter's map: past it, the oldest tenth is
// dropped, so a flood of distinct keys (forged addresses) cannot grow it
// without end. The dropped keys start a fresh burst, which is what a new key
// gets anyway.
const maxVisitors = 10_000

// KeyedRateLimiter returns middleware that rate-limits by an arbitrary string key
// extracted from the request via keyFn. Useful for per-resource limits (e.g. per service ID).
// An empty key is not limited (keyFn could not identify the caller).
func KeyedRateLimiter(r rate.Limit, b int, keyFn func(*gin.Context) string) gin.HandlerFunc {
	visitors := make(map[string]*ipLimiter)
	var mu sync.Mutex

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			for k, v := range visitors {
				if time.Since(v.lastSeen) > 10*time.Minute {
					delete(visitors, k)
				}
			}
			mu.Unlock()
		}
	}()

	getVisitor := func(key string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		v, ok := visitors[key]
		if !ok {
			if len(visitors) >= maxVisitors {
				evictOldest(visitors, maxVisitors/10)
			}
			v = &ipLimiter{limiter: rate.NewLimiter(r, b)}
			visitors[key] = v
		}
		v.lastSeen = time.Now()
		return v.limiter
	}

	return func(c *gin.Context) {
		key := keyFn(c)
		if key == "" {
			c.Next()
			return
		}
		// A refused request takes no token, and says when the next one
		// would pass: Retry-After, and retry_after in the body for clients
		// that read only the body.
		res := getVisitor(key).Reserve()
		if d := res.Delay(); d > 0 {
			res.Cancel()
			secs := int(math.Ceil(d.Seconds()))
			c.Header("Retry-After", strconv.Itoa(secs))
			respond.ErrorData(c, http.StatusTooManyRequests, "too many requests — slow down", gin.H{"retry_after": secs})
			return
		}
		c.Next()
	}
}

// RateLimiter returns middleware that allows r requests per second per
// client (ClientKey), with a burst of b. Stale entries (idle > 10 min) are
// cleaned up periodically.
func RateLimiter(r rate.Limit, b int) gin.HandlerFunc {
	return KeyedRateLimiter(r, b, ClientKey)
}

// ClientKey is the client's address as a per-IP limiter counts it: an IPv4
// address, or an IPv6 address's /64. One host is usually given a whole /64,
// so keying on the full address would let it pick a fresh limit per request.
func ClientKey(c *gin.Context) string {
	ip := c.ClientIP()
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is4() {
		return a.String()
	}
	p, _ := a.Prefix(64) // cannot fail for an IPv6 address
	return p.String()
}

// evictOldest removes the n least recently seen entries. Linear: it runs
// once per 1,000 new keys at the cap.
func evictOldest(visitors map[string]*ipLimiter, n int) {
	type kv struct {
		k string
		t time.Time
	}
	all := make([]kv, 0, len(visitors))
	for k, v := range visitors {
		all = append(all, kv{k, v.lastSeen})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	for i := 0; i < n && i < len(all); i++ {
		delete(visitors, all[i].k)
	}
}
