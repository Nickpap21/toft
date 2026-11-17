package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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
// its environment, what its DOCKER_CONFIG holds (and its config.json, as
// login) and what its last argument
// (a build's context) holds, and returns the folder those are written to.
func fakeDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + dir + "/args\n" +
		"cat > " + dir + "/stdin\n" +
		"env > " + dir + "/env\n" +
		"ls -A \"$DOCKER_CONFIG\" > " + dir + "/config\n" +
		"cat \"$DOCKER_CONFIG/config.json\" > " + dir + "/login 2>/dev/null\n" +
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
		map[string]*string{"TOFT_GIT_SHA": &sha}, nil, func(l string) { logged = append(logged, l) })
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

// A build on the panel's host with the service's registry login pulls its
// base images with it: the docker build's own config holds it, nixpacks
// never sees it, and it is removed after the build. Without a password the
// config holds no login.
func TestLocalBuildPullsWithTheServicesLogin(t *testing.T) {
	dir := fakeDocker(t)
	shellNixpacks(t)
	nix := "#!/bin/sh\nenv >> " + dir + "/nixenv\n[ \"$1\" = plan ] && echo '{}'\n" +
		"[ \"$1\" = build ] && mkdir \"$2/.nixpacks\" && echo 'FROM scratch' > \"$2/.nixpacks/Dockerfile\"\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nixpacks"), []byte(nix), 0o700))
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		return string(raw)
	}
	for name, nixpacks := range map[string]*deployment.NixpacksCommands{"dockerfile": nil, "nixpacks": {}} {
		for _, f := range []string{"login", "nixenv"} {
			_ = os.Remove(filepath.Join(dir, f))
		}
		err := (&LocalOrchestrator{}).BuildFromArchive(context.Background(), sourceTar(t, map[string]string{"Dockerfile": "FROM scratch\n"}, nil),
			"", testImageTag, nixpacks, nil, &deployment.RegistryAuth{Server: "ghcr.io", Username: "bot", Password: "hunter2"}, func(string) {})
		require.NoError(t, err, name)
		assert.JSONEq(t, `{"auths":{"ghcr.io":{"auth":"Ym90Omh1bnRlcjI="}}}`, read("login"), name)
		assert.NotContains(t, read("args"), "hunter2", name)
		var cfg string
		for _, line := range strings.Split(read("env"), "\n") {
			if v, ok := strings.CutPrefix(line, "DOCKER_CONFIG="); ok {
				cfg = v
			}
		}
		require.NotEmpty(t, cfg, name)
		assert.NoDirExists(t, cfg, "%s: the login is removed after the build", name)
		if nixpacks != nil {
			assert.NotContains(t, read("nixenv"), "DOCKER_CONFIG", "nixpacks gets no login")
		}

		for _, none := range []*deployment.RegistryAuth{nil, {Server: "ghcr.io", Username: "bot"}} {
			_ = os.Remove(filepath.Join(dir, "login"))
			err := (&LocalOrchestrator{}).BuildFromArchive(context.Background(), sourceTar(t, map[string]string{"Dockerfile": "FROM scratch\n"}, nil),
				"", testImageTag, nixpacks, nil, none, func(string) {})
			require.NoError(t, err, name)
			assert.Empty(t, read("login"), "%s: no password, no login", name)
			assert.Equal(t, "cli-plugins\n", read("config"), name)
		}
	}
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
			tc.baseDir, testImageTag, nil, nil, nil, func(string) {})
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
	shellNixpacks(t)
	script := "#!/bin/sh\necho \"$1\" >> " + dir + "/nixran\nenv >> " + dir + "/nixenv\n[ \"$1\" = plan ] && echo '{}'\n" +
		"[ \"$1\" = build ] && mkdir \"$2/.nixpacks\" && echo 'FROM scratch' > \"$2/.nixpacks/Dockerfile\"\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nixpacks"), []byte(script), 0o700))
	t.Setenv("JWT_SECRET", "the backend's")
	build := func(links map[string]string) error {
		return (&LocalOrchestrator{}).BuildFromArchive(context.Background(), sourceTar(t, map[string]string{"package.json": "{}"}, links),
			"", testImageTag, &deployment.NixpacksCommands{}, nil, nil, func(string) {})
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

// As on a server (TestRemoteNixpacksRefusesAPlanThatWritesOutsideItsFolder,
// TestRemoteNixpacksConfigFileStaysInTheRepository): on the panel's host
// nixpacks runs as the backend, and writes nothing for a plan that names a
// file outside its folder, nor starts for a config file outside the
// repository.
func TestLocalNixpacksWritesAndReadsInItsFolderOnly(t *testing.T) {
	dir := fakeDocker(t)
	shellNixpacks(t)
	script := "#!/bin/sh\necho \"$1\" >> " + dir + "/nixran\n[ \"$1\" = plan ] && cat " + dir + "/plan\n" +
		"[ \"$1\" = build ] && mkdir \"$2/.nixpacks\" && echo 'FROM scratch' > \"$2/.nixpacks/Dockerfile\"\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nixpacks"), []byte(script), 0o700))
	build := func(plan string, buildArgs map[string]*string) error {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "plan"), []byte(plan), 0o600))
		return (&LocalOrchestrator{}).BuildFromArchive(context.Background(), sourceTar(t, map[string]string{"package.json": "{}"}, nil),
			"", testImageTag, &deployment.NixpacksCommands{}, buildArgs, nil, func(string) {})
	}
	file := "/etc/toft/config.toml"
	err := build("{}", map[string]*string{"NIXPACKS_CONFIG_FILE": &file})
	require.ErrorIs(t, err, service.ErrInvalidConfig)
	assert.NoFileExists(t, filepath.Join(dir, "nixran"), "nixpacks did not run")

	for name, plan := range map[string]string{
		"an asset":          `{"staticAssets":{"../../../../usr/local/bin/nixpacks":"#!/bin/sh\n"}}`,
		"a nixpkgs archive": `{"phases":{"setup":{"nixpkgsArchive":"/../../x"}}}`,
	} {
		require.ErrorIs(t, build(plan, nil), service.ErrInvalidConfig, name)
	}
	ran, err := os.ReadFile(filepath.Join(dir, "nixran"))
	require.NoError(t, err)
	assert.Equal(t, "plan\nplan\n", string(ran), "the Dockerfile and the plan's files were never written")
}

