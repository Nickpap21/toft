package docker

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/gitcred"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerclient "github.com/docker/docker/client"
	"go.uber.org/zap"
)

// cleanupTimeout bounds a best-effort cleanup command (rm -rf of a temp
// dir, docker logout), which runs even after ctx was cancelled.
const cleanupTimeout = 30 * time.Second

// remoteHost is what RemoteOrchestrator needs of a server's SSH connection;
// sshpool.RemoteHost satisfies it. Every call honours ctx: when it ends, the
// remote command is stopped.
type remoteHost interface {
	Exec(ctx context.Context, cmd string) (string, error)
	ExecInput(ctx context.Context, cmd string, stdin io.Reader) (string, error)
	ExecStream(ctx context.Context, cmd string, logFn func(string)) error
	Upload(ctx context.Context, remotePath string, content []byte) error
	DockerClient() (*dockerclient.Client, error)
	SubsystemStream(ctx context.Context, name string, stdin io.Reader, lineFn func(string)) error
}

// RemoteOrchestrator builds and runs containers on a remote server via SSH.
// Build uses SSH exec (git clone + docker build on the server).
// Run/Stop use the Docker SDK tunnelled through the SSH connection.
// It implements deployment.Orchestrator.
type RemoteOrchestrator struct {
	host remoteHost
	net  rollout.NetConfig
	log  *zap.Logger
	// agent: the server's agent runs rollouts and compose ups the panel
	// hands it (D-111) rather than the panel driving them over the tunnel.
	agent bool
	// epoch is the panel's, on each hand-off (D-113).
	epoch string
	// deadlines: the agent ends a rollout at the deploy's time limit (D-125).
	deadlines bool
	// checks: the agent creates slots with the service's own container
	// check (rollout.Slot.Check).
	checks bool
	// metrics: the server's agent samples it every minute and keeps the
	// samples (D-113); the collector asks it for them (AgentServiceStats).
	metrics bool
}

func newRemoteOrchestrator(h remoteHost, net rollout.NetConfig, log *zap.Logger) *RemoteOrchestrator {
	return &RemoteOrchestrator{host: h, net: net.WithDefaults(), log: log}
}

// cleanup runs a best-effort cleanup command. It runs even when ctx has been
// cancelled (the operation was abandoned), bounded by cleanupTimeout.
func (o *RemoteOrchestrator) cleanup(ctx context.Context, cmd string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	_, _ = o.host.Exec(ctx, cmd)
}

// streamRetry runs a build or a pull, which are safe to repeat, once more
// when the connection to the server dropped under it (an agent that
// reconnected, D-106). Any other failure is returned as it is.
func (o *RemoteOrchestrator) streamRetry(ctx context.Context, cmd string, logFn func(string)) error {
	err := o.host.ExecStream(ctx, cmd, logFn)
	if err == nil || ctx.Err() != nil || !errors.Is(err, sshpool.ErrConnectionLost) {
		return err
	}
	logFn("Lost the connection to the server; running this step again once it is back...")
	return o.host.ExecStream(ctx, cmd, logFn)
}

// BuildFromGit clones repoURL@branch on the remote server and runs docker build there.
func (o *RemoteOrchestrator) BuildFromGit(ctx context.Context, repoURL, branch, baseDir, imageTag string, buildArgs map[string]*string, logFn func(string)) (deployment.Commit, error) {
	if err := service.ValidateBaseDirectory(baseDir); err != nil {
		return deployment.Commit{}, err
	}
	tmpDir, err := o.host.Exec(ctx, "mktemp -d /tmp/paas-build-XXXXXX")
	if err != nil {
		return deployment.Commit{}, fmt.Errorf("create remote temp dir: %w", err)
	}
	defer o.cleanup(ctx, "rm -rf "+tmpDir)

	logFn(fmt.Sprintf("Cloning %s @ %s on remote...", sanitizeRepoURL(repoURL), branch))
	if err := o.cloneOnRemote(ctx, repoURL, branch, tmpDir, logFn); err != nil {
		return deployment.Commit{}, err
	}
	commit := clonedCommit(branch, logFn, func() (string, error) { return o.host.Exec(ctx, gitHeadCmd(tmpDir)) })
	if err := o.dropGitDir(ctx, tmpDir); err != nil {
		return commit, err
	}

	// Always use the repo root as build context. When baseDir is set, point -f at the
	// Dockerfile inside that subdirectory so monorepo root-level files are accessible.
	buildArgFlags := buildArgsCLI(buildArgs)
	dockerfileFlag := ""
	// -f must be absolute: each ExecStream call is a fresh SSH session with no
	// guaranteed cwd, so a path relative to tmpDir (e.g. "api/Dockerfile") resolves
	// against the session's home directory instead of the cloned repo.
	dir, err := service.JoinBaseDirectory(tmpDir, baseDir)
	if err != nil {
		return commit, err
	}
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	if err := o.insideBuild(ctx, tmpDir, dockerfilePath); err != nil {
		return commit, err
	}
	if baseDir != "" {
		dockerfileFlag = fmt.Sprintf("-f %s ", shellEscape(dockerfilePath))
	}
	logFn("Building Docker image on remote...")
	buildCmd := fmt.Sprintf("docker build -t %s %s %s%s 2>&1", imageTag, buildArgFlags, dockerfileFlag, tmpDir)
	if err := o.streamRetry(ctx, buildCmd, logFn); err != nil {
		return commit, fmt.Errorf("remote docker build: %w", err)
	}

	logFn(fmt.Sprintf("Image built: %s", imageTag))
	return commit, nil
}

