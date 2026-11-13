package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// BodyLimit caps every request body at def bytes, or at the entry in routes
// for the request's route pattern ("PUT /api/v1/services/:serviceID/config").
// Past the cap the body reader fails, so ShouldBindJSON answers 400 and the
// connection closes; nothing is read into memory beyond the cap. Routes with
// a compose file or a config file get a larger cap, unauthenticated ones that
// store what they get a smaller one.
func BodyLimit(def int64, routes map[string]int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil {
			c.Next()
			return
		}
		n := def
		if v, ok := routes[c.Request.Method+" "+c.FullPath()]; ok {
			n = v
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}
