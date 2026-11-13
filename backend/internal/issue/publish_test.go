package issue

import (
	"context"
	"reflect"
	"testing"

	"github.com/Nickpap21/toft/backend/pkg/broker"
	"go.uber.org/zap"
)

type told struct {
	org string
	e   broker.Event
}

// tellings records what the service tells the workspace's tabs.
type tellings struct{ got []told }

func (p *tellings) Publish(orgID string, e broker.Event) { p.got = append(p.got, told{orgID, e}) }

// take returns what was told since the last take.
func (p *tellings) take() []told {
	out := p.got
	p.got = nil
	return out
}

func issuesChanged(project string) told {
	return told{"o1", broker.Changed(project, "issues")}
}

// Each change to an issue tells its workspace once, after it has committed:
// the counts that read open issues follow.
func TestChangesTellTheWorkspace(t *testing.T) {
	ctx := context.Background()
	repo := newFake()
	pub := &tellings{}
	svc := NewService(repo, fakeCode{holds: map[string][]string{}}, zap.NewNop()).WithPublisher(pub)
	expect := func(what string, want ...told) {
		t.Helper()
		if got := pub.take(); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: told %+v, want %+v", what, got, want)
		}
	}

	if _, err := svc.Create(ctx, "p1", "dev", CreateInput{Title: "One"}); err != nil {
		t.Fatal(err)
	}
	expect("create", issuesChanged("p1"))
	if _, err := svc.Create(ctx, "p1", "dev", CreateInput{Title: ""}); err == nil {
		t.Fatal("an empty title was accepted")
	}
	expect("a refused create")

	if _, err := svc.Update(ctx, "p1", 1, "dev", UpdateInput{Priority: ptr("high")}); err != nil {
		t.Fatal(err)
	}
	expect("update without a status change", issuesChanged("p1"))
	if _, err := svc.Update(ctx, "p1", 1, "dev", UpdateInput{Status: ptr(StatusCancelled)}); err != nil {
		t.Fatal(err)
	}
	expect("close", issuesChanged("p1"))
	if _, err := svc.Update(ctx, "p1", 1, "dev", UpdateInput{Status: ptr(StatusTodo)}); err != nil {
		t.Fatal(err)
	}
	expect("reopen", issuesChanged("p1"))

	if _, err := svc.Create(ctx, "p1", "dev", CreateInput{Title: "Two"}); err != nil {
		t.Fatal(err)
	}
	pub.take()
	if _, err := svc.Relate(ctx, "p1", 2, "dev", RelationInput{Kind: "relates", Issue: "STO-1"}); err != nil {
		t.Fatal(err)
	}
	expect("a relation that moves nothing")
	if _, err := svc.Relate(ctx, "p1", 2, "dev", RelationInput{Kind: "duplicates", Issue: "STO-1"}); err != nil {
		t.Fatal(err)
	}
	expect("a duplicate, cancelled", issuesChanged("p1"))

	if _, err := svc.Clone(ctx, "p1", 1, "dev", CloneInput{}); err != nil {
		t.Fatal(err)
	}
	expect("clone", issuesChanged("p1"))
}

// An issue the outbox's events move tells the workspace once per move, and
// an event that moves nothing tells nothing.
func TestMovedByEventsTellsTheWorkspace(t *testing.T) {
	ctx := context.Background()
	repo := newFake()
	pub := &tellings{}
	svc := NewService(repo, fakeCode{holds: map[string][]string{}}, zap.NewNop()).WithPublisher(pub)
	if _, err := svc.Create(ctx, "p1", "dev", CreateInput{Title: "Webhook retries"}); err != nil {
		t.Fatal(err)
	}
	pub.take()

	repo.git = append(repo.git, GitEvent{ID: 1, ProjectID: "p1", RepositoryID: "r1", Kind: "push", Branch: "fix/STO-1-x", AfterSHA: "a1", CreatedAt: repo.tick()})
	svc.ApplyEvents(ctx)
	if got, want := pub.take(), []told{issuesChanged("p1")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a push moving an issue: told %+v, want %+v", got, want)
	}

	repo.git = append(repo.git, GitEvent{ID: 2, ProjectID: "p1", RepositoryID: "r1", Kind: "push", Branch: "fix/STO-1-x", AfterSHA: "a2", CreatedAt: repo.tick()})
	svc.ApplyEvents(ctx)
	if got := pub.take(); len(got) != 0 {
		t.Fatalf("a push moving nothing: told %+v", got)
	}
}