// BuildFromArchive unpacks source, a commit's tree as a tar (D-77), into a
// new directory on the remote server over SSH and builds imageTag there:
// docker build with baseDir/Dockerfile, or nixpacks in baseDir. The server
// gets the files only: no URL, no token, no .git.
func (o *RemoteOrchestrator) BuildFromArchive(ctx context.Context, source io.Reader, baseDir, imageTag string, nixpacks *deployment.NixpacksCommands, buildArgs map[string]*string, logFn func(string)) error {
	if err := service.ValidateBaseDirectory(baseDir); err != nil {
		return err
	}
	tmpDir, err := o.host.Exec(ctx, "mktemp -d /tmp/paas-build-XXXXXX")
	if err != nil {
		return fmt.Errorf("create remote temp dir: %w", err)
	}
	tmpDir = strings.TrimSpace(tmpDir)
	defer o.cleanup(ctx, "rm -rf "+shellEscape(tmpDir))

	logFn("Sending the source to the server...")
	// -o: the files belong to the SSH user, not to the uid the tar names.
	if _, err := o.host.ExecInput(ctx, "tar -x -o -f - -C "+shellEscape(tmpDir), source); err != nil {
		return fmt.Errorf("unpack the source on the server: %w", err)
	}
	dir := tmpDir
	if baseDir != "" {
		if dir, err = service.JoinBaseDirectory(tmpDir, baseDir); err != nil {
			return err
		}
	}
	if err := o.insideBuild(ctx, tmpDir, dir, filepath.Join(dir, "Dockerfile")); err != nil {
		return err
	}
	if nixpacks != nil {
		logFn(fmt.Sprintf("Building %s with nixpacks on remote...", imageTag))
		cmdFlags := ""
		for _, a := range nixpacksCommandArgs(*nixpacks) {
			cmdFlags += " " + shellEscape(a)
		}
		buildCmd := fmt.Sprintf("nixpacks build %s --name %s%s %s 2>&1", shellEscape(dir), imageTag, cmdFlags, nixpacksEnvCLI(buildArgs))
		if err := o.streamRetry(ctx, buildCmd, logFn); err != nil {
			return fmt.Errorf("remote nixpacks build: %w", err)
		}
	} else {
		dockerfileFlag := ""
		if baseDir != "" {
			dockerfileFlag = fmt.Sprintf("-f %s ", shellEscape(filepath.Join(dir, "Dockerfile")))
		}
		logFn("Building Docker image on remote...")
		buildCmd := fmt.Sprintf("docker build -t %s %s %s%s 2>&1", imageTag, buildArgsCLI(buildArgs), dockerfileFlag, shellEscape(tmpDir))
		if err := o.streamRetry(ctx, buildCmd, logFn); err != nil {
			return fmt.Errorf("remote docker build: %w", err)
		}
	}
	logFn(fmt.Sprintf("Image built: %s", imageTag))
	return nil
}

// insideBuild refuses a build whose paths a link in the repository takes
// out of root, the folder it was cloned or unpacked into: base_directory's
// check is by name only, and docker build -f and nixpacks follow links.
func (o *RemoteOrchestrator) insideBuild(ctx context.Context, root string, paths ...string) error {
	out, err := o.host.Exec(ctx, insideBuildCmd(root, paths...))
	if err != nil {
		return fmt.Errorf("check the build folder: %w", err)
	}
	if strings.Contains(out, "outside") {
		return fmt.Errorf("%w: base_directory or its Dockerfile is a link out of the repository", service.ErrInvalidConfig)
	}
	return nil
}

// insideBuildCmd prints "outside" for each path that, its links followed
// (realpath -m: the last parts needn't exist), isn't under root. A failed
// realpath fails the command, so nothing passes unchecked.
func insideBuildCmd(root string, paths ...string) string {
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		quoted = append(quoted, shellEscape(p))
	}
	return "r=$(realpath -m -- " + shellEscape(root) + ") || exit 1; for p in " + strings.Join(quoted, " ") +
		`; do q=$(realpath -m -- "$p") || exit 1; case "$q/" in "$r"/*) ;; *) echo outside ;; esac; done`
}

// dropGitDir removes the clone's .git before the build, as the archive
// build never has one: a fresh clone's .git differs every time, so a COPY
// of the repository would miss the layer cache even on the same commit
// (D-131's 63 s same-commit redeploy).
func (o *RemoteOrchestrator) dropGitDir(ctx context.Context, dir string) error {
	if _, err := o.host.Exec(ctx, "rm -rf "+shellEscape(dir+"/.git")); err != nil {
		return fmt.Errorf("remove .git from the build context: %w", err)
	}
	return nil
}

