package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
	"github.com/Nickpap21/toft/backend/pkg/config"
	"github.com/Nickpap21/toft/backend/pkg/reservation"
)

// toft upgrade (D-63): moves the panel host to a release in place, and back
// when the new one isn't healthy. The schema only moves forward, so a
// rollback runs the previous images on the newer schema; a failed migration
// aborts with the running version untouched. install.sh runs it for the
// first install too.
//
// Layout on the host:
//
//	/etc/toft/secrets.env         the stack's settings and secrets (0600); IMAGE_TAG is the running release
//	/opt/toft                     a symlink to the running release's bundle
//	/var/lib/toft/releases/<v>/   each release's bundle and toft binary
//	/var/lib/toft/upgrade/        database dumps (the last 3), state.json, the lock

const upgradeUsage = `usage: toft upgrade --to vX.Y.Z [--repo ghcr.io/OWNER] [--force]

Pulls the release's images by the digests its signed manifest names (and
refuses a release without one), saves the database, migrates it, swaps the
images and checks their health for up to 2 minutes; an unhealthy release is
rolled back to the one that ran before. Run as root on the panel host.`

// atlasImage is the migrations' Atlas, pinned by digest as in the Makefile
// (AGENTS.md: change both together).
const atlasImage = "arigaio/atlas:1.3.3-community@sha256:9c9958f5b8d26d404ab2e28098c8629526eb4bec647f790278611e58a0b19839"

// firstReading is the first release that reads v1 sealed values, and
// firstSealing the first that writes them (D-126).
const firstReading, firstSealing = "v0.27.0", "v0.28.0"

// keepDumps is how many database dumps upgrades keep.
const keepDumps = 3

// host runs commands on the panel host; the tests fake it.
type host interface {
	// run runs name with args, the given extra environment and stdin (nil
	// for none), writing stdout to out when it is not nil. A failed command's
	// error carries the end of its stderr.
	run(ctx context.Context, out io.Writer, stdin io.Reader, env []string, name string, args ...string) error
}

type osHost struct{}

func (osHost) run(ctx context.Context, out io.Writer, stdin io.Reader, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = stdin
	cmd.Stdout = out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 600 {
			msg = "…" + msg[len(msg)-600:]
		}
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return nil
}

// paths are the directories upgrade works in; the tests point them at a
// temporary directory.
type paths struct {
	secrets  string // /etc/toft/secrets.env
	current  string // /opt/toft
	releases string // /var/lib/toft/releases
	work     string // /var/lib/toft/upgrade
	units    string // "/" on a host: where /etc/systemd/system and friends live
	toft     string // /usr/local/bin/toft
	traefik  string // /var/lib/toft/traefik: the panel's Traefik files, outside the releases
}

var hostPaths = paths{
	secrets:  "/etc/toft/secrets.env",
	current:  "/opt/toft",
	releases: "/var/lib/toft/releases",
	work:     "/var/lib/toft/upgrade",
	units:    "/",
	toft:     "/usr/local/bin/toft",
	traefik:  "/var/lib/toft/traefik",
}

type upgrader struct {
	h     host
	p     paths
	repo  string
	out   io.Writer
	log   io.Writer     // this upgrade's log: what the commands printed
	wait  time.Duration // how long a release has to become healthy
	poll  time.Duration
	now   func() time.Time
	sleep func(time.Duration)
	// size is the host's cores and memory, for the apps' slice; free the
	// bytes free on a path's filesystem.
	size func() (cpus int, memory int64, err error)
	free func(path string) (uint64, error)
	// self is this toft's release; exec replaces the process with another
	// toft (handover).
	self string
	exec func(path string, args []string) error
	// run is the upgrade in progress, for the panel (D-68).
	run *upgradeRun
	// listen says whether a TCP port on the host is free (git's SSH, D-71);
	// nil reads as free. git is whether the last compose run had git.
	listen func(port string) bool
	git    bool
	// key is the release key, which signs each release's image manifest.
	key ed25519.PublicKey
}

// releaseKeyPEM is the public half of the release signing key, the one the
// agents check (D-106). A copy of cmd/toft-agent/release.pub: go:embed
// can't reach outside this directory. TestReleaseKeyCopies keeps the two,
// and install.sh's, the same.
//
//go:embed release.pub
var releaseKeyPEM []byte

func builtInReleaseKey() ed25519.PublicKey {
	b, _ := pem.Decode(releaseKeyPEM)
	if b == nil {
		return nil
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil
	}
	pk, _ := k.(ed25519.PublicKey)
	return pk
}

// state is /var/lib/toft/upgrade/state.json.
type state struct {
	Version   string    `json:"version"`
	Previous  string    `json:"previous,omitempty"`
	Images    []string  `json:"images"`
	UpdatedAt time.Time `json:"updated_at"`
}

