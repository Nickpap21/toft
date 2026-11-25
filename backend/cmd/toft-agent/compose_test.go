package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/builder"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/Nickpap21/toft/backend/internal/testenv"
	"github.com/docker/docker/api/types/container"
)

// fakeCompose points the agent at a docker CLI that records what it was
// given in the returned directory, and a release of testSvc to deploy. A
// builder's lock, root's in /run, is in a folder of the test's. Beside
// docker is an nft that lists what it is asked for (firewall): a test that
// builds puts the directory first on PATH.
func fakeCompose(t *testing.T) (out, release string) {
	t.Helper()
	stateDir = t.TempDir()
	out = t.TempDir()
	saved, savedRoot, savedLock := dockerBin, rollout.ComposeRoot, builder.LockDir
	t.Cleanup(func() { dockerBin, rollout.ComposeRoot, builder.LockDir = saved, savedRoot, savedLock })
	rollout.ComposeRoot = t.TempDir()
	builder.LockDir = t.TempDir()
	release = filepath.Join(rollout.ComposeDir(testSvc), "r-1")
	if err := os.MkdirAll(release, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, "docker-compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dockerBin = filepath.Join(out, "docker")
	script := "#!/bin/sh\n" + answersConfig(stackPulls) +
		`echo "$@" >> ` + out + "/args\n" +
		`cat "$3/.env" > ` + out + "/env\n" +
		`[ -n "$DOCKER_CONFIG" ] && cat "$DOCKER_CONFIG/config.json" > ` + out + "/config\n" +
		"env > " + out + "/procenv\n" +
		"echo ' Container web Started'\n"
	if err := os.WriteFile(dockerBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	firewall(t, out, true)
	return out, release
}

// firewall is the server's nft in dir: one that lists the chain a build
// asks for, or one on which Toft's rules aren't loaded. What it was asked
// goes in the file asked.
func firewall(t *testing.T, dir string, loaded bool) {
	t.Helper()
	script := "#!/bin/sh\necho \"$*\" >> " + dir + "/asked\n"
	if !loaded {
		script += "echo 'Error: No such file or directory' >&2\nexit 1\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "nft"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

// What docker compose config answers for a stack that builds nothing, and
// for one that builds.
const (
	stackPulls  = `{"services":{"web":{"image":"example/web"}}}`
	stackBuilds = `{"services":{"web":{"build":{"context":"."}}}}`
)

// answersConfig is the start of a fake docker's script: it answers docker
// compose config, the reading that says whether the stack builds, with
// stack, and docker compose version with one that builds a stack.
func answersConfig(stack string) string {
	return `case "$*" in *' config --format json') echo '` + stack + `'; exit 0;; 'compose version --short') echo 5.7.2; exit 0;; esac` + "\n"
}

func composeRequest(release string) rollout.Request {
	return rollout.Request{Compose: &rollout.ComposeUp{
		DeploymentID: "dep-c", ServiceID: testSvc, ServiceName: "web",
		ProjectDir: release, ComposeFile: release + "/docker-compose.yml",
		Overrides: []rollout.ComposeOverride{{Name: "paas-route.yml", Content: []byte("services: {}\n")}},
		DotEnv:    []byte("SECRET=s3\n"),
		Auth:      &rollout.RegistryAuth{Server: "ghcr.io", Username: "u", Password: "hunter2"},
	}}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, _ := os.ReadFile(path)
	return string(raw)
}

// The agent runs a compose deploy's up with the env file and the overrides
// it writes, in a clean environment with the registry's credentials in a
// config of its own, and removes them all once up ends.
func TestAgentRunsAComposeUp(t *testing.T) {
	out, release := fakeCompose(t)
	t.Setenv("JWT_SECRET", "not for compose")
	m := testRollouts(fakedocker.New(t))
	req := composeRequest(release)
	if err := checkComposeUp(*req.Compose); err != nil {
		t.Fatal(err)
	}
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()

	rec, err := readRecord("dep-c")
	if err != nil || rec.Progress.Result == nil || rec.Progress.Result.Step != "" || rec.Request != nil {
		t.Fatalf("record %+v %v", rec, err)
	}
	args := read(t, out+"/args")
	want := "compose --project-directory " + release + " -f " + release + "/docker-compose.yml -f " +
		filepath.Join(rolloutDir(), "dep-c.d", "paas-route.yml") + " --project-name " +
		rollout.ComposeProjectName(testSvc, "web")
	want = want + " up --no-start --pull always --remove-orphans --no-build\n" + want + " up -d --pull never --no-build --no-recreate --remove-orphans\n"
	if args != want {
		t.Fatalf("args\n%q\nwant\n%q", args, want)
	}
	if read(t, out+"/env") != "SECRET=s3\n" {
		t.Fatalf("env file %q", read(t, out+"/env"))
	}
	if cfg := read(t, out+"/config"); !strings.Contains(cfg, `"ghcr.io"`) || !strings.Contains(cfg, "dTpodW50ZXIy") {
		t.Fatalf("docker config %q", cfg)
	}
	env := read(t, out+"/procenv")
	if strings.Contains(env, "JWT_SECRET") {
		t.Fatalf("compose ran with the agent's environment: %s", env)
	}
	if !strings.Contains(env, "BUILDX_GIT_INFO=false\n") || !strings.Contains(env, "BUILDX_GIT_LABELS=false\n") {
		t.Fatalf("a build compose starts would read git in its context: %s", env)
	}
	for _, p := range []string{release + "/.env", filepath.Join(rolloutDir(), "dep-c.d")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s was left behind", p)
		}
	}
	if log := read(t, logFile("dep-c")); !strings.Contains(log, "Container web Started") {
		t.Fatalf("log %q", log)
	}
}

// Without credentials the up still runs with a docker config of its own,
// holding none: a login made on the server is not the deploy's to pull with.
func TestAgentComposeWithoutCredentialsHasAnEmptyConfig(t *testing.T) {
	out, release := fakeCompose(t)
	m := testRollouts(fakedocker.New(t))
	req := composeRequest(release)
	req.Compose.Auth = nil
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if cfg := read(t, out+"/config"); cfg != `{"auths":{}}` {
		t.Fatalf("docker config %q", cfg)
	}
}

// Docker Hub's credentials go under the key the docker CLI looks Hub up
// by, whichever of its names the service's registry was given as.
func TestAgentDockerConfigKeysDockerHubAsTheCLIDoes(t *testing.T) {
	for _, server := range []string{"", "docker.io", "index.docker.io", "registry-1.docker.io"} {
		cfg, err := dockerConfig(t.TempDir(), &rollout.RegistryAuth{Server: server, Username: "u", Password: "hunter2"})
		if err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(cfg, "config.json")); got != `{"auths":{"https://index.docker.io/v1/":{"auth":"dTpodW50ZXIy"}}}` {
			t.Fatalf("%q: docker config %q", server, got)
		}
	}
}

// A stack whose images route themselves is taken down after up.
func TestAgentTakesDownAStackThatRoutesItself(t *testing.T) {
	out, release := fakeCompose(t)
	f := fakedocker.New(t)
	proj := rollout.ComposeProjectName(testSvc, "web")
	f.Add(proj+"-db-1", container.StateRunning, map[string]string{
		rollout.LabelComposeProject: proj, "com.docker.compose.service": "db", "traefik.enable": "true",
	})
	m := testRollouts(f)
	req := composeRequest(release)
	req.Compose.RoutedService = "web"
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-c"); rec.Progress.Result == nil || rec.Progress.Result.Step != rollout.StepComposeUp ||
		!strings.Contains(rec.Progress.Result.Err, "traefik.enable") {
		t.Fatalf("result %+v", rec.Progress.Result)
	}
	if args := read(t, out+"/args"); !strings.HasSuffix(args, " --project-name "+proj+" down --remove-orphans\n") {
		t.Fatalf("args %q", args)
	}
}

// A routed service whose image adds routers of its own is refused on its
// created container, before anything starts: the stack is taken down and
// up -d never runs.
func TestAgentRefusesAStackThatRoutesItselfBeforeItStarts(t *testing.T) {
	out, release := fakeCompose(t)
	f := fakedocker.New(t)
	proj := rollout.ComposeProjectName(testSvc, "web")
	f.Add(proj+"-web-1", container.StateCreated, map[string]string{
		rollout.LabelComposeProject: proj, "com.docker.compose.service": "web",
		"traefik.enable": "true", "traefik.http.routers.paas-1234abcd-x.rule": "Host(`victim.example`)",
	})
	m := testRollouts(f)
	req := composeRequest(release)
	req.Compose.RoutedService = "web"
	req.Compose.AllowedLabels = map[string]bool{"traefik.enable": true}
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-c"); rec.Progress.Result == nil || rec.Progress.Result.Step != rollout.StepComposeUp ||
		!strings.Contains(rec.Progress.Result.Err, "traefik.http.routers.paas-1234abcd-x.rule") {
		t.Fatalf("result %+v", rec.Progress.Result)
	}
	args := read(t, out+"/args")
	if strings.Contains(args, " up -d ") || !strings.HasSuffix(args, " --project-name "+proj+" down --remove-orphans\n") {
		t.Fatalf("args %q", args)
	}
}

// A stack that docker compose config shows as building goes up on Toft's
// builder: the agent makes sure of it first (ensure.sh, with the deploy's
// own Docker config), then runs the create with --build and the builder
// named in its environment.
func TestAgentBuildsAStackOnTheBuilder(t *testing.T) {
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		testenv.Skip(t, "the builder's memory limit is read from /proc/meminfo")
	}
	out, release := fakeCompose(t)
	// ensure.sh finds docker on PATH; the network is there, the builder's
	// container isn't.
	t.Setenv("PATH", out+":"+os.Getenv("PATH"))
	script := "#!/bin/sh\n" + answersConfig(stackBuilds) +
		`echo "$DOCKER_CONFIG|$BUILDX_BUILDER|$*" >> ` + out + "/args\n" +
		`case "$1 $2" in "network inspect") echo "bridge|$TOFT_BUILDER_BRIDGE|true|build|false";; "container inspect") exit 1;; esac` + "\n"
	if err := os.WriteFile(dockerBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	m := testRollouts(fakedocker.New(t))
	if err := m.start(composeRequest(release)); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-c"); rec.Progress.Result == nil || rec.Progress.Result.Step != "" {
		t.Fatalf("result %+v\n%s", rec.Progress, read(t, logFile("dep-c")))
	}
	cfg := filepath.Join(rolloutDir(), "dep-c.d", "docker")
	stack := "compose --project-directory " + release + " -f " + release + "/docker-compose.yml -f " +
		filepath.Join(rolloutDir(), "dep-c.d", "paas-route.yml") + " --project-name " + rollout.ComposeProjectName(testSvc, "web")
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(read(t, out+"/args")), "\n") {
		// The builder's settings are ensure.sh's own; its commands are what counts here.
		if words := strings.Fields(line); len(words) > 2 && strings.HasSuffix(words[0], "|buildx") {
			line = strings.Join(words[:2], " ")
		}
		got = append(got, line)
	}
	want := []string{
		cfg + "||buildx version",
		cfg + "||network inspect -f {{.Driver}}|{{index .Options \"com.docker.network.bridge.name\"}}|{{index .Labels \"paas.managed\"}}|{{index .Labels \"paas.network.kind\"}}|{{.EnableIPv6}} toft-build",
		cfg + "||container inspect -f {{.HostConfig.NetworkMode}}|{{range .Config.Env}}{{println .}}{{end}} buildx_buildkit_toft-build0",
		// Again, once the check has the builder's lock.
		cfg + "||container inspect -f {{.HostConfig.NetworkMode}}|{{range .Config.Env}}{{println .}}{{end}} buildx_buildkit_toft-build0",
		cfg + "||buildx rm",
		cfg + "||buildx create",
		cfg + "||buildx inspect",
		cfg + "||exec buildx_buildkit_toft-build0 sh -c p=$(pidof buildkitd) && for i in $p; do echo 1000 >/proc/$i/oom_score_adj || exit 1; done",
		cfg + "|toft-build|" + stack + " up --no-start --pull always --remove-orphans --build",
		cfg + "||" + stack + " up -d --pull never --no-build --no-recreate --remove-orphans",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("docker ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A panel rolled back to 1.2.3 sends no builder with a compose deploy (the
// request had none then). The agent builds on its own toft-build, on the
// bridge the firewall holds, never on Docker's default builder, and asks
// its server's firewall first as for any build: where the rules aren't
// loaded, it builds nothing.
func TestAPanelRolledBackTo123BuildsOnTheAgentsOwnBuilder(t *testing.T) {
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		testenv.Skip(t, "the builder's memory limit is read from /proc/meminfo")
	}
	out, release := fakeCompose(t)
	t.Setenv("PATH", out+":"+os.Getenv("PATH"))
	script := "#!/bin/sh\n" + answersConfig(stackBuilds) +
		`echo "builder=$BUILDX_BUILDER|$*" >> ` + out + "/args\n" +
		`case "$1 $2" in "network inspect") echo "bridge|$TOFT_BUILDER_BRIDGE|true|build|false";; "container inspect") exit 1;; esac` + "\n"
	if err := os.WriteFile(dockerBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// 1.2.3's request has no Builder field: the agent reads it as none.
	req := composeRequest(release)
	if req.Compose.Builder != nil {
		t.Fatalf("the request names a builder, which 1.2.3's doesn't: %+v", req.Compose.Builder)
	}
	m := testRollouts(fakedocker.New(t))
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-c"); rec.Progress.Result == nil || rec.Progress.Result.Step != "" {
		t.Fatalf("result %+v\n%s", rec.Progress, read(t, logFile("dep-c")))
	}
	if asked := read(t, out+"/asked"); asked != "list chain ip toft from_untrusted\n" {
		t.Fatalf("the firewall was asked %q", asked)
	}
	ran := read(t, out+"/args")
	created, built := false, false
	for _, line := range strings.Split(strings.TrimSpace(ran), "\n") {
		named, cmd, _ := strings.Cut(line, "|")
		switch {
		case strings.HasPrefix(cmd, "buildx create "):
			created = true
			for _, want := range []string{"--name toft-build ", "--driver docker-container", "--driver-opt network=toft-build "} {
				if !strings.Contains(cmd+" ", want) {
					t.Fatalf("the builder was made without %q:\n%s", want, cmd)
				}
			}
		case strings.HasSuffix(cmd, " --build") || strings.Contains(cmd, " --build ") || strings.Contains(cmd, " build "):
			built = true
			if named != "builder=toft-build" {
				t.Fatalf("a build ran on %q, not on the agent's toft-build:\n%s", named, cmd)
			}
		}
	}
	if !created || !built {
		t.Fatalf("made the builder: %v, built: %v\n%s", created, built, ran)
	}

	// The server's firewall isn't loaded.
	firewall(t, out, false)
	if err := os.Remove(out + "/args"); err != nil {
		t.Fatal(err)
	}
	req = composeRequest(release)
	req.Compose.DeploymentID = "dep-d"
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-d"); rec.Progress.Result == nil || !strings.Contains(rec.Progress.Result.Err, "Toft's rules aren't loaded on it") {
		t.Fatalf("result %+v", rec.Progress.Result)
	}
	if ran := read(t, out+"/args"); strings.Contains(ran, "buildx") || strings.Contains(ran, " up ") {
		t.Fatalf("docker built without the firewall:\n%s", ran)
	}
}

// The check of the builder refuses on the agent's server (Docker has no
// buildx): the agent runs no compose up at all, and the deploy fails with
// the check's words.
func TestAgentRunsNothingWhenTheBuilderIsRefused(t *testing.T) {
	out, release := fakeCompose(t)
	t.Setenv("PATH", out+":"+os.Getenv("PATH"))
	script := "#!/bin/sh\n" + answersConfig(stackBuilds) + `echo "$*" >> ` + out + "/args\n" +
		`[ "$1 $2" = "buildx version" ] && exit 1` + "\nexit 0\n"
	if err := os.WriteFile(dockerBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	m := testRollouts(fakedocker.New(t))
	if err := m.start(composeRequest(release)); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	const want = "docker compose up: the builder toft-build: Docker's buildx plugin is missing"
	if rec, _ := readRecord("dep-c"); rec.Progress.Result == nil || rec.Progress.Result.Step != rollout.StepComposeUp ||
		!strings.HasPrefix(rec.Progress.Result.Err, want) {
		t.Fatalf("result %+v\n%s", rec.Progress.Result, read(t, logFile("dep-c")))
	}
	if ran := strings.TrimSpace(read(t, out+"/args")); ran != "buildx version" {
		t.Fatalf("docker ran, after the check refused:\n%s", ran)
	}
}

// The builder is the panel's to describe: an agent whose own BuildKit is
// another release's makes the builder the request names, so the container
// the panel's builds on the server started is kept, not removed under a
// build that runs on it. One that isn't Toft's builder is refused before
// docker runs.
func TestAgentMakesThePanelsBuilder(t *testing.T) {
	panels := builder.Spec{Name: "toft-build", Bridge: "toft-ubuild", Memory: 1 << 30, Firewall: true,
		Image: "moby/buildkit:v0.1.0@sha256:" + strings.Repeat("a", 64), Cache: "20GB"}
	if panels.Image == builder.Image {
		t.Fatal("the test needs an image that isn't the agent's own")
	}
	out, release := fakeCompose(t)
	t.Setenv("PATH", out+":"+os.Getenv("PATH"))
	// The builder's container is there, made for the panel's settings.
	script := "#!/bin/sh\n" + answersConfig(stackBuilds) + `echo "$*" >> ` + out + "/args\n" +
		`case "$1 $2" in "network inspect") echo "bridge|$TOFT_BUILDER_BRIDGE|true|build|false";; ` +
		`"container inspect") printf 'toft-build|TOFT_BUILDER_SPEC=%s\nPATH=/bin\n' "` + panels.Image + ` toft-build 1073741824  20GB";; esac` + "\n"
	if err := os.WriteFile(dockerBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	req := composeRequest(release)
	req.Compose.Builder = &panels
	m := testRollouts(fakedocker.New(t))
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-c"); rec.Progress.Result == nil || rec.Progress.Result.Step != "" {
		t.Fatalf("result %+v\n%s", rec.Progress, read(t, logFile("dep-c")))
	}
	ran := read(t, out+"/args")
	if strings.Contains(ran, "rm -f") {
		t.Fatalf("the agent removed the builder's container:\n%s", ran)
	}
	for _, want := range []string{"--driver-opt image=" + panels.Image + " ", "--driver-opt memory=1073741824 ", " up --no-start --pull always --remove-orphans --build"} {
		if !strings.Contains(ran, want) {
			t.Fatalf("docker didn't run %q:\n%s", want, ran)
		}
	}

	if err := os.Remove(out + "/args"); err != nil {
		t.Fatal(err)
	}
	req = composeRequest(release)
	req.Compose.DeploymentID = "dep-d"
	req.Compose.Builder = &builder.Spec{Name: "default", Bridge: "docker0"}
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-d"); rec.Progress.Result == nil || !strings.HasPrefix(rec.Progress.Result.Err, "the panel's builder: no builder of Toft's is") {
		t.Fatalf("result %+v", rec.Progress.Result)
	}
	if ran := read(t, out+"/args"); ran != "" {
		t.Fatalf("docker ran for a builder that isn't Toft's:\n%s", ran)
	}

	// A builder that is no deploy's builds nothing: the stack is read, and
	// that is all. A deploy's, for an untrusted preview, builds on that
	// builder once the server's firewall has answered, and builds nothing
	// where the rules aren't loaded.
	untrusted := builder.Spec{Name: "toft-build-untrusted", Bridge: "toft-upbuild", Memory: 1 << 30}
	req = composeRequest(release)
	req.Compose.DeploymentID, req.Compose.Builder = "dep-e", &untrusted
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-e"); rec.Progress.Result == nil || !strings.Contains(rec.Progress.Result.Err, "Toft's rules aren't loaded on it") {
		t.Fatalf("result %+v", rec.Progress.Result)
	}
	if ran := read(t, out+"/args"); strings.Contains(ran, "buildx") || strings.Contains(ran, " up ") {
		t.Fatalf("docker built without the firewall:\n%s", ran)
	}
	walled := untrusted
	walled.Firewall = true
	firewall(t, out, false)
	req = composeRequest(release)
	req.Compose.DeploymentID, req.Compose.Builder = "dep-g", &walled
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-g"); rec.Progress.Result == nil || !strings.Contains(rec.Progress.Result.Err, "Toft's rules aren't loaded on it") {
		t.Fatalf("result %+v", rec.Progress.Result)
	}
	if ran := read(t, out+"/args"); strings.Contains(ran, "buildx") || strings.Contains(ran, " up ") {
		t.Fatalf("docker built where the firewall isn't loaded:\n%s", ran)
	}
	firewall(t, out, true)
	req = composeRequest(release)
	req.Compose.DeploymentID, req.Compose.Builder = "dep-f", &walled
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-f"); rec.Progress.Result == nil || rec.Progress.Result.Step != "" {
		t.Fatalf("result %+v\n%s", rec.Progress, read(t, logFile("dep-f")))
	}
	if ran := read(t, out+"/args"); !strings.Contains(ran, "--driver-opt network=toft-build-untrusted ") {
		t.Fatalf("the agent didn't build on the untrusted builder:\n%s", ran)
	}
}

