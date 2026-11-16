package docker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/domain"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/gitcred"
	"github.com/Nickpap21/toft/backend/pkg/lines"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/moby/go-archive"
	"go.uber.org/zap"
)

// LocalOrchestrator manages Docker containers using the local Docker socket.
// It implements deployment.Orchestrator.
type LocalOrchestrator struct {
	dockerClient *client.Client
	log          *zap.Logger
	net          rollout.NetConfig
	// dataDir holds files apps bind-mount (compose releases). It is mounted
	// into the backend container at the same path as on the host, because
	// Docker reads bind-mount sources on the host.
	dataDir string
	// helperOwner labels the helper containers this process starts on the
	// panel's host, so SweepPanelHelpers tells an earlier process's apart.
	helperOwner string
	// latencyURL is the panel host's Traefik's metrics page, its metrics
	// entrypoint (docker-compose.yml) on the control plane's network (D-156);
	// "" reads none.
	latencyURL string
	// latencyCause is why the last read of latencyURL failed, "" when it
	// did not: a failure is logged when its cause changes, not every minute.
	latencyCause atomic.Value
}

// NewLocalOrchestrator creates a LocalOrchestrator connected to the local
// Docker socket. net names the proxy and the control plane's network;
// dataDir is TOFT_DATA_DIR (see LocalOrchestrator.dataDir).
func NewLocalOrchestrator(log *zap.Logger, net rollout.NetConfig, dataDir string) (*LocalOrchestrator, error) {
	dc, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to local Docker daemon: %w", err)
	}
	net = net.WithDefaults()
	return &LocalOrchestrator{dockerClient: dc, log: log, net: net, dataDir: dataDir, helperOwner: rand.Text(),
		latencyURL: "http://" + net.ProxyContainer + ":9100/metrics"}, nil
}

// BuildFromGit clones repoURL@branch locally, then builds a Docker image from the result.
// The repo root is always used as the Docker build context so Dockerfiles can reference
// root-level files (e.g. monorepo package.json). When baseDir is non-empty the Dockerfile
// is resolved as baseDir/Dockerfile relative to the repo root.
func (o *LocalOrchestrator) BuildFromGit(ctx context.Context, repoURL, branch, baseDir, imageTag string, buildArgs map[string]*string, auth *deployment.RegistryAuth, logFn func(string)) (deployment.Commit, error) {
	if err := service.ValidateBaseDirectory(baseDir); err != nil {
		return deployment.Commit{}, err
	}
	logFn(fmt.Sprintf("[1/2] Cloning %s @ %s...", sanitizeRepoURL(repoURL), branch))

	repoRoot, cleanup, err := cloneRepository(ctx, repoURL, branch, "", logFn)
	if err != nil {
		return deployment.Commit{}, err
	}
	defer cleanup()
	commit := clonedCommit(branch, logFn, func() (string, error) { return gitHead(ctx, repoRoot) })
	// .git stays out of the build context: a fresh clone's differs every
	// time, so the layer cache would miss even on the same commit.
	if err := os.RemoveAll(filepath.Join(repoRoot, ".git")); err != nil {
		return commit, fmt.Errorf("remove .git from the build context: %w", err)
	}

	dockerfilePath := "Dockerfile"
	if baseDir != "" {
		dockerfilePath = filepath.Join(filepath.Clean(strings.TrimPrefix(baseDir, "/")), "Dockerfile")
	}

	logFn(fmt.Sprintf("[2/2] Building image %s...", imageTag))
	return commit, o.buildImage(ctx, repoRoot, dockerfilePath, imageTag, buildArgs, auth, logFn)
}

// BuildFromArchive builds imageTag from source, a commit's tree as a tar
// (D-77), unpacked into a temporary directory: the build context, with the
// Dockerfile at baseDir/Dockerfile; or nixpacks' in baseDir.
func (o *LocalOrchestrator) BuildFromArchive(ctx context.Context, source io.Reader, baseDir, imageTag string, nixpacks *deployment.NixpacksCommands, buildArgs map[string]*string, auth *deployment.RegistryAuth, logFn func(string)) error {
	if err := service.ValidateBaseDirectory(baseDir); err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "paas-build-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	if err := archive.Untar(source, tmpDir, &archive.TarOptions{NoLchown: true}); err != nil {
		return fmt.Errorf("unpack the source: %w", err)
	}
	if nixpacks == nil {
		dockerfilePath := "Dockerfile"
		if baseDir != "" {
			dockerfilePath = filepath.Join(filepath.Clean(strings.TrimPrefix(baseDir, "/")), "Dockerfile")
		}
		logFn(fmt.Sprintf("Building image %s...", imageTag))
		return localBuild(ctx, tmpDir, dockerfilePath, imageTag, buildArgs, auth, logFn)
	}
	buildDir, err := service.JoinBaseDirectory(tmpDir, baseDir)
	if err != nil {
		return err
	}
	if err := insideBuild(tmpDir, buildDir); err != nil {
		return err
	}
	logFn(fmt.Sprintf("Building %s with nixpacks...", imageTag))
	return localNixpacks(ctx, buildDir, imageTag, *nixpacks, buildArgs, auth, logFn)
}

// BuildFromGitNixpacks clones repoURL@branch locally, then builds via nixpacks.
// nixpacks must be installed on the host (https://nixpacks.com).
func (o *LocalOrchestrator) BuildFromGitNixpacks(ctx context.Context, repoURL, branch, baseDir, imageTag string, cmds deployment.NixpacksCommands, buildArgs map[string]*string, auth *deployment.RegistryAuth, logFn func(string)) (deployment.Commit, error) {
	if err := service.ValidateBaseDirectory(baseDir); err != nil {
		return deployment.Commit{}, err
	}
	logFn(fmt.Sprintf("[1/2] Cloning %s @ %s...", sanitizeRepoURL(repoURL), branch))

	buildDir, cleanup, err := cloneRepository(ctx, repoURL, branch, baseDir, logFn)
	if err != nil {
		return deployment.Commit{}, err
	}
	defer cleanup()
	commit := clonedCommit(branch, logFn, func() (string, error) { return gitHead(ctx, buildDir) })
	// As in BuildFromGit; with a baseDir, the clone's .git is outside buildDir already.
	if err := os.RemoveAll(filepath.Join(buildDir, ".git")); err != nil {
		return commit, fmt.Errorf("remove .git from the build context: %w", err)
	}

	logFn(fmt.Sprintf("[2/2] Building %s with nixpacks...", imageTag))
	return commit, localNixpacks(ctx, buildDir, imageTag, cmds, buildArgs, auth, logFn)
}

