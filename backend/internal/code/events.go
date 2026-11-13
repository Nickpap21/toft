package code

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"go.uber.org/zap"
)

// The events outbox (D-77): every branch a push or a panel merge moves is a
// git_events row, written once git has accepted it, and one dispatcher
// (RunEvents) hands each to the PushConsumer, which deploys the services
// building from that branch. A merge's update-ref runs no hook, so
// MergePull writes its own.

// Push is a branch a push or a merge moved, as its consumer gets it.
type Push struct {
	EventID                        int64
	RepositoryID, ProjectID, OrgID string
	Branch, SHA, Subject           string
	// Kind is push or merge; ActorName is who pushed or merged (an
	// account's name), PullNumber the pull request a merge merged.
	Kind, ActorID, ActorName string
	PullNumber               int
}

// PushConsumer deploys what a push moved. Satisfied by *webhook.Deployer.
// It must be safe to call twice for one event: a dispatch cut short is
// handed over again.
type PushConsumer interface {
	DeployPush(ctx context.Context, p Push) error
}

// GitEvent is an outbox row to write. Kind is push, merge or mirror (a
// branch moved), tag, or pull_request with its Action; Deploys says whether
// the dispatcher deploys it (a branch that moved), and the others are
// written dispatched (D-79).
type GitEvent struct {
	RepoID, Kind, Action, Branch, Before, After, ActorID, PullID, Subject string
	Commits                                                               int
	Forced, Deploys                                                       bool
	// WithKey: pushed with an API key, or by whoever an unknown actor is
	// (D-97); a pull request's preview then waits for approval.
	WithKey bool
	// AgentID is the agent connection that pushed (D-102).
	AgentID string
}

// hookEvents are the webhook events a git event is delivered as.
func (e GitEvent) hookEvents() []string {
	switch e.Kind {
	case "merge":
		return []string{"push", "pull_request"}
	case "pull_request":
		return []string{"pull_request"}
	}
	return []string{"push"}
}

// ClaimedEvent is an outbox row the dispatcher leased.
type ClaimedEvent struct {
	ID                                     int64
	RepoID, ProjectID, OrgID, Kind, Branch string
	After, Subject, ActorID, ActorName     string
	PullNumber, Attempts                   int
	CreatedAt                              time.Time
}

const (
	claimBatch   = 20
	maxAttempts  = 5
	staleAfter   = time.Hour
	keepEvents   = 90 * 24 * time.Hour
	dispatchTick = 30 * time.Second
)

// WithPushConsumer sets what deploys pushes.
func (s *Service) WithPushConsumer(c PushConsumer) *Service {
	s.consumer = c
	return s
}

// Issues is what pull requests and branches show of the issues module, and
// how its events reach it (D-81). Satisfied by *issue.Service.
type Issues interface {
	// IssueKeys are the project's issue key and its former ones (none
	// before its first issue).
	IssueKeys(ctx context.Context, projectID string) []string
	// ProjectHandles are the project's workspace's @handles (D-88).
	ProjectHandles(ctx context.Context, projectID string) []string
	PullIssues(ctx context.Context, pullID string) ([]IssueRef, error)
	BranchIssues(ctx context.Context, repoID string, branches []string) (map[string][]IssueRef, error)
	// ApplyEvents applies the outbox's events it has not applied yet.
	ApplyEvents(ctx context.Context)
}

// WithIssues links pull requests and branches to issues, and lets the
// events dispatcher hand them every event.
func (s *Service) WithIssues(i Issues) *Service {
	s.issues = i
	return s
}

// Docs is what pull requests show of the docs module (D-86). Satisfied by
// *doc.Service.
type Docs interface {
	PullDocs(ctx context.Context, pullID string) ([]DocRef, error)
}

// WithDocs lists the pages that name a pull request on it.
func (s *Service) WithDocs(d Docs) *Service {
	s.docs = d
	return s
}

// issueKeys are the project's issue keys, none without the issues module.
func (s *Service) issueKeys(ctx context.Context, projectID string) []string {
	if s.issues == nil {
		return nil
	}
	return s.issues.IssueKeys(ctx, projectID)
}

// handles are a project's workspace's @handles, none without the issues
// module.
func (s *Service) handles(ctx context.Context, projectID string) []string {
	if s.issues == nil {
		return nil
	}
	return s.issues.ProjectHandles(ctx, projectID)
}

