package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/testenv"
	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
)

// fakeHost answers the commands toft upgrade runs, as a panel host would.
type fakeHost struct {
	t       *testing.T
	calls   []string
	images  map[string]string // ref → image ID present on the host
	running map[string]string // container → the image ID it runs
	// healthy reports a container's health given the image it runs.
	healthy     func(name, image string) string
	postgres    bool // paas-postgres is running
	failAtlas   bool
	failRestore bool
	pullFails   bool
	contract    bool // the release has a contract.sql
	// contractEmpty: the release's contract.sql holds only its header.
	contractEmpty bool
	git           bool // the release has git hosting's compose file (D-71)
	dockerVers    string
	checkFails    bool   // server reseal --check finds values under another key
	answerIP      string // what server answer-on-ip prints
	curl          string // what curl prints; "" fails, as with no way out
	route         string // what ip route get prints
	// The releases' signed image manifests (<v>-release): key, a test key,
	// signs them; registry holds the images by digest; release is the
	// version whose manifest was pulled last.
	key      ed25519.PrivateKey
	registry map[string]string // digest ref → image ID
	release  string
	// noRelease: no manifest (a release from before them); badSig: signed
	// by another key; serve: the one served is this version's; swapped: the
	// registry holds another frontend than the one signed.
	noRelease, badSig, swapped bool
	serve                      string
}

func newFakeHost(t *testing.T) *fakeHost {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeHost{
		t:          t,
		key:        key,
		registry:   map[string]string{},
		images:     map[string]string{},
		running:    map[string]string{},
		healthy:    func(string, string) string { return "healthy" },
		dockerVers: "29.1.3",
	}
}

func (f *fakeHost) run(_ context.Context, out io.Writer, stdin io.Reader, env []string, name string, args ...string) error {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, call)
	if out == nil {
		out = io.Discard
	}
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	if name == "curl" {
		if f.curl == "" {
			return errors.New("curl: (28) timed out")
		}
		_, _ = io.WriteString(out, f.curl)
		return nil
	}
	if name == "ip" {
		_, _ = io.WriteString(out, f.route)
		return nil
	}
	if name != "docker" {
		return nil // nft, systemctl
	}
	switch {
	case args[0] == "version":
		_, _ = io.WriteString(out, f.dockerVers)
	case args[0] == "image" && args[1] == "inspect":
		id, ok := f.images[args[len(args)-1]]
		if !ok {
			return errors.New("No such image")
		}
		_, _ = io.WriteString(out, id)
	case args[0] == "pull" && f.pullFails:
		return errors.New("manifest unknown")
	case args[0] == "pull" && strings.HasSuffix(args[len(args)-1], "-release"):
		if f.noRelease {
			return errors.New("manifest unknown")
		}
		ref := strings.TrimSuffix(args[len(args)-1], "-release")
		f.release = ref[strings.LastIndex(ref, ":")+1:]
	case args[0] == "pull" && strings.Contains(args[len(args)-1], "@sha256:"):
		ref := args[len(args)-1]
		id, ok := f.registry[ref]
		if !ok {
			return errors.New("manifest unknown")
		}
		f.images[ref] = id
	case args[0] == "pull":
		ref := args[len(args)-1]
		f.images[ref] = "sha256:" + ref
	case args[0] == "tag":
		f.images[args[2]] = f.images[args[1]]
	case args[0] == "create":
		_, _ = io.WriteString(out, "cid")
	case args[0] == "cp":
		dst := args[2]
		if strings.HasSuffix(args[1], ":/toft-images") {
			write(f.t, dst, f.manifest())
		} else if strings.HasSuffix(args[1], ":/toft-images.sig") {
			key := f.key
			if f.badSig {
				_, key, _ = ed25519.GenerateKey(rand.Reader)
			}
			write(f.t, dst, base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(f.manifest()))))
		} else if strings.HasSuffix(args[1], "/bundle") {
			for _, file := range []string{"docker-compose.yml", "traefik/dynamic/dynamic.yml", "migrations/schema.sql", "migrations/premigrate.sql", "migrations/atlas.hcl",
				"host/firewall.nft", "host/toft-firewall.service", "host/toft-backup.sh", "host/toft-backup.service", "host/toft-backup.timer",
				"host/toft-release-check.service", "host/toft-release-check.timer"} {
				write(f.t, filepath.Join(dst, file), "x")
			}
			if f.contract {
				write(f.t, filepath.Join(dst, "migrations/contract.sql"), "ALTER TABLE x DROP COLUMN IF EXISTS y;")
			}
			if f.contractEmpty {
				write(f.t, filepath.Join(dst, "migrations/contract.sql"), "-- Contract steps (D-63).\n--\n-- None in this release.\n\n")
			}
			if f.git {
				write(f.t, filepath.Join(dst, "docker-compose.git.yml"), "x")
			}
		} else {
			write(f.t, dst, "toft binary")
		}
	case args[0] == "inspect" && args[2] == "{{.Image}}":
		var ids []string
		for _, c := range args[3:] {
			ids = append(ids, f.running[c])
		}
		_, _ = io.WriteString(out, strings.Join(ids, "\n"))
	case args[0] == "exec" && contains(args, "answer-on-ip"):
		_, _ = io.WriteString(out, "{\"msg\":\"config loaded\"}\n"+f.answerIP+"\n")
	case args[0] == "exec" && contains(args, "reseal"):
		if f.checkFails {
			_, _ = io.WriteString(out, `{"msg":"value not under the current key"}`)
			return errors.New("exit status 1")
		}
	case args[0] == "inspect" && strings.Contains(call, "{{.State.Running}}"):
		_, _ = io.WriteString(out, map[bool]string{true: "true", false: "false"}[f.postgres])
	case args[0] == "inspect":
		var lines []string
		for _, c := range args[3:] {
			img := f.running[c]
			lines = append(lines, "/"+c+" "+img+" "+f.healthy(c, img))
		}
		_, _ = io.WriteString(out, strings.Join(lines, "\n"))
	case args[0] == "exec" && args[2] == "pg_dump" || args[0] == "exec" && args[1] == "paas-postgres" && args[2] == "pg_dump":
		_, _ = io.WriteString(out, "PGDMP")
	case args[0] == "exec" && strings.Contains(call, "-tA"):
		_, _ = io.WriteString(out, "1")
	case args[0] == "exec" && strings.Contains(call, "pg_restore"):
		if f.failRestore {
			return errors.New("exit status 3")
		}
	case args[0] == "run" && strings.Contains(call, "atlas"):
		if f.failAtlas {
			return fmt.Errorf("%s: exit status 1: Error: pq: column x does not exist", call) // as osHost.run
		}
	case args[0] == "compose" && contains(args, "--remove-orphans"):
		// The stack starts on the images IMAGE_TAG names.
		env, _ := readEnv(args[slices.Index(args, "--env-file")+1])
		f.postgres = true
		for _, c := range []string{"backend", "frontend"} {
			f.running["paas-"+c] = f.images["ghcr.io/acme/paas-"+c+":"+env["IMAGE_TAG"]]
		}
	case args[0] == "compose" && contains(args, "postgres"):
		f.postgres = true
	}
	return nil
}