// Rollout runs a single-container deploy's rollout on the local Docker
// (rollout.Run).
func (o *LocalOrchestrator) Rollout(ctx context.Context, spec rollout.Spec, route deployment.Routing, hooks rollout.Hooks) rollout.Result {
	o.log.Info("rollout (local)", zap.String("service_id", spec.Slot.ServiceID), zap.String("image", spec.Slot.Image))
	spec.Slot.Labels = slotLabels(spec, route)
	return rollout.Run(ctx, rollout.DockerHost{Client: o.dockerClient, Net: o.net, Warn: o.warnf(spec.Slot.ServiceID)}, spec, hooks)
}

// warnf returns a function that logs a warning about serviceID.
func (o *LocalOrchestrator) warnf(serviceID string) func(string) {
	return func(msg string) { o.log.Warn(msg, zap.String("service_id", serviceID)) }
}

// Stop gracefully stops and removes both blue and green containers for the given service.
func (o *LocalOrchestrator) Stop(ctx context.Context, serviceID string) error {
	o.log.Info("stopping container (local)", zap.String("service_id", serviceID))
	return stopAllSlots(ctx, o.dockerClient, serviceID)
}

// StreamLogs streams live container logs line by line to logFn, each line
// led by Docker's RFC 3339 timestamp and a space.
// tailLines controls how many historical lines to include before live output.
// containerName overrides auto-resolution; pass "" to let the orchestrator find the active container.
func (o *LocalOrchestrator) StreamLogs(ctx context.Context, serviceID string, tailLines int, containerName string, logFn func(string)) error {
	if containerName == "" {
		containerName = resolveContainer(ctx, o.dockerClient, serviceID, "")
		if containerName == "" {
			return fmt.Errorf("no running container found for service %s", serviceID)
		}
	}
	reader, err := o.dockerClient.ContainerLogs(ctx, containerName, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true, // the service layer strips them unless asked for
		Tail:       fmt.Sprintf("%d", tailLines),
	})
	if err != nil {
		return fmt.Errorf("ContainerLogs: %w", err)
	}
	defer reader.Close()

	pr, pw := io.Pipe()
	go func() {
		_, copyErr := stdcopy.StdCopy(pw, pw, reader)
		pw.CloseWithError(copyErr)
	}()

	return lines.Read(pr, logFn)
}

// LogsSince reads a container's log from since to now, without following
// it, each line led by Docker's RFC 3339 timestamp and a space.
func (o *LocalOrchestrator) LogsSince(ctx context.Context, containerName string, since time.Time, logFn func(string)) error {
	reader, err := o.dockerClient.ContainerLogs(ctx, containerName, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Since:      since.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return fmt.Errorf("ContainerLogs: %w", err)
	}
	defer reader.Close()

	pr, pw := io.Pipe()
	go func() {
		_, copyErr := stdcopy.StdCopy(pw, pw, reader)
		pw.CloseWithError(copyErr)
	}()

	return lines.Read(pr, logFn)
}

// RunCompose writes the compose file to a new release directory of the
// service (newComposeRelease) and runs docker compose with the service's env vars as its process environment
// (for ${VAR} interpolation) and nothing of the backend's own environment but
// PATH, HOME, DOCKER_HOST and XDG_RUNTIME_DIR (localCompose). route, when not
// nil, adds the platform's routing override file (composeRouteOverride).
func (o *LocalOrchestrator) RunCompose(ctx context.Context, deploymentID string, serviceID, serviceName, composeContent string, envVars map[string]string, route *deployment.ComposeRoute, nets deployment.ComposeNetworks, limits map[string]deployment.ComposeLimit, auth *deployment.RegistryAuth, hooks rollout.Hooks) error {
	logFn := hooks.Log
	release, err := o.newComposeRelease(serviceID)
	if err != nil {
		return err
	}
	composeFile := filepath.Join(release, "docker-compose.yml")
	if err := os.WriteFile(composeFile, []byte(composeContent), 0600); err != nil {
		return fmt.Errorf("write compose file: %w", err)
	}
	return o.composeUp(ctx, serviceID, serviceName, release, release, composeFile, envVars, route, nets, limits, auth, logFn)
}

// RunComposeFromRepo copies the clone at repoDir into a new release
// directory of the service and runs docker compose from there, in the same
// clean environment as RunCompose. The clone itself is a temporary directory
// inside the backend container, where Docker cannot see its files.
func (o *LocalOrchestrator) RunComposeFromRepo(ctx context.Context, deploymentID string, serviceID, serviceName, repoDir, composeFilePath string, envVars map[string]string, route *deployment.ComposeRoute, nets deployment.ComposeNetworks, limits map[string]deployment.ComposeLimit, auth *deployment.RegistryAuth, hooks rollout.Hooks) error {
	logFn := hooks.Log
	release, err := o.newComposeRelease(serviceID)
	if err != nil {
		return err
	}
	if err := copyRepo(repoDir, release); err != nil {
		return fmt.Errorf("copy repo into %s: %w", release, err)
	}
	return o.composeUp(ctx, serviceID, serviceName, release, release, filepath.Join(release, composeFilePath), envVars, route, nets, limits, auth, logFn)
}

// localComposeDir is the directory holding a compose service's releases on
// the control-plane server.
func (o *LocalOrchestrator) localComposeDir(serviceID string) string {
	return filepath.Join(o.dataDir, "compose", serviceID)
}

// newComposeRelease creates an empty, private directory for this deploy's
// compose files, as the remote orchestrator does: it outlives the deploy, so
// relative bind mounts resolve while the stack runs, and a new path per
// deploy makes up recreate exactly the services that mount from it.
func (o *LocalOrchestrator) newComposeRelease(serviceID string) (string, error) {
	if o.dataDir == "" {
		return "", errors.New("compose release: no data directory configured")
	}
	dir := o.localComposeDir(serviceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create compose directory: %w", err)
	}
	release, err := os.MkdirTemp(dir, "r-")
	if err != nil {
		return "", fmt.Errorf("create compose release directory: %w", err)
	}
	return release, nil
}

