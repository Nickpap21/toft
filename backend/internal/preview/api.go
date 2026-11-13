package preview

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"

	pactor "github.com/Nickpap21/toft/backend/pkg/actor"
	"go.uber.org/zap"
)

// EnvironmentChecker says whether an environment is one of a project's own
// (not a preview's).
type EnvironmentChecker interface {
	OwnEnvironment(ctx context.Context, projectID, environmentID string) (bool, error)
}

// domainPattern is a preview domain: two labels or more, the schema's CHECK.
var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// GetSettings returns a project's previews settings.
func (s *Service) GetSettings(ctx context.Context, projectID string) (Settings, error) {
	return s.store.Settings(ctx, projectID)
}

// SetSettings turns a project's previews on (copying environmentID) or off
// (""), and sets their domain ("" for sslip.io names) and, unless max is
// nil, how many run at once. Turning them off or copying another
// environment removes the running ones; open pull requests then get one at
// their next push, or from Start preview. A higher limit starts the ones
// waiting for room; a lower one stops none.
func (s *Service) SetSettings(ctx context.Context, envs EnvironmentChecker, projectID, environmentID, domain string, max *int, by Actor) (Settings, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain != "" && (len(domain) > 200 || !domainPattern.MatchString(domain)) {
		return Settings{}, ErrBadDomain
	}
	if max != nil && (*max < 1 || *max > 20) {
		return Settings{}, ErrBadMax
	}
	if environmentID != "" {
		ok, err := envs.OwnEnvironment(ctx, projectID, environmentID)
		if err != nil {
			return Settings{}, err
		}
		if !ok {
			return Settings{}, ErrBadEnvironment
		}
		if !s.isolated {
			return Settings{}, ErrLegacyNetworks
		}
	}
	before, err := s.store.Settings(ctx, projectID)
	if err != nil {
		return Settings{}, err
	}
	if err := s.store.SetSettings(ctx, projectID, environmentID, domain, max); err != nil {
		return Settings{}, err
	}
	if before.EnvironmentID == environmentID && max != nil && *max > before.Max {
		waiting, err := s.store.PullsOf(ctx, ScopeWaitingRoom, projectID)
		if err != nil {
			return Settings{}, err
		}
		// In the background, as a removal's next start: each start copies
		// and deploys, and closing the tab mustn't leave them waiting.
		go func() {
			for _, id := range waiting {
				if err := s.reconcile(context.Background(), id, by, cause{kind: causeStart}); err != nil {
					s.log.Warn("previews: start one waiting for room", zap.Error(err), zap.String("pull_request_id", id))
				}
			}
		}()
	}
	if before.EnvironmentID != environmentID {
		pulls, err := s.store.PullsOf(ctx, ScopeProject, projectID)
		if err != nil {
			return Settings{}, err
		}
		for _, id := range pulls {
			if err := s.reconcile(ctx, id, by, cause{kind: causeSettings}); err != nil {
				s.log.Warn("previews: after a settings change", zap.Error(err), zap.String("pull_request_id", id))
			}
		}
	}
	return s.store.Settings(ctx, projectID)
}

// Of returns a pull request's preview, or ErrNotFound.
func (s *Service) Of(ctx context.Context, projectID, repoID string, number int) (*View, error) {
	pullID, err := s.store.PullID(ctx, projectID, repoID, number)
	if err != nil {
		return nil, err
	}
	pv, err := s.store.ByPull(ctx, pullID)
	if err != nil {
		return nil, err
	}
	if pv == nil {
		return nil, ErrNotFound
	}
	pr, err := s.store.Pull(ctx, pullID)
	if err != nil {
		return nil, err
	}
	set, err := s.store.Settings(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, pv, pr, set, "")
}

// List returns a project's previews, oldest first, a page at a time.
func (s *Service) List(ctx context.Context, projectID, cursor string, limit int) ([]View, string, error) {
	rows, next, err := s.store.List(ctx, projectID, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	set, err := s.store.Settings(ctx, projectID)
	if err != nil {
		return nil, "", err
	}
	out := make([]View, 0, len(rows))
	for _, r := range rows {
		pr, err := s.store.Pull(ctx, r.PullID)
		if err != nil {
			continue
		}
		pv := r.Preview
		v, err := s.view(ctx, &pv, pr, set, r.EnvironmentName)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *v)
	}
	return out, next, nil
}

