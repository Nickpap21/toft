package code

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"go.uber.org/zap"
)

// Commit checks and what each push did (D-77). External CI reports checks
// by name on a commit; every deploy of a service that builds from the
// repository reports one of Toft's own (ReportDeploy).

// CheckSummary is a commit's checks together.
type CheckSummary struct {
	State   string `json:"state"` // success, failure, pending
	Total   int    `json:"total"`
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Pending int    `json:"pending"`
}

// CheckDeployment is the deployment a deploy check reports.
type CheckDeployment struct {
	ID        string `json:"id"`
	ServiceID string `json:"service_id"`
}

// CommitCheck is one check of a commit as the API shows it.
type CommitCheck struct {
	Name        string           `json:"name"`
	State       string           `json:"state"`
	Description string           `json:"description"`
	DetailsURL  *string          `json:"details_url"`
	Deployment  *CheckDeployment `json:"deployment"`
	Reporter    *Person          `json:"reporter"`
	StartedAt   *time.Time       `json:"started_at"`
	CompletedAt *time.Time       `json:"completed_at"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// CommitChecks is a commit's checks, Toft's own first.
type CommitChecks struct {
	SHA     string        `json:"sha"`
	Summary *CheckSummary `json:"summary"`
	Checks  []CommitCheck `json:"checks"`
}

// CheckInput is POST …/commits/{sha}/checks's body.
type CheckInput struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Description string `json:"description"`
	DetailsURL  string `json:"details_url"`
}

// StoredCheck is a check row with its commit.
type StoredCheck struct {
	SHA string
	CommitCheck
}

// NewCheck is a check to store, or to update by (repository, SHA, name).
type NewCheck struct {
	RepoID, SHA, Name, State, Description, DetailsURL, DeploymentID, ReporterID string
}

// DeployTarget is a service that builds from a repository's branch.
type DeployTarget struct {
	ServiceID        string  `json:"service_id"`
	ServiceName      string  `json:"service_name"`
	EnvironmentName  *string `json:"environment_name"`
	EnvironmentColor *string `json:"environment_color"`
	ServerName       *string `json:"server_name"`
	Branch           string  `json:"branch"`
	BaseDirectory    string  `json:"base_directory"`
	DeployOnPush     bool    `json:"deploy_on_push"`
	// FilesChanged is, on a pull request, how many of its files are under
	// BaseDirectory.
	FilesChanged *int `json:"files_changed,omitempty"`
}

// Pipeline is a push or a merge with the checks of the commit it moved the
// branch to.
type Pipeline struct {
	ID           int64        `json:"id"`
	Kind         string       `json:"kind"`
	Branch       string       `json:"branch"`
	Before       string       `json:"before"`
	After        string       `json:"after"`
	Subject      string       `json:"subject"`
	Commits      int          `json:"commits"`
	Forced       bool         `json:"forced"`
	Actor        *Person      `json:"actor"`
	PullNumber   *int         `json:"pull_number"`
	CreatedAt    time.Time    `json:"created_at"`
	DispatchedAt *time.Time   `json:"dispatched_at"`
	Error        *string      `json:"error"`
	Checks       CommitChecks `json:"checks"`
}

var (
	ErrBadCheckName   = errors.New("a check's name has 1 to 100 characters and no control characters")
	ErrReservedName   = errors.New("names starting with toft/ are Toft's own checks")
	ErrBadCheckState  = errors.New("state is pending, running, success, failure, cancelled or skipped")
	ErrBadCheckURL    = errors.New("details_url is an http or https address of 2,000 characters at most")
	ErrBadDescription = errors.New("a description has 300 characters at most")
	ErrBadSHA         = errors.New("name the commit by its full SHA")
	ErrNoCommit       = errors.New("this repository has no such commit")
	ErrTooManyChecks  = errors.New("a commit has 100 checks at most")
	ErrRepoInUse      = errors.New("services build from this repository: give them another source first")
)

// RepoInUseError is ErrRepoInUse with the services' names.
type RepoInUseError struct{ Services []string }

func (e *RepoInUseError) Error() string { return ErrRepoInUse.Error() }
func (e *RepoInUseError) Unwrap() error { return ErrRepoInUse }

const (
	maxChecks      = 100
	deployPrefix   = "toft/deploy/"
	reservedPrefix = "toft/"
)

var checkStates = map[string]bool{"pending": true, "running": true, "success": true, "failure": true, "cancelled": true, "skipped": true}

func fullSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validCheckName(n string) bool {
	if n == "" || utf8.RuneCountInString(n) > 100 || !utf8.ValidString(n) {
		return false
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (in CheckInput) check() error {
	switch {
	case !validCheckName(in.Name):
		return ErrBadCheckName
	case strings.HasPrefix(strings.ToLower(in.Name), reservedPrefix):
		return ErrReservedName
	case !checkStates[in.State]:
		return ErrBadCheckState
	case utf8.RuneCountInString(in.Description) > 300 || strings.ContainsRune(in.Description, 0):
		return ErrBadDescription
	}
	if in.DetailsURL != "" {
		u, err := url.Parse(in.DetailsURL)
		if err != nil || len(in.DetailsURL) > 2000 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return ErrBadCheckURL
		}
	}
	return nil
}

// summarize is checks together, nil when there are none.
func summarize(checks []CommitCheck) *CheckSummary {
	states := make([]string, 0, len(checks))
	for _, c := range checks {
		states = append(states, c.State)
	}
	return SummarizeStates(states)
}

// SummarizeStates is checks in these states together, nil for none.
func SummarizeStates(states []string) *CheckSummary {
	if len(states) == 0 {
		return nil
	}
	s := &CheckSummary{Total: len(states)}
	for _, state := range states {
		switch state {
		case "success", "skipped":
			s.Passed++
		case "failure", "cancelled":
			s.Failed++
		default:
			s.Pending++
		}
	}
	switch {
	case s.Failed > 0:
		s.State = "failure"
	case s.Pending > 0:
		s.State = "pending"
	default:
		s.State = "success"
	}
	return s
}

// checksOf is the checks of shas in repository repoID, by SHA; every SHA
// asked for has an entry.
func (s *Service) checksOf(ctx context.Context, repoID string, shas []string) (map[string]*CommitChecks, error) {
	out := make(map[string]*CommitChecks, len(shas))
	for _, sha := range shas {
		out[sha] = &CommitChecks{SHA: sha, Checks: []CommitCheck{}}
	}
	if len(shas) == 0 {
		return out, nil
	}
	rows, err := s.repo.ListChecks(ctx, repoID, shas)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if cc := out[r.SHA]; cc != nil {
			cc.Checks = append(cc.Checks, r.CommitCheck)
		}
	}
	for _, cc := range out {
		cc.Summary = summarize(cc.Checks)
	}
	return out, nil
}

// commitSHA is the full SHA of the commit ref names in repoID, or
// ErrNoCommit.
func (s *Service) commitSHA(ctx context.Context, repoID, ref string) (string, error) {
	var pg gitstore.Page[gitstore.Commit]
	err := s.git.Browse(ctx, repoID, "commits", url.Values{"ref": {ref}, "limit": {"1"}}, &pg)
	if c := browseCode(err); c == "ref_not_found" || c == "empty" || c == "bad_ref" {
		return "", ErrNoCommit
	}
	if err != nil {
		return "", err
	}
	if len(pg.Items) == 0 {
		return "", ErrNoCommit
	}
	return pg.Items[0].SHA, nil
}

// Checks is a commit's checks; sha may be short.
func (s *Service) Checks(ctx context.Context, projectID, repoID, sha string) (*CommitChecks, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	if !fullSHA(sha) {
		if sha, err = s.commitSHA(ctx, r.ID, sha); err != nil {
			return nil, err
		}
	}
	all, err := s.checksOf(ctx, r.ID, []string{sha})
	if err != nil {
		return nil, err
	}
	return all[sha], nil
}

// ReportCheck creates or updates (by name) a check on the commit sha, and
// says whether it was created.
func (s *Service) ReportCheck(ctx context.Context, projectID, repoID, sha, reporterID string, in CheckInput) (*CommitCheck, bool, error) {
	if err := in.check(); err != nil {
		return nil, false, err
	}
	if !fullSHA(sha) {
		return nil, false, ErrBadSHA
	}
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, false, err
	}
	if got, err := s.commitSHA(ctx, r.ID, sha); err != nil {
		return nil, false, err
	} else if got != sha {
		return nil, false, ErrNoCommit
	}
	exists, err := s.repo.CheckExists(ctx, r.ID, sha, in.Name)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		// ponytail: count then insert, so two reports at once may pass 100 by one.
		if n, err := s.repo.CountChecks(ctx, r.ID, sha); err != nil {
			return nil, false, err
		} else if n >= maxChecks {
			return nil, false, ErrTooManyChecks
		}
	}
	if err := s.repo.UpsertCheck(ctx, NewCheck{RepoID: r.ID, SHA: sha, Name: in.Name, State: in.State,
		Description: in.Description, DetailsURL: in.DetailsURL, ReporterID: reporterID}); err != nil {
		return nil, false, err
	}
	all, err := s.checksOf(ctx, r.ID, []string{sha})
	if err != nil {
		return nil, false, err
	}
	for _, c := range all[sha].Checks {
		if c.Name == in.Name {
			return &c, !exists, nil
		}
	}
	return nil, false, errors.New("the check reported isn't there")
}

// DeployCheckName is the check a deploy of service in environment reports
// ("" for a project service): toft/deploy/<environment>/<service>, at most
// 100 characters.
func DeployCheckName(environment, service string) string {
	n := deployPrefix + service
	if environment != "" {
		n = deployPrefix + environment + "/" + service
	}
	if r := []rune(n); len(r) > 100 {
		n = string(r[:100])
	}
	return n
}

// PreviewCheckName is a preview service's check (D-97):
// toft/preview/<service>, at most 100 characters.
func PreviewCheckName(service string) string {
	n := []rune(reservedPrefix + "preview/" + service)
	return string(n[:min(len(n), 100)])
}

// ReportDeploy records a deploy of service (in environment) as its check
// on sha in repositoryID (DeployCheckName, or PreviewCheckName for one of
// a preview's). A check never fails a deploy: errors are logged.
func (s *Service) ReportDeploy(ctx context.Context, repositoryID, sha, environment, service, state, description, deploymentID string, preview bool) {
	name := DeployCheckName(environment, service)
	if preview {
		name = PreviewCheckName(service)
	}
	if r := []rune(description); len(r) > 300 {
		description = string(r[:299]) + "…"
	}
	if err := s.repo.UpsertCheck(ctx, NewCheck{RepoID: repositoryID, SHA: sha, Name: name, State: state,
		Description: description, DeploymentID: deploymentID}); err != nil {
		s.log.Warn("deploy check", zap.Error(err), zap.String("repository_id", repositoryID), zap.String("deployment_id", deploymentID))
	}
}

// FailDeployChecks fails the running checks of deployments a restart
// interrupted.
func (s *Service) FailDeployChecks(ctx context.Context, deploymentIDs []string, description string) {
	if len(deploymentIDs) == 0 {
		return
	}
	if err := s.repo.FailDeployChecks(ctx, deploymentIDs, description); err != nil {
		s.log.Warn("fail deploy checks", zap.Error(err))
	}
}

// deployTargets is what builds from repository repoID; files, when given,
// are a pull request's, counted under each target's base directory.
func (s *Service) deployTargets(ctx context.Context, repoID, branch string, files []gitstore.FileChange) ([]DeployTarget, error) {
	all, err := s.repo.DeployTargets(ctx, repoID)
	if err != nil {
		return nil, err
	}
	out := make([]DeployTarget, 0, len(all))
	for _, t := range all {
		if branch != "" && t.Branch != branch {
			continue
		}
		if files != nil {
			n := filesUnder(files, t.BaseDirectory)
			t.FilesChanged = &n
		}
		out = append(out, t)
	}
	return out, nil
}

// ChangesUnder says whether head changes files under dir against base
// (D-86: a service's Connected work), comparing in paas-git.
func (s *Service) ChangesUnder(ctx context.Context, repoID, base, head, dir string) (bool, error) {
	var cmp gitstore.Compare
	if err := s.git.Browse(ctx, repoID, "compare", url.Values{"base": {base}, "head": {head}}, &cmp); err != nil {
		return false, fmt.Errorf("compare: %w", err)
	}
	return filesUnder(cmp.Files, dir) > 0, nil
}

// filesUnder counts the files under dir ("/" or "" is every file).
func filesUnder(files []gitstore.FileChange, dir string) int {
	dir = strings.Trim(path.Clean("/"+dir), "/")
	n := 0
	for _, f := range files {
		if dir == "" || under(f.Path, dir) || f.OldPath != "" && under(f.OldPath, dir) {
			n++
		}
	}
	return n
}

func under(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// withChecks adds each commit's (or branch tip's) checks together to a
// browse answer: commits, a commit, branches.
func (s *Service) withChecks(ctx context.Context, repoID, op string, out json.RawMessage) (json.RawMessage, error) {
	decode := func(v any) error {
		d := json.NewDecoder(bytes.NewReader(out))
		d.UseNumber()
		return d.Decode(v)
	}
	shaOf := func(item map[string]any) string {
		if op == "branches" {
			c, _ := item["commit"].(map[string]any)
			sha, _ := c["sha"].(string)
			return sha
		}
		sha, _ := item["sha"].(string)
		return sha
	}
	var items []map[string]any
	var page map[string]any
	switch op {
	case "commits", "branches":
		if err := decode(&page); err != nil {
			return nil, err
		}
		raw, _ := page["items"].([]any)
		for _, it := range raw {
			if m, ok := it.(map[string]any); ok {
				items = append(items, m)
			}
		}
	case "commit":
		var one map[string]any
		if err := decode(&one); err != nil {
			return nil, err
		}
		items, page = []map[string]any{one}, one
	default:
		return out, nil
	}
	shas := make([]string, 0, len(items))
	for _, it := range items {
		if sha := shaOf(it); sha != "" {
			shas = append(shas, sha)
		}
	}
	all, err := s.checksOf(ctx, repoID, shas)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if cc := all[shaOf(it)]; cc != nil {
			it["checks"] = cc.Summary
		} else {
			it["checks"] = nil
		}
	}
	if op == "branches" && s.issues != nil {
		names := make([]string, 0, len(items))
		for _, it := range items {
			if n, ok := it["name"].(string); ok {
				names = append(names, n)
			}
		}
		linked, err := s.issues.BranchIssues(ctx, repoID, names)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			n, _ := it["name"].(string)
			if refs := linked[n]; refs != nil {
				it["issues"] = refs
			} else {
				it["issues"] = []IssueRef{}
			}
		}
	}
	return json.Marshal(page)
}

// Pipelines pages a repository's pushes and merges, newest first, each with
// its commit's checks; cursor is the last id of the previous page.
func (s *Service) Pipelines(ctx context.Context, projectID, repoID, branch, cursor string, limit int) ([]Pipeline, string, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, "", err
	}
	var before int64
	if cursor != "" {
		if before, err = strconv.ParseInt(cursor, 10, 64); err != nil || before <= 0 {
			return nil, "", ErrBadCursor
		}
	}
	evs, err := s.repo.ListEvents(ctx, r.ID, branch, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(evs) > limit {
		evs = evs[:limit]
		next = strconv.FormatInt(evs[len(evs)-1].ID, 10)
	}
	shas := make([]string, 0, len(evs))
	for _, e := range evs {
		shas = append(shas, e.After)
	}
	all, err := s.checksOf(ctx, r.ID, shas)
	if err != nil {
		return nil, "", err
	}
	for i := range evs {
		evs[i].Checks = *all[evs[i].After]
	}
	return evs, next, nil
}

// ReportPreviewCheck records a preview service's check on sha without a
// deployment: waiting for approval, or refused before it built (D-97).
func (s *Service) ReportPreviewCheck(ctx context.Context, repositoryID, sha, service, state, description string) {
	s.ReportDeploy(ctx, repositoryID, sha, "", service, state, description, "", true)
}