// pruneComposeReleases removes the service's releases other than keep.
func (o *LocalOrchestrator) pruneComposeReleases(serviceID, keep string) {
	dir := o.localComposeDir(serviceID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if p := filepath.Join(dir, e.Name()); p != keep {
			if err := os.RemoveAll(p); err != nil {
				o.log.Warn("could not remove old compose release", zap.String("path", p), zap.Error(err))
			}
		}
	}
}

// copyRepo copies the clone at src into the existing directory dst, keeping
// modes and symlinks (the deploy policy has refused links that leave the
// repository). .git is left out: it can hold the clone URL with its token.
func copyRepo(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.Mkdir(target, info.Mode().Perm())
		case d.Type().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		default:
			return nil // a clone has no devices, sockets or pipes
		}
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// composeUp brings the service's stack up from composeFile, updating it in
// place, with projectDir as the project directory. release is the deploy's
// directory; after a successful up the service's older releases are removed
// (every container that mounted from one was recreated onto this one).
func (o *LocalOrchestrator) composeUp(ctx context.Context, serviceID, serviceName, release, projectDir, composeFile string, envVars map[string]string, route *deployment.ComposeRoute, nets deployment.ComposeNetworks, limits map[string]deployment.ComposeLimit, auth *deployment.RegistryAuth, logFn func(string)) error {
	projectName := rollout.ComposeProjectName(serviceID, serviceName)

	// The deploy has a Docker config of its own, holding its registry's
	// credentials or nothing, not a `docker login` on the host: a login
	// there is every other deploy's to pull with, and Toft's own would be
	// for as long as the deploy runs, its logout ending theirs.
	var raw []byte
	if auth != nil && auth.Password != "" {
		var err error
		if raw, err = rollout.RegistryConfig(*auth, auth.Server); err != nil {
			return err
		}
	}
	cfg, cleanup, err := dockerConfigDir(raw)
	if err != nil {
		return err
	}
	defer cleanup()
	dockerCfg := []string{"DOCKER_CONFIG=" + cfg}

	serviceEnv, dropped := composespec.ServiceEnv(envVars)
	logDroppedEnv(dropped, logFn)

	if !nets.Isolated {
		// Legacy: routed services join the control plane's network.
		if err := o.ensureNetwork(ctx); err != nil {
			logFn("warning: could not ensure " + o.net.LegacyNetwork + ": " + err.Error())
		}
	}

	overrides, err := composeOverrides(serviceID, o.net, route, nets, limits)
	if err != nil {
		return err
	}
	files := []string{"-f", composeFile}
	for _, ov := range overrides {
		overrideFile, cleanup, err := writeLocalOverride(ov)
		if err != nil {
			return err
		}
		defer cleanup()
		files = append(files, "-f", overrideFile)
	}

	// Take down a stack still running under the legacy project name
	// (paas-{id[:8]}), whose containers could hold this stack's
	// container_name entries. down works by project name, so the routing
	// override (labels and an external network) is not needed. The current
	// project is updated in place: up recreates only the containers whose
	// image or configuration changed, so a database in the stack keeps running.
	for _, proj := range rollout.ComposeProjectNames(serviceID, serviceName) {
		if proj == projectName {
			continue
		}
		down := localCompose(ctx, projectDir, serviceEnv,
			"--project-directory", projectDir, "-f", composeFile, "--project-name", proj,
			"down", "--remove-orphans")
		down.Run() //nolint:errcheck — not a failure if nothing to down
	}

	allowed := routeLabelKeys(serviceID, nets.Home, route)
	if route != nil {
		// Refuse an image that routes itself before up replaces anything:
		// pull the routed service's image and read its labels. A service
		// built during up is left to the check after it.
		base := append([]string{"--project-directory", projectDir}, files...)
		base = append(base, "--project-name", projectName)
		out, err := localCompose(ctx, projectDir, serviceEnv, append(base, "config", "--images", route.Service)...).Output()
		pull := func(image string) error {
			cmd := exec.CommandContext(ctx, "docker", "pull", "--quiet", image)
			cmd.Env = append(os.Environ(), dockerCfg...)
			return cmd.Run()
		}
		if image := singleImage(string(out)); err == nil && image != "" && pull(image) == nil {
			if keys, ok := routedImageViolations(ctx, o.dockerClient, image, allowed); ok && len(keys) > 0 {
				err := routedImageError(route.Service, image, keys)
				logFn("✗ " + err.Error())
				return err
			}
		}
	}

	logFn(fmt.Sprintf("Running docker compose up... (%d vars)", len(serviceEnv)))
	base := append([]string{"--project-directory", projectDir}, files...)
	base = append(base, "--project-name", projectName)
	run := func(args ...string) error {
		cmd := localCompose(ctx, projectDir, serviceEnv, append(slices.Clone(base), args...)...)
		cmd.Env = append(cmd.Env, dockerCfg...)
		return runStream(cmd, logFn)
	}
	// The stack's routes are checked on its created containers, which
	// carry their images' labels, before any starts, and again after up,
	// failed or not: a container up started before failing must not keep
	// routes of its own.
	check := func() error {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if err := rollout.CheckStackRoutes(cctx, o.dockerClient, projectName, routedService(route), allowed); err != nil {
			logFn("✗ " + err.Error())
			down := localCompose(context.WithoutCancel(ctx), projectDir, serviceEnv,
				"--project-directory", projectDir, "-f", composeFile, "--project-name", projectName, "down", "--remove-orphans")
			_ = down.Run()
			return err
		}
		return nil
	}
	// A failed create (a pull or a build) started nothing and recreated
	// nothing yet: the running stack is untouched, and checking its old
	// containers against the new routes would take it down.
	if err := run("up", "--no-start", "--pull", "always", "--build", "--remove-orphans"); err != nil {
		return fmt.Errorf("compose up: %w", err)
	}
	if err := check(); err != nil {
		return err
	}
	// Nothing pulled, built or recreated: what starts is what was checked.
	upErr := run("up", "-d", "--pull", "never", "--no-build", "--no-recreate", "--remove-orphans")
	if err := check(); err != nil {
		return err
	}
	if upErr != nil {
		return fmt.Errorf("compose up: %w", upErr)
	}
	o.pruneComposeReleases(serviceID, release)
	return nil
}

