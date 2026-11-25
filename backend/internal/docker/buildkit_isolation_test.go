package docker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"maps"
	mrand "math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/builder"
	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/testenv"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// These run real builds, on the Docker of the machine the tests run on,
// through the code a deploy builds with: D-167's cases that Toft's own
// arguments close (private/plans/2026-11-14-buildkit-builder.md, by their
// numbers there). Each leaves a few bytes of cache mount behind, which the
// builder's own garbage collection takes.

const isolationBase = "busybox:1.37"

// realBuilder is the builder of the tests that build on a real Docker: one
// of their own, so they leave the machine's toft-build as it is. Its memory
// is given (a Mac has no /proc/meminfo), and so is the firewall, which is
// not what they test. The tests leave it running, as a
// deploy leaves Toft's: `docker buildx rm toft-test-build` and
// `docker network rm toft-test-build` take it away.
var realBuilder = builder.Spec{Name: "toft-test-build", Bridge: "toft-utest", Memory: 2 << 30, Firewall: true, Unchecked: true}

// firstUp runs a compose deploy's first up of the stack in dir on this
// machine, as project, and returns its log. Whether it builds is the
// reading of docker compose config alone, as in a deploy. The project and
// its service web's image are removed when the test ends.
func firstUp(t *testing.T, dir, project string, files ...string) (string, error) {
	t.Helper()
	return firstUpWith(t, dir, project, nil, files...)
}

// firstUpWith is firstUp with env, the service's variables, as a deploy on
// the panel's host hands them to compose: in a file of its own.
func firstUpWith(t *testing.T, dir, project string, env map[string]string, files ...string) (string, error) {
	t.Helper()
	stack := []string{"--project-directory", dir, "--project-name", project}
	if env != nil {
		file, cleanup, err := localEnvFile(dir, env, func(string) {})
		require.NoError(t, err)
		t.Cleanup(cleanup)
		stack = append([]string{"--env-file", file}, stack...)
	}
	for _, f := range files {
		stack = append(stack, "-f", f)
	}
	t.Cleanup(func() {
		_ = localCompose(context.Background(), dir, append(slices.Clone(stack), "down", "--remove-orphans")...).Run()
		_ = exec.Command("docker", "image", "rm", "-f", project+"-web").Run()
	})
	cfg, cleanup, err := dockerConfigDir(nil)
	require.NoError(t, err)
	defer cleanup()
	var lines []string
	logFn := func(l string) { lines = append(lines, l) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	err = builder.FirstUp(ctx, localHost{dir: dir, log: logFn}, realBuilder, cfg, stack, []string{"--no-start", "--pull", "always", "--remove-orphans"}, logFn)
	return strings.Join(lines, "\n"), err
}

// testNetwork is a network for the stacks of one test, as a deploy's
// stack has the panel's.
func testNetwork(t *testing.T) string {
	t.Helper()
	name := "toft-test-" + strings.ToLower(rand.Text())
	out, err := exec.Command("docker", "network", "create", name).CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", name).Run() })
	return name
}

// needsBuildKit skips without a Docker that builds with BuildKit; in CI
// that is a failure (testenv.Skip).
func needsBuildKit(t *testing.T) {
	t.Helper()
	if out, err := exec.Command("docker", "buildx", "version").CombinedOutput(); err != nil {
		testenv.Skip(t, "needs docker with the buildx plugin: %v: %s", err, out)
	}
	if out, err := exec.Command("docker", "version", "--format", "{{.Server.Version}}").CombinedOutput(); err != nil {
		testenv.Skip(t, "needs a running Docker: %v: %s", err, out)
	}
}

// buildAs builds dockerfile, with files beside it, as a deploy of
// serviceID would, and returns the build's log. The image is removed when
// the test ends.
func buildAs(t *testing.T, serviceID, dockerfile string, files map[string]string) (tag, log string, err error) {
	t.Helper()
	dir := t.TempDir()
	files = maps.Clone(files)
	if files == nil {
		files = map[string]string{}
	}
	files["Dockerfile"] = dockerfile
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}

	tag = "paas/" + serviceID + ":" + uuid.NewString()
	t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", tag).Run() })
	var lines []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	err = (&LocalOrchestrator{builder: realBuilder}).localBuild(ctx, dir, "Dockerfile", tag, nil, nil, func(l string) { lines = append(lines, l) })
	return tag, strings.Join(lines, "\n"), err
}