// upgradeRun is /var/lib/toft/upgrade/run.json, with run.out beside it holding
// what toft upgrade printed: the upgrade in progress, or the last one. The
// panel follows it through a read-only mount (D-68), whoever started it.
type upgradeRun struct {
	To         string     `json:"to"`
	From       string     `json:"from"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Step is preflight, pull, save, migrate, restart or health.
	Step string `json:"step"`
	// Outcome is running, succeeded, rolled_back or failed.
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
	Log     string `json:"log,omitempty"`
	// Notes are a step's facts for the panel's step list, by step: "38 GB
	// free", "2 images", "1 MiB".
	Notes map[string]string `json:"notes,omitempty"`
}

// beginRun records an upgrade from from to to, and sends what it prints to
// run.out as well.
func (u *upgrader) beginRun(to, from, logPath string) (end func(error)) {
	r := &upgradeRun{To: to, From: from, StartedAt: u.now().UTC(), Step: "preflight", Outcome: "running", Log: logPath}
	out, err := os.OpenFile(filepath.Join(u.p.work, "run.out"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	prev := u.out
	if err == nil {
		u.out = io.MultiWriter(prev, out)
	}
	u.run = r
	u.saveRun()
	return func(err error) {
		at := u.now().UTC()
		r.FinishedAt = &at
		switch {
		case err == nil:
			r.Outcome = "succeeded"
		case errors.Is(err, errRolledBack):
			r.Outcome, r.Error = "rolled_back", err.Error()
		default:
			r.Outcome, r.Error = "failed", err.Error()
		}
		u.saveRun()
		u.out, u.run = prev, nil
		if out != nil {
			_ = out.Close()
		}
	}
}

// step records that the run reached step.
func (u *upgrader) step(name string) {
	if u.run != nil {
		u.run.Step = name
		u.saveRun()
	}
}

// note records a fact of step for the panel.
func (u *upgrader) note(step, text string) {
	if u.run != nil {
		if u.run.Notes == nil {
			u.run.Notes = map[string]string{}
		}
		u.run.Notes[step] = text
		u.saveRun()
	}
}

func (u *upgrader) saveRun() {
	b, _ := json.MarshalIndent(u.run, "", "  ")
	_ = writeAtomic(filepath.Join(u.p.work, "run.json"), b, 0o600)
}

// errRolledBack is an upgrade whose release was unhealthy and whose
// previous release runs again.
var errRolledBack = errors.New("rolled back")

func upgradeCmd(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	to := fs.String("to", "", "the release to run, e.g. v0.2.0")
	repo := fs.String("repo", getenv("TOFT_IMAGE_REPO"), "where the images are; ghcr.io/<GHCR_USER> from secrets.env by default")
	force := fs.Bool("force", false, "allow the same or an older release")
	handedOver := fs.Bool("handed-over", false, "set by the toft that handed the upgrade over")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError(upgradeUsage)
	}
	if _, ok := buildinfo.Parse(*to); !ok {
		return usageError("--to needs a release such as v0.2.0\n" + upgradeUsage)
	}
	if os.Geteuid() != 0 {
		return errors.New("run toft upgrade as root on the panel host")
	}
	u := &upgrader{h: osHost{}, p: hostPaths, repo: *repo, out: stdout, wait: 2 * time.Minute, poll: 3 * time.Second, now: time.Now, sleep: time.Sleep, size: hostSize, free: freeBytes,
		self: buildinfo.Version, exec: func(path string, args []string) error { return syscall.Exec(path, args, os.Environ()) },
		listen: portFree, key: builtInReleaseKey()}
	if !*handedOver {
		if err := u.handover(ctx, *to, args); err != nil {
			return err
		}
	}
	return u.upgrade(ctx, *to, *force)
}

// handover runs the target release's own toft for the upgrade (D-65), so
// every upgrade runs the upgrader of the release it installs, its fixes
// included. It returns when this toft should upgrade: it is that release,
// or the release's toft could not be fetched (said, then carried on).
func (u *upgrader) handover(ctx context.Context, to string, args []string) error {
	if to == u.self {
		return nil
	}
	path, err := u.fetchToft(ctx, to)
	if err != nil {
		u.say("Upgrading with this toft (%s): the release's own could not be fetched: %v", u.self, err)
		return nil
	}
	return u.exec(path, append(append([]string{path, "upgrade"}, args...), "--handed-over"))
}

// fetchToft copies the release's toft out of its signed backend image.
func (u *upgrader) fetchToft(ctx context.Context, to string) (string, error) {
	env, err := readEnv(u.p.secrets)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(u.p.work, 0o700); err != nil {
		return "", err
	}
	refs, err := u.signedImages(ctx, u.repoFor(env), to)
	if err != nil {
		return "", err
	}
	if _, err := u.image(ctx, refs[0]); err != nil {
		return "", err
	}
	cid, err := u.dockerOut(ctx, "create", refs[0])
	if err != nil {
		return "", err
	}
	defer u.docker(context.WithoutCancel(ctx), io.Discard, "rm", cid) //nolint:errcheck
	path := filepath.Join(u.p.work, "toft-"+to)
	if err := u.docker(ctx, io.Discard, "cp", cid+":/usr/local/bin/toft", path); err != nil {
		return "", err
	}
	return path, os.Chmod(path, 0o755)
}

// signedImages returns the release's backend and frontend images by the
// digests its image manifest names, once the release key's signature of it
// checks out. release.yml signs the manifest and ships it in the backend
// repository as <to>-release, which the panel's registry login reads.
// A tag can be moved by whoever can push to the package; the manifest
// can't be signed without the release key. A release from before signed
// manifests has none, and is refused, unless it is the one this panel ran
// before this one and its images are still here: going back runs what ran.
func (u *upgrader) signedImages(ctx context.Context, repo, to string) ([]string, error) {
	ref := repo + "/paas-backend:" + to + "-release"
	// Two files in an amd64 image, read but never run: every host pulls the same one.
	if err := u.docker(ctx, io.Discard, "pull", "--quiet", "--platform=linux/amd64", ref); err != nil {
		if refs := u.previousImages(ctx, repo, to); refs != nil {
			u.say("%s has no signed image manifest: it ran here before, so the images it ran from are used, not pulled.", to)
			return refs, nil
		}
		return nil, fmt.Errorf("%s has no signed image manifest, which releases from before signed images lack, and can't be installed: %w", to, err)
	}
	cid, err := u.dockerOut(ctx, "create", "--platform=linux/amd64", ref, "/none")
	if err != nil {
		return nil, fmt.Errorf("unpack %s: %w", ref, err)
	}
	defer u.docker(context.WithoutCancel(ctx), io.Discard, "rm", cid) //nolint:errcheck
	dir, err := os.MkdirTemp(u.p.work, "release-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	var files [2][]byte
	for i, name := range []string{"toft-images", "toft-images.sig"} {
		if err := u.docker(ctx, io.Discard, "cp", cid+":/"+name, filepath.Join(dir, name)); err != nil {
			return nil, fmt.Errorf("unpack %s: %w", ref, err)
		}
		if files[i], err = os.ReadFile(filepath.Join(dir, name)); err != nil {
			return nil, err
		}
	}
	digests, err := verifyImages(u.key, to, files[0], files[1])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	return []string{repo + "/paas-backend@" + digests["backend"], repo + "/paas-frontend@" + digests["frontend"]}, nil
}

// previousImages returns to's images by their tags here when to is the
// release this panel ran before the current one and both are present, or nil.
func (u *upgrader) previousImages(ctx context.Context, repo, to string) []string {
	if u.readState().Previous != to {
		return nil
	}
	refs := []string{repo + "/paas-backend:" + to, repo + "/paas-frontend:" + to}
	for _, r := range refs {
		if id, err := u.dockerOut(ctx, "image", "inspect", "--format", "{{.Id}}", r); err != nil || id == "" {
			return nil
		}
	}
	return refs
}

// verifyImages checks key's signature (base64) of manifest, whose lines
// are "toft-image <version> <backend|frontend> sha256:<hex>", and returns
// the digests it names for to. Lines for other images are a later
// release's, and skipped.
func verifyImages(key ed25519.PublicKey, to string, manifest, sigB64 []byte) (map[string]string, error) {
	if key == nil {
		return nil, errors.New("this toft has no release key to check it with")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB64)))
	if err != nil || !ed25519.Verify(key, manifest, sig) {
		return nil, errors.New("the image manifest is not signed by the release key")
	}
	digests := map[string]string{}
	for _, line := range strings.Split(string(manifest), "\n") {
		f := strings.Fields(line)
		if len(f) != 4 || f[0] != "toft-image" || (f[2] != "backend" && f[2] != "frontend") {
			continue
		}
		if f[1] != to {
			return nil, fmt.Errorf("the image manifest is signed for %s, not %s", f[1], to)
		}
		hex, ok := strings.CutPrefix(f[3], "sha256:")
		if !ok || len(hex) != 64 || strings.Trim(hex, "0123456789abcdef") != "" {
			return nil, fmt.Errorf("the image manifest names %q, not a digest", f[3])
		}
		digests[f[2]] = f[3]
	}
	if digests["backend"] == "" || digests["frontend"] == "" {
		return nil, fmt.Errorf("the image manifest names no backend and frontend for %s", to)
	}
	return digests, nil
}

func (u *upgrader) repoFor(env map[string]string) string {
	if u.repo != "" {
		return u.repo
	}
	return "ghcr.io/" + env["GHCR_USER"]
}

func (u *upgrader) say(format string, a ...any)  { fmt.Fprintf(u.out, format+"\n", a...) }
func (u *upgrader) ok(format string, a ...any)   { u.say("  ✓ "+format, a...) }
func (u *upgrader) fail(format string, a ...any) { u.say("  ✗ "+format, a...) }
func (u *upgrader) docker(ctx context.Context, out io.Writer, args ...string) error {
	return u.h.run(ctx, out, nil, nil, "docker", args...)
}
func (u *upgrader) dockerOut(ctx context.Context, args ...string) (string, error) {
	var b bytes.Buffer
	err := u.docker(ctx, &b, args...)
	return strings.TrimSpace(b.String()), err
}

func (u *upgrader) upgrade(ctx context.Context, to string, force bool) (err error) {
	if err := os.MkdirAll(u.p.work, 0o700); err != nil {
		return err
	}
	unlock, err := lock(filepath.Join(u.p.work, "lock"))
	if err != nil {
		return err
	}
	defer unlock()

	env, err := readEnv(u.p.secrets)
	if err != nil {
		return fmt.Errorf("read %s: %w", u.p.secrets, err)
	}
	logPath := filepath.Join(u.p.work, fmt.Sprintf("%s-%s.log", to, u.now().UTC().Format("20060102T150405")))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	u.log = logFile
	defer func() {
		if err != nil {
			u.say("Its log: %s", logPath)
		}
	}()
	st := u.readState()
	from := st.Version
	end := u.beginRun(to, from, logPath)
	defer func() { end(err) }()
	repo := u.repoFor(env)
	if from == "" {
		u.say("Installing Toft %s", to)
	} else {
		u.say("Upgrading Toft %s → %s", from, to)
	}

	// ── Preflight ───────────────────────────────────────────────────────────
	if from != "" && !force && !buildinfo.Newer(to, from) {
		return fmt.Errorf("%s runs here; %s is not newer (--force to run it anyway)", from, to)
	}
	if err := u.preflight(ctx); err != nil {
		return err
	}
	// From v0.28.0 secrets are sealed in a format only v0.27.0 and later
	// read, and an unhealthy release rolls back to the one it started from
	// (D-126): a release that isn't one of those, such as an adopted panel's
	// images, is refused too.
	sealsV1 := !buildinfo.Newer(firstSealing, to)
	if _, ok := buildinfo.Parse(from); from != "" && sealsV1 && (!ok || buildinfo.Newer(firstReading, from)) {
		return fmt.Errorf("%s runs here: upgrade to %s first, then to %s (%s seals secrets in a format the releases before %s can't read, "+
			"and an upgrade that fails rolls back to the release it started from)", from, firstReading, to, firstSealing, firstReading)
	}

	// ── Images, while the running version keeps serving ─────────────────────
	u.step("pull")
	refs, err := u.signedImages(ctx, repo, to)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(refs))
	for i, ref := range refs {
		id, err := u.image(ctx, ref)
		if err != nil {
			return err
		}
		// The stack runs the release's tag (docker-compose.yml): here it
		// names the signed image, whatever the registry's tag points at.
		tag := repo + "/paas-" + []string{"backend", "frontend"}[i] + ":" + to
		if err := u.docker(ctx, io.Discard, "tag", ref, tag); err != nil {
			return fmt.Errorf("tag %s: %w", ref, err)
		}
		ids = append(ids, id)
	}
	u.note("pull", fmt.Sprintf("%d images", len(refs)))
	if from == "" {
		u.ok("Images ready")
	} else {
		u.ok("Images ready  (%s kept serving)", from)
	}

	// ── The release's bundle and toft, from its backend image ───────────────
	release := filepath.Join(u.p.releases, to)
	if err := u.stage(ctx, refs[0], release); err != nil {
		return err
	}
	if err := u.traefikDir(env, filepath.Join(release, "bundle")); err != nil {
		return fmt.Errorf("the panel's Traefik files: %w", err)
	}
	if err := u.checkGitPort(ctx, env, filepath.Join(release, "bundle")); err != nil {
		return err
	}
	// The backend reads run.json and the release check through this (D-68).
	if env["TOFT_UPGRADE_DIR"] == "" {
		if err := setEnv(u.p.secrets, "TOFT_UPGRADE_DIR", u.p.work); err != nil {
			return err
		}
	}
	// install.sh records this; a panel installed before it did still needs it
	// for its apps' temporary addresses (D-150).
	if env["TOFT_PANEL_IP"] == "" {
		if ip := u.publicIP(ctx); ip == "" {
			u.say("Couldn't find this server's public IP. With no domain, the panel's apps need it for their temporary addresses: run install.sh again to record it.")
		} else if err := setEnv(u.p.secrets, "TOFT_PANEL_IP", ip); err != nil {
			return err
		}
	}

	// ── Save the database, then migrate it ──────────────────────────────────
	u.step("save")
	running := u.postgresRunning(ctx)
	if running {
		dump, err := u.dump(ctx, env, from)
		if err != nil {
			return err
		}
		size := ""
		if fi, err := os.Stat(dump); err == nil {
			if size = fmt.Sprintf("%.0f MiB", float64(fi.Size())/(1<<20)); fi.Size() < 1<<20 {
				size = fmt.Sprintf("%d KiB", fi.Size()>>10)
			}
			u.note("save", size)
			size = "  (" + size + ")"
		}
		u.ok("Database saved to %s%s", dump, size)
	}
	u.step("migrate")
	contract, err := u.migrate(ctx, env, filepath.Join(release, "bundle"))
	if err != nil {
		u.fail("Migration failed; %s", map[bool]string{true: "nothing else changed", false: from + " keeps running"}[from == ""])
		return err
	}

	// ── Swap ───────────────────────────────────────────────────────────────
	// A release from v0.28.0 refuses the example's ENCRYPTION_KEY: replace
	// it now, keeping it as the previous key, so the new backend's start
	// seals every secret under the new one (D-126). A rotation left
	// unfinished (ENCRYPTION_KEY_PREVIOUS set: an upgrade that rolled back,
	// or one interrupted) is finished too.
	placeholder := config.IsPlaceholder(env["ENCRYPTION_KEY"])
	rotated := sealsV1 && (placeholder || env["ENCRYPTION_KEY_PREVIOUS"] != "")
	if rotated {
		if placeholder {
			u.say("ENCRYPTION_KEY is the example's placeholder, known to anyone with the repository; replacing it")
		}
		if err := u.newKey(); err != nil {
			return err
		}
	}
	prevTag := env["IMAGE_TAG"]
	prevTarget, _ := os.Readlink(u.p.current)
	u.step("restart")
	if err := u.swap(ctx, filepath.Join(release, "bundle"), to); err != nil {
		return err
	}
	u.step("health")
	started := u.now()
	if err := u.healthy(ctx, ids); err == nil {
		took := int(u.now().Sub(started).Seconds())
		u.note("health", fmt.Sprintf("%d s", took))
		u.ok("Healthy after %d s", took)
	} else {
		u.fail("Not healthy after %s: %v", u.wait, err)
		if from == "" || prevTarget == "" {
			return fmt.Errorf("%s did not become healthy: %w", to, err)
		}
		u.say("\nRolling back")
		if rerr := u.rollback(ctx, env, prevTarget, prevTag, from, contract); rerr != nil {
			return fmt.Errorf("%s was unhealthy and the rollback to %s failed: %w", to, from, rerr)
		}
		u.say("\nToft %s is running. The upgrade to %s failed.", from, to)
		return fmt.Errorf("%s did not become healthy: %w", to, errRolledBack)
	}

	// ── Settle ─────────────────────────────────────────────────────────────
	if err := u.installHost(ctx, filepath.Join(release, "bundle", "host"), env["TOFT_DATA_DIR"]); err != nil {
		u.fail("Host units not updated: %v", err)
	}
	if err := installFile(filepath.Join(release, "toft"), u.p.toft, 0o755); err != nil {
		u.fail("toft not updated: %v", err)
	}
	u.writeState(state{Version: to, Previous: from, Images: refs, UpdatedAt: u.now().UTC()})
	u.pruneReleases(to, from)
	if rotated {
		if err := u.finishRotation(ctx); err != nil {
			u.fail("The key rotation is unfinished: %v", err)
		}
	}
	u.say("\nToft %s is running.", to)
	return nil
}

// publicIP finds this server's public IPv4 address as install.sh does: ask
// ifconfig.me, or take the source address of the route out. "" if neither says.
func (u *upgrader) publicIP(ctx context.Context) string {
	var out bytes.Buffer
	if u.h.run(ctx, &out, nil, nil, "curl", "-fsS4", "--max-time", "5", "https://ifconfig.me") == nil {
		if ip := net.ParseIP(strings.TrimSpace(out.String())); ip.To4() != nil {
			return ip.String()
		}
	}
	out.Reset()
	if u.h.run(ctx, &out, nil, nil, "ip", "-4", "route", "get", "1.1.1.1") != nil {
		return ""
	}
	fields := strings.Fields(out.String())
	for i := 0; i+1 < len(fields); i++ {
		if ip := net.ParseIP(fields[i+1]); fields[i] == "src" && ip.To4() != nil {
			return ip.String()
		}
	}
	return ""
}

// preflight checks what the release needs before anything changes (D-29's
// Docker, and room for the images and a dump).
func (u *upgrader) preflight(ctx context.Context) error {
	v, err := u.dockerOut(ctx, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return fmt.Errorf("docker is not running: %w", err)
	}
	if major, _ := strconv.Atoi(strings.SplitN(v, ".", 2)[0]); major < 29 {
		return fmt.Errorf("Docker %s runs here; Toft needs Docker 29 or newer", v)
	}
	if free, err := u.free(filepath.Dir(u.p.work)); err == nil {
		if free < 3<<30 {
			// 3 GiB is 3.22 GB: 3.2 would read as enough beside "only 3.2 GB free".
			return fmt.Errorf("only %.1f GB free; an upgrade needs 3.3 GB for the images and a dump", float64(free)/1e9)
		}
		u.note("preflight", fmt.Sprintf("%.0f GB free", float64(free)/1e9))
	}
	u.ok("Requirements met · Docker %s", v)
	return nil
}

// image makes ref, an image by digest, present (a local copy is that
// image) and returns its ID.
func (u *upgrader) image(ctx context.Context, ref string) (string, error) {
	if id, err := u.dockerOut(ctx, "image", "inspect", "--format", "{{.Id}}", ref); err == nil && id != "" {
		return id, nil
	}
	if err := u.docker(ctx, io.Discard, "pull", "--quiet", ref); err != nil {
		return "", fmt.Errorf("pull %s: %w", ref, err)
	}
	id, err := u.dockerOut(ctx, "image", "inspect", "--format", "{{.Id}}", ref)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", ref, err)
	}
	return id, nil
}

// stage copies the release's bundle and toft binary out of its backend
// image, and links the bundle's .env to the secrets.
func (u *upgrader) stage(ctx context.Context, backend, release string) error {
	_ = os.RemoveAll(release)
	if err := os.MkdirAll(release, 0o755); err != nil {
		return err
	}
	cid, err := u.dockerOut(ctx, "create", backend)
	if err != nil {
		return fmt.Errorf("unpack %s: %w", backend, err)
	}
	defer u.docker(context.WithoutCancel(ctx), io.Discard, "rm", cid) //nolint:errcheck
	for _, cp := range [][2]string{{"/usr/share/toft/bundle", filepath.Join(release, "bundle")}, {"/usr/local/bin/toft", filepath.Join(release, "toft")}} {
		if err := u.docker(ctx, io.Discard, "cp", cid+":"+cp[0], cp[1]); err != nil {
			return fmt.Errorf("unpack %s: %w", backend, err)
		}
	}
	bundle := filepath.Join(release, "bundle")
	if err := os.MkdirAll(filepath.Join(bundle, "traefik", "logs"), 0o755); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(bundle, ".env"))
	return os.Symlink(u.p.secrets, filepath.Join(bundle, ".env"))
}

func (u *upgrader) postgresRunning(ctx context.Context) bool {
	s, err := u.dockerOut(ctx, "inspect", "--format", "{{.State.Running}}", "paas-postgres")
	return err == nil && s == "true"
}

// dump saves the database (pg_dump's custom format) and keeps the last
// keepDumps.
func (u *upgrader) dump(ctx context.Context, env map[string]string, version string) (string, error) {
	name := filepath.Join(u.p.work, fmt.Sprintf("%s-%s.dump", version, u.now().UTC().Format("20060102T150405")))
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	err = u.docker(ctx, f, "exec", "paas-postgres", "pg_dump", "-Fc", "-U", env["POSTGRES_USER"], env["POSTGRES_DB"])
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("save the database: %w", err)
	}
	// Newest first by the time in the name: <version>-<YYYYMMDDTHHMMSS>.dump.
	dumps, _ := filepath.Glob(filepath.Join(u.p.work, "*.dump"))
	stamp := func(p string) string { return strings.TrimSuffix(p, ".dump")[max(0, len(p)-len(".dump")-15):] }
	sort.Slice(dumps, func(i, j int) bool { return stamp(dumps[i]) > stamp(dumps[j]) })
	for _, old := range dumps[min(len(dumps), keepDumps):] {
		_ = os.Remove(old)
	}
	return name, nil
}

// compose runs docker compose on a bundle directory with the secrets, and
// with git hosting (docker-compose.git.yml, D-71) when the bundle has it
// and the install has not turned it off.
func (u *upgrader) compose(ctx context.Context, bundle string, args ...string) error {
	subnet, _ := u.dockerOut(ctx, "network", "inspect", "paas-network", "-f", "{{range .IPAM.Config}}{{.Subnet}},{{end}}")
	env := []string{"TOFT_CONTROL_PLANE_SUBNET=" + strings.TrimSuffix(subnet, ",")}
	base := []string{"compose", "--project-directory", bundle, "-f", filepath.Join(bundle, "docker-compose.yml")}
	u.git = u.withGit(bundle)
	if u.git {
		base = append(base, "-f", filepath.Join(bundle, gitCompose))
	}
	base = append(base, "--env-file", u.p.secrets)
	return u.h.run(ctx, io.Discard, nil, env, "docker", append(base, args...)...)
}

// gitCompose is git hosting's compose file in a bundle (D-71).
const gitCompose = "docker-compose.git.yml"

// withGit says whether bundle runs git hosting here: it has the file, and
// the secrets don't say TOFT_GIT=off.
func (u *upgrader) withGit(bundle string) bool {
	if _, err := os.Stat(filepath.Join(bundle, gitCompose)); err != nil {
		return false
	}
	env, err := readEnv(u.p.secrets)
	return err != nil || !strings.EqualFold(strings.TrimSpace(env["TOFT_GIT"]), "off")
}

// checkGitPort refuses a release with git hosting when another program
// holds the port git's SSH is published on (sshd moved to 2222, say);
// TOFT_GIT_SSH_PORT moves it.
func (u *upgrader) checkGitPort(ctx context.Context, env map[string]string, bundle string) error {
	if !u.withGit(bundle) {
		return nil
	}
	port := env["TOFT_GIT_SSH_PORT"]
	if port == "" {
		port = "2222"
	}
	if u.listen == nil || u.listen(port) {
		return nil
	}
	// Ours already: the running backend publishes it.
	if out, err := u.dockerOut(ctx, "port", "paas-backend", "2222/tcp"); err == nil && strings.Contains(out, ":"+port) {
		return nil
	}
	return fmt.Errorf("port %s is in use by another program: git over SSH needs it. Set TOFT_GIT_SSH_PORT in %s to a free port, or TOFT_GIT=off to install without git", port, u.p.secrets)
}

// portFree says whether TCP port is free on every address of the host.
func portFree(port string) bool {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// migrate brings the database to the release's schema: the data steps, then
// Atlas (which never drops a table, column or foreign key, atlas.hcl), then
// the release's planned drops. It reports whether a planned drop ran, which
// makes a rollback restore the dump.
func (u *upgrader) migrate(ctx context.Context, env map[string]string, bundle string) (contract bool, err error) {
	if err := u.compose(ctx, bundle, "up", "-d", "--no-recreate", "postgres"); err != nil {
		return false, fmt.Errorf("start postgres: %w", err)
	}
	ready := false
	for i := 0; i < 30 && !ready; i++ {
		// Over TCP: on a fresh volume the image's setup server answers on the
		// socket before it has created POSTGRES_DB, and listens on nothing else.
		ready = u.docker(ctx, io.Discard, "exec", "paas-postgres", "pg_isready", "-q", "-h", "127.0.0.1", "-U", env["POSTGRES_USER"]) == nil
		if !ready {
			u.sleep(time.Second)
		}
	}
	if !ready {
		return false, errors.New("postgres did not become ready in 30 s")
	}
	user, db := env["POSTGRES_USER"], env["POSTGRES_DB"]
	psql := func(stdin io.Reader, args ...string) error {
		return u.h.run(ctx, io.Discard, stdin, nil, "docker", append([]string{"exec", "-i", "paas-postgres", "psql", "-v", "ON_ERROR_STOP=1", "-q", "-U", user, "-d", db}, args...)...)
	}
	var exists bytes.Buffer
	if err := u.h.run(ctx, &exists, nil, nil, "docker", "exec", "paas-postgres", "psql", "-tA", "-U", user, "-d", db, "-c", "SELECT 1 FROM pg_database WHERE datname = 'atlas_dev'"); err != nil {
		return false, fmt.Errorf("read databases: %w", err)
	}
	if strings.TrimSpace(exists.String()) != "1" {
		if err := psql(nil, "-c", "CREATE DATABASE atlas_dev"); err != nil {
			return false, fmt.Errorf("create atlas_dev: %w", err)
		}
	}
	migrations := filepath.Join(bundle, "migrations")
	if err := u.runFile(psql, filepath.Join(migrations, "premigrate.sql")); err != nil {
		return false, fmt.Errorf("data steps (premigrate.sql): %w", err)
	}
	url := fmt.Sprintf("postgres://%s:%s@postgres:5432/%%s?sslmode=disable", user, env["POSTGRES_PASSWORD"])
	if err := u.docker(ctx, u.log, "run", "--rm", "--network", "paas-internal", "-v", migrations+":/migrations:ro", atlasImage,
		"schema", "apply", "--url", fmt.Sprintf(url, db), "--dev-url", fmt.Sprintf(url, "atlas_dev"),
		"--to", "file:///migrations/schema.sql", "--config", "file:///migrations/atlas.hcl", "--env", "toft", "--auto-approve"); err != nil {
		// The error repeats the command, the URLs' password with it: it goes
		// to run.json, the panel's Upgrade page and the install log.
		return false, fmt.Errorf("schema (Atlas): %s", strings.ReplaceAll(err.Error(), ":"+env["POSTGRES_PASSWORD"]+"@", ":…@"))
	}
	if hasStatements(filepath.Join(migrations, "contract.sql")) {
		if err := u.runFile(psql, filepath.Join(migrations, "contract.sql")); err != nil {
			return true, fmt.Errorf("planned drops (contract.sql): %w", err)
		}
		contract = true
	}
	u.note("migrate", map[bool]string{true: "planned drops run", false: "nothing dropped"}[contract])
	u.ok("Schema migrated%s", map[bool]string{true: ", planned drops run", false: ""}[contract])
	return contract, nil
}

// hasStatements reports whether the SQL file at path holds anything but
// "--" comments and blank lines: a contract.sql between contract steps is
// kept with its header, and must not count as a planned drop (which makes a
// failed upgrade restore its dump). A missing file holds none; one that
// can't be read counts as holding some, so running it reports the error.
func hasStatements(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return !errors.Is(err, os.ErrNotExist)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
			return true
		}
	}
	return false
}

func (u *upgrader) runFile(psql func(io.Reader, ...string) error, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return psql(f)
}

// swap points /opt/toft at the release's bundle, keeps the panel's routes,
// records the image tag and starts the stack.
// traefikDir keeps the panel's Traefik files (the middlewares, the routes
// the backend writes) in one place outside the releases
// (D-65). Bound inside a release, a container held that release's real
// path: Traefik went on reading an old release's copy, or a deleted one
// when a release was staged again. An install from before moves there.
func (u *upgrader) traefikDir(env map[string]string, bundle string) error {
	dir := env["TOFT_TRAEFIK_DIR"]
	if dir == "" {
		dir = u.p.traefik
		if err := setEnv(u.p.secrets, "TOFT_TRAEFIK_DIR", dir); err != nil {
			return err
		}
		env["TOFT_TRAEFIK_DIR"] = dir
	}
	if err := os.MkdirAll(filepath.Join(dir, "dynamic"), 0o755); err != nil {
		return err
	}
	// The access log file the panel's Traefik wrote, never rotated: it
	// logs to its container now, capped by Docker's local driver.
	if err := os.RemoveAll(filepath.Join(dir, "logs")); err != nil {
		return err
	}
	if err := installFile(filepath.Join(bundle, "traefik", "dynamic", "dynamic.yml"), filepath.Join(dir, "dynamic", "dynamic.yml"), 0o644); err != nil {
		return err
	}
	routes := filepath.Join(dir, "dynamic", "panel.yml")
	if _, err := os.Stat(routes); err != nil {
		if old, err := os.ReadFile(filepath.Join(u.p.current, "traefik", "dynamic", "panel.yml")); err == nil {
			return writeAtomic(routes, old, 0o644)
		}
	}
	return nil
}

func (u *upgrader) swap(ctx context.Context, bundle, to string) error {
	if err := setEnv(u.p.secrets, "IMAGE_TAG", to); err != nil {
		return fmt.Errorf("record the release: %w", err)
	}
	if err := relink(bundle, u.p.current); err != nil {
		return err
	}
	if err := u.compose(ctx, u.p.current, "up", "-d", "--remove-orphans"); err != nil {
		return fmt.Errorf("start %s: %w", to, err)
	}
	if u.git {
		u.ok("Started traefik, postgres, backend, frontend, git")
	} else {
		u.ok("Started traefik, postgres, backend, frontend")
	}
	return nil
}

// healthy waits for the stack's containers to report healthy, with the
// backend and frontend running the release's images.
func (u *upgrader) healthy(ctx context.Context, want []string) error {
	deadline := u.now().Add(u.wait)
	var last error
	for {
		last = u.checkHealth(ctx, want)
		if last == nil || !u.now().Before(deadline) {
			return last
		}
		u.sleep(u.poll)
	}
}

func (u *upgrader) checkHealth(ctx context.Context, want []string) error {
	names := []string{"paas-backend", "paas-frontend", "paas-traefik", "paas-postgres"}
	if u.git {
		names = append(names, "paas-git")
	}
	out, err := u.dockerOut(ctx, append([]string{"inspect", "--format", "{{.Name}} {{.Image}} {{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}"}, names...)...)
	if err != nil {
		return fmt.Errorf("inspect the stack: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		name := strings.TrimPrefix(f[0], "/")
		if f[2] != "healthy" {
			return fmt.Errorf("%s is %s", name, f[2])
		}
		if (name == "paas-backend" && f[1] != want[0]) || (name == "paas-frontend" && f[1] != want[1]) {
			return fmt.Errorf("%s does not run the release's image", name)
		}
	}
	return nil
}

// rollback starts the previous release again on the newer schema; after a
// planned drop it restores the dump first, losing what was written since.
// restoreScript restores the dump on stdin over the database ($2, as $1):
// public is dropped and recreated, then the dump's SQL runs, all in one
// transaction.
const restoreScript = `{ echo 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;'; pg_restore -f -; } | psql -v ON_ERROR_STOP=1 --single-transaction -q -U "$1" -d "$2"`

func (u *upgrader) rollback(ctx context.Context, env map[string]string, prevTarget, prevTag, from string, contract bool) error {
	if contract {
		// This upgrade's dump: the newest of the version it started from.
		dumps, _ := filepath.Glob(filepath.Join(u.p.work, from+"-*.dump"))
		sort.Strings(dumps)
		if len(dumps) == 0 {
			return errors.New("the release dropped columns and no dump is left to restore")
		}
		f, err := os.Open(dumps[len(dumps)-1])
		if err != nil {
			return err
		}
		defer f.Close()
		// The release's new tables reference the dump's (a foreign key), so
		// pg_restore --clean can't drop them and restores half: the schema
		// is reset and the dump restored in one transaction instead. A
		// failure changes nothing, and the previous release runs on the
		// newer schema (D-63), so it is started either way.
		if err := u.h.run(ctx, io.Discard, f, nil, "docker", "exec", "-i", "paas-postgres", "sh", "-c", restoreScript, "sh", env["POSTGRES_USER"], env["POSTGRES_DB"]); err != nil {
			u.fail("Database not restored from %s (%v); %s starts on the new schema", dumps[len(dumps)-1], err, from)
		} else {
			u.ok("Database restored from %s", dumps[len(dumps)-1])
		}
	}
	if err := setEnv(u.p.secrets, "IMAGE_TAG", prevTag); err != nil {
		return err
	}
	if err := relink(prevTarget, u.p.current); err != nil {
		return err
	}
	if err := u.compose(ctx, u.p.current, "up", "-d", "--remove-orphans"); err != nil {
		return err
	}
	st := u.readState()
	ids := make([]string, 0, 2)
	for _, ref := range st.Images {
		id, err := u.dockerOut(ctx, "image", "inspect", "--format", "{{.Id}}", ref)
		if err != nil {
			return fmt.Errorf("the previous image %s is gone: %w", ref, err)
		}
		ids = append(ids, id)
	}
	if len(ids) == 2 {
		if err := u.healthy(ctx, ids); err != nil {
			return err
		}
	}
	u.ok("Started %s again%s", from, map[bool]string{true: "", false: "  (it runs on the new schema; nothing was dropped)"}[contract])
	return nil
}

// installHost installs the release's host units, as the provisioner does on
// remote servers: the host firewall and the backup timer; the slice that
// caps the apps on this server (D-26, D-62); and the daily release check
// (D-68).
func (u *upgrader) installHost(ctx context.Context, src, dataDir string) error {
	// The backend writes the panel host's backup settings into
	// <TOFT_DATA_DIR>/backup; the timer reads /var/lib/toft/backup (D-43).
	if dataDir == "" {
		dataDir = "/var/paas"
	}
	backupDir := filepath.Join(u.p.units, dataDir, "backup")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	link := filepath.Join(u.p.units, "var/lib/toft/backup")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return err
		}
		if err := relink(filepath.Join(dataDir, "backup"), link); err != nil {
			return err
		}
	}
	cpus, mem, err := u.size()
	if err != nil {
		return fmt.Errorf("read the host's size: %w", err)
	}
	appsCPU, appsMem := reservation.Apps(cpus, mem)
	slice := fmt.Sprintf(`# Written by toft upgrade (D-26, D-62): the apps on the panel's server share
# what the system's (%g vCPU, %d GiB) and the panel's (%g vCPU, %d GiB)
# reservation leave. Edits are overwritten at the next upgrade.
[Unit]
Description=Toft apps on the panel's server
Before=slices.target

[Slice]
CPUAccounting=yes
MemoryAccounting=yes
CPUQuota=%d%%
MemoryMax=%d
`, reservation.SystemCPU, reservation.SystemMemory>>30, reservation.PanelCPU, reservation.PanelMemory>>30, int(appsCPU*100), appsMem)
	unit := filepath.Join(u.p.units, "etc/systemd/system/toft-apps.slice")
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		return err
	}
	if err := writeAtomic(unit, []byte(slice), 0o644); err != nil {
		return err
	}
	for _, f := range [][3]string{
		{"firewall.nft", "etc/toft/firewall.nft", "0644"},
		{"toft-firewall.service", "etc/systemd/system/toft-firewall.service", "0644"},
		{"toft-backup.sh", "usr/local/lib/toft/backup.sh", "0755"},
		{"toft-backup.service", "etc/systemd/system/toft-backup.service", "0644"},
		{"toft-backup.timer", "etc/systemd/system/toft-backup.timer", "0644"},
		{"toft-release-check.service", "etc/systemd/system/toft-release-check.service", "0644"},
		{"toft-release-check.timer", "etc/systemd/system/toft-release-check.timer", "0644"},
	} {
		mode, _ := strconv.ParseUint(f[2], 8, 32)
		if err := installFile(filepath.Join(src, f[0]), filepath.Join(u.p.units, f[1]), os.FileMode(mode)); err != nil {
			return err
		}
	}
	for _, args := range [][]string{
		{"nft", "-c", "-f", filepath.Join(u.p.units, "etc/toft/firewall.nft")},
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "toft-firewall"},
		{"systemctl", "restart", "toft-firewall"},
		{"systemctl", "enable", "--now", "toft-backup.timer"},
		{"systemctl", "enable", "--now", "toft-release-check.timer"},
	} {
		if err := u.h.run(ctx, io.Discard, nil, nil, args[0], args[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func freeBytes(path string) (uint64, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, err
	}
	return fs.Bavail * uint64(fs.Bsize), nil
}

// hostSize reads the host's cores and memory (MemTotal).
func hostSize() (int, int64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
			kib, err := strconv.ParseInt(f[1], 10, 64)
			return runtime.NumCPU(), kib << 10, err
		}
	}
	return 0, 0, errors.New("no MemTotal in /proc/meminfo")
}

