package issue

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.uber.org/zap"
)

// Cycles, due dates and estimates (D-90): a workspace's cycles, made from
// its schedule by the reconcile loop; each issue's cycle, start and due
// dates and estimate; the Timeline of a project's issues.

var (
	ErrBadCycle       = errors.New("a cycle is one of the workspace's that hasn't ended, and the workspace uses cycles")
	ErrContainerCycle = errors.New("an epic or release spans cycles: put its sub-issues in one instead")
	ErrBadDates       = errors.New("dates are YYYY-MM-DD, and an issue starts on or before the day it is due")
	ErrBadEstimate    = errors.New("an estimate is a value of the workspace's scale; an epic's or release's is its sub-issues'")
	ErrEstimatesOff   = errors.New("the workspace doesn't use estimates")
	ErrBadSchedule    = errors.New("cycles are 1 to 4 weeks long and start on a weekday from 0 (Sunday) to 6 (Saturday)")
	ErrBadTimeZone    = errors.New("not a time zone this server knows: an IANA name, as Europe/Athens")
	ErrBadScale       = errors.New("estimates are off, fibonacci, linear or tshirt")
	ErrBadCycleName   = errors.New("a cycle's name has 60 characters at most and no control characters")
)

// cyclesAhead is how many cycles Toft keeps ready after the current one.
const cyclesAhead = 2

// estimateScales are the points each scale offers (T-shirt: XS to XL).
var estimateScales = map[string][]int{
	"fibonacci": {1, 2, 3, 5, 8, 13},
	"linear":    {1, 2, 3, 4, 5},
	"tshirt":    {1, 2, 3, 5, 8},
}

const dateLayout = "2006-01-02"

// ─── types ───────────────────────────────────────────────────────────────────

// CycleSchedule is a workspace's cycles: their length, the weekday they
// start on (0 Sunday to 6 Saturday) and the time zone their days are in.
type CycleSchedule struct {
	Enabled  bool   `json:"enabled"`
	Weeks    int    `json:"weeks"`
	StartDay int    `json:"start_day"`
	TimeZone string `json:"time_zone"`
}

// OrgSettings are a workspace's issue settings as stored; Weeks 0 is off.
type OrgSettings struct {
	Digits, Weeks, StartDay int
	Zone, Estimates         string
}

// OrgSchedule is a workspace that uses cycles, for the reconcile loop.
type OrgSchedule struct {
	OrgID, Zone     string
	Weeks, StartDay int
}

// StoredCycle is a cycle as stored; Final holds its counts once closed.
type StoredCycle struct {
	ID               string
	Number           int
	Name             string
	StartsOn, EndsOn time.Time
	Closed           bool
	Final            *CycleCounts
}

// CycleCounts are a cycle's issues: scope leaves cancelled ones out;
// started is in progress, in review or shipped; carried is what an ended
// cycle moved to the next.
type CycleCounts struct {
	Scope         int `json:"scope"`
	Started       int `json:"started"`
	Shipped       int `json:"shipped"`
	Cancelled     int `json:"cancelled"`
	Carried       int `json:"carried"`
	Points        int `json:"points"`
	StartedPoints int `json:"started_points"`
	ShippedPoints int `json:"shipped_points"`
}

// Cycle is one of a workspace's cycles: state is upcoming, current or
// ended; days_left counts the days after today it runs.
type Cycle struct {
	Number   int         `json:"number"`
	Name     string      `json:"name"`
	StartsOn string      `json:"starts_on"`
	EndsOn   string      `json:"ends_on"`
	State    string      `json:"state"`
	DaysLeft int         `json:"days_left"`
	Counts   CycleCounts `json:"counts"`
}

// CycleRef is the cycle an issue is in.
type CycleRef struct {
	Number   int    `json:"number"`
	Name     string `json:"name"`
	StartsOn string `json:"starts_on"`
	EndsOn   string `json:"ends_on"`
	State    string `json:"state"`
}

