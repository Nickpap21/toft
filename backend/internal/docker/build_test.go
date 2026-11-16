package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const (
	testDeploymentID = "9f8e7d6c-1111-2222-3333-444455556666"
	// testImageTag is a git deploy's image (deployment's gitImageTag).
	testImageTag = "paas/" + testServiceID + ":" + testDeploymentID
)

// A build's arguments end with the two that keep it to its service, so
// they win over anything before them; a BUILDKIT_ argument that came with
// the build (a Nixpacks plan's variable) is not passed on.
func TestBuildArgListEndsWithTheServicesOwn(t *testing.T) {
	v := func(s string) *string { return &s }
	got, err := buildArgList(testImageTag, map[string]*string{
		"VITE_API": v("https://api.example.com"), "NEXT_PUBLIC_X": v("a b"), "UNSET": nil,
		"BUILDKIT_SYNTAX": v("example.invalid/nope:1"), "BUILDKIT_CACHE_MOUNT_NS": v("paas/other"), "BUILDKIT_INLINE_CACHE": v("1"),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"NEXT_PUBLIC_X=a b",
		"VITE_API=https://api.example.com",
		"BUILDKIT_SYNTAX=" + dockerfileFrontend,
		"BUILDKIT_CACHE_MOUNT_NS=paas/" + testServiceID,
	}, got)
	assert.Contains(t, dockerfileFrontend, "@sha256:", "the frontend is pinned by digest")
}

// A build whose tag doesn't name its service has no namespace of its own,
// so it isn't built.
func TestBuildArgListRefusesATagThatIsNotAServices(t *testing.T) {
	for _, tag := range []string{
		"", "img:1", "paas/x:1", "paas/" + testServiceID, "paas/" + testServiceID + ":latest",
		"paas/" + testServiceID + "/x:" + testDeploymentID, "other/" + testServiceID + ":" + testDeploymentID,
		"paas/../" + testServiceID + ":" + testDeploymentID, "paas/" + testServiceID + ":" + testDeploymentID + ";id",
	} {
		_, err := buildArgList(tag, nil)
		assert.Error(t, err, tag)
	}
}

// fakeDocker puts a docker in PATH that records its arguments, its stdin,
// its environment, what its DOCKER_CONFIG holds and what its last argument
// (a build's context) holds, and returns the folder those are written to.
func fakeDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + dir + "/args\n" +
		"cat > " + dir + "/stdin\n" +
		"env > " + dir + "/env\n" +
		"ls -A \"$DOCKER_CONFIG\" > " + dir + "/config\n" +
		"for last; do :; done; (cd \"$last\" && find . -type f | sort) > " + dir + "/context\n" +
		"echo '#1 building'\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// sourceTar is a commit's tree as BuildFromArchive gets it: files by name,
// and links (name to target).
func sourceTar(t *testing.T, files, links map[string]string) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	for name, target := range links {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0o777}))
	}
	require.NoError(t, tw.Close())
	return &buf
}

// A build on the panel's host is the docker CLI's (BuildKit, where it has
// buildx), with the tree unpacked in a folder as its context (the CLI
// applies a .dockerignore to a folder, not to a tar), the Dockerfile named
// inside it, the service's two arguments last, none of the backend's
// environment and an empty Docker config of its own; folder and config are
// removed afterwards.
func TestLocalBuildRunsDockerBuild(t *testing.T) {
	dir := fakeDocker(t)
	t.Setenv("JWT_SECRET", "the backend's")
	sha := "abc"
	var logged []string
	source := sourceTar(t, map[string]string{"services/api/Dockerfile": "FROM scratch\n", ".dockerignore": "secret\n", "secret": "s"}, nil)
	err := (&LocalOrchestrator{}).BuildFromArchive(context.Background(), source, "services/api", testImageTag, nil,
		map[string]*string{"TOFT_GIT_SHA": &sha}, func(l string) { logged = append(logged, l) })
	require.NoError(t, err)

	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		return string(raw)
	}
	args := strings.Split(strings.TrimSpace(read("args")), "\n")
	buildDir := args[len(args)-1]
	assert.Equal(t, []string{
		"build", "-t", testImageTag, "-f", buildDir + "/services/api/Dockerfile",
		"--build-arg", "TOFT_GIT_SHA=abc",
		"--build-arg", "BUILDKIT_SYNTAX=" + dockerfileFrontend,
		"--build-arg", "BUILDKIT_CACHE_MOUNT_NS=paas/" + testServiceID,
		buildDir,
	}, args)
	assert.Empty(t, read("stdin"))
	assert.Equal(t, "./.dockerignore\n./secret\n./services/api/Dockerfile\n", read("context"))
	assert.NoDirExists(t, buildDir, "the unpacked tree is removed after the build")
	assert.Equal(t, "cli-plugins\n", read("config"), "the build's Docker config holds no login, only the way to the CLI's plugins")
	env := read("env")
	assert.NotContains(t, env, "JWT_SECRET")
	assert.Contains(t, env, "BUILDX_GIT_INFO=false\n", "buildx runs no git in a tenant's tree")
	var cfg string
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(line, "DOCKER_CONFIG="); ok {
			cfg = v
		}
	}
	require.NotEmpty(t, cfg)
	assert.NoDirExists(t, cfg, "the config directory is removed after the build")
	assert.Contains(t, logged, "#1 building")
	assert.Contains(t, logged, "Image built: "+testImageTag)
}

