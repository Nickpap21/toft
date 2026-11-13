package configfile

import "time"

// ConfigFile represents a file to be written into a container at deploy time.
type ConfigFile struct {
	ID        string    `json:"id"`
	ServiceID string    `json:"service_id"`
	Path      string    `json:"path"` // container destination path, e.g. /etc/nginx/conf.d/default.conf
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UpsertInput is the write payload for creating or updating a config file.
type UpsertInput struct {
	Path    string `json:"path" binding:"required,max=4096"`
	Content string `json:"content"`
}
