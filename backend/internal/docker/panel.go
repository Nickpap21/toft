package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// PanelContainer is one of the panel's own containers on the local host
// (D-68), as Docker reports it.
type PanelContainer struct {
	Name, Image string
	// State is Docker's (running, restarting, exited, …), "missing" when no
	// container has the name; Health is healthy, unhealthy, starting, or ""
	// without a health check.
	State, Health string
	StartedAt     string // State.StartedAt, RFC 3339
	Restarts      int
	// Stats of a running container; nil otherwise.
	CPUPercent       *float64
	MemoryBytes      *int64
	MemoryLimitBytes *int64
}

// PanelContainers inspects the named containers and, with stats, takes the
// running ones' CPU and memory. Stats wait about a second for a second CPU
// reading, so they run side by side.
func (o *LocalOrchestrator) PanelContainers(ctx context.Context, names []string, stats bool) ([]PanelContainer, error) {
	out := make([]PanelContainer, len(names))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := panelContainer(ctx, o.dockerClient, name, stats)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
			out[i] = c
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, firstErr
}

func panelContainer(ctx context.Context, dc *client.Client, name string, stats bool) (PanelContainer, error) {
	c := PanelContainer{Name: name, State: "missing"}
	ins, err := dc.ContainerInspect(ctx, name)
	if client.IsErrNotFound(err) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("inspect %s: %w", name, err)
	}
	c.Image = ins.Config.Image
	c.Restarts = ins.RestartCount
	if ins.State != nil {
		c.State = string(ins.State.Status)
		c.StartedAt = ins.State.StartedAt
		if ins.State.Health != nil {
			c.Health = string(ins.State.Health.Status)
		}
	}
	if c.State != "running" || !stats {
		return c, nil
	}
	st, err := containerStats(ctx, dc, ins.ID)
	if err != nil {
		return c, nil // stopped since the inspect: the state says enough
	}
	const mib = 1 << 20
	cpu, mem := st.CPUPercent, int64(st.MemoryMB*mib)
	c.CPUPercent, c.MemoryBytes = &cpu, &mem
	if st.MemoryLimitMB > 0 {
		limit := int64(st.MemoryLimitMB * mib)
		c.MemoryLimitBytes = &limit
	}
	return c, nil
}

// HostFacts is what the local Docker daemon says about its host.
type HostFacts struct {
	CPUs                      int
	MemoryBytes               int64
	OS, Kernel, DockerVersion string
}

// HostFacts asks the local daemon.
func (o *LocalOrchestrator) HostFacts(ctx context.Context) (HostFacts, error) {
	info, err := o.dockerClient.Info(ctx)
	if err != nil {
		return HostFacts{}, fmt.Errorf("docker info: %w", err)
	}
	return HostFacts{CPUs: info.NCPU, MemoryBytes: info.MemTotal, OS: info.OperatingSystem, Kernel: info.KernelVersion, DockerVersion: info.ServerVersion}, nil
}

// DiskUse is what Docker keeps on the local host's disk, in bytes.
type DiskUse struct {
	Images, ImagesUnused                                        int
	ImagesBytes, VolumesBytes, BuildCacheBytes, ContainersBytes int64
}

// DiskUse is docker system df. It sizes every volume, so it can take
// seconds on a busy host; callers cache it.
func (o *LocalOrchestrator) DiskUse(ctx context.Context) (DiskUse, error) {
	du, err := o.dockerClient.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return DiskUse{}, fmt.Errorf("docker system df: %w", err)
	}
	d := DiskUse{Images: len(du.Images), ImagesBytes: du.LayersSize}
	for _, im := range du.Images {
		if im != nil && im.Containers == 0 {
			d.ImagesUnused++
		}
	}
	for _, v := range du.Volumes {
		if v != nil && v.UsageData != nil && v.UsageData.Size > 0 {
			d.VolumesBytes += v.UsageData.Size
		}
	}
	for _, b := range du.BuildCache {
		if b != nil && !b.Shared {
			d.BuildCacheBytes += b.Size
		}
	}
	for _, c := range du.Containers {
		if c != nil {
			d.ContainersBytes += c.SizeRw
		}
	}
	return d, nil
}

// selfImage is the image the backend runs from, found through its own
// container: its hostname is the container's ID, and compose names it
// paas-backend. The helper containers below use it, so nothing is pulled
// and the tools (nsenter, systemd-run) are the release's own.
func (o *LocalOrchestrator) selfImage(ctx context.Context) (string, error) {
	var names []string
	if h, err := os.Hostname(); err == nil {
		names = append(names, h)
	}
	for _, n := range append(names, "paas-backend") {
		if ins, err := o.dockerClient.ContainerInspect(ctx, n); err == nil {
			return ins.Image, nil
		}
	}
	return "", errors.New("the backend is not running in a container, so it can't reach its host")
}