// view is pv as the API shows it.
func (s *Service) view(ctx context.Context, pv *Preview, pr Pull, set Settings, envName string) (*View, error) {
	if pv.Untrusted && pv.State == StateWaitingApproval && pv.ApprovedSHA == "" && pv.Note != "" {
		// Before any approval the note is only ever untrustedBlock's, kept
		// from the last push: read it again, so a server that has gVisor
		// since doesn't keep the approval button hidden.
		pv.Note = s.untrustedBlock(ctx, set)
	}
	v := &View{PullNumber: pr.Number, RepositoryID: pr.RepoID, Repository: pr.RepoName, Title: pr.Title, Branch: pr.Head,
		Copies: set.EnvironmentName, Untrusted: pv.Untrusted, HeadSHA: pv.HeadSHA, ApprovedSHA: pv.ApprovedSHA,
		ApprovedAt: pv.ApprovedAt, Stopped: pv.StoppedReason, Note: pv.Note, CreatedAt: pv.CreatedAt, UpdatedAt: pv.UpdatedAt,
		Services: []ServiceView{}, Reached: []string{}, Migrations: pv.MigrationFiles, Unchecked: pv.MigrationsUnchecked}
	if v.Migrations == nil {
		v.Migrations = []string{}
	}
	v.NeedsApprove = pv.Untrusted && pv.ApprovedSHA != pv.HeadSHA && pv.State != StateRemoving
	if pr.OpenedWithKey {
		v.Opened = &OpenedWithKey{Key: pr.APIKeyName, Owner: pr.AuthorName, Agent: pr.AgentName}
	}
	v.ApprovedBy = pv.ApprovedByName
	if pv.EnvID != "" {
		if envName == "" {
			envName = EnvironmentName(pr.Number, pr.RepoName, pr.FirstRepository)
		}
		v.Environment = &ViewEnv{ID: pv.EnvID, Name: envName}
		copies, err := s.store.Copies(ctx, pv.EnvID)
		if err != nil {
			return nil, err
		}
		copied := map[string]bool{}
		for _, c := range copies {
			copied[c.PreviewOf] = true
			sv := ServiceView{ID: c.ID, Name: c.Name, Server: c.ServerName, SHA: c.DeploymentSHA, Error: c.DeploymentError,
				DeploymentID: c.DeploymentID, StartedAt: c.DeploymentCreatedAt, FinishedAt: c.DeploymentFinishedAt,
				Addresses: append([]string{}, c.Hostnames...), Isolation: c.Isolation}
			sv.Status = serviceStatus(c)
			if pv.State == StateActive && pv.Note != "" && c.DeploymentSHA != pv.ApprovedSHA {
				// Refused for its Dockerfile (deployAll), so no deployment
				// carries this commit: what runs keeps running, and with
				// nothing deployed no deployment will end it.
				switch {
				case sv.Status == "running":
					sv.Error = pv.Note
				case sv.Status == "starting" && c.DeploymentID == "":
					sv.Status, sv.Error = "failed", pv.Note
				}
			}
			v.Services = append(v.Services, sv)
		}
		if !pv.Untrusted {
			if sources, err := s.store.SourceServices(ctx, set.EnvironmentID); err == nil {
				for _, src := range sources {
					if !copied[src.ID] {
						v.Reached = append(v.Reached, src.Name)
					}
				}
			}
		}
	}
	v.Status = previewStatus(pv, v.Services)
	if v.Status == "blocked" {
		v.CanStart = pv.Note
	}
	return v, nil
}

// serviceStatus is a preview service's: starting, running, failed or
// stopped.
func serviceStatus(c CopyService) string {
	switch {
	case c.DeploymentStatus == "failed":
		// A refused deploy of a stopped one is a failure, with why.
		return "failed"
	case c.DeploymentStatus == "pending" || c.DeploymentStatus == "building":
		// Started again: its container is still stopped until it deploys.
		return "starting"
	case c.RuntimeStatus == "stopped":
		return "stopped"
	case c.DeploymentStatus == "running":
		return "running"
	}
	return "starting"
}

