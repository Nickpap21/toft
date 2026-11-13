package instance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
	"go.uber.org/zap"
)

// Upgrades from the panel (D-68). toft upgrade runs on the host, as a
// transient systemd unit a helper container starts, so it outlives the
// panel's restart; the panel follows it through toft's own files in
// /var/lib/toft/upgrade, mounted read-only: state.json (what runs),
// run.json and run.out (the upgrade in progress or the last one) and
// available.json (the daily release check).

// releaseNotes is where a release's notes are, by tag.
const releaseNotes = "https://github.com/Nickpap21/toft/releases/tag/"

// SettingUpgrade is the instance_settings key holding who started the
// upgrade in progress, so its outcome is written to the audit log as theirs
// by the release that comes up after it.
const SettingUpgrade = "panel_upgrade"

// Install is how this panel was installed: installer (install.sh, which
// upgrades from the panel) or deploy_sh (which upgrades with deploy.sh).
type Install string

const (
	InstallInstaller Install = "installer"
	InstallDeploySh  Install = "deploy_sh"
)

// UpgradeRun is an upgrade in progress, or the last one.
type UpgradeRun struct {
	To         string     `json:"to"`
	From       string     `json:"from"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	// Step is starting (asked for, not begun yet), preflight, pull, save,
	// migrate, restart or health.
	Step string `json:"step"`
	// Outcome is running, succeeded, rolled_back or failed.
	Outcome string `json:"outcome"`
	Error   string `json:"error"`
	// Output is what toft upgrade printed, line by line.
	Output []string `json:"output"`
	// Notes are facts of its steps, by step ("38 GB free", "2 images").
	Notes map[string]string `json:"notes"`
}

// UpgradeStatus is the panel's version, the newest release, and the
// upgrade in progress or the last one.
type UpgradeStatus struct {
	Install Install `json:"install"`
	Current string  `json:"current"`
	// Latest is "" until the release check has run.
	Latest     string     `json:"latest"`
	Available  bool       `json:"available"`
	CheckedAt  *time.Time `json:"checked_at"`
	CheckError string     `json:"check_error"`
	NotesURL   string     `json:"notes_url"`
	// Since is when the running release was installed or upgraded to.
	Since *time.Time  `json:"since"`
	Run   *UpgradeRun `json:"run"`
	// Dumps is how many database dumps toft keeps from before upgrades,
	// LastDumpAt when the newest was saved.
	Dumps      int        `json:"dumps"`
	LastDumpAt *time.Time `json:"last_dump_at"`
}

// Errors of upgrades.
var (
	ErrUpgradeDeploySh = errors.New("this panel was set up with deploy.sh, which upgrades it; move it to the installer to upgrade from here")
	ErrUpgradeRunning  = errors.New("an upgrade is already running")
	ErrBadRelease      = errors.New("that is not the newest release, or not newer than the one running")
)

var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// WithUpgrades sets where toft's upgrade files are mounted.
func (s *Service) WithUpgrades(dir string) *Service {
	s.upgradeDir = dir
	return s
}

func (s *Service) readUpgradeFile(name string, v any) bool {
	if s.upgradeDir == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(s.upgradeDir, name))
	return err == nil && json.Unmarshal(b, v) == nil
}

// UpgradeStatus reads the upgrade files. Operators only.
func (s *Service) UpgradeStatus(ctx context.Context, actorID string) (*UpgradeStatus, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	return s.upgradeStatus(), nil
}

// PanelReleases are the releases toft upgrade keeps on the panel's server,
// the running one and the one before it (D-135); nil when the panel wasn't
// installed with the installer, so no release image is offered for removal.
func (s *Service) PanelReleases() []string {
	var state struct {
		Version  string `json:"version"`
		Previous string `json:"previous"`
	}
	if !s.readUpgradeFile("state.json", &state) || state.Version == "" {
		return nil
	}
	keep := []string{state.Version, state.Previous}
	// An upgrade under way pulls its release: a disk clean keeps it too.
	if st := s.upgradeStatus(); st.Run != nil && st.Run.Outcome == "running" && st.Run.To != "" {
		keep = append(keep, st.Run.To)
	}
	return keep
}

func (s *Service) upgradeStatus() *UpgradeStatus {
	st := &UpgradeStatus{Install: InstallDeploySh, Current: buildinfo.Version}
	var state struct {
		Version   string    `json:"version"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if s.readUpgradeFile("state.json", &state) && state.Version != "" {
		st.Install = InstallInstaller
		if !state.UpdatedAt.IsZero() {
			st.Since = &state.UpdatedAt
		}
	}
	var av struct {
		Latest    string    `json:"latest"`
		CheckedAt time.Time `json:"checked_at"`
		Error     string    `json:"error"`
	}
	if s.readUpgradeFile("available.json", &av) {
		st.Latest, st.CheckError = av.Latest, av.Error
		if !av.CheckedAt.IsZero() {
			st.CheckedAt = &av.CheckedAt
		}
	}
	if st.Latest != "" {
		st.NotesURL = releaseNotes + st.Latest
		st.Available = buildinfo.Newer(st.Latest, st.Current)
	}
	var r UpgradeRun
	if s.readUpgradeFile("run.json", &r) && r.To != "" {
		if b, err := os.ReadFile(filepath.Join(s.upgradeDir, "run.out")); err == nil {
			r.Output = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		}
		if r.Output == nil {
			r.Output = []string{}
		}
		if r.Notes == nil {
			r.Notes = map[string]string{}
		}
		st.Run = &r
	}
	if s.upgradeDir != "" {
		dumps, _ := filepath.Glob(filepath.Join(s.upgradeDir, "*.dump"))
		for _, d := range dumps {
			if fi, err := os.Stat(d); err == nil {
				st.Dumps++
				if t := fi.ModTime().UTC(); st.LastDumpAt == nil || t.After(*st.LastDumpAt) {
					st.LastDumpAt = &t
				}
			}
		}
	}
	// Asked for, but toft hasn't written its run yet (it may be fetching
	// the release's own upgrader first).
	s.upgradeMu.Lock()
	req := s.upgradeAsked
	s.upgradeMu.Unlock()
	if req != nil && (st.Run == nil || st.Run.StartedAt.Before(req.At)) && time.Since(req.At) < 10*time.Minute {
		st.Run = &UpgradeRun{To: req.To, From: st.Current, StartedAt: req.At, Step: "starting", Outcome: "running", Output: []string{}, Notes: map[string]string{}}
	}
	return st
}