// CycleDay is a cycle's counts at the end of one of its days.
type CycleDay struct {
	Day           string `json:"day"`
	Scope         int    `json:"scope"`
	Started       int    `json:"started"`
	Shipped       int    `json:"shipped"`
	ScopePoints   int    `json:"scope_points"`
	StartedPoints int    `json:"started_points"`
	ShippedPoints int    `json:"shipped_points"`
}

// CycleShare is one project's or assignee's issues in a cycle ("" id:
// unassigned).
type CycleShare struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Total   int    `json:"total"`
	Shipped int    `json:"shipped"`
}

// CycleDetail is one cycle with its chart and shares.
type CycleDetail struct {
	Cycle
	Days      []CycleDay   `json:"days"`
	Projects  []CycleShare `json:"projects"`
	Assignees []CycleShare `json:"assignees"`
	// Added is how many came in after it started; CarriedIn how many the
	// cycle before it carried over.
	Added     int  `json:"added"`
	CarriedIn int  `json:"carried_in"`
	Previous  *int `json:"previous"`
	Next      *int `json:"next"`
}

// CycleIssueRow is a cycle's issue as stored.
type CycleIssueRow struct {
	ID, ProjectID, Project, Key string
	Number                      int
	Title, Status, Priority     string
	TypeID                      string
	AssigneeID, AssigneeName    string
	Estimate                    *int
	DueOn                       *time.Time
	Labels                      []string
	CarriedFrom, CarriedTo      int
}

// CycleIssue is a cycle's issue: carried_from is the cycle that carried it
// in, carried_to the one this cycle carried it to when it ended.
type CycleIssue struct {
	Key         string   `json:"key"`
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority"`
	ProjectID   string   `json:"project_id"`
	Project     string   `json:"project"`
	Type        TypeRef  `json:"type"`
	Assignee    *Person  `json:"assignee"`
	Labels      []string `json:"labels"`
	Estimate    *int     `json:"estimate"`
	DueOn       *string  `json:"due_on"`
	CarriedFrom *int     `json:"carried_from"`
	CarriedTo   *int     `json:"carried_to"`
}

// ProjectCycles are the workspace's cycles as a project sees them: the
// upcoming ones, the current one and the last six ended, newest first,
// with the project's own counts.
type ProjectCycles struct {
	Enabled   bool    `json:"enabled"`
	Estimates string  `json:"estimates"`
	Cycles    []Cycle `json:"cycles"`
}

// ─── dates ───────────────────────────────────────────────────────────────────

// civil is the date t falls on where it is, as midnight UTC.
func civil(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func addDays(d time.Time, n int) time.Time { return d.AddDate(0, 0, n) }

// todayIn is today's date in zone (UTC when the zone is unknown).
func todayIn(zone string, now time.Time) time.Time {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	return civil(now.In(loc))
}

func fmtDate(t time.Time) string { return t.Format(dateLayout) }

func optDate(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := fmtDate(*t)
	return &s
}

// parseDate reads YYYY-MM-DD; "" is none.
func parseDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil || t.Year() < 2000 || t.Year() > 2999 {
		return nil, ErrBadDates
	}
	return &t, nil
}

func stateOf(c StoredCycle, today time.Time) string {
	switch {
	case c.EndsOn.Before(today):
		return "ended"
	case c.StartsOn.After(today):
		return "upcoming"
	}
	return "current"
}

// ─── the schedule ────────────────────────────────────────────────────────────

// Schedule is what planCycles needs of a workspace's schedule.
type Schedule struct {
	Weeks    int
	StartDay time.Weekday
}

// cycleEnd is the last day of a cycle starting on start: weeks long, and
// up to six days longer so the next one starts on the schedule's weekday
// (after the start day changed).
func cycleEnd(start time.Time, sc Schedule) time.Time {
	end := addDays(start, sc.Weeks*7-1)
	for addDays(end, 1).Weekday() != sc.StartDay {
		end = addDays(end, 1)
	}
	return end
}

// lastStartDay is the latest day on or before today that is wd.
func lastStartDay(today time.Time, wd time.Weekday) time.Time {
	return addDays(today, -((int(today.Weekday()) - int(wd) + 7) % 7))
}

