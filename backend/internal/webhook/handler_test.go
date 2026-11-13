package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/code"
	"github.com/Nickpap21/toft/backend/internal/project"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const (
	projectA = "11111111-1111-1111-1111-111111111111" // workspace org-a
	projectB = "22222222-2222-2222-2222-222222222222" // workspace org-b
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	valid := sign("s3cret", body)
	cases := []struct {
		name   string
		secret string
		header string
		want   bool
	}{
		{"no secret rejects even a signed request", "", valid, false},
		{"no secret rejects an unsigned request", "", "", false},
		{"valid signature", "s3cret", valid, true},
		{"wrong signature", "s3cret", "sha256=deadbeef", false},
		{"missing prefix", "s3cret", valid[len("sha256="):], false},
		{"signed with another secret", "other", valid, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, verifySignature(tc.secret, body, tc.header))
		})
	}
}

// fakeSecrets holds plain secrets by project.
type fakeSecrets struct {
	mu       sync.Mutex
	secrets  map[string]string
	pushed   map[string]LastPush
	failPush bool
}

func (f *fakeSecrets) Get(_ context.Context, projectID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.secrets[projectID]; ok {
		return s, nil
	}
	return "", ErrUnknownWebhook
}

func (f *fakeSecrets) Ensure(_ context.Context, projectID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.secrets[projectID]; !ok {
		f.secrets[projectID] = "new-" + projectID[:4]
	}
	return f.secrets[projectID], nil
}

func (f *fakeSecrets) Rotate(_ context.Context, projectID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets[projectID] = "rotated-" + projectID[:4]
	return f.secrets[projectID], nil
}

func (f *fakeSecrets) LastPush(_ context.Context, projectID string) (LastPush, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pushed[projectID], nil
}

func (f *fakeSecrets) MarkPushed(_ context.Context, projectID, repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPush {
		return errors.New("database down")
	}
	if f.pushed == nil {
		f.pushed = map[string]LastPush{}
	}
	now := time.Now()
	f.pushed[projectID] = LastPush{At: &now, Repo: repo}
	return nil
}

type fakeProjects struct{}

func (fakeProjects) GetProject(_ context.Context, id string) (*project.Project, error) {
	return nil, errors.New("not needed")
}

func (fakeProjects) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	return (id == projectA && orgID == "org-a") || (id == projectB && orgID == "org-b"), nil
}

// fakeFinder records which project each push looked in.
type fakeFinder struct{ calls chan string }

func (f *fakeFinder) Get(context.Context, string) (*service.Service, error) {
	return nil, errors.New("not found")
}

func (f *fakeFinder) FindByProjectRepoAndBranch(_ context.Context, projectID, repoURL, repositoryID, branch string) ([]service.Service, error) {
	if repositoryID != "" {
		f.calls <- projectID + " repository:" + repositoryID + " " + branch
		return nil, nil
	}
	f.calls <- projectID + " " + repoURL + " " + branch
	return nil, nil
}

// A push to the project's own repository (D-77) looks in its project for
// the services building from that repository's branch.
func TestDeployPushFindsTheRepositorysServices(t *testing.T) {
	finder := &fakeFinder{calls: make(chan string, 1)}
	h := &Handler{serviceSvc: finder, projects: fakeProjects{}, log: zap.NewNop()}
	if err := h.DeployPush(context.Background(), code.Push{EventID: 7, RepositoryID: "r-1", ProjectID: projectA, Branch: "main", Kind: "merge", ActorName: "Ada"}); err != nil {
		t.Fatal(err)
	}
	if got := <-finder.calls; got != projectA+" repository:r-1 main" {
		t.Errorf("looked for %q", got)
	}
}

func newRouter(secrets *fakeSecrets, finder *fakeFinder, role, org string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &Handler{serviceSvc: finder, projects: fakeProjects{}, secrets: secrets, log: zap.NewNop()}
	r := gin.New()
	v1 := r.Group("/api/v1")
	h.RegisterRoutes(v1)
	protected := v1.Group("")
	protected.Use(func(c *gin.Context) { c.Set("orgID", org); c.Set("memberRole", role); c.Next() })
	h.RegisterProtectedRoutes(protected)
	return r
}

