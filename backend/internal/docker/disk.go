package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
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
	"github.com/docker/docker/pkg/stdcopy"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/builder"
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
	log   *zap.Logger
	// buildctl runs buildctl in a container; nil is execBuildctl on dc.
	buildctl func(ctx context.Context, container string, args ...string) (out string, ran bool, err error)
}

// DiskHost opens srv's Docker for the disk advisor; nil is the panel's own.
func (f *Factory) DiskHost(ctx context.Context, srv *server.Server) (disk.Host, error) {
	if srv == nil || srv.ID == "" {
		return &diskHost{dc: f.local.dockerClient, fs: localFilesystem, close: func() {}, log: f.log}, nil
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
	}, close: func() { _ = dc.Close() }, log: f.log}, nil
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
	if err := h.addBuilders(ctx, &u); err != nil {
		return disk.Usage{}, err
	}
	if u.SizeBytes, u.UsedBytes, u.FreeBytes, err = h.fs(ctx); err != nil {
		return disk.Usage{}, err
	}
	return u, nil
}

// builders are Toft's builders (D-170). A builder's cache is in its own
// BuildKit, where Docker's build-cache API doesn't reach: buildctl in the
// builder's container measures and prunes it. The untrusted one is first,
// so a prune to a limit leaves it its cache before the trusted one's.
var builders = []builder.Spec{builder.Untrusted, builder.Trusted}

// cacheRecord is a record of buildctl du or prune (--format '{{json .}}').
type cacheRecord struct {
	Size   int64 `json:"size"`
	InUse  bool  `json:"inUse"`
	Shared bool  `json:"shared"`
}

// cacheRecords reads buildctl's JSON: du prints one array (null when the
// cache is empty), prune a record a line.
func cacheRecords(out string) ([]cacheRecord, error) {
	var all []cacheRecord
	dec := json.NewDecoder(strings.NewReader(out))
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("buildctl's answer: %w", err)
		}
		recs := []cacheRecord{{}}
		target := any(&recs[0])
		if bytes.HasPrefix(raw, []byte("[")) || string(raw) == "null" {
			target = &recs
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return nil, fmt.Errorf("buildctl's answer: %w", err)
		}
		all = append(all, recs...)
	}
	return all, nil
}

// builderCache is what a builder's cache holds and what of it nothing
// uses; ran is false where the builder isn't running.
func (h *diskHost) builderCache(ctx context.Context, b builder.Spec) (size, reclaimable int64, ran bool, err error) {
	out, ran, err := h.runBuildctl(ctx, b, "du", "--format", "{{json .}}")
	if err != nil || !ran {
		return 0, 0, ran, err
	}
	recs, err := cacheRecords(out)
	for _, r := range recs {
		size += r.Size
		if !r.Shared && !r.InUse {
			reclaimable += r.Size
		}
	}
	return size, reclaimable, true, err
}

// addBuilders counts each builder's cache as build cache and takes its
// state volume out of the volumes: it is Toft's, not an app's data. A
// builder that isn't running, or whose buildctl fails, is measured by its
// volume, none of it reclaimable until it answers.
func (h *diskHost) addBuilders(ctx context.Context, u *disk.Usage) error {
	for _, b := range builders {
		state := b.Container() + "_state"
		at := slices.IndexFunc(u.Volumes, func(v disk.Volume) bool { return v.Name == state })
		size, reclaimable, ran, err := h.builderCache(ctx, b)
		if errors.Is(err, disk.ErrUnreachable) {
			return fmt.Errorf("the cache of builder %s: %w", b.Name, err)
		}
		if ran = ran && err == nil; !ran && at >= 0 { // a builder that doesn't answer is measured as one that isn't running
			size = max(u.Volumes[at].Bytes, 0)
		}
		u.BuildCacheBytes += size
		u.BuildCacheReclaimable += reclaimable
		if at >= 0 {
			u.Volumes = slices.Delete(u.Volumes, at, at+1)
		}
	}
	return nil
}

// pruneBuilders trims the builders' caches to maxUsed bytes between them,
// or with all removes every unused record. It returns what it freed and
// what of maxUsed is left for Docker's own cache. A builder whose prune
// fails is logged and left as it is: the other builder's cache and Docker's
// own are still trimmed. Only a server that stopped answering is an error.
func (h *diskHost) pruneBuilders(ctx context.Context, maxUsed int64, all bool) (freed, left int64, err error) {
	left = maxUsed
	for _, b := range builders {
		n, size, err := h.pruneBuilder(ctx, b, left, all)
		freed += n
		if errors.Is(err, disk.ErrUnreachable) {
			return freed, left, fmt.Errorf("prune builder %s: %w", b.Name, err)
		}
		if err != nil {
			h.log.Warn("disk: a builder's cache was not pruned", zap.String("builder", b.Name), zap.Error(err))
			continue
		}
		left = max(left-size, 0)
	}
	return freed, left, nil
}

