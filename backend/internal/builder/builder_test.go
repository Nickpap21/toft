package builder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Nickpap21/toft/backend/internal/testenv"
)

// fakeHost records what Run asks of a host. output answers Output by the
// command's name.
type fakeHost struct {
	calls  []hostCall
	output func(name string) (string, error)
	// firewall answers OnHost (nil: the firewall is loaded); asked is the
	// scripts it was given.
	firewall func() (string, error)
	asked    []string
	// compose answers `docker compose version --short` (nil: 5.7.2).
	compose func() (string, error)
}

func (f *fakeHost) OnHost(_ context.Context, script string) (string, error) {
	f.asked = append(f.asked, script)
	if f.firewall == nil {
		return "loaded\n", nil
	}
	return f.firewall()
}

type hostCall struct {
	stream bool
	held   bool // under the builder's lock; argv is then what the shell becomes
	env    []string
	argv   []string
}

func (f *fakeHost) Output(_ context.Context, env []string, name string, args ...string) (string, error) {
	f.calls = append(f.calls, hostCall{env: env, argv: append([]string{name}, args...)})
	if slices.Equal(args, []string{"compose", "version", "--short"}) {
		if f.compose == nil {
			return "5.7.2\n", nil
		}
		return f.compose()
	}
	if f.output == nil {
		return "", nil
	}
	return f.output(name)
}

func (f *fakeHost) Stream(_ context.Context, env []string, name string, args ...string) error {
	call := hostCall{stream: true, env: env, argv: append([]string{name}, args...)}
	if len(args) > 3 && slices.Equal(call.argv[:4], []string{"sh", "-c", held, "sh"}) {
		call.held, call.argv = true, args[3:]
	}
	f.calls = append(f.calls, call)
	return nil
}

// testLock is the lock's variables as Run sets them for the builder name.
func testLock(name string) []string {
	return []string{"TOFT_BUILDER_LOCK=/run/toft/" + name + ".lock", "TOFT_BUILDER_WAIT=300"}
}

const testConfig = "/tmp/paas-docker-abc"

// walled is s as a deploy's.
func walled(s Spec) Spec {
	s.Firewall = true
	return s
}

// Before every build Run asks the host whether Toft's firewall is loaded
// there, and where it isn't (ErrNoFirewall), or the host doesn't say
// (ErrFirewallUnread), nothing is built and nothing runs for it, not even
// ensure.sh: a Dockerfile's build, and a stack's that builds. A stack that
// builds nothing goes up, unasked. A Spec that is no deploy's (the zero
// value, Trusted, Untrusted) builds nothing either. Only the development
// setting's Spec builds without asking.
func TestRunBuildsNothingWithoutTheFirewall(t *testing.T) {
	ctx := context.Background()
	commands := []Command{Build("-t", "paas/x:y", "/src"), Up([]string{"--project-name", "p"}, "--build")}
	down := errors.New("create the helper: cannot connect to the Docker daemon")
	for name, tc := range map[string]struct {
		answer string
		err    error
		want   error
	}{
		"the chain is missing":    {"missing\n", nil, ErrNoFirewall},
		"no answer":               {"", nil, ErrNoFirewall},
		"the host can't be asked": {"", down, ErrFirewallUnread},
		"loaded, then it failed":  {"loaded\n", down, ErrFirewallUnread},
	} {
		for _, c := range commands {
			h := &fakeHost{firewall: func() (string, error) { return tc.answer, tc.err }}
			err := Run(ctx, h, walled(Trusted), testConfig, c, func(string) {})
			require.ErrorIs(t, err, tc.want, name)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err, "%s: why is in the refusal", name)
			}
			assert.Empty(t, h.calls, name)
			assert.Equal(t, []string{firewallCheck}, h.asked, name)
		}
	}
	assert.Contains(t, ErrNoFirewall.Error(), "provisions the server again", "the refusal says what to do")
	assert.Equal(t, "nft list chain ip toft from_untrusted >/dev/null 2>&1 && echo loaded || echo missing", firewallCheck)

	for name, s := range map[string]Spec{"the zero value": {}, "trusted": Trusted, "untrusted": Untrusted,
		"with limits": {Name: "toft-build", Bridge: "toft-ubuild", Memory: 1 << 30}, "unchecked": {Name: "toft-build", Bridge: "toft-ubuild", Unchecked: true}} {
		for _, c := range commands {
			h := &fakeHost{}
			require.ErrorIs(t, Run(ctx, h, s, testConfig, c, func(string) {}), ErrNoFirewall, name)
			assert.Empty(t, h.calls, name)
		}
	}

	missing := func() (string, error) { return "missing\n", nil }
	dev := walled(Trusted)
	dev.Unchecked = true
	h := &fakeHost{firewall: missing}
	require.NoError(t, Run(ctx, h, dev, testConfig, commands[0], func(string) {}))
	assert.Empty(t, h.asked, "the development setting asks no firewall")
	assert.Len(t, h.calls, 2, "ensure.sh, then the build")

	stack := func(config string) *fakeHost {
		return &fakeHost{firewall: missing, output: func(string) (string, error) { return config, nil }}
	}
	h = stack(`{"services":{"web":{"build":{"context":"/r"}}}}`)
	require.ErrorIs(t, FirstUp(ctx, h, walled(Trusted), testConfig, []string{"--project-name", "p"}, nil, func(string) {}), ErrNoFirewall)
	require.Len(t, h.calls, 2, "the host's reading of the stack and its compose's version, and no up")
	h = stack(`{"services":{"web":{"image":"example/web"}}}`)
	require.NoError(t, FirstUp(ctx, h, walled(Trusted), testConfig, []string{"--project-name", "p"}, nil, func(string) {}))
	assert.Equal(t, []string{"docker", "compose", "--project-name", "p", "up", "--no-build"}, h.calls[1].argv)
	assert.Empty(t, h.asked, "a stack that builds nothing")
}

