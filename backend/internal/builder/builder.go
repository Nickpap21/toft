// Package builder is Toft's own BuildKit builder on a server (D-170) and
// the one way the backend and the agent run a build or a compose up: Run.
// Its test fails on a build command written anywhere else.
package builder

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// lock.sh is the builder's lock on a host; ensure.sh, after it, makes the
// builder on the host it runs on, before every build.
var (
	//go:embed lock.sh
	lockSh string
	//go:embed ensure.sh
	ensureSh string

	ensure = lockSh + ensureSh
	// held runs its arguments, a build, holding the lock with the other
	// builds: the builder isn't replaced under it.
	// The lock's variables stop at the shell: compose reads a stack with
	// its own environment, which holds nothing the deploy policy didn't.
	held = lockSh + `lock -s "This server's builder is being made or replaced. Deploy again."` +
		"\nunset TOFT_BUILDER_LOCK TOFT_BUILDER_WAIT\nexec \"$@\"\n"
)

// LockDir is where a host keeps its builders' locks. /run is root's, so no
// other login of the host can put something else at the path, and it is
// empty after a restart. A test, which may not be root, sets a folder of
// its own.
var LockDir = "/run/toft"

// lockEnv is the lock every build on s and every check of it takes, and
// how long each waits for it.
func lockEnv(s Spec) []string {
	return []string{"TOFT_BUILDER_LOCK=" + LockDir + "/" + s.Name + ".lock", "TOFT_BUILDER_WAIT=300"}
}

// Image is the builder's BuildKit, moby/buildkit v0.35.0 as of 2026-11-18;
// scripts/buildkit-image.sh fails in CI when a newer release is out.
const Image = "moby/buildkit:v0.35.0@sha256:aa632b921ef07411d2cb77292cd34213d4f7b384001d6fceef1e1f25bf9752bb"

// cache is the most a builder's cache may use: a tenth of the disk it is on.
const cache = "10%"

// Spec is a builder: its name, which is its network's too, and the
// network's bridge. A bridge named toft-u… is of the host firewall's
// untrusted class (server/firewall.nft), which is what keeps a build from
// the host and from private addresses.
type Spec struct {
	Name, Bridge string
	// Memory is the builder's limit in bytes; 0 is half of the host's.
	Memory int64
	// Image is the builder's BuildKit and Cache the most its cache may
	// use; empty, they are this build's own (Full). The panel sends both
	// with a rollout: a server has one builder, which the panel's builds
	// there and its agent's must want the same, whatever the agent's
	// version.
	Image, Cache string
	// Cgroup is the builder's cgroup parent: the apps' slice or the
	// builders' on the panel's host, "" for Docker's own.
	Cgroup string
	// Firewall: the builder is a deploy's, and Run builds on it once the
	// host says its firewall is loaded (firewallCheck). Without it Run
	// builds nothing, so the zero Spec builds nothing.
	Firewall bool
	// Unchecked: Run doesn't ask the host for its firewall
	// (TOFT_DEV_BUILDS_WITHOUT_FIREWALL). The panel's own host only: an
	// agent refuses it from a panel (Check).
	Unchecked bool
}

// FirewallChain reads the chain of the host's firewall (server/firewall.nft)
// that keeps a toft-u bridge off private addresses and off the host, and
// fails where it isn't loaded. A server's probe reads the same.
const FirewallChain = "nft list chain ip toft from_untrusted"

// firewallCheck is what Run asks a host before every build.
const firewallCheck = FirewallChain + " >/dev/null 2>&1 && echo loaded || echo missing"

var (
	// ErrNoFirewall is Run's refusal on a host whose firewall isn't loaded,
	// and on a Spec that is no deploy's.
	ErrNoFirewall = errors.New("this server's firewall doesn't keep builds off private addresses: Toft's rules aren't loaded on it, so nothing is built here. " +
		"An instance operator provisions the server again from its page (the panel's own server: run toft upgrade); then deploy again")
	// ErrFirewallUnread is Run's refusal on a host that didn't say whether
	// its firewall is loaded.
	ErrFirewallUnread = errors.New("this server didn't say whether Toft's firewall is loaded on it, so nothing is built here. Deploy again")
)