// shellNixpacks runs the test's nixpacks, a shell script, as the backend:
// nixpacks' sandbox has no /bin/sh. TestLocalNixpacksRunsInItsSandbox
// covers the sandbox.
func shellNixpacks(t *testing.T) {
	was := nixpacksSandboxed
	nixpacksSandboxed = false
	t.Cleanup(func() { nixpacksSandboxed = was })
}

// On the panel's host the backend is root, and nixpacks runs as
// toft-sandbox (65532) in a root of its own: no capabilities, no environment, nothing of the host
// but its own binary and the build's folder, and no way back out. The fake
// nixpacks is a static Go program that tries each of these and says what
// it found, and leaves a child running that tries to climb out once its
// folder is handed back; a process of another user's, nobody's, outlives
// the sandbox. It needs root on Linux, in a container of its own: the
// sandbox kills every process of 65532's, and on a host some of those can
// be other containers' (distroless images' nonroot). make test-linux and
// CI's step set TOFT_TEST_SANDBOX for it.
func TestLocalNixpacksRunsInItsSandbox(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || os.Getenv("TOFT_TEST_SANDBOX") != "1" {
		t.Skip("needs root on Linux, in a container: make test-linux")
	}
	dir := fakeDocker(t)
	other := filepath.Join(t.TempDir(), "paas-build-other")
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(other, "secret"), []byte("another deploy's"), 0o644))
	src := filepath.Join(t.TempDir(), "main.go")
	require.NoError(t, os.WriteFile(src, []byte(strings.ReplaceAll(sandboxProbe, "OTHER", other)), 0o644))
	gobuild := exec.Command("go", "build", "-o", filepath.Join(dir, "nixpacks"), src)
	gobuild.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := gobuild.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Setenv("JWT_SECRET", "the backend's")
	const escaped = "/tmp/toft-sandbox-escaped"
	require.NoError(t, os.RemoveAll(escaped))
	bystander := exec.Command("sleep", "60")
	bystander.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	require.NoError(t, bystander.Start())
	t.Cleanup(func() { _ = bystander.Process.Kill(); _ = bystander.Wait() })

	got := map[string]string{}
	linger := 0
	err = (&LocalOrchestrator{}).BuildFromArchive(context.Background(), sourceTar(t, map[string]string{"package.json": "{}"}, map[string]string{"Procfile": "package.json"}),
		"", testImageTag, &deployment.NixpacksCommands{}, nil, nil, func(l string) {
			if k, v, ok := strings.Cut(strings.TrimPrefix(l, "sandbox "), ": "); ok && strings.HasPrefix(l, "sandbox ") {
				got[k] = v
			}
			if p, ok := strings.CutPrefix(l, "linger pid: "); ok {
				linger, _ = strconv.Atoi(p)
			}
		})
	require.NoError(t, err)
	const gone = "no such file or directory"
	assert.Equal(t, map[string]string{
		"ids":              "65532 65532 65532 65532 []",
		"capabilities":     "[0 0 0 0 0 0]",
		"environment":      "[]",
		"root":             "[nixpacks tmp]",
		"/etc/passwd":      gone,
		"/proc/self":       gone,
		"/dev/null":        gone,
		"/var/run":         gone,
		other:              gone,
		"linger":           "ok",
		"fds":              "0",
		"chroot":           "operation not permitted",
		"setuid 0":         "operation not permitted",
		"climb, /etc":      gone,
		"write beside dir": "permission denied",
		"a link in dir":    "{}",
	}, got)
	// The one left running was killed before its folder left the sandbox,
	// so it never got out.
	require.NotZero(t, linger)
	time.Sleep(200 * time.Millisecond)
	if status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", linger)); err == nil {
		assert.Regexp(t, `(?m)^State:\s+Z`, string(status), "nixpacks' child is still running")
		assert.Regexp(t, `(?m)^Threads:\s+1$`, string(status), "a thread of nixpacks' child is still running")
	}
	assert.NoFileExists(t, escaped, "nixpacks' child climbed out of the sandbox")
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", bystander.Process.Pid))
	require.NoError(t, err)
	assert.Regexp(t, `(?m)^State:\s+S`, string(status), "the sandbox killed a process of another user's")
	files, err := os.ReadFile(filepath.Join(dir, "context"))
	require.NoError(t, err)
	assert.Equal(t, "./.nixpacks/Dockerfile\n./package.json\n", string(files), "the build got its folder back, with nixpacks' Dockerfile")
	left, err := filepath.Glob(filepath.Join(os.TempDir(), "paas-nix-*"))
	require.NoError(t, err)
	assert.Empty(t, left, "the sandbox's root is removed")
}

