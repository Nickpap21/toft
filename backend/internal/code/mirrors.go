package code

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Import and mirrors (D-79). An import is a pull mirror's first sync into an
// empty repository; with keep it then pulls every 10 minutes and the
// repository is read-only. A push mirror force-pushes every branch and tag
// seconds after each change. Every sync runs git in paas-git through the
// backend's egress (gitstore.Egress), which holds the remote's URL and
// credentials for that one operation.

const (
	pullEvery      = 10 * time.Minute
	maxPushMirrors = 5
	mirrorWorkers  = 4 // syncs at once, so one slow host doesn't hold up the rest
	mirrorTick     = 15 * time.Second
	maxBackoff     = time.Hour
	// MirrorFailingAfter is how many failed syncs in a row raise the
	// mirror_failing alert.
	MirrorFailingAfter = 3
)

var (
	ErrBadRemote      = errors.New("the URL must be an https:// clone URL with a path, and no user, query or fragment")
	ErrBadUsername    = errors.New("the username has a character a username can't have")
	ErrPrivateAddress = errors.New("the URL leads to an address that isn't public, which Toft doesn't reach")
	ErrNotEmpty       = errors.New("only an empty repository can be imported into")
	ErrImportRunning  = errors.New("this repository is being imported, or pulls from another host already")
	ErrMirrorLimit    = errors.New("a repository has 5 push mirrors at most")
	ErrSyncRunning    = errors.New("a sync is running; it ends on its own")
	ErrMirrorReadOnly = errors.New("this repository pulls from another host, so it's read-only here: push there, or stop mirroring in its settings")
	ErrNotYourMirror  = errors.New("only owners remove a mirror; owners and developers cancel an import that hasn't finished")
	ErrEgressOff      = errors.New("this panel can't reach other hosts")
)

// Mirror is a pull or push mirror as the API shows it.
type Mirror struct {
	ID           string        `json:"id"`
	Direction    string        `json:"direction"`
	URL          string        `json:"url"`
	Username     string        `json:"username"`
	HasPassword  bool          `json:"has_password"`
	Keep         bool          `json:"keep"`
	State        string        `json:"state"`
	CreatedAt    time.Time     `json:"created_at"`
	CreatedBy    *Person       `json:"created_by"`
	SyncingSince *time.Time    `json:"syncing_since"`
	LastSyncAt   *time.Time    `json:"last_sync_at"`
	LastOKAt     *time.Time    `json:"last_ok_at"`
	NextSyncAt   *time.Time    `json:"next_sync_at"`
	Failures     int           `json:"failures"`
	Error        *string       `json:"error"`
	LastMoved    *int          `json:"last_moved"`
	Remote       *MirrorRemote `json:"remote"`
}

// MirrorRemote is what a pull mirror last found on the remote.
type MirrorRemote struct {
	DefaultBranch string `json:"default_branch"`
	Branches      int    `json:"branches"`
	Tags          int    `json:"tags"`
}

// ImportInput is POST …/import's body.
type ImportInput struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	Keep     bool   `json:"keep"`
}

// MirrorInput is POST …/mirrors' body: a push mirror.
type MirrorInput struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// StoredMirror is a mirror row; the password stays in the database.
type StoredMirror struct {
	Mirror
	RepoID string
}

// NewMirror is a mirror to store.
type NewMirror struct {
	ID, RepoID, Direction, URL, Username, PasswordEnc, CreatedBy string
	Keep                                                         bool
}

// ClaimedMirror is a mirror the worker is to sync now.
type ClaimedMirror struct {
	ID, RepoID, OrgID, Direction, URL, Username, PasswordEnc, CreatedBy string
	Keep, Imported                                                      bool
	Failures                                                            int
}

// MirrorResult is how a sync ended: Err "" when it succeeded, then Moved
// refs; RetryAt when to try a failed one again (zero: wait for Sync now).
type MirrorResult struct {
	Err     string
	Moved   int
	RetryAt time.Time
}

// MirrorHealth is a mirror for the mirror_failing alert.
type MirrorHealth struct {
	MirrorID string
	Failing  bool
	Detail   string
}

// state is a mirror's state, in the API's words.
func (m *Mirror) state() string {
	switch {
	case m.Direction == "pull" && m.LastOKAt == nil && m.SyncingSince == nil && m.Failures > 0:
		return "failed"
	case m.Direction == "pull" && m.LastOKAt == nil:
		return "importing"
	case m.SyncingSince != nil:
		return "syncing"
	case m.Failures > 0:
		return "failing"
	case m.LastOKAt == nil:
		return "pending"
	}
	return "ok"
}