// BuildFromGitNixpacks clones repoURL@branch on the remote server and builds via nixpacks.
// nixpacks must be installed on the remote server.
func (o *RemoteOrchestrator) BuildFromGitNixpacks(ctx context.Context, repoURL, branch, baseDir, imageTag string, cmds deployment.NixpacksCommands, buildArgs map[string]*string, logFn func(string)) (deployment.Commit, error) {
	if err := service.ValidateBaseDirectory(baseDir); err != nil {
		return deployment.Commit{}, err
	}
	tmpDir, err := o.host.Exec(ctx, "mktemp -d /tmp/paas-build-XXXXXX")
	if err != nil {
		return deployment.Commit{}, fmt.Errorf("create remote temp dir: %w", err)
	}
	tmpDir = strings.TrimSpace(tmpDir)
	defer o.cleanup(ctx, "rm -rf "+tmpDir)

	logFn(fmt.Sprintf("Cloning %s @ %s on remote...", sanitizeRepoURL(repoURL), branch))
	if err := o.cloneOnRemote(ctx, repoURL, branch, tmpDir, logFn); err != nil {
		return deployment.Commit{}, err
	}
	commit := clonedCommit(branch, logFn, func() (string, error) { return o.host.Exec(ctx, gitHeadCmd(tmpDir)) })
	if err := o.dropGitDir(ctx, tmpDir); err != nil {
		return commit, err
	}

	buildDir := tmpDir
	if baseDir != "" {
		if buildDir, err = service.JoinBaseDirectory(tmpDir, baseDir); err != nil {
			return commit, err
		}
	}
	if err := o.insideBuild(ctx, tmpDir, buildDir); err != nil {
		return commit, err
	}

	logFn(fmt.Sprintf("Building %s with nixpacks on remote...", imageTag))
	nixpacksEnvFlags := nixpacksEnvCLI(buildArgs)
	cmdFlags := ""
	for _, a := range nixpacksCommandArgs(cmds) {
		cmdFlags += " " + shellEscape(a) // the commands are user input
	}
	buildCmd := fmt.Sprintf("nixpacks build %s --name %s%s %s 2>&1", shellEscape(buildDir), imageTag, cmdFlags, nixpacksEnvFlags)
	if err := o.streamRetry(ctx, buildCmd, logFn); err != nil {
		return commit, fmt.Errorf("remote nixpacks build: %w", err)
	}

	logFn(fmt.Sprintf("Image built: %s", imageTag))
	return commit, nil
}

// Rollout runs a single-container deploy's rollout on the server: its agent
// runs it when it can (handOff, D-111), otherwise the panel does, through
// the SSH-tunnelled Docker API (rollout.Run).
func (o *RemoteOrchestrator) Rollout(ctx context.Context, spec rollout.Spec, route deployment.Routing, hooks rollout.Hooks) rollout.Result {
	o.log.Info("rollout (remote)", zap.String("service_id", spec.Slot.ServiceID), zap.String("image", spec.Slot.Image))
	spec.Slot.Labels = slotLabels(spec, route)
	if o.agent && hooks.HandOff != nil {
		return o.handOff(ctx, rollout.Request{Spec: spec, Net: o.net}, hooks)
	}
	dc, err := o.host.DockerClient()
	if err != nil {
		msg := "SSH Docker client: " + err.Error()
		if hooks.Log != nil {
			hooks.Log("✗ " + msg)
		}
		return rollout.Result{Step: rollout.StepConnect, Err: msg}
	}
	defer dc.Close()
	warn := func(msg string) { o.log.Warn(msg, zap.String("service_id", spec.Slot.ServiceID)) }
	return rollout.Run(ctx, tunnelHost{DockerHost: rollout.DockerHost{Client: dc, Net: o.net, Warn: warn}, o: o}, spec, hooks)
}

// tunnelHost is a rollout host over the SSH Docker tunnel whose pull runs
// on the server's docker CLI (pull): a pull cut off by a lost connection
// runs once more.
type tunnelHost struct {
	rollout.DockerHost
	o *RemoteOrchestrator
}

func (h tunnelHost) Pull(ctx context.Context, image string, auth *rollout.RegistryAuth, logFn func(string)) error {
	return h.o.pull(ctx, image, auth, logFn)
}

// pull pulls a Docker image on the remote server with optional registry auth.
func (o *RemoteOrchestrator) pull(ctx context.Context, imageRef string, auth *rollout.RegistryAuth, logFn func(string)) error {
	o.log.Info("pulling image (remote)", zap.String("image", imageRef))

	if auth != nil && auth.Password != "" {
		server := auth.Server
		if server == "" {
			server = registryFromImageRef(imageRef)
		}
		logFn(fmt.Sprintf("Logging in to registry %s...", server))
		// docker login requires a username; fall back to "token" for token-only credentials.
		username := auth.Username
		if username == "" {
			username = "token"
		}
		logout, err := o.registryLogin(ctx, server, username, auth.Password, logFn)
		if err != nil {
			return err
		}
		defer logout()
	}

	logFn(fmt.Sprintf("Pulling %s...", imageRef))
	return o.streamRetry(ctx, fmt.Sprintf("docker pull %s 2>&1", shellEscape(imageRef)), logFn)
}