// cyclePlan is what a schedule asks of a workspace's cycles today.
type cyclePlan struct {
	Move  []StoredCycle // upcoming cycles with their new dates
	Add   []StoredCycle // new cycles, by number
	Close []StoredCycle // ended cycles not closed yet, in order
	// Current is the current cycle's number.
	Current int
}

// planCycles keeps a current cycle and cyclesAhead after it, redraws the
// upcoming ones from the cycle before each, and closes the ended ones. A
// workspace that starts (or starts again after a pause) gets its current
// cycle from this week's start day, never overlapping the last one. cs is
// in number order.
func planCycles(sc Schedule, cs []StoredCycle, today time.Time) cyclePlan {
	var p cyclePlan
	all := slices.Clone(cs)
	for i := range all {
		c := &all[i]
		if i == 0 || c.Closed || !c.StartsOn.After(today) {
			continue
		}
		start := addDays(all[i-1].EndsOn, 1)
		end := cycleEnd(start, sc)
		if !start.Equal(c.StartsOn) || !end.Equal(c.EndsOn) {
			c.StartsOn, c.EndsOn = start, end
			p.Move = append(p.Move, *c)
		}
	}
	cur := -1
	for i, c := range all {
		if stateOf(c, today) == "current" {
			cur = i
		}
	}
	if cur < 0 {
		start, number := lastStartDay(today, sc.StartDay), 1
		if n := len(all); n > 0 {
			last := all[n-1]
			number = last.Number + 1
			if !start.After(last.EndsOn) {
				start = addDays(last.EndsOn, 1)
			}
		}
		c := StoredCycle{Number: number, StartsOn: start, EndsOn: cycleEnd(start, sc)}
		all = append(all, c)
		p.Add = append(p.Add, c)
		cur = len(all) - 1
	}
	for len(all)-1-cur < cyclesAhead {
		last := all[len(all)-1]
		start := addDays(last.EndsOn, 1)
		c := StoredCycle{Number: last.Number + 1, StartsOn: start, EndsOn: cycleEnd(start, sc)}
		all = append(all, c)
		p.Add = append(p.Add, c)
	}
	for _, c := range all {
		if c.ID != "" && !c.Closed && c.EndsOn.Before(today) {
			p.Close = append(p.Close, c)
		}
	}
	p.Current = all[cur].Number
	return p
}

// tickCycles brings every workspace's cycles up to its schedule: run by the
// reconcile loop (Run) and after a schedule changes.
func (s *Service) tickCycles(ctx context.Context) {
	orgs, err := s.repo.CycleSchedules(ctx)
	if err != nil {
		s.log.Warn("list cycle schedules", zap.Error(err))
		return
	}
	for _, o := range orgs {
		if err := s.tickOrg(ctx, o); err != nil && ctx.Err() == nil {
			s.log.Warn("bring cycles up to date", zap.Error(err), zap.String("org_id", o.OrgID))
		}
	}
}

func (s *Service) tickOrg(ctx context.Context, o OrgSchedule) error {
	today := todayIn(o.Zone, s.now())
	cs, err := s.repo.Cycles(ctx, o.OrgID)
	if err != nil {
		return err
	}
	p := planCycles(Schedule{Weeks: o.Weeks, StartDay: time.Weekday(o.StartDay)}, cs, today)
	for _, c := range p.Move {
		if err := s.repo.MoveCycle(ctx, c, today); err != nil {
			return err
		}
	}
	for _, c := range p.Add {
		if err := s.repo.AddCycle(ctx, o.OrgID, c); err != nil {
			return err
		}
	}
	for _, c := range p.Close {
		if err := s.repo.CloseCycle(ctx, o.OrgID, c); err != nil {
			return err
		}
	}
	cur, err := s.repo.CycleByNumber(ctx, o.OrgID, p.Current)
	if err != nil {
		return err
	}
	return s.repo.SnapshotCycle(ctx, cur.ID, today)
}

func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// ─── settings ────────────────────────────────────────────────────────────────