// pruneBuilder prunes one builder's cache and returns what it freed and,
// unless all, what the cache holds afterwards.
func (h *diskHost) pruneBuilder(ctx context.Context, b builder.Spec, keep int64, all bool) (freed, size int64, err error) {
	args := []string{"prune", "--format", "{{json .}}", "--all"}
	if !all {
		// In MB; 0 keeps nothing that isn't in use.
		args = []string{"prune", "--format", "{{json .}}", "--keep-storage", strconv.FormatInt(keep/1_000_000, 10)}
	}
	out, ran, err := h.runBuildctl(ctx, b, args...)
	if err != nil || !ran {
		return 0, 0, err
	}
	recs, err := cacheRecords(out)
	if err != nil {
		return 0, 0, err
	}
	for _, r := range recs {
		freed += r.Size
	}
	if !all {
		if size, _, _, err = h.builderCache(ctx, b); err != nil {
			return freed, 0, fmt.Errorf("the cache after the prune: %w", err)
		}
	}
	return freed, size, nil
}

func (h *diskHost) runBuildctl(ctx context.Context, b builder.Spec, args ...string) (string, bool, error) {
	if h.buildctl != nil {
		return h.buildctl(ctx, b.Container(), args...)
	}
	return execBuildctl(ctx, h.dc, b, args...)
}

// isBuilder is ensure.sh's test of a builder's container (fits): on the
// builder's own network, with the mark ensure.sh gives it. The name alone
// says nothing: a compose file written before container names starting
// buildx_buildkit_ were refused may hold one.
func isBuilder(c container.InspectResponse, b builder.Spec) bool {
	return c.HostConfig != nil && string(c.HostConfig.NetworkMode) == b.Name && c.Config != nil &&
		slices.ContainsFunc(c.Config.Env, func(e string) bool { return strings.HasPrefix(e, "TOFT_BUILDER_SPEC=") })
}

// execBuildctl runs buildctl in b's container and returns what it printed.
// ran is false, with no error, where the container isn't there, isn't
// running or isn't the builder's (isBuilder). The exec's stream is closed
// when ctx ends, so a buildctl that never exits holds nothing.
func execBuildctl(ctx context.Context, dc *client.Client, b builder.Spec, args ...string) (string, bool, error) {
	c, err := dc.ContainerInspect(ctx, b.Container())
	switch {
	case errdefs.IsNotFound(err):
		return "", false, nil
	case err != nil:
		return "", false, reach(fmt.Errorf("docker inspect: %w", err))
	case !isBuilder(c, b):
		return "", false, nil
	}
	// By ID: the container that passed the test, not whatever has its name by now.
	ex, err := dc.ContainerExecCreate(ctx, c.ID, container.ExecOptions{Cmd: append([]string{"buildctl"}, args...), AttachStdout: true, AttachStderr: true})
	switch {
	case errdefs.IsNotFound(err), errdefs.IsConflict(err):
		return "", false, nil
	case err != nil:
		return "", false, reach(fmt.Errorf("docker exec: %w", err))
	}
	resp, err := dc.ContainerExecAttach(ctx, ex.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", false, reach(fmt.Errorf("docker exec: %w", err))
	}
	stop := context.AfterFunc(ctx, resp.Close)
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, io.LimitReader(resp.Reader, 64<<20))
	stop()
	resp.Close()
	if ctx.Err() != nil {
		return "", false, fmt.Errorf("buildctl %s: %w", args[0], ctx.Err())
	}
	if err != nil {
		return "", false, reach(fmt.Errorf("docker exec: %w", err))
	}
	in, err := dc.ContainerExecInspect(ctx, ex.ID)
	if err != nil {
		return "", false, reach(fmt.Errorf("docker exec: %w", err))
	}
	if in.ExitCode != 0 {
		return "", false, fmt.Errorf("buildctl %s: exit %d: %s", args[0], in.ExitCode, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), true, nil
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

// PruneBuildCache prunes the builders' caches, then Docker's own (what was
// built before Toft had builders, and by a release that had none), which
// gets what the builders left of maxUsed.
func (h *diskHost) PruneBuildCache(ctx context.Context, maxUsed int64, all bool) (int64, error) {
	freed, maxUsed, err := h.pruneBuilders(ctx, maxUsed, all)
	if err != nil {
		return freed, err
	}
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
		return freed, reach(fmt.Errorf("docker builder prune: %w", err))
	}
	return freed + int64(rep.SpaceReclaimed), nil
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