// Full is s with this build's own BuildKit and cache limit where it names
// none.
func (s Spec) Full() Spec {
	if s.Image == "" {
		s.Image = Image
	}
	if s.Cache == "" {
		s.Cache = cache
	}
	return s
}

var (
	pinnedImage = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	cacheSize   = regexp.MustCompile(`^[0-9]+(%|[KMGT]?B)?$`)
)

// Check is nil when s is a builder an agent takes from the panel: one of
// Toft's two by name and bridge, a BuildKit pinned by digest, and a cache
// limit that is a size.
func (s Spec) Check() error {
	switch {
	case (s.Name != Trusted.Name || s.Bridge != Trusted.Bridge) && (s.Name != Untrusted.Name || s.Bridge != Untrusted.Bridge):
		return fmt.Errorf("no builder of Toft's is %q on the bridge %q", s.Name, s.Bridge)
	case s.Memory < 0:
		return errors.New("a memory limit below zero")
	case s.Cgroup != "":
		return fmt.Errorf("the cgroup parent %q: only the panel's host has one", s.Cgroup)
	case s.Unchecked:
		return errors.New("a build that doesn't ask the firewall: only the panel's host has one")
	case s.Image != "" && !pinnedImage.MatchString(s.Image):
		return fmt.Errorf("the image %q isn't pinned by digest", s.Image)
	case s.Cache != "" && !cacheSize.MatchString(s.Cache):
		return fmt.Errorf("the cache limit %q isn't a size", s.Cache)
	}
	return nil
}

var (
	// Trusted is the builder every build runs on.
	Trusted = Spec{Name: "toft-build", Bridge: "toft-ubuild"}
	// Untrusted is the builder of untrusted previews: a network of its own,
	// which an operator's list of private addresses never opens.
	Untrusted = Spec{Name: "toft-build-untrusted", Bridge: "toft-upbuild"}
)

// Container is the builder's BuildKit container, as buildx names it
// (ensure.sh's ctr); its cache is in the volume of that name with _state.
func (s Spec) Container() string { return "buildx_buildkit_" + s.Name + "0" }

// Host is where a build runs: the panel's host, a server, or the agent's
// own. env is added to the clean environment the host gives a command.
type Host interface {
	// Output runs name with args and returns what it wrote to stdout. When
	// it fails, the error is what it wrote to stderr.
	Output(ctx context.Context, env []string, name string, args ...string) (string, error)
	// Stream runs name with args, its output in the deploy's log.
	Stream(ctx context.Context, env []string, name string, args ...string) error
	// OnHost runs script, one of Toft's own, with sh as root in the host's
	// own namespaces, and returns what it printed. On the panel's host that
	// is not where Output and Stream run: they run in the backend's
	// container.
	OnHost(ctx context.Context, script string) (string, error)
}

// Command is what Run runs on a builder: a Build or an Up.
type Command struct {
	compose bool
	args    []string
}

// Build is a docker build with args: the tag, the Dockerfile, the build
// arguments and the context.
func Build(args ...string) Command { return Command{args: args} }

// Up is `docker compose <project> up <flags>`, project being compose's own
// arguments: the project's folder, files and name.
func Up(project []string, flags ...string) Command {
	return Command{compose: true, args: upArgs(project, flags)}
}

func upArgs(project, flags []string) []string {
	return slices.Concat([]string{"compose"}, project, []string{"up"}, flags)
}

