// Package disk is the disk advisor (D-135): what fills a host's disk, what
// of it is safe to delete, and deleting it through the Docker API, by hand
// or automatically, under a policy with defaults and settings.
package disk

import (
	"errors"
	"fmt"
	"time"
)

// Kind is a class of image. InUse, Rollback and PanelReleases are kept;
// the others are removable.
type Kind string

const (
	InUse            Kind = "in_use"
	Rollback         Kind = "rollback"
	PanelReleases    Kind = "panel_releases"
	Untagged         Kind = "untagged"
	OldBuilds        Kind = "old_builds"
	OldPanelReleases Kind = "old_panel_releases"
	UnusedPulled     Kind = "unused_pulled"
)

// kindOrder is the order classes are reported in, kept first.
var kindOrder = []Kind{InUse, Rollback, PanelReleases, Untagged, OldBuilds, OldPanelReleases, UnusedPulled}

// cleanOrder is the order a clean removes classes in: builds and releases
// first, so the untagged images they leave go with them (PruneChildren).
var cleanOrder = []Kind{OldPanelReleases, OldBuilds, UnusedPulled, Untagged}

// Removable reports whether a clean may remove the class.
func (k Kind) Removable() bool {
	switch k {
	case Untagged, OldBuilds, OldPanelReleases, UnusedPulled:
		return true
	}
	return false
}

// Defaults, the same for every host unless its policy says otherwise.
const (
	DefaultRollbackKept   = 10
	MaxRollbackKept       = 100
	DefaultAlertPercent   = 85
	alertResolveGap       = 5
	defaultCachePercent   = 10
	minCacheLimit         = int64(1_000_000_000)
	MinAlertPercent       = 50
	MaxAlertPercent       = 99
	fallbackCacheLimit    = int64(10_000_000_000) // when the disk's size is unknown
	readTTL               = 5 * time.Minute
	automaticCleanEvery   = 24 * time.Hour
	automaticCleanStartIn = 15 * time.Minute
)

// DefaultAutoClean is what the automatic clean removes unless the policy
// says otherwise; the panel's own server also removes old panel releases.
func DefaultAutoClean(panel bool) []Kind {
	if panel {
		return []Kind{Untagged, OldBuilds, OldPanelReleases}
	}
	return []Kind{Untagged, OldBuilds}
}

// Policy is a host's stored disk policy: nil fields follow the default.
type Policy struct {
	BuildCacheLimitBytes *int64
	AutoClean            []Kind // nil: the default; empty: off
	AlertPercent         *int
}

// PolicyValues are a policy with every value filled in.
type PolicyValues struct {
	BuildCacheLimitBytes int64  `json:"build_cache_limit_bytes"`
	AutoClean            []Kind `json:"auto_clean"`
	AlertPercent         int    `json:"alert_percent"`
	AlertResolvePercent  int    `json:"alert_resolve_percent"`
}

// PolicyView is the policy as the API shows it: stored, effective and the
// defaults.
type PolicyView struct {
	BuildCacheLimitBytes *int64       `json:"build_cache_limit_bytes"`
	AutoClean            []Kind       `json:"auto_clean"`
	AlertPercent         *int         `json:"alert_percent"`
	Effective            PolicyValues `json:"effective"`
	Defaults             PolicyValues `json:"defaults"`
}

// defaults are a host's defaults; size is its disk's size (0 unknown).
func defaults(panel bool, size int64) PolicyValues {
	limit := fallbackCacheLimit
	if size > 0 {
		limit = max(size*defaultCachePercent/100, minCacheLimit)
	}
	return PolicyValues{BuildCacheLimitBytes: limit, AutoClean: DefaultAutoClean(panel), AlertPercent: DefaultAlertPercent, AlertResolvePercent: DefaultAlertPercent - alertResolveGap}
}

// view fills p in from the defaults.
func (p Policy) view(panel bool, size int64) PolicyView {
	d := defaults(panel, size)
	e := d
	if p.BuildCacheLimitBytes != nil {
		e.BuildCacheLimitBytes = *p.BuildCacheLimitBytes
	}
	if p.AutoClean != nil {
		e.AutoClean = p.AutoClean
	}
	if p.AlertPercent != nil {
		e.AlertPercent = *p.AlertPercent
		e.AlertResolvePercent = *p.AlertPercent - alertResolveGap
	}
	return PolicyView{BuildCacheLimitBytes: p.BuildCacheLimitBytes, AutoClean: p.AutoClean, AlertPercent: p.AlertPercent, Effective: e, Defaults: d}
}

// Validate checks a policy for a host whose disk is size bytes (0 unknown).
func (p Policy) Validate(panel bool, size int64) error {
	if l := p.BuildCacheLimitBytes; l != nil && (*l < minCacheLimit || size > 0 && *l > size) {
		return fmt.Errorf("%w: build_cache_limit_bytes is at least 1 GB and at most the disk's size", ErrInvalidPolicy)
	}
	if a := p.AlertPercent; a != nil && (*a < MinAlertPercent || *a > MaxAlertPercent) {
		return fmt.Errorf("%w: alert_percent is 50 to 99", ErrInvalidPolicy)
	}
	seen := map[Kind]bool{}
	for _, k := range p.AutoClean {
		if !k.Removable() || k == OldPanelReleases && !panel || seen[k] {
			return fmt.Errorf("%w: auto_clean holds untagged, old_builds, unused_pulled and, on the panel's server, old_panel_releases, each once", ErrInvalidPolicy)
		}
		seen[k] = true
	}
	return nil
}