// What Run asks, as a shell runs it: loaded only where nft lists the chain.
// A host without the chain, and one without nft, are both missing.
func TestTheFirewallCheckReadsTheChain(t *testing.T) {
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	ask := func(nft string) string {
		dir := t.TempDir()
		if nft != "" {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "nft"), []byte("#!/bin/sh\necho \"$*\" > "+dir+"/asked\n"+nft+"\n"), 0o700))
		}
		cmd := exec.Command(sh, "-c", firewallCheck)
		cmd.Env = []string{"PATH=" + dir}
		out, err := cmd.Output()
		require.NoError(t, err)
		if nft != "" {
			asked, _ := os.ReadFile(filepath.Join(dir, "asked"))
			assert.Equal(t, "list chain ip toft from_untrusted\n", string(asked))
		}
		return string(out)
	}
	assert.Equal(t, "loaded\n", ask("echo 'table ip toft {'"))
	assert.Equal(t, "missing\n", ask("echo 'Error: No such file or directory' >&2; exit 1"), "the chain was removed")
	assert.Equal(t, "missing\n", ask(""), "no nft on the host")
}

// ensure.sh runs first, where the build runs, with the build's own
// DOCKER_CONFIG and every variable it reads; then the build, on the
// builder and loaded into Docker's store.
func TestRunMakesSureOfTheBuilderThenBuildsOnIt(t *testing.T) {
	h := &fakeHost{output: func(string) (string, error) { return "Builds on this server are limited to 512 MiB\n", nil }}
	var logged []string
	err := Run(context.Background(), h, Spec{Name: "toft-build", Bridge: "toft-ubuild", Memory: 1 << 30, Cgroup: "toft-apps.slice", Firewall: true}, testConfig,
		Build("-t", "paas/x:y", "/src"), func(l string) { logged = append(logged, l) })
	require.NoError(t, err)

	require.Len(t, h.calls, 2)
	assert.False(t, h.calls[0].stream)
	assert.Equal(t, []string{"sh", "-c", ensure}, h.calls[0].argv)
	assert.NotEmpty(t, ensure)
	assert.Equal(t, []string{
		"DOCKER_CONFIG=" + testConfig,
		"TOFT_BUILDER=toft-build",
		"TOFT_BUILDER_BRIDGE=toft-ubuild",
		"TOFT_BUILDER_IMAGE=" + Image,
		"TOFT_BUILDER_MEMORY=1073741824",
		"TOFT_BUILDER_CGROUP=toft-apps.slice",
		"TOFT_BUILDER_CACHE=10%",
		"TOFT_BUILDER_LOCK=/run/toft/toft-build.lock",
		"TOFT_BUILDER_WAIT=300",
	}, h.calls[0].env)
	assert.Equal(t, []string{"Builds on this server are limited to 512 MiB"}, logged)

	assert.True(t, h.calls[1].held, "the build runs under the builder's lock")
	assert.Equal(t, []string{"docker", "buildx", "build", "--builder", "toft-build", "--load", "-t", "paas/x:y", "/src"}, h.calls[1].argv)
	assert.Equal(t, append([]string{"DOCKER_CONFIG=" + testConfig, "BUILDX_GIT_INFO=false", "BUILDX_GIT_LABELS=false"}, testLock("toft-build")...), h.calls[1].env)
}

