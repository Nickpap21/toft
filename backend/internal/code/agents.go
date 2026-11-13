package code

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/actor"
	"go.uber.org/zap"
)

// Agents' code (D-102): an agent makes a branch with create_branch and gets
// a credential that pushes to it alone, over HTTPS; its pull requests take
// the untrusted path (D-97) since they are opened with ViaKey.

// AgentGate is the workspace's agent policy (D-100, D-102). Satisfied by
// *agent.Service.
type AgentGate interface {
	Check(ctx context.Context, t actor.Target) error
}

// WithAgents puts agents' branches, pull requests and comments under the
// policy, and audits their pushes.
func (s *Service) WithAgents(g AgentGate, audit AuditWriter) *Service {
	s.agents, s.audit = g, audit
	return s
}

func (s *Service) checkAgent(ctx context.Context, t actor.Target) error {
	if s.agents == nil {
		return nil
	}
	return s.agents.Check(ctx, t)
}

// AgentBranchPrefix starts an agent's push credential.
const AgentBranchPrefix = "toft_agt_"

// agentBranchTTL is how long a push credential works; create_branch on the
// same branch issues the next.
const agentBranchTTL = 24 * time.Hour

// AgentBranch is a branch an agent made, as the repository stores it.
type AgentBranch struct {
	RepoID, Name, ConnectionID, UserID, OrgID, AgentName string
	Paused                                               bool
	// UserName and ExpiresAt (its push password's) are AgentBranch's only.
	UserName  string
	ExpiresAt time.Time
}

// CreateBranchInput is POST …/repositories/{repositoryID}/branches's body:
// the branch, and where it starts (a branch, tag or commit; the default
// branch when empty).
type CreateBranchInput struct {
	Name string `json:"name" binding:"required,max=255"`
	From string `json:"from" binding:"max=255"`
	// ViaKey: made with an API key or by an agent (the handler sets it).
	ViaKey bool `json:"-"`
}

// CreatedBranch is a new branch, with an agent's push credential.
type CreatedBranch struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
	// Push is set for an agent: where and how it pushes to this branch.
	Push *AgentPush `json:"push"`
}

// AgentPush is an agent's way to push to its branch: Basic auth with
// Username and Password at URL, until ExpiresAt.
type AgentPush struct {
	URL       string    `json:"url"`
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expires_at"`
	Command   string    `json:"command"`
}

var (
	ErrBranchExists   = errors.New("a branch by that name exists: choose another")
	ErrBranchDeployed = errors.New("a service deploys from a branch by that name, so an agent can't make or push to it")
	ErrNotAgentBranch = errors.New("that branch is another agent's or a person's: an agent pushes only to branches it created")
	ErrAgentsPaused   = errors.New("agents are paused, in this workspace or on this panel: an owner resumes them under Settings → Agents")
)

