package main

import (
	"context"
	"github.com/Nickpap21/toft/backend/internal/agenthub"
	"net/http"
	"time"

	"github.com/Nickpap21/toft/backend/api"
	"github.com/Nickpap21/toft/backend/internal/agent"
	"github.com/Nickpap21/toft/backend/internal/alert"
	"github.com/Nickpap21/toft/backend/internal/apikey"
	"github.com/Nickpap21/toft/backend/internal/auth"
	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/configfile"
	"github.com/Nickpap21/toft/backend/internal/dashboard"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/Nickpap21/toft/backend/internal/disk"
	"github.com/Nickpap21/toft/backend/internal/doc"
	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/environment"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/events"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/internal/inbox"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/issue"
	"github.com/Nickpap21/toft/backend/internal/mcp"
	"github.com/Nickpap21/toft/backend/internal/observability"
	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/Nickpap21/toft/backend/internal/preview"
	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/internal/reveal"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/internal/sharedvolume"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/internal/version"
	"github.com/Nickpap21/toft/backend/internal/webhook"
	"github.com/Nickpap21/toft/backend/pkg/config"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

// handlers is every HTTP handler the router mounts. The route table test
// (routes_test.go) builds them with nil services, so registration must not
// touch a service.
type handlers struct {
	auth          *auth.Handler
	user          *user.Handler
	instance      *instance.Handler
	org           *org.Handler
	server        *server.Handler
	project       *project.Handler
	environment   *environment.Handler
	service       *service.Handler
	domain        *domain.Handler
	envvar        *envvar.Handler
	reveal        *reveal.Handler
	observability *observability.Handler
	sharedVolume  *sharedvolume.Handler
	resources     *resources.Handler
	version       *version.Handler
	events        *events.Handler
	deployment    *deployment.Handler
	webhook       *webhook.Handler
	apiKey        *apikey.Handler
	dashboard     *dashboard.Handler
	catalog       *catalog.Handler
	configFile    *configfile.Handler
	deployPolicy  *deploypolicy.Handler
	alert         *alert.Handler
	code          *code.Handler
	gitHTTP       *code.GitHTTP
	issue         *issue.Handler
	doc           *doc.Handler
	inbox         *inbox.Handler
	preview       *preview.Handler
	agent         *agent.Handler
	disk          *disk.Handler
	agentHub      *agenthub.Hub
	mcp           *mcp.Server
}

// routerDeps is what the middleware needs besides the handlers.
type routerDeps struct {
	apiKeys      middleware.APIKeyValidator
	orgs         middleware.OrgLookup
	users        middleware.UserStatusChecker
	services     *service.ServiceManager
	environments *environment.Service
	// origins are the browser origins CORS allows besides the API's own.
	origins func() []string
	// db answers whether the panel's database does, for /api/v1/health;
	// nil (the route test) reads as up.
	db func(context.Context) error
}

// newRouter mounts every route (D-18: the route table test compares it with
// api/openapi.yaml). main wires the real services in; the test wires none.
func newRouter(cfg *config.Config, appLog *zap.Logger, h handlers, d routerDeps) *gin.Engine {
	r := gin.New()
	// Only these peers may say who the client is (X-Forwarded-For); Gin
	// otherwise believes every peer, which lets a client pick its own
	// address for the per-IP limiters.
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		appLog.Fatal("invalid TOFT_TRUSTED_PROXIES", zap.Error(err), zap.Strings("value", cfg.TrustedProxies))
	}
	r.Use(middleware.Recoverer(appLog))
	r.Use(middleware.RequestLogger(appLog))
	r.Use(corsMiddleware(d.origins))
	// Request bodies: 1 MiB, or 4 MiB where a compose file or a config file
	// travels (their contents are capped at 1 MiB by the API and the
	// database; JSON escaping and the other fields take the rest).
	const mib = 1 << 20
	r.Use(middleware.BodyLimit(mib, map[string]int64{
		"POST /api/v1/projects/:projectID/services":        4 * mib,
		"POST /api/v1/projects/:projectID/shared-services": 4 * mib,
		"POST /api/v1/shared-services":                     4 * mib,
		"PUT /api/v1/services/:serviceID/config":           4 * mib,
		"PUT /api/v1/services/:serviceID/config-files":     4 * mib,
		// git (D-71): a push up to receive.maxInputSize with its commands,
		// and a fetch's wants and haves (gzip, and capped again unpacked).
		"POST " + code.RouteReceivePack: gitstore.MaxPush + 64*mib,
		"POST " + code.RouteUploadPack:  code.MaxNegotiation,
		// OAuth's registration needs no account: ten redirect URIs and a
		// name fit in far less.
		"POST /api/v1/oauth/register": 64 << 10,
	}))

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	// For an uptime monitor outside the panel (D-68): on the panel's own
	// origin, under /api/, and 503 while its database doesn't answer.
	r.GET("/api/v1/health", func(c *gin.Context) {
		if d.db != nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
			defer cancel()
			if err := d.db(ctx); err != nil {
				respond.Error(c, http.StatusServiceUnavailable, "the panel's database is not answering")
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	// The contract of the running version, for the CLI, MCP and anyone with
	// a key (D-59). Public: it describes the API, it is not the API.
	r.GET("/api/v1/openapi.yaml", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", api.Spec)
	})
	r.GET("/api/v1/docs", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", api.Docs)
	})

	// git's smart HTTP at the boards' URLs (https://<panel>/<workspace>/
	// <repository>.git), outside /api/v1: git signs in with an API key in
	// Basic auth. Requests that make the backend check a key are limited per
	// address; a clone is two or three.
	h.gitHTTP.RegisterRoutes(r, middleware.KeyedRateLimiter(rate.Every(100*time.Millisecond), 60, code.HasCredentials))

	v1 := r.Group("/api/v1")

	// Agents (D-100): OAuth's metadata and public endpoints, 30 a minute
	// per address; and the MCP endpoint, 120 calls a minute per token
	// (burst 30), whose tools run through this router in-process.
	h.agent.RegisterPublicRoutes(r, v1, middleware.RateLimiter(rate.Every(2*time.Second), 30))

	// Servers' agents (D-106): the install script, the binary and the
	// connection, 30 a minute per address (servers behind one NAT share it).
	// The full address, not ClientKey's /64: a cloud subnet's servers share
	// one, and all reconnect when the panel restarts.
	h.server.RegisterAgentRoutes(v1, h.agentHub, middleware.KeyedRateLimiter(rate.Every(2*time.Second), 30, func(c *gin.Context) string { return c.ClientIP() }))
	// Per address first (10 a second, burst 60, initial values: several
	// clients behind one address fit, a header changed on every request or
	// none at all does not escape), then per token.
	mcpAddrLimit := middleware.RateLimiter(rate.Every(100*time.Millisecond), 60)
	mcpLimit := middleware.KeyedRateLimiter(rate.Every(500*time.Millisecond), 30, func(c *gin.Context) string {
		return c.GetHeader("Authorization")
	})
	mcpHandler := gin.WrapH(h.mcp.Handler(h.agent.IssuerOf))
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		r.Handle(method, "/mcp", mcpAddrLimit, mcpLimit, mcpHandler)
	}

	// Rate-limited auth endpoints (5 req/min per IP, burst of 3), and sign-in
	// also per account (10 per 15 minutes), which holds whatever address a
	// client presents.
	authGroup := v1.Group("/auth")
	authGroup.Use(middleware.RateLimiter(rate.Every(12*time.Second), 3))
	h.auth.RegisterRoutes(authGroup, middleware.KeyedRateLimiter(rate.Every(90*time.Second), 10, auth.LoginAccountKey))

	// Public endpoints (no auth required).
	h.webhook.RegisterRoutes(v1)

	// Public registration status and invite validation, read by the sign-up
	// and login pages on every load. Kept off the /auth limiter so page loads
	// don't spend the register/login burst; they have their own looser
	// per-IP limit.
	instancePublic := v1.Group("")
	instancePublic.Use(middleware.RateLimiter(rate.Every(time.Second), 10))
	h.org.RegisterPublicRoutes(instancePublic)
	// A reset link's token can't be guessed at speed either: 10 tries per 15
	// minutes per token (D-56).
	h.instance.RegisterPublicRoutes(instancePublic,
		middleware.KeyedRateLimiter(rate.Every(90*time.Second), 10, func(c *gin.Context) string { return c.Param("token") }))

	// Protected endpoints — JWT or API key required; deactivated users are
	// rejected; org context injected.
	protected := v1.Group("")
	protected.Use(middleware.AuthWithOrg(cfg.JWTSecret, d.apiKeys, d.orgs, d.users))
	// Project-scoped API keys reach only their project's deploys and
	// variables; everything else passes through.
	protected.Use(apikey.EnforceScopes(apiKeyProjects{services: d.services, environments: d.environments}))
	{
		// Operator changes (D-50), the confirmation host shells and host
		// keys need (D-53) and a password change ask for the password
		// again: 10 attempts per 15 minutes per account, as sign-in, in one
		// bucket for all.
		passwordLimit := middleware.KeyedRateLimiter(rate.Every(90*time.Second), 10, middleware.UserIDFrom)
		h.user.RegisterRoutes(protected, passwordLimit)
		h.instance.RegisterRoutes(protected, passwordLimit)
		h.auth.RegisterProtectedRoutes(protected, passwordLimit)
		h.org.RegisterRoutes(protected)
		h.server.RegisterRoutes(protected)
		h.project.RegisterRoutes(protected)
		h.environment.RegisterRoutes(protected)
		h.service.RegisterRoutes(protected)
		h.domain.RegisterRoutes(protected)
		h.envvar.RegisterRoutes(protected)
		h.reveal.RegisterRoutes(protected)
		h.deployment.RegisterRoutes(protected)
		h.apiKey.RegisterRoutes(protected)
		h.dashboard.RegisterRoutes(protected)
		h.catalog.RegisterRoutes(protected)
		h.configFile.RegisterRoutes(protected)
		h.deployPolicy.RegisterRoutes(protected)
		h.alert.RegisterRoutes(protected)
		h.observability.RegisterRoutes(protected)
		h.webhook.RegisterProtectedRoutes(protected)
		h.sharedVolume.RegisterRoutes(protected)
		h.resources.RegisterRoutes(protected)
		h.version.RegisterRoutes(protected)
		h.events.RegisterRoutes(protected)
		h.code.RegisterRoutes(protected)
		h.issue.RegisterRoutes(protected)
		h.doc.RegisterRoutes(protected)
		h.inbox.RegisterRoutes(protected)
		h.preview.RegisterRoutes(protected)
		h.agent.RegisterRoutes(protected)
		h.disk.RegisterRoutes(protected)
	}

	return r
}