// WithEgress lets the service reach other hosts (import, mirrors and
// webhooks), keeping credentials encrypted with key.
func (s *Service) WithEgress(e *gitstore.Egress, keys *crypto.Keys) *Service {
	s.egress, s.keys = e, keys
	return s
}

func (s *Service) nudgeMirrors() {
	select {
	case s.mirrorWake <- struct{}{}:
	default:
	}
}

// checkRemote is a mirror's or an import's URL, cleaned, or why it can't
// be one: https, a host and a path, no user, query or fragment, and a
// literal address that is public (a name is checked when connecting).
func checkRemote(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "?#@ \t\r\n") || strings.Trim(u.Path, "/") == "" {
		return "", ErrBadRemote
	}
	if netcheck.CheckHost(u.Hostname()) != nil {
		return "", ErrPrivateAddress
	}
	return strings.TrimRight(raw, "/"), nil
}

func checkUsername(s string) error {
	if len(s) > 255 || strings.ContainsRune(s, ':') || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return ErrBadUsername
	}
	return nil
}

// mirrorPasswordAD is where a mirror's password is sealed for (D-126).
func mirrorPasswordAD(id string) string {
	if u, err := uuid.Parse(id); err == nil {
		id = u.String()
	}
	return crypto.AD("repository_mirrors.password_encrypted", id)
}

func (s *Service) encryptOpt(mirrorID, plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if strings.IndexFunc(plain, unicode.IsControl) >= 0 || len(plain) > 4096 {
		return "", ErrBadUsername
	}
	return s.keys.Seal(mirrorPasswordAD(mirrorID), plain)
}

// Import starts copying a remote repository into the project's empty
// repository repoID (owners and developers); with keep it then pulls as a
// pull mirror. It returns at once: the worker runs it.
func (s *Service) Import(ctx context.Context, projectID, repoID, actorID string, in ImportInput) (*Mirror, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	if s.egress == nil {
		return nil, ErrEgressOff
	}
	remote, err := checkRemote(in.URL)
	if err != nil {
		return nil, err
	}
	if err := checkUsername(in.Username); err != nil {
		return nil, err
	}
	var sum gitstore.Summary
	if err := s.git.Browse(ctx, r.ID, "summary", nil, &sum); err != nil {
		return nil, err
	}
	if !sum.Empty {
		return nil, ErrNotEmpty
	}
	// The ID is made here, so the password is sealed for it (D-126).
	id := uuid.NewString()
	enc, err := s.encryptOpt(id, in.Password)
	if err != nil {
		return nil, err
	}
	id, err = s.repo.CreateMirror(ctx, NewMirror{ID: id, RepoID: r.ID, Direction: "pull", URL: remote, Username: in.Username,
		PasswordEnc: enc, CreatedBy: actorID, Keep: in.Keep})
	if err != nil {
		return nil, err
	}
	s.nudgeMirrors()
	return s.mirror(ctx, r.ID, id)
}

// AddPushMirror adds a push mirror to the project's repository repoID
// (owners, with the password), pushed to at once.
func (s *Service) AddPushMirror(ctx context.Context, projectID, repoID, actorID string, in MirrorInput) (*Mirror, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	if s.egress == nil {
		return nil, ErrEgressOff
	}
	remote, err := checkRemote(in.URL)
	if err != nil {
		return nil, err
	}
	if err := checkUsername(in.Username); err != nil {
		return nil, err
	}
	if n, err := s.repo.CountPushMirrors(ctx, r.ID); err != nil {
		return nil, err
	} else if n >= maxPushMirrors {
		return nil, ErrMirrorLimit
	}
	// The ID is made here, so the password is sealed for it (D-126).
	id := uuid.NewString()
	enc, err := s.encryptOpt(id, in.Password)
	if err != nil {
		return nil, err
	}
	id, err = s.repo.CreateMirror(ctx, NewMirror{ID: id, RepoID: r.ID, Direction: "push", URL: remote, Username: in.Username,
		PasswordEnc: enc, CreatedBy: actorID, Keep: true})
	if err != nil {
		return nil, err
	}
	s.nudgeMirrors()
	return s.mirror(ctx, r.ID, id)
}

