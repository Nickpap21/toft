package issue

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// Cycles, dates and estimates (D-90) in Postgres.

func pgDate(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *t, Valid: true}
}

func day(t time.Time) pgtype.Date { return pgtype.Date{Time: t, Valid: true} }

func fromDate(d pgtype.Date) *time.Time {
	if !d.Valid {
		return nil
	}
	t := civil(d.Time)
	return &t
}

func dateOf(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return civil(d.Time)
}

func est16(n *int) *int16 {
	if n == nil {
		return nil
	}
	v := int16(*n)
	return &v
}

func estInt(n *int16) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

func i32(p *int32) int {
	if p == nil {
		return 0
	}
	return int(*p)
}

func (r *postgresRepository) OrgSettings(ctx context.Context, orgID string) (OrgSettings, error) {
	row, err := r.q.GetOrgIssueSettings(ctx, id(orgID))
	if err != nil {
		return OrgSettings{}, fmt.Errorf("GetOrgIssueSettings: %w", notFound(err))
	}
	o := OrgSettings{Digits: int(row.IssueKeyDigits), StartDay: int(row.CycleStartDay), Zone: row.CycleTimeZone, Estimates: row.IssueEstimates}
	if row.CycleWeeks != nil {
		o.Weeks = int(*row.CycleWeeks)
	}
	return o, nil
}

func (r *postgresRepository) SetCycleSchedule(ctx context.Context, orgID string, weeks *int, startDay int, zone string) error {
	var w *int16
	if weeks != nil {
		v := int16(*weeks)
		w = &v
	}
	if err := r.q.SetOrgCycleSchedule(ctx, sqlcdb.SetOrgCycleScheduleParams{Weeks: w, StartDay: int16(startDay), TimeZone: zone, ID: id(orgID)}); err != nil {
		return fmt.Errorf("SetOrgCycleSchedule: %w", err)
	}
	return nil
}

func (r *postgresRepository) SetEstimates(ctx context.Context, orgID, scale string) error {
	if err := r.q.SetOrgIssueEstimates(ctx, sqlcdb.SetOrgIssueEstimatesParams{Estimates: scale, ID: id(orgID)}); err != nil {
		return fmt.Errorf("SetOrgIssueEstimates: %w", err)
	}
	return nil
}

func (r *postgresRepository) CycleSchedules(ctx context.Context) ([]OrgSchedule, error) {
	rows, err := r.q.ListCycleSchedules(ctx)
	if err != nil {
		return nil, fmt.Errorf("ListCycleSchedules: %w", err)
	}
	out := make([]OrgSchedule, 0, len(rows))
	for _, row := range rows {
		out = append(out, OrgSchedule{OrgID: str(row.ID), Zone: row.CycleTimeZone, Weeks: int(row.Weeks), StartDay: int(row.CycleStartDay)})
	}
	return out, nil
}