// pruneReleases keeps the running and the previous release's bundles.
func (u *upgrader) pruneReleases(keep ...string) {
	entries, _ := os.ReadDir(u.p.releases)
	for _, e := range entries {
		kept := false
		for _, k := range keep {
			kept = kept || e.Name() == k
		}
		if !kept {
			_ = os.RemoveAll(filepath.Join(u.p.releases, e.Name()))
		}
	}
}

func (u *upgrader) readState() state {
	var st state
	if b, err := os.ReadFile(filepath.Join(u.p.work, "state.json")); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func (u *upgrader) writeState(st state) {
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := writeAtomic(filepath.Join(u.p.work, "state.json"), b, 0o600); err != nil {
		u.fail("state not recorded: %v", err)
	}
}

// ─── files ───────────────────────────────────────────────────────────────────

func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another toft upgrade or key rotation is running")
	}
	return func() { f.Close() }, nil
}

// readEnv reads KEY=VALUE lines; comments and blank lines are skipped.
func readEnv(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			env[strings.TrimSpace(k)] = v
		}
	}
	return env, nil
}

// setEnv sets keys in an env file, given as key, value pairs, in one write,
// keeping every other line and its mode.
func setEnv(path string, kv ...string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	for i := 0; i+1 < len(kv); i += 2 {
		key, value := kv[i], kv[i+1]
		found := false
		for j, line := range lines {
			if k, _, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == key {
				lines[j] = key + "=" + value
				found = true
			}
		}
		if !found {
			lines = append(lines, key+"="+value)
		}
	}
	return writeAtomic(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// relink points link at target, replacing it in one rename.
func relink(target, link string) error {
	tmp := link + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func installFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return writeAtomic(dst, b, mode)
}

// ─── versions ────────────────────────────────────────────────────────────────