// previewStatus is what the panel shows: needs_approval, waiting_room,
// blocked (can't start, with why in its note), starting, updating,
// running, failed, stopped or removing.
func previewStatus(pv *Preview, services []ServiceView) string {
	switch pv.State {
	case StateRemoving:
		return "removing"
	case StateStopped:
		return "stopped"
	case StateWaitingApproval:
		if pv.Note != "" {
			return "blocked"
		}
		return "needs_approval"
	case StateWaitingRoom:
		if pv.Note != "" {
			return "blocked"
		}
		return "waiting_room"
	}
	statuses := make([]string, 0, len(services))
	for _, sv := range services {
		statuses = append(statuses, sv.Status)
	}
	switch {
	case slices.Contains(statuses, "starting"):
		for _, sv := range services {
			if sv.Status == "running" || sv.SHA != "" {
				return "updating"
			}
		}
		return "starting"
	case slices.Contains(statuses, "failed"):
		return "failed"
	case len(statuses) > 0 && !slices.ContainsFunc(statuses, func(st string) bool { return st != "running" }):
		return "running"
	case slices.Contains(statuses, "stopped"):
		return "stopped"
	}
	return "starting"
}

// pullFor returns a pull request's ID and its preview, which must exist.
func (s *Service) pullFor(ctx context.Context, projectID, repoID string, number int) (string, *Preview, error) {
	pullID, err := s.store.PullID(ctx, projectID, repoID, number)
	if err != nil {
		return "", nil, err
	}
	pv, err := s.store.ByPull(ctx, pullID)
	if err != nil {
		return "", nil, err
	}
	return pullID, pv, nil
}

// Approve lets an untrusted preview run sha, the pull request's head (an
// owner or developer, with a session: the handler checks).
func (s *Service) Approve(ctx context.Context, projectID, repoID string, number int, sha string, by Actor) (*View, error) {
	pullID, pv, err := s.pullFor(ctx, projectID, repoID, number)
	if err != nil {
		return nil, err
	}
	if pv == nil {
		return nil, ErrNotFound
	}
	if !pv.Untrusted {
		return nil, ErrNotUntrusted
	}
	if sha != pv.HeadSHA {
		return nil, ErrStaleSHA
	}
	s.mu.Lock()
	now := s.now().UTC()
	pv.ApprovedSHA, pv.ApprovedBy, pv.ApprovedAt = sha, by.UserID, &now
	if pv.State == StateStopped && pv.StoppedReason == StoppedForRoom {
		pv.State, pv.StoppedReason = StateActive, ""
	}
	err = s.store.Update(ctx, pv)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	pr, _ := s.store.Pull(ctx, pullID)
	set, _ := s.store.Settings(ctx, projectID)
	s.code.AddEvent(ctx, pullID, by.UserID, "preview_approved", map[string]any{"sha": sha})
	s.auditf(ctx, set.OrgID, by, "preview.approved", pr, map[string]any{"sha": sha})
	if err := s.reconcile(ctx, pullID, by, cause{kind: causeApprove}); err != nil {
		return nil, err
	}
	return s.Of(ctx, projectID, repoID, number)
}

