package docker

import (
	"context"
	"crypto/rand"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/testenv"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These run real builds, on the Docker of the machine the tests run on,
// through the code a deploy builds with: D-167's cases that Toft's own
// arguments close (private/plans/2026-11-14-buildkit-builder.md, by their
// numbers there). Each leaves a few bytes of cache mount behind, which the
// builder's own garbage collection takes.

const isolationBase = "busybox:1.37"

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
	err = localBuild(ctx, dir, "Dockerfile", tag, nil, func(l string) { lines = append(lines, l) })
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
		base, log, err := buildAs(t, a, "FROM "+isolationBase+"\nONBUILD COPY who /who\n"+
			`ONBUILD RUN --mount=type=cache,id=shared,target=/s echo "2-`+n+`-$(cat /who): [$(cat /s/f 2>/dev/null)]"`+"\n", nil)
		require.NoError(t, err, log)
		_, log, err = buildAs(t, a, "FROM "+base+"\n", map[string]string{"who": "a"})
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
			deployment.ComposeNetworks{Home: "paas-network", Services: []string{"web"}, Built: []string{"web"}}, nil)
		require.NoError(t, err)
		project := "toft-test-" + strings.ToLower(rand.Text())
		args := []string{"--project-directory", dir, "--project-name", project, "-f", filepath.Join(dir, "docker-compose.yml")}
		for _, ov := range overrides {
			file, cleanup, err := writeLocalOverride(ov)
			require.NoError(t, err)
			t.Cleanup(cleanup)
			args = append(args, "-f", file)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "image", "rm", "-f", project+"-web").Run() })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		out, err := localCompose(ctx, dir, nil, append(args, "build")...).CombinedOutput()
		return string(out), err
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
		done <- localBuild(ctx, dir, "Dockerfile", tag, nil, func(l string) {
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
