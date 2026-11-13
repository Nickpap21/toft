package docker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/versions"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"

	"github.com/Nickpap21/toft/backend/internal/disk"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/pkg/hostproc"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
)

// diskHost is a host's Docker API for the disk advisor (D-135): the local
// socket on the panel's server, the SSH-tunnelled socket elsewhere (agent
// servers carry the same SSH). Only the filesystem's size is read with a
// command; everything that deletes is an API call, never forced.
type diskHost struct {
	dc    *client.Client
	fs    func(ctx context.Context) (int64, int64, int64, error)
	close func()
}

// DiskHost opens srv's Docker for the disk advisor; nil is the panel's own.
func (f *Factory) DiskHost(ctx context.Context, srv *server.Server) (disk.Host, error) {
	if srv == nil || srv.ID == "" {
		return &diskHost{dc: f.local.dockerClient, fs: localFilesystem, close: func() {}}, nil
	}
	if f.ssh == nil {
		return nil, errors.New("remote servers are not configured: no SSH provider")
	}
	h := f.ssh.SSH(srv)
	cctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if _, err := h.Ensure(cctx, sshpool.EnsureOpts{}); err != nil {
		return nil, fmt.Errorf("SSH connect to server %q: %w", srv.Name, err)
	}
	dc, err := h.DockerClient()
	if err != nil {
		return nil, fmt.Errorf("SSH Docker client: %w", err)
	}
	return &diskHost{dc: dc, fs: func(ctx context.Context) (int64, int64, int64, error) {
		out, err := h.Exec(ctx, dockerRootDF)
		if err != nil {
			return 0, 0, 0, errors.Join(disk.ErrUnreachable, fmt.Errorf("remote df: %w", err))
		}
		return parseDFBytes(out)
	}, close: func() { _ = dc.Close() }}, nil
}

// dockerRootDF sizes the filesystem holding Docker's data, in bytes.
const dockerRootDF = `df -P -B1 /var/lib/docker 2>/dev/null || df -P -B1 /`

// parseDFBytes reads `df -P -B1`: the first line after the header, whose
// second column is the size, third the use and fourth what is available.
func parseDFBytes(out string) (int64, int64, int64, error) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		size, err1 := strconv.ParseInt(f[1], 10, 64)
		used, err2 := strconv.ParseInt(f[2], 10, 64)
		free, err3 := strconv.ParseInt(f[3], 10, 64)
		if err1 == nil && err2 == nil && err3 == nil {
			return size, used, free, nil
		}
	}
	return 0, 0, 0, fmt.Errorf("df: no filesystem in %q", out)
}

// localFilesystem: / inside the backend container is Docker's data root,
// which backs its overlay (as LocalOrchestrator.DiskUsage reads).
func localFilesystem(context.Context) (int64, int64, int64, error) {
	size, used, free, err := hostproc.Disk("/")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("statfs /: %w", err)
	}
	return size, used, free, nil
}

func (h *diskHost) Close() { h.close() }

func (h *diskHost) Filesystem(ctx context.Context) (int64, int64, int64, error) { return h.fs(ctx) }

func (h *diskHost) Usage(ctx context.Context) (disk.Usage, error) {
	du, err := h.dc.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return disk.Usage{}, fmt.Errorf("docker system df: %w", err)
	}
	u := disk.Usage{ImagesBytes: du.LayersSize, Images: make([]disk.Image, 0, len(du.Images))}
	for _, im := range du.Images {
		if im == nil {
			continue
		}
		u.Images = append(u.Images, disk.Image{ID: im.ID, Tags: im.RepoTags, Digests: im.RepoDigests, Size: im.Size, SharedSize: im.SharedSize, Containers: im.Containers})
	}
	for _, v := range du.Volumes {
		if v == nil {
			continue
		}
		dv := disk.Volume{Name: v.Name, Labels: v.Labels, Bytes: -1, RefCount: -1}
		if v.UsageData != nil {
			dv.Bytes, dv.RefCount = v.UsageData.Size, v.UsageData.RefCount
		}
		if t, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
			dv.CreatedAt = &t
		}
		u.Volumes = append(u.Volumes, dv)
	}
	for _, b := range du.BuildCache {
		if b == nil {
			continue
		}
		u.BuildCacheBytes += b.Size
		if !b.Shared && !b.InUse {
			u.BuildCacheReclaimable += b.Size
		}
	}
	for _, c := range du.Containers {
		if c != nil {
			u.ContainersBytes += c.SizeRw
		}
	}
	if u.SizeBytes, u.UsedBytes, u.FreeBytes, err = h.fs(ctx); err != nil {
		return disk.Usage{}, err
	}
	return u, nil
}

func (h *diskHost) RemoveImage(ctx context.Context, ref string) error {
	_, err := h.dc.ImageRemove(ctx, ref, dockerimage.RemoveOptions{PruneChildren: true})
	switch {
	case err == nil, errdefs.IsNotFound(err):
		return nil
	case errdefs.IsConflict(err):
		return errors.Join(disk.ErrInUse, err)
	}
	return reach(err)
}

// ImageUsed reports whether any container, running or not, uses the image.
func (h *diskHost) ImageUsed(ctx context.Context, id string) (bool, error) {
	cs, err := h.dc.ContainerList(ctx, container.ListOptions{All: true, Limit: 1, Filters: filters.NewArgs(filters.Arg("ancestor", id))})
	if err != nil {
		return false, reach(fmt.Errorf("docker ps: %w", err))
	}
	return len(cs) > 0, nil
}

// reach marks an error that never reached Docker (the connection to the
// server failed), so a clean says the server stopped answering rather than
// the transport's own words.
func reach(err error) error {
	if client.IsErrConnectionFailed(err) {
		return errors.Join(disk.ErrUnreachable, err)
	}
	return err
}

func (h *diskHost) PruneBuildCache(ctx context.Context, maxUsed int64, all bool) (int64, error) {
	opts := build.CachePruneOptions{All: all}
	if !all {
		// max-used-space is API 1.48 (Engine 28); keep-storage, the same
		// limit, before it.
		if versions.LessThan(h.dc.ClientVersion(), "1.48") {
			opts.KeepStorage = maxUsed
		} else {
			opts.MaxUsedSpace = maxUsed
		}
	}
	rep, err := h.dc.BuildCachePrune(ctx, opts)
	if err != nil {
		return 0, reach(fmt.Errorf("docker builder prune: %w", err))
	}
	return int64(rep.SpaceReclaimed), nil
}

func (h *diskHost) InspectVolume(ctx context.Context, name string) (disk.Volume, error) {
	v, err := h.dc.VolumeInspect(ctx, name)
	if err != nil {
		return disk.Volume{}, reach(fmt.Errorf("docker volume inspect: %w", err))
	}
	// Inspect doesn't count mounts; Docker refuses to remove a mounted one.
	return disk.Volume{Name: v.Name, Labels: v.Labels, Bytes: -1, RefCount: 0}, nil
}

func (h *diskHost) RemoveVolume(ctx context.Context, name string) error {
	err := h.dc.VolumeRemove(ctx, name, false)
	switch {
	case err == nil, errdefs.IsNotFound(err):
		return nil
	case errdefs.IsConflict(err):
		return disk.ErrVolumeInUse
	}
	return reach(fmt.Errorf("docker volume rm: %w", err))
}