// panelNsenter enters the namespaces of the host's init as root, without
// the supplementary groups root has in the image (27 is sudo on Ubuntu).
var panelNsenter = []string{"nsenter", "-t", "1", "-m", "-u", "-i", "-n", "-p", "-S", "0", "-G", "0", "--"}

// noHealthcheck keeps the backend image's health check off its helpers.
var noHealthcheck = &container.HealthConfig{Test: []string{"NONE"}}

// The helpers' labels; the value is the helperOwner that started them. A
// shell also carries the operator who opened it, for its audit row.
const (
	panelShellLabel  = "toft.panel-shell"
	panelHelperLabel = "toft.panel-helper"
	panelActorLabel  = "toft.panel-shell.actor"
)

// panelLoginShell starts the host's own login shell, bash when it has one.
const panelLoginShell = `cd /root 2>/dev/null || cd /; if command -v bash >/dev/null 2>&1; then exec bash -l; fi; exec sh -l`

// PanelShell is a root shell on the panel's own host (D-68): a short-lived
// container from the backend's image, privileged and in the host's PID
// namespace, enters the namespaces of the host's init with nsenter. No SSH
// key, sshd or firewall rule is involved. It is removed when it ends.
type PanelShell struct {
	dc   *client.Client
	id   string
	conn types.HijackedResponse
}

// OpenPanelShell starts the shell with a cols by rows terminal for actorID.
func (o *LocalOrchestrator) OpenPanelShell(ctx context.Context, cols, rows int, actorID string) (*PanelShell, error) {
	img, err := o.selfImage(ctx)
	if err != nil {
		return nil, err
	}
	cfg := &container.Config{
		Image:        img,
		Entrypoint:   panelNsenter,
		Cmd:          []string{"/bin/sh", "-c", panelLoginShell},
		Env:          []string{"TERM=xterm-256color", "HOME=/root"},
		Tty:          true,
		OpenStdin:    true,
		StdinOnce:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Labels:       map[string]string{panelShellLabel: o.helperOwner, panelActorLabel: actorID},
		Healthcheck:  noHealthcheck,
	}
	host := &container.HostConfig{
		Privileged:  true,
		PidMode:     "host",
		NetworkMode: "none",
		AutoRemove:  true,
		ConsoleSize: [2]uint{uint(rows), uint(cols)},
	}
	created, err := o.dockerClient.ContainerCreate(ctx, cfg, host, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("create the shell's container: %w", err)
	}
	sh := &PanelShell{dc: o.dockerClient, id: created.ID}
	sh.conn, err = o.dockerClient.ContainerAttach(ctx, created.ID, container.AttachOptions{Stream: true, Stdin: true, Stdout: true, Stderr: true})
	if err != nil {
		sh.remove()
		return nil, fmt.Errorf("attach to the shell: %w", err)
	}
	if err := o.dockerClient.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		sh.conn.Close()
		sh.remove()
		return nil, fmt.Errorf("start the shell: %w", err)
	}
	return sh, nil
}

func (s *PanelShell) Read(p []byte) (int, error)  { return s.conn.Reader.Read(p) }
func (s *PanelShell) Write(p []byte) (int, error) { return s.conn.Conn.Write(p) }

// Resize sets the terminal's size.
func (s *PanelShell) Resize(cols, rows int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.dc.ContainerResize(ctx, s.id, container.ResizeOptions{Height: uint(rows), Width: uint(cols)})
}

// Detach lets go of the shell and leaves its container running, for
// SweepPanelHelpers to remove once it is idle.
func (s *PanelShell) Detach() error {
	s.conn.Close()
	return nil
}

// Close ends the shell and removes its container.
func (s *PanelShell) Close() error {
	s.conn.Close()
	s.remove()
	return nil
}

func (s *PanelShell) remove() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.dc.ContainerRemove(ctx, s.id, container.RemoveOptions{Force: true})
}

// SweptHelper is a helper SweepPanelHelpers removed: a shell, with who
// opened it ("" before v0.2.3) and when, or a finished command.
type SweptHelper struct {
	Shell   bool
	ActorID string
	Opened  time.Time
}

// SweepPanelHelpers removes the helpers an earlier backend process left on
// the panel's host. A restart ends the connection to an open shell but not
// the shell, which would stay open as root with nobody attached. A shell is
// removed only once it is back at its prompt (nsenter and the shell, nothing
// else), so what was started from it runs to its end: an upgrade run from
// the shell restarts the backend half way.
func (o *LocalOrchestrator) SweepPanelHelpers(ctx context.Context) ([]SweptHelper, error) {
	var cs []container.Summary
	for _, label := range []string{panelShellLabel, panelHelperLabel} {
		// One list each: Docker matches every label filter it is given.
		got, err := o.dockerClient.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", label))})
		if err != nil {
			return nil, fmt.Errorf("list the panel's helpers: %w", err)
		}
		cs = append(cs, got...)
	}
	swept := make([]SweptHelper, 0, len(cs))
	for _, c := range cs {
		owner, shell := c.Labels[panelShellLabel]
		if !shell {
			owner = c.Labels[panelHelperLabel]
		}
		if owner == o.helperOwner {
			continue
		}
		if c.State == "running" {
			if !shell {
				continue // a command, which ends by itself
			}
			top, err := o.dockerClient.ContainerTop(ctx, c.ID, nil)
			if err != nil || len(top.Processes) > 2 {
				continue
			}
		}
		if err := o.dockerClient.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err == nil {
			swept = append(swept, SweptHelper{Shell: shell, ActorID: c.Labels[panelActorLabel], Opened: time.Unix(c.Created, 0)})
		}
	}
	return swept, nil
}

