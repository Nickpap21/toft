package docker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"golang.org/x/crypto/ssh"
)

// Where builder.Run runs a build's commands (builder.Host): the panel's
// host, and a server over SSH or its agent's tunnel.

// localHost runs them on the panel's host: each an argv, never a shell, so
// a tenant's values stay arguments, in the clean environment compose gets
// (localComposeEnv).
type localHost struct {
	dir string // the working directory; "" for a build
	log func(string)
	// onHost runs an argv on the host itself (LocalOrchestrator.RunOnHost):
	// the backend's container isn't in the host's namespaces.
	onHost func(ctx context.Context, argv []string) (string, error)
}

func (h localHost) command(ctx context.Context, env []string, name string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// Cancelled (a deleted service, the deploy's time limit, D-125), the
	// CLI gets to tell the builder to stop, and compose to stop what it
	// started, rather than being killed mid-way.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 30 * time.Second
	cmd.Dir = h.dir
	// A compose build invokes buildx, which otherwise reads git from the
	// build context — the repository's own working tree. Toft needs none of
	// it, and the context is the repository's to shape, so buildx runs no
	// git there.
	cmd.Env = append(localComposeEnv(), "BUILDX_GIT_INFO=false", "BUILDX_GIT_LABELS=false")
	cmd.Env = append(cmd.Env, env...)
	return cmd
}

func (h localHost) Output(ctx context.Context, env []string, name string, args ...string) (string, error) {
	out, err := h.command(ctx, env, name, args).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return string(out), fmt.Errorf("%s (%w)", strings.TrimSpace(string(exit.Stderr)), err)
	}
	return string(out), err
}

func (h localHost) Stream(ctx context.Context, env []string, name string, args ...string) error {
	return runStream(h.command(ctx, env, name, args), h.log)
}

// OnHost runs script through the host helper, as the panel's own server's
// probe does. A localHost without one reaches no host.
func (h localHost) OnHost(ctx context.Context, script string) (string, error) {
	if h.onHost == nil {
		return "", errors.New("no way to the panel's host")
	}
	return h.onHost(ctx, []string{"sh", "-c", script})
}

// sshHost runs them on a server, each as one shell command, every word
// that isn't plain quoted, in the environment compose gets there
// (remoteCleanEnv): nothing the login's profile exports chooses a build's
// Docker or its builder.
type sshHost struct {
	host remoteHost
	log  func(string)
}

func (h sshHost) command(env []string, name string, args []string) string {
	var b strings.Builder
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		b.WriteString(k + "=" + shellWord(v) + " ")
	}
	b.WriteString(name)
	for _, a := range args {
		b.WriteString(" " + shellWord(a))
	}
	return remoteCleanEnv(b.String())
}

// plainWord is a word the shell reads as it is written.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWord is s as one word of a shell command: quoted unless it is plain,
// so a command reads in a log as it would be typed.
func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return shellEscape(s)
}

// Output keeps what the command says on stderr out of its answer, and
// returns it as the error when the command fails.
func (h sshHost) Output(ctx context.Context, env []string, name string, args ...string) (string, error) {
	out, err := h.host.Exec(ctx, `e=$(mktemp) && { `+h.command(env, name, args)+` 2>"$e"; s=$?; [ $s = 0 ] || cat "$e"; rm -f "$e"; exit $s; }`)
	// The SSH client's own error quotes the whole command, which here can
	// be a script: an exit says what the command wrote instead.
	var exit *ssh.ExitError
	switch {
	case err == nil:
		return out, nil
	case ctx.Err() != nil:
		return "", ctx.Err()
	case errors.Is(err, sshpool.ErrConnectionLost):
		return "", fmt.Errorf("%s on the server: %w", name, sshpool.ErrConnectionLost)
	case errors.As(err, &exit):
		return "", fmt.Errorf("%s (exit status %d)", out, exit.ExitStatus())
	}
	return "", err
}

func (h sshHost) Stream(ctx context.Context, env []string, name string, args ...string) error {
	return h.host.ExecStream(ctx, h.command(env, name, args)+" 2>&1", h.log)
}

// OnHost runs script as the server's login, as its probe runs.
func (h sshHost) OnHost(ctx context.Context, script string) (string, error) {
	return h.host.Exec(ctx, script)
}
