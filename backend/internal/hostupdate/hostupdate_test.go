package hostupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const runID = "0123456789abcdef0123456789abcdef"

// fakeHost runs Script with apt-get, dpkg-query, dpkg and systemctl
// replaced by stubs that log their arguments to calls.
type fakeHost struct {
	t        *testing.T
	dir, bin string
}

func newFakeHost(t *testing.T, docker, failInstall bool) *fakeHost {
	t.Helper()
	h := &fakeHost{t: t, dir: t.TempDir(), bin: t.TempDir()}
	dockerLine := ""
	if docker {
		dockerLine = `echo "install ok installed docker-ce"; echo "install ok installed containerd.io"`
	}
	statusLine := ""
	if docker {
		statusLine = `printf "install ok installed"`
	}
	fail := ""
	if failInstall {
		fail = `case "$*" in *" -s "*) ;; *dist-upgrade*) echo "E: Failed to fetch http://archive.ubuntu.com/tzdata.deb"; exit 100;; esac`
	}
	stubs := map[string]string{
		"apt-get": `echo "apt-get $*" >>"$CALLS"
case "$*" in *" -s "*) echo "Inst libaudit1 [1] (2 Ubuntu:24.04/noble-updates [amd64])"; echo "Inst tzdata [a] (b Ubuntu:24.04/noble-updates [all])";; esac
` + fail,
		"dpkg-query": `case "$*" in *Version*) printf "5:29.4.3-1";; *Package*) ` + dockerLine + `;; *Status*) ` + statusLine + `;; esac`,
		"dpkg":       `echo "dpkg $*" >>"$CALLS"`,
		"systemctl":  `echo "systemctl $*" >>"$CALLS"; [ "$1" = is-active ] && echo inactive; [ "$*" = "restart docker" ] && echo 29.4.3 >"$ENGINE"; exit 0`,
		// The engine running: what $ENGINE holds, else the package's.
		"docker": `cat "$ENGINE" 2>/dev/null || echo 29.4.3`,
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(h.bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *fakeHost) run(script string, args ...string) (string, error) {
	cmd := exec.Command("sh", append([]string{"-c", script, "sh"}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+h.bin+":"+os.Getenv("PATH"), "TOFT_HOST_UPDATE_DIR="+h.dir, "CALLS="+filepath.Join(h.dir, "calls"),
		"ENGINE="+filepath.Join(h.dir, "engine"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (h *fakeHost) status() Status {
	h.t.Helper()
	out, err := h.run(StatusScript)
	if err != nil {
		h.t.Fatalf("status: %v\n%s", err, out)
	}
	st, err := Parse(out, time.Now())
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

func (h *fakeHost) calls() string {
	b, _ := os.ReadFile(filepath.Join(h.dir, "calls"))
	return string(b)
}

func TestInstallHoldsDockerForTheRunOnly(t *testing.T) {
	h := newFakeHost(t, true, false)
	if out, err := h.run(Script, runID, ActionInstall); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	st := h.status()
	if st.ID != runID || st.Action != ActionInstall || st.Outcome != OutcomeSucceeded || st.Step != StepDone || st.Packages != 2 || st.Finished == nil {
		t.Fatalf("status = %+v", st)
	}
	pin, _ := os.ReadFile(filepath.Join(h.dir, "hold.pref"))
	for _, want := range []string{"Package: docker-ce\nPin: version 5:29.4.3-1\nPin-Priority: 1001", "Package: containerd.io\n"} {
		if !strings.Contains(string(pin), want) {
			t.Errorf("pin file lacks %q:\n%s", want, pin)
		}
	}
	calls := h.calls()
	for _, want := range []string{"dpkg --configure -a", "apt-get -q -o DPkg::Lock::Timeout=600 update",
		"-o Dpkg::Options::=--force-confold -o Dir::Etc::Preferences=" + filepath.Join(h.dir, "hold.pref") + " dist-upgrade"} {
		if !strings.Contains(calls, want) {
			t.Errorf("no %q in:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "apt-mark") || strings.Contains(calls, "autoremove") {
		t.Errorf("apt-mark or autoremove ran:\n%s", calls)
	}
}

func TestInstallFailureKeepsAptsError(t *testing.T) {
	h := newFakeHost(t, false, true)
	if _, err := h.run(Script, runID, ActionInstall); err == nil {
		t.Fatal("a failed install exited 0")
	}
	st := h.status()
	if st.Outcome != OutcomeFailed || st.Step != StepInstall || st.Finished == nil {
		t.Fatalf("status = %+v", st)
	}
	if want := "apt couldn't install the updates: Failed to fetch http://archive.ubuntu.com/tzdata.deb"; st.Error != want {
		t.Errorf("error = %q, want %q", st.Error, want)
	}
	if len(st.Output) == 0 || !strings.Contains(strings.Join(st.Output, "\n"), "E: Failed to fetch") {
		t.Errorf("output = %q", st.Output)
	}
}

func TestDockerUpdatesOnlyItsPackages(t *testing.T) {
	h := newFakeHost(t, true, false)
	if out, err := h.run(Script, runID, ActionDocker); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if st := h.status(); st.Outcome != OutcomeSucceeded {
		t.Fatalf("status = %+v", st)
	}
	if !strings.Contains(h.calls(), "--force-confold install --only-upgrade docker-ce containerd.io") {
		t.Errorf("calls:\n%s", h.calls())
	}
}

// A host where the package didn't restart Docker (a policy-rc.d): the run
// restarts it, so the new engine runs when it says it succeeded.
func TestDockerRestartedWhenThePackageDidnt(t *testing.T) {
	h := newFakeHost(t, true, false)
	if err := os.WriteFile(filepath.Join(h.dir, "engine"), []byte("29.3.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := h.run(Script, runID, ActionDocker); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if st := h.status(); st.Outcome != OutcomeSucceeded {
		t.Fatalf("status = %+v", st)
	}
	if !strings.Contains(h.calls(), "systemctl restart docker") {
		t.Errorf("Docker wasn't restarted:\n%s", h.calls())
	}
}

func TestDockerNotFromAPackage(t *testing.T) {
	h := newFakeHost(t, false, false)
	if _, err := h.run(Script, runID, ActionDocker); err == nil {
		t.Fatal("exited 0")
	}
	if st := h.status(); st.Outcome != OutcomeFailed || st.Error != "Docker wasn't installed from a package" {
		t.Fatalf("status = %+v", st)
	}
	if strings.Contains(h.calls(), "apt-get") {
		t.Errorf("apt ran:\n%s", h.calls())
	}
}

func TestRebootWritesRebootingFirst(t *testing.T) {
	h := newFakeHost(t, false, false)
	if out, err := h.run(Script, runID, ActionReboot); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if st := h.status(); st.Outcome != OutcomeRebooting || st.Step != StepReboot || st.Finished != nil {
		t.Fatalf("status = %+v", st)
	}
	if !strings.Contains(h.calls(), "systemctl reboot") {
		t.Errorf("calls:\n%s", h.calls())
	}
}

func TestStartLine(t *testing.T) {
	if _, err := StartLine("x; rm -rf /", ActionInstall); err == nil {
		t.Error("an invalid id went into the line")
	}
	if _, err := StartLine(runID, "install; reboot"); err == nil {
		t.Error("an invalid action went into the line")
	}
	line, err := StartLine(runID, ActionDocker)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "systemd-run --unit="+Unit+" --collect") || !strings.Contains(line, `"$D/run.sh" `+runID+" docker") {
		t.Errorf("line:\n%s", line)
	}
}

func TestParseBeforeAnyRunAndClockSkew(t *testing.T) {
	st, err := Parse("active:inactive\nnowboot:b2\nnow:1000\n--output--\n", time.Unix(1100, 0))
	if err != nil || st.ID != "" || st.Active || len(st.Output) != 0 {
		t.Fatalf("st = %+v, err = %v", st, err)
	}
	st, err = Parse("active:active\nnowboot:b1\nnow:1000\nid:"+runID+"\nstarted:900\nfinished:\nboot:b1\n--output--\nline one\n", time.Unix(1100, 0))
	if err != nil || !st.Active || !st.Started.Equal(time.Unix(1000, 0)) || st.Finished != nil || len(st.Output) != 1 {
		t.Fatalf("st = %+v, err = %v", st, err)
	}
	st, _ = Parse("active:inactive\n--output--\n(Reading database ... \r(Reading database ... 95%\r(Reading database ... 100%\r\nSetting up zlib1g ...\r\n\x1b[1mdone\n", time.Now())
	if want := []string{"(Reading database ... 100%", "Setting up zlib1g ...", "[1mdone"}; strings.Join(st.Output, "|") != strings.Join(want, "|") {
		t.Errorf("output = %q, want %q", st.Output, want)
	}
	if _, err := Parse("active:active\n", time.Now()); err == nil {
		t.Error("a cut-off read parsed")
	}
}
