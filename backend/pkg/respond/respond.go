package respond

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"github.com/gin-gonic/gin"
)

type envelope struct {
	Data    any    `json:"data,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
	// RFC 9457 problem details beside the older error shape (D-59): new
	// clients read Code and Detail; Error and Data.code stay until /api/v2.
	Type   string `json:"type,omitempty"`
	Title  string `json:"title,omitempty"`
	Status int    `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
	Code   string `json:"code,omitempty"`
	// Field is the request field a problem is about, when it is about one.
	Field string `json:"field,omitempty"`
	// Next is the cursor of the following page of a list that can grow
	// (D-59); absent on the last page.
	Next string `json:"next,omitempty"`
	// Version is the version of a list a client can replace: what the PUT
	// must send back.
	Version string `json:"version,omitempty"`
}

// JSONVersioned writes a list a client can replace: data and the version of
// what was read, which the PUT must send back.
func JSONVersioned(c *gin.Context, status int, data any, version string) {
	c.JSON(status, envelope{Data: data, Version: version})
}

// Codes of a replace refused for the version it sent: the same on every
// endpoint that replaces a whole list.
const (
	CodeVersionRequired  = "version_required"
	CodeChangedSinceRead = "changed_since_read"
)

// VersionRefused answers err if it is a replace refused for its version
// (listversion) and reports whether it did.
func VersionRefused(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, listversion.ErrRequired):
		ErrorData(c, http.StatusBadRequest, err.Error(), gin.H{"code": CodeVersionRequired, "field": "version"})
	case errors.Is(err, listversion.ErrChanged):
		ErrorData(c, http.StatusConflict, err.Error(), gin.H{"code": CodeChangedSinceRead})
	default:
		return false
	}
	return true
}

// JSONPage writes a page of a list: data and, unless this is the last page,
// the cursor of the next one (sent back as ?cursor=).
func JSONPage(c *gin.Context, status int, data any, next string) {
	c.JSON(status, envelope{Data: data, Next: next})
}

// Cursor encodes where a page ended: the last row's time and ID, newest
// first, so the next page is every row older than it. Opaque to clients.
func Cursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id))
}

// CursorAt is Cursor for a time already formatted as RFC 3339.
func CursorAt(at, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at + "|" + id))
}

// ParseCursor reads a Cursor. An empty cursor is the first page.
func ParseCursor(s string) (at time.Time, id string, err error) {
	if s == "" {
		return time.Time{}, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", errors.New("cursor is not one this API issued")
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", errors.New("cursor is not one this API issued")
	}
	at, err = time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", errors.New("cursor is not one this API issued")
	}
	return at, parts[1], nil
}

// ProblemTypeBase prefixes a problem's "type" URI with its code.
const ProblemTypeBase = "https://toft.dev/problems/"

// problem fills the RFC 9457 fields of an error envelope. code and field
// come from data's entries when data is a map with them.
func problem(status int, msg string, data any) envelope {
	e := envelope{Error: msg, Data: data, Title: http.StatusText(status), Status: status, Detail: msg, Type: "about:blank"}
	var m map[string]any
	switch d := data.(type) {
	case gin.H:
		m = d
	case map[string]any:
		m = d
	}
	if code, ok := m["code"].(string); ok && code != "" {
		e.Code, e.Type = code, ProblemTypeBase+code
	}
	e.Field, _ = m["field"].(string)
	return e
}

// JSON writes a JSON response with the given status code and a data payload.
func JSON(c *gin.Context, status int, data any) {
	c.JSON(status, envelope{Data: data})
}

// Bare writes v as the whole body, without the envelope, for the formats
// another standard fixes: OAuth's token and error responses (RFC 6749) and
// its metadata documents (RFC 8414, RFC 9728).
func Bare(c *gin.Context, status int, v any) {
	c.Header("Cache-Control", "no-store")
	c.JSON(status, v)
}

// Message writes a plain message JSON response.
func Message(c *gin.Context, status int, msg string) {
	c.JSON(status, envelope{Message: msg})
}

// Error writes a standardized JSON error response and aborts the chain.
func Error(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, problem(status, msg, nil))
}

// Plain writes msg as text/plain and aborts the chain, for git's smart HTTP:
// git shows a refusal to its user only in that format, as "remote: <msg>".
func Plain(c *gin.Context, status int, msg string) {
	c.Abort()
	c.Data(status, "text/plain; charset=utf-8", []byte(msg+"\n"))
}

// NoContent writes a 204 response.
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}

// Stream lifts the server's WriteTimeout for a response that streams (server-
// sent events). The timeout counts from the request, so without this every
// stream ends at it, whatever it was in the middle of. Call it before the slow
// work starts. A client that leaves still ends the request; one that stays
// connected but stops reading holds its stream until TCP gives up.
func Stream(c *gin.Context) {
	// Fails only for a writer that cannot set deadlines; gin's can (respond_test).
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})
}

// Long gives a request that waits on servers (a delete that stops and removes
// containers, one by one, each up to Docker's 10-second stop timeout) d to
// answer instead of the server's WriteTimeout, which would cut the answer
// off after the work is done. Call it before the work starts.
func Long(c *gin.Context, d time.Duration) {
	// Fails only for a writer that cannot set deadlines; gin's can (respond_test).
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(d))
}

// LongWork is how long the requests that call Long may take.
const LongWork = 10 * time.Minute

// ErrorData writes an error response that also carries a structured payload
// (for example the violations behind a 422), and aborts the chain. Clients
// read the human message from "error" and the details from "data".
func ErrorData(c *gin.Context, status int, msg string, data any) {
	c.AbortWithStatusJSON(status, problem(status, msg, data))
}