// cacheReader is a Dockerfile whose build prints "<mark>: [<what it
// found>]" from the cache mounts named in mounts, at /m0, /m1, ...
func cacheReader(mark string, mounts ...string) string {
	run, cat := "RUN", ""
	for i, m := range mounts {
		target := "/m" + string(rune('0'+i))
		run += " --mount=type=cache," + m + ",target=" + target
		cat += " " + target + "/f"
	}
	return "FROM " + isolationBase + "\n" + run + ` echo "` + mark + `: [$(cat` + cat + ` 2>/dev/null | tr '\n' ' ')]"` + "\n"
}

func TestBuildsOfOneServiceAreKeptFromAnothers(t *testing.T) {
	needsBuildKit(t)
	a, b := uuid.NewString(), uuid.NewString()
	n := rand.Text() // a fresh one each run: no step is answered from the layer cache
	two := []string{"id=/c", "id=shared"}

	// 1a: a writes to two cache mounts and reads them back.
	_, log, err := buildAs(t, a, "FROM "+isolationBase+"\nRUN --mount=type=cache,target=/c --mount=type=cache,id=shared,target=/s echo "+n+" > /c/f && echo "+n+" > /s/f\n", nil)
	require.NoError(t, err, log)
	_, log, err = buildAs(t, a, cacheReader("1a-"+n, "id=/c", "id=shared"), nil)
	require.NoError(t, err, log)
	require.Contains(t, log, "1a-"+n+": ["+n+" "+n+" ]", "a reads what a wrote: the cases below would see a leak")

	t.Run("1b another service reads nothing", func(t *testing.T) {
		_, log, err := buildAs(t, b, cacheReader("1b-"+n, two...), nil)
		require.NoError(t, err, log)
		assert.Contains(t, log, "1b-"+n+": []")
	})
	t.Run("1d a Dockerfile can't declare the namespace", func(t *testing.T) {
		arg := "ARG BUILDKIT_CACHE_MOUNT_NS=" + buildNamespace(a) + "\n"
		df := cacheReader("1d-"+n, two...)
		_, log, err := buildAs(t, b, arg+strings.Replace(df, "\n", "\n"+arg, 1), nil)
		require.NoError(t, err, log)
		assert.Contains(t, log, "1d-"+n+": []")
	})
	t.Run("1e a cache id can't name another namespace", func(t *testing.T) {
		ns := buildNamespace(a)
		_, log, err := buildAs(t, b, cacheReader("1e-"+n,
			"id="+ns+"//c", "id="+ns+"/shared", "id=../"+a+"//c", "id=../"+a+"/shared", "id=../../"+ns+"/shared", "id=/"+ns+"/shared"), nil)
		require.NoError(t, err, log)
		assert.Contains(t, log, "1e-"+n+": []")
	})
	t.Run("2b a base image's ONBUILD reads the cache of the build that uses it", func(t *testing.T) {
		// From a registry: the builder has no image of this Docker's.
		base := privateRegistry(t, "", "") + "/base:" + strings.ToLower(n)
		seedImage(t, "", base, "FROM "+isolationBase+"\nONBUILD COPY who /who\n"+
			`ONBUILD RUN --mount=type=cache,id=shared,target=/s echo "2-`+n+`-$(cat /who): [$(cat /s/f 2>/dev/null)]"`+"\n")
		_, log, err := buildAs(t, a, "FROM "+base+"\n", map[string]string{"who": "a"})
		require.NoError(t, err, log)
		require.Contains(t, log, "2-"+n+"-a: ["+n+"]", "2a: used by a, it reads a's")
		_, log, err = buildAs(t, b, "FROM "+base+"\n", map[string]string{"who": "b"})
		require.NoError(t, err, log)
		assert.Contains(t, log, "2-"+n+"-b: []")
	})
	t.Run("6a a syntax line doesn't choose the frontend", func(t *testing.T) {
		_, log, err := buildAs(t, b, "# syntax=example.invalid/nope:1\nFROM "+isolationBase+"\nRUN echo 6a-"+n+"\n", nil)
		require.NoError(t, err, log)
		assert.NotContains(t, log, "example.invalid", "the named frontend is never looked up")
	})
	// BuildKit's own rule, with or without Toft's argument: here so a builder that changes it is noticed.
	t.Run("6d nor does an ARG", func(t *testing.T) {
		_, log, err := buildAs(t, b, "ARG BUILDKIT_SYNTAX=example.invalid/nope:1\nFROM "+isolationBase+"\nRUN echo 6d-"+n+"\n", nil)
		require.NoError(t, err, log)
		assert.NotContains(t, log, "resolve image config for docker-image://example.invalid")
	})

	// The compose forms: a stack's builds, with the override a deploy adds.
	composeBuild := func(t *testing.T, serviceID, composeFile string, files map[string]string) (string, error) {
		t.Helper()
		dir := t.TempDir()
		files["docker-compose.yml"] = composeFile
		for name, content := range files {
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
		}
		overrides, err := composeOverrides(serviceID, rollout.NetConfig{}, nil,
			deployment.ComposeNetworks{Home: testNetwork(t), Services: []string{"web"}, Built: []string{"web"}}, nil)
		require.NoError(t, err)
		stack := []string{filepath.Join(dir, "docker-compose.yml")}
		for _, ov := range overrides {
			file, cleanup, err := writeLocalOverride(ov)
			require.NoError(t, err)
			t.Cleanup(cleanup)
			stack = append(stack, file)
		}
		return firstUp(t, dir, "toft-test-"+strings.ToLower(rand.Text()), stack...)
	}
	t.Run("compose: a stack of the same service reads its cache, another's reads nothing", func(t *testing.T) {
		const file = "services:\n  web:\n    build: .\n"
		log, err := composeBuild(t, a, file, map[string]string{"Dockerfile": cacheReader("c1a-"+n, two...)})
		require.NoError(t, err, log)
		require.Contains(t, log, "c1a-"+n+": ["+n+" "+n+" ]")
		log, err = composeBuild(t, b, file, map[string]string{"Dockerfile": cacheReader("c1b-"+n, two...)})
		require.NoError(t, err, log)
		assert.Contains(t, log, "c1b-"+n+": []")
	})
	t.Run("compose: the file's own build.args lose to the override", func(t *testing.T) {
		for form, args := range map[string]string{
			"map":  "        BUILDKIT_CACHE_MOUNT_NS: " + buildNamespace(a) + "\n        BUILDKIT_SYNTAX: example.invalid/nope:1\n",
			"list": "        - BUILDKIT_CACHE_MOUNT_NS=" + buildNamespace(a) + "\n        - BUILDKIT_SYNTAX=example.invalid/nope:1\n",
		} {
			log, err := composeBuild(t, b, "services:\n  web:\n    build:\n      context: .\n      args:\n"+args,
				map[string]string{"Dockerfile": "# syntax=example.invalid/nope:1\n" + cacheReader("c1h-"+form+"-"+n, two...)})
			require.NoError(t, err, form+": "+log)
			assert.Contains(t, log, "c1h-"+form+"-"+n+": []", form)
			assert.NotContains(t, log, "resolve image config for docker-image://example.invalid", form)
		}
	})
}

