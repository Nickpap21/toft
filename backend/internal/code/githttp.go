package code

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// GitHTTP is git's smart HTTP front door, at the URLs the boards draw
// (https://<panel>/<workspace>/<repository>.git). A client signs in with
// Basic auth, an API key as the password, or an agent's push credential for
// its branch (D-102); the username is not read.
type GitHTTP struct {
	svc  *Service
	keys middleware.APIKeyValidator
	log  *zap.Logger
}

func NewGitHTTP(svc *Service, keys middleware.APIKeyValidator, log *zap.Logger) *GitHTTP {
	return &GitHTTP{svc: svc, keys: keys, log: log}
}

// Routes git clients call, for the body limits (cmd/server/router.go).
const (
	RouteInfoRefs    = "/:workspace/:repo/info/refs"
	RouteUploadPack  = "/:workspace/:repo/git-upload-pack"
	RouteReceivePack = "/:workspace/:repo/git-receive-pack"
)

// MaxNegotiation caps an upload-pack request (wants and haves), unpacked.
const MaxNegotiation = 64 << 20

// RegisterRoutes mounts the three routes on the engine's root. limit runs
// before authentication on requests that carry credentials.
func (h *GitHTTP) RegisterRoutes(r gin.IRoutes, limit gin.HandlerFunc) {
	r.GET(RouteInfoRefs, limit, h.infoRefs)
	r.POST(RouteUploadPack, limit, h.rpc("upload-pack"))
	r.POST(RouteReceivePack, limit, h.rpc("receive-pack"))
}

// HasCredentials keys the limiter: only requests that make the backend check
// a key count.
func HasCredentials(c *gin.Context) string {
	if c.GetHeader("Authorization") == "" {
		return ""
	}
	return middleware.ClientKey(c)
}

func (h *GitHTTP) infoRefs(c *gin.Context) {
	service := strings.TrimPrefix(c.Query("service"), "git-")
	if !gitstore.Services[service] || !strings.HasPrefix(c.Query("service"), "git-") {
		// The dumb protocol, which Toft doesn't serve.
		respond.Plain(c, http.StatusForbidden, "only git's smart HTTP protocol is served: use git 1.6.6 or newer")
		return
	}
	t, actor, ok := h.authorize(c, service == "receive-pack")
	if !ok {
		return
	}
	protocol := c.GetHeader("Git-Protocol")
	h.serve(c, t, actor, service, "advertise", protocol, nil, func(w io.Writer) {
		// Protocol v2 starts with git's own capability advertisement.
		if !gitstore.IsV2(protocol) {
			io.WriteString(w, pktLine("# service=git-"+service+"\n")+"0000")
		}
	})
}

func (h *GitHTTP) rpc(service string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.ContentType() != "application/x-git-"+service+"-request" {
			respond.Plain(c, http.StatusUnsupportedMediaType, "not a git request")
			return
		}
		t, actor, ok := h.authorize(c, service == "receive-pack")
		if !ok {
			return
		}
		var body io.Reader = c.Request.Body
		if c.GetHeader("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(c.Request.Body)
			if err != nil {
				respond.Plain(c, http.StatusBadRequest, "the request is not gzip")
				return
			}
			defer gz.Close()
			body = gz
			if service == "upload-pack" {
				body = io.LimitReader(gz, MaxNegotiation)
			}
		}
		h.serve(c, t, actor, service, "stateless", c.GetHeader("Git-Protocol"), body, nil)
	}
}

// authorize reads the API key, finds the repository and checks the key's
// account may read it (or push to it); it answers the client itself when not.
func (h *GitHTTP) authorize(c *gin.Context, write bool) (*Target, Actor, bool) {
	if !h.svc.Enabled() {
		respond.Plain(c, http.StatusNotFound, "not found")
		return nil, Actor{}, false
	}
	_, password, hasAuth := c.Request.BasicAuth()
	if hasAuth && strings.HasPrefix(password, AgentBranchPrefix) {
		return h.authorizeAgent(c, password, write)
	}
	if !hasAuth || !strings.HasPrefix(password, "paas_") {
		c.Header("WWW-Authenticate", `Basic realm="Toft", charset="UTF-8"`)
		respond.Plain(c, http.StatusUnauthorized, "sign in with an API key as the password (Settings → API keys)")
		return nil, Actor{}, false
	}
	id, err := h.keys.ValidateAPIKey(c.Request.Context(), password)
	if err != nil {
		msg := "that API key is not valid"
		var expired *middleware.APIKeyExpiredError
		if errors.As(err, &expired) {
			msg = expired.Error()
		}
		c.Header("WWW-Authenticate", `Basic realm="Toft", charset="UTF-8"`)
		respond.Plain(c, http.StatusUnauthorized, msg)
		return nil, Actor{}, false
	}
	var actor Actor
	t, err := h.svc.Resolve(c.Request.Context(), c.Param("workspace")+"/"+c.Param("repo"))
	if err == nil {
		actor = Actor{UserID: id.UserID, ProjectID: id.ProjectID, Scopes: id.Scopes, ViaKey: true}
		err = h.svc.Authorize(c.Request.Context(), actor, t, write)
	}
	return h.authorized(c, t, actor, err)
}

