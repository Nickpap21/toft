package code

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/internal/user"
	"github.com/Nickpap21/toft/backend/pkg/broker"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/Nickpap21/toft/backend/pkg/middleware"
	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"go.uber.org/zap"
)

// Members reads an account's state and its role in a workspace in one
// query, and an account's name and email (a merge's author and committer).
// Satisfied by *user.Service.
type Members interface {
	RequestIdentity(ctx context.Context, userID, orgID, sessionHash string) (middleware.Identity, error)
	GetByID(ctx context.Context, id string) (*user.User, error)
}

// Service is the code module's use cases.
type Service struct {
	repo    Repository
	git     *gitstore.Client // nil: git hosting is off (TOFT_GIT=off)
	members Members
	// host is the panel's address in clone URLs; sshPort the published
	// SSH port (TOFT_GIT_SSH_PORT).
	host    func() string
	sshPort int
	// fingerprint is the SSH host key's, for GET /git.
	fingerprint string
	log         *zap.Logger

	// ponytail: an in-process lock, since this backend is the only writer of
	// repositories; an advisory lock if two backends ever run at once.
	naming sync.Mutex
	// keyUsed is key ID → when last_used_at was last written.
	keyUsed sync.Map
	// consumer deploys pushes (D-77); wake starts the dispatcher at once.
	consumer PushConsumer
	wake     chan struct{}
	// issues links pull requests and branches to issues (D-81); nil
	// without the issues module.
	issues Issues
	docs   Docs
	// previews follow pull requests (D-97); nil without them.
	previews Previews

	// Import, mirrors and webhooks (D-79): egress reaches other hosts for
	// git, keys seal their credentials (ENCRYPTION_KEY), hookClient
	// posts webhooks to public addresses only; the wakes start their
	// workers at once, and syncing holds each running sync's cancel.
	egress     *gitstore.Egress
	keys       *crypto.Keys
	hookClient *http.Client
	mirrorWake chan struct{}
	hookWake   chan struct{}
	syncing    sync.Map
	// agents is the agent policy (D-102); audit records agents' pushes.
	agents AgentGate
	audit  AuditWriter
	// pub tells the workspace's tabs when its pull requests change; nil
	// tells no one.
	pub Publisher
}

// Publisher tells a workspace's open tabs. Satisfied by *broker.Broker.
type Publisher interface {
	Publish(orgID string, e broker.Event)
}

// WithPublisher tells the workspace's tabs when a pull request is opened,
// closed, reopened or merged, so the open pull request counts refresh.
func (s *Service) WithPublisher(p Publisher) *Service {
	s.pub = p
	return s
}

// pullsChanged tells r's workspace that projectID's open pull requests
// changed. Call it once the change has committed.
func (s *Service) pullsChanged(r *StoredRepo, projectID string) {
	if s.pub != nil {
		s.pub.Publish(r.OrgID, broker.Changed(projectID, "pulls"))
	}
}

// NewService serves code with git as the executor's client, nil when git
// hosting is off.
func NewService(repo Repository, git *gitstore.Client, members Members, host func() string, sshPort int, log *zap.Logger) *Service {
	return &Service{repo: repo, git: git, members: members, host: host, sshPort: sshPort, log: log, wake: make(chan struct{}, 1),
		hookClient: netcheck.NewPublicOnlyClient(10*time.Second, false), mirrorWake: make(chan struct{}, 1), hookWake: make(chan struct{}, 1)}
}

// WithHostKey sets the SSH host key's fingerprint GET /git shows.
func (s *Service) WithHostKey(fingerprint string) *Service {
	s.fingerprint = fingerprint
	return s
}

// Enabled says whether this instance hosts git.
func (s *Service) Enabled() bool { return s.git != nil }

// Info is GET /git; requestHost is the address the client reached the API
// on, used while the panel has no domain.
func (s *Service) Info(requestHost string) GitInfo {
	if !s.Enabled() {
		return GitInfo{}
	}
	return GitInfo{Enabled: true, SSHHost: s.hostFor(requestHost), SSHPort: s.sshPort, HostKeyFingerprint: s.fingerprint}
}

// hostFor is the panel's domain, or without one the host the client used.
func (s *Service) hostFor(requestHost string) string {
	if h := s.host(); h != "" {
		return h
	}
	if h, _, err := net.SplitHostPort(requestHost); err == nil {
		return h
	}
	return requestHost
}