// upgradeAsk is an upgrade the panel started, for the audit log.
type upgradeAsk struct {
	To string    `json:"to"`
	By string    `json:"by"`
	At time.Time `json:"at"`
	// Recorded is the run (its start) whose outcome is in the audit log.
	Recorded *time.Time `json:"recorded,omitempty"`
}

// StartUpgrade upgrades the panel to the newest release, with the
// operator's password (D-23). It returns once toft upgrade has started on
// the host; the panel follows it with UpgradeStatus.
func (s *Service) StartUpgrade(ctx context.Context, actorID, password, to string) (*UpgradeStatus, error) {
	if err := s.reauthenticate(ctx, actorID, password); err != nil {
		return nil, err
	}
	st := s.upgradeStatus()
	switch {
	case st.Install != InstallInstaller:
		return nil, ErrUpgradeDeploySh
	case st.Run != nil && st.Run.Outcome == "running":
		return nil, ErrUpgradeRunning
	case !releaseTag.MatchString(to) || to != st.Latest || !st.Available:
		return nil, ErrBadRelease
	}
	if s.panel == nil {
		return nil, errors.New("the panel's monitor is not configured")
	}
	// The upgrade restarts the panel's containers under a run of updates.
	if active, err := s.panelUpdateRunActive(ctx); err != nil {
		return nil, err
	} else if active {
		return nil, server.ErrUpdateRunning
	}
	now := time.Now().UTC()
	// A transient unit: it runs on after this helper, and after the panel
	// itself restarts.
	argv := []string{"systemd-run", fmt.Sprintf("--unit=toft-upgrade-%d", now.Unix()), "--collect",
		"--description=Toft upgrade to " + to, "/usr/local/bin/toft", "upgrade", "--to", to}
	if _, err := s.panel.probe.RunOnHost(ctx, argv); err != nil {
		return nil, fmt.Errorf("start the upgrade: %w", err)
	}
	ask := upgradeAsk{To: to, By: actorID, At: now}
	s.upgradeMu.Lock()
	s.upgradeAsked = &ask
	s.upgradeMu.Unlock()
	if raw, err := json.Marshal(ask); err == nil {
		if err := s.repo.MergeSetting(ctx, SettingUpgrade, raw, actorID); err != nil {
			s.log.Warn("recording who started the upgrade failed", zap.Error(err))
		}
	}
	s.audit(ctx, actorID, "panel.upgrade_started", "", map[string]any{"to": to, "from": st.Current})
	return s.upgradeStatus(), nil
}

