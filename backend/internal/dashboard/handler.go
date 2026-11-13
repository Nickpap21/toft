package dashboard

import (
	"context"
	"net/http"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/respond"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// reader loads Home's data for a workspace. Satisfied by *PostgresRepository.
type reader interface {
	Load(ctx context.Context, orgID, userID string) (*DashboardData, error)
}

// Handler serves Home (the Main board) in a single round-trip.
type Handler struct {
	repo reader
	log  *zap.Logger
}

// NewHandler creates a new dashboard Handler.
func NewHandler(repo reader, log *zap.Logger) *Handler {
	return &Handler{repo: repo, log: log}
}

// RegisterRoutes mounts the dashboard endpoint on the provided router group.
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/dashboard", h.getDashboard)
}

// ─── Response types ───────────────────────────────────────────────────────────

// DashboardData is Home's workspace-wide data: what the services and servers
// lists do not already give the page.
type DashboardData struct {
	Deploys24h     Deploys24h         `json:"deploys_24h"`
	FailedDeploys  []FailedDeploy     `json:"failed_deploys"`
	OpenAlerts     []OpenAlert        `json:"open_alerts"`
	Deploying      []LiveDeploy       `json:"deploying"`
	ProjectDeploys []ProjectDeploy    `json:"project_deploys"`
	RecentDeploys  []RecentDeployment `json:"recent_deployments"`
	// Work and Activity are the pull request and issue tiles and their
	// activity (D-88).
	Work     Work       `json:"work"`
	Activity []Activity `json:"activity"`
}

// Work counts open pull requests and issues in progress, and the person's.
type Work struct {
	PullsOpen         int64 `json:"pulls_open"`
	PullsYours        int64 `json:"pulls_yours"`
	IssuesInProgress  int64 `json:"issues_in_progress"`
	IssuesYours       int64 `json:"issues_yours"`
	IssuesShippedWeek int64 `json:"issues_shipped_week"`
	// The current cycle's number (0: the workspace doesn't use cycles) and
	// its shipped issues (D-90).
	CycleNumber        int   `json:"cycle_number"`
	IssuesShippedCycle int64 `json:"issues_shipped_cycle"`
}

// Activity is an issue opened or shipped, a pull request merged or a page
// edited.
type Activity struct {
	Kind       string    `json:"kind"`
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Automatic  bool      `json:"automatic"`
	ProjectID  string    `json:"project_id"`
	Project    string    `json:"project"`
	Title      string    `json:"title"`
	Key        string    `json:"key,omitempty"`
	Number     int       `json:"number,omitempty"`
	Repository string    `json:"repository,omitempty"`
	PageID     string    `json:"page_id,omitempty"`
}

// Deploys24h counts the last 24 hours' deploys. MedianSeconds is the median
// time, start to finish, of those that went live; 0 when none did.
type Deploys24h struct {
	Total         int64   `json:"total"`
	Succeeded     int64   `json:"succeeded"`
	Failed        int64   `json:"failed"`
	MedianSeconds float64 `json:"median_seconds"`
}

// FailedDeploy is a service whose latest deploy failed.
type FailedDeploy struct {
	ID              string    `json:"id"`
	ServiceID       string    `json:"service_id"`
	ServiceName     string    `json:"service_name"`
	ProjectID       string    `json:"project_id"`
	ProjectName     string    `json:"project_name"`
	EnvironmentName string    `json:"environment_name"`
	ServerName      string    `json:"server_name"`
	ErrorSummary    string    `json:"error_summary"`
	CommitSHA       string    `json:"commit_sha"`
	FailedAt        time.Time `json:"failed_at"`
}

// OpenAlert is an alert still firing. Subject is a service, server or shared
// volume ID, or "control-plane"; SubjectName is its name.
type OpenAlert struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	Subject         string    `json:"subject"`
	SubjectName     string    `json:"subject_name"`
	Detail          string    `json:"detail"`
	ProjectID       string    `json:"project_id"`
	ProjectName     string    `json:"project_name"`
	EnvironmentName string    `json:"environment_name"`
	FiredAt         time.Time `json:"fired_at"`
}

// LiveDeploy is a deploy in progress.
type LiveDeploy struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	ServiceID       string    `json:"service_id"`
	ServiceName     string    `json:"service_name"`
	SourceType      string    `json:"source_type"`
	HasComposeRepo  bool      `json:"has_compose_repo"`
	ProjectID       string    `json:"project_id"`
	ProjectName     string    `json:"project_name"`
	EnvironmentName string    `json:"environment_name"`
	ServerName      string    `json:"server_name"`
	CommitSHA       string    `json:"commit_sha"`
	CreatedAt       time.Time `json:"created_at"`
}

// ProjectDeploy is a project's latest deploy, and what it deployed from.
type ProjectDeploy struct {
	ProjectID   string    `json:"project_id"`
	Status      string    `json:"status"`
	ServiceName string    `json:"service_name"`
	SourceType  string    `json:"source_type"`
	GitBranch   string    `json:"git_branch"`
	DockerImage string    `json:"docker_image"`
	CreatedAt   time.Time `json:"created_at"`
}

// RecentDeployment is one of the workspace's latest deploys.
type RecentDeployment struct {
	ID              string     `json:"id"`
	Status          string     `json:"status"`
	ServiceID       string     `json:"service_id"`
	ServiceName     string     `json:"service_name"`
	ProjectID       string     `json:"project_id"`
	ProjectName     string     `json:"project_name"`
	EnvironmentName string     `json:"environment_name"`
	CreatedAt       time.Time  `json:"created_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

// ─── Handler ──────────────────────────────────────────────────────────────────

func (h *Handler) getDashboard(c *gin.Context) {
	orgID := middleware.OrgIDFrom(c)
	if orgID == "" {
		respond.Error(c, http.StatusForbidden, "no org context")
		return
	}
	data, err := h.repo.Load(c.Request.Context(), orgID, middleware.UserIDFrom(c))
	if err != nil {
		h.log.Error("dashboard load failed", zap.Error(err), zap.String("org_id", orgID))
		respond.Error(c, http.StatusInternalServerError, "failed to load dashboard")
		return
	}
	respond.JSON(c, http.StatusOK, data)
}
