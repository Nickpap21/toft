package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/advisory"
	"github.com/Nickpap21/toft/backend/internal/hostupdate"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// Server updates, phase 1, read-only (D-130, D-133): what apt would install,
// whether a reboot waits, the Docker engine and its advisories, and whether
// unattended-upgrades runs. Read every 6 hours over SSH or the agent, as the
// capability probe; the panel's own server through its host helper. Nothing
// is installed and apt's lists aren't refreshed: apt's daily job does that.

// HostUpdates is what the last updates read found on a host (the API's
// HostUpdates).
type HostUpdates struct {
	CheckedAt time.Time `json:"checked_at"`
	// ReadAt is the last read that worked, which the rest comes from; nil
	// before one.
	ReadAt         *time.Time `json:"read_at"`
	Error          string     `json:"error"`
	ListsUpdatedAt *time.Time `json:"lists_updated_at"`
	Security       int        `json:"security"`
	Other          int        `json:"other"`
	// Held is how many of them are Docker's, which an install holds back
	// (D-134).
	Held     int              `json:"held"`
	Packages []PendingPackage `json:"packages"`
	// Removals are the packages apt would remove to install them.
	Removals   []string     `json:"removals"`
	Truncated  bool         `json:"truncated"`
	Reboot     RebootState  `json:"reboot"`
	Docker     DockerEngine `json:"docker"`
	Unattended Unattended   `json:"unattended"`
}

// PendingPackage is one update apt would install.
type PendingPackage struct {
	Name      string `json:"name"`
	Current   string `json:"current"`
	Candidate string `json:"candidate"`
	Security  bool   `json:"security"`
}

// RebootState: /var/run/reboot-required and the packages that asked.
type RebootState struct {
	Required bool       `json:"required"`
	Since    *time.Time `json:"since"`
	Packages []string   `json:"packages"`
}

// DockerEngine is the running engine, the package that installed it
// (docker-ce, docker.io or none), the one apt would install instead, and
// the advisories that affect it (set when read, from the advisory book).
type DockerEngine struct {
	Version    string              `json:"version"`
	Package    string              `json:"package"`
	Candidate  string              `json:"candidate"`
	Advisories []advisory.Advisory `json:"advisories"`
}