// localCompose returns a `docker compose <args>` command that runs in dir
// with the clean environment of composeProcessEnv: PATH, HOME, DOCKER_HOST
// and XDG_RUNTIME_DIR from the backend's environment plus serviceEnv (already
// filtered by composespec.ServiceEnv), and nothing else. Every local docker
// compose invocation goes through it, so compose interpolates with exactly
// what the deploy policy checked.
func localCompose(ctx context.Context, dir string, serviceEnv map[string]string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	// Cancelled (the deploy's time limit, D-125), compose gets to stop what
	// it started rather than being killed mid-way.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	cmd.Dir = dir
	cmd.Env = localComposeEnv(serviceEnv) // never nil: a nil Env would inherit everything
	return cmd
}

// writeLocalOverride writes a platform override file into a private random
// temp directory of its own (never inside the repository checkout) and
// returns the file and a cleanup that removes the directory.
func writeLocalOverride(ov overrideFile) (string, func(), error) {
	dir, err := os.MkdirTemp("", "paas-override-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir for %s: %w", ov.name, err)
	}
	cleanup := func() { os.RemoveAll(dir) }
	file := filepath.Join(dir, ov.name)
	if err := os.WriteFile(file, ov.content, 0600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write %s: %w", ov.name, err)
	}
	return file, cleanup, nil
}

// ExecInContainer runs a command inside a running container, streaming output to logFn.
// Always uses `docker exec` with the container name directly (never docker compose exec).
func (o *LocalOrchestrator) ExecInContainer(ctx context.Context, serviceID, containerName, command string, logFn func(string)) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", containerName, "sh", "-c", command)
	out, err := cmd.CombinedOutput()
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			logFn(line)
		}
	}
	if err != nil {
		return fmt.Errorf("exec in %s: %w", containerName, err)
	}
	return nil
}

// ListContainers returns the names of the service's containers, running or
// stopped: its blue/green slots and the containers of its compose project
// (listServiceContainerNames).
func (o *LocalOrchestrator) ListContainers(ctx context.Context, serviceID, serviceName string) ([]string, error) {
	return listServiceContainerNames(ctx, o.dockerClient, serviceID, serviceName)
}

// ExecCommands runs each command locally via sh -c, streaming output to logFn.
// Stops on first failure.
func (o *LocalOrchestrator) ExecCommands(ctx context.Context, commands []string, logFn func(string)) error {
	for _, cmdStr := range commands {
		if strings.TrimSpace(cmdStr) == "" {
			continue
		}
		logFn("$ " + cmdStr)
		cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
		out, err := cmd.CombinedOutput()
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if line != "" {
				logFn(line)
			}
		}
		if err != nil {
			return fmt.Errorf("command %q: %w", cmdStr, err)
		}
	}
	return nil
}

// ensureNetwork creates the control plane's network if it does not already
// exist (legacy app networks only).
func (o *LocalOrchestrator) ensureNetwork(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, "docker", "network", "create", o.net.LegacyNetwork).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