// The CLI reads the Dockerfile on the panel's host, so one that a link in
// the repository takes out of the tree (one of the backend's own files) is
// refused before docker runs.
func TestLocalBuildRefusesADockerfileLinkedOutOfTheTree(t *testing.T) {
	dir := fakeDocker(t)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "Dockerfile"), []byte("FROM scratch\n"), 0o600))
	for name, tc := range map[string]struct {
		baseDir string
		links   map[string]string
	}{
		"the Dockerfile is a link":               {"", map[string]string{"Dockerfile": filepath.Join(outside, "Dockerfile")}},
		"its folder is a link":                   {"api", map[string]string{"api": outside}},
		"its folder is a link, to one with none": {"api", map[string]string{"api": t.TempDir()}},
	} {
		err := (&LocalOrchestrator{}).BuildFromArchive(context.Background(), sourceTar(t, map[string]string{"main.go": "package main\n"}, tc.links),
			tc.baseDir, testImageTag, nil, nil, func(string) {})
		require.ErrorIs(t, err, service.ErrInvalidConfig, name)
		assert.ErrorContains(t, err, "a link out of the repository", name)
		assert.NoFileExists(t, filepath.Join(dir, "args"), name+": docker did not run")
	}
}

// Nixpacks on the panel's host reads the repository's files, through
// links too, and puts what they say in its plan: it runs with none of the
// backend's environment, and not at all on a tree with a link that leaves
// it or leads nowhere.
func TestLocalNixpacksIsKeptToTheTree(t *testing.T) {
	dir := fakeDocker(t)
	script := "#!/bin/sh\necho \"$1\" >> " + dir + "/nixran\nenv >> " + dir + "/nixenv\n[ \"$1\" = plan ] && echo '{}'\n" +
		"[ \"$1\" = build ] && mkdir \"$2/.nixpacks\" && echo 'FROM scratch' > \"$2/.nixpacks/Dockerfile\"\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nixpacks"), []byte(script), 0o700))
	t.Setenv("JWT_SECRET", "the backend's")
	build := func(links map[string]string) error {
		return (&LocalOrchestrator{}).BuildFromArchive(context.Background(), sourceTar(t, map[string]string{"package.json": "{}"}, links),
			"", testImageTag, &deployment.NixpacksCommands{}, nil, func(string) {})
	}

	for name, links := range map[string]map[string]string{
		"a link out of the tree": {"Procfile": "/proc/1/environ"},
		"a link to nowhere":      {"Procfile": "not-there"},
	} {
		err := build(links)
		require.ErrorIs(t, err, service.ErrInvalidConfig, name)
		assert.ErrorContains(t, err, "Procfile is a link that doesn't stay in the build folder", name)
		assert.NoFileExists(t, filepath.Join(dir, "nixran"), name+": nixpacks did not run")
	}

	require.NoError(t, build(map[string]string{"Procfile": "package.json"}), "a link inside the tree builds")
	ran, err := os.ReadFile(filepath.Join(dir, "nixran"))
	require.NoError(t, err)
	assert.Equal(t, "plan\nbuild\n", string(ran))
	env, err := os.ReadFile(filepath.Join(dir, "nixenv"))
	require.NoError(t, err)
	assert.NotContains(t, string(env), "JWT_SECRET")
}