// Run runs c on the builder s, on h, and nothing where the host's firewall
// isn't loaded now (ErrNoFirewall) or the host doesn't say
// (ErrFirewallUnread): it asks before every build, whatever a probe of the
// server once read. ensure.sh runs next, there, with
// config, the command's DOCKER_CONFIG, which gets the builder's
// definition; when it refuses, nothing else runs and the error is its own
// words. log gets what ensure.sh says for the deploy's log. c then runs
// under the host's builder lock (held), which the shell that takes it
// hands to docker by becoming it.
func Run(ctx context.Context, h Host, s Spec, config string, c Command, log func(string)) error {
	if !s.Firewall {
		return ErrNoFirewall
	}
	if !s.Unchecked {
		actx, cancel := context.WithTimeout(ctx, time.Minute)
		out, err := h.OnHost(actx, firewallCheck)
		cancel()
		if err != nil {
			return fmt.Errorf("%w (%w)", ErrFirewallUnread, err)
		}
		if strings.TrimSpace(out) != "loaded" {
			return ErrNoFirewall
		}
	}
	s = s.Full()
	memory := ""
	if s.Memory > 0 {
		memory = strconv.FormatInt(s.Memory, 10)
	}
	// Every variable the script reads, set or empty: none is left to the
	// environment the host runs it in.
	out, err := h.Output(ctx, slices.Concat([]string{
		"DOCKER_CONFIG=" + config,
		"TOFT_BUILDER=" + s.Name,
		"TOFT_BUILDER_BRIDGE=" + s.Bridge,
		"TOFT_BUILDER_IMAGE=" + s.Image,
		"TOFT_BUILDER_MEMORY=" + memory,
		"TOFT_BUILDER_CGROUP=" + s.Cgroup,
		"TOFT_BUILDER_CACHE=" + s.Cache,
	}, lockEnv(s)), "sh", "-c", ensure)
	for line := range strings.Lines(out) {
		if line = strings.TrimSpace(line); line != "" {
			log(line)
		}
	}
	if err != nil {
		return fmt.Errorf("the builder %s: %w", s.Name, err)
	}
	env := []string{"DOCKER_CONFIG=" + config, "BUILDX_BUILDER=" + s.Name, "DOCKER_BUILDKIT=1"}
	args := c.args
	if !c.compose {
		// BUILDX_GIT_INFO and BUILDX_GIT_LABELS: buildx doesn't run git in
		// the context, which is the repository's to shape.
		env = []string{"DOCKER_CONFIG=" + config, "BUILDX_GIT_INFO=false", "BUILDX_GIT_LABELS=false"}
		args = slices.Concat([]string{"buildx", "build", "--builder", s.Name, "--load"}, c.args)
	}
	return h.Stream(ctx, slices.Concat(env, lockEnv(s)), "sh", slices.Concat([]string{"-c", held, "sh", "docker"}, args)...)
}

// FirstUp runs a compose deploy's first up, the one that pulls and builds:
// `docker compose <project> up <flags>` with --build or --no-build, which
// only FirstUp writes. Whether the stack builds is the host's own reading
// of it (docker compose config, with the up's files, folder, name,
// environment and DOCKER_CONFIG), never the panel's: a build the panel
// missed would otherwise start an image an earlier deploy left there. A
// stack that builds goes through Run with --build, so it never starts
// that image either. One that doesn't runs with --no-build and without a
// builder, so it deploys on a server that has no buildx.
func FirstUp(ctx context.Context, h Host, s Spec, config string, project, flags []string, log func(string)) error {
	env := []string{"DOCKER_CONFIG=" + config}
	out, err := h.Output(ctx, env, "docker", slices.Concat([]string{"compose"}, project, []string{"config", "--format", "json"})...)
	if err != nil {
		return fmt.Errorf("docker compose config: %w", err)
	}
	var stack struct {
		Services map[string]struct {
			Build *json.RawMessage `json:"build"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(out), &stack); err != nil {
		return fmt.Errorf("read docker compose config: %w", err)
	}
	for _, svc := range stack.Services {
		if svc.Build != nil {
			return Run(ctx, h, s, config, Up(project, slices.Concat(flags, []string{"--build"})...), log)
		}
	}
	return h.Stream(ctx, env, "docker", upArgs(project, slices.Concat(flags, []string{"--no-build"}))...)
}