// Every variable ensure.sh reads is in what Run gives it, the ones with no
// value empty: a compose deploy's environment on the panel's host holds
// the service's variables, and none of the script's is a tenant's to set.
func TestRunSetsEveryVariableTheScriptReads(t *testing.T) {
	h := &fakeHost{}
	require.NoError(t, Run(context.Background(), h, walled(Trusted), testConfig, Build("."), func(string) {}))
	set := map[string]bool{}
	for _, kv := range h.calls[0].env {
		name, _, _ := strings.Cut(kv, "=")
		set[name] = true
	}
	assert.Contains(t, h.calls[0].env, "TOFT_BUILDER_MEMORY=", "no limit of the panel's: the script takes half of the host's")
	assert.Contains(t, h.calls[0].env, "TOFT_BUILDER_CGROUP=", "no slice off the panel's host: Docker's own cgroup")
	read := 0
	for _, word := range strings.FieldsFunc(ensure, func(r rune) bool { return !(r == '_' || r >= 'A' && r <= 'Z') }) {
		if strings.HasPrefix(word, "TOFT_BUILDER") && word != "TOFT_BUILDER_SPEC" {
			read++
			assert.True(t, set[word], "%s is read by ensure.sh and not set by Run", word)
		}
	}
	assert.NotZero(t, read)
}

// A compose up names the builder in its environment, which is how compose
// takes one, with the same DOCKER_CONFIG.
func TestRunRunsAComposeUpOnTheBuilder(t *testing.T) {
	h := &fakeHost{}
	err := Run(context.Background(), h, walled(Untrusted), testConfig, Up([]string{"--project-name", "p"}, "--no-start", "--build"), func(string) {})
	require.NoError(t, err)
	require.Len(t, h.calls, 2)
	assert.Contains(t, h.calls[0].env, "TOFT_BUILDER=toft-build-untrusted")
	assert.Contains(t, h.calls[0].env, "TOFT_BUILDER_BRIDGE=toft-upbuild")
	assert.True(t, h.calls[1].held)
	assert.Equal(t, []string{"docker", "compose", "--project-name", "p", "up", "--no-start", "--build"}, h.calls[1].argv)
	assert.Equal(t, append([]string{"DOCKER_CONFIG=" + testConfig, "BUILDX_BUILDER=toft-build-untrusted", "DOCKER_BUILDKIT=1"}, testLock("toft-build-untrusted")...), h.calls[1].env)
}

// When ensure.sh refuses (its exits 20 to 24), or fails any other way,
// nothing is built and the error is the script's own words.
func TestRunBuildsNothingWhenTheBuilderIsRefused(t *testing.T) {
	for _, words := range []string{
		"Docker's buildx plugin is missing on this server: install docker-buildx-plugin, then deploy again. (exit status 20)",
		"The network toft-build on this server isn't the one Toft makes for builds. Remove or rename it, then deploy again. (exit status 21)",
		"Couldn't create the build network toft-build: no space (exit status 22)",
		"Couldn't start the builder toft-build: pull access denied (exit status 23)",
		"Builds are running on this server's builder, and its settings have to change before the next one. Deploy again when they have finished. (exit status 24)",
		"exit status 1",
	} {
		h := &fakeHost{output: func(string) (string, error) { return "", errors.New(words) }}
		for _, c := range []Command{Build("."), Up(nil, "--build")} {
			err := Run(context.Background(), h, walled(Trusted), testConfig, c, func(string) {})
			require.ErrorContains(t, err, words)
		}
		for _, call := range h.calls {
			assert.False(t, call.stream, "nothing runs after a refusal: %v", call.argv)
		}
	}
}