// Mirrors lists the repository's mirrors, the pull mirror first.
func (s *Service) Mirrors(ctx context.Context, projectID, repoID string) ([]Mirror, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	stored, err := s.repo.ListMirrors(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Mirror, 0, len(stored))
	for _, m := range stored {
		out = append(out, m.Mirror)
	}
	return out, nil
}

func (s *Service) mirror(ctx context.Context, repoID, id string) (*Mirror, error) {
	stored, err := s.repo.ListMirrors(ctx, repoID)
	if err != nil {
		return nil, err
	}
	for _, m := range stored {
		if m.ID == id {
			return &m.Mirror, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteMirror removes a push mirror, stops a pull mirror or cancels an
// import, stopping a sync that runs; owners, or anyone who writes for an
// import that hasn't finished. It returns what it removed.
func (s *Service) DeleteMirror(ctx context.Context, projectID, repoID, id string, owner bool) (*Mirror, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	m, err := s.mirror(ctx, r.ID, id)
	if err != nil {
		return nil, err
	}
	if !owner && (m.Direction != "pull" || m.LastOKAt != nil) {
		return nil, ErrNotYourMirror
	}
	if cancel, ok := s.syncing.Load(id); ok {
		cancel.(context.CancelFunc)()
	}
	if err := s.repo.DeleteMirror(ctx, r.ID, id); err != nil {
		return nil, err
	}
	return m, nil
}

// SyncMirror makes a mirror sync now (an import that failed tries again).
func (s *Service) SyncMirror(ctx context.Context, projectID, repoID, id string) (*Mirror, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	if _, err := s.mirror(ctx, r.ID, id); err != nil {
		return nil, err
	}
	ok, err := s.repo.RequestSync(ctx, r.ID, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSyncRunning
	}
	s.nudgeMirrors()
	return s.mirror(ctx, r.ID, id)
}

// FailingMirrors is every mirror, failing when its last 3 syncs failed;
// alert.Monitor reads it every minute.
func (s *Service) FailingMirrors(ctx context.Context) ([]MirrorHealth, error) {
	return s.repo.MirrorFailures(ctx)
}

// RunMirrors syncs what is due until ctx ends, mirrorWorkers at once: at
// once when a mirror is added or asked to sync, and every 15 s.
func (s *Service) RunMirrors(ctx context.Context) {
	if !s.Enabled() || s.egress == nil {
		return
	}
	if err := s.repo.ResetSyncing(ctx); err != nil {
		s.log.Error("mirrors: reset syncs left running", zap.Error(err))
	}
	slots := make(chan struct{}, mirrorWorkers)
	var wg sync.WaitGroup
	defer wg.Wait()
	tick := time.NewTicker(mirrorTick)
	defer tick.Stop()
	for {
		if free := mirrorWorkers - len(slots); free > 0 {
			ms, err := s.repo.ClaimMirrors(ctx, free)
			if err != nil && ctx.Err() == nil {
				s.log.Error("mirrors: claim", zap.Error(err))
			}
			for _, m := range ms {
				slots <- struct{}{}
				wg.Add(1)
				go func() {
					defer wg.Done()
					s.syncMirror(ctx, m)
					<-slots
					s.nudgeMirrors()
				}()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.mirrorWake:
		case <-tick.C:
		}
	}
}

// syncMirror runs one sync and records how it ended.
func (s *Service) syncMirror(ctx context.Context, m ClaimedMirror) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.syncing.Store(m.ID, cancel)
	defer s.syncing.Delete(m.ID)
	moved, err := s.runSync(ctx, m)
	if ctx.Err() != nil {
		// Removed meanwhile, or the panel is stopping: the next start
		// syncs it again (ResetSyncing).
		return
	}
	res := MirrorResult{Moved: moved}
	if err != nil {
		res.Err = err.Error()
		if m.Direction == "push" || !m.Imported {
			wait := min(time.Minute<<min(m.Failures, 6), maxBackoff)
			if m.Direction == "pull" {
				wait = max(wait, pullEvery)
			}
			res.RetryAt = time.Now().Add(wait)
		}
		s.log.Warn("mirror sync failed", zap.String("mirror_id", m.ID), zap.String("direction", m.Direction), zap.String("error", res.Err))
	}
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer fcancel()
	if _, err := s.repo.FinishMirror(fctx, m.ID, res); err != nil {
		s.log.Error("mirrors: record a sync", zap.Error(err), zap.String("mirror_id", m.ID))
	}
	if err == nil && m.Direction == "pull" && !m.Keep {
		s.log.Info("repository imported", zap.String("repository_id", m.RepoID), zap.Int("refs", moved))
	}
}

// runSync is one pull (ls, then fetch) or push through the egress; it
// returns how many refs moved, or an error in words.
func (s *Service) runSync(ctx context.Context, m ClaimedMirror) (int, error) {
	password := ""
	if m.PasswordEnc != "" {
		p, err := s.keys.Open(mirrorPasswordAD(m.ID), m.PasswordEnc)
		if err != nil {
			return 0, errors.New("the stored token can't be read: remove the mirror and add it again")
		}
		password = p
	}
	service := "git-upload-pack"
	if m.Direction == "push" {
		service = "git-receive-pack"
	}
	gitURL, op, end, err := s.egress.Open(m.URL, m.Username, password, service)
	if err != nil {
		return 0, err
	}
	defer end()
	if m.Direction == "push" {
		var res gitstore.PushResult
		if err := s.git.Remote(ctx, m.RepoID, "push", gitstore.RemoteRequest{URL: gitURL}, &res); err != nil {
			return 0, syncError(err, op, m.URL, "write to", m.Username != "" || password != "")
		}
		return res.Moved, nil
	}
	var refs gitstore.RemoteRefs
	if err := s.git.Remote(ctx, m.RepoID, "ls", gitstore.RemoteRequest{URL: gitURL}, &refs); err != nil {
		return 0, syncError(err, op, m.URL, "read", m.Username != "" || password != "")
	}
	if err := s.repo.SetMirrorRemote(ctx, m.ID, refs); err != nil {
		s.log.Warn("mirrors: note the remote", zap.Error(err))
	}
	req := gitstore.RemoteRequest{URL: gitURL}
	if !m.Imported {
		req.SetHead = refs.Head
	}
	var res gitstore.FetchResult
	if err := s.git.Remote(ctx, m.RepoID, "fetch", req, &res); err != nil {
		return 0, syncError(err, op, m.URL, "read", m.Username != "" || password != "")
	}
	if len(res.Updated) > 0 {
		s.recordMoves(context.WithoutCancel(ctx), m.RepoID, "", "mirror", true, res.Updated, "")
	}
	return len(res.Updated), nil
}

// syncError says why a sync failed, from what the remote answered (op) or
// else git's own words; creds says whether a username or token was given.
func syncError(err error, op *gitstore.EgressOp, remote, verb string, creds bool) error {
	status, location, reach := op.Result()
	switch {
	case (status == 401 || status == 403) && !creds:
		// GitHub and GitLab answer a repository that doesn't exist the same
		// way as a private one.
		return fmt.Errorf("%s asked for credentials (%d): the repository is private, or doesn't exist. Give a username and token that can %s it", hostOf(remote), status, verb)
	case errors.Is(reach, netcheck.ErrPrivateAddress):
		return ErrPrivateAddress
	case reach != nil:
		return fmt.Errorf("Couldn't reach %s: %s", hostOf(remote), lastPart(reach.Error()))
	case status == 401 || status == 403:
		return fmt.Errorf("%s refused the credentials (%d): check the username and token, and that it can %s this repository", hostOf(remote), status, verb)
	case status == 404:
		return fmt.Errorf("%s answered 404: the repository doesn't exist, or the credentials can't see it", hostOf(remote))
	case status >= 300 && status < 400:
		if u, perr := url.Parse(remote); perr == nil && location != "" {
			if l, perr := u.Parse(location); perr == nil {
				l.Path = strings.TrimSuffix(strings.TrimSuffix(l.Path, "/info/refs"), "/")
				l.RawQuery = ""
				return fmt.Errorf("the repository moved to %s: use that URL", l.String())
			}
		}
		return fmt.Errorf("%s answered %d (a redirect, which Toft doesn't follow): use the repository's new URL", hostOf(remote), status)
	case status >= 500:
		return fmt.Errorf("%s answered %d; Toft tries again", hostOf(remote), status)
	}
	var be *gitstore.BrowseError
	if errors.As(err, &be) {
		return errors.New("git: " + be.Msg)
	}
	return err
}

func hostOf(remote string) string {
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		return u.Host
	}
	return "the host"
}

// lastPart is what a wrapped network error ends with ("no such host").
func lastPart(s string) string {
	if i := strings.LastIndex(s, ": "); i >= 0 && i+2 < len(s) {
		return s[i+2:]
	}
	return s
}
