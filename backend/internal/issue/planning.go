package issue

import (
	"context"
	"slices"
	"time"
)

// The Timeline (D-90): a project's epics and releases with their issues,
// and the dated issues outside them, with the blocks between them and the
// workspace's cycles.

// TimelineRow is an issue as the Timeline reads it.
type TimelineRow struct {
	ID, ParentID             string
	Number                   int
	Title, Status, Priority  string
	TypeID                   string
	StartOn, DueOn           *time.Time
	Estimate                 *int
	AssigneeID, AssigneeName string
	StatusAt                 time.Time
	StartedAt                *time.Time
	Labels                   []string
	Children, Shipped, Done  int
	ChildrenPoints           int
}

// TimelineIssue is a bar, a diamond or a row of the Timeline: started_at
// is when it first moved to In progress or later, shipped_at when it
// shipped; parent is its parent's key when the parent is on the Timeline.
type TimelineIssue struct {
	Key       string     `json:"key"`
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	Priority  string     `json:"priority"`
	Type      TypeRef    `json:"type"`
	Parent    string     `json:"parent,omitempty"`
	StartOn   *string    `json:"start_on"`
	DueOn     *string    `json:"due_on"`
	StartedAt *time.Time `json:"started_at"`
	ShippedAt *time.Time `json:"shipped_at"`
	Assignee  *Person    `json:"assignee"`
	Labels    []string   `json:"labels"`
	Estimate  *int       `json:"estimate"`
	Progress  *Progress  `json:"progress"`
}

// TimelineBlock is a blocks relation between two issues on the Timeline.
type TimelineBlock struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// TimelineView is a project's Timeline from a day: undated counts the open
// issues left off it (not started, no dates, outside any epic or release).
type TimelineView struct {
	Cycles  []Cycle         `json:"cycles"`
	Items   []TimelineIssue `json:"items"`
	Blocks  []TimelineBlock `json:"blocks"`
	Undated int             `json:"undated"`
}

// TimelineFilter narrows the Timeline's issues; a container stays while
// one of its issues matches.
type TimelineFilter struct {
	Assignee string // an account's id, none, or "" for any
	Labels   []string
	TypeIDs  []string
}

func (f TimelineFilter) match(r TimelineRow, t TypeRef) bool {
	switch {
	case f.Assignee == "none" && r.AssigneeID != "",
		f.Assignee != "" && f.Assignee != "none" && r.AssigneeID != f.Assignee,
		len(f.TypeIDs) > 0 && !slices.Contains(f.TypeIDs, t.ID),
		len(f.Labels) > 0 && !slices.ContainsFunc(r.Labels, func(l string) bool { return slices.Contains(f.Labels, l) }):
		return false
	}
	return true
}

// IssueTimeline is a project's Timeline: its issues open or closed since
// from, the workspace's cycles that end on or after from.
func (s *Service) IssueTimeline(ctx context.Context, projectID string, from time.Time, f TimelineFilter) (*TimelineView, error) {
	p, err := s.repo.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	types, err := s.types(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	if from.IsZero() {
		from = addDays(todayIn(p.Zone, s.now()), -28)
	}
	since, err := startOfDay(from, p.Zone)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.TimelineIssues(ctx, projectID, since)
	if err != nil {
		return nil, err
	}
	out := &TimelineView{Cycles: []Cycle{}, Items: []TimelineIssue{}, Blocks: []TimelineBlock{}}
	if p.Cycles {
		cs, err := s.repo.Cycles(ctx, p.OrgID)
		if err != nil {
			return nil, err
		}
		today := todayIn(p.Zone, s.now())
		for _, c := range cs {
			if !c.EndsOn.Before(from) {
				out.Cycles = append(out.Cycles, cycleOf(c, today, nil))
			}
		}
	}

	byID := make(map[string]*TimelineRow, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	// under is the container an issue sits under, at any depth.
	under := func(r *TimelineRow) *TimelineRow {
		for p := byID[r.ParentID]; p != nil; p = byID[p.ParentID] {
			if typeOf(types, p.TypeID).Kind == "container" {
				return p
			}
		}
		return nil
	}
	open := func(r *TimelineRow) bool { return r.Status != StatusShipped && r.Status != StatusCancelled }
	keep := map[string]bool{}
	for i := range rows {
		r := &rows[i]
		t := typeOf(types, r.TypeID)
		switch {
		case t.Kind == "container":
		case under(r) != nil:
			if f.match(*r, t) {
				keep[r.ID] = true
			}
		case r.StartOn != nil || r.DueOn != nil || r.Status == StatusInProgress || r.Status == StatusInReview ||
			(r.Status == StatusShipped && r.StartedAt != nil):
			if f.match(*r, t) {
				keep[r.ID] = true
			}
		case open(r) && f.match(*r, t):
			out.Undated++
		}
	}
	// A container stays with its kept issues, or on its own when it
	// matches; so do the containers above it.
	for i := range rows {
		r := &rows[i]
		t := typeOf(types, r.TypeID)
		if t.Kind != "container" {
			if keep[r.ID] {
				for c := under(r); c != nil; c = under(c) {
					keep[c.ID] = true
				}
			}
			continue
		}
		if f.match(*r, t) {
			keep[r.ID] = true
			for c := under(r); c != nil; c = under(c) {
				keep[c.ID] = true
			}
		}
	}
	key := func(n int) string { return KeyOf(p.Key, n, p.Digits) }
	for i := range rows {
		r := &rows[i]
		if !keep[r.ID] {
			continue
		}
		t := typeOf(types, r.TypeID)
		it := TimelineIssue{Key: key(r.Number), Number: r.Number, Title: r.Title, Status: r.Status, Priority: r.Priority, Type: t,
			StartOn: optDate(r.StartOn), DueOn: optDate(r.DueOn), StartedAt: r.StartedAt, Assignee: person(r.AssigneeID, r.AssigneeName),
			Labels: r.Labels, Estimate: r.Estimate}
		if par := byID[r.ParentID]; par != nil && keep[par.ID] {
			it.Parent = key(par.Number)
		}
		if r.Status == StatusShipped {
			at := r.StatusAt
			it.ShippedAt = &at
		}
		if r.Children > 0 {
			it.Progress = &Progress{Total: r.Children, Shipped: r.Shipped, Done: r.Done}
		}
		if t.Kind == "container" {
			it.Estimate = nil
			if r.ChildrenPoints > 0 {
				pts := r.ChildrenPoints
				it.Estimate = &pts
			}
		}
		out.Items = append(out.Items, it)
	}
	blocks, err := s.repo.ProjectBlocks(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, b := range blocks {
		from, to := byID[b[0]], byID[b[1]]
		if from != nil && to != nil && keep[from.ID] && keep[to.ID] {
			out.Blocks = append(out.Blocks, TimelineBlock{From: key(from.Number), To: key(to.Number)})
		}
	}
	return out, nil
}