// A compose deploy's first up: docker compose's own reading of the stack,
// where it runs, is the only thing that says whether it builds. A stack
// that builds goes up on the builder with the flags it was given and
// --build; one that doesn't gets the same flags with --no-build, and no
// builder is made; a reading that fails fails the deploy with its message.
func TestFirstUpAsksTheHostWhetherTheStackBuilds(t *testing.T) {
	project := []string{"--project-directory", "/r", "-f", "/r/docker-compose.yml", "--project-name", "p"}
	config := hostCall{env: []string{"DOCKER_CONFIG=" + testConfig},
		argv: []string{"docker", "compose", "--project-directory", "/r", "-f", "/r/docker-compose.yml", "--project-name", "p", "config", "--format", "json"}}
	answer := func(config string, err error) func(string) (string, error) {
		return func(name string) (string, error) {
			if name == "docker" {
				return config, err
			}
			return "", nil
		}
	}

	t.Run("a build", func(t *testing.T) {
		for _, stack := range []string{
			`{"services":{"db":{"image":"postgres:16"},"web":{"build":{"context":"/r"}}}}`,
			`{"services":{"web":{"image":"example/web","build":{"context":"/r"}}}}`,
		} {
			h := &fakeHost{output: answer(stack, nil)}
			require.NoError(t, FirstUp(context.Background(), h, walled(Trusted), testConfig, project, []string{"--no-start", "--pull", "always"}, func(string) {}))
			require.Len(t, h.calls, 4)
			assert.Equal(t, config, h.calls[0])
			assert.Equal(t, hostCall{env: config.env, argv: []string{"docker", "compose", "version", "--short"}}, h.calls[1])
			assert.Equal(t, "sh", h.calls[2].argv[0], "ensure.sh, before the up")
			assert.Equal(t, append(append([]string{"docker", "compose"}, project...), "up", "--no-start", "--pull", "always", "--build"), h.calls[3].argv)
			assert.Contains(t, h.calls[3].env, "BUILDX_BUILDER=toft-build")
		}
	})
	t.Run("none", func(t *testing.T) {
		for _, stack := range []string{
			`{"services":{"db":{"image":"postgres:16"},"web":{"image":"example/web","build":null}}}`,
			`{"services":{}}`,
			`{}`,
		} {
			h := &fakeHost{output: answer(stack, nil)}
			flags := []string{"--no-start", "--pull", "always", "--remove-orphans"}
			require.NoError(t, FirstUp(context.Background(), h, walled(Trusted), testConfig, project, flags, func(string) {}))
			require.Len(t, h.calls, 2, "no ensure.sh: the stack deploys on a server without buildx")
			assert.Equal(t, config, h.calls[0])
			assert.True(t, h.calls[1].stream)
			assert.False(t, h.calls[1].held, "nothing is built: no builder's lock")
			assert.Equal(t, append(append([]string{"docker", "compose"}, project...), "up", "--no-start", "--pull", "always", "--remove-orphans", "--no-build"), h.calls[1].argv)
			assert.Equal(t, []string{"DOCKER_CONFIG=" + testConfig}, h.calls[1].env, "no builder is named")
			assert.Len(t, flags, 4, "the caller's flags are its own")
		}
	})
	t.Run("the reading fails", func(t *testing.T) {
		for name, h := range map[string]*fakeHost{
			"config fails": {output: answer("", errors.New("services.web.build.context must be a string (exit status 15)"))},
			"not JSON":     {output: answer("services:\n", nil)},
			"nothing":      {output: answer("", nil)},
		} {
			err := FirstUp(context.Background(), h, walled(Trusted), testConfig, project, []string{"--no-start"}, func(string) {})
			require.Error(t, err, name)
			assert.Len(t, h.calls, 1, "%s: nothing runs", name)
		}
		h := &fakeHost{output: answer("", errors.New("services.web.build.context must be a string (exit status 15)"))}
		assert.ErrorContains(t, FirstUp(context.Background(), h, walled(Trusted), testConfig, project, nil, func(string) {}), "services.web.build.context must be a string")
	})
}

