package doc

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

func (p *tellings) take() []told {
	out := p.got
	p.got = nil
	return out
}

// Each change to a page tells its workspace once, after it has committed:
// the Docs count follows.
func TestChangesTellTheWorkspace(t *testing.T) {
	ctx := context.Background()
	pub := &tellings{}
	var ran []string
	svc := NewService(newFake(), fakeKeys{}, fakeChanges{}, fakeExec{&ran}, zap.NewNop())
	svc.WithPublisher(pub)
	once := told{"o1", broker.Changed("p1", "docs")}
	expect := func(what string, want ...told) {
		t.Helper()
		if got := pub.take(); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: told %+v, want %+v", what, got, want)
		}
	}

	folder, err := svc.Create(ctx, "o1", "p1", "dev", CreateInput{Title: "Runbooks"})
	if err != nil {
		t.Fatal(err)
	}
	expect("create", once)
	page, err := svc.Create(ctx, "o1", "p1", "dev", CreateInput{Title: "Restore"})
	if err != nil {
		t.Fatal(err)
	}
	pub.take()
	if _, err := svc.Create(ctx, "o1", "p1", "dev", CreateInput{Title: ""}); err == nil {
		t.Fatal("an empty title was accepted")
	}
	expect("a refused create")

	if _, err := svc.Update(ctx, "o1", "p1", page.ID, "dev", UpdateInput{Title: ptr("Restore drill"), BaseVersion: 1}); err != nil {
		t.Fatal(err)
	}
	expect("a save", once)
	if _, err := svc.Update(ctx, "o1", "p1", page.ID, "dev", UpdateInput{ParentID: &folder.ID}); err != nil {
		t.Fatal(err)
	}
	expect("a move", once)
	if _, err := svc.Update(ctx, "o1", "p1", page.ID, "dev", UpdateInput{Title: ptr("Stale"), BaseVersion: 1}); err == nil {
		t.Fatal("a stale save was accepted")
	}
	expect("a refused save")

	if _, err := svc.Verify(ctx, "o1", "p1", page.ID, "dev"); err != nil {
		t.Fatal(err)
	}
	expect("verify", once)
	if err := svc.Delete(ctx, "o1", "p1", page.ID); err != nil {
		t.Fatal(err)
	}
	expect("delete", once)
	if _, err := svc.Restore(ctx, "o1", "p1", page.ID); err != nil {
		t.Fatal(err)
	}
	expect("restore", once)
}

func ptr[T any](v T) *T { return &v }