// A create that fails (a pull or a build) has recreated nothing: the running
// stack still carries the last good deploy's routes. The result is the
// create's error, and the stack is neither checked nor taken down.
func TestAgentLeavesTheRunningStackAloneWhenCreateFails(t *testing.T) {
	out, release := fakeCompose(t)
	if err := os.WriteFile(dockerBin, []byte("#!/bin/sh\n"+answersConfig(stackPulls)+"echo \"$@\" >> "+out+"/args\n"+
		"case \"$*\" in *' up --no-start '*) echo pull access denied; exit 1;; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := fakedocker.New(t)
	proj := rollout.ComposeProjectName(testSvc, "web")
	f.Add(proj+"-web-1", container.StateRunning, map[string]string{
		rollout.LabelComposeProject: proj, "com.docker.compose.service": "web",
		"traefik.enable": "true", "traefik.http.routers.paas-1234abcd-old.rule": "Host(`old.example.com`)",
	})
	m := testRollouts(f)
	req := composeRequest(release)
	req.Compose.RoutedService = "web"
	req.Compose.AllowedLabels = map[string]bool{"traefik.enable": true}
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-c"); rec.Progress.Result == nil || rec.Progress.Result.Step != rollout.StepComposeUp ||
		!strings.HasPrefix(rec.Progress.Result.Err, "docker compose up:") || strings.Contains(rec.Progress.Result.Err, "-old.rule") {
		t.Fatalf("result %+v", rec.Progress.Result)
	}
	if args := read(t, out+"/args"); strings.Contains(args, " down ") || strings.Contains(args, " up -d ") {
		t.Fatalf("args %q", args)
	}
}

// An up that fails after starting a container that routes itself still has
// the stack's routes checked, and the stack taken down.
func TestAgentTakesDownAStackThatRoutesItselfWhenUpFails(t *testing.T) {
	out, release := fakeCompose(t)
	// up -d fails, as when a dependency never turns healthy.
	if err := os.WriteFile(dockerBin, []byte("#!/bin/sh\n"+answersConfig(stackPulls)+"echo \"$@\" >> "+out+"/args\n"+
		"case \"$*\" in *' up -d '*) sleep 0.5; exit 1;; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := fakedocker.New(t)
	proj := rollout.ComposeProjectName(testSvc, "web")
	m := testRollouts(f)
	req := composeRequest(release)
	req.Compose.RoutedService = "web"
	req.Compose.AllowedLabels = map[string]bool{"traefik.enable": true}
	// The container appears once create has run: the check before start
	// passes, and up -d starts a container whose image routes itself.
	go func() {
		for !strings.Contains(read(t, out+"/args"), " up -d ") {
			time.Sleep(time.Millisecond)
		}
		f.Add(proj+"-web-1", container.StateRunning, map[string]string{
			rollout.LabelComposeProject: proj, "com.docker.compose.service": "web",
			"traefik.enable": "true", "traefik.http.routers.paas-1234abcd-x.rule": "Host(`victim.example`)",
		})
	}()
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	<-m.whenIdle()
	if rec, _ := readRecord("dep-c"); rec.Progress.Result == nil ||
		!strings.Contains(rec.Progress.Result.Err, "traefik.http.routers.paas-1234abcd-x.rule") {
		t.Fatalf("result %+v", rec.Progress.Result)
	}
	if args := read(t, out+"/args"); !strings.HasSuffix(args, " --project-name "+proj+" down --remove-orphans\n") {
		t.Fatalf("args %q", args)
	}
}

// An up the agent's restart interrupted runs once more; a second restart
// fails the deploy rather than running it a third time.
func TestAgentResumesAComposeUpOnce(t *testing.T) {
	out, release := fakeCompose(t)
	m := testRollouts(fakedocker.New(t))
	m.queues[testSvc] = []func(){func() {}} // the agent stopped before it ran
	if err := m.start(composeRequest(release)); err != nil {
		t.Fatal(err)
	}
	rec, _ := readRecord("dep-c")
	rec.Progress.Step = rollout.StepComposeUp
	if err := writeRecord(rec); err != nil {
		t.Fatal(err)
	}
	m = testRollouts(fakedocker.New(t))
	m.resume(context.Background())
	<-m.whenIdle()
	if rec, _ := readRecord("dep-c"); rec.Progress.Result == nil || rec.Progress.Result.Step != "" {
		t.Fatalf("resumed: %+v", rec.Progress)
	}
	if strings.Count(read(t, out+"/args"), "\n") != 2 { // create, then start
		t.Fatalf("args %q", read(t, out+"/args"))
	}
	if cfg := read(t, out+"/config"); cfg != `{"auths":{}}` {
		t.Fatalf("the credentials were journaled: %q", cfg)
	}

	rec, _ = readRecord("dep-c")
	rec.Request, rec.Progress = &rollout.Request{Compose: composeRequest(release).Compose}, rollout.Progress{Step: stepComposeAgain}
	if err := writeRecord(rec); err != nil {
		t.Fatal(err)
	}
	m = testRollouts(fakedocker.New(t))
	m.resume(context.Background())
	<-m.whenIdle()
	if rec, _ := readRecord("dep-c"); rec.Progress.Result == nil || rec.Progress.Result.Step != rollout.StepComposeUp {
		t.Fatalf("a second restart: %+v", rec.Progress)
	}
	if strings.Count(read(t, out+"/args"), "\n") != 2 {
		t.Fatal("a second restart ran up again")
	}
}

func TestComposeUpStaysInTheServicesReleases(t *testing.T) {
	_, release := fakeCompose(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(release, "escape")); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(c *rollout.ComposeUp){
		"another directory": func(c *rollout.ComposeUp) { c.ProjectDir = outside },
		"a symlink out":     func(c *rollout.ComposeUp) { c.ProjectDir = release + "/escape" },
		"dot-dot":           func(c *rollout.ComposeUp) { c.ComposeFile = release + "/../../x.yml" },
		"an override path":  func(c *rollout.ComposeUp) { c.Overrides[0].Name = "../x.yml" },
		"a bad service":     func(c *rollout.ComposeUp) { c.ServiceID = "../" + testSvc },
	} {
		c := *composeRequest(release).Compose
		c.Overrides = append([]rollout.ComposeOverride(nil), c.Overrides...)
		mut(&c)
		if err := checkComposeUp(c); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A command of the builder's check that the deploy's end interrupts is
// asked to end first (SIGTERM), so it can finish what it was doing.
func TestComposeHostOutputAsksACommandToEnd(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if _, err := os.Stat(dir + "/started"); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	_, err := composeHost{clean: os.Environ()}.Output(ctx, nil, "sh", "-c",
		"trap 'echo asked > "+dir+"/asked; exit 0' TERM; echo started > "+dir+"/started; while :; do sleep 0.05; done")
	if ctx.Err() == nil {
		t.Fatalf("the command ended by itself: %v", err)
	}
	if got := read(t, dir+"/asked"); got != "asked\n" {
		t.Fatalf("the command was killed without being asked (err %v)", err)
	}
}