// A compose build invokes buildx, which by default reads git from the build
// context — the repository's own working tree, which a tenant shapes. A
// deploy's compose build (localCompose) turns that off, so buildx starts no
// git in the context. A git on PATH records every call: the build a deploy
// runs records none, while the same build with buildx's default records some.
func TestComposeBuildRunsNoGitInTheContext(t *testing.T) {
	needsBuildKit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		testenv.Skip(t, "needs git on PATH: %v", err)
	}

	// stack writes a buildable one-service stack into a fresh directory and a
	// git on PATH that records it ran, then returns the compose arguments a
	// deploy builds it with and the path the recorder writes to.
	stack := func(t *testing.T) (dir, ran string, args []string) {
		dir = t.TempDir()
		ran = filepath.Join(t.TempDir(), "git-ran")
		bin := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(bin, "git"),
			[]byte("#!/bin/sh\necho called >> '"+ran+"'\nexec '"+realGit+"' \"$@\"\n"), 0o700))
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		n := rand.Text()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  web:\n    build: .\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM "+isolationBase+"\nRUN echo "+n+"\n"), 0o644))
		project := "toft-test-" + strings.ToLower(rand.Text())
		t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", project+"-web").Run() })
		args = []string{"--project-directory", dir, "--project-name", project, "-f", filepath.Join(dir, "docker-compose.yml"), "build"}
		return
	}

	t.Run("buildx's default reads git, the control", func(t *testing.T) {
		dir, ran, args := stack(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
		cmd.Dir, cmd.Env = dir, localComposeEnv() // the deploy's clean env, without Toft's BUILDX_GIT_* vars
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		assert.FileExists(t, ran, "with buildx's default the build reads git from the context")
	})

	t.Run("a deploy's build reads none", func(t *testing.T) {
		dir, ran, args := stack(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		out, err := localCompose(ctx, dir, args...).CombinedOutput()
		require.NoError(t, err, string(out))
		assert.NoFileExists(t, ran, "the deploy's compose build starts no git in the context")
	})
	// As a deploy from a repository runs it: the copy of the clone, with a
	// build context further down that holds a repository's files.
	t.Run("nor in a copied repository whose context holds a repository", func(t *testing.T) {
		dir, ran, args := stack(t)
		app := filepath.Join(dir, "app")
		out, err := exec.Command(realGit, "init", "--quiet", "--bare", app).CombinedOutput()
		require.NoError(t, err, string(out))
		require.NoError(t, os.Rename(filepath.Join(dir, "Dockerfile"), filepath.Join(app, "Dockerfile")))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  web:\n    build: ./app\n"), 0o644))
		release := t.TempDir()
		require.NoError(t, copyRepo(dir, release))
		require.FileExists(t, filepath.Join(release, "app", "HEAD"), "the copy keeps the context's files")
		for i, a := range args {
			args[i] = strings.Replace(a, dir, release, 1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		out, err = localCompose(ctx, release, args...).CombinedOutput()
		require.NoError(t, err, string(out))
		assert.NoFileExists(t, ran, "the deploy's compose build starts no git in the context")
	})
}

// A build on the panel's host leaves out what the repository's
// .dockerignore names, as a build on a server does.
func TestLocalBuildHonoursDockerignore(t *testing.T) {
	needsBuildKit(t)
	n := rand.Text()
	_, log, err := buildAs(t, uuid.NewString(),
		"FROM "+isolationBase+"\nCOPY . /src\nRUN echo \"ctx-"+n+": [$(ls /src | tr '\\n' ' ')]\"\n",
		map[string]string{".dockerignore": "secret.txt\n", "secret.txt": "s", "kept.txt": "k"})
	require.NoError(t, err, log)
	assert.Contains(t, log, "ctx-"+n+": [Dockerfile kept.txt ]")
}

// seedImage builds dockerfile on realBuilder and pushes it as ref, the way
// an image gets into privateRegistry: only the builder reaches it. cfg is
// the Docker config with the registry's login ("": none); the builder's
// definition goes in it as ensure.sh writes it in a build's.
func seedImage(t *testing.T, cfg, ref, dockerfile string) {
	t.Helper()
	if cfg == "" {
		dir, cleanup, err := dockerConfigDir(nil)
		require.NoError(t, err)
		defer cleanup()
		cfg = dir
	}
	for _, args := range [][]string{
		{"buildx", "create", "--name", realBuilder.Name, "--driver", "docker-container"},
		{"buildx", "build", "--builder", realBuilder.Name, "--push", "-t", ref, "-"},
	} {
		seed := exec.Command("docker", args...)
		seed.Env = append(os.Environ(), "DOCKER_CONFIG="+cfg)
		seed.Stdin = strings.NewReader(dockerfile)
		out, err := seed.CombinedOutput()
		require.NoError(t, err, string(out))
	}
}

// privateRegistry starts a registry that only realBuilder reaches and
// returns its host. With a user it takes that user and password only. It is removed when the test ends.
func privateRegistry(t *testing.T, user, password string) string {
	t.Helper()
	// The builder's container is there once a build has run on it.
	_, log, err := buildAs(t, uuid.NewString(), "FROM scratch\nCOPY Dockerfile /\n", nil)
	require.NoError(t, err, log)
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		require.NoError(t, err, "docker %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	name := "toft-test-registry-" + strings.ToLower(rand.Text())
	// On the builder's own loopback, in its network namespace (the builder
	// pulls, never the daemon; a registry on 127.0.0.1 may speak plain
	// HTTP, and a build reaches no address of the host), at a port picked
	// at random. Its storage is a tmpfs, so no volume is left behind. Without
	// its debug server: that one takes the host's port 5001, and the
	// registry exits where something else has it.
	host := fmt.Sprintf("127.0.0.1:%d", 20000+mrand.IntN(40000))
	create := []string{"create", "--name", name, "--network", "container:buildx_buildkit_" + realBuilder.Name + "0", "--tmpfs", "/var/lib/registry", "-e", "REGISTRY_HTTP_ADDR=" + host, "-e", "REGISTRY_HTTP_DEBUG_ADDR="}
	answer := "{}"
	if user != "" {
		create = append(create, "-e", "REGISTRY_AUTH=htpasswd", "-e", "REGISTRY_AUTH_HTPASSWD_REALM=test", "-e", "REGISTRY_AUTH_HTPASSWD_PATH=/htpasswd")
		answer = "401"
	}
	docker(append(create, "registry:3")...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	if user != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		require.NoError(t, err)
		htpasswd := filepath.Join(t.TempDir(), "htpasswd")
		require.NoError(t, os.WriteFile(htpasswd, []byte(user+":"+string(hash)+"\n"), 0o644))
		docker("cp", htpasswd, name+":/htpasswd")
	}
	docker("start", name)
	require.Eventually(t, func() bool {
		out, _ := exec.Command("docker", "exec", name, "wget", "-qO-", "http://"+host+"/v2/").CombinedOutput()
		return strings.Contains(string(out), answer)
	}, 30*time.Second, 200*time.Millisecond, "the registry answers")
	return host
}

// A Dockerfile's private base image is pulled with the service's registry
// login and with nothing else, and no RUN step can read that login: not in
// its environment, nor in a file anywhere in its filesystem, the build's
// context included. The step builds what it looks for from halves, so the
// log has a value only if the step found or printed it; a file with a
// control value shows the search finds what is there.
func TestBuildPullsItsBaseWithALoginNoRunStepCanRead(t *testing.T) {
	needsBuildKit(t)
	n, marker := rand.Text(), rand.Text()
	host := privateRegistry(t, "bot", marker)
	auth := &rollout.RegistryAuth{Server: host, Username: "bot", Password: marker}
	b64 := base64.StdEncoding.EncodeToString([]byte("bot:" + marker))

	// The base image, pushed with the login; this Docker never has it.
	base := host + "/base:" + strings.ToLower(n)
	raw, err := rollout.RegistryConfig(*auth, host)
	require.NoError(t, err)
	cfg, cleanup, err := dockerConfigDir(raw)
	require.NoError(t, err)
	defer cleanup()
	seedImage(t, cfg, base, "FROM "+isolationBase+"\nRUN echo "+n+" > /base\n")

	half := func(s string) string { return `"$(echo ` + s[:len(s)/2] + `)$(echo ` + s[len(s)/2:] + `)"` }
	dir := t.TempDir()
	df := "FROM " + base + "\nCOPY . /ctx\n" +
		"RUN M=" + half(marker) + "; B=" + half(b64) + "; C=" + half("ctl"+n) + "; env; tr '\\0' '\\n' < /proc/self/environ; " +
		`find / \( -path /proc -o -path /sys \) -prune -o -type f -exec grep -H -o -e "$M" -e "$B" -e "$C" {} + 2>/dev/null; ` +
		`find / \( -path /proc -o -path /sys \) -prune -o -name "conf""ig.json" -print; cat /base; echo "ran-$C"` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ctl"), []byte("ctl"+n+"\n"), 0o644))
	build := func(auth *rollout.RegistryAuth) (string, error) {
		tag := "paas/" + uuid.NewString() + ":" + uuid.NewString()
		t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", tag).Run() })
		var lines []string
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		err := (&LocalOrchestrator{builder: realBuilder}).localBuild(ctx, dir, "Dockerfile", tag, nil, auth, func(l string) { lines = append(lines, l) })
		return strings.Join(lines, "\n"), err
	}

	log, err := build(nil)
	require.Error(t, err, "without the login the base image is not this build's to pull: %s", log)
	assert.NotContains(t, log, "ran-ctl"+n)

	log, err = build(auth)
	require.NoError(t, err, log)
	require.Contains(t, log, "ran-ctl"+n, "the step ran")
	require.Contains(t, log, "/ctx/ctl:ctl"+n, "the search finds a file's value")
	assert.NotContains(t, log, marker)
	assert.NotContains(t, log, b64)
	assert.NotContains(t, log, "config.json")
}

// A build cancelled mid-way (its service deleted, the deploy's time limit)
// ends soon, and leaves no image and no step running.
func TestCancelledBuildStops(t *testing.T) {
	needsBuildKit(t)
	n := rand.Text()
	const nap = "sleep 86399"
	dir := t.TempDir()
	df := "FROM " + isolationBase + "\nRUN echo started-" + n + " && " + nap + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644))

	tag := "paas/" + uuid.NewString() + ":" + uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(5*time.Minute, cancel) // a build that never reaches the step fails the test, not the run
	started := false
	done := make(chan error, 1)
	go func() {
		done <- (&LocalOrchestrator{builder: realBuilder}).localBuild(ctx, dir, "Dockerfile", tag, nil, nil, func(l string) {
			// The step's output, not the line that names its command.
			if strings.HasSuffix(l, " started-"+n) {
				started = true
				cancel()
			}
		})
	}()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(6 * time.Minute):
		t.Fatal("the cancelled build did not end")
	}
	require.True(t, started, "the build reached its step")
	assert.Error(t, exec.Command("docker", "image", "inspect", tag).Run(), "no image")
	if runtime.GOOS == "linux" {
		// The step ran on this host's kernel: it is gone from its process list.
		assert.Eventually(t, func() bool { return exec.Command("pgrep", "-f", nap).Run() != nil }, 30*time.Second, 200*time.Millisecond, "the step still runs")
	}
}

// A compose deploy never starts an image an earlier deploy left on the
// server: a stack that docker compose config shows as building is built,
// on the builder, and the container created is the new image's. Nothing
// here tells the deploy whether the stack builds; the three stacks build
// in ways a reader of the file alone can miss.
func TestComposeDeployBuildsOverAnEarlierDeploysImage(t *testing.T) {
	needsBuildKit(t)
	const dockerfile = "FROM " + isolationBase + "\nCOPY word /word\nCMD [\"cat\", \"/word\"]\n"
	for name, files := range map[string]map[string]string{
		"its own build": {"docker-compose.yml": "services:\n  web:\n    build: .\n"},
		"through extends": {
			"docker-compose.yml": "services:\n  web:\n    extends:\n      file: base.yml\n      service: base\n",
			"base.yml":           "services:\n  base:\n    build: .\n",
		},
		"through include": {
			"docker-compose.yml": "include:\n  - stack.yml\n",
			"stack.yml":          "services:\n  web:\n    build: .\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, project := t.TempDir(), "toft-test-"+strings.ToLower(rand.Text())
			files["Dockerfile"] = dockerfile
			for name, content := range files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
			}
			for _, word := range []string{"old-" + project, "new-" + project} {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "word"), []byte(word+"\n"), 0o644))
				log, err := firstUp(t, dir, project, filepath.Join(dir, "docker-compose.yml"))
				require.NoError(t, err, log)
				assert.Contains(t, log, "COPY word /word", "built, and through the deploy's log")
				out, err := exec.Command("docker", "start", "-a", project+"-web-1").CombinedOutput()
				require.NoError(t, err, string(out))
				assert.Equal(t, word, strings.TrimSpace(string(out)), "the container is this deploy's image")
			}
			out, err := exec.Command("docker", "buildx", "du", "--verbose").CombinedOutput()
			require.NoError(t, err, string(out))
			assert.NotContains(t, string(out), "COPY word /word", "nothing was built on Docker's own builder")
		})
	}
}

