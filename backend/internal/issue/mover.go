package issue

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"
)

// The mover (D-06, D-81): the events outbox's pushes, pull requests and
// merges link issues and move them forward; a merge live on every service
// it deploys to ships them. Applying an event is idempotent (history rows
// are unique per issue, event and kind; moves are guarded by status), and
// an event is marked applied only after, so a crash replays it as a no-op.

const (
	applyBatch     = 50
	maxFailures    = 5
	reconcileEvery = 5 * time.Minute
)

// ApplyEvents applies the outbox's unapplied events, oldest first. It runs
// on the events dispatcher's goroutine (code.Service.RunEvents), woken by
// every event written. Satisfies code.Issues.
func (s *Service) ApplyEvents(ctx context.Context) {
	for ctx.Err() == nil {
		events, err := s.repo.Unapplied(ctx, applyBatch)
		if err != nil {
			s.log.Warn("read events for issues", zap.Error(err))
			return
		}
		for _, e := range events {
			if err := s.apply(ctx, e); err != nil {
				s.failures[e.ID]++
				if s.failures[e.ID] < maxFailures {
					s.log.Warn("apply an event to issues; retried at the next event or tick", zap.Error(err), zap.Int64("event_id", e.ID))
					return
				}
				s.log.Error("apply an event to issues; given up", zap.Error(err), zap.Int64("event_id", e.ID))
			}
			delete(s.failures, e.ID)
			if err := s.repo.MarkApplied(ctx, e.ID); err != nil {
				s.log.Warn("mark an event applied to issues", zap.Error(err), zap.Int64("event_id", e.ID))
				return
			}
		}
		if len(events) < applyBatch {
			return
		}
	}
}

// apply links and moves the issues an event names.
func (s *Service) apply(ctx context.Context, e GitEvent) error {
	p, err := s.repo.Project(ctx, e.ProjectID)
	if err != nil || p.Key == "" {
		return err // no issues in the project yet: nothing to move
	}
	base := Event{ActorID: e.ActorID, Automatic: true, GitEventID: e.ID}
	switch {
	case (e.Kind == "push" || e.Kind == "mirror") && strings.Trim(e.AfterSHA, "0") != "":
		issues, err := s.named(ctx, p, e, e.Branch)
		if err != nil {
			return err
		}
		ev := base
		ev.Branch = e.Branch
		for _, c := range issues {
			linked, err := s.repo.LinkBranch(ctx, c.ID, e.RepositoryID, e.Branch)
			if err != nil {
				return err
			}
			if err := s.moveOrNote(ctx, c, ev, fromForBranch, StatusInProgress, linked); err != nil {
				return err
			}
		}
	case e.Kind == "pull_request" && e.PullID != "":
		ev := base
		ev.PullID = e.PullID
		if e.Action == "closed" {
			// Closed unmerged: back from review, unless another linked one
			// is still open.
			issues, err := s.repo.ForPull(ctx, e.PullID)
			if err != nil {
				return err
			}
			for _, c := range issues {
				if !c.CreatedAt.Before(e.CreatedAt) {
					continue
				}
				n, err := s.repo.OtherOpenPulls(ctx, c.ID, e.PullID)
				if err != nil {
					return err
				}
				if n > 0 {
					continue
				}
				if moved, err := s.repo.Move(ctx, Move{From: fromForReopened, To: StatusInProgress, Event: withIssue(ev, c.ID)}); err != nil {
					return err
				} else if moved {
					s.afterStatus(ctx, c.ID, StatusInProgress)
				}
			}
			return nil
		}
		issues, err := s.named(ctx, p, e, e.PullHead, e.PullTitle, e.PullBody)
		if err != nil {
			return err
		}
		for _, c := range issues {
			linked, err := s.repo.LinkPull(ctx, c.ID, e.RepositoryID, e.PullID)
			if err != nil {
				return err
			}
			if err := s.moveOrNote(ctx, c, ev, fromForPull, StatusInReview, linked); err != nil {
				return err
			}
		}
	case e.Kind == "merge" && e.PullID != "":
		// Its issues, and any its text names that weren't linked yet.
		named, err := s.named(ctx, p, e, e.PullHead, e.PullTitle, e.PullBody)
		if err != nil {
			return err
		}
		for _, c := range named {
			if _, err := s.repo.LinkPull(ctx, c.ID, e.RepositoryID, e.PullID); err != nil {
				return err
			}
		}
		issues, err := s.repo.ForPull(ctx, e.PullID)
		if err != nil {
			return err
		}
		ev := base
		ev.Kind, ev.PullID = "merged", e.PullID
		for _, c := range issues {
			if !c.CreatedAt.Before(e.CreatedAt) {
				continue
			}
			if _, err := s.repo.Record(ctx, withIssue(ev, c.ID)); err != nil {
				return err
			}
		}
		// A branch nothing deploys from ships at the merge.
		return s.ship(ctx, e.RepositoryID)
	}
	return nil
}