// RunOnHost runs argv on the panel's host, in its namespaces, through a
// short-lived helper container as OpenPanelShell's (D-68), and returns what
// it printed. argv is fixed by the caller, never a shell line.
func (o *LocalOrchestrator) RunOnHost(ctx context.Context, argv []string) (string, error) {
	return o.RunOnHostStream(ctx, argv, nil, nil)
}

// RunOnHostStream is RunOnHost with stdin, unless it is nil, and each line
// printed handed to logFn, unless it is nil, as it comes. It returns the
// last MiB of what was printed.
func (o *LocalOrchestrator) RunOnHostStream(ctx context.Context, argv []string, stdin io.Reader, logFn func(string)) (string, error) {
	img, err := o.selfImage(ctx)
	if err != nil {
		return "", err
	}
	cfg := &container.Config{
		Image:        img,
		Entrypoint:   panelNsenter,
		Cmd:          argv,
		AttachStdout: true,
		AttachStderr: true,
		Labels:       map[string]string{panelHelperLabel: o.helperOwner},
		Healthcheck:  noHealthcheck,
	}
	if stdin != nil {
		cfg.OpenStdin, cfg.StdinOnce, cfg.AttachStdin = true, true, true
	}
	host := &container.HostConfig{Privileged: true, PidMode: "host", NetworkMode: "none"}
	created, err := o.dockerClient.ContainerCreate(ctx, cfg, host, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("create the helper: %w", err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = o.dockerClient.ContainerRemove(rctx, created.ID, container.RemoveOptions{Force: true})
	}()
	// Attached before it starts, so nothing it prints or reads is missed.
	att, err := o.dockerClient.ContainerAttach(ctx, created.ID, container.AttachOptions{Stream: true, Stdin: stdin != nil, Stdout: true, Stderr: true})
	if err != nil {
		return "", fmt.Errorf("attach to the helper: %w", err)
	}
	defer att.Close()
	wait, errc := o.dockerClient.ContainerWait(ctx, created.ID, container.WaitConditionNextExit)
	if err := o.dockerClient.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", fmt.Errorf("start the helper: %w", err)
	}
	if stdin != nil {
		go func() {
			_, _ = io.Copy(att.Conn, stdin)
			_ = att.CloseWrite()
		}()
	}
	out := &helperOutput{logFn: logFn}
	copied := make(chan struct{})
	go func() {
		_, _ = stdcopy.StdCopy(out, out, att.Reader)
		close(copied)
	}()
	var code int64
	select {
	case res := <-wait:
		code = res.StatusCode
	case err := <-errc:
		return "", fmt.Errorf("wait for the helper: %w", err)
	case <-ctx.Done():
		return "", ctx.Err()
	}
	// Docker ends the stream once the helper exits, after what it still holds.
	select {
	case <-copied:
	case <-time.After(10 * time.Second):
		att.Close()
		<-copied
	}
	out.flush()
	text := strings.TrimSpace(string(out.buf))
	if code != 0 {
		return text, fmt.Errorf("%s exited %d: %s", argv[0], code, lastLine(text))
	}
	return text, nil
}

// helperOutputKeep is how much of a helper's output RunOnHostStream returns:
// the end, where an error is.
const helperOutputKeep = 1 << 20

// helperOutput keeps the last helperOutputKeep bytes a helper printed and hands
// each line to logFn, when set.
type helperOutput struct {
	buf, partial []byte
	logFn        func(string)
}

func (w *helperOutput) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > helperOutputKeep {
		w.buf = w.buf[len(w.buf)-helperOutputKeep:]
	}
	if w.logFn != nil {
		w.partial = append(w.partial, p...)
		for {
			i := bytes.IndexByte(w.partial, '\n')
			if i < 0 {
				break
			}
			w.logFn(strings.TrimRight(string(w.partial[:i]), "\r"))
			w.partial = w.partial[i+1:]
		}
	}
	return len(p), nil
}

// flush hands logFn the last line when it didn't end in a newline.
func (w *helperOutput) flush() {
	if w.logFn != nil && len(w.partial) > 0 {
		w.logFn(strings.TrimRight(string(w.partial), "\r"))
		w.partial = nil
	}
}

func lastLine(s string) string {
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