// StopCompose stops all containers belonging to the service's compose stack.
// It first tries `docker compose down` for every known project name variant (handles
// renames), then uses the Docker SDK to hard-stop any containers still carrying the
// compose project label — ensuring nothing is left running even if compose down failed
// silently (which it does when no containers matched the project name).
func (o *LocalOrchestrator) StopCompose(ctx context.Context, serviceID, serviceName, _ string) error {
	tmpDir, err := os.MkdirTemp("", "paas-compose-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	if o.dataDir != "" {
		// The stack's containers are removed below, so its files can go too.
		defer os.RemoveAll(o.localComposeDir(serviceID))
	}

	// Try compose down for every known project name variant (current name +
	// legacy id-only), in the same clean environment as a deploy. Down works
	// by project name, from an empty directory: the stack's file isn't read,
	// so a ${VAR:?…} its variables no longer fill can't stop it.
	for _, proj := range rollout.ComposeProjectNames(serviceID, serviceName) {
		cmd := localCompose(ctx, tmpDir, nil,
			"--project-directory", tmpDir,
			"--project-name", proj,
			"down", "--remove-orphans",
		)
		_ = cmd.Run() // best-effort; errors handled by SDK fallback below
	}

	// SDK fallback: stop & remove any containers still labelled with our project names.
	// This catches the case where `docker compose down` returned 0 but matched nothing
	// (e.g. project-name mismatch after a rename, or a Docker Compose version bug).
	var firstErr error
	for _, proj := range rollout.ComposeProjectNames(serviceID, serviceName) {
		list, err := o.dockerClient.ContainerList(ctx, container.ListOptions{
			All:     true,
			Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+proj)),
		})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, c := range list {
			timeout := 10
			_ = o.dockerClient.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
			if err := o.dockerClient.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Stats returns a one-shot CPU and memory snapshot for the service container.
// containerName overrides auto-resolution; pass "" to let the orchestrator find the active container.
func (o *LocalOrchestrator) Stats(ctx context.Context, serviceID string, containerName string) (*service.ContainerStats, error) {
	if containerName == "" {
		containerName = resolveContainer(ctx, o.dockerClient, serviceID, "")
		if containerName == "" {
			return nil, fmt.Errorf("no running container found for service %s", serviceID)
		}
	}
	st, err := containerStats(ctx, o.dockerClient, containerName)
	if err != nil {
		return nil, err
	}
	info, err := o.dockerClient.ContainerInspect(ctx, containerName)
	if err != nil {
		return nil, fmt.Errorf("ContainerInspect: %w", err)
	}
	st.StartedAt, st.RestartCount = startedAt(info.State.StartedAt), info.RestartCount
	return st, nil
}

// ListVolumes inspects each named volume on the local Docker host.
// Returns a VolumeInfo per name; Exists is true if the volume is found.
func (o *LocalOrchestrator) ListVolumes(ctx context.Context, volumeNames []string) ([]service.VolumeInfo, error) {
	result := make([]service.VolumeInfo, 0, len(volumeNames))
	for _, name := range volumeNames {
		// Parse MountName from "paas-vol-{id8}-{mountName}"
		parts := strings.SplitN(name, "-", 4) // ["paas", "vol", "{id8}", "{mountName}"]
		mountName := ""
		if len(parts) == 4 {
			mountName = parts[3]
		}
		info := service.VolumeInfo{Name: name, MountName: mountName}
		if _, err := o.dockerClient.VolumeInspect(ctx, name); err == nil {
			info.Exists = true
		}
		result = append(result, info)
	}
	return result, nil
}

// VolumeSizes returns the size of each named Docker volume that exists.
func (o *LocalOrchestrator) VolumeSizes(ctx context.Context, names []string) (map[string]int64, error) {
	return volumeSizes(ctx, o.dockerClient, names)
}

// PruneVolume removes a named Docker volume from the local host.
// The container must be stopped before calling this.
func (o *LocalOrchestrator) PruneVolume(ctx context.Context, volumeName string) error {
	return o.dockerClient.VolumeRemove(ctx, volumeName, false)
}

// DiscoverVolumes inspects running containers for a service and returns all mounts it finds.
// Used for services created before the volumes feature where svc.Volumes is empty in DB.
func (o *LocalOrchestrator) DiscoverVolumes(ctx context.Context, serviceID string) ([]service.VolumeInfo, error) {
	return discoverVolumes(ctx, o.dockerClient, serviceID)
}

// discoverVolumes returns the mounts of one of serviceID's own slot
// containers (rollout.SlotsOf: never a container that only carries its label).
func discoverVolumes(ctx context.Context, dc *client.Client, serviceID string) ([]service.VolumeInfo, error) {
	list, err := rollout.ServiceContainers(ctx, dc, serviceID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return []service.VolumeInfo{}, nil
	}

	inspect, err := dc.ContainerInspect(ctx, list[0].ID)
	if err != nil {
		return nil, fmt.Errorf("ContainerInspect: %w", err)
	}

	result := make([]service.VolumeInfo, 0, len(inspect.Mounts))
	for _, m := range inspect.Mounts {
		info := service.VolumeInfo{
			Target:   m.Destination,
			Exists:   true,
			ReadOnly: !m.RW,
		}
		if m.Type == mount.TypeBind {
			info.Type = "bind"
			info.Name = m.Source
			info.MountName = m.Source
			info.HostPath = m.Source
		} else {
			info.Type = "volume"
			info.Name = m.Name
			parts := strings.SplitN(m.Name, "-", 4)
			if len(parts) == 4 {
				info.MountName = parts[3]
			} else {
				info.MountName = m.Name
			}
		}
		result = append(result, info)
	}
	return result, nil
}

// buildImage builds a Docker image from a local directory.
// buildArgs are forwarded as --build-arg KEY=VALUE so that ARG declarations
// in the Dockerfile can expose them to RUN steps (e.g. NEXT_PUBLIC_* in Next.js).
func (o *LocalOrchestrator) buildImage(ctx context.Context, buildContextDir, dockerfilePath, imageTag string, buildArgs map[string]*string, auth *deployment.RegistryAuth, logFn func(string)) error {
	o.log.Info("building image", zap.String("tag", imageTag), zap.String("context", buildContextDir), zap.String("dockerfile", dockerfilePath))
	return localBuild(ctx, buildContextDir, dockerfilePath, imageTag, buildArgs, auth, logFn)
}

// localBuild runs docker build of imageTag on the local Docker, with the
// folder dir as the build context (so the CLI applies its .dockerignore)
// and the Dockerfile at dockerfilePath inside it, which a link in the
// repository can't take out of it: the CLI reads the Dockerfile on this
// host. BuildKit where the CLI has buildx (the backend's image does),
// the classic builder where it doesn't. buildArgs are followed by the two
// arguments that keep the build to its service (buildArgList). The build
// has the clean environment compose gets and a Docker config of its own,
// holding auth (the service's registry login) or nothing: a registry login
// on this host is not this build's to pull with. The CLI hands the login
// to the builder for its pulls only; no RUN step sees it.
func localBuild(ctx context.Context, dir, dockerfilePath, imageTag string, buildArgs map[string]*string, auth *deployment.RegistryAuth, logFn func(string)) error {
	kvs, err := buildArgList(imageTag, buildArgs)
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("the build folder: %w", err)
	}
	dockerfile := filepath.Join(dir, dockerfilePath)
	if err := insideBuild(dir, filepath.Dir(dockerfile)); err != nil {
		return err
	}
	// One that isn't there is the build's to report.
	if real, err := filepath.EvalSymlinks(dockerfile); err == nil && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s is a link out of the repository", service.ErrInvalidConfig, dockerfilePath)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s: %w", service.ErrInvalidConfig, dockerfilePath, err)
	}
	var raw []byte
	if auth != nil && auth.Password != "" {
		if raw, err = rollout.RegistryConfig(*auth, auth.Server); err != nil {
			return fmt.Errorf("the build's registry login: %w", err)
		}
	}
	cfg, cleanup, err := dockerConfigDir(raw)
	if err != nil {
		return err
	}
	defer cleanup()
	args := []string{"build", "-t", imageTag, "-f", dockerfile}
	for _, kv := range kvs {
		args = append(args, "--build-arg", kv)
	}
	cmd := exec.CommandContext(ctx, "docker", append(args, dir)...)
	// BUILDX_GIT_INFO: buildx doesn't run git in the context, which is the
	// repository's to shape.
	cmd.Env = append(localComposeEnv(nil), "DOCKER_CONFIG="+cfg, "BUILDX_GIT_INFO=false")
	// Cancelled (a deleted service, the deploy's time limit), the CLI gets
	// to tell the builder to stop.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	if err := runStream(cmd, logFn); err != nil {
		return fmt.Errorf("docker build: %w", err)
	}
	logFn(fmt.Sprintf("Image built: %s", imageTag))
	return nil
}