// A command's output reaches the deployment log while the command runs:
// this one waits, after its first line, for the test to have read it.
func TestRunStreamHandsLinesOverAsTheyAreWritten(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	pr, pw := io.Pipe()
	cmd := exec.Command("sh", "-c", "echo one; echo err >&2; read _; echo two")
	cmd.Stdin = pr
	var got []string
	err := runStream(cmd, func(l string) {
		got = append(got, l)
		if l == "err" {
			pw.Close()
		}
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "err", "two"}, got)
}

// Only the stack's services that have a build get the build arguments: on
// one without, build.args would make compose build it from the project
// directory under its image's name.
func TestComposeBuildOverrideNamesOnlyBuiltServices(t *testing.T) {
	out, err := composeOverrides(testServiceID, rollout.NetConfig{}, nil, deployment.ComposeNetworks{
		Home: "paas-network", Services: []string{"db", "web", "worker"}, Built: []string{"web", "worker"}}, nil)
	require.NoError(t, err)
	var build []byte
	for _, f := range out {
		if f.name == "docker-compose.build.yml" {
			build = f.content
		}
	}
	require.NotNil(t, build)
	var got struct {
		Services map[string]struct {
			Build struct {
				Args map[string]string `yaml:"args"`
			} `yaml:"build"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(build, &got))
	want := map[string]string{"BUILDKIT_SYNTAX": dockerfileFrontend, "BUILDKIT_CACHE_MOUNT_NS": "paas/" + testServiceID}
	require.Len(t, got.Services, 2)
	assert.Equal(t, want, got.Services["web"].Build.Args)
	assert.Equal(t, want, got.Services["worker"].Build.Args)

	none, err := composeOverrides(testServiceID, rollout.NetConfig{}, nil, deployment.ComposeNetworks{Home: "paas-network", Services: []string{"db"}}, nil)
	require.NoError(t, err)
	for _, f := range none {
		assert.NotEqual(t, "docker-compose.build.yml", f.name, "a stack that builds nothing gets no build override")
	}

	_, err = composeOverrides("svc-1", rollout.NetConfig{}, nil, deployment.ComposeNetworks{Home: "paas-network", Built: []string{"web"}}, nil)
	assert.Error(t, err, "a build with no service ID has no namespace")
}

// A compose deploy on the panel's host with credentials never logs the
// host in: its up runs with a docker config of its own, removed afterwards.
func TestLocalComposeKeepsItsCredentialsToItself(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"$@\" >> " + bin + "/args\n" +
		"case \"$*\" in *' up --no-start '*) echo \"$DOCKER_CONFIG\" > " + bin + "/dir; cat \"$DOCKER_CONFIG/config.json\" > " + bin + "/config; exit 1;; esac\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	o := &LocalOrchestrator{dataDir: t.TempDir(), dockerClient: fakedocker.New(t).Client(), log: zap.NewNop()}
	err := o.RunCompose(context.Background(), "dep-1", testServiceID, "web", "services: {web: {image: ghcr.io/acme/web:1}}\n",
		nil, nil, deployment.ComposeNetworks{Home: "paas-network"}, nil,
		&deployment.RegistryAuth{Server: "ghcr.io", Username: "bot", Password: "hunter2"}, rollout.Hooks{Log: func(string) {}})
	require.Error(t, err, "the fake's up fails")

	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(bin, name))
		require.NoError(t, err)
		return strings.TrimSpace(string(raw))
	}
	assert.NotContains(t, read("args"), "login")
	assert.NotContains(t, read("args"), "hunter2")
	assert.JSONEq(t, `{"auths":{"ghcr.io":{"auth":"Ym90Omh1bnRlcjI="}}}`, read("config"))
	assert.NoDirExists(t, read("dir"), "the credentials are removed after the deploy")

	// Without credentials the config is the deploy's own too, and empty: a
	// login made on the host is not its to pull with.
	err = o.RunCompose(context.Background(), "dep-2", testServiceID, "web", "services: {web: {image: ghcr.io/acme/web:1}}\n",
		nil, nil, deployment.ComposeNetworks{Home: "paas-network"}, nil, nil, rollout.Hooks{Log: func(string) {}})
	require.Error(t, err, "the fake's up fails")
	assert.NotEmpty(t, read("dir"))
	assert.Empty(t, read("config"))
}