// Stop removes all slot containers for the service on the remote server via SSH-tunnelled Docker.
func (o *RemoteOrchestrator) Stop(ctx context.Context, serviceID string) error {
	o.log.Info("stopping container (remote)", zap.String("service_id", serviceID))

	dc, err := o.host.DockerClient()
	if err != nil {
		return fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()

	return stopAllSlots(ctx, dc, serviceID)
}

// StreamLogs streams live logs from the remote container to logFn via SSH,
// each line led by Docker's RFC 3339 timestamp and a space.
// containerName overrides auto-resolution; pass "" to let the orchestrator find the active container.
func (o *RemoteOrchestrator) StreamLogs(ctx context.Context, serviceID string, tailLines int, containerName string, logFn func(string)) error {
	o.log.Info("streaming logs (remote)", zap.String("service_id", serviceID))
	if containerName == "" {
		containerName = remoteActiveContainerName(ctx, o.host, serviceID, "")
		if containerName == "" {
			return fmt.Errorf("no running container found for service %s", serviceID)
		}
	}
	cmd := fmt.Sprintf("docker logs --follow --timestamps --tail=%d %s 2>&1", tailLines, shellEscape(containerName))
	return o.host.ExecStream(ctx, cmd, logFn)
}

// remoteActiveContainerName returns the running container name for a service on the remote host.
// Tries the service's slots by label first (any naming scheme), then blue/green slot names.
func remoteActiveContainerName(ctx context.Context, sc interface {
	Exec(ctx context.Context, cmd string) (string, error)
}, serviceID, serviceName string) string {
	// By the paas.service_id label, keeping only containers with a slot's
	// reserved name (rollout.IsSlotName): an image can carry the label too.
	out, err := sc.Exec(ctx, fmt.Sprintf(
		"docker ps --filter %s --format '{{.Names}}' 2>/dev/null",
		shellEscape("label="+rollout.LabelServiceID+"="+serviceID),
	))
	if err == nil {
		for _, name := range strings.Split(out, "\n") {
			if name = strings.TrimSpace(name); rollout.IsSlotName(name, serviceID) {
				return name
			}
		}
	}
	// Try blue/green slots by name (useful when serviceName is known; empty envName = legacy names).
	for _, name := range []string{rollout.BlueContainerName(serviceID, serviceName, ""), rollout.GreenContainerName(serviceID, serviceName, "")} {
		out, err := sc.Exec(ctx, fmt.Sprintf("docker inspect --format '{{.State.Running}}' %s 2>/dev/null", name))
		if err == nil && strings.TrimSpace(out) == "true" {
			return name
		}
	}
	// Try compose project containers by label.
	out, err = sc.Exec(ctx, fmt.Sprintf(
		"docker ps --filter %s --format '{{.Names}}' 2>/dev/null | head -1",
		shellEscape("label="+rollout.LabelComposeProject+"="+rollout.ComposeProjectName(serviceID, serviceName)),
	))
	if err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	return ""
}

// cloneOnRemote shallow-clones repoURL@branch into dir on the remote host. A
// user:token pair in the URL goes into a private git credential file next to
// dir instead of onto the command line, where the remote process list would
// show it. A URL with only a token as its user is cloned as given: git would
// prompt for the missing password if it came from a credential file.
func (o *RemoteOrchestrator) cloneOnRemote(ctx context.Context, repoURL, branch, dir string, logFn func(string)) error {
	cloneURL, credLine := gitcred.Split(repoURL)
	gitOpts := ""
	if credLine != "" {
		credFile := dir + ".git-credentials"
		if err := o.uploadPrivate(ctx, credFile, []byte(credLine+"\n")); err != nil {
			return fmt.Errorf("upload git credentials: %w", err)
		}
		defer o.cleanup(ctx, "rm -f "+shellEscape(credFile))
		gitOpts = "-c credential.helper= -c " + shellEscape("credential.helper=store --file="+credFile) + " "
	}
	// "--" ends the options, so the quoted URL is never read as one.
	cloneCmd := fmt.Sprintf("git %sclone --depth 1 --branch %s -- %s %s 2>&1", gitOpts, shellEscape(branch), shellEscape(cloneURL), dir)
	if err := o.host.ExecStream(ctx, cloneCmd, logFn); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}
	return nil
}

// registryLogin runs `docker login` on the remote host with the password on
// stdin, and returns the matching logout. In an `echo <password> | docker
// login` pipeline the password would sit on the remote shell's command line,
// readable by any user in the process list. An empty server means Docker Hub.
func (o *RemoteOrchestrator) registryLogin(ctx context.Context, server, username, password string, logFn func(string)) (logout func(), err error) {
	serverArg := ""
	if server != "" {
		serverArg = " " + shellEscape(server)
	}
	out, err := o.host.ExecInput(ctx,
		fmt.Sprintf("docker login%s -u %s --password-stdin 2>&1", serverArg, shellEscape(username)),
		strings.NewReader(password))
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			logFn(line)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("docker login: %w", err)
	}
	return func() { o.cleanup(ctx, "docker logout"+serverArg) }, nil
}

// newComposeRelease creates an empty, private directory for this deploy's
// compose files under rollout.ComposeDir. It outlives the deploy: relative bind
// mounts point into it while the stack runs, and Docker resolves them again
// whenever it restarts a container. A new path per deploy also changes the
// configuration of every compose service that bind-mounts a file from it, so
// up recreates exactly those services and they see the new files.
func (o *RemoteOrchestrator) newComposeRelease(ctx context.Context, serviceID string) (string, error) {
	dir := shellEscape(rollout.ComposeDir(serviceID))
	release, err := o.host.Exec(ctx, "mkdir -p "+dir+" && mktemp -d "+dir+"/r-XXXXXXXX")
	if err != nil {
		return "", fmt.Errorf("create compose release directory: %w", err)
	}
	return strings.TrimSpace(release), nil
}

// RunCompose uploads the compose file to a new release directory of the
// service (newComposeRelease), writes the service's env vars to the
// project's .env (the only variables compose interpolates with besides PATH,
// HOME, DOCKER_HOST and XDG_RUNTIME_DIR: every compose command runs under
// remoteCompose's env -i), and runs docker compose there. route, when not
// nil, adds the platform's routing override file (composeRouteOverride).
func (o *RemoteOrchestrator) RunCompose(ctx context.Context, deploymentID string, serviceID, serviceName, composeContent string, envVars map[string]string, route *deployment.ComposeRoute, nets deployment.ComposeNetworks, limits map[string]deployment.ComposeLimit, auth *deployment.RegistryAuth, hooks rollout.Hooks) error {
	release, err := o.newComposeRelease(ctx, serviceID)
	if err != nil {
		return err
	}
	composeFile := release + "/docker-compose.yml"
	if err := o.uploadPrivate(ctx, composeFile, []byte(composeContent)); err != nil {
		return fmt.Errorf("upload compose file: %w", err)
	}
	return o.composeUp(ctx, deploymentID, serviceID, serviceName, release, release, composeFile, envVars, route, nets, limits, auth, hooks)
}