// digest is the fake registry's digest of an image.
func digest(component, version string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(component+version)))
}

// manifest is the image manifest release.yml signs for the release pulled
// last, with the registry holding its images.
func (f *fakeHost) manifest() string {
	v := f.release
	if f.serve != "" {
		v = f.serve
	}
	var b strings.Builder
	for _, c := range []string{"backend", "frontend"} {
		fmt.Fprintf(&b, "toft-image %s %s %s\n", v, c, digest(c, v))
		held := digest(c, v)
		if f.swapped && c == "frontend" {
			held = digest(c, v+"-moved")
		}
		f.registry["ghcr.io/acme/paas-"+c+"@"+held] = "sha256:ghcr.io/acme/paas-" + c + ":" + v
	}
	return b.String()
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time           { return c.t }
func (c *clock) sleep(d time.Duration)    { c.t = c.t.Add(d) }
func (f *fakeHost) did(prefix string) int { return countPrefix(f.calls, prefix) }

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// newUpgrader is a panel host in a temporary directory, with secrets.env.
func newUpgrader(t *testing.T, h *fakeHost) (*upgrader, *bytes.Buffer) {
	root := t.TempDir()
	p := paths{
		secrets:  filepath.Join(root, "etc/toft/secrets.env"),
		current:  filepath.Join(root, "opt/toft"),
		releases: filepath.Join(root, "var/lib/toft/releases"),
		work:     filepath.Join(root, "var/lib/toft/upgrade"),
		units:    filepath.Join(root, "units"),
		toft:     filepath.Join(root, "usr/local/bin/toft"),
		traefik:  filepath.Join(root, "var/lib/toft/traefik"),
	}
	write(t, p.secrets, "# toft\nPOSTGRES_USER=paas_user\nPOSTGRES_PASSWORD=pw\nPOSTGRES_DB=paas_db\nGHCR_USER=acme\nIMAGE_TAG=\n")
	if err := os.MkdirAll(filepath.Dir(p.current), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)}
	var out bytes.Buffer
	size := func() (int, int64, error) { return 4, 8 << 30, nil }
	free := func(string) (uint64, error) { return 40 << 30, nil }
	return &upgrader{h: h, p: p, out: &out, wait: 2 * time.Minute, poll: 3 * time.Second, now: c.now, sleep: c.sleep, size: size, free: free,
		key: h.key.Public().(ed25519.PublicKey)}, &out
}

func tag(t *testing.T, u *upgrader) string {
	t.Helper()
	env, err := readEnv(u.p.secrets)
	if err != nil {
		t.Fatal(err)
	}
	return env["IMAGE_TAG"]
}