// dockerConfigDir makes a docker CLI config directory for one build or
// deploy, holding config (a config.json; nil for none), and returns it
// with what removes it. The link in it: the CLI's plugins (buildx,
// compose) installed for this user, not system-wide, are found through
// the config directory.
func dockerConfigDir(config []byte) (string, func(), error) {
	cfg, err := os.MkdirTemp("", "paas-docker-*")
	if err != nil {
		return "", nil, fmt.Errorf("create a docker config dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(cfg) }
	if home, err := os.UserHomeDir(); err == nil {
		_ = os.Symlink(filepath.Join(home, ".docker", "cli-plugins"), filepath.Join(cfg, "cli-plugins"))
	}
	if config != nil {
		if err := os.WriteFile(filepath.Join(cfg, "config.json"), config, 0o600); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("write the registry credentials: %w", err)
		}
	}
	return cfg, cleanup, nil
}

// localNixpacks builds imageTag from dir with Nixpacks, as
// RemoteOrchestrator.nixpacksBuild does on a server: nixpacks plans and
// writes the Dockerfile, localBuild runs it.
func localNixpacks(ctx context.Context, dir, imageTag string, cmds deployment.NixpacksCommands, buildArgs map[string]*string, auth *deployment.RegistryAuth, logFn func(string)) error {
	serviceID, err := builtService(imageTag)
	if err != nil {
		return err
	}
	// nixpacks writes into dir/.nixpacks: one the repository brought (a
	// link out of it, say) goes first.
	if err := os.RemoveAll(filepath.Join(dir, ".nixpacks")); err != nil {
		return fmt.Errorf("clear the nixpacks folder: %w", err)
	}
	if err := noLinksOut(dir); err != nil {
		return err
	}
	flags := nixpacksArgs(cmds, buildArgs)
	// nixpacks gets the clean environment the build does, none of the
	// backend's own.
	planCmd := exec.CommandContext(ctx, "nixpacks", append([]string{"plan", dir}, flags...)...)
	planCmd.Env = localComposeEnv(nil)
	plan, err := planCmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			logFn(strings.TrimSpace(string(exit.Stderr)))
		}
		return fmt.Errorf("nixpacks plan: %w", err)
	}
	vars, err := nixpacksVariables(plan)
	if err != nil {
		return err
	}
	write := exec.CommandContext(ctx, "nixpacks", append([]string{"build", dir, "--out", dir, "--cache-key", serviceID}, flags...)...)
	write.Env = localComposeEnv(nil)
	write.WaitDelay = 30 * time.Second // a child that keeps the output open doesn't keep the deploy
	if err := runStream(write, logFn); err != nil {
		return fmt.Errorf("nixpacks build: %w", err)
	}
	return localBuild(ctx, dir, ".nixpacks/Dockerfile", imageTag, vars, auth, logFn)
}

// ─── Build-arg helpers ────────────────────────────────────────────────────────

// nixpacksCommandArgs are the flags that replace Nixpacks' detected install
// and build commands; none for "".
func nixpacksCommandArgs(cmds deployment.NixpacksCommands) []string {
	var args []string
	if c := strings.TrimSpace(cmds.Install); c != "" {
		args = append(args, "--install-cmd", c)
	}
	if c := strings.TrimSpace(cmds.Build); c != "" {
		args = append(args, "--build-cmd", c)
	}
	return args
}

// ─── Blue/Green slot helpers ──────────────────────────────────────────────────

// activeSlot returns "blue", "green", or "" (no slot running). When both
// run (a switch in progress, or one whose previous slot could not be
// retired), the newer one is active.
func activeSlot(ctx context.Context, dc *client.Client, serviceID, serviceName, envName string) string {
	return rollout.PickActiveSlot(rollout.RunningSlots(ctx, dc, serviceID, serviceName, envName), "")
}

// activeContainerName returns the name of whichever slot is currently running,
// falling back to the blue name if neither is running.
func activeContainerName(ctx context.Context, dc *client.Client, serviceID, serviceName, envName string) string {
	switch activeSlot(ctx, dc, serviceID, serviceName, envName) {
	case "green":
		return rollout.GreenContainerName(serviceID, serviceName, envName)
	default:
		return rollout.BlueContainerName(serviceID, serviceName, envName)
	}
}

// resolveContainer finds the active container name for a service.
// Tries blue/green slots first (if serviceName provided); then falls back to label-based lookup.
func resolveContainer(ctx context.Context, dc *client.Client, serviceID, serviceName string) string {
	if serviceName != "" {
		// Try with empty envName first (legacy names), then label fallback handles the rest.
		name := activeContainerName(ctx, dc, serviceID, serviceName, "")
		list, _ := dc.ContainerList(ctx, container.ListOptions{
			All:     false,
			Filters: filters.NewArgs(filters.Arg("name", "/"+name)),
		})
		if len(list) > 0 {
			return name
		}
	}
	// Fallback: the service's running slots under any naming scheme
	// (env-prefixed, renamed), by label and reserved slot name: a container
	// that only carries the label because its image does is not one of them.
	list, _ := dc.ContainerList(ctx, container.ListOptions{
		All:     false,
		Filters: filters.NewArgs(filters.Arg("label", rollout.LabelServiceID+"="+serviceID)),
	})
	if slots := rollout.SlotsOf(list, serviceID); len(slots) > 0 {
		return rollout.SummaryName(slots[0])
	}
	// Compose: find by project label.
	list, _ = dc.ContainerList(ctx, container.ListOptions{
		All:     false,
		Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+rollout.ComposeProjectName(serviceID, serviceName))),
	})
	if len(list) > 0 && len(list[0].Names) > 0 {
		return strings.TrimPrefix(list[0].Names[0], "/")
	}
	return ""
}

// stopAllSlots stops and removes all of a service's slot containers
// regardless of naming scheme: the paas.service_id label together with a
// slot's reserved name (rollout.ServiceContainers), so a container that only carries
// the label because its image does is never stopped.
func stopAllSlots(ctx context.Context, dc *client.Client, serviceID string) error {
	// Stop legacy container (pre-blue/green naming). Best effort — may not exist.
	_ = stopContainer(ctx, dc, serviceID)

	list, err := rollout.ServiceContainers(ctx, dc, serviceID)
	if err != nil {
		return err
	}
	for _, c := range list {
		timeout := 10
		_ = dc.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
		if err := dc.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("ContainerRemove %s: %w", c.ID[:12], err)
		}
	}
	return nil
}

