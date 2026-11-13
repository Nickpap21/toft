package broker

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestChangedEventShape(t *testing.T) {
	e := Changed("p1", "issues", "alerts")
	got, ok := e.Payload.(ChangedPayload)
	if e.Type != "changed" || !ok || got.ProjectID != "p1" || len(got.Kinds) != 2 || got.Kinds[0] != "issues" || got.Kinds[1] != "alerts" {
		t.Fatalf("event = %+v", e)
	}
	b, err := json.Marshal(Changed("", "docs").Payload)
	if err != nil || string(b) != `{"kinds":["docs"]}` {
		t.Fatalf("payload json = %s, %v", b, err)
	}
}

// Subscribing and cancelling while publishing used to race on the inner map
// (Publish ranged over it after releasing the lock). Run under -race.
func TestPublishWhileSubscribing(t *testing.T) {
	b := New()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				b.Publish("org", Event{Type: "deployment.running"})
			}
		}
	}()
	for i := 0; i < 500; i++ {
		ch, cancel := b.Subscribe("org")
		select {
		case <-ch:
		default:
		}
		cancel()
	}
	close(stop)
	wg.Wait()
}

func TestPublishReachesSubscribersAndSkipsFullOnes(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe("org")
	defer cancel()
	for i := 0; i < 40; i++ { // buffer is 32: the extra sends are dropped, not blocked
		b.Publish("org", Event{Type: "x"})
	}
	if len(ch) != 32 {
		t.Fatalf("buffered %d events, want 32", len(ch))
	}
	b.Publish("other", Event{Type: "y"})
	if len(ch) != 32 {
		t.Fatal("an event for another workspace reached this subscriber")
	}
}