// sandboxProbe is TestLocalNixpacksRunsInItsSandbox's nixpacks. OTHER is
// another build's folder.
const sandboxProbe = `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

// linger is the child nixpacks leaves running. From its folder, wherever
// that is moved, it climbs, and outside the sandbox leaves
// /tmp/toft-sandbox-escaped. The folder is its own while it runs: open to
// all, it stays open once handed back to root. Its first thread exits
// alone, which shows the process as a zombie while the climbing thread
// runs on; it writes "z" to its output first.
func init() {
	if len(os.Args) < 2 || os.Args[1] != "linger" {
		return
	}
	_ = os.Chmod(".", 0o777)
	here, err := os.Open(".")
	if err != nil {
		return
	}
	go func() {
		for {
			_ = here.Chdir()
			for i := 0; i < 40; i++ {
				_ = os.Chdir("..")
			}
			if _, err := os.Lstat("etc/passwd"); err == nil {
				_ = os.WriteFile("tmp/toft-sandbox-escaped", []byte("x"), 0o644)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	_, _ = os.Stdout.WriteString("z")
	syscall.RawSyscall(syscall.SYS_EXIT, 0, 0, 0)
}

func main() {
	if os.Args[1] == "plan" {
		fmt.Println("{}")
		return
	}
	dir := os.Args[2]
	say := func(k string, v any) { fmt.Printf("sandbox %s: %v\n", k, v) }
	why := func(err error) string {
		if pe, ok := err.(*os.PathError); ok {
			err = pe.Err
		}
		if err == nil {
			return "ok"
		}
		return err.Error()
	}
	groups, _ := os.Getgroups()
	say("ids", fmt.Sprint(os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid(), groups))
	// capget, version 3: effective, permitted and inheritable, two words each.
	hdr := [2]uint32{0x20080522, 0}
	var caps [6]uint32
	if _, _, e := syscall.RawSyscall(syscall.SYS_CAPGET, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&caps)), 0); e != 0 {
		say("capabilities", e.Error())
	} else {
		say("capabilities", caps)
	}
	say("environment", os.Environ())
	entries, _ := os.ReadDir("/")
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	say("root", names)
	for _, p := range []string{"/etc/passwd", "/proc/self", "/dev/null", "/var/run", "OTHER"} {
		_, err := os.Lstat(p)
		say(p, why(err))
	}
	fds := 0
	for fd := 3; fd < 256; fd++ {
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) == nil {
			fds++
		}
	}
	say("fds", fds)
	// One left running, in a session of its own, with no output to wait for:
	// its stdio is a removed file (Go's runtime wants fds 0 to 2, and there
	// is no /dev/null).
	out, err := os.Create(filepath.Join(dir, "linger-out"))
	if err != nil {
		panic(err)
	}
	_ = os.Remove(out.Name())
	fd := out.Fd()
	pid, err := syscall.ForkExec("/nixpacks", []string{"/nixpacks", "linger"}, &syscall.ProcAttr{Dir: dir, Files: []uintptr{fd, fd, fd}, Sys: &syscall.SysProcAttr{Setsid: true}})
	say("linger", why(err))
	fmt.Printf("linger pid: %d\n", pid)
	// Until its first thread has gone.
	for i := 0; i < 200; i++ {
		if n, _ := out.ReadAt(make([]byte, 1), 0); n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	say("chroot", why(syscall.Chroot(".")))
	say("setuid 0", why(syscall.Setuid(0)))
	for i := 0; i < 20; i++ {
		_ = os.Chdir("..")
	}
	_, err = os.Lstat("etc/passwd")
	say("climb, /etc", why(err))
	say("write beside dir", why(os.WriteFile(filepath.Join(dir, "..", "escape"), []byte("x"), 0o644)))
	if b, err := os.ReadFile(filepath.Join(dir, "Procfile")); err == nil {
		say("a link in dir", string(b))
	} else {
		say("a link in dir", why(err))
	}
	if err := os.MkdirAll(filepath.Join(dir, ".nixpacks"), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".nixpacks", "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		panic(err)
	}
}
`

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