// RunComposeFromRepo tars the local repo directory, uploads it to the remote,
// extracts it, writes the .env file into the compose file's directory, and
// runs docker compose from there. This preserves relative file paths (e.g.
// ../nginx/images.conf) in the compose file. The deployment pipeline wrote
// the same .env into repoDir before the policy check
// (composespec.StageDotEnv), so the check read what compose reads here.
func (o *RemoteOrchestrator) RunComposeFromRepo(ctx context.Context, deploymentID string, serviceID, serviceName, repoDir, composeFilePath string, envVars map[string]string, route *deployment.ComposeRoute, nets deployment.ComposeNetworks, limits map[string]deployment.ComposeLimit, auth *deployment.RegistryAuth, hooks rollout.Hooks) error {
	logFn := hooks.Log
	// Tar the local repo and upload it into a new release directory.
	logFn("Uploading repo to remote...")
	tarball, err := tarDir(repoDir)
	if err != nil {
		return fmt.Errorf("tar repo: %w", err)
	}
	release, err := o.newComposeRelease(ctx, serviceID)
	if err != nil {
		return err
	}
	remoteTarPath := release + "/.paas-repo.tar.gz"
	if err := o.host.Upload(ctx, shellEscape(remoteTarPath), tarball); err != nil {
		return fmt.Errorf("upload repo: %w", err)
	}
	if _, err := o.host.Exec(ctx, fmt.Sprintf("tar -xzf %s -C %s && rm %s",
		shellEscape(remoteTarPath), shellEscape(release), shellEscape(remoteTarPath))); err != nil {
		return fmt.Errorf("extract repo: %w", err)
	}
	logFn(fmt.Sprintf("Repo uploaded (%d KB)", len(tarball)/1024))

	// --project-directory must be the compose file's own directory so that relative
	// paths in bind mounts (e.g. ../nginx/images.conf) resolve correctly.
	composeFileDir := path.Dir(composeFilePath) // e.g. "environments/dev"
	return o.composeUp(ctx, deploymentID, serviceID, serviceName, release, release+"/"+composeFileDir, release+"/"+composeFilePath, envVars, route, nets, limits, auth, hooks)
}

