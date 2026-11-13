package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/rollout"
)

// dockerBin is the docker CLI compose runs through.
var dockerBin = "docker"

// stepComposeAgain is a compose up run again after the agent restarted.
const stepComposeAgain = rollout.StepComposeUp + "-again"

var overrideNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// checkComposeUp keeps a compose up inside its service's releases: the
// agent writes .env into ProjectDir.
func checkComposeUp(c rollout.ComposeUp) error {
	if !rolloutIDRe.MatchString(c.ServiceID) {
		return fmt.Errorf("%q is not a service ID", c.ServiceID)
	}
	base, err := filepath.EvalSymlinks(rollout.ComposeDir(c.ServiceID))
	if err != nil {
		return fmt.Errorf("the service has no releases on this server: %w", err)
	}
	for _, p := range []string{c.ProjectDir, c.ComposeFile} {
		if real, err := filepath.EvalSymlinks(p); err != nil || !strings.HasPrefix(real, base+"/") {
			return fmt.Errorf("%q is not in the service's releases", p)
		}
	}
	for _, ov := range c.Overrides {
		if !overrideNameRe.MatchString(ov.Name) {
			return fmt.Errorf("%q is not an override file's name", ov.Name)
		}
	}
	return nil
}

// composeUp runs a compose deploy's docker compose up (D-111), its output
// in the rollout's log. The agent's unit stops only the agent
// (KillMode=process), so an up outlives a restart: a resumed one waits for
// it, then runs up once more, which is safe to repeat. A second restart
// fails the deploy.
func (m *rollouts) composeUp(ctx context.Context, id string, c rollout.ComposeUp, rec *record) rollout.Result {
	project := rollout.ComposeProjectName(c.ServiceID, c.ServiceName)
	fail := func(msg string) rollout.Result {
		appendLogLine(id, "✗ "+msg)
		return rollout.Result{Step: rollout.StepComposeUp, Err: msg, Changed: true}
	}
	dir := filepath.Join(rolloutDir(), id+".d")
	dotEnv := filepath.Join(c.ProjectDir, ".env")
	// The containers hold their environment: the secrets don't stay on disk.
	defer os.RemoveAll(dir)
	defer os.Remove(dotEnv)

	switch rec.Progress.Step {
	case stepComposeAgain:
		waitForUp(ctx, rec.PID, project)
		return fail("the agent restarted twice during docker compose up: check the stack, then deploy again")
	case rollout.StepComposeUp:
		appendLogLine(id, "↻ The agent restarted during docker compose up; running it again once the first one ends")
		waitForUp(ctx, rec.PID, project)
		rec.Progress.Step = stepComposeAgain
	default:
		rec.Progress.Step = rollout.StepComposeUp
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail("prepare docker compose: " + err.Error())
	}
	if err := writeNew(dotEnv, c.DotEnv); err != nil {
		return fail("write the env file: " + err.Error())
	}
	args := []string{"compose", "--project-directory", c.ProjectDir, "-f", c.ComposeFile}
	for _, ov := range c.Overrides {
		p := filepath.Join(dir, ov.Name)
		if err := writeNew(p, ov.Content); err != nil {
			return fail("write " + ov.Name + ": " + err.Error())
		}
		args = append(args, "-f", p)
	}
	args = append(args, "--project-name", project)

	// Only the platform variables the policy expects (composespec.RunEnv),
	// and the registry's credentials in a config of the deploy's own.
	env := make([]string, 0, len(composespec.RunEnv)+1)
	for _, k := range composespec.RunEnv {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	if c.Auth != nil && c.Auth.Password != "" {
		cfg, err := dockerConfig(dir, *c.Auth)
		if err != nil {
			return fail("write the registry credentials: " + err.Error())
		}
		env = append(env, "DOCKER_CONFIG="+cfg)
	}

	logf, err := os.OpenFile(logFile(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fail("open the log: " + err.Error())
	}
	defer logf.Close()
	// run runs one docker compose command of the deploy, its PID on record
	// so a restart waits for it (waitForUp).
	run := func(extra ...string) error {
		cmd := exec.CommandContext(ctx, dockerBin, append(slices.Clone(args), extra...)...)
		cmd.Env, cmd.Stdout, cmd.Stderr = env, logf, logf
		// At the deploy's time limit compose gets to stop what it started.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 30 * time.Second
		// The step goes on record before up starts, so a restart from here on
		// counts this run; its PID follows once it has one.
		rec.PID = 0
		if err := writeRecord(*rec); err != nil {
			return errors.New("record the compose run: " + err.Error())
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		rec.PID = cmd.Process.Pid
		if err := writeRecord(*rec); err != nil {
			appendLogLine(id, "warning: could not record the compose run: "+err.Error())
		}
		// compose writes the log itself: attached panels look every half second.
		done := make(chan struct{})
		go func() {
			t := time.NewTicker(500 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					m.notify(id, false)
				}
			}
		}()
		err := cmd.Wait()
		close(done)
		return err
	}
	// The stack's routes are checked on its created containers, which
	// carry their images' labels, before any starts, and again after up,
	// failed or not: a container up started before failing must not keep
	// routes of its own.
	check := func() error {
		if err := rollout.CheckStackRoutes(context.Background(), m.dc, project, c.RoutedService, c.AllowedLabels); err != nil {
			down := exec.Command(dockerBin, append(args, "down", "--remove-orphans")...)
			down.Env, down.Stdout, down.Stderr = env, logf, logf
			if derr := down.Run(); derr != nil {
				appendLogLine(id, "warning: docker compose down: "+derr.Error())
			}
			return err
		}
		return nil
	}
	// A failed create (a pull or a build) started nothing and recreated
	// nothing yet: the running stack is untouched, and checking its old
	// containers against the new routes would take it down.
	if err := run("up", "--no-start", "--pull", "always", "--remove-orphans"); err != nil {
		return fail("docker compose up: " + err.Error())
	}
	if err := check(); err != nil {
		return fail(err.Error())
	}
	// Nothing pulled, built or recreated: what starts is what was checked.
	upErr := run("up", "-d", "--pull", "never", "--no-build", "--no-recreate", "--remove-orphans")
	if err := check(); err != nil {
		return fail(err.Error())
	}
	if upErr != nil {
		return fail("docker compose up: " + upErr.Error())
	}
	return rollout.Result{Changed: true}
}

// waitForUp waits for the docker compose up a restart left running: pid,
// while it still runs compose for project. At the deploy's time limit
// (ctx) it stops it as composeUp would.
func waitForUp(ctx context.Context, pid int, project string) {
	var kill <-chan time.Time
	for pid > 0 {
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil || !bytes.Contains(cmdline, []byte(project)) {
			return
		}
		select {
		case <-ctx.Done():
			// As cmd.Cancel and WaitDelay end a run of the agent's own.
			_ = syscall.Kill(pid, syscall.SIGTERM)
			ctx = context.Background() // then wait for it to end
			kill = time.After(30 * time.Second)
		case <-kill:
			_ = syscall.Kill(pid, syscall.SIGKILL)
		case <-time.After(time.Second):
		}
	}
}

// dockerConfig writes a docker CLI config in dir with the registry's
// credentials, so they never reach the host's own; the CLI plugins
// (compose among them) are linked from the host's.
func dockerConfig(dir string, a rollout.RegistryAuth) (string, error) {
	server, user := a.Server, a.Username
	if server == "" {
		server = "https://index.docker.io/v1/"
	}
	if user == "" {
		user = "token"
	}
	raw, err := json.Marshal(map[string]any{"auths": map[string]any{
		server: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(user + ":" + a.Password))},
	}})
	if err != nil {
		return "", err
	}
	cfg := filepath.Join(dir, "docker")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		return "", err
	}
	if err := writeNew(filepath.Join(cfg, "config.json"), raw); err != nil {
		return "", err
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/root"
	}
	_ = os.Symlink(filepath.Join(home, ".docker", "cli-plugins"), filepath.Join(cfg, "cli-plugins"))
	return cfg, nil
}

// writeNew writes data to a new file at path, readable by its owner only,
// replacing (never following) whatever is there.
func writeNew(path string, data []byte) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
