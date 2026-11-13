package code

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/actor"
)

// An event an agent caused, as a preview it started (create_preview),
// names the agent acting for the person, as its pushes do (D-100); a
// person's names no agent.
func TestPullEventNamesTheAgent(t *testing.T) {
	repo := &fakeRepo{items: map[string][]StoredItem{}}
	svc := NewService(repo, nil, nil, nil, 2222, zap.NewNop())
	agentCtx := actor.WithAgent(context.Background(), actor.Agent{ConnectionID: "conn-1", Name: "Claude Code", UserID: "bob", OrgID: "o1"})

	svc.AddPullEvent(agentCtx, "pr-1", "bob", "preview_started", map[string]any{"sha": "abc"})
	svc.AddPullEvent(agentCtx, "pr-1", "bob", "preview_stopped", nil)
	svc.AddPullEvent(context.Background(), "pr-1", "bob", "preview_updated", map[string]any{"sha": "def"})

	want := []string{"Claude Code", "Claude Code", ""}
	for i, it := range repo.items["pr-1"] {
		var d struct {
			Agent string `json:"agent"`
		}
		if err := json.Unmarshal(it.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Agent != want[i] {
			t.Errorf("%s: agent %q, want %q", it.Kind, d.Agent, want[i])
		}
	}
}

// A pull request opened with an API key records its first pushed event with
// with_key, as later pushes do (D-97); one a person opened records false.
// The executor is a stub, so this runs without a new enough git.
func TestOpenPullRecordsWithKey(t *testing.T) {
	sockDir, err := os.MkdirTemp("", "ow")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "g.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.Repeat("a", 40)
	executor := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/browse/compare") {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(gitstore.Compare{Base: gitstore.Ref{Name: "main", Kind: "branch", SHA: head}, Head: gitstore.Ref{Kind: "branch", SHA: head}, Ahead: 1,
			Conflicts: []string{}, Commits: []gitstore.Commit{}, Files: []gitstore.FileChange{}})
	})}
	go executor.Serve(ln)
	t.Cleanup(func() { executor.Close() })

	const repoID = "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11"
	repo := &fakeRepo{
		stored: []StoredRepo{{ID: repoID, Name: "shop", OrgSlug: "acme", CreatedAt: time.Now()}},
		items:  map[string][]StoredItem{},
	}
	svc := NewService(repo, gitstore.NewClient(sock), nil, func() string { return "panel.test" }, 2222, zap.NewNop())

	for i, viaKey := range []bool{true, false} {
		_, err := svc.OpenPull(context.Background(), "p1", repoID, "dev", OpenPullInput{Title: "t", Base: "main", Head: []string{"feat", "feat2"}[i], ViaKey: viaKey})
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			WithKey bool `json:"with_key"`
		}
		found := false
		for _, it := range repo.items[repo.pulls[i].ID] {
			if it.Kind == "pushed" {
				found = true
				if err := json.Unmarshal(it.Data, &d); err != nil {
					t.Fatal(err)
				}
				if d.WithKey != viaKey {
					t.Errorf("ViaKey %v: with_key %v", viaKey, d.WithKey)
				}
				break
			}
		}
		if !found {
			t.Errorf("ViaKey %v: no pushed event", viaKey)
		}
	}
}