// WorkspaceSettings are the workspace's defaults.
func (s *Service) WorkspaceSettings(ctx context.Context, orgID string) (*WorkspaceSettings, error) {
	o, err := s.repo.OrgSettings(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return settingsOf(o), nil
}

func settingsOf(o OrgSettings) *WorkspaceSettings {
	sc := CycleSchedule{Enabled: o.Weeks > 0, Weeks: o.Weeks, StartDay: o.StartDay, TimeZone: o.Zone}
	if sc.Weeks == 0 {
		sc.Weeks = 2
	}
	return &WorkspaceSettings{Digits: o.Digits, Cycles: sc, Estimates: o.Estimates}
}

// SaveWorkspaceSettings saves what in sends; a new schedule applies at once.
func (s *Service) SaveWorkspaceSettings(ctx context.Context, orgID string, in WorkspaceSettingsInput) (*WorkspaceSettings, error) {
	if in.Digits != nil {
		if *in.Digits < 0 || *in.Digits > 6 {
			return nil, ErrBadDigits
		}
	}
	if in.Estimates != nil && *in.Estimates != "off" && estimateScales[*in.Estimates] == nil {
		return nil, ErrBadScale
	}
	if c := in.Cycles; c != nil {
		if c.Weeks < 1 || c.Weeks > 4 || c.StartDay < 0 || c.StartDay > 6 {
			return nil, ErrBadSchedule
		}
		if _, err := time.LoadLocation(c.TimeZone); err != nil || c.TimeZone == "" || c.TimeZone == "Local" || len(c.TimeZone) > 64 {
			return nil, ErrBadTimeZone
		}
	}
	if in.Digits != nil {
		if err := s.repo.SetOrgDigits(ctx, orgID, *in.Digits); err != nil {
			return nil, err
		}
	}
	if in.Estimates != nil {
		if err := s.repo.SetEstimates(ctx, orgID, *in.Estimates); err != nil {
			return nil, err
		}
	}
	if c := in.Cycles; c != nil {
		var weeks *int
		if c.Enabled {
			weeks = &c.Weeks
		}
		if err := s.repo.SetCycleSchedule(ctx, orgID, weeks, c.StartDay, c.TimeZone); err != nil {
			return nil, err
		}
		if c.Enabled {
			if err := s.tickOrg(ctx, OrgSchedule{OrgID: orgID, Zone: c.TimeZone, Weeks: c.Weeks, StartDay: c.StartDay}); err != nil {
				return nil, err
			}
		}
	}
	return s.WorkspaceSettings(ctx, orgID)
}

// ─── reads ───────────────────────────────────────────────────────────────────

func cycleOf(c StoredCycle, today time.Time, live map[string]CycleCounts) Cycle {
	out := Cycle{Number: c.Number, Name: c.Name, StartsOn: fmtDate(c.StartsOn), EndsOn: fmtDate(c.EndsOn), State: stateOf(c, today)}
	if out.State != "ended" {
		out.DaysLeft = int(c.EndsOn.Sub(today).Hours() / 24)
	}
	switch {
	case c.Final != nil:
		out.Counts = *c.Final
	case live != nil:
		out.Counts = live[c.ID]
		if out.State == "ended" {
			out.Counts.Carried = out.Counts.Scope - out.Counts.Shipped
		}
	}
	return out
}

func refOf(number int, name string, starts, ends time.Time, today time.Time) *CycleRef {
	if number == 0 {
		return nil
	}
	return &CycleRef{Number: number, Name: name, StartsOn: fmtDate(starts), EndsOn: fmtDate(ends),
		State: stateOf(StoredCycle{StartsOn: starts, EndsOn: ends}, today)}
}

// Cycles pages the workspace's cycles, newest first; state picks upcoming,
// current or ended ones ("" all); the cursor is the last one's number.
func (s *Service) Cycles(ctx context.Context, orgID, state, cursor string, limit int) ([]Cycle, string, error) {
	if state != "" && !slices.Contains([]string{"upcoming", "current", "ended"}, state) {
		return nil, "", ErrBadQuery
	}
	before := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 {
			return nil, "", ErrBadCursor
		}
		before = n
	}
	o, err := s.repo.OrgSettings(ctx, orgID)
	if err != nil {
		return nil, "", err
	}
	cs, err := s.repo.Cycles(ctx, orgID)
	if err != nil {
		return nil, "", err
	}
	live, err := s.repo.CycleCounts(ctx, orgID, "")
	if err != nil {
		return nil, "", err
	}
	today := todayIn(o.Zone, s.now())
	limit = min(max(limit, 1), 100)
	out := make([]Cycle, 0, min(limit, len(cs)))
	next := ""
	for i := len(cs) - 1; i >= 0; i-- {
		c := cs[i]
		if (before > 0 && c.Number >= before) || (state != "" && stateOf(c, today) != state) {
			continue
		}
		if len(out) == limit {
			next = strconv.Itoa(out[len(out)-1].Number)
			break
		}
		out = append(out, cycleOf(c, today, live))
	}
	return out, next, nil
}