// authorizeAgent signs an agent in with its branch's push credential: it
// reads its branch's repository and pushes to that branch alone (D-102).
func (h *GitHTTP) authorizeAgent(c *gin.Context, password string, write bool) (*Target, Actor, bool) {
	b, err := h.svc.AgentCredential(c.Request.Context(), password)
	if err != nil {
		c.Header("WWW-Authenticate", `Basic realm="Toft", charset="UTF-8"`)
		respond.Plain(c, http.StatusUnauthorized, "that push credential is not valid or has expired: call create_branch on the branch again for a new one")
		return nil, Actor{}, false
	}
	var actor Actor
	t, err := h.svc.Resolve(c.Request.Context(), c.Param("workspace")+"/"+c.Param("repo"))
	if err == nil && t.ID != b.RepoID {
		err = ErrNotFound
	}
	if err == nil {
		actor = Actor{UserID: b.UserID, ViaKey: true, AgentConnectionID: b.ConnectionID, AgentName: b.AgentName, PushOnly: b.Name, Paused: b.Paused}
		err = h.svc.Authorize(c.Request.Context(), actor, t, write)
	}
	return h.authorized(c, t, actor, err)
}

func (h *GitHTTP) authorized(c *gin.Context, t *Target, actor Actor, err error) (*Target, Actor, bool) {
	switch {
	case errors.Is(err, ErrNotFound):
		respond.Plain(c, http.StatusNotFound, "repository not found")
		return nil, Actor{}, false
	case errors.Is(err, ErrForbidden):
		respond.Plain(c, http.StatusForbidden, err.Error())
		return nil, Actor{}, false
	case err != nil:
		h.log.Error("git http: authorize", zap.Error(err))
		respond.Plain(c, http.StatusInternalServerError, "failed to read the repository")
		return nil, Actor{}, false
	}
	return t, actor, true
}

// serve streams the executor's answer to the client; first, if set, writes
// what goes before git's output.
func (h *GitHTTP) serve(c *gin.Context, t *Target, actor Actor, service, mode, protocol string, body io.Reader, first func(io.Writer)) {
	// A clone or a push may take longer than the server's timeouts; the
	// executor ends one that runs too long or stops moving.
	rc := http.NewResponseController(c.Writer)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	suffix := "result"
	if mode == "advertise" {
		suffix = "advertisement"
	}
	w := &lazyWriter{c: c, contentType: fmt.Sprintf("application/x-git-%s-%s", service, suffix), first: first}
	res, err := h.svc.Serve(c.Request.Context(), t, actor, service, mode, protocol, body, w)
	switch {
	case (errors.Is(err, ErrBranchDeployed) || errors.Is(err, ErrNotAgentBranch) || errors.Is(err, ErrAgentsPaused)) && !w.started:
		respond.Plain(c, http.StatusForbidden, err.Error())
	case errors.Is(err, gitstore.ErrBusy) && !w.started:
		c.Header("Retry-After", "10")
		respond.Plain(c, http.StatusServiceUnavailable, err.Error())
	case err != nil && !w.started:
		h.log.Error("git http", zap.Error(err), zap.String("repository", t.ID))
		respond.Plain(c, http.StatusBadGateway, "git is not answering")
	case err != nil:
		h.log.Warn("git http: cut off", zap.Error(err), zap.String("repository", t.ID))
	case res.Exit != 0:
		// git said what went wrong in the stream; this is for the log.
		h.log.Info("git http: git exited", zap.Int("exit", res.Exit), zap.String("stderr", res.Stderr), zap.String("service", service), zap.String("repository", t.ID))
	}
	if !w.started && err == nil {
		w.start()
	}
}

// lazyWriter writes the success headers on the first byte, so an executor
// that can't start still gets an error status.
type lazyWriter struct {
	c           *gin.Context
	contentType string
	first       func(io.Writer)
	started     bool
}

func (l *lazyWriter) start() {
	l.started = true
	h := l.c.Writer.Header()
	h.Set("Content-Type", l.contentType)
	h.Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	l.c.Status(http.StatusOK)
	if l.first != nil {
		l.first(l.c.Writer)
	}
}

func (l *lazyWriter) Write(p []byte) (int, error) {
	if !l.started {
		l.start()
	}
	n, err := l.c.Writer.Write(p)
	l.c.Writer.Flush()
	return n, err
}

// pktLine frames s as one git packet line.
func pktLine(s string) string {
	return fmt.Sprintf("%04x%s", len(s)+4, s)
}