// ─── Shared container helpers ─────────────────────────────────────────────────

// slotLabels are the labels of a rollout's new slot: its Traefik routing,
// paas.service_id and paas.deployment_id, which recovery uses to tell slots
// apart.
func slotLabels(spec rollout.Spec, route deployment.Routing) map[string]string {
	labels := buildTraefikLabels(spec.Slot.ServiceID, route.Domains, spec.Port, route.Access)
	labels[rollout.LabelServiceID] = spec.Slot.ServiceID
	if spec.DeploymentID != "" {
		labels[rollout.LabelDeploymentID] = spec.DeploymentID
	}
	return labels
}

// stopContainer stops and removes the container for a service, if it exists.
func stopContainer(ctx context.Context, dc *client.Client, serviceID string) error {
	containerName := rollout.ContainerNameForService(serviceID)

	containers, err := dc.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("name", "/"+containerName)),
	})
	if err != nil {
		return fmt.Errorf("ContainerList: %w", err)
	}

	for _, c := range containers {
		if rollout.SummaryName(c) != containerName { // the name filter also matches longer names
			continue
		}
		timeout := 10
		_ = dc.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
		if err := dc.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("ContainerRemove %s: %w", c.ID[:12], err)
		}
	}
	return nil
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

func cloneRepository(ctx context.Context, repoURL, branch, baseDirectory string, logFn func(string)) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "paas-build-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	cloneURL, gitArgs, forget, err := gitcred.Local(repoURL)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	defer forget()
	// "--" ends the options: a URL or branch is never read as one, whatever
	// it starts with (the service layer refuses those at save as well).
	args := append(gitArgs, "clone", "--depth", "1", "--branch", branch, "--progress", "--", cloneURL, tmpDir)
	output, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
	for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
		if line != "" {
			logFn(line)
		}
	}
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("git clone failed: %w", err)
	}

	buildDir, err := service.JoinBaseDirectory(tmpDir, baseDirectory)
	if err == nil {
		err = insideBuild(tmpDir, buildDir)
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return buildDir, cleanup, nil
}

// insideBuild refuses dir, which nixpacks reads, when a link in the
// repository takes it out of root: base_directory's check is by name only.
// (localBuild checks a docker build's Dockerfile.)
func insideBuild(root, dir string) error {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("the build folder: %w", err)
	}
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("%w: base_directory: %w", service.ErrInvalidConfig, err)
	}
	if d != r && !strings.HasPrefix(d, r+string(filepath.Separator)) {
		return fmt.Errorf("%w: base_directory is a link out of the repository", service.ErrInvalidConfig)
	}
	return nil
}

// noLinksOut refuses a build folder that holds a link leaving it, or one
// leading nowhere: nixpacks reads the files it finds through links, on this
// host, and puts what they say in its plan (a Procfile's command, say).
func noLinksOut(dir string) error {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("the build folder: %w", err)
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink == 0 {
			return err
		}
		if real, err := filepath.EvalSymlinks(path); err != nil || (real != root && !strings.HasPrefix(real, root+string(filepath.Separator))) {
			rel, _ := filepath.Rel(dir, path)
			return fmt.Errorf("%w: %s is a link that doesn't stay in the build folder", service.ErrInvalidConfig, rel)
		}
		return nil
	})
}

// gitHead is `git log -1 --format=%H%n%s` in dir, a clone on this host.
func gitHead(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "log", "-1", "--format=%H%n%s").Output()
	if err != nil {
		return "", fmt.Errorf("git log: %w", err)
	}
	return string(out), nil
}

// gitHeadCmd is gitHead's command for a clone on a remote server.
func gitHeadCmd(dir string) string {
	return "git -C " + shellEscape(dir) + " log -1 " + shellEscape("--format=%H%n%s")
}

var fullCommitSHA = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// parseHeadCommit reads gitHead's output: a full SHA (SHA-1 or SHA-256),
// then the subject.
func parseHeadCommit(out string) (deployment.Commit, error) {
	sha, subject, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if sha = strings.TrimSpace(sha); !fullCommitSHA.MatchString(sha) {
		if len(out) > 80 {
			out = out[:80] + "…"
		}
		return deployment.Commit{}, fmt.Errorf("unexpected git log output %q", out)
	}
	return deployment.Commit{SHA: sha, Subject: strings.TrimSpace(subject)}, nil
}

// clonedCommit is the commit a build's clone checked out, read by head and
// logged as the clone's last line ("Cloned main at 3a90c11"). One it cannot
// read is logged and left empty, and the build goes on: the deployment then
// shows what it was started with.
func clonedCommit(branch string, logFn func(string), head func() (string, error)) deployment.Commit {
	out, err := head()
	var c deployment.Commit
	if err == nil {
		c, err = parseHeadCommit(out)
	}
	if err != nil {
		logFn("⚠ Could not read the commit checked out: " + err.Error())
		return deployment.Commit{}
	}
	logFn(fmt.Sprintf("Cloned %s at %s", branch, c.SHA[:7]))
	return c
}

// dnsResolver is the ACME resolver for wildcard certificates (DNS-01), which
// Traefik on a remote server has when the server has a DNS provider
// (server.provision).
const dnsResolver = "letsencrypt-dns"

