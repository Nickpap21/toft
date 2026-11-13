package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/docker/docker/api/types/container"
)

// fakeCompose points the agent at a docker CLI that records what it was
// given in the returned directory, and a release of testSvc to deploy.
func fakeCompose(t *testing.T) (out, release string) {
	t.Helper()
	stateDir = t.TempDir()
	out = t.TempDir()
	saved, savedRoot := dockerBin, rollout.ComposeRoot
	t.Cleanup(func() { dockerBin, rollout.ComposeRoot = saved, savedRoot })
	rollout.ComposeRoot = t.TempDir()
	release = filepath.Join(rollout.ComposeDir(testSvc), "r-1")
	if err := os.MkdirAll(release, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, "docker-compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dockerBin = filepath.Join(out, "docker")
	script := "#!/bin/sh\n" +
		`echo "$@" >> ` + out + "/args\n" +
		`cat "$3/.env" > ` + out + "/env\n" +
		`[ -n "$DOCKER_CONFIG" ] && cat "$DOCKER_CONFIG/config.json" > ` + out + "/config\n" +
		"env > " + out + "/procenv\n" +
		"echo ' Container web Started'\n"
	if err := os.WriteFile(dockerBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return out, release
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
	want = want + " up --no-start --pull always --remove-orphans\n" + want + " up -d --pull never --no-build --no-recreate --remove-orphans\n"
	if args != want {
		t.Fatalf("args\n%q\nwant\n%q", args, want)
	}
	if read(t, out+"/env") != "SECRET=s3\n" {
		t.Fatalf("env file %q", read(t, out+"/env"))
	}
	if cfg := read(t, out+"/config"); !strings.Contains(cfg, `"ghcr.io"`) || !strings.Contains(cfg, "dTpodW50ZXIy") {
		t.Fatalf("docker config %q", cfg)
	}
	if env := read(t, out+"/procenv"); strings.Contains(env, "JWT_SECRET") {
		t.Fatalf("compose ran with the agent's environment: %s", env)
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

// A create that fails (a pull or a build) has recreated nothing: the running
// stack still carries the last good deploy's routes. The result is the
// create's error, and the stack is neither checked nor taken down.
func TestAgentLeavesTheRunningStackAloneWhenCreateFails(t *testing.T) {
	out, release := fakeCompose(t)
	if err := os.WriteFile(dockerBin, []byte("#!/bin/sh\necho \"$@\" >> "+out+"/args\n"+
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
	if err := os.WriteFile(dockerBin, []byte("#!/bin/sh\necho \"$@\" >> "+out+"/args\n"+
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
	if _, err := os.Stat(out + "/config"); !os.IsNotExist(err) {
		t.Fatal("the credentials were journaled")
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