// The up of a stack that builds nothing carries --no-build. Given a stack
// that does build, with no image of it here, that command fails and makes
// no image: nothing is built outside the builder.
func TestComposeUpWithNoBuildBuildsNothing(t *testing.T) {
	needsBuildKit(t)
	dir, project := t.TempDir(), "toft-test-"+strings.ToLower(rand.Text())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services:\n  web:\n    build: .\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM "+isolationBase+"\n"), 0o644))
	stack := []string{"--project-directory", dir, "--project-name", project}
	t.Cleanup(func() {
		_ = localCompose(context.Background(), dir, append(slices.Clone(stack), "down", "--remove-orphans")...).Run()
		_ = exec.Command("docker", "image", "rm", "-f", project+"-web").Run()
	})
	out, err := localCompose(context.Background(), dir, append(slices.Clone(stack), "up", "--no-start", "--pull", "always", "--remove-orphans", "--no-build")...).CombinedOutput()
	require.Error(t, err, string(out))
	assert.Error(t, exec.Command("docker", "image", "inspect", project+"-web").Run(), "no image was made: %s", out)
}

// policyHost is a host whose builds run with a BuildKit source policy, as
// buildx reads one: from a variable of its own environment.
type policyHost struct {
	builder.Host
	policy string
}

func (h policyHost) Stream(ctx context.Context, env []string, name string, args ...string) error {
	return h.Host.Stream(ctx, append(env, sourcePolicyVar+"="+h.policy), name, args...)
}