// nudge wakes the dispatcher, and the previews.
func (s *Service) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
	if s.previews != nil {
		s.previews.Nudge()
	}
}

// recordPushes writes the events of what a push moved (recordMoves), an
// agent's with its connection (D-102).
func (s *Service) recordPushes(ctx context.Context, repoID string, a Actor, updates []gitstore.RefUpdate) {
	s.recordMoves(ctx, repoID, a.UserID, "push", a.ViaKey, updates, a.AgentConnectionID)
}

// recordMoves writes an event for each branch or tag updates moved, kind
// push or a pull mirror's mirror, and wakes the dispatcher when a branch
// moved. A deleted branch and a tag deploy nothing; they are written for
// webhooks and push mirrors (D-79). Errors are logged: the push succeeded.
func (s *Service) recordMoves(ctx context.Context, repoID, actorID, kind string, withKey bool, updates []gitstore.RefUpdate, agentID string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	wrote := false
	for _, u := range updates {
		ev := GitEvent{RepoID: repoID, Kind: kind, Before: u.Old, After: u.New, ActorID: actorID, WithKey: withKey, AgentID: agentID}
		deleted := strings.Trim(u.New, "0") == ""
		if branch, ok := strings.CutPrefix(u.Ref, "refs/heads/"); ok {
			ev.Branch, ev.Deploys = branch, !deleted
		} else if tag, ok := strings.CutPrefix(u.Ref, "refs/tags/"); ok {
			ev.Kind, ev.Branch = "tag", tag
		} else {
			continue
		}
		if !deleted {
			var head gitstore.Page[gitstore.Commit]
			if err := s.git.Browse(ctx, repoID, "commits", url.Values{"ref": {u.New}, "limit": {"1"}}, &head); err == nil && len(head.Items) > 0 {
				ev.Subject = head.Items[0].Subject
			}
		}
		if ev.Deploys && strings.Trim(u.Old, "0") != "" {
			var info struct {
				Count  int  `json:"count"`
				Forced bool `json:"forced"`
			}
			if err := s.git.Browse(ctx, repoID, "range", url.Values{"from": {u.Old}, "to": {u.New}}, &info); err == nil {
				ev.Commits, ev.Forced = info.Count, info.Forced
			}
		}
		if _, err := s.repo.InsertEvent(ctx, ev); err != nil {
			s.log.Error("record a push", zap.Error(err), zap.String("repository_id", repoID), zap.String("ref", u.Ref))
			continue
		}
		wrote = true
	}
	// Every event wakes the dispatcher: what deploys nothing still moves
	// issues (D-81).
	if wrote {
		s.nudge()
	}
	s.nudgeHooks()
	s.nudgeMirrors()
}

// recordMerge writes the event of a merge from the panel.
func (s *Service) recordMerge(ctx context.Context, repoID, actorID string, p *StoredPull, method, before, after, subject string, commits int) {
	if _, err := s.repo.InsertEvent(ctx, GitEvent{RepoID: repoID, Kind: "merge", Branch: p.Base, Before: before, After: after,
		ActorID: actorID, PullID: p.ID, Subject: subject, Commits: commits, Deploys: true}); err != nil {
		s.log.Error("record a merge", zap.Error(err), zap.String("repository_id", repoID), zap.Int("number", p.Number))
		return
	}
	s.nudge()
	s.nudgeHooks()
	s.nudgeMirrors()
}

// recordPull writes a pull request's opened, pushed, closed or reopened,
// for webhooks (D-79) and previews (D-97); a merge is recordMerge's.
// withKey says the pull request was opened, or pushed to, with an API key.
func (s *Service) recordPull(ctx context.Context, repoID, actorID string, p *StoredPull, action, head string, withKey bool) {
	agent, _ := actor.AgentFrom(ctx)
	if _, err := s.repo.InsertEvent(ctx, GitEvent{RepoID: repoID, Kind: "pull_request", Action: action, Branch: p.Base,
		Before: strings.Repeat("0", 40), After: head, ActorID: actorID, PullID: p.ID, Subject: p.Title, WithKey: withKey,
		AgentID: agent.ConnectionID}); err != nil {
		s.log.Error("record a pull request event", zap.Error(err), zap.String("repository_id", repoID), zap.Int("number", p.Number))
		return
	}
	s.nudge()
	s.nudgeHooks()
}