// CheckRelease runs the release check now ("Check now"). Operators only.
func (s *Service) CheckRelease(ctx context.Context, actorID string) (*UpgradeStatus, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	if s.upgradeStatus().Install != InstallInstaller {
		return nil, ErrUpgradeDeploySh
	}
	if s.panel == nil {
		return nil, errors.New("the panel's monitor is not configured")
	}
	// A refused login is recorded in available.json, which the answer shows.
	if _, err := s.panel.probe.RunOnHost(ctx, []string{"/usr/local/bin/toft", "release-check"}); err != nil {
		s.log.Info("release check failed", zap.Error(err))
	}
	return s.upgradeStatus(), nil
}

// recordUpgrade writes a finished upgrade's outcome to the audit log once,
// as the operator who started it, from whichever release came up after it.
// The panel monitor calls it every tick; it reads two small files.
func (s *Service) recordUpgrade(ctx context.Context) {
	var r UpgradeRun
	if !s.readUpgradeFile("run.json", &r) || r.Outcome == "running" || r.FinishedAt == nil {
		return
	}
	var ask upgradeAsk
	raw, err := s.repo.GetSetting(ctx, SettingUpgrade)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return
	}
	if raw != nil {
		_ = json.Unmarshal(raw, &ask)
	}
	if ask.Recorded != nil && !r.StartedAt.After(*ask.Recorded) {
		return
	}
	// The run's start marks it recorded, whoever started it.
	by := ""
	if ask.To == r.To && !ask.At.After(r.StartedAt) {
		by = ask.By
	}
	started := r.StartedAt
	mark, _ := json.Marshal(map[string]any{"recorded": started})
	if err := s.repo.MergeSetting(ctx, SettingUpgrade, mark, by); err != nil {
		s.log.Warn("recording the upgrade's outcome failed", zap.Error(err))
		return
	}
	detail := map[string]any{"to": r.To, "from": r.From, "seconds": int(r.FinishedAt.Sub(r.StartedAt).Seconds())}
	switch {
	case r.Outcome == "succeeded" && r.From == "":
		s.audit(ctx, by, "panel.installed", "", detail)
	case r.Outcome == "succeeded":
		s.audit(ctx, by, "panel.upgraded", "", detail)
	case r.Outcome == "rolled_back":
		detail["error"] = r.Error
		s.audit(ctx, by, "panel.upgrade_rolled_back", "", detail)
		s.alertUpgrade(fmt.Sprintf("The upgrade to %s wasn't healthy, so %s runs again. The log is on the panel's Instance page.", r.To, r.From))
	default:
		detail["error"] = r.Error
		s.audit(ctx, by, "panel.upgrade_failed", "", detail)
		s.alertUpgrade(fmt.Sprintf("The upgrade to %s stopped: %s", r.To, r.Error))
	}
}

// UpgradeAlerter sends the panel's own alerts; alert.Service satisfies it.
type UpgradeAlerter interface {
	PanelUpgradeFailed(detail string)
}

// WithAlerts sets where a failed upgrade is sent (D-68).
func (s *Service) WithAlerts(a UpgradeAlerter) *Service {
	s.alerts = a
	return s
}

func (s *Service) alertUpgrade(detail string) {
	if s.alerts != nil {
		s.alerts.PanelUpgradeFailed(detail)
	}
}