// composeUp writes the service's env vars to projectDir/.env and brings the
// service's stack up from composeFile, updating it in place. Every docker
// compose command runs under remoteCompose (a clean environment).
//
// The .env file is removed when composeUp returns: the containers hold
// their environment, and the secrets should not stay on the server's disk.
// release is the deploy's directory; after a successful up the service's
// older releases are removed (their bind mounts were all replaced).
//
// When the server's agent runs rollouts, up itself is handed to it
// (D-111): it writes the .env file and the override files, logs in to the
// registry, runs up and checks the stack's routes, and goes on if the panel
// restarts.
func (o *RemoteOrchestrator) composeUp(ctx context.Context, deploymentID, serviceID, serviceName, release, projectDir, composeFile string, envVars map[string]string, route *deployment.ComposeRoute, nets deployment.ComposeNetworks, limits map[string]deployment.ComposeLimit, auth *deployment.RegistryAuth, hooks rollout.Hooks) error {
	logFn := hooks.Log
	onAgent := o.agent && hooks.HandOff != nil
	projectName := rollout.ComposeProjectName(serviceID, serviceName)

	// The project directory's .env is what compose interpolates ${VAR} with
	// (docker compose v2.24+ no longer uses --env-file for interpolation).
	// It replaces any .env from the repository; for a repository deploy the
	// pipeline already wrote the same file into the clone before the policy
	// check (composespec.StageDotEnv), so the check read it too.
	serviceEnv, dropped := composespec.ServiceEnv(envVars)
	logDroppedEnv(dropped, logFn)
	dotEnv, err := composespec.DotEnvFile(serviceEnv)
	if err != nil {
		return fmt.Errorf("write env file: %w", err)
	}
	if !onAgent {
		if err := o.uploadPrivate(ctx, projectDir+"/.env", dotEnv); err != nil {
			return fmt.Errorf("upload env file: %w", err)
		}
		defer o.cleanup(ctx, "rm -f "+shellEscape(projectDir+"/.env"))
	}

	if auth != nil && auth.Password != "" && !onAgent {
		logFn(fmt.Sprintf("Logging in to registry %s...", auth.Server))
		username := auth.Username
		if username == "" {
			username = "token"
		}
		logout, err := o.registryLogin(ctx, auth.Server, username, auth.Password, logFn)
		if err != nil {
			return err
		}
		defer logout()
	}

	if !nets.Isolated {
		// Legacy: routed services join the control plane's network. A no-op
		// if it already exists.
		if err := o.host.ExecStream(ctx, "docker network create "+shellEscape(o.net.LegacyNetwork)+" 2>&1 || true", logFn); err != nil {
			logFn("warning: could not ensure " + o.net.LegacyNetwork + ": " + err.Error())
		}
	}

	overrides, err := composeOverrides(serviceID, o.net, route, nets, limits)
	if err != nil {
		return err
	}
	files := "-f " + shellEscape(composeFile)
	for _, ov := range overrides {
		overridePath, cleanup, err := o.uploadOverride(ctx, ov)
		if err != nil {
			return err
		}
		defer cleanup()
		files += " -f " + shellEscape(overridePath)
	}

	// Take down a stack still running under a legacy project name (the
	// service was renamed); down works by project name, so the routing
	// override is not needed. The current project is updated in place: up
	// recreates only the containers whose image or configuration changed, so
	// a database in the stack keeps running. Its output isn't logged (as
	// the local RunCompose): on an agent's server no .env is on disk, so
	// loading the file warns of every variable it names.
	for _, proj := range rollout.ComposeProjectNames(serviceID, serviceName) {
		if proj == projectName {
			continue
		}
		_, _ = o.host.Exec(ctx, remoteCompose(fmt.Sprintf(
			"--project-directory %s -f %s --project-name %s down --remove-orphans 2>&1 || true",
			shellEscape(projectDir), shellEscape(composeFile), shellEscape(proj),
		)))
	}

	allowed := routeLabelKeys(serviceID, nets.Home, route)
	if route != nil {
		// Refuse an image that routes itself before up replaces anything:
		// pull the routed service's image and read its labels. A service
		// built during up is left to the check after it.
		out, err := o.host.Exec(ctx, remoteCompose(fmt.Sprintf(
			"--project-directory %s %s --project-name %s config --images %s 2>/dev/null",
			shellEscape(projectDir), files, shellEscape(projectName), shellEscape(route.Service))))
		if image := singleImage(out); err == nil && image != "" {
			if _, err := o.host.Exec(ctx, "docker pull --quiet "+shellEscape(image)+" >/dev/null 2>&1"); err == nil {
				if dc, err := o.host.DockerClient(); err == nil {
					keys, ok := routedImageViolations(ctx, dc, image, allowed)
					dc.Close()
					if ok && len(keys) > 0 {
						err := routedImageError(route.Service, image, keys)
						logFn("✗ " + err.Error())
						return err
					}
				}
			}
		}
	}

	if onAgent {
		ovs := make([]rollout.ComposeOverride, 0, len(overrides))
		for _, ov := range overrides {
			ovs = append(ovs, rollout.ComposeOverride{Name: ov.name, Content: ov.content})
		}
		logFn(fmt.Sprintf("Running docker compose up on the server's agent... (%d vars)", len(serviceEnv)))
		res := o.handOff(ctx, rollout.Request{Compose: &rollout.ComposeUp{
			DeploymentID: deploymentID, ServiceID: serviceID, ServiceName: serviceName,
			ProjectDir: projectDir, ComposeFile: composeFile, Overrides: ovs, DotEnv: dotEnv, Auth: auth,
			RoutedService: routedService(route), AllowedLabels: allowed,
		}}, hooks)
		if res.Step != "" {
			return errors.New(res.Err)
		}
		o.pruneComposeReleases(ctx, serviceID, release)
		return nil
	}

	logFn(fmt.Sprintf("Running docker compose up... (%d vars)", len(serviceEnv)))
	dc, err := o.host.DockerClient()
	if err != nil {
		return fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	up := func(flags string) error {
		return o.host.ExecStream(ctx, remoteCompose(fmt.Sprintf(
			"--project-directory %s %s --project-name %s up %s --remove-orphans 2>&1",
			shellEscape(projectDir), files, shellEscape(projectName), flags,
		)), logFn)
	}
	// The stack's routes are checked on its created containers, which
	// carry their images' labels, before any starts, and again after up,
	// failed or not: a container up started before failing must not keep
	// routes of its own.
	check := func() error {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if err := rollout.CheckStackRoutes(cctx, dc, projectName, routedService(route), allowed); err != nil {
			logFn("✗ " + err.Error())
			o.cleanup(ctx, remoteCompose(fmt.Sprintf("--project-directory %s -f %s --project-name %s down --remove-orphans 2>&1",
				shellEscape(projectDir), shellEscape(composeFile), shellEscape(projectName))))
			return err
		}
		return nil
	}
	// A failed create (a pull or a build) started nothing and recreated
	// nothing yet: the running stack is untouched, and checking its old
	// containers against the new routes would take it down.
	if err := up("--no-start --pull always"); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	// Nothing pulled, built or recreated: what starts is what was checked.
	upErr := up("-d --pull never --no-build --no-recreate")
	if err := check(); err != nil {
		return err
	}
	if upErr != nil {
		return upErr
	}
	o.pruneComposeReleases(ctx, serviceID, release)
	return nil
}

// pruneComposeReleases removes the service's releases other than release:
// every container that mounted a file from one was just recreated onto it.
func (o *RemoteOrchestrator) pruneComposeReleases(ctx context.Context, serviceID, release string) {
	o.cleanup(ctx, fmt.Sprintf("find %s -mindepth 1 -maxdepth 1 ! -path %s -exec rm -rf {} +",
		shellEscape(rollout.ComposeDir(serviceID)), shellEscape(release)))
}

// uploadPrivate writes content to remotePath, readable by its owner only,
// through the SSH session's stdin (not the command line, where a process
// listing would show it). The path is shell-quoted.
func (o *RemoteOrchestrator) uploadPrivate(ctx context.Context, remotePath string, content []byte) error {
	quoted := shellEscape(remotePath)
	// rm -f first: a symlink left at the path (from an uploaded repository)
	// is removed, not followed.
	if _, err := o.host.Exec(ctx, "umask 077 && rm -f "+quoted+" && : > "+quoted); err != nil {
		return err
	}
	if err := o.host.Upload(ctx, quoted, content); err != nil {
		return err
	}
	_, err := o.host.Exec(ctx, "chmod 600 "+quoted)
	return err
}

// uploadOverride writes a platform override file into a private random
// temp directory of its own on the remote (never inside the uploaded
// repository) and returns the file and a cleanup that removes the directory.
func (o *RemoteOrchestrator) uploadOverride(ctx context.Context, ov overrideFile) (string, func(), error) {
	dir, err := o.host.Exec(ctx, "mktemp -d /tmp/paas-override-XXXXXX")
	if err != nil {
		return "", nil, fmt.Errorf("create remote temp dir for %s: %w", ov.name, err)
	}
	dir = strings.TrimSpace(dir)
	cleanup := func() { o.cleanup(ctx, "rm -rf "+shellEscape(dir)) }
	file := dir + "/" + ov.name
	if err := o.uploadPrivate(ctx, file, ov.content); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("upload %s: %w", ov.name, err)
	}
	return file, cleanup, nil
}