// sourcePolicyVar names a file of rules buildx sends with a build: which
// sources the builder refuses, and which it fetches in another's place.
const sourcePolicyVar = "EXPERIMENTAL_BUILDKIT_SOURCE_POLICY"

// A compose deploy's build runs the Dockerfile frontend Toft names and
// fetches the sources the stack names. The service's variables are for
// compose to read the stack with: one that names a source policy in the
// repository changes neither, whether the variables reach compose as a
// deploy on the panel's host hands them over or in the project's .env, as
// on a server.
func TestAServicesVariablesSetNoSourcePolicy(t *testing.T) {
	needsBuildKit(t)
	serviceID := uuid.NewString()
	override, err := composeBuildOverride(serviceID, []string{"web"})
	require.NoError(t, err)
	// The frontend every build runs, fetched from a registry that isn't one.
	policy := `{"rules":[{"action":"CONVERT","selector":{"identifier":"docker-image://docker.io/docker/dockerfile*"},` +
		`"updates":{"identifier":"docker-image://example.invalid/nope:1"}}]}`
	stack := func(t *testing.T) string {
		dir := t.TempDir()
		for name, content := range map[string]string{
			"docker-compose.yml":       "services:\n  web:\n    build: .\n",
			"docker-compose.build.yml": string(override),
			"Dockerfile":               "FROM " + isolationBase + "\nRUN echo " + strings.ToLower(rand.Text()) + "\n",
			"policy.json":              policy,
		} {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
		}
		return dir
	}

	// The policy is one buildx takes: a build that has it in its own
	// environment goes to that registry for the frontend.
	t.Run("the policy changes a build that runs with it", func(t *testing.T) {
		dir := stack(t)
		cfg, cleanup, err := dockerConfigDir(nil)
		require.NoError(t, err)
		defer cleanup()
		var lines []string
		logFn := func(l string) { lines = append(lines, l) }
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		tag := "paas/" + serviceID + ":" + uuid.NewString()
		t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", tag).Run() })
		err = builder.Run(ctx, policyHost{localHost{log: logFn}, filepath.Join(dir, "policy.json")}, realBuilder, cfg,
			builder.Build("-t", tag, "--build-arg", buildSyntaxArg+"="+dockerfileFrontend, dir), logFn)
		require.Error(t, err, strings.Join(lines, "\n"))
		require.Contains(t, strings.Join(lines, "\n"), "example.invalid")
	})

	for name, vars := range map[string]func(dir string) map[string]string{
		"handed to compose on the panel's host": func(string) map[string]string {
			return map[string]string{sourcePolicyVar: "policy.json", "APP_KEY": "1"}
		},
		"in the project's .env": func(dir string) map[string]string {
			dotEnv, err := composespec.DotEnvFile(map[string]string{sourcePolicyVar: filepath.Join(dir, "policy.json"), "APP_KEY": "1"})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), dotEnv, 0o600))
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir, project := stack(t), "toft-test-"+strings.ToLower(rand.Text())
			log, err := firstUpWith(t, dir, project, vars(dir),
				filepath.Join(dir, "docker-compose.yml"), filepath.Join(dir, "docker-compose.build.yml"))
			assert.NoError(t, err, log)
			assert.NotContains(t, log, "example.invalid", "the build ran Toft's frontend")
		})
	}
}