func withIssue(e Event, issueID string) Event {
	e.IssueID = issueID
	return e
}

// named are the project's issues whose key texts name, opened before the
// event (D-81: an event moves only issues that existed when it happened).
func (s *Service) named(ctx context.Context, p *Project, e GitEvent, texts ...string) ([]Candidate, error) {
	keys, err := s.keys(ctx, p)
	if err != nil {
		return nil, err
	}
	numbers := numbersIn(keys, texts...)
	if len(numbers) == 0 {
		return nil, nil
	}
	all, err := s.repo.ByNumbers(ctx, p.ID, numbers)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		if c.CreatedAt.Before(e.CreatedAt) {
			out = append(out, c)
		}
	}
	return out, nil
}

// moveOrNote moves c to `to` from one of from; when it doesn't move but
// the event linked new work, the history says so.
func (s *Service) moveOrNote(ctx context.Context, c Candidate, ev Event, from []string, to string, linked bool) error {
	moved, err := s.repo.Move(ctx, Move{From: from, To: to, Event: withIssue(ev, c.ID)})
	if moved {
		s.afterStatus(ctx, c.ID, to)
	}
	if err != nil || moved || !linked {
		return err
	}
	ev.Kind = "linked"
	_, err = s.repo.Record(ctx, withIssue(ev, c.ID))
	return err
}

// ship ships the issues whose merged pull requests (in repoID, or every
// repository for "") run on every service their merge deploys to, or
// deploy nowhere; one with a linked pull request still open waits.
func (s *Service) ship(ctx context.Context, repoID string) error {
	cands, err := s.repo.ShipCandidates(ctx, repoID)
	if err != nil {
		return err
	}
	// Candidates come grouped by issue: it ships when all its merges run.
	for i := 0; i < len(cands); {
		j := i
		for j < len(cands) && cands[j].IssueID == cands[i].IssueID {
			j++
		}
		if err := s.shipIssue(ctx, cands[i:j]); err != nil {
			return err
		}
		i = j
	}
	return nil
}

func (s *Service) shipIssue(ctx context.Context, merges []ShipCandidate) error {
	if merges[0].HasOpen {
		return nil
	}
	var last ShipCandidate
	deployment := ""
	for _, m := range merges {
		targets, err := s.repo.BranchTargets(ctx, m.RepositoryID, m.Base)
		if err != nil {
			return err
		}
		for _, t := range targets {
			if !s.runs(ctx, m.RepositoryID, m.MergeSHA, t.LiveSHA) {
				return nil
			}
			deployment = t.LiveDeploymentID
		}
		if m.MergedAt.After(last.MergedAt) {
			last = m
		}
	}
	moved, err := s.repo.Move(ctx, Move{From: fromForShipped, To: StatusShipped, Event: Event{
		IssueID: last.IssueID, ActorID: last.MergedBy, Automatic: true, PullID: last.PullID, DeploymentID: deployment,
	}})
	if moved {
		s.afterStatus(ctx, last.IssueID, StatusShipped)
	}
	return err
}

// DeployLive ships what a service's deploy going live completes.
// Satisfies deployment.Shipper.
func (s *Service) DeployLive(ctx context.Context, serviceID string) {
	s.shipFailures(ctx, serviceID)
	repoID, err := s.repo.ServiceRepository(ctx, serviceID)
	if err != nil || repoID == "" {
		if err != nil {
			s.log.Warn("ship issues: the service's repository", zap.Error(err), zap.String("service_id", serviceID))
		}
		return
	}
	if err := s.ship(ctx, repoID); err != nil {
		s.log.Warn("ship issues", zap.Error(err), zap.String("service_id", serviceID))
	}
}

// Run reconciles until ctx ends: at start and every 5 minutes it ships
// what a lost DeployLive (a restart mid-deploy) left behind, and brings
// every workspace's cycles up to its schedule (D-90).
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(reconcileEvery)
	defer tick.Stop()
	for {
		if err := s.ship(ctx, ""); err != nil && ctx.Err() == nil {
			s.log.Warn("reconcile shipped issues", zap.Error(err))
		}
		s.tickCycles(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