func TestUpgrade_FreshInstall(t *testing.T) {
	h := newFakeHost(t)
	u, out := newUpgrader(t, h)

	if err := u.upgrade(context.Background(), "v0.1.0", false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := tag(t, u); got != "v0.1.0" {
		t.Fatalf("IMAGE_TAG = %q", got)
	}
	if target, _ := os.Readlink(u.p.current); target != filepath.Join(u.p.releases, "v0.1.0", "bundle") {
		t.Fatalf("/opt/toft → %q", target)
	}
	if env, _ := os.Readlink(filepath.Join(u.p.current, ".env")); env != u.p.secrets {
		t.Fatalf("the bundle's .env → %q, want the secrets", env)
	}
	if h.did("docker exec paas-postgres pg_dump") != 0 {
		t.Fatal("a fresh install has no database to save")
	}
	if h.did("docker exec paas-postgres pg_isready -q -h 127.0.0.1 ") == 0 {
		t.Fatal("postgres is ready when it answers over TCP: the setup server answers on the socket before the database exists")
	}
	if st := u.readState(); st.Version != "v0.1.0" || st.Previous != "" {
		t.Fatalf("state = %+v", st)
	}
	for _, f := range []string{"etc/toft/firewall.nft", "etc/systemd/system/toft-backup.timer", "usr/local/lib/toft/backup.sh", "etc/systemd/system/toft-release-check.timer"} {
		if _, err := os.Stat(filepath.Join(u.p.units, f)); err != nil {
			t.Errorf("host unit %s not installed", f)
		}
	}
	if target, _ := os.Readlink(filepath.Join(u.p.units, "var/lib/toft/backup")); target != "/var/paas/backup" {
		t.Errorf("/var/lib/toft/backup → %q, want the backend's backup directory", target)
	}
	slice, _ := os.ReadFile(filepath.Join(u.p.units, "etc/systemd/system/toft-apps.slice"))
	for _, want := range []string{"CPUQuota=250%", "MemoryMax=5368709120"} {
		if !strings.Contains(string(slice), want) {
			t.Errorf("toft-apps.slice lacks %s:\n%s", want, slice)
		}
	}
	if b, _ := os.ReadFile(u.p.toft); string(b) != "toft binary" {
		t.Error("toft not installed from the release")
	}
	// Migrations run before the stack starts.
	atlas, up := -1, -1
	for i, c := range h.calls {
		if strings.Contains(c, atlasImage) && atlas < 0 {
			atlas = i
		}
		if strings.HasSuffix(c, "up -d --remove-orphans") && up < 0 {
			up = i
		}
	}
	if atlas < 0 || up < 0 || atlas > up {
		t.Fatalf("atlas at %d, stack up at %d: migrations must come first", atlas, up)
	}
}

// installed is a host running v0.1.0 through toft, with the panel's routes.
func installed(t *testing.T) (*fakeHost, *upgrader, *bytes.Buffer) {
	h := newFakeHost(t)
	u, out := newUpgrader(t, h)
	if err := u.upgrade(context.Background(), "v0.1.0", false); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	write(t, filepath.Join(u.p.traefik, "dynamic/panel.yml"), "routes")
	h.calls = nil
	out.Reset()
	return h, u, out
}

// A panel installed before install.sh recorded TOFT_PANEL_IP gets it from
// its next upgrade (D-150).
func TestUpgrade_RecordsThePanelIP(t *testing.T) {
	panelIP := func(u *upgrader) string {
		env, err := readEnv(u.p.secrets)
		if err != nil {
			t.Fatal(err)
		}
		return env["TOFT_PANEL_IP"]
	}

	t.Run("from the public address", func(t *testing.T) {
		h, u, out := installed(t)
		h.curl = "203.0.113.9\n"
		if err := u.upgrade(context.Background(), "v0.2.0", false); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if got := panelIP(u); got != "203.0.113.9" {
			t.Fatalf("TOFT_PANEL_IP = %q", got)
		}
	})

	t.Run("an answer that isn't an address falls back to the route", func(t *testing.T) {
		h, u, out := installed(t)
		h.curl = "<html>busy</html>"
		h.route = "1.1.1.1 via 10.0.0.1 dev eth0 src 10.0.0.5 uid 0\n    cache\n"
		if err := u.upgrade(context.Background(), "v0.2.0", false); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if got := panelIP(u); got != "10.0.0.5" {
			t.Fatalf("TOFT_PANEL_IP = %q", got)
		}
	})

	t.Run("one already recorded stays", func(t *testing.T) {
		h, u, out := installed(t)
		if err := setEnv(u.p.secrets, "TOFT_PANEL_IP", "198.51.100.1"); err != nil {
			t.Fatal(err)
		}
		h.curl = "203.0.113.9"
		if err := u.upgrade(context.Background(), "v0.2.0", false); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if got := panelIP(u); got != "198.51.100.1" {
			t.Fatalf("TOFT_PANEL_IP = %q", got)
		}
		if h.did("curl -fsS4 --max-time 5 https://ifconfig.me") != 0 {
			t.Fatal("looked the address up with one recorded")
		}
	})

	t.Run("none found is said and doesn't stop the upgrade", func(t *testing.T) {
		_, u, out := installed(t)
		if err := u.upgrade(context.Background(), "v0.2.0", false); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if got := panelIP(u); got != "" {
			t.Fatalf("TOFT_PANEL_IP = %q", got)
		}
		if !strings.Contains(out.String(), "Couldn't find this server's public IP") {
			t.Fatalf("output:\n%s", out)
		}
	})
}

func TestUpgrade_Healthy(t *testing.T) {
	h, u, out := installed(t)

	if err := u.upgrade(context.Background(), "v0.2.0", false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := tag(t, u); got != "v0.2.0" {
		t.Fatalf("IMAGE_TAG = %q", got)
	}
	if h.did("docker exec paas-postgres pg_dump -Fc -U paas_user paas_db") != 1 {
		t.Fatal("the database was not saved before the migration")
	}
	if dumps, _ := filepath.Glob(filepath.Join(u.p.work, "v0.1.0-*.dump")); len(dumps) != 1 {
		t.Fatalf("dumps = %v", dumps)
	}
	if b, _ := os.ReadFile(filepath.Join(u.p.traefik, "dynamic/panel.yml")); string(b) != "routes" {
		t.Fatal("the panel's routes were not kept")
	}
	if st := u.readState(); st.Version != "v0.2.0" || st.Previous != "v0.1.0" {
		t.Fatalf("state = %+v", st)
	}
	for _, v := range []string{"v0.1.0", "v0.2.0"} {
		if _, err := os.Stat(filepath.Join(u.p.releases, v)); err != nil {
			t.Errorf("release %s pruned; the running and the previous one stay", v)
		}
	}
	if !strings.Contains(out.String(), "(v0.1.0 kept serving)") {
		t.Errorf("output:\n%s", out)
	}
	// The panel's step list shows each step's facts.
	var run upgradeRun
	b, _ := os.ReadFile(filepath.Join(u.p.work, "run.json"))
	if err := json.Unmarshal(b, &run); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"pull", "save", "migrate", "health"} {
		if run.Notes[step] == "" {
			t.Errorf("no note for %s: %v", step, run.Notes)
		}
	}
	if run.Notes["preflight"] != "43 GB free" || run.Notes["pull"] != "2 images" || run.Notes["migrate"] != "nothing dropped" {
		t.Errorf("notes = %v", run.Notes)
	}
}

// An unhealthy release: the previous images start again on the new schema.
func TestUpgrade_UnhealthyRollsBack(t *testing.T) {
	h, u, out := installed(t)
	h.healthy = func(name, image string) string {
		if name == "paas-backend" && strings.Contains(image, "v0.2.0") {
			return "unhealthy"
		}
		return "healthy"
	}

	err := u.upgrade(context.Background(), "v0.2.0", false)
	if !errors.Is(err, errRolledBack) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if got := tag(t, u); got != "v0.1.0" {
		t.Fatalf("IMAGE_TAG = %q, want v0.1.0 back", got)
	}
	if target, _ := os.Readlink(u.p.current); target != filepath.Join(u.p.releases, "v0.1.0", "bundle") {
		t.Fatalf("/opt/toft → %q", target)
	}
	if h.running["paas-backend"] != h.images["ghcr.io/acme/paas-backend:v0.1.0"] {
		t.Fatal("the previous backend is not running")
	}
	if h.did("docker exec -i paas-postgres sh -c "+restoreScript) != 0 {
		t.Fatal("no planned drop ran, so the schema stays: nothing is restored")
	}
	if st := u.readState(); st.Version != "v0.1.0" {
		t.Fatalf("state = %+v", st)
	}
	for _, want := range []string{"Not healthy after 2m0s: paas-backend is unhealthy", "Rolling back", "Toft v0.1.0 is running. The upgrade to v0.2.0 failed."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// A release with planned drops restores the dump before the old images.
func TestUpgrade_UnhealthyAfterContractRestoresDump(t *testing.T) {
	h, u, out := installed(t)
	h.contract = true
	h.healthy = func(name, image string) string {
		if name == "paas-frontend" && strings.Contains(image, "v0.2.0") {
			return "starting"
		}
		return "healthy"
	}

	if err := u.upgrade(context.Background(), "v0.2.0", false); !errors.Is(err, errRolledBack) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if h.did("docker exec -i paas-postgres sh -c "+restoreScript+" sh paas_user paas_db") != 1 {
		t.Fatalf("the dump was not restored:\n%s", strings.Join(h.calls, "\n"))
	}
}

// A contract.sql with only comments is no contract step: the upgrade says
// nothing was dropped, and a failed one starts the previous release again
// without restoring the dump.
func TestUpgrade_CommentOnlyContractIsNoContractStep(t *testing.T) {
	h, u, out := installed(t)
	h.contractEmpty = true
	h.healthy = func(name, image string) string {
		if name == "paas-frontend" && strings.Contains(image, "v0.2.0") {
			return "starting"
		}
		return "healthy"
	}

	if err := u.upgrade(context.Background(), "v0.2.0", false); !errors.Is(err, errRolledBack) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if h.did("docker exec -i paas-postgres sh -c "+restoreScript) != 0 {
		t.Fatalf("a comment-only contract.sql restored the dump:\n%s", strings.Join(h.calls, "\n"))
	}
	if h.running["paas-backend"] != h.images["ghcr.io/acme/paas-backend:v0.1.0"] {
		t.Fatal("the previous backend is not running")
	}
	if !strings.Contains(out.String(), "nothing was dropped") {
		t.Errorf("output lacks %q:\n%s", "nothing was dropped", out)
	}
}

// A restore that fails changed nothing (one transaction), and the previous
// release runs on the newer schema (D-63): it starts anyway.
func TestUpgrade_FailedRestoreStillStartsThePrevious(t *testing.T) {
	h, u, out := installed(t)
	h.contract = true
	h.failRestore = true
	h.healthy = func(name, image string) string {
		if name == "paas-frontend" && strings.Contains(image, "v0.2.0") {
			return "starting"
		}
		return "healthy"
	}

	if err := u.upgrade(context.Background(), "v0.2.0", false); !errors.Is(err, errRolledBack) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if h.running["paas-backend"] != h.images["ghcr.io/acme/paas-backend:v0.1.0"] {
		t.Fatal("the previous backend is not running")
	}
	if !strings.Contains(out.String(), "Database not restored") {
		t.Errorf("output lacks the failed restore:\n%s", out)
	}
}

// A failed migration changes nothing: the running release keeps serving.
func TestUpgrade_FailedMigrationAborts(t *testing.T) {
	h, u, out := installed(t)
	h.failAtlas = true

	err := u.upgrade(context.Background(), "v0.2.0", false)
	if err == nil || !strings.Contains(err.Error(), "schema (Atlas)") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), ":pw@") {
		t.Fatalf("the error shows the database password: %v", err)
	}
	if got := tag(t, u); got != "v0.1.0" {
		t.Fatalf("IMAGE_TAG = %q", got)
	}
	if h.did("docker compose") != 1 || !strings.Contains(h.calls[indexOf(h.calls, "docker compose")], "--no-recreate postgres") {
		t.Fatalf("the stack was touched beyond postgres:\n%s", strings.Join(h.calls, "\n"))
	}
	if !strings.Contains(out.String(), "v0.1.0 keeps running") {
		t.Errorf("output:\n%s", out)
	}
}

func indexOf(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func TestUpgrade_Refusals(t *testing.T) {
	h, u, _ := installed(t)
	for _, v := range []string{"v0.1.0", "v0.0.9", "v0.1.0-rc.1"} {
		if err := u.upgrade(context.Background(), v, false); err == nil || !strings.Contains(err.Error(), "is not newer") {
			t.Errorf("%s: err = %v", v, err)
		}
	}
	if len(h.calls) != 0 {
		t.Fatalf("a refused upgrade ran %v", h.calls)
	}

	h2 := newFakeHost(t)
	h2.dockerVers = "27.3.1"
	u2, _ := newUpgrader(t, h2)
	if err := u2.upgrade(context.Background(), "v0.1.0", false); err == nil || !strings.Contains(err.Error(), "Docker 29 or newer") {
		t.Fatalf("err = %v", err)
	}

	u3, _ := newUpgrader(t, newFakeHost(t))
	// The most it refuses: what it says it needs must still be more.
	u3.free = func(string) (uint64, error) { return 3<<30 - 1, nil }
	if err := u3.upgrade(context.Background(), "v0.1.0", false); err == nil || !strings.Contains(err.Error(), "only 3.2 GB free; an upgrade needs 3.3 GB") {
		t.Fatalf("err = %v", err)
	}
}

func TestDumpsKeepTheLastThree(t *testing.T) {
	_, u, out := installed(t)
	c := &clock{t: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	u.now, u.sleep = c.now, c.sleep
	for _, v := range []string{"v0.2.0", "v0.3.0", "v0.4.0", "v0.5.0"} {
		c.t = c.t.Add(time.Hour)
		if err := u.upgrade(context.Background(), v, false); err != nil {
			t.Fatalf("%s: %v\n%s", v, err, out)
		}
	}
	dumps, _ := filepath.Glob(filepath.Join(u.p.work, "*.dump"))
	if len(dumps) != keepDumps {
		t.Fatalf("dumps = %v", dumps)
	}
	for _, d := range dumps {
		if strings.Contains(d, "v0.1.0-") {
			t.Fatalf("the oldest dump was kept: %v", dumps)
		}
	}
}

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.2.0", "v0.1.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.1.0", "v0.1.0", false},
		{"v0.1.0", "v0.1.0-rc.1", true},
		{"v0.1.0-rc.2", "v0.1.0-rc.1", true},
		{"v0.1.0-rc.1", "v0.1.0", false},
		{"v0.10.0", "v0.9.0", true},
	} {
		if got := buildinfo.Newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
	for _, bad := range []string{"0.1.0", "v1.2", "v1.2.x", "latest", ""} {
		if _, ok := buildinfo.Parse(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

// The panel's Traefik files live outside the releases (D-65): installs keep
// them in TOFT_TRAEFIK_DIR, and one from before (routes inside the release
// that runs) moves there with its routes.
func TestUpgrade_TraefikFilesOutsideTheReleases(t *testing.T) {
	_, u, out := installed(t)
	env, _ := readEnv(u.p.secrets)
	if env["TOFT_TRAEFIK_DIR"] != u.p.traefik {
		t.Fatalf("TOFT_TRAEFIK_DIR = %q", env["TOFT_TRAEFIK_DIR"])
	}
	if _, err := os.Stat(filepath.Join(u.p.traefik, "dynamic/dynamic.yml")); err != nil {
		t.Fatal("the release's middlewares were not installed")
	}

	// An install from before: no TOFT_TRAEFIK_DIR, routes in the release.
	b, _ := os.ReadFile(u.p.secrets)
	write(t, u.p.secrets, strings.ReplaceAll(string(b), "TOFT_TRAEFIK_DIR="+u.p.traefik+"\n", ""))
	_ = os.RemoveAll(u.p.traefik)
	write(t, filepath.Join(u.p.current, "traefik/dynamic/panel.yml"), "old routes")
	if err := u.upgrade(context.Background(), "v0.2.0", false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if env, _ := readEnv(u.p.secrets); env["TOFT_TRAEFIK_DIR"] != u.p.traefik {
		t.Fatal("TOFT_TRAEFIK_DIR was not set")
	}
	if b, _ := os.ReadFile(filepath.Join(u.p.traefik, "dynamic/panel.yml")); string(b) != "old routes" {
		t.Fatal("the routes did not move out of the release")
	}

	// The access log file it wrote, never rotated, goes: Traefik logs to
	// its container now.
	write(t, filepath.Join(u.p.traefik, "logs/access.log"), "GET / 200")
	if err := u.upgrade(context.Background(), "v0.2.1", false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(u.p.traefik, "logs")); !os.IsNotExist(err) {
		t.Fatalf("the old access log is still there: %v", err)
	}
}

// An upgrade runs the target release's own toft (D-65).
func TestHandover(t *testing.T) {
	h := newFakeHost(t)
	u, out := newUpgrader(t, h)
	var ran []string
	u.exec = func(path string, args []string) error { ran = args; return errors.New("exec'd") }

	u.self = "v0.1.1"
	if err := u.handover(context.Background(), "v0.1.2", []string{"--to", "v0.1.2"}); err == nil || err.Error() != "exec'd" {
		t.Fatalf("err = %v", err)
	}
	want := []string{filepath.Join(u.p.work, "toft-v0.1.2"), "upgrade", "--to", "v0.1.2", "--handed-over"}
	if strings.Join(ran, " ") != strings.Join(want, " ") {
		t.Fatalf("exec %v, want %v", ran, want)
	}
	if b, _ := os.ReadFile(want[0]); string(b) != "toft binary" {
		t.Fatal("the release's toft was not copied out of its image")
	}

	// The release's own toft upgrades itself.
	ran = nil
	u.self = "v0.1.2"
	if err := u.handover(context.Background(), "v0.1.2", nil); err != nil || ran != nil {
		t.Fatalf("err = %v, exec %v", err, ran)
	}

	// No image: this toft carries on, and says so.
	u.self = "v0.1.1"
	h.pullFails = true
	if err := u.handover(context.Background(), "v0.9.9", nil); err != nil || ran != nil {
		t.Fatalf("err = %v, exec %v", err, ran)
	}
	if !strings.Contains(out.String(), "Upgrading with this toft (v0.1.1)") {
		t.Fatalf("output: %s", out)
	}
}

// Git hosting (D-71): its compose file joins the stack unless TOFT_GIT=off,
// paas-git must be healthy too, and a port another program holds stops
// the upgrade before anything changes.
func TestUpgrade_Git(t *testing.T) {
	h := newFakeHost(t)
	h.git = true
	u, out := newUpgrader(t, h)
	u.listen = func(port string) bool { return port != "2222" }
	err := u.upgrade(context.Background(), "v0.3.0", false)
	if err == nil || !strings.Contains(err.Error(), "port 2222 is in use") {
		t.Fatalf("a held port: %v\n%s", err, out)
	}
	if h.did("docker compose") != 0 {
		t.Fatal("compose ran before the port was checked")
	}

	write(t, u.p.secrets, "POSTGRES_USER=paas_user\nPOSTGRES_DB=paas_db\nGHCR_USER=acme\nTOFT_GIT_SSH_PORT=2223\n")
	if err := u.upgrade(context.Background(), "v0.3.0", false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	up := h.calls[slices.IndexFunc(h.calls, func(c string) bool { return strings.HasSuffix(c, "up -d --remove-orphans") })]
	if !strings.Contains(up, "docker-compose.git.yml") {
		t.Fatalf("the stack started without git: %s", up)
	}
	if h.did("docker inspect --format {{.Name}}") == 0 || !slices.ContainsFunc(h.calls, func(c string) bool {
		return strings.HasPrefix(c, "docker inspect --format {{.Name}}") && strings.HasSuffix(c, "paas-git")
	}) {
		t.Fatal("paas-git's health was not checked")
	}

	// Installed without git: the file stays out, and so does paas-git.
	h.calls = nil
	write(t, u.p.secrets, "POSTGRES_USER=paas_user\nPOSTGRES_DB=paas_db\nGHCR_USER=acme\nIMAGE_TAG=v0.3.0\nTOFT_GIT=off\n")
	u.listen = func(string) bool { return false }
	if err := u.upgrade(context.Background(), "v0.3.0", true); err != nil {
		t.Fatalf("without git: %v\n%s", err, out)
	}
	for _, c := range h.calls {
		if strings.Contains(c, "docker-compose.git.yml") || strings.HasSuffix(c, "paas-git") {
			t.Fatalf("git without git: %s", c)
		}
	}
}

// The panel's images run by the digests the release key signed for the
// release (<v>-release), never by a tag, which whoever can push to the
// package can move. Anything else is refused before anything changes.
func TestUpgrade_SignedImages(t *testing.T) {
	t.Run("pulls the signed digests, whatever the tags say", func(t *testing.T) {
		h, u, out := installed(t)
		h.images["ghcr.io/acme/paas-frontend:v0.2.0"] = "sha256:moved" // a tag moved and pulled
		if err := u.upgrade(context.Background(), "v0.2.0", false); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		for _, c := range []string{"backend", "frontend"} {
			ref := "ghcr.io/acme/paas-" + c + "@" + digest(c, "v0.2.0")
			if h.did("docker pull --quiet "+ref) != 1 {
				t.Errorf("%s not pulled by its digest:\n%s", c, strings.Join(h.calls, "\n"))
			}
			if h.running["paas-"+c] != h.registry[ref] {
				t.Errorf("paas-%s runs %q, not the signed image", c, h.running["paas-"+c])
			}
		}
		if h.did("docker pull --quiet ghcr.io/acme/paas-backend:v0.2.0") != 0 || h.did("docker pull --quiet ghcr.io/acme/paas-frontend:v0.2.0") != 0 {
			t.Fatal("pulled by tag")
		}
		if st := u.readState(); !strings.Contains(strings.Join(st.Images, " "), "@sha256:") {
			t.Fatalf("state records %v, not the digests a rollback checks", st.Images)
		}
	})

	for _, c := range []struct {
		name string
		set  func(*fakeHost)
		want string
	}{
		{"an image whose digest isn't the signed one", func(h *fakeHost) { h.swapped = true }, "manifest unknown"},
		{"a manifest the release key didn't sign", func(h *fakeHost) { h.badSig = true }, "not signed by the release key"},
		{"another version's manifest", func(h *fakeHost) { h.serve = "v0.1.9" }, "signed for v0.1.9, not v0.2.0"},
		{"a release with no manifest", func(h *fakeHost) { h.noRelease = true }, "v0.2.0 has no signed image manifest"},
	} {
		t.Run("refuses "+c.name, func(t *testing.T) {
			h, u, out := installed(t)
			c.set(h)
			err := u.upgrade(context.Background(), "v0.2.0", false)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q\n%s", err, c.want, out)
			}
			if got := tag(t, u); got != "v0.1.0" {
				t.Fatalf("IMAGE_TAG = %q", got)
			}
			if h.did("docker compose") != 0 || h.did("docker exec paas-postgres pg_dump") != 0 {
				t.Fatalf("a refused release changed something:\n%s", strings.Join(h.calls, "\n"))
			}
		})
	}

	// A panel goes back to the release it ran before, from before signed
	// manifests (--force), with the images it ran that release from.
	t.Run("goes back to the previous release by its images here", func(t *testing.T) {
		h, u, out := installed(t)
		if err := u.upgrade(context.Background(), "v0.2.0", false); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		before := map[string]string{}
		for _, c := range []string{"backend", "frontend"} {
			before[c] = h.images["ghcr.io/acme/paas-"+c+":v0.1.0"]
		}
		h.noRelease, h.calls = true, nil
		if err := u.upgrade(context.Background(), "v0.1.0", true); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if got := tag(t, u); got != "v0.1.0" {
			t.Fatalf("IMAGE_TAG = %q", got)
		}
		for _, c := range []string{"backend", "frontend"} {
			if h.did("docker pull --quiet ghcr.io/acme/paas-"+c+":v0.1.0") != 0 {
				t.Fatalf("pulled %s by tag", c)
			}
			if h.running["paas-"+c] != before[c] {
				t.Errorf("paas-%s runs %q, not the image v0.1.0 ran from", c, h.running["paas-"+c])
			}
		}
	})

	t.Run("refuses the previous release once its images are gone", func(t *testing.T) {
		h, u, out := installed(t)
		if err := u.upgrade(context.Background(), "v0.2.0", false); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		delete(h.images, "ghcr.io/acme/paas-frontend:v0.1.0")
		h.noRelease, h.calls = true, nil
		err := u.upgrade(context.Background(), "v0.1.0", true)
		if err == nil || !strings.Contains(err.Error(), "v0.1.0 has no signed image manifest") {
			t.Fatalf("err = %v\n%s", err, out)
		}
		if h.did("docker pull --quiet ghcr.io/acme/paas-frontend:v0.1.0") != 0 || h.did("docker compose") != 0 {
			t.Fatalf("a refused release changed something:\n%s", strings.Join(h.calls, "\n"))
		}
	})

	t.Run("the handover runs only the signed release's toft", func(t *testing.T) {
		h := newFakeHost(t)
		u, _ := newUpgrader(t, h)
		u.self = "v0.1.1"
		h.badSig = true
		u.exec = func(string, []string) error { t.Fatal("ran an unsigned toft"); return nil }
		if err := u.handover(context.Background(), "v0.1.2", nil); err != nil {
			t.Fatal(err)
		}
		if h.did("docker create ghcr.io/acme/paas-backend") != 0 {
			t.Fatal("unpacked an unsigned backend image")
		}
	})
}

// toft and install.sh check the release key the agents check (D-106):
// go:embed can't reach ../toft-agent, so each holds a copy, kept the same here.
func TestReleaseKeyCopies(t *testing.T) {
	agent, err := os.ReadFile("../toft-agent/release.pub")
	if err != nil {
		t.Fatal(err)
	}
	if string(releaseKeyPEM) != string(agent) {
		t.Fatal("cmd/toft/release.pub is not cmd/toft-agent/release.pub")
	}
	if builtInReleaseKey() == nil {
		t.Fatal("the built-in release key doesn't parse")
	}
	script, err := os.ReadFile("../../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^RELEASE_KEY='([^']*)'$`).FindSubmatch(script)
	if m == nil || strings.TrimSpace(string(m[1])) != strings.TrimSpace(string(agent)) {
		t.Fatal("install.sh's RELEASE_KEY is not cmd/toft-agent/release.pub")
	}
}

// install.sh's signed_digest, run with OpenSSL as on a host: the digest
// only from a manifest the release key signed for the release.
func TestInstallScriptChecksTheManifest(t *testing.T) {
	if out, err := exec.Command("openssl", "pkeyutl", "-help").CombinedOutput(); !strings.Contains(string(out), "-rawin") {
		testenv.Skip(t, "OpenSSL 3 (pkeyutl -rawin) is missing: %v", err)
	}
	script, err := os.ReadFile("../../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`(?ms)^signed_digest\(\) \{$.*?^\}$`).Find(script)
	if fn == nil {
		t.Fatal("install.sh has no signed_digest")
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	manifest := "toft-image v0.2.0 backend " + digest("backend", "v0.2.0") + "\ntoft-image v0.2.0 frontend " + digest("frontend", "v0.2.0") + "\n"
	run := func(body string, signer ed25519.PrivateKey, release string) (string, int) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "toft-images"), body)
		write(t, filepath.Join(dir, "toft-images.sig"), base64.StdEncoding.EncodeToString(ed25519.Sign(signer, []byte(manifest))))
		cmd := exec.Command("sh", "-c", string(fn)+"\nsigned_digest \"$1\" backend", "sh", dir)
		cmd.Env = append(os.Environ(), "RELEASE="+release, "RELEASE_KEY="+string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
		out, err := cmd.Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out)), 0
	}
	if out, code := run(manifest, key, "v0.2.0"); code != 0 || out != digest("backend", "v0.2.0") {
		t.Fatalf("a signed manifest: %q, exit %d", out, code)
	}
	if _, code := run(strings.Replace(manifest, digest("backend", "v0.2.0"), digest("backend", "evil"), 1), key, "v0.2.0"); code != 1 {
		t.Fatalf("an edited manifest: exit %d, want 1", code)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if _, code := run(manifest, other, "v0.2.0"); code != 1 {
		t.Fatalf("another key's signature: exit %d, want 1", code)
	}
	if _, code := run(manifest, key, "v0.3.0"); code != 2 {
		t.Fatalf("another version's manifest: exit %d, want 2", code)
	}
}