// Class is one class of image on a host.
type Class struct {
	Kind      Kind     `json:"kind"`
	Removable bool     `json:"removable"`
	Automatic bool     `json:"automatic"`
	Count     int      `json:"count"`
	Bytes     int64    `json:"bytes"`
	Examples  []string `json:"examples"`
}

// VolumeKind is why a volume is offered for deletion.
type VolumeKind string

const (
	DeletedService VolumeKind = "deleted_service"
	ComposeOrphan  VolumeKind = "compose_orphan"
	Anonymous      VolumeKind = "anonymous"
)

// OrphanVolume is a volume no service owns.
type OrphanVolume struct {
	Name        string     `json:"name"`
	Bytes       int64      `json:"bytes"`
	CreatedAt   *time.Time `json:"created_at"`
	Kind        VolumeKind `json:"kind"`
	ServiceName string     `json:"service_name"`
}

// BuildCache is the build cache and its limit.
type BuildCache struct {
	Bytes            int64 `json:"bytes"`
	ReclaimableBytes int64 `json:"reclaimable_bytes"`
	OverLimitBytes   int64 `json:"over_limit_bytes"`
	LimitBytes       int64 `json:"limit_bytes"`
}

// VolumesInUse counts the volumes a service owns or a container mounts.
type VolumesInUse struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// RollbackKept is one service's kept rollback images on a host.
type RollbackKept struct {
	ServiceID   string `json:"service_id"`
	ServiceName string `json:"service_name"`
	Kept        int    `json:"kept"`
	Images      int    `json:"images"`
	Bytes       int64  `json:"bytes"`
}

// OtherWorkspaces is what the services of the host's other workspaces keep,
// with no names: the host's owner may not see them.
type OtherWorkspaces struct {
	Services       int   `json:"services"`
	RollbackImages int   `json:"rollback_images"`
	RollbackBytes  int64 `json:"rollback_bytes"`
}

// Report is what fills a host's disk.
type Report struct {
	ReadAt          time.Time       `json:"read_at"`
	SizeBytes       int64           `json:"size_bytes"`
	UsedBytes       int64           `json:"used_bytes"`
	FreeBytes       int64           `json:"free_bytes"`
	Classes         []Class         `json:"classes"`
	BuildCache      BuildCache      `json:"build_cache"`
	Volumes         []OrphanVolume  `json:"volumes"`
	VolumesInUse    VolumesInUse    `json:"volumes_in_use"`
	ContainersBytes int64           `json:"containers_bytes"`
	OtherBytes      int64           `json:"other_bytes"`
	RollbackKept    []RollbackKept  `json:"rollback_kept"`
	OtherWorkspaces OtherWorkspaces `json:"other_workspaces"`
	Policy          PolicyView      `json:"policy"`
	Clean           *Clean          `json:"clean"`
	LastClean       *Clean          `json:"last_clean"`
}

// Outcome is how a clean went.
type Outcome string

const (
	Running   Outcome = "running"
	Succeeded Outcome = "succeeded"
	Failed    Outcome = "failed"
)

// Clean is one clean of a host, in progress or ended.
type Clean struct {
	ID            string     `json:"id"`
	Kinds         []Kind     `json:"kinds"`
	BuildCacheAll bool       `json:"build_cache_all"`
	Automatic     bool       `json:"automatic"`
	Outcome       Outcome    `json:"outcome"`
	Step          string     `json:"step"`
	FreedBytes    int64      `json:"freed_bytes"`
	Removed       int        `json:"removed"`
	Skipped       int        `json:"skipped"`
	StartedAt     time.Time  `json:"started_at"`
	StartedBy     string     `json:"started_by"`
	FinishedAt    *time.Time `json:"finished_at"`
	Error         string     `json:"error"`

	actorID string // who started it; "" for Toft
	// after: what the clean saw once done, for the disk alert's detail.
	after struct {
		size, used, free, left int64
		leftKinds              []Kind
	}
}

var (
	// ErrCleanRunning: a clean of the host is under way.
	ErrCleanRunning = errors.New("a clean of this server is running: wait for it to finish")
	// ErrDeployRunning: a deploy may be about to start an image the clean
	// would remove.
	ErrDeployRunning = errors.New("a deploy is running here: free space once it's done")
	// ErrUnreachable: the host's Docker can't be reached.
	ErrUnreachable = errors.New("the server's Docker can't be reached")
	// ErrInUse: Docker refused, because a container uses the image or
	// volume (or another image is built on the image).
	ErrInUse = errors.New("in use")
	// ErrVolumeInUse: a container mounts the volume.
	ErrVolumeInUse = errors.New("a container mounts this volume")
	// ErrVolumeNotRemovable: the volume isn't one the advisor offers.
	ErrVolumeNotRemovable = errors.New("this volume isn't one Toft offers to delete: a service owns it, or it isn't listed")
	// ErrInvalidPolicy: a policy value out of range.
	ErrInvalidPolicy = errors.New("invalid disk policy")
	// ErrNothingToClean: no kind and no build cache asked for.
	ErrNothingToClean = errors.New("choose at least one kind of image, or the build cache")
	// ErrKindNotHere: a kind this host doesn't have (old panel releases on a server).
	ErrKindNotHere = errors.New("old_panel_releases can be cleaned only on the panel's server")
	// ErrNotFound: no such server in the workspace.
	ErrNotFound = errors.New("server not found")
	// ErrForbidden: the caller may not do this.
	ErrForbidden = errors.New("only the workspace's owners can do this")
)