func (r *postgresRepository) Cycles(ctx context.Context, orgID string) ([]StoredCycle, error) {
	rows, err := r.q.ListCycles(ctx, id(orgID))
	if err != nil {
		return nil, fmt.Errorf("ListCycles: %w", err)
	}
	out := make([]StoredCycle, 0, len(rows))
	for _, row := range rows {
		c := StoredCycle{ID: str(row.ID), Number: int(row.Number), Name: row.Name, StartsOn: dateOf(row.StartsOn), EndsOn: dateOf(row.EndsOn),
			Closed: row.ClosedAt.Valid}
		if c.Closed {
			c.Final = &CycleCounts{Scope: i32(row.FinalScope), Shipped: i32(row.FinalShipped), Cancelled: i32(row.FinalCancelled),
				Carried: i32(row.FinalCarried), Points: i32(row.FinalPoints), ShippedPoints: i32(row.FinalShippedPoints)}
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *postgresRepository) AddCycle(ctx context.Context, orgID string, c StoredCycle) error {
	if _, err := r.q.InsertCycle(ctx, sqlcdb.InsertCycleParams{OrgID: id(orgID), Number: int32(c.Number), StartsOn: day(c.StartsOn), EndsOn: day(c.EndsOn)}); err != nil {
		return fmt.Errorf("InsertCycle: %w", err)
	}
	return nil
}

func (r *postgresRepository) MoveCycle(ctx context.Context, c StoredCycle, today time.Time) error {
	if _, err := r.q.MoveUpcomingCycle(ctx, sqlcdb.MoveUpcomingCycleParams{StartsOn: day(c.StartsOn), EndsOn: day(c.EndsOn), ID: id(c.ID), Today: day(today)}); err != nil {
		return fmt.Errorf("MoveUpcomingCycle: %w", err)
	}
	return nil
}

func (r *postgresRepository) RenameCycle(ctx context.Context, orgID string, number int, name string) (bool, error) {
	n, err := r.q.RenameCycle(ctx, sqlcdb.RenameCycleParams{Name: name, OrgID: id(orgID), Number: int32(number)})
	if err != nil {
		return false, fmt.Errorf("RenameCycle: %w", err)
	}
	return n == 1, nil
}

func (r *postgresRepository) CycleByNumber(ctx context.Context, orgID string, number int) (*StoredCycle, error) {
	row, err := r.q.CycleByNumber(ctx, sqlcdb.CycleByNumberParams{OrgID: id(orgID), Number: int32(number)})
	if err != nil {
		return nil, notFound(err)
	}
	return &StoredCycle{ID: str(row.ID), Number: number, StartsOn: dateOf(row.StartsOn), EndsOn: dateOf(row.EndsOn)}, nil
}

func (r *postgresRepository) CloseCycle(ctx context.Context, orgID string, c StoredCycle) error {
	return r.inTx(ctx, func(q *sqlcdb.Queries) error {
		next, err := q.CycleByNumber(ctx, sqlcdb.CycleByNumberParams{OrgID: id(orgID), Number: int32(c.Number + 1)})
		if err != nil {
			return fmt.Errorf("CycleByNumber: %w", err)
		}
		if err := q.SnapshotCycleDay(ctx, sqlcdb.SnapshotCycleDayParams{CycleID: id(c.ID), Day: day(c.EndsOn)}); err != nil {
			return fmt.Errorf("SnapshotCycleDay: %w", err)
		}
		n, err := q.CloseCycle(ctx, id(c.ID))
		if err != nil {
			return fmt.Errorf("CloseCycle: %w", err)
		}
		if n == 0 {
			return nil // closed already
		}
		moved, err := q.CarryCycleIssues(ctx, sqlcdb.CarryCycleIssuesParams{ToCycleID: next.ID, CycleID: id(c.ID)})
		if err != nil {
			return fmt.Errorf("CarryCycleIssues: %w", err)
		}
		if len(moved) == 0 {
			return nil
		}
		if err := q.InsertCycleCarryovers(ctx, sqlcdb.InsertCycleCarryoversParams{CycleID: id(c.ID), IssueIds: moved, ToCycleID: next.ID}); err != nil {
			return fmt.Errorf("InsertCycleCarryovers: %w", err)
		}
		if err := q.InsertCarryEvents(ctx, sqlcdb.InsertCarryEventsParams{IssueIds: moved, FromValue: strconv.Itoa(c.Number), ToValue: strconv.Itoa(c.Number + 1)}); err != nil {
			return fmt.Errorf("InsertCarryEvents: %w", err)
		}
		return nil
	})
}

func (r *postgresRepository) SnapshotCycle(ctx context.Context, cycleID string, d time.Time) error {
	if err := r.q.SnapshotCycleDay(ctx, sqlcdb.SnapshotCycleDayParams{CycleID: id(cycleID), Day: day(d)}); err != nil {
		return fmt.Errorf("SnapshotCycleDay: %w", err)
	}
	return nil
}

func (r *postgresRepository) CycleCounts(ctx context.Context, orgID, projectID string) (map[string]CycleCounts, error) {
	rows, err := r.q.CycleCounts(ctx, sqlcdb.CycleCountsParams{OrgID: id(orgID), ProjectID: id(projectID)})
	if err != nil {
		return nil, fmt.Errorf("CycleCounts: %w", err)
	}
	out := make(map[string]CycleCounts, len(rows))
	for _, row := range rows {
		out[str(row.CycleID)] = CycleCounts{Scope: int(row.Scope), Started: int(row.Started), Shipped: int(row.Shipped), Cancelled: int(row.Cancelled),
			Points: int(row.Points), StartedPoints: int(row.StartedPoints), ShippedPoints: int(row.ShippedPoints)}
	}
	return out, nil
}

func (r *postgresRepository) CycleDays(ctx context.Context, cycleID string) ([]CycleDay, error) {
	rows, err := r.q.ListCycleDays(ctx, id(cycleID))
	if err != nil {
		return nil, fmt.Errorf("ListCycleDays: %w", err)
	}
	out := make([]CycleDay, 0, len(rows))
	for _, row := range rows {
		out = append(out, CycleDay{Day: fmtDate(dateOf(row.Day)), Scope: int(row.Scope), Started: int(row.Started), Shipped: int(row.Shipped),
			ScopePoints: int(row.ScopePoints), StartedPoints: int(row.StartedPoints), ShippedPoints: int(row.ShippedPoints)})
	}
	return out, nil
}

func (r *postgresRepository) CycleShares(ctx context.Context, cycleID string) ([]CycleShare, []CycleShare, error) {
	rows, err := r.q.CycleShares(ctx, id(cycleID))
	if err != nil {
		return nil, nil, fmt.Errorf("CycleShares: %w", err)
	}
	projects, assignees := make([]CycleShare, 0, len(rows)), make([]CycleShare, 0, len(rows))
	for _, row := range rows {
		sh := CycleShare{ID: row.Ref, Name: row.Name, Total: int(row.Total), Shipped: int(row.Shipped)}
		if row.By == "project" {
			projects = append(projects, sh)
		} else {
			assignees = append(assignees, sh)
		}
	}
	return projects, assignees, nil
}

func (r *postgresRepository) CycleMoves(ctx context.Context, c StoredCycle, since time.Time) (int, int, error) {
	row, err := r.q.CycleMoves(ctx, sqlcdb.CycleMovesParams{CycleID: id(c.ID), Number: strconv.Itoa(c.Number), Since: ts(since)})
	if err != nil {
		return 0, 0, fmt.Errorf("CycleMoves: %w", err)
	}
	return int(row.Added), int(row.CarriedIn), nil
}

func (r *postgresRepository) CycleIssues(ctx context.Context, cycleID, projectID, afterID string, limit int) ([]CycleIssueRow, error) {
	rows, err := r.q.ListCycleIssues(ctx, sqlcdb.ListCycleIssuesParams{CycleID: id(cycleID), ProjectID: id(projectID), AfterID: id(afterID), RowLimit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("ListCycleIssues: %w", err)
	}
	out := make([]CycleIssueRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, CycleIssueRow{ID: str(row.ID), ProjectID: str(row.ProjectID), Project: row.ProjectName,
			Key: KeyOf(row.IssueKey, int(row.Number), int(row.Digits)), Number: int(row.Number), Title: row.Title, Status: row.Status,
			Priority: row.Priority, TypeID: str(row.TypeID), AssigneeID: str(row.AssigneeID), AssigneeName: row.AssigneeName,
			Estimate: estInt(row.Estimate), DueOn: fromDate(row.DueOn), Labels: row.Labels,
			CarriedFrom: int(row.CarriedFrom), CarriedTo: int(row.CarriedTo)})
	}
	return out, nil
}

func (r *postgresRepository) TimelineIssues(ctx context.Context, projectID string, since time.Time) ([]TimelineRow, error) {
	rows, err := r.q.ListTimelineIssues(ctx, sqlcdb.ListTimelineIssuesParams{ProjectID: id(projectID), Since: ts(since)})
	if err != nil {
		return nil, fmt.Errorf("ListTimelineIssues: %w", err)
	}
	out := make([]TimelineRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, TimelineRow{ID: str(row.ID), ParentID: str(row.ParentID), Number: int(row.Number), Title: row.Title,
			Status: row.Status, Priority: row.Priority, TypeID: str(row.TypeID), StartOn: fromDate(row.StartOn), DueOn: fromDate(row.DueOn),
			Estimate: estInt(row.Estimate), AssigneeID: str(row.AssigneeID), AssigneeName: row.AssigneeName, StatusAt: row.StatusChangedAt.Time,
			StartedAt: at(row.StartedAt), Labels: row.Labels, Children: int(row.Children), Shipped: int(row.ChildrenShipped),
			Done: int(row.ChildrenDone), ChildrenPoints: int(row.ChildrenPoints)})
	}
	return out, nil
}

func (r *postgresRepository) ProjectBlocks(ctx context.Context, projectID string) ([][2]string, error) {
	rows, err := r.q.ListProjectBlocks(ctx, id(projectID))
	if err != nil {
		return nil, fmt.Errorf("ListProjectBlocks: %w", err)
	}
	out := make([][2]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, [2]string{str(row.FromIssueID), str(row.ToIssueID)})
	}
	return out, nil
}