// Cycle is one cycle with its days, its shares and its neighbours.
func (s *Service) Cycle(ctx context.Context, orgID string, number int) (*CycleDetail, error) {
	o, err := s.repo.OrgSettings(ctx, orgID)
	if err != nil {
		return nil, err
	}
	cs, err := s.repo.Cycles(ctx, orgID)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(cs, func(c StoredCycle) bool { return c.Number == number })
	if i < 0 {
		return nil, ErrNotFound
	}
	c := cs[i]
	live, err := s.repo.CycleCounts(ctx, orgID, "")
	if err != nil {
		return nil, err
	}
	today := todayIn(o.Zone, s.now())
	d := &CycleDetail{Cycle: cycleOf(c, today, live)}
	if d.Days, err = s.repo.CycleDays(ctx, c.ID); err != nil {
		return nil, err
	}
	if d.Projects, d.Assignees, err = s.repo.CycleShares(ctx, c.ID); err != nil {
		return nil, err
	}
	since, err := startOfDay(c.StartsOn, o.Zone)
	if err != nil {
		return nil, err
	}
	if d.Added, d.CarriedIn, err = s.repo.CycleMoves(ctx, c, since); err != nil {
		return nil, err
	}
	if i > 0 {
		d.Previous = &cs[i-1].Number
	}
	if i < len(cs)-1 {
		d.Next = &cs[i+1].Number
	}
	return d, nil
}

// startOfDay is the moment day begins in zone.
func startOfDay(day time.Time, zone string) (time.Time, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	y, m, dd := day.Date()
	return time.Date(y, m, dd, 0, 0, 0, 0, loc), nil
}

// RenameCycle names a cycle ("" goes back to its number).
func (s *Service) RenameCycle(ctx context.Context, orgID string, number int, name string) (*CycleDetail, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > 60 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return nil, ErrBadCycleName
	}
	ok, err := s.repo.RenameCycle(ctx, orgID, number, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return s.Cycle(ctx, orgID, number)
}

// CycleIssues pages a cycle's issues across the workspace (one project's
// when projectID isn't ""), with those it carried over when it ended.
func (s *Service) CycleIssues(ctx context.Context, orgID string, number int, projectID, cursor string, limit int) ([]CycleIssue, string, error) {
	c, err := s.repo.CycleByNumber(ctx, orgID, number)
	if err != nil {
		return nil, "", err
	}
	types, err := s.types(ctx, orgID)
	if err != nil {
		return nil, "", err
	}
	limit = min(max(limit, 1), 500)
	rows, err := s.repo.CycleIssues(ctx, c.ID, projectID, cursor, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[len(rows)-1].ID
	}
	out := make([]CycleIssue, 0, len(rows))
	for _, r := range rows {
		it := CycleIssue{Key: r.Key, Number: r.Number, Title: r.Title, Status: r.Status, Priority: r.Priority,
			ProjectID: r.ProjectID, Project: r.Project, Type: typeOf(types, r.TypeID), Assignee: person(r.AssigneeID, r.AssigneeName),
			Labels: r.Labels, Estimate: r.Estimate, DueOn: optDate(r.DueOn)}
		if r.CarriedFrom > 0 {
			it.CarriedFrom = &r.CarriedFrom
		}
		if r.CarriedTo > 0 {
			it.CarriedTo = &r.CarriedTo
		}
		out = append(out, it)
	}
	return out, next, nil
}