// Unattended is unattended-upgrades: State active, inactive or missing;
// LastResult ok, failed or never. Both are unknown until a read works.
type Unattended struct {
	State         string     `json:"state"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastResult    string     `json:"last_result"`
	LastError     string     `json:"last_error"`
}

// Read limits: what the API lists and stores of a long list.
const (
	maxPendingPackages = 500
	maxRebootPackages  = 50
	updatesReadEvery   = 6 * time.Hour
	// unattendedLate: unattended-upgrades runs daily, so two days without a
	// run means it doesn't.
	unattendedLate = 48 * time.Hour
)

// updatesScript prints apt's pending updates as its simulation's Inst lines
// and its exit code (a broken dpkg or unreadable lists list nothing, which
// must not read as up to date), then one key:value line a fact, with times
// as the host's epoch seconds and its now, so the panel corrects for a host
// clock that is off. The simulation takes no lock (Debug::NoLocking), so a
// run of unattended-upgrades can't make it fail. The lists came down at
// apt's daily update-stamp or when an update by hand renamed one into
// /var/lib/apt/lists, whichever is later. unattended-upgrades writes its
// own stamp at the end of every run; apt's daily job writes upgrade-stamp
// only when it exits 0. dockerpkg says which package installed Docker.
func updatesScript() string {
	return `if ! command -v apt-get >/dev/null 2>&1; then echo noapt; exit 0; fi
echo apt:1
out=$(LC_ALL=C apt-get -s -o Debug::NoLocking=1 dist-upgrade 2>&1); rc=$?
printf 'aptrc:%s\n' "$rc"
if [ "$rc" -ne 0 ]; then printf '%s\n' "$out" | grep '^E: ' | head -n 1 | cut -c1-300 | sed 's/^/apterr:/'; fi
printf '%s\n' "$out" | grep '^Inst \|^Remv ' | head -n 5000
st() { stat -c %Y "$1" 2>/dev/null; }
printf 'lists:%s\n' "$(st /var/lib/apt/periodic/update-stamp)"
printf 'listsdir:%s\n' "$(st /var/lib/apt/lists)"
printf 'uurun:%s\n' "$(st /var/lib/apt/periodic/unattended-upgrades-stamp)"
printf 'uuok:%s\n' "$(st /var/lib/apt/periodic/upgrade-stamp)"
printf 'uuunit:%s %s\n' "$(systemctl show -p LoadState --value unattended-upgrades.service 2>/dev/null)" "$(systemctl show -p ActiveState --value unattended-upgrades.service 2>/dev/null)"
printf 'uuerr:%s\n' "$(awk '/Starting unattended upgrades script/{e=""} / ERROR /{e=$0} END{print e}' /var/log/unattended-upgrades/unattended-upgrades.log 2>/dev/null | cut -c1-300)"
if [ -f /var/run/reboot-required ]; then
  printf 'reboot:%s\n' "$(st /var/run/reboot-required)"
  sort -u /var/run/reboot-required.pkgs 2>/dev/null | head -n ` + strconv.Itoa(maxRebootPackages) + ` | sed 's/^/rebootpkg:/'
fi
printf 'docker:%s\n' "$(docker version --format '{{.Server.Version}}' 2>/dev/null)"
printf 'dockerpkg:%s\n' "$(dpkg-query -W -f='${Status} ${Package}\n' docker-ce docker.io 2>/dev/null | awk '$3 == "installed" {print $4; exit}')"
printf 'now:%s\n' "$(date +%s)"
`
}

// errNoApt: the host doesn't use apt, so there's nothing to read.
var errNoApt = errors.New("this server doesn't use apt, so Toft can't read its updates")

// inst is a simulation line: Inst name [current] (candidate origin, origin [arch]).
var inst = regexp.MustCompile(`^Inst (\S+)(?: \[([^\]]*)\])? \((\S+) ([^\[]*)`)

// remv is a simulation's removal: Remv name [version].
var remv = regexp.MustCompile(`^Remv (\S+)`)

// parseUpdates reads updatesScript's output at now, the panel's clock.
func parseUpdates(out string, now time.Time) (HostUpdates, error) {
	u := HostUpdates{CheckedAt: now, ReadAt: &now, Packages: []PendingPackage{}, Removals: []string{}, Reboot: RebootState{Packages: []string{}},
		Docker: DockerEngine{Advisories: []advisory.Advisory{}}}
	stamps := map[string]int64{}
	seen := false
	aptrc, apterr := "", ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "noapt" {
			return u, errNoApt
		}
		if m := inst.FindStringSubmatch(line); m != nil {
			p := PendingPackage{Name: m[1], Current: m[2], Candidate: m[3], Security: strings.Contains(m[4], "-security")}
			if p.Security {
				u.Security++
			} else {
				u.Other++
			}
			u.Packages = append(u.Packages, p)
			if p.Name == "docker-ce" || p.Name == "docker.io" {
				u.Docker.Candidate = upstreamVersion(p.Candidate)
			}
			if hostupdate.IsDockerPackage(p.Name) {
				u.Held++
			}
			continue
		}
		if m := remv.FindStringSubmatch(line); m != nil {
			if len(u.Removals) < maxPendingPackages {
				u.Removals = append(u.Removals, m[1])
			}
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "apt":
			seen = true
		case "aptrc":
			aptrc = val
		case "apterr":
			apterr = strings.TrimSpace(strings.TrimPrefix(val, "E:"))
		case "dockerpkg":
			u.Docker.Package = val
		case "lists", "listsdir", "uurun", "uuok", "reboot", "now":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil && n > 0 {
				stamps[key] = n
			}
			if key == "reboot" {
				u.Reboot.Required = true
			}
		case "rebootpkg":
			if val != "" {
				u.Reboot.Packages = append(u.Reboot.Packages, val)
			}
		case "uuunit":
			load, active, _ := strings.Cut(val, " ")
			switch {
			case load == "" || load == "not-found":
				u.Unattended.State = "missing"
			case active == "active":
				u.Unattended.State = "active"
			default:
				u.Unattended.State = "inactive"
			}
		case "uuerr":
			u.Unattended.LastError = val
		case "docker":
			u.Docker.Version = val
		}
	}
	if !seen {
		return u, errors.New("the read printed nothing it knows")
	}
	if aptrc != "" && aptrc != "0" {
		if apterr == "" {
			apterr = "it exited " + aptrc
		}
		return u, errors.New("apt couldn't work out the pending updates: " + apterr)
	}
	if d, ok := stamps["listsdir"]; ok && d > stamps["lists"] {
		stamps["lists"] = d
	}
	// The host's times, moved onto the panel's clock.
	skew := time.Duration(0)
	if n, ok := stamps["now"]; ok {
		skew = now.Sub(time.Unix(n, 0)).Round(time.Second)
	}
	at := func(key string) *time.Time {
		n, ok := stamps[key]
		if !ok {
			return nil
		}
		t := time.Unix(n, 0).Add(skew).UTC()
		return &t
	}
	u.ListsUpdatedAt, u.Reboot.Since = at("lists"), at("reboot")
	u.Unattended.LastRunAt, u.Unattended.LastSuccessAt = at("uurun"), at("uuok")
	switch run, ok := u.Unattended.LastRunAt, u.Unattended.LastSuccessAt; {
	case run == nil:
		u.Unattended.LastResult = "never"
	case ok == nil || run.Sub(*ok) > time.Minute:
		u.Unattended.LastResult = "failed"
	default:
		u.Unattended.LastResult, u.Unattended.LastError = "ok", ""
	}
	if u.Unattended.State == "" {
		u.Unattended.State = "missing"
	}
	sort.SliceStable(u.Packages, func(i, j int) bool {
		a, b := u.Packages[i], u.Packages[j]
		if a.Security != b.Security {
			return a.Security
		}
		return a.Name < b.Name
	})
	if len(u.Packages) > maxPendingPackages {
		u.Packages, u.Truncated = u.Packages[:maxPendingPackages], true
	}
	return u, nil
}

// upstreamVersion is a Debian version's upstream part: 5:29.8.2-1~ubuntu.24.04~noble is 29.8.2.
func upstreamVersion(v string) string {
	if _, rest, ok := strings.Cut(v, ":"); ok {
		v = rest
	}
	v, _, _ = strings.Cut(v, "-")
	return v
}

// ReadUpdatesOn reads a host's updates through sc, as ReadUpdates does over
// SSH: the panel's own server runs it through its host helper.
func ReadUpdatesOn(sc Shell, now time.Time) (HostUpdates, error) {
	encoded := base64.StdEncoding.EncodeToString([]byte(updatesScript()))
	out, err := sc.Exec("echo " + encoded + " | base64 -d | sh")
	if err != nil {
		return HostUpdates{}, err
	}
	return parseUpdates(out, now)
}

// UpdateHealth is what the alerts make of a host's last read (D-133): a
// sentence for each of security_updates, reboot_required and
// docker_advisory, "" when it is fine. Known is false before a read that
// worked; the alerts then stay as they are.
type UpdateHealth struct {
	ServerID                 string // "" for the panel's own server
	Known                    bool
	Security, Reboot, Docker string
}

// Health judges u at now. Security updates wait when unattended-upgrades
// isn't running, failed, hasn't run for two days, or ran since apt's lists
// last came down and left them: not in the hours between a security
// release and the next nightly run.
func (u *HostUpdates) Health(now time.Time) UpdateHealth {
	h := UpdateHealth{}
	if u == nil || u.Unattended.State == "" || u.Unattended.State == "unknown" {
		return h
	}
	h.Known = true
	uu := u.Unattended
	if u.Security > 0 {
		why := ""
		switch {
		case uu.State != "active":
			why = "unattended-upgrades isn't running"
		case uu.LastResult == "never":
			why = "unattended-upgrades has never run"
		case uu.LastResult == "failed":
			why = "unattended-upgrades' last run failed"
		case uu.LastRunAt.Before(now.Add(-unattendedLate)):
			why = "unattended-upgrades hasn't run for 2 days"
		case u.ListsUpdatedAt != nil && uu.LastRunAt.After(*u.ListsUpdatedAt):
			why = "unattended-upgrades ran since the package lists came down and didn't install them"
		}
		if why != "" {
			h.Security = fmt.Sprintf("%d security %s waiting: %s", u.Security, plural(u.Security, "update", "updates"), why)
		}
	}
	if u.Reboot.Required {
		h.Reboot = "a reboot is waiting"
		if u.Reboot.Since != nil {
			h.Reboot += " since " + u.Reboot.Since.Format("2 Jan 15:04 UTC")
		}
		if n := len(u.Reboot.Packages); n > 0 {
			h.Reboot += ", for " + strings.Join(u.Reboot.Packages[:min(n, 3)], ", ")
			if n > 3 {
				h.Reboot += fmt.Sprintf(" and %d more", n-3)
			}
		}
	}
	if n := len(u.Docker.Advisories); n > 0 {
		worst := u.Docker.Advisories[0]
		h.Docker = fmt.Sprintf("Docker %s is affected by %d published security %s, the worst %s: %s (%s, fixed in %s)",
			u.Docker.Version, n, plural(n, "advisory", "advisories"), worst.Severity, worst.Summary, worst.ID, worst.FixedIn)
	}
	return h
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// UpdateStore keeps each server's last read. Satisfied by the Postgres
// repository.
type UpdateStore interface {
	SaveUpdates(ctx context.Context, serverID string, u HostUpdates) error
	SetUpdatesError(ctx context.Context, serverID, msg string) error
	GetUpdates(ctx context.Context, serverID string) (*HostUpdates, error)
	ListUpdates(ctx context.Context) (map[string]*HostUpdates, error)
	StaleUpdateServers(ctx context.Context) ([]string, error)
}

// WithUpdates stores what the updates read finds, judged against book.
func (s *Service) WithUpdates(st UpdateStore, book *advisory.Book) {
	s.updates, s.advisories = st, book
}

// ReadUpdates reads the server's updates now and stores them; a read that
// fails keeps what the last one read, with its error.
func (s *Service) ReadUpdates(ctx context.Context, serverID string) (*HostUpdates, error) {
	if s.updates == nil {
		return nil, errors.New("no updates store")
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u, err := ReadUpdatesOn(shellFor(rctx, s.SSH(srv)), time.Now().UTC())
	if err != nil {
		if serr := s.updates.SetUpdatesError(ctx, serverID, UpdatesError(err)); serr != nil {
			return nil, serr
		}
		return s.Updates(ctx, serverID)
	}
	if err := s.updates.SaveUpdates(ctx, serverID, u); err != nil {
		return nil, err
	}
	return s.Updates(ctx, serverID)
}

// UpdatesError is the stored sentence for a read that failed.
func UpdatesError(err error) string {
	msg := "The read couldn't run: " + err.Error()
	if errors.Is(err, errNoApt) {
		msg = "This server doesn't use apt, so Toft can't read its updates."
	}
	if len(msg) > 500 {
		msg = strings.ToValidUTF8(msg[:500], "")
	}
	return msg
}

// Updates is the server's last read, with the advisories that affect its
// Docker now; nil before the first.
func (s *Service) Updates(ctx context.Context, serverID string) (*HostUpdates, error) {
	if s.updates == nil {
		return nil, nil
	}
	u, err := s.updates.GetUpdates(ctx, serverID)
	if err != nil || u == nil {
		return u, err
	}
	s.SetAdvisories(u)
	return u, nil
}

// SetAdvisories fills in the advisories that affect u's Docker, from the
// book as it is now, so one fetched today counts without a new read.
func (s *Service) SetAdvisories(u *HostUpdates) { u.SetAdvisories(s.advisories) }

// SetAdvisories fills in book's advisories that affect u's Docker. A
// distribution's docker.io keeps Docker's version while its maintainers
// backport fixes, so Docker's advisories say nothing about it.
func (u *HostUpdates) SetAdvisories(book *advisory.Book) {
	u.Docker.Advisories = []advisory.Advisory{}
	if book != nil && u.Docker.Package != "docker.io" {
		u.Docker.Advisories = book.Affecting(u.Docker.Version)
	}
}

// RunUpdateReads reads, at start and every hour, the servers whose last
// read is 6 hours old or never ran, and follows the runs of updates nobody
// watches (D-134), until ctx ends.
func (s *Service) RunUpdateReads(ctx context.Context) {
	if s.updates == nil {
		return
	}
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		ids, err := s.updates.StaleUpdateServers(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.Warn("list servers to read updates of", zap.Error(err))
		}
		for _, id := range ids {
			if _, err := s.ReadUpdates(ctx, id); err != nil && ctx.Err() == nil {
				s.log.Warn("read updates", zap.Error(err), zap.String("server_id", id))
			}
		}
		s.FollowUpdateRuns(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// UpdateProblems is every server's UpdateHealth, for the alerts.
func (s *Service) UpdateProblems(ctx context.Context) ([]UpdateHealth, error) {
	if s.updates == nil {
		return nil, nil
	}
	all, err := s.updates.ListUpdates(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]UpdateHealth, 0, len(all))
	for id, u := range all {
		s.SetAdvisories(u)
		h := u.Health(now)
		h.ServerID = id
		out = append(out, h)
	}
	return out, nil
}

func (r *postgresRepository) SaveUpdates(ctx context.Context, serverID string, u HostUpdates) error {
	u.Error, u.Docker.Advisories = "", nil
	raw, err := json.Marshal(u)
	if err != nil {
		return fmt.Errorf("encode updates: %w", err)
	}
	if err := r.q.UpsertServerUpdates(ctx, sqlcdb.UpsertServerUpdatesParams{ServerID: pgUUID(serverID), Report: raw}); err != nil {
		return fmt.Errorf("UpsertServerUpdates: %w", err)
	}
	return nil
}

func (r *postgresRepository) SetUpdatesError(ctx context.Context, serverID, msg string) error {
	if err := r.q.SetServerUpdatesError(ctx, sqlcdb.SetServerUpdatesErrorParams{ServerID: pgUUID(serverID), Error: msg}); err != nil {
		return fmt.Errorf("SetServerUpdatesError: %w", err)
	}
	return nil
}

// decodeUpdates is a stored row as the API's shape.
func decodeUpdates(report []byte, msg string, checkedAt time.Time) (*HostUpdates, error) {
	u := &HostUpdates{}
	if err := json.Unmarshal(report, u); err != nil {
		return nil, fmt.Errorf("decode updates: %w", err)
	}
	u.Error, u.CheckedAt = msg, checkedAt
	u.Complete()
	return u, nil
}

// Complete fills in what a stored report leaves out: empty lists rather
// than null, and unknown for a host whose only reads failed, so its report
// is empty.
func (u *HostUpdates) Complete() {
	if u.Packages == nil {
		u.Packages = []PendingPackage{}
	}
	if u.Removals == nil {
		u.Removals = []string{}
	}
	if u.Reboot.Packages == nil {
		u.Reboot.Packages = []string{}
	}
	if u.Docker.Advisories == nil {
		u.Docker.Advisories = []advisory.Advisory{}
	}
	if u.Unattended.State == "" {
		u.Unattended.State, u.Unattended.LastResult = "unknown", "unknown"
	}
}

func (r *postgresRepository) GetUpdates(ctx context.Context, serverID string) (*HostUpdates, error) {
	row, err := r.q.GetServerUpdates(ctx, pgUUID(serverID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("GetServerUpdates: %w", err)
	}
	return decodeUpdates(row.Report, row.Error, row.CheckedAt.Time)
}

func (r *postgresRepository) ListUpdates(ctx context.Context) (map[string]*HostUpdates, error) {
	rows, err := r.q.ListServerUpdates(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListServerUpdates: %w", err)
	}
	out := make(map[string]*HostUpdates, len(rows))
	for _, row := range rows {
		u, err := decodeUpdates(row.Report, row.Error, row.CheckedAt.Time)
		if err != nil {
			return nil, err
		}
		out[uuidString(row.ServerID)] = u
	}
	return out, nil
}

func (r *postgresRepository) StaleUpdateServers(ctx context.Context) ([]string, error) {
	rows, err := r.q.ListStaleUpdateServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListStaleUpdateServers: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, id := range rows {
		out = append(out, uuidString(id))
	}
	return out, nil
}