// CreateBranch makes a branch (D-102). For an agent it also issues the
// credential that pushes to it: on a branch it made before, a new one, and
// no new branch.
func (s *Service) CreateBranch(ctx context.Context, projectID, repoID, userID string, in CreateBranchInput) (*CreatedBranch, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	if r.Mirrored {
		return nil, ErrMirrorReadOnly
	}
	name := strings.TrimSpace(in.Name)
	if !gitstore.ValidRef(name) || name == "HEAD" {
		return nil, ErrBadName
	}
	if deployed, err := s.repo.BranchDeployed(ctx, r.ID, name); err != nil {
		return nil, err
	} else if deployed {
		return nil, ErrBranchDeployed
	}
	a, isAgent := actor.AgentFrom(ctx)
	var made gitstore.CreatedBranch
	mine := false
	if isAgent {
		if b, err := s.repo.AgentBranch(ctx, r.ID, name); err == nil {
			if b.ConnectionID != a.ConnectionID {
				return nil, ErrNotAgentBranch
			}
			mine = true
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		// A new password for a branch the agent made needs no person: the
		// branch was allowed when it was made.
		if !mine {
			if err := s.checkAgent(ctx, actor.Target{Action: "code", Tool: "create_branch", ProjectID: projectID}); err != nil {
				return nil, err
			}
		}
	}
	if mine {
		var page gitstore.Page[gitstore.Commit]
		if err := s.git.Browse(ctx, r.ID, "commits", map[string][]string{"ref": {name}, "limit": {"1"}}, &page); err != nil {
			return nil, err
		}
		made.Branch = name
		if len(page.Items) > 0 {
			made.SHA = page.Items[0].SHA
		}
	} else {
		from := strings.TrimSpace(in.From)
		if from == "" {
			var sum gitstore.Summary
			if err := s.git.Browse(ctx, r.ID, "summary", nil, &sum); err != nil {
				return nil, err
			}
			from = sum.DefaultBranch
		}
		if err := s.git.Write(ctx, r.ID, "create-branch", gitstore.CreateBranchRequest{Branch: name, From: from}, &made); err != nil {
			var be *gitstore.BrowseError
			if errors.As(err, &be) && be.Code == "branch_exists" {
				return nil, ErrBranchExists
			}
			return nil, err
		}
		// A push that made the branch, as a git push making it is: one made
		// with a key makes a pull request from it untrusted (D-97).
		if made.SHA != "" {
			s.recordMoves(ctx, r.ID, userID, "push", in.ViaKey, []gitstore.RefUpdate{
				{Ref: "refs/heads/" + made.Branch, Old: strings.Repeat("0", len(made.SHA)), New: made.SHA}}, a.ConnectionID)
		}
	}
	out := &CreatedBranch{Branch: made.Branch, SHA: made.SHA}
	if !isAgent {
		return out, nil
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token := AgentBranchPrefix + base64.RawURLEncoding.EncodeToString(raw)
	exp := time.Now().Add(agentBranchTTL)
	if err := s.repo.UpsertAgentBranch(ctx, r.ID, name, a.ConnectionID, userID, hashCredential(token), exp); err != nil {
		return nil, err
	}
	url := "https://" + s.host() + "/" + r.OrgSlug + "/" + r.Name + ".git"
	out.Push = &AgentPush{URL: url, Username: "agent", Password: token, ExpiresAt: exp,
		Command: "git push https://agent:" + token + "@" + strings.TrimPrefix(url, "https://") + " HEAD:" + name}
	return out, nil
}

func hashCredential(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// AgentCredential reads an agent's push credential: the branch it pushes
// to and who acts, or ErrNotFound when it isn't live.
func (s *Service) AgentCredential(ctx context.Context, raw string) (*AgentBranch, error) {
	if !strings.HasPrefix(raw, AgentBranchPrefix) {
		return nil, ErrNotFound
	}
	return s.repo.AgentBranchByToken(ctx, hashCredential(raw))
}

// checkAgentPush refuses an agent's push its credential no longer allows:
// a paused workspace or instance, a branch that is the default one or that a service
// now deploys from (D-102).
func (s *Service) checkAgentPush(ctx context.Context, t *Target, a Actor) error {
	if a.Paused {
		return ErrAgentsPaused
	}
	deployed, err := s.repo.BranchDeployed(ctx, t.ID, a.PushOnly)
	if err != nil {
		return err
	}
	if deployed {
		return ErrBranchDeployed
	}
	var sum gitstore.Summary
	if err := s.git.Browse(ctx, t.ID, "summary", nil, &sum); err != nil {
		return err
	}
	if sum.DefaultBranch == a.PushOnly {
		return ErrNotAgentBranch
	}
	return nil
}

// auditPush records an agent's push as the agent acting for its person.
func (s *Service) auditPush(ctx context.Context, t *Target, a Actor, updates []gitstore.RefUpdate) {
	if s.audit == nil || a.AgentConnectionID == "" {
		return
	}
	for _, u := range updates {
		ctx := actor.WithAgent(ctx, actor.Agent{ConnectionID: a.AgentConnectionID, Name: a.AgentName, UserID: a.UserID, OrgID: t.OrgID})
		s.audit.WriteAuditLogDetail(ctx, t.OrgID, a.UserID, "agent.pushed", "repository:"+t.ID,
			map[string]any{"branch": strings.TrimPrefix(u.Ref, "refs/heads/"), "before": u.Old, "after": u.New})
	}
	s.log.Info("agent pushed", zap.String("repository_id", t.ID), zap.String("branch", a.PushOnly))
}