// ProjectCycles are the workspace's cycles with one project's counts.
func (s *Service) ProjectCycles(ctx context.Context, projectID string) (*ProjectCycles, error) {
	p, err := s.repo.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	cs, err := s.repo.Cycles(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	live, err := s.repo.CycleCounts(ctx, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	today := todayIn(p.Zone, s.now())
	out := &ProjectCycles{Enabled: p.Cycles, Estimates: p.Estimates, Cycles: []Cycle{}}
	ended := 0
	for i := len(cs) - 1; i >= 0; i-- {
		c := cs[i]
		c.Final = nil // the project's share, not the workspace's
		cy := cycleOf(c, today, live)
		if cy.State == "ended" {
			if ended++; ended > 6 {
				break
			}
		}
		out.Cycles = append(out.Cycles, cy)
	}
	return out, nil
}

// ─── an issue's fields ───────────────────────────────────────────────────────

// cycleFor is the id of the cycle number names for an issue of type t (0:
// none): one of the workspace's that hasn't ended, never for a container.
func (s *Service) cycleFor(ctx context.Context, p *Project, t TypeRef, number int) (string, error) {
	if number == 0 {
		return "", nil
	}
	if t.Kind == "container" {
		return "", ErrContainerCycle
	}
	if !p.Cycles || number < 0 {
		return "", ErrBadCycle
	}
	c, err := s.repo.CycleByNumber(ctx, p.OrgID, number)
	if errors.Is(err, ErrNotFound) {
		return "", ErrBadCycle
	}
	if err != nil {
		return "", err
	}
	if stateOf(*c, todayIn(p.Zone, s.now())) == "ended" {
		return "", ErrBadCycle
	}
	return c.ID, nil
}

// checkEstimate reads an estimate (0: none) for an issue of type t.
func checkEstimate(p *Project, t TypeRef, n int) (*int, error) {
	if n == 0 {
		return nil, nil
	}
	if p.Estimates == "off" || p.Estimates == "" {
		return nil, ErrEstimatesOff
	}
	if t.Kind == "container" || !slices.Contains(estimateScales[p.Estimates], n) {
		return nil, ErrBadEstimate
	}
	return &n, nil
}

func itoa0(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func intOr0(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func dateOr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return fmtDate(*t)
}

func inOrder(start, due *time.Time) error {
	if start != nil && due != nil && start.After(*due) {
		return ErrBadDates
	}
	return nil
}

// validCycleFilter is a cycle filter: current, next, none or a number.
func validCycleFilter(v string) bool {
	if v == "current" || v == "next" || v == "none" {
		return true
	}
	n, err := strconv.Atoi(v)
	return err == nil && n > 0 && n < 1_000_000
}

// cycleByKey is the cycle current, next or a number names; nil when there
// is none.
func (s *Service) cycleByKey(ctx context.Context, p *Project, key string) (*StoredCycle, error) {
	cs, err := s.repo.Cycles(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	today := todayIn(p.Zone, s.now())
	for i, c := range cs {
		switch {
		case key == "current" && stateOf(c, today) == "current",
			key == "next" && i > 0 && stateOf(cs[i-1], today) == "current",
			key == strconv.Itoa(c.Number):
			return &c, nil
		}
	}
	return nil, nil
}

// openCycle is cycleID when that cycle hasn't ended, else "".
func (s *Service) openCycle(ctx context.Context, p *Project, cycleID string) string {
	cs, err := s.repo.Cycles(ctx, p.OrgID)
	if err != nil || !p.Cycles {
		return ""
	}
	today := todayIn(p.Zone, s.now())
	for _, c := range cs {
		if c.ID == cycleID && stateOf(c, today) != "ended" {
			return c.ID
		}
	}
	return ""
}
