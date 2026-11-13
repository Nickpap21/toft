package issue

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

var (
	ErrNotFailed  = errors.New("that deploy didn't fail")
	ErrNoProject  = errors.New("a workspace service's failures have no project to open an issue in")
	ErrNotService = errors.New("that deploy isn't this service's")
)

// AlreadyOpen is the service's open issue from its failures.
type AlreadyOpen struct{ Key string }

func (e *AlreadyOpen) Error() string {
	return "the service already has an open issue from its failures: " + e.Key
}

// MyIssues are the open issues assigned to a person across the workspace
// (D-88).
func (s *Service) MyIssues(ctx context.Context, orgID, userID string) ([]MyIssue, error) {
	out, err := s.repo.MyIssues(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	types, err := s.types(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		t := typeOf(types, out[i].typeID)
		out[i].Type = &t
	}
	return out, nil
}

// OpenFromFailure opens an issue from a failed deploy (D-88): a Bug of high
// priority labelled with the service, shipped when a later deploy of the
// service goes live.
func (s *Service) OpenFromFailure(ctx context.Context, orgID, serviceID, deploymentID, actorID string) (*Detail, error) {
	d, err := s.repo.FailedDeployment(ctx, deploymentID)
	if err != nil {
		return nil, err
	}
	if d.OrgID != orgID {
		return nil, ErrNotFound
	}
	if d.ServiceID != serviceID {
		return nil, ErrNotService
	}
	if d.Status != "failed" {
		return nil, ErrNotFailed
	}
	if d.ProjectID == "" {
		return nil, ErrNoProject
	}
	open, err := s.repo.OpenFailureIssues(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	if len(open) > 0 {
		p, err := s.repo.Project(ctx, d.ProjectID)
		if err != nil {
			return nil, err
		}
		return nil, &AlreadyOpen{Key: KeyOf(p.Key, open[0].Number, p.Digits)}
	}
	return s.openFailure(ctx, d, actorID)
}

func (s *Service) openFailure(ctx context.Context, d *FailedDeployment, actorID string) (*Detail, error) {
	types, err := s.types(ctx, d.OrgID)
	if err != nil {
		return nil, err
	}
	typeID := ""
	for _, t := range types {
		if t.Icon == "bug" && t.Kind == "standard" && !t.Archived {
			typeID = t.ID
			break
		}
	}
	where := d.ServiceName
	if d.Environment != "" {
		where += " in " + d.Environment
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The deploy of **%s** failed at %s UTC.\n\n", where, d.At.UTC().Format("2006-01-02 15:04"))
	if d.Error != "" {
		fmt.Fprintf(&b, "```\n%s\n```\n\n", strings.TrimSpace(d.Error))
	}
	if d.CommitSHA != "" {
		fmt.Fprintf(&b, "Commit `%s`", short(d.CommitSHA, 7))
		if d.CommitMessage != "" {
			fmt.Fprintf(&b, ": %s", d.CommitMessage)
		}
		b.WriteString(".\n\n")
	}
	b.WriteString("Opened from the failed deploy " + short(d.ID, 8) + ". It moves to Shipped when a later deploy of " + d.ServiceName + " goes live.\n")
	label := strings.ToLower(strings.TrimSpace(d.ServiceName))
	if len(label) > 40 || strings.ContainsRune(label, ',') {
		label = ""
	}
	in := CreateInput{Title: "Deploy failed: " + where, Description: b.String(), Priority: "high", TypeID: typeID}
	if label != "" {
		in.Labels = []string{label}
	}
	det, err := s.Create(ctx, d.ProjectID, actorID, in)
	if err != nil {
		return nil, err
	}
	st, err := s.repo.Get(ctx, d.ProjectID, det.Number)
	if err != nil {
		return nil, err
	}
	if err := s.repo.SetFromDeployment(ctx, st.ID, d.ID); err != nil {
		return nil, err
	}
	return det, nil
}

// DeployFailed hears of every failed deploy (D-88): in a project that asks
// for it, the service's open issue from its failures notes this one, or
// one is opened. Satisfies deployment.Shipper.
func (s *Service) DeployFailed(ctx context.Context, serviceID, deploymentID string) {
	d, err := s.repo.FailedDeployment(ctx, deploymentID)
	if err != nil {
		s.log.Warn("failed deploy issue: the deploy", zap.Error(err), zap.String("deployment_id", deploymentID))
		return
	}
	if !d.FailedDeployIssues || d.ProjectID == "" {
		return
	}
	open, err := s.repo.OpenFailureIssues(ctx, serviceID)
	if err != nil {
		s.log.Warn("failed deploy issue: open ones", zap.Error(err))
		return
	}
	if len(open) > 0 {
		if _, err := s.repo.Record(ctx, Event{IssueID: open[0].ID, Kind: "deploy_failed", Automatic: true, DeploymentID: d.ID, To: short(d.CommitSHA, 7)}); err != nil {
			s.log.Warn("failed deploy issue: note", zap.Error(err))
		}
		return
	}
	if _, err := s.openFailure(ctx, d, ""); err != nil {
		s.log.Warn("failed deploy issue: open", zap.Error(err), zap.String("service_id", serviceID))
	}
}

// shipFailures ships a service's open issues from its failures once a deploy
// of it is live.
func (s *Service) shipFailures(ctx context.Context, serviceID string) {
	open, err := s.repo.OpenFailureIssues(ctx, serviceID)
	if err != nil || len(open) == 0 {
		if err != nil {
			s.log.Warn("ship failure issues", zap.Error(err))
		}
		return
	}
	live, err := s.repo.LiveDeployment(ctx, serviceID)
	if err != nil {
		s.log.Warn("ship failure issues: the live deploy", zap.Error(err))
		return
	}
	for _, f := range open {
		moved, err := s.repo.Move(ctx, Move{From: fromForShipped, To: StatusShipped, Event: Event{IssueID: f.ID, Automatic: true, DeploymentID: live}})
		if err != nil {
			s.log.Warn("ship failure issue", zap.Error(err))
			continue
		}
		if moved {
			s.afterStatus(ctx, f.ID, StatusShipped)
		}
	}
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
