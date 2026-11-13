package observability

import (
	"fmt"
	"time"
)

// Config stores the PLG (Prometheus + Loki + Grafana) wiring for an org or project scope.
// A NULL project_id means the config applies to the entire org.
type Config struct {
	ID                  string    `json:"id"`
	OrgID               string    `json:"org_id"`
	ProjectID           string    `json:"project_id,omitempty"` // empty = org-scoped
	LokiServiceID       string    `json:"loki_service_id,omitempty"`
	GrafanaServiceID    string    `json:"grafana_service_id,omitempty"`
	PrometheusServiceID string    `json:"prometheus_service_id,omitempty"`
	LokiURL             string    `json:"loki_url,omitempty"`    // e.g. "http://loki:3100"
	GrafanaURL          string    `json:"grafana_url,omitempty"` // public domain once wired
	PrometheusURL       string    `json:"prometheus_url,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// UpsertLokiInput sets the Loki service binding.
type UpsertLokiInput struct {
	ServiceID string `json:"service_id" binding:"required"`
	LokiURL   string `json:"loki_url"`
}

// UpsertGrafanaInput sets the Grafana service binding.
type UpsertGrafanaInput struct {
	ServiceID  string `json:"service_id" binding:"required"`
	GrafanaURL string `json:"grafana_url"`
}

// UpsertPrometheusInput sets the Prometheus service binding.
type UpsertPrometheusInput struct {
	ServiceID string `json:"service_id" binding:"required"`
}

// ProjectSetting is a project's own Loki and Grafana ("" when it uses the
// workspace's).
type ProjectSetting struct {
	ProjectID  string `json:"project_id"`
	Name       string `json:"name"`
	LokiURL    string `json:"loki_url"`
	GrafanaURL string `json:"grafana_url"`
}

// LokiTarget is the Loki a project's or workspace's services send logs to:
// an address a person set, or the catalog Loki and the 127.0.0.1 port it is
// published on, on its own server (D-148).
type LokiTarget struct {
	URL       string
	ServiceID string // the catalog Loki's service
	ServerID  string // its server, "" for the panel's own
	Server    string // its server's name, "" for the panel's own
	Port      int    // its 127.0.0.1 port; 0 until it deployed with one
}

// ShipTo is where a service (serviceID) on serverID ("" the panel's own)
// sends its logs: the address set, else the catalog Loki's 127.0.0.1 port
// when both run on one server. A container alias counts as no address: it
// resolves only inside containers, never for a host's shipper, and rows
// from before D-48 still hold one. "" keeps the logs on the server: no
// Loki, Loki on another server, or Loki itself, whose own logs stay local
// rather than loop through it.
func (t LokiTarget) ShipTo(serverID, serviceID string) string {
	switch {
	case t.URL != "" && !IsContainerAlias(t.URL):
		return t.URL
	case t.Port != 0 && t.ServerID == serverID && t.ServiceID != serviceID:
		return fmt.Sprintf("http://127.0.0.1:%d", t.Port)
	}
	return ""
}