// tarDir creates an in-memory .tar.gz of srcDir, skipping .git directories.
func tarDir(srcDir string) ([]byte, error) {
	var buf strings.Builder
	_ = buf // use bytes.Buffer instead
	var byteBuf strings.Builder
	_ = byteBuf

	pr, pw := io.Pipe()
	gw := gzip.NewWriter(pw)
	tw := tar.NewWriter(gw)

	errCh := make(chan error, 1)
	go func() {
		err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			// Skip .git directory entirely.
			if info.IsDir() && info.Name() == ".git" {
				return filepath.SkipDir
			}
			relPath, err := filepath.Rel(srcDir, path)
			if err != nil {
				return err
			}
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = relPath
			if err := tw.WriteHeader(header); err != nil {
				return err
			}
			if !info.IsDir() {
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(tw, f)
				return err
			}
			return nil
		})
		tw.Close()
		gw.Close()
		pw.CloseWithError(err)
		errCh <- err
	}()

	data, readErr := io.ReadAll(pr)
	walkErr := <-errCh
	if walkErr != nil {
		return nil, walkErr
	}
	if readErr != nil {
		return nil, readErr
	}
	return data, nil
}

// StopCompose stops all containers in the compose stack on the remote host.
// Tries `docker compose down` for every known project name variant, then uses
// the Docker SDK to hard-stop any containers still carrying our compose project label.
func (o *RemoteOrchestrator) StopCompose(ctx context.Context, serviceID, serviceName, _ string) error {
	tmpDir, err := o.host.Exec(ctx, "mktemp -d /tmp/paas-compose-XXXXXX")
	if err != nil {
		return fmt.Errorf("create remote temp dir: %w", err)
	}
	tmpDir = strings.TrimSpace(tmpDir)
	defer o.cleanup(ctx, "rm -rf "+tmpDir)
	// The stack's containers are removed below, so its files can go too.
	defer o.cleanup(ctx, "rm -rf "+shellEscape(rollout.ComposeDir(serviceID)))

	// Try compose down for every known project name variant, in the same
	// clean environment as a deploy, by project name from an empty directory
	// (as the local StopCompose).
	for _, proj := range rollout.ComposeProjectNames(serviceID, serviceName) {
		o.host.Exec(ctx, remoteCompose(fmt.Sprintf( //nolint:errcheck — handled by SDK fallback
			"--project-directory %s --project-name %s down --remove-orphans 2>&1 || true",
			shellEscape(tmpDir), shellEscape(proj),
		)))
	}

	// SDK fallback: stop & remove any containers still labelled with our project names.
	dc, err := o.host.DockerClient()
	if err != nil {
		return nil // best-effort; SSH Docker client unavailable
	}
	defer dc.Close()

	for _, proj := range rollout.ComposeProjectNames(serviceID, serviceName) {
		list, err := dc.ContainerList(ctx, container.ListOptions{
			All:     true,
			Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project="+proj)),
		})
		if err != nil {
			continue
		}
		for _, c := range list {
			timeout := 10
			_ = dc.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
			_ = dc.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
		}
	}
	return nil
}

// ExecInContainer runs a command inside a running container on the remote host, streaming output to logFn.
// Always uses `docker exec` with the container name directly.
func (o *RemoteOrchestrator) ExecInContainer(ctx context.Context, serviceID, containerName, command string, logFn func(string)) error {
	sshCmd := fmt.Sprintf("docker exec -i %s sh -c %s 2>&1", shellEscape(containerName), shellEscape(command))
	return o.host.ExecStream(ctx, sshCmd, logFn)
}