func buildTraefikLabels(serviceID string, domains []string, port int, ac service.AccessControl) map[string]string {
	// Worker/cron services have no domains and no exposed port — skip Traefik entirely.
	if len(domains) == 0 || port == 0 {
		return map[string]string{"traefik.enable": "false"}
	}

	labels := map[string]string{"traefik.enable": "true"}

	// Build the middleware list once — shared across all domain routers for this service.
	prefix := fmt.Sprintf("paas-%s", serviceID[:8])
	middlewares := []string{"secure-headers@file"}

	if ac.BasicAuthUser != "" && ac.BasicAuthPassword != "" {
		entry := rollout.HtpasswdEntry(ac.BasicAuthUser, ac.BasicAuthPassword)
		if entry != "" {
			labels[rollout.BasicAuthUsersLabel(serviceID)] = entry
			middlewares = append(middlewares, prefix+"-basicauth")
		}
	}

	if len(ac.IPAllowlist) > 0 {
		labels[fmt.Sprintf("traefik.http.middlewares.%s-ipallow.ipallowlist.sourcerange", prefix)] = strings.Join(ac.IPAllowlist, ",")
		middlewares = append(middlewares, prefix+"-ipallow")
	}

	middlewareStr := strings.Join(middlewares, ",")

	svcName := traefikServiceName(serviceID)
	for i, hostname := range domains {
		routerName := fmt.Sprintf("paas-%s-%d", serviceID[:8], i)
		// The HTTP router of a temporary (sslip.io) name that still waits for
		// its certificate serves instead of redirecting (below), under a name
		// of its own: the deploy that follows the certificate redirects, and
		// one router that the two slots of that deploy define differently is
		// a routing change, which takes the router down until the previous
		// slot has left Traefik's rotation.
		plain := domain.IsTemporary(hostname) && !slices.Contains(ac.Certified, hostname) && (ac.BasicAuthUser == "" || ac.BasicAuthPassword == "")
		httpRouter := fmt.Sprintf("paas-%s-%d-http", serviceID[:8], i)
		if plain {
			httpRouter = fmt.Sprintf("paas-%s-%d-plain-http", serviceID[:8], i)
		}
		rule, resolver := fmt.Sprintf("Host(`%s`)", hostname), "letsencrypt"
		if base, ok := strings.CutPrefix(hostname, "*."); ok {
			// A wildcard: one label in front of base. Traefik ranks routers by
			// rule length, which would put this regexp ahead of every exact
			// host on the server, so it gets the lowest priority. Its
			// certificate (*.base) needs the DNS-01 resolver, which the
			// provisioner adds when the server has a DNS provider; Traefik
			// can't derive the name from a regexp, so it is given.
			rule, resolver = fmt.Sprintf("HostRegexp(`^[^.]+\\.%s$`)", regexp.QuoteMeta(base)), dnsResolver
			labels[fmt.Sprintf("traefik.http.routers.%s.priority", routerName)] = "1"
			labels[fmt.Sprintf("traefik.http.routers.%s.priority", httpRouter)] = "1"
			labels[fmt.Sprintf("traefik.http.routers.%s.tls.domains[0].main", routerName)] = hostname
		}

		if len(ac.TLSDomains) > 0 && !strings.HasPrefix(hostname, "*.") {
			labels[fmt.Sprintf("traefik.http.routers.%s.tls.domains[0].main", routerName)] = ac.TLSDomains[0]
			if len(ac.TLSDomains) > 1 {
				labels[fmt.Sprintf("traefik.http.routers.%s.tls.domains[0].sans", routerName)] = strings.Join(ac.TLSDomains[1:], ",")
			}
			if ac.TLSResolver != "" {
				resolver = ac.TLSResolver
			}
		}
		labels[fmt.Sprintf("traefik.http.routers.%s.rule", routerName)] = rule
		labels[fmt.Sprintf("traefik.http.routers.%s.entrypoints", routerName)] = "websecure"
		labels[fmt.Sprintf("traefik.http.routers.%s.tls.certresolver", routerName)] = resolver
		labels[fmt.Sprintf("traefik.http.routers.%s.middlewares", routerName)] = middlewareStr
		labels[fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port", svcName)] = fmt.Sprintf("%d", port)
		// HTTP router: it redirects to HTTPS. A temporary (sslip.io) name
		// serves over HTTP instead until its certificate is out, since that
		// can wait hours behind a limit Let's Encrypt shares among everyone
		// (D-150); it wins over the entrypoint's catch-all redirect, which
		// Traefik runs at priority 1. Behind basic auth it still redirects:
		// a password never crosses plain HTTP.
		labels[fmt.Sprintf("traefik.http.routers.%s.rule", httpRouter)] = rule
		labels[fmt.Sprintf("traefik.http.routers.%s.entrypoints", httpRouter)] = "web"
		if plain {
			labels[fmt.Sprintf("traefik.http.routers.%s.middlewares", httpRouter)] = middlewareStr
		} else {
			labels[fmt.Sprintf("traefik.http.routers.%s.middlewares", httpRouter)] = "redirect-to-https@file"
		}
	}
	return labels
}

// traefikServiceName is the Traefik service (load balancer) the routers of
// serviceID point at.
func traefikServiceName(serviceID string) string {
	return fmt.Sprintf("paas-svc-%s", serviceID[:8])
}

// runStream runs cmd and hands logFn each line it writes, stdout and
// stderr, as it is written: a build's log is followed while it runs.
func runStream(cmd *exec.Cmd, logFn func(string)) error {
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	read := make(chan struct{})
	// Ends when pw closes, which is right after cmd does.
	go func() {
		defer close(read)
		_ = lines.Read(pr, func(line string) {
			if line != "" {
				logFn(line)
			}
		})
		_, _ = io.Copy(io.Discard, pr) // a failed read never blocks the command's writes
	}()
	err := cmd.Run()
	pw.Close()
	<-read
	return err
}

// shellEscape wraps a string in single quotes, escaping any existing single quotes.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// sanitizeRepoURL strips embedded credentials from a repo URL before logging.
func sanitizeRepoURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	u.User = nil
	return u.String()
}

// diskUser is the part of the Docker client volumeSizes uses.
type diskUser interface {
	DiskUsage(ctx context.Context, options types.DiskUsageOptions) (types.DiskUsage, error)
}

// volumeSizes reads the sizes of names from Docker's disk usage report
// (GET /system/df, volumes only). Docker computes it by walking every
// volume's files on the host. A volume missing from the report does not
// exist yet; one whose size is not available (-1) is left out.
func volumeSizes(ctx context.Context, dc diskUser, names []string) (map[string]int64, error) {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	du, err := dc.DiskUsage(ctx, types.DiskUsageOptions{Types: []types.DiskUsageObject{types.VolumeObject}})
	if err != nil {
		return nil, fmt.Errorf("DiskUsage: %w", err)
	}
	out := make(map[string]int64, len(names))
	for _, v := range du.Volumes {
		if v != nil && want[v.Name] && v.UsageData != nil && v.UsageData.Size >= 0 {
			out[v.Name] = v.UsageData.Size
		}
	}
	return out, nil
}