// Start starts a pull request's preview: one it hasn't got (previews on),
// or one that is stopped.
func (s *Service) Start(ctx context.Context, projectID, repoID string, number int, by Actor) (*View, error) {
	pullID, pv, err := s.pullFor(ctx, projectID, repoID, number)
	if err != nil {
		return nil, err
	}
	pr, err := s.store.Pull(ctx, pullID)
	if err != nil {
		return nil, err
	}
	if pr.State != "open" {
		return nil, ErrPullNotOpen
	}
	set, err := s.store.Settings(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if set.EnvironmentID == "" {
		return nil, ErrOff
	}
	if s.agents != nil {
		if err := s.agents.Check(ctx, pactor.Target{Action: "deploy", Tool: "create_preview", ProjectID: projectID, Class: "preview"}); err != nil {
			return nil, err
		}
	}
	if pv != nil && pv.State == StateStopped {
		s.mu.Lock()
		pv.State, pv.StoppedReason = StateActive, ""
		err = s.store.Update(ctx, pv)
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	if err := s.reconcile(ctx, pullID, by, cause{kind: causeStart}); err != nil {
		return nil, err
	}
	return s.Of(ctx, projectID, repoID, number)
}

// Redeploy deploys a started preview's services again at the commit it
// runs (an untrusted one's approved commit).
func (s *Service) Redeploy(ctx context.Context, projectID, repoID string, number int, by Actor) (*View, error) {
	pullID, pv, err := s.pullFor(ctx, projectID, repoID, number)
	if err != nil {
		return nil, err
	}
	if pv == nil {
		return nil, ErrNotFound
	}
	if pv.EnvID == "" {
		return nil, ErrNotStarted
	}
	if err := s.reconcile(ctx, pullID, by, cause{kind: causeRedeploy}); err != nil {
		return nil, err
	}
	return s.Of(ctx, projectID, repoID, number)
}

// Stop stops a started preview's services; Start starts them again.
func (s *Service) Stop(ctx context.Context, projectID, repoID string, number int, by Actor) (*View, error) {
	pullID, pv, err := s.pullFor(ctx, projectID, repoID, number)
	if err != nil {
		return nil, err
	}
	if pv == nil {
		return nil, ErrNotFound
	}
	if pv.EnvID == "" {
		return nil, ErrNotStarted
	}
	if err := s.stop(ctx, pullID, StoppedByHand, "", by); err != nil {
		return nil, err
	}
	return s.Of(ctx, projectID, repoID, number)
}

// stop stops a preview's services and records why.
func (s *Service) stop(ctx context.Context, pullID, reason, note string, by Actor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pv, err := s.store.ByPull(ctx, pullID)
	if err != nil || pv == nil || pv.EnvID == "" {
		return err
	}
	copies, err := s.store.Copies(ctx, pv.EnvID)
	if err != nil {
		return err
	}
	for _, c := range copies {
		if c.RuntimeStatus == "stopped" {
			continue
		}
		if err := s.services.Stop(context.WithoutCancel(ctx), c.ID); err != nil {
			return err
		}
	}
	pv.State, pv.StoppedReason, pv.Note = StateStopped, reason, note
	if err := s.store.Update(ctx, pv); err != nil {
		return err
	}
	pr, err := s.store.Pull(ctx, pullID)
	if err != nil {
		return nil
	}
	set, _ := s.store.Settings(ctx, pr.ProjectID)
	s.code.AddEvent(ctx, pullID, by.UserID, "preview_stopped", map[string]any{"reason": reason, "note": note})
	s.auditf(ctx, set.OrgID, by, "preview.stopped", pr, map[string]any{"reason": reason, "note": note})
	return nil
}

// RoomCandidates are the running previews' services of orgID ("" every
// workspace's) on a server ("" the panel's own), oldest preview first.
func (s *Service) RoomCandidates(ctx context.Context, serverID, orgID string) ([]RoomCandidate, error) {
	return s.store.RoomCandidates(ctx, serverID, orgID)
}

// StopForRoom stops a preview so a deploy fits on its server (D-15): note
// says for what.
func (s *Service) StopForRoom(ctx context.Context, pullID, note string, by Actor) error {
	return s.stop(ctx, pullID, StoppedForRoom, note, by)
}

// RemoveForEnvironment removes the previews copying an environment that is
// being deleted.
func (s *Service) RemoveForEnvironment(ctx context.Context, environmentID string, by Actor) error {
	return s.removeAll(ctx, ScopeSource, environmentID, "the environment it copies was deleted", by)
}

// RemoveForRepository removes a repository's pull requests' previews.
func (s *Service) RemoveForRepository(ctx context.Context, repoID string, by Actor) error {
	return s.removeAll(ctx, ScopeRepository, repoID, "the repository was deleted", by)
}

func (s *Service) removeAll(ctx context.Context, scope Scope, id, why string, by Actor) error {
	pulls, err := s.store.PullsOf(ctx, scope, id)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range pulls {
		if err := s.reconcile(ctx, p, by, cause{kind: causeRemoved, why: why}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ApprovalsNeeded are a workspace's previews waiting for approval, for
// Needs you.
func (s *Service) ApprovalsNeeded(ctx context.Context, orgID string) ([]Approval, error) {
	return s.store.ApprovalsNeeded(ctx, orgID)
}