func push(r *gin.Engine, path, signature string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", signature)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAPushDeploysOnlyTheProjectWhoseSecretSignedIt(t *testing.T) {
	secrets := &fakeSecrets{secrets: map[string]string{projectA: "secret-a", projectB: "secret-b"}}
	finder := &fakeFinder{calls: make(chan string, 4)}
	r := newRouter(secrets, finder, "owner", "org-a")
	body := []byte(`{"ref":"refs/heads/main","repository":{"clone_url":"https://github.com/acme/app.git","full_name":"acme/app"},"head_commit":{"id":"abc"}}`)

	w := push(r, "/api/v1/webhooks/github/"+projectB, sign("secret-b", body), body)
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	last, _ := secrets.LastPush(context.Background(), projectB)
	require.NotNil(t, last.At, "a signed push is recorded")
	assert.Equal(t, "acme/app", last.Repo)
	select {
	case got := <-finder.calls:
		assert.Equal(t, projectB+" https://github.com/acme/app.git main", got, "only project B's services are looked at")
	case <-time.After(2 * time.Second):
		t.Fatal("the push was not matched")
	}

	// Workspace A's secret cannot deploy project B, nor sign for the old URL.
	assert.Equal(t, http.StatusUnauthorized, push(r, "/api/v1/webhooks/github/"+projectB, sign("secret-a", body), body).Code)
	assert.Equal(t, http.StatusNotFound, push(r, "/api/v1/webhooks/github/33333333-3333-3333-3333-333333333333", sign("secret-a", body), body).Code)
	assert.Equal(t, http.StatusNotFound, push(r, "/api/v1/webhooks/github/not-a-uuid", sign("secret-a", body), body).Code)
	old := push(r, "/api/v1/webhooks/github", sign("secret-a", body), body)
	assert.Equal(t, http.StatusGone, old.Code)
	assert.Contains(t, old.Body.String(), "Webhook card")

	select {
	case got := <-finder.calls:
		t.Fatalf("a refused push was matched: %s", got)
	case <-time.After(100 * time.Millisecond):
	}
	last, _ = secrets.LastPush(context.Background(), projectA)
	assert.Nil(t, last.At, "a refused push is not recorded")
}

func TestTheWebhookIsForTheProjectsOwnersAndDevelopers(t *testing.T) {
	info := func(role, org, project string, header map[string]string) (int, map[string]string) {
		secrets := &fakeSecrets{secrets: map[string]string{}}
		r := newRouter(secrets, &fakeFinder{calls: make(chan string, 1)}, role, org)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+project+"/webhook", nil)
		req.Host = "panel.example.com"
		for k, v := range header {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var body struct{ Data map[string]string }
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Data
	}

	code, data := info("developer", "org-a", projectA, map[string]string{"X-Forwarded-Proto": "https"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "https://panel.example.com/api/v1/webhooks/github/"+projectA, data["url"], "behind Traefik the URL is https")
	assert.Equal(t, "new-1111", data["secret"])

	code, _ = info("viewer", "org-a", projectA, nil)
	assert.Equal(t, http.StatusForbidden, code, "a viewer never sees the secret")
	code, _ = info("owner", "org-a", projectB, nil)
	assert.Equal(t, http.StatusNotFound, code, "nor anyone outside the project's workspace")
}

func TestRotatingTheSecretRefusesPushesSignedWithTheOldOne(t *testing.T) {
	secrets := &fakeSecrets{secrets: map[string]string{projectA: "secret-a"}}
	finder := &fakeFinder{calls: make(chan string, 4)}
	r := newRouter(secrets, finder, "developer", "org-a")
	body := []byte(`{"ref":"refs/heads/main","repository":{"clone_url":"https://github.com/acme/app.git"}}`)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectA+"/webhook/rotate", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "rotated-1111")

	assert.Equal(t, http.StatusUnauthorized, push(r, "/api/v1/webhooks/github/"+projectA, sign("secret-a", body), body).Code)
	assert.Equal(t, http.StatusAccepted, push(r, "/api/v1/webhooks/github/"+projectA, sign("rotated-1111", body), body).Code)

	viewer := newRouter(secrets, finder, "viewer", "org-a")
	w = httptest.NewRecorder()
	viewer.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectA+"/webhook/rotate", nil))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestAPushRecordsItsPusherAndCommitSubject(t *testing.T) {
	if got := pushedBy(" sam "); got != "Push · sam" {
		t.Errorf("pushedBy = %q", got)
	}
	if got := pushedBy(""); got != "Push" {
		t.Errorf("pushedBy without a pusher = %q", got)
	}
	if got := firstLine("Merge !40: Structured logging\n\nLonger body"); got != "Merge !40: Structured logging" {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine(strings.Repeat("é", 300)); len([]rune(got)) != 200 {
		t.Errorf("firstLine keeps %d runes, want 200", len([]rune(got)))
	}
}

// Recording the push never stops its deploy, and a tag push is neither
// recorded nor deployed.
func TestThePushIsRecordedBesideTheDeploy(t *testing.T) {
	secrets := &fakeSecrets{secrets: map[string]string{projectA: "secret-a"}, failPush: true}
	finder := &fakeFinder{calls: make(chan string, 2)}
	r := newRouter(secrets, finder, "owner", "org-a")

	body := []byte(`{"ref":"refs/heads/main","repository":{"clone_url":"https://github.com/acme/app.git","full_name":"acme/app"},"head_commit":{"id":"abc"}}`)
	require.Equal(t, http.StatusAccepted, push(r, "/api/v1/webhooks/github/"+projectA, sign("secret-a", body), body).Code)
	select {
	case <-finder.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("a push whose recording failed was not deployed")
	}

	secrets.failPush = false
	tag := []byte(`{"ref":"refs/tags/v1","repository":{"clone_url":"https://github.com/acme/app.git","full_name":"acme/app"},"head_commit":{"id":"abc"}}`)
	require.Equal(t, http.StatusOK, push(r, "/api/v1/webhooks/github/"+projectA, sign("secret-a", tag), tag).Code)
	last, _ := secrets.LastPush(context.Background(), projectA)
	assert.Nil(t, last.At, "a tag push is not recorded")
}
