// Package sharedvolume is shared volumes (D-38): storage the platform creates
// and names, owned by a workspace, which several of its services on the same
// server mount, read-write or read-only, instead of bind-mounting a server
// folder. Design: private/specs/2026-10-12-shared-volumes-design.md.
package sharedvolume

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/listversion"
)

// DefaultSizeLimitBytes is a new volume's limit when none is given (10 GiB;
// founders, 2026-10-12).
const DefaultSizeLimitBytes int64 = 10 << 30

var (
	ErrNotFound      = errors.New("shared volume not found")
	ErrNameTaken     = errors.New("the workspace already has a shared volume with this name")
	ErrInUse         = errors.New("the shared volume is mounted by a service; remove the mount first")
	ErrInvalid       = errors.New("invalid shared volume")
	ErrUnknownServer = errors.New("server not found in this workspace")
	// ErrWrongServer: a service can mount only volumes on its own server.
	ErrWrongServer = errors.New("a service can only mount shared volumes on its own server")
	// ErrCompose: compose services do not mount shared volumes yet.
	ErrCompose = errors.New("docker compose services cannot mount shared volumes yet")
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Volume is a shared volume as the API shows it.
type Volume struct {
	ID string `json:"id"`
	// ServerID is "" for the control-plane server.
	ServerID       string      `json:"server_id"`
	ServerName     string      `json:"server_name"`
	Name           string      `json:"name"`
	SizeLimitBytes int64       `json:"size_limit_bytes"`
	UsedBytes      *int64      `json:"used_bytes"`
	UsedAt         *time.Time  `json:"used_at"`
	CreatedAt      time.Time   `json:"created_at"`
	Mounts         []MountedBy `json:"mounts"`
	orgID          string
}

// MountedBy is one service's mount of a volume.
type MountedBy struct {
	ServiceID   string `json:"service_id"`
	ServiceName string `json:"service_name"`
	Target      string `json:"target"`
	ReadOnly    bool   `json:"read_only"`
}

// Mount is one of a service's mounts.
type Mount struct {
	VolumeID string `json:"shared_volume_id"`
	// Name is the volume's name, in responses.
	Name     string `json:"name,omitempty"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
	serverID string
	orgID    string
}

// versionOf says which state of a service's mounts a client read, and a
// replace must send it back: a digest of what the replace can delete.
func versionOf(mounts []Mount) string {
	lines := make([]string, 0, len(mounts))
	for _, m := range mounts {
		lines = append(lines, fmt.Sprintf("%q %s %t", m.Target, m.VolumeID, m.ReadOnly))
	}
	return listversion.Of(lines)
}

// CreateInput is a new volume.
type CreateInput struct {
	Name           string `json:"name"`
	ServerID       string `json:"server_id"`
	SizeLimitBytes int64  `json:"size_limit_bytes"`
}

// Usage is a volume's size, for the alerts monitor. Measured is false when
// its server could not be reached or its Docker volume does not exist yet.
type Usage struct {
	ID, Name       string
	SizeLimitBytes int64
	UsedBytes      int64
	Measured       bool
}

// DockerName is the Docker volume a shared volume lives in.
func DockerName(id string) string { return "paas-shared-" + id }

// validTarget reports whether p can be a mount point: absolute, without ".."
// (the database checks the same).
func validTarget(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}