// EnsureRepositories gives every project without a repository one, named
// from the project, and each workspace that needs one its slug. It runs at
// the backend's start (projects from before git hosting, or made by an
// older release after a rollback) and after a project is created. The bare
// repository is made on its first use.
func (s *Service) EnsureRepositories(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	s.naming.Lock()
	defer s.naming.Unlock()
	unnamed, err := s.repo.Unnamed(ctx)
	if err != nil || len(unnamed) == 0 {
		return err
	}
	slugs := map[string]string{}
	var orgIDs []string
	for _, u := range unnamed {
		if !slices.Contains(orgIDs, u.OrgID) {
			orgIDs = append(orgIDs, u.OrgID)
		}
		if u.OrgSlug == "" {
			slugs[u.OrgID] = ""
		}
	}
	if len(slugs) > 0 {
		takenSlugs, err := s.repo.TakenSlugs(ctx)
		if err != nil {
			return err
		}
		taken := map[string]bool{}
		for _, t := range takenSlugs {
			taken[t] = true
		}
		for _, u := range unnamed {
			if slugs[u.OrgID] == "" && u.OrgSlug == "" {
				slugs[u.OrgID] = freeName(slugify(u.OrgName, maxOrgSlug, "workspace"), maxOrgSlug, taken)
			}
		}
	}
	names, err := s.repo.TakenNames(ctx, orgIDs)
	if err != nil {
		return err
	}
	taken := map[string]map[string]bool{}
	for _, id := range orgIDs {
		taken[id] = map[string]bool{}
		for _, n := range names[id] {
			taken[id][n] = true
		}
	}
	repos := make([]NewRepo, 0, len(unnamed))
	for _, u := range unnamed {
		name := freeName(slugify(u.ProjectName, maxRepoName, "code"), maxRepoName, taken[u.OrgID])
		repos = append(repos, NewRepo{OrgID: u.OrgID, ProjectID: u.ProjectID, Name: name})
	}
	if err := s.repo.Name(ctx, slugs, repos); err != nil {
		return fmt.Errorf("name repositories: %w", err)
	}
	return nil
}

// ProjectRepositories lists a project's repositories with their URLs. The
// caller has checked the project is in the caller's workspace. A project
// whose repository was not made with it (an error then, or an older
// release after a rollback) gets it now.
func (s *Service) ProjectRepositories(ctx context.Context, projectID, requestHost string) ([]Repo, error) {
	if !s.Enabled() {
		return nil, ErrGitOff
	}
	stored, err := s.repo.ListProjectRepos(ctx, projectID)
	if err == nil && len(stored) == 0 {
		if err = s.EnsureRepositories(ctx); err == nil {
			stored, err = s.repo.ListProjectRepos(ctx, projectID)
		}
	}
	if err != nil {
		return nil, err
	}
	pulls, err := s.pullMirrors(ctx, projectID, stored)
	if err != nil {
		return nil, err
	}
	out := make([]Repo, 0, len(stored))
	host := s.hostFor(requestHost)
	for _, r := range stored {
		repo := s.toRepo(r, host)
		repo.PullMirror = pulls[r.ID]
		out = append(out, repo)
	}
	return out, nil
}

// pullMirrors is the pull mirrors of stored's mirrored repositories, by
// repository ID, read once for the project.
func (s *Service) pullMirrors(ctx context.Context, projectID string, stored []StoredRepo) (map[string]*Mirror, error) {
	out := map[string]*Mirror{}
	if !slices.ContainsFunc(stored, func(r StoredRepo) bool { return r.Mirrored }) {
		return out, nil
	}
	ms, err := s.repo.ProjectPullMirrors(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		out[m.RepoID] = &m.Mirror
	}
	return out, nil
}

func (s *Service) toRepo(r StoredRepo, host string) Repo {
	full := r.OrgSlug + "/" + r.Name
	return Repo{
		ID:                   r.ID,
		Name:                 r.Name,
		FullName:             full,
		SSHURL:               "ssh://git@" + host + ":" + strconv.Itoa(s.sshPort) + "/" + full + ".git",
		HTTPSURL:             "https://" + host + "/" + full + ".git",
		CreatedAt:            r.CreatedAt,
		ProtectDefaultBranch: r.Protect,
		OpenPulls:            r.OpenPulls,
	}
}