// ListContainers returns the names of the service's containers on the
// remote host, running or stopped: its blue/green slots and the containers
// of its compose project (listServiceContainerNames).
func (o *RemoteOrchestrator) ListContainers(ctx context.Context, serviceID, serviceName string) ([]string, error) {
	dc, err := o.host.DockerClient()
	if err != nil {
		return nil, fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return listServiceContainerNames(ctx, dc, serviceID, serviceName)
}

// ExecCommands runs each command on the remote host via SSH ExecStream, streaming output to logFn.
// Stops on first failure.
func (o *RemoteOrchestrator) ExecCommands(ctx context.Context, commands []string, logFn func(string)) error {
	for _, cmdStr := range commands {
		if strings.TrimSpace(cmdStr) == "" {
			continue
		}
		logFn("$ " + cmdStr)
		if err := o.host.ExecStream(ctx, cmdStr+" 2>&1", logFn); err != nil {
			return fmt.Errorf("command %q: %w", cmdStr, err)
		}
	}
	return nil
}

// Stats fetches a one-shot resource snapshot from the remote container via SSH.
// containerName overrides auto-resolution; pass "" to let the orchestrator find the active container.
func (o *RemoteOrchestrator) Stats(ctx context.Context, serviceID string, containerName string) (*service.ContainerStats, error) {
	if containerName == "" {
		containerName = remoteActiveContainerName(ctx, o.host, serviceID, "")
		if containerName == "" {
			return nil, fmt.Errorf("no running container found for service %s", serviceID)
		}
	}
	name := shellEscape(containerName)
	out, err := o.host.Exec(ctx, fmt.Sprintf(
		"docker stats --no-stream --format '{{.CPUPerc}} {{.MemUsage}}' %s && docker inspect --format '{{.State.StartedAt}} {{.RestartCount}}' %s",
		name, name,
	))
	if err != nil {
		return nil, fmt.Errorf("remote stats: %w", err)
	}
	usage, state, _ := strings.Cut(strings.TrimSpace(out), "\n")
	st, err := parseRemoteStats(strings.TrimSpace(usage))
	if err != nil {
		return nil, err
	}
	if f := strings.Fields(state); len(f) == 2 {
		st.StartedAt = startedAt(f[0])
		st.RestartCount, _ = strconv.Atoi(f[1])
	}
	return st, nil
}

// parseRemoteStats parses "0.10% 45.2MiB / 1.95GiB" from `docker stats --format`.
func parseRemoteStats(line string) (*service.ContainerStats, error) {
	parts := strings.Fields(line)
	if len(parts) < 4 {
		return nil, fmt.Errorf("unexpected stats output: %q", line)
	}
	cpuStr := strings.TrimSuffix(parts[0], "%")
	cpu, err := strconv.ParseFloat(cpuStr, 64)
	if err != nil {
		return nil, fmt.Errorf("parse cpu: %w", err)
	}
	memUsed, err := parseDockerSize(parts[1])
	if err != nil {
		return nil, fmt.Errorf("parse mem usage: %w", err)
	}
	memLimit, err := parseDockerSize(parts[3])
	if err != nil {
		return nil, fmt.Errorf("parse mem limit: %w", err)
	}
	return &service.ContainerStats{
		CPUPercent:    cpu,
		MemoryMB:      memUsed,
		MemoryLimitMB: memLimit,
	}, nil
}

// ListVolumes inspects each named volume on the remote Docker host via SSH-tunnelled Docker SDK.
func (o *RemoteOrchestrator) ListVolumes(ctx context.Context, volumeNames []string) ([]service.VolumeInfo, error) {
	dc, err := o.host.DockerClient()
	if err != nil {
		return nil, fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()

	result := make([]service.VolumeInfo, 0, len(volumeNames))
	for _, name := range volumeNames {
		parts := strings.SplitN(name, "-", 4) // ["paas", "vol", "{id8}", "{mountName}"]
		mountName := ""
		if len(parts) == 4 {
			mountName = parts[3]
		}
		info := service.VolumeInfo{Name: name, MountName: mountName}
		if _, err := dc.VolumeInspect(ctx, name); err == nil {
			info.Exists = true
		}
		result = append(result, info)
	}
	return result, nil
}

// VolumeSizes returns the size of each named Docker volume that exists on
// the remote host.
func (o *RemoteOrchestrator) VolumeSizes(ctx context.Context, names []string) (map[string]int64, error) {
	dc, err := o.host.DockerClient()
	if err != nil {
		return nil, fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return volumeSizes(ctx, dc, names)
}

// PruneVolume removes a named Docker volume from the remote host via SSH-tunnelled Docker SDK.
func (o *RemoteOrchestrator) PruneVolume(ctx context.Context, volumeName string) error {
	dc, err := o.host.DockerClient()
	if err != nil {
		return fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()
	return dc.VolumeRemove(ctx, volumeName, false)
}

// DiscoverVolumes inspects running containers for a service on the remote host and returns all mounts.
// Used for services created before the volumes feature where svc.Volumes is empty in DB.
func (o *RemoteOrchestrator) DiscoverVolumes(ctx context.Context, serviceID string) ([]service.VolumeInfo, error) {
	dc, err := o.host.DockerClient()
	if err != nil {
		return nil, fmt.Errorf("SSH Docker client: %w", err)
	}
	defer dc.Close()

	return discoverVolumes(ctx, dc, serviceID)
}

// parseDockerSize converts Docker size strings like "45.2MiB" or "1.95GiB" to MB.
func parseDockerSize(s string) (float64, error) {
	units := []struct {
		suffix string
		mb     float64
	}{
		{"TiB", 1024 * 1024}, {"GiB", 1024}, {"MiB", 1}, {"KiB", 1.0 / 1024},
		{"TB", 1024 * 1024}, {"GB", 1024}, {"MB", 1}, {"kB", 1.0 / 1024}, {"B", 1.0 / (1024 * 1024)},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			if err != nil {
				return 0, err
			}
			return n * u.mb, nil
		}
	}
	return 0, fmt.Errorf("unrecognized size: %q", s)
}

// registryFromImageRef extracts the registry hostname from an image reference.
// "ghcr.io/org/repo:tag" → "ghcr.io", "nginx:latest" → "" (Docker Hub, no login needed).
func registryFromImageRef(imageRef string) string {
	// Strip tag/digest suffix first.
	ref := strings.SplitN(imageRef, "@", 2)[0]
	ref = strings.SplitN(ref, ":", 2)[0]
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) < 2 {
		return "" // bare image name like "nginx" — Docker Hub
	}
	host := parts[0]
	// Docker Hub image refs like "library/nginx" or "user/repo" have no dot or colon in the host part.
	if strings.ContainsAny(host, ".:") || host == "localhost" {
		return host
	}
	return "" // Docker Hub shorthand e.g. "org/repo"
}