// RunEvents dispatches the outbox until ctx ends: at once when an event is
// written, and every 30 s for retries and for what an earlier run left; it
// also deletes dispatched events older than 90 days, hourly.
func (s *Service) RunEvents(ctx context.Context) {
	if s.consumer == nil {
		return
	}
	tick := time.NewTicker(dispatchTick)
	defer tick.Stop()
	var gc time.Time
	for {
		s.dispatch(ctx)
		if s.issues != nil {
			s.issues.ApplyEvents(ctx)
		}
		if time.Since(gc) > time.Hour {
			gc = time.Now()
			if n, err := s.repo.DeleteOldEvents(ctx, gc.Add(-keepEvents)); err != nil {
				s.log.Warn("delete old git events", zap.Error(err))
			} else if n > 0 {
				s.log.Info("deleted old git events", zap.Int64("count", n))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-tick.C:
		}
	}
}

// dispatch hands the due events to the consumer, oldest first. Of several
// events for one branch only the newest deploys (a push deploy builds the
// branch's tip anyway); an event over an hour old is skipped, so a panel
// that was down doesn't deploy yesterday's push; a failure is retried 5
// times, then kept with its error.
func (s *Service) dispatch(ctx context.Context) {
	for ctx.Err() == nil {
		evs, err := s.repo.ClaimEvents(ctx, claimBatch)
		if err != nil {
			s.log.Error("claim git events", zap.Error(err))
			return
		}
		if len(evs) == 0 {
			return
		}
		sort.Slice(evs, func(i, j int) bool { return evs[i].ID < evs[j].ID })
		newest := map[string]int64{}
		for _, e := range evs {
			newest[e.RepoID+"\x00"+e.Branch] = e.ID
		}
		for _, e := range evs {
			switch {
			case newest[e.RepoID+"\x00"+e.Branch] != e.ID:
				s.finish(ctx, e.ID, "superseded by a newer push to "+e.Branch)
			case time.Since(e.CreatedAt) > staleAfter:
				s.finish(ctx, e.ID, "skipped: over an hour old when the panel got to it")
			default:
				err := s.consumer.DeployPush(ctx, Push{EventID: e.ID, RepositoryID: e.RepoID, ProjectID: e.ProjectID, OrgID: e.OrgID,
					Branch: e.Branch, SHA: e.After, Subject: e.Subject, Kind: e.Kind, ActorID: e.ActorID, ActorName: e.ActorName, PullNumber: e.PullNumber})
				switch {
				case ctx.Err() != nil:
					return // the lease runs out and the next start dispatches it
				case err == nil:
					s.finish(ctx, e.ID, "")
				case e.Attempts >= maxAttempts:
					s.log.Error("deploy a push", zap.Error(err), zap.Int64("event_id", e.ID))
					s.finish(ctx, e.ID, "couldn't start its deploys: "+err.Error())
				default:
					s.log.Warn("deploy a push, retrying", zap.Error(err), zap.Int64("event_id", e.ID))
					if err := s.repo.RetryEvent(ctx, e.ID, time.Now().Add(time.Duration(1<<e.Attempts)*15*time.Second), err.Error()); err != nil {
						s.log.Error("retry git event", zap.Error(err), zap.Int64("event_id", e.ID))
					}
				}
			}
		}
	}
}

func (s *Service) finish(ctx context.Context, id int64, msg string) {
	if err := s.repo.FinishEvent(ctx, id, msg); err != nil {
		s.log.Error("finish git event", zap.Error(err), zap.Int64("event_id", id))
	}
}

// ─── for the deploy pipeline ─────────────────────────────────────────────────

// HasRepository says whether repositoryID is one of projectID's.
func (s *Service) HasRepository(ctx context.Context, projectID, repositoryID string) (bool, error) {
	_, err := s.repoOf(ctx, projectID, repositoryID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Archive is the tar of the commit ref names in the project's repository
// repositoryID (the default branch when ref is ""), for a deploy's build.
func (s *Service) Archive(ctx context.Context, projectID, repositoryID, ref string) (*gitstore.Archive, error) {
	r, err := s.repoOf(ctx, projectID, repositoryID)
	if err != nil {
		return nil, err
	}
	return s.git.Archive(ctx, r.ID, ref)
}
