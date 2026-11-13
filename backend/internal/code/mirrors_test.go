package code

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/testenv"

	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ─── the fake's mirrors and webhooks ─────────────────────────────────────────

type fakeMirror struct {
	StoredMirror
	passwordEnc string
}

type fakeDelivery struct {
	StoredDelivery
	webhookID string
	gitEvent  int64
}

func fakeID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func (f *fakeRepo) mirrorByID(id string) *fakeMirror {
	for _, m := range f.mirrors {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (f *fakeRepo) ListMirrors(_ context.Context, repoID string) ([]StoredMirror, error) {
	out := []StoredMirror{}
	for _, m := range f.mirrors {
		if m.RepoID == repoID {
			m.State = m.state()
			out = append(out, m.StoredMirror)
		}
	}
	slices.SortStableFunc(out, func(a, b StoredMirror) int { return strings.Compare(a.Direction, b.Direction) })
	return out, nil
}
func (f *fakeRepo) ProjectPullMirrors(ctx context.Context, _ string) ([]StoredMirror, error) {
	out := []StoredMirror{}
	for _, m := range f.mirrors {
		if m.Direction == "pull" {
			m.State = m.state()
			out = append(out, m.StoredMirror)
		}
	}
	return out, nil
}
func (f *fakeRepo) CountPushMirrors(_ context.Context, repoID string) (int, error) {
	n := 0
	for _, m := range f.mirrors {
		if m.RepoID == repoID && m.Direction == "push" {
			n++
		}
	}
	return n, nil
}
func (f *fakeRepo) CreateMirror(_ context.Context, nm NewMirror) (string, error) {
	if nm.Direction == "pull" {
		for i, m := range f.mirrors {
			if m.RepoID == nm.RepoID && m.Direction == "pull" {
				if m.LastOKAt != nil || m.SyncingSince != nil || m.Failures == 0 {
					return "", ErrImportRunning
				}
				f.mirrors = slices.Delete(f.mirrors, i, i+1)
				break
			}
		}
	}
	now := time.Now()
	m := &fakeMirror{StoredMirror: StoredMirror{RepoID: nm.RepoID, Mirror: Mirror{ID: nm.ID, Direction: nm.Direction,
		URL: nm.URL, Username: nm.Username, HasPassword: nm.PasswordEnc != "", Keep: nm.Keep, CreatedAt: now, NextSyncAt: &now}}, passwordEnc: nm.PasswordEnc}
	f.mirrors = append(f.mirrors, m)
	f.setMirrored(nm.RepoID)
	return m.ID, nil
}
func (f *fakeRepo) setMirrored(repoID string) {
	for i := range f.stored {
		if f.stored[i].ID == repoID {
			f.stored[i].Mirrored = slices.ContainsFunc(f.mirrors, func(m *fakeMirror) bool { return m.RepoID == repoID && m.Direction == "pull" })
		}
	}
}
func (f *fakeRepo) DeleteMirror(_ context.Context, repoID, id string) error {
	for i, m := range f.mirrors {
		if m.ID == id && m.RepoID == repoID {
			f.mirrors = slices.Delete(f.mirrors, i, i+1)
			f.setMirrored(repoID)
			return nil
		}
	}
	return ErrNotFound
}
func (f *fakeRepo) RequestSync(_ context.Context, _, id string) (bool, error) {
	m := f.mirrorByID(id)
	if m == nil || m.SyncingSince != nil {
		return false, nil
	}
	now := time.Now()
	m.NextSyncAt = &now
	return true, nil
}
func (f *fakeRepo) ClaimMirrors(_ context.Context, n int) ([]ClaimedMirror, error) {
	out := []ClaimedMirror{}
	for _, m := range f.mirrors {
		if len(out) < n && m.NextSyncAt != nil && !m.NextSyncAt.After(time.Now()) && m.SyncingSince == nil {
			now := time.Now()
			m.SyncingSince, m.NextSyncAt = &now, nil
			out = append(out, ClaimedMirror{ID: m.ID, RepoID: m.RepoID, OrgID: "o1", Direction: m.Direction, URL: m.URL, Username: m.Username,
				PasswordEnc: m.passwordEnc, Keep: m.Keep, Imported: m.LastOKAt != nil, Failures: m.Failures})
		}
	}
	return out, nil
}
func (f *fakeRepo) SetMirrorRemote(_ context.Context, id string, refs gitstore.RemoteRefs) error {
	if m := f.mirrorByID(id); m != nil {
		m.Remote = &MirrorRemote{DefaultBranch: refs.Head, Branches: refs.Branches, Tags: refs.Tags}
	}
	return nil
}
func (f *fakeRepo) FinishMirror(_ context.Context, id string, r MirrorResult) (int, error) {
	m := f.mirrorByID(id)
	if m == nil {
		return 0, nil
	}
	now := time.Now()
	m.SyncingSince, m.LastSyncAt = nil, &now
	if r.Err == "" {
		m.LastOKAt, m.Failures, m.Error, m.LastMoved = &now, 0, nil, &r.Moved
		if m.Direction == "pull" {
			next := now.Add(pullEvery)
			m.NextSyncAt = &next
			if !m.Keep {
				_ = f.DeleteMirror(context.Background(), m.RepoID, id)
			}
		}
		return 0, nil
	}
	m.Failures++
	msg := r.Err
	m.Error = &msg
	if m.Direction == "pull" && m.LastOKAt == nil {
		m.NextSyncAt = nil
	} else if m.NextSyncAt == nil || m.NextSyncAt.After(r.RetryAt) {
		m.NextSyncAt = &r.RetryAt
	}
	return m.Failures, nil
}
func (f *fakeRepo) ResetSyncing(context.Context) error { return nil }
func (f *fakeRepo) MirrorFailures(context.Context) ([]MirrorHealth, error) {
	out := []MirrorHealth{}
	for _, m := range f.mirrors {
		out = append(out, MirrorHealth{MirrorID: m.ID, Failing: m.Failures >= MirrorFailingAfter, Detail: str(m.Error)})
	}
	return out, nil
}

func (f *fakeRepo) hookByID(id string) *StoredWebhook {
	for _, w := range f.hooks {
		if w.ID == id {
			return w
		}
	}
	return nil
}
func (f *fakeRepo) ListWebhooks(context.Context, string) ([]StoredWebhook, error) {
	out := []StoredWebhook{}
	for _, w := range f.hooks {
		out = append(out, *w)
	}
	return out, nil
}
func (f *fakeRepo) CountWebhooks(context.Context, string) (int, error) { return len(f.hooks), nil }
func (f *fakeRepo) CreateWebhook(_ context.Context, w NewWebhook) (string, error) {
	now := time.Now()
	h := &StoredWebhook{Webhook: Webhook{ID: w.ID, URL: w.URL, Events: w.Events, Active: w.Active, CreatedAt: now,
		CreatedBy: person(w.CreatedBy, w.CreatedBy), SecretRotatedAt: now, SecretRotatedBy: person(w.CreatedBy, w.CreatedBy)}, SecretEnc: w.SecretEnc}
	f.hooks = append(f.hooks, h)
	return h.ID, nil
}
func (f *fakeRepo) UpdateWebhook(_ context.Context, _, id string, in WebhookUpdate) (bool, error) {
	w := f.hookByID(id)
	if w == nil {
		return false, nil
	}
	if in.URL != nil {
		w.URL = *in.URL
	}
	if in.Events != nil {
		w.Events = in.Events
	}
	if in.Active != nil {
		w.Active = *in.Active
		if !w.Active {
			for _, d := range f.deliveries {
				if d.webhookID == id && d.State == "pending" {
					d.State = "failed"
				}
			}
		}
	}
	return true, nil
}
func (f *fakeRepo) RotateWebhookSecret(_ context.Context, _, id, secretEnc, actorID string) (bool, error) {
	w := f.hookByID(id)
	if w == nil {
		return false, nil
	}
	w.SecretEnc, w.SecretRotatedAt, w.SecretRotatedBy = secretEnc, time.Now(), person(actorID, actorID)
	return true, nil
}
func (f *fakeRepo) DeleteWebhook(_ context.Context, _, id string) (bool, error) {
	for i, w := range f.hooks {
		if w.ID == id {
			f.hooks = slices.Delete(f.hooks, i, i+1)
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeRepo) InsertPing(_ context.Context, webhookID, actorID string) (string, error) {
	d := &fakeDelivery{webhookID: webhookID, StoredDelivery: StoredDelivery{WebhookDelivery: WebhookDelivery{ID: fakeID(300 + len(f.deliveries)),
		Event: "ping", State: "pending", CreatedAt: time.Now(), Actor: person(actorID, actorID)}}}
	f.deliveries = append(f.deliveries, d)
	return d.ID, nil
}
func (f *fakeRepo) ListDeliveries(_ context.Context, webhookID string, _ time.Time, _ string, n int) ([]WebhookDelivery, error) {
	out := []WebhookDelivery{}
	for i := len(f.deliveries) - 1; i >= 0 && len(out) < n; i-- {
		if d := f.deliveries[i]; d.webhookID == webhookID {
			out = append(out, d.WebhookDelivery)
		}
	}
	return out, nil
}
func (f *fakeRepo) LastDeliveries(context.Context, string) (map[string]WebhookDelivery, error) {
	out := map[string]WebhookDelivery{}
	for _, d := range f.deliveries {
		out[d.webhookID] = d.WebhookDelivery
	}
	return out, nil
}
func (f *fakeRepo) GetDelivery(_ context.Context, _, webhookID, id string) (*StoredDelivery, error) {
	for _, d := range f.deliveries {
		if d.ID == id && d.webhookID == webhookID {
			cp := d.StoredDelivery
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) Redeliver(_ context.Context, webhookID, id, actorID string) (bool, error) {
	for _, d := range f.deliveries {
		if d.ID == id && d.webhookID == webhookID && d.State != "pending" {
			d.State, d.Attempts, d.Actor, d.NextAttemptAt = "pending", 0, person(actorID, actorID), nil
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeRepo) ClaimDeliveries(_ context.Context, n int) ([]ClaimedDelivery, error) {
	out := []ClaimedDelivery{}
	for _, d := range f.deliveries {
		w := f.hookByID(d.webhookID)
		if len(out) < n && d.State == "pending" && w != nil && w.Active && (d.NextAttemptAt == nil || !d.NextAttemptAt.After(time.Now())) {
			d.Attempts++
			lease := time.Now().Add(time.Minute)
			d.NextAttemptAt = &lease
			out = append(out, ClaimedDelivery{ID: d.ID, WebhookID: w.ID, RepoID: testRepoID, URL: w.URL, SecretEnc: w.SecretEnc,
				Event: d.Event, ActorID: personID(d.Actor), GitEventID: d.gitEvent, Payload: str(d.Payload), Attempts: d.Attempts, CreatedAt: d.CreatedAt})
		}
	}
	return out, nil
}
func personID(p *Person) string {
	if p == nil {
		return ""
	}
	return p.ID
}
func (f *fakeRepo) delivery(id string) *fakeDelivery {
	for _, d := range f.deliveries {
		if d.ID == id {
			return d
		}
	}
	return nil
}
func (f *fakeRepo) SetDeliveryPayload(_ context.Context, id, summary, payload string) error {
	d := f.delivery(id)
	d.Summary, d.Payload = summary, &payload
	return nil
}
func (f *fakeRepo) FinishDelivery(_ context.Context, id string, r DeliveryResult) error {
	d := f.delivery(id)
	now := time.Now()
	d.State, d.ResponseStatus, d.ResponseBody, d.Error, d.DurationMS, d.DeliveredAt = r.State, r.ResponseStatus, r.ResponseBody, r.Error, r.DurationMS, &now
	d.NextAttemptAt = nil
	if r.State == "pending" {
		d.NextAttemptAt = &r.NextAttemptAt
	}
	return nil
}
func (f *fakeRepo) DeleteOldDeliveries(context.Context, time.Time) (int64, error) { return 0, nil }
func (f *fakeRepo) GetEvent(_ context.Context, id int64) (*StoredEvent, error) {
	for _, e := range f.events {
		if e.id == id {
			return &StoredEvent{ID: id, RepoID: e.RepoID, Kind: e.Kind, Action: e.Action, Branch: e.Branch, Before: e.Before, After: e.After,
				ActorID: e.ActorID, ActorName: e.ActorID, PullRequestID: e.PullID, Commits: e.Commits, Forced: e.Forced, CreatedAt: e.at}, nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) PullByID(_ context.Context, id string) (*StoredPull, error) {
	if p := f.pull(id); p != nil {
		cp := *p
		return &cp, nil
	}
	return nil, ErrNotFound
}
func (f *fakeRepo) RepoForHook(_ context.Context, id string) (*HookRepo, error) {
	for _, r := range f.stored {
		if r.ID == id {
			return &HookRepo{ID: r.ID, Name: r.Name, ProjectID: "p1", OrgSlug: r.OrgSlug}, nil
		}
	}
	return nil, ErrNotFound
}

// ─── tests ───────────────────────────────────────────────────────────────────

const testRepoID = "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11"

func TestRemoteAndWebhookURLs(t *testing.T) {
	for raw, want := range map[string]error{
		"https://github.com/founders/shop.git":            nil,
		"https://gitlab.example.com:8443/a/b":             nil,
		"https://github.com/founders/shop/":               nil,
		"http://github.com/founders/shop.git":             ErrBadRemote,
		"https://x:token@github.com/a/b.git":              ErrBadRemote,
		"https://github.com/a/b.git?x=1":                  ErrBadRemote,
		"https://github.com/a/b.git#main":                 ErrBadRemote,
		"https://github.com":                              ErrBadRemote,
		"https://github.com/":                             ErrBadRemote,
		"ssh://git@github.com/a/b.git":                    ErrBadRemote,
		"https://127.0.0.1/a/b.git":                       ErrPrivateAddress,
		"https://169.254.169.254/latest/x":                ErrPrivateAddress,
		"https://[::1]/a/b":                               ErrPrivateAddress,
		"https://10.0.0.5/a/b":                            ErrPrivateAddress,
		"https://[::ffff:192.168.1.1]/a/b":                ErrPrivateAddress,
		"https://github.com/a b":                          ErrBadRemote,
		"file:///etc/passwd":                              ErrBadRemote,
		"https://github.com/" + strings.Repeat("a", 2100): ErrBadRemote,
	} {
		got, err := checkRemote(raw)
		if !errors.Is(err, want) {
			t.Errorf("checkRemote(%q) = %q, %v; want %v", raw, got, err, want)
		}
		if err == nil && strings.HasSuffix(got, "/") {
			t.Errorf("checkRemote(%q) kept the trailing slash: %q", raw, got)
		}
	}
	for raw, want := range map[string]error{
		"https://hooks.slack.com/services/T0/B0/x": nil,
		"https://ci.example.com/hook?token=abc":    nil,
		"https://ci.example.com":                   nil,
		"http://ci.example.com/hook":               ErrBadHookURL,
		"https://u:p@ci.example.com/hook":          ErrBadHookURL,
		"https://ci.example.com/hook#x":            ErrBadHookURL,
		"https://192.168.0.10/hook":                ErrPrivateAddress,
		"https://[fd00::1]/hook":                   ErrPrivateAddress,
	} {
		if _, err := checkHookURL(raw); !errors.Is(err, want) {
			t.Errorf("checkHookURL(%q) = %v; want %v", raw, err, want)
		}
	}
	if _, err := checkEvents([]string{"push", "push", "pull_request"}); err != nil {
		t.Error(err)
	}
	for _, bad := range [][]string{nil, {}, {"tag"}, {"push", "issues"}} {
		if _, err := checkEvents(bad); !errors.Is(err, ErrBadEvents) {
			t.Errorf("checkEvents(%v) = %v", bad, err)
		}
	}
	if checkUsername("a:b") == nil || checkUsername("x\n") == nil || checkUsername("x-access-token") != nil {
		t.Error("checkUsername")
	}
}

func TestMirrorState(t *testing.T) {
	now := time.Now()
	for want, m := range map[string]Mirror{
		"importing": {Direction: "pull"},
		"failed":    {Direction: "pull", Failures: 1},
		"pending":   {Direction: "push"},
		"syncing":   {Direction: "push", SyncingSince: &now, LastOKAt: &now},
		"ok":        {Direction: "pull", LastOKAt: &now},
		"failing":   {Direction: "push", LastOKAt: &now, Failures: 2},
	} {
		if got := m.state(); got != want {
			t.Errorf("%+v: %s, want %s", m, got, want)
		}
	}
}

// remoteGit is another host's git over HTTPS (git http-backend), wanting
// x-access-token and s3cret, with bare repositories under its root.
func remoteGit(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		testenv.Skip(t, "git is not installed")
	}
	if b, err := exec.Command(gitPath, "--exec-path").Output(); err != nil {
		testenv.Skip(t, "git has no exec path")
	} else if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(b)), "git-http-backend")); err != nil {
		testenv.Skip(t, "git http-backend is not installed (alpine: git-daemon)")
	}
	root := t.TempDir()
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{
		"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + root,
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.receivepack", "GIT_CONFIG_VALUE_0=true",
	}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "x-access-token" || p != "s3cret" {
			http.Error(w, "who are you", http.StatusUnauthorized)
			return
		}
		r.Header.Del("Authorization")
		if r.ContentLength < 0 {
			b, _ := io.ReadAll(r.Body)
			r.Body, r.ContentLength, r.TransferEncoding = io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, root
}

// toLoopback is client's transport dialling addr whatever the URL's host:
// the remote is on loopback, which the API refuses, so the tests name it
// example.com (in httptest's certificate).
func toLoopback(client *http.Client, addr string) *http.Transport {
	tr := client.Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	return tr
}

// TestImportMirrorsAndWebhooks runs D-79 end to end with the real git
// client: an import through the egress from a TLS remote, its events in the
// outbox and as a webhook's deliveries, signed and retried; a push mirror;
// a failure in words; and the API's answers checked against openapi.yaml.
func TestImportMirrorsAndWebhooks(t *testing.T) {
	if _, err := gitstore.CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git %d.%d or newer: %v", gitstore.MinVersion[0], gitstore.MinVersion[1], err)
	}
	gin.SetMode(gin.TestMode)
	remote, remoteRoot := remoteGit(t)
	env := gitEnv(t)
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := run(t, env, dir, append([]string{"git"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(out)
	}
	// The original: main and dev, a tag, HEAD on dev.
	w := t.TempDir()
	git(w, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(w, "a.txt"), []byte("one\n"), 0o644)
	git(w, "add", ".")
	git(w, "commit", "-qm", "First")
	git(w, "tag", "v1")
	git(w, "checkout", "-qb", "dev")
	git(w, "commit", "-q", "--allow-empty", "-m", "Second")
	orig := filepath.Join(remoteRoot, "shop.git")
	git(remoteRoot, "init", "-q", "--bare", "-b", "dev", orig)
	git(w, "push", "-q", orig, "main", "dev", "v1")
	git(remoteRoot, "init", "-q", "--bare", filepath.Join(remoteRoot, "copy.git"))

	sockDir, err := os.MkdirTemp("", "tm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	egress := gitstore.NewEgress(toLoopback(remote.Client(), remote.Listener.Addr().String()))
	remoteURL := "https://example.com"
	go egress.Serve(ctx, filepath.Join(sockDir, "e.sock"))
	ln, err := net.Listen("unix", filepath.Join(sockDir, "g.sock"))
	if err != nil {
		t.Fatal(err)
	}
	executor := &http.Server{Handler: gitstore.NewExecutor(t.TempDir()).WithHooks(hooksDir(t)).WithEgress(filepath.Join(sockDir, "e.sock")).Handler()}
	go executor.Serve(ln)
	t.Cleanup(func() { executor.Close() })

	const key = "test-encryption-key-32-bytes-long!!"
	repo := &fakeRepo{
		stored: []StoredRepo{{ID: testRepoID, Name: "shop", OrgSlug: "acme", CreatedAt: time.Now()}},
		items:  map[string][]StoredItem{},
	}
	// A receiver that fails once, then takes everything.
	var mu sync.Mutex
	var got []*http.Request
	var bodies [][]byte
	fail := true
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		got, bodies = append(got, r), append(bodies, b)
		if fail {
			fail = false
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(receiver.Close)
	svc := NewService(repo, gitstore.NewClient(filepath.Join(sockDir, "g.sock")), fakeMembers{"own": {"o1": "owner"}, "dev": {"o1": "developer"}},
		func() string { return "panel.test" }, 2222, zap.NewNop()).WithEgress(egress, crypto.NewKeys(key, "")).WithHookClient(receiver.Client())

	router := func(userID, role string) http.Handler {
		g := gin.New()
		NewHandler(svc, projectsIn{"p1": "o1"}, nil, zap.NewNop()).RegisterRoutes(g.Group("/api/v1", func(c *gin.Context) {
			c.Set("userID", userID)
			c.Set("orgID", "o1")
			c.Set("memberRole", role)
			c.Next()
		}))
		return g
	}
	own, dev, viewer := router("own", "owner"), router("dev", "developer"), router("eve", "viewer")
	base := "/api/v1/projects/p1/repositories/" + testRepoID
	call := func(h http.Handler, method, path string, body any) (int, map[string]any) {
		t.Helper()
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rec, req)
		out := map[string]any{}
		if rec.Body.Len() == 0 {
			apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], rec.Code, nil)
			return rec.Code, out
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, path, err, rec.Body)
		}
		apispec.Check(t, method, strings.SplitN(path, "?", 2)[0], rec.Code, out)
		return rec.Code, out
	}
	data := func(m map[string]any) map[string]any { d, _ := m["data"].(map[string]any); return d }
	code := func(m map[string]any) string { return fmt.Sprint(m["code"]) }

	// A webhook on pushes, added straight to the store: the receiver is on
	// a loopback address, which the API refuses.
	secret := "0123456789abcdef" // gitleaks:allow
	enc, _ := crypto.NewKeys(key, "").Seal(hookSecretAD(fakeID(250)), secret)
	repo.hooks = append(repo.hooks, &StoredWebhook{Webhook: Webhook{ID: fakeID(250), URL: receiver.URL + "/hook", Events: []string{"push", "pull_request"},
		Active: true, CreatedAt: time.Now(), SecretRotatedAt: time.Now()}, SecretEnc: enc})

	// The API's refusals.
	if st, out := call(viewer, "POST", base+"/import", map[string]any{"url": remoteURL + "/shop.git"}); st != http.StatusForbidden {
		t.Errorf("a viewer imported: %d %v", st, out)
	}
	if st, out := call(dev, "POST", base+"/import", map[string]any{"url": "https://127.0.0.1/a.git"}); st != http.StatusBadRequest || code(out) != "private_address" {
		t.Errorf("a private address: %d %v", st, out)
	}
	if st, out := call(dev, "POST", base+"/import", map[string]any{"url": "https://github.com/a/b?x"}); st != http.StatusBadRequest || code(out) != "bad_url" {
		t.Errorf("a query: %d %v", st, out)
	}

	// The import, through the service (the remote is on loopback too).
	m, err := svc.Import(ctx, "p1", testRepoID, "dev", ImportInput{URL: remoteURL + "/shop.git/", Username: "x-access-token", Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if m.State != "importing" || m.Direction != "pull" || !m.HasPassword || m.Keep || strings.HasSuffix(m.URL, "/") {
		t.Fatalf("import: %+v", m)
	}
	if st, out := call(own, "GET", base, nil); st != http.StatusOK || data(out)["pull_mirror"] == nil {
		t.Errorf("the repository while importing: %d %v", st, out)
	}
	if st, out := call(dev, "POST", base+"/pulls", map[string]any{"title": "x", "base": "main", "head": "dev"}); st != http.StatusConflict || code(out) != "mirror_read_only" {
		t.Errorf("a pull request while importing: %d %v", st, out)
	}
	// The first is still queued: a second import replaces only a failed one.
	if _, err := svc.Import(ctx, "p1", testRepoID, "dev", ImportInput{URL: remoteURL + "/shop.git"}); !errors.Is(err, ErrImportRunning) {
		t.Errorf("a second import while one waits: %v", err)
	}
	claimed, _ := repo.ClaimMirrors(ctx, 4)
	if len(claimed) != 1 {
		t.Fatalf("claimed %+v", claimed)
	}
	svc.syncMirror(ctx, claimed[0])
	if ms, _ := repo.ListMirrors(ctx, testRepoID); len(ms) != 0 {
		t.Fatalf("an import without keep is still there: %+v", ms)
	}
	var sum gitstore.Summary
	if err := svc.git.Browse(ctx, testRepoID, "summary", nil, &sum); err != nil || sum.DefaultBranch != "dev" || sum.BranchCount != 2 || sum.TagCount != 1 {
		t.Fatalf("after the import: %+v %v", sum, err)
	}
	// Its events: two branches (mirror, deploying) and a tag (dispatched).
	kinds := map[string]bool{}
	for _, e := range repo.events {
		kinds[e.Kind+":"+e.Branch] = e.dispatched
	}
	if len(repo.events) != 3 || kinds["mirror:main"] || kinds["mirror:dev"] || !kinds["tag:v1"] {
		t.Fatalf("events: %+v", repo.events)
	}

	// Each became a push delivery. The first attempt meets a 503, so it
	// waits 30 s; the rest go through, signed.
	ds, _ := repo.ClaimDeliveries(ctx, 10)
	if len(ds) != 3 {
		t.Fatalf("deliveries: %+v", ds)
	}
	for _, d := range ds {
		svc.deliver(ctx, d)
	}
	pending := 0
	for _, d := range repo.deliveries {
		switch d.State {
		case "pending":
			pending++
			if d.Error == nil || !strings.Contains(*d.Error, "503") || d.NextAttemptAt.Sub(time.Now()) < 25*time.Second {
				t.Errorf("the retried one: %+v", d)
			}
		case "delivered":
		default:
			t.Errorf("a delivery: %+v", d)
		}
	}
	if pending != 1 || len(got) != 3 {
		t.Fatalf("pending %d, sent %d", pending, len(got))
	}
	for i, req := range got {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(bodies[i])
		if req.Header.Get("X-Toft-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || req.Header.Get("X-Toft-Event") != "push" ||
			req.Header.Get("User-Agent") != hookAgent || len(req.Header.Get("X-Toft-Delivery")) != 36 {
			t.Errorf("request %d's headers: %v", i, req.Header)
		}
	}
	// A redelivery sends the same bytes and ID.
	var failed *fakeDelivery
	for _, d := range repo.deliveries {
		if d.State == "pending" {
			failed = d
		}
	}
	failed.State = "failed"
	hookPath := base + "/webhooks/" + fakeID(250)
	if st, out := call(own, "POST", hookPath+"/deliveries/"+failed.ID+"/redeliver", nil); st != http.StatusAccepted {
		t.Fatalf("redeliver: %d %v", st, out)
	}
	if st, out := call(own, "POST", hookPath+"/deliveries/"+failed.ID+"/redeliver", nil); st != http.StatusConflict || code(out) != "delivery_pending" {
		t.Errorf("redeliver twice: %d %v", st, out)
	}
	ds, _ = repo.ClaimDeliveries(ctx, 10)
	svc.deliver(ctx, ds[0])
	if failed.State != "delivered" || !bytes.Equal(bodies[len(bodies)-1], []byte(*failed.Payload)) || got[len(got)-1].Header.Get("X-Toft-Delivery") != failed.ID {
		t.Errorf("the redelivery: %+v", failed)
	}
	// The payloads and the pages, checked against the spec.
	if st, out := call(own, "GET", hookPath+"/deliveries/"+failed.ID, nil); st != http.StatusOK || data(out)["payload"] == nil {
		t.Errorf("a delivery: %d %v", st, out)
	} else if p, _ := data(out)["payload"].(map[string]any); p["via"] != "mirror" || p["event"] != "push" {
		t.Errorf("payload: %v", p)
	}
	call(own, "GET", hookPath+"/deliveries", nil)
	call(own, "GET", base+"/webhooks", nil)
	if st, _ := call(dev, "GET", base+"/webhooks", nil); st != http.StatusForbidden {
		t.Errorf("a developer listed webhooks: %d", st)
	}

	// A push mirror pushes everything; a wrong token fails in words, and the
	// mirror waits before trying again.
	pm, err := svc.AddPushMirror(ctx, "p1", testRepoID, "own", MirrorInput{URL: remoteURL + "/copy.git", Username: "x-access-token", Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, _ = repo.ClaimMirrors(ctx, 4)
	svc.syncMirror(ctx, claimed[0])
	if out := git(remoteRoot, "--git-dir=copy.git", "for-each-ref", "--format=%(refname)"); out != "refs/heads/dev\nrefs/heads/main\nrefs/tags/v1" {
		t.Fatalf("the push mirror's refs: %q", out)
	}
	if mm := repo.mirrorByID(pm.ID); mm.state() != "ok" || mm.LastMoved == nil || *mm.LastMoved != 3 {
		t.Errorf("push mirror: %+v", mm.Mirror)
	}
	bad, _ := svc.AddPushMirror(ctx, "p1", testRepoID, "own", MirrorInput{URL: remoteURL + "/copy.git", Username: "x-access-token", Password: "wrong"})
	claimed, _ = repo.ClaimMirrors(ctx, 4)
	svc.syncMirror(ctx, claimed[0])
	if mm := repo.mirrorByID(bad.ID); mm.Failures != 1 || mm.Error == nil || !strings.Contains(*mm.Error, "refused the credentials (401)") ||
		mm.NextSyncAt == nil || time.Until(*mm.NextSyncAt) < 50*time.Second {
		t.Errorf("the failing mirror: %+v %s", mm.Mirror, str(mm.Error))
	}
	if st, out := call(own, "GET", base+"/mirrors", nil); st != http.StatusOK {
		t.Errorf("mirrors: %d %v", st, out)
	}
	if st, out := call(dev, "DELETE", base+"/mirrors/"+bad.ID, nil); st != http.StatusForbidden {
		t.Errorf("a developer removed a push mirror: %d %v", st, out)
	}
	if st, out := call(own, "DELETE", base+"/mirrors/"+bad.ID, nil); st != http.StatusNoContent {
		t.Errorf("remove: %d %v", st, out)
	}
	// Not empty any more: no second import.
	if st, out := call(dev, "POST", base+"/import", map[string]any{"url": "https://github.com/a/b.git"}); st != http.StatusConflict || code(out) != "not_empty" {
		t.Errorf("import into a repository with branches: %d %v", st, out)
	}
	// Webhooks through the API: create (the secret once), pause, rotate,
	// ping refused while paused, delete.
	st, out := call(own, "POST", base+"/webhooks", map[string]any{"url": "https://ci.example.com/hooks/toft", "events": []string{"push"}})
	if st != http.StatusCreated || data(out)["secret"] == "" {
		t.Fatalf("create: %d %v", st, out)
	}
	id := data(out)["id"].(string)
	if st, out := call(own, "POST", base+"/webhooks", map[string]any{"url": "https://ci.example.com", "events": []string{"tags"}}); st != http.StatusBadRequest || code(out) != "bad_events" {
		t.Errorf("bad events: %d %v", st, out)
	}
	if st, out := call(own, "PATCH", base+"/webhooks/"+id, map[string]any{"active": false}); st != http.StatusOK || data(out)["active"] != false {
		t.Errorf("pause: %d %v", st, out)
	}
	if st, out := call(own, "POST", base+"/webhooks/"+id+"/test", nil); st != http.StatusConflict || code(out) != "webhook_paused" {
		t.Errorf("ping a paused one: %d %v", st, out)
	}
	if st, out := call(own, "POST", base+"/webhooks/"+id+"/secret", nil); st != http.StatusOK || len(fmt.Sprint(data(out)["secret"])) != 64 {
		t.Errorf("rotate: %d %v", st, out)
	}
	if st, _ := call(own, "DELETE", base+"/webhooks/"+id, nil); st != http.StatusNoContent {
		t.Errorf("delete: %d", st)
	}
}