// repoOf is the project's repository repoID, or ErrNotFound. The caller
// has checked the project is in the caller's workspace.
func (s *Service) repoOf(ctx context.Context, projectID, repoID string) (*StoredRepo, error) {
	if !s.Enabled() {
		return nil, ErrGitOff
	}
	stored, err := s.repo.ListProjectRepos(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, r := range stored {
		if r.ID == repoID {
			return &r, nil
		}
	}
	return nil, ErrNotFound
}

// Repository is one of the project's repositories with what the browser
// opens on: its default branch, whether it is empty, its branch and tag
// counts.
func (s *Service) Repository(ctx context.Context, projectID, repoID, requestHost string) (*RepositoryDetail, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	d := &RepositoryDetail{Repo: s.toRepo(*r, s.hostFor(requestHost))}
	if r.Protect && r.ProtectedBranch != "" {
		d.ProtectedBranch = &r.ProtectedBranch
	}
	if r.Mirrored {
		pulls, err := s.pullMirrors(ctx, projectID, []StoredRepo{*r})
		if err != nil {
			return nil, err
		}
		d.PullMirror = pulls[r.ID]
	}
	if err := s.git.Browse(ctx, r.ID, "summary", nil, &d.Summary); err != nil {
		return nil, err
	}
	counts, err := s.repo.CountPulls(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	d.Pulls = PullCounts{Open: counts["open"], Merged: counts["merged"], Closed: counts["closed"]}
	if d.Deploys, err = s.deployTargets(ctx, r.ID, "", nil); err != nil {
		return nil, err
	}
	if d.AgentBranches, err = s.repo.AgentBranches(ctx, r.ID); err != nil {
		return nil, err
	}
	return d, nil
}

// Browse runs one of the browser's reads (gitstore.Client.Browse) on the
// project's repository, its answer as the executor wrote it.
func (s *Service) Browse(ctx context.Context, projectID, repoID, op string, q url.Values) (json.RawMessage, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	if err := s.git.Browse(ctx, r.ID, op, q, &out); err != nil {
		return nil, err
	}
	return s.withChecks(ctx, r.ID, op, out)
}

// Raw is a file's bytes from the project's repository, and their size.
func (s *Service) Raw(ctx context.Context, projectID, repoID string, q url.Values) (io.ReadCloser, int64, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, 0, err
	}
	return s.git.Raw(ctx, r.ID, q)
}

// DeleteProjectRepositories removes a project's repositories from disk,
// before the project's row (and with it theirs) is deleted. With git off
// the rows go and the disk is left as it is.
func (s *Service) DeleteProjectRepositories(ctx context.Context, projectID string) error {
	if !s.Enabled() {
		return nil
	}
	stored, err := s.repo.ListProjectRepos(ctx, projectID)
	if err != nil {
		return err
	}
	for _, r := range stored {
		if err := s.git.Delete(ctx, r.ID); err != nil {
			return fmt.Errorf("delete repository %s: %w", r.Name, err)
		}
	}
	return nil
}

// Resolve finds the repository a client named by "<workspace>/<name>", or
// ErrNotFound.
func (s *Service) Resolve(ctx context.Context, path string) (*Target, error) {
	if !s.Enabled() {
		return nil, ErrNotFound
	}
	ws, name, ok := SplitPath(path)
	if !ok {
		return nil, ErrNotFound
	}
	return s.repo.RepoByPath(ctx, ws, name)
}

// Authorize says whether a may read t, or with write push to it. Every
// member of the repository's workspace reads; owners and developers push;
// a project-scoped API key needs its project and code_read or code_write.
// A repository a can't read is ErrNotFound, so its existence doesn't leak;
// one a may read but not push to is ErrForbidden.
func (s *Service) Authorize(ctx context.Context, a Actor, t *Target, write bool) error {
	id, err := s.members.RequestIdentity(ctx, a.UserID, t.OrgID, "")
	if err != nil || !id.Active || id.Role == "" {
		return ErrNotFound
	}
	if a.ProjectID != "" {
		if a.ProjectID != t.ProjectID || !(slices.Contains(a.Scopes, ScopeCodeRead) || slices.Contains(a.Scopes, ScopeCodeWrite)) {
			return ErrNotFound
		}
		if write && !slices.Contains(a.Scopes, ScopeCodeWrite) {
			return ErrForbidden
		}
	}
	if write && id.Role != "owner" && id.Role != "developer" {
		return ErrForbidden
	}
	return nil
}

// Serve runs git's service for an authorized client (gitstore.Client.Serve),
// refusing pushes to a protected default branch; after a push it records
// the branches the push moved in the events outbox (recordPushes), which
// deploys them, and moves the pull requests on them (afterPush).
func (s *Service) Serve(ctx context.Context, t *Target, actor Actor, service, mode, protocol string, stdin io.Reader, stdout io.Writer) (gitstore.Result, error) {
	if !s.Enabled() {
		return gitstore.Result{}, ErrGitOff
	}
	// On the advertisement too: git shows a refusal only from that first
	// request; the push itself checks again.
	if actor.PushOnly != "" && service == "receive-pack" {
		if err := s.checkAgentPush(ctx, t, actor); err != nil {
			return gitstore.Result{}, err
		}
	}
	// A name with protection off is the previous release's turning it off.
	protect := ""
	if t.ProtectDefault {
		protect = t.ProtectedBranch
	}
	if t.ProtectDefault && protect == "" && service == "receive-pack" {
		// Protected before 0080, or while it had no branch: HEAD's name, as
		// before, stored once a branch exists. HEAD moves only through an
		// owner with the password, which stores the name too.
		var sum gitstore.Summary
		if err := s.git.Browse(ctx, t.ID, "summary", nil, &sum); err != nil {
			return gitstore.Result{}, fmt.Errorf("the protected branch: %w", err)
		}
		protect = sum.DefaultBranch
		if !sum.Empty {
			if err := s.repo.FillProtectedBranch(ctx, t.ID, protect); err != nil {
				s.log.Warn("store the protected branch", zap.Error(err), zap.String("repository_id", t.ID))
			}
		}
	}
	// An agent's push credential counts as the account that issued it.
	res, err := s.git.Serve(ctx, t.ID, service, mode, protocol, protect, t.MirrorURL, actor.PushOnly, actor.UserID, t.OrgID, stdin, stdout)
	if err == nil && len(res.Updated) > 0 {
		s.recordPushes(context.WithoutCancel(ctx), t.ID, actor, res.Updated)
		s.afterPush(context.WithoutCancel(ctx), t, actor, res.Updated)
		s.auditPush(context.WithoutCancel(ctx), t, actor, res.Updated)
	}
	return res, err
}

// ─── SSH keys ────────────────────────────────────────────────────────────────

const maxKeys = 50

func (s *Service) ListKeys(ctx context.Context, userID string) ([]SSHKey, error) {
	if !s.Enabled() {
		return nil, ErrGitOff
	}
	return s.repo.ListKeys(ctx, userID)
}

func (s *Service) AddKey(ctx context.Context, userID string, in AddSSHKeyInput) (*SSHKey, error) {
	if !s.Enabled() {
		return nil, ErrGitOff
	}
	normalized, fingerprint, keyType, err := parseKey(in.PublicKey)
	if err != nil {
		return nil, err
	}
	if n, err := s.repo.CountKeys(ctx, userID); err != nil {
		return nil, err
	} else if n >= maxKeys {
		return nil, ErrTooManyKeys
	}
	return s.repo.AddKey(ctx, userID, in.Title, normalized, fingerprint, keyType)
}

func (s *Service) DeleteKey(ctx context.Context, userID, id string) (*SSHKey, error) {
	if !s.Enabled() {
		return nil, ErrGitOff
	}
	return s.repo.DeleteKey(ctx, userID, id)
}

// keyUsedEvery is how often a key's last_used_at is written at most.
const keyUsedEvery = time.Minute

// KeyOwner is the account an SSH key with this fingerprint belongs to, if
// that account may sign in; it notes the key's use.
func (s *Service) KeyOwner(ctx context.Context, fingerprint string) (string, error) {
	keyID, userID, err := s.repo.KeyOwner(ctx, fingerprint)
	if err != nil {
		return "", err
	}
	now := time.Now()
	if last, ok := s.keyUsed.Load(keyID); !ok || now.Sub(last.(time.Time)) >= keyUsedEvery {
		s.keyUsed.Store(keyID, now)
		go func() {
			if err := s.repo.TouchKey(context.WithoutCancel(ctx), keyID); err != nil {
				s.log.Warn("ssh key last_used_at", zap.Error(err), zap.String("key_id", keyID))
			}
		}()
	}
	return userID, nil
}

// Previews is what the code module tells the previews module (D-97).
// Satisfied by *preview.Service via main.go.
type Previews interface {
	// Nudge: an event was written.
	Nudge()
	RemoveForRepository(ctx context.Context, repoID string) error
}

// WithPreviews lets pull requests' events move their previews.
func (s *Service) WithPreviews(p Previews) { s.previews = p }