// A stack is built only by a docker compose that keeps the service's
// variables out of the environment of the build it starts: 2.38.0 to
// 2.39.2 don't, and neither may a version the host doesn't give or that
// isn't three numbers. Nothing is built there, and the deploy's error says
// what to upgrade. A stack that builds nothing goes up on any of them.
func TestFirstUpBuildsOnlyWithAComposeThatKeepsVariablesFromTheBuild(t *testing.T) {
	project := []string{"--project-name", "p"}
	stack := func(config string) func(string) (string, error) {
		return func(string) (string, error) { return config, nil }
	}
	builds := stack(`{"services":{"web":{"build":{"context":"/r"}}}}`)
	version := func(v string, err error) func() (string, error) {
		return func() (string, error) { return v, err }
	}

	for _, v := range []string{"2.37.3", "2.39.3", "2.39.4", "2.40.0", "v2.40.3\n", "5.7.2", "5.8.0-desktop.1", "1.29.2", "3.38.1"} {
		h := &fakeHost{output: builds, compose: version(v, nil)}
		require.NoError(t, FirstUp(context.Background(), h, walled(Trusted), testConfig, project, nil, func(string) {}), v)
		require.Len(t, h.calls, 4, v)
		assert.Equal(t, []string{"compose", "--project-name", "p", "up", "--build"}, h.calls[3].argv[1:], v)
	}

	refused := map[string]func() (string, error){
		"2.38.0":            version("2.38.0\n", nil),
		"2.38.2":            version("2.38.2\n", nil),
		"2.38.9":            version("2.38.9", nil),
		"2.39.0":            version("2.39.0", nil),
		"2.39.2":            version("v2.39.2", nil),
		"2.39.2-desktop.1":  version("2.39.2-desktop.1\n", nil),
		"a distribution's":  version("2.38.2+ds1-0ubuntu1~24.04.1\n", nil),
		"nothing":           version("", nil),
		"a word":            version("dev\n", nil),
		"two numbers":       version("2.39\n", nil),
		"not at the start":  version("Docker Compose version v5.7.2\n", nil),
		"the command fails": version("5.7.2\n", errors.New("unknown command: docker compose (exit status 1)")),
	}
	for name, v := range refused {
		h := &fakeHost{output: builds, compose: v}
		err := FirstUp(context.Background(), h, walled(Trusted), testConfig, project, nil, func(string) {})
		require.ErrorContains(t, err, "Upgrade docker-compose-plugin, then deploy again", name)
		assert.ErrorContains(t, err, "2.38.0 to 2.39.2", name)
		assert.Len(t, h.calls, 2, "%s: the stack's reading and the version, no builder and no up", name)
		assert.Empty(t, h.asked, "%s: the firewall isn't asked for a build that won't run", name)

		h = &fakeHost{output: stack(`{"services":{"web":{"image":"example/web"}}}`), compose: v}
		require.NoError(t, FirstUp(context.Background(), h, walled(Trusted), testConfig, project, nil, func(string) {}), name)
		require.Len(t, h.calls, 2, "%s: a stack that builds nothing asks no version", name)
		assert.Equal(t, []string{"compose", "--project-name", "p", "up", "--no-build"}, h.calls[1].argv[1:], name)
	}
	h := &fakeHost{output: builds, compose: version("2.38.2\n", nil)}
	assert.ErrorContains(t, FirstUp(context.Background(), h, walled(Trusted), testConfig, project, nil, func(string) {}), "Docker Compose (2.38.2)")
	h = &fakeHost{output: builds, compose: version("dev\n", nil)}
	assert.ErrorContains(t, FirstUp(context.Background(), h, walled(Trusted), testConfig, project, nil, func(string) {}), "Docker Compose (a version Toft can't read)")
}

// ensureDocker puts a docker on PATH that keeps one host's builder in dir:
// the container buildx starts for it (ctr, made slowly), and each client's
// definition in its own DOCKER_CONFIG. made counts the containers started,
// calls is every command.
func ensureDocker(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	script := `#!/bin/sh
d=` + dir + `
echo "$*" >>"$d/calls"
case "$1 $2" in
"network inspect") echo "bridge|$TOFT_BUILDER_BRIDGE|true|build|false" ;;
"container inspect") cat "$d/ctr" 2>/dev/null || exit 1 ;;
"rm -f") rm -f "$d/ctr" ;;
"buildx create") for a; do case "$a" in env.TOFT_BUILDER_SPEC=*) echo "${a#env.}" >"$DOCKER_CONFIG/def" ;; esac; done ;;
"buildx build") cat >/dev/null ;;
"env ") env ;;
"buildx inspect") [ -e "$d/ctr" ] || { echo made >>"$d/made"; sleep 0.3; printf '%s|%s\nPATH=/bin\n' "$TOFT_BUILDER" "$(cat "$DOCKER_CONFIG/def")" >"$d/ctr"; } ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// ensureOnce runs ensure.sh as Run gives it to a host, for a client with a
// Docker config of its own and the BuildKit image. The lock is in dir, the
// fake Docker's folder, and is waited for wait seconds.
func ensureOnce(t *testing.T, dir, image string, wait int) error {
	cmd := exec.Command("sh", "-c", ensure)
	cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+t.TempDir(), "TOFT_BUILDER=toft-build", "TOFT_BUILDER_BRIDGE=toft-ubuild",
		"TOFT_BUILDER_IMAGE="+image, "TOFT_BUILDER_MEMORY=2147483648", "TOFT_BUILDER_CGROUP=", "TOFT_BUILDER_CACHE=10%",
		"TOFT_BUILDER_LOCK="+filepath.Join(dir, "run", "toft-build.lock"), "TOFT_BUILDER_WAIT="+strconv.Itoa(wait))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// Two first builds at once on a host make one builder: the second waits
// for the first's check, finds its container and keeps it.
func TestTwoFirstBuildsMakeOneBuilder(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		testenv.Skip(t, "ensure.sh takes its lock with flock")
	}
	dir := ensureDocker(t)
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- ensureOnce(t, dir, Image, 30) }()
	}
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	made, err := os.ReadFile(filepath.Join(dir, "made"))
	require.NoError(t, err)
	assert.Equal(t, "made\n", string(made))
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	require.NoError(t, err)
	assert.NotContains(t, string(calls), "rm -f")
}

// A builder made for the same settings is kept, by whichever client asks:
// its container is removed only when the settings differ.
func TestABuilderIsRemovedOnlyForOtherSettings(t *testing.T) {
	dir := ensureDocker(t)
	removed := func() bool {
		calls, err := os.ReadFile(filepath.Join(dir, "calls"))
		require.NoError(t, err)
		return strings.Contains(string(calls), "rm -f buildx_buildkit_toft-build0")
	}
	require.NoError(t, ensureOnce(t, dir, Image, 30))
	require.NoError(t, ensureOnce(t, dir, Image, 30))
	assert.False(t, removed(), "the same settings")
	require.NoError(t, ensureOnce(t, dir, "moby/buildkit:other", 30))
	assert.True(t, removed(), "another image")
}

// heldBuild starts what Run runs a build as, a docker that stays until its
// input closes, and returns once the build has the lock; stop ends it.
func heldBuild(t *testing.T, dir string) (stop func()) {
	t.Helper()
	cmd := exec.Command("sh", "-c", held, "sh", "docker", "buildx", "build", ".")
	cmd.Env = append(os.Environ(), "TOFT_BUILDER_LOCK="+filepath.Join(dir, "run", "toft-build.lock"), "TOFT_BUILDER_WAIT=30")
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Start())
	stop = sync.OnceFunc(func() {
		in.Close()
		assert.NoError(t, cmd.Wait(), out.String())
	})
	t.Cleanup(stop)
	require.Eventually(t, func() bool {
		calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
		return strings.Contains(string(calls), "buildx build .")
	}, 10*time.Second, 20*time.Millisecond, "the build never started")
	return stop
}

// Builds on a builder run beside each other, and a check that finds the
// builder as it should be runs beside them. A check that has to replace it
// waits for them: it refuses when they outlast its wait, the builder
// untouched, and replaces it once they have ended.
func TestABuilderIsNotReplacedUnderABuild(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		testenv.Skip(t, "the builder's lock is taken with flock")
	}
	dir := ensureDocker(t)
	removed := func() bool {
		calls, err := os.ReadFile(filepath.Join(dir, "calls"))
		require.NoError(t, err)
		return strings.Contains(string(calls), "rm -f")
	}
	require.NoError(t, ensureOnce(t, dir, Image, 30))

	first := heldBuild(t, dir)
	second := heldBuild(t, dir) // it has the lock while the first holds it
	require.NoError(t, ensureOnce(t, dir, Image, 0), "the same settings: no wait")

	err := ensureOnce(t, dir, "moby/buildkit:other", 1)
	require.ErrorContains(t, err, "exit status 24")
	assert.ErrorContains(t, err, "Builds are running on this server's builder")
	assert.False(t, removed())

	first()
	err = ensureOnce(t, dir, "moby/buildkit:other", 1)
	require.ErrorContains(t, err, "exit status 24", "the second build goes on")
	assert.False(t, removed())

	second()
	require.NoError(t, ensureOnce(t, dir, "moby/buildkit:other", 1))
	assert.True(t, removed())
}

// A build doesn't start while a check has the builder's lock alone, and
// says so when the check outlasts its wait. The lock's variables stop at
// the shell: docker, and what compose reads, get none of them.
func TestABuildWaitsForTheBuildersCheck(t *testing.T) {
	dir := ensureDocker(t)
	build := func(wait string) (string, error) {
		cmd := exec.Command("sh", "-c", held, "sh", "docker", "env")
		cmd.Stdin = strings.NewReader("")
		cmd.Env = append(os.Environ(), "TOFT_BUILDER_LOCK="+filepath.Join(dir, "run", "toft-build.lock"), "TOFT_BUILDER_WAIT="+wait)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := build("30")
	require.NoError(t, err, out)
	assert.Contains(t, out, "PATH=")
	assert.NotContains(t, out, "TOFT_BUILDER")

	if _, err := exec.LookPath("flock"); err != nil {
		testenv.Skip(t, "the builder's lock is taken with flock")
	}
	alone := exec.Command("flock", "-x", filepath.Join(dir, "run", "toft-build.lock"), "sleep", "30")
	require.NoError(t, alone.Start())
	t.Cleanup(func() { _ = alone.Process.Kill(); _ = alone.Wait() })
	require.Eventually(t, func() bool {
		return exec.Command("flock", "-n", "-s", filepath.Join(dir, "run", "toft-build.lock"), "true").Run() != nil
	}, 10*time.Second, 20*time.Millisecond)
	out, err = build("1")
	require.ErrorContains(t, err, "exit status 24")
	assert.Contains(t, out, "This server's builder is being made or replaced. Deploy again.")
	assert.NotContains(t, out, "PATH=", "docker didn't run")
}

// What an agent takes from the panel as its builder: one of Toft's two, a
// BuildKit pinned by digest and a cache limit that is a size.
func TestCheckTakesOnlyABuilderOfTofts(t *testing.T) {
	require.NoError(t, Trusted.Check())
	require.NoError(t, Untrusted.Full().Check())
	assert.Equal(t, Spec{Name: "toft-build", Bridge: "toft-ubuild", Image: Image, Cache: "10%"}, Trusted.Full())
	kept := Spec{Name: "toft-build", Bridge: "toft-ubuild", Memory: 1 << 30, Image: "example.com/buildkit:v1@sha256:" + strings.Repeat("a", 64), Cache: "20GB"}
	require.NoError(t, kept.Check())
	assert.Equal(t, kept, kept.Full(), "what the panel names stays")
	for name, s := range map[string]Spec{
		"another name":         {Name: "default", Bridge: "toft-ubuild"},
		"another bridge":       {Name: "toft-build", Bridge: "docker0"},
		"the other's bridge":   {Name: "toft-build", Bridge: "toft-upbuild"},
		"a negative limit":     {Name: "toft-build", Bridge: "toft-ubuild", Memory: -1},
		"a cgroup parent":      {Name: "toft-build", Bridge: "toft-ubuild", Cgroup: "system.slice"},
		"no firewall asked":    {Name: "toft-build", Bridge: "toft-ubuild", Unchecked: true},
		"an image by tag":      {Name: "toft-build", Bridge: "toft-ubuild", Image: "moby/buildkit:latest"},
		"an image with more":   {Name: "toft-build", Bridge: "toft-ubuild", Image: "moby/buildkit@sha256:" + strings.Repeat("a", 64) + ",network=host"},
		"a cache limit's text": {Name: "toft-build", Bridge: "toft-ubuild", Cache: "10%\"\n[worker.oci]"},
	} {
		assert.Error(t, s.Check(), name)
	}
}
