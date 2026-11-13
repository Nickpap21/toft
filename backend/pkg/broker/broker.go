package broker

import (
	"sync"
	"sync/atomic"
)

// Event is a domain event published to all subscribers of an org.
type Event struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

// ChangedPayload is the data of a "changed" event: what the workspace's
// counts and lists read moved. Kinds are "issues", "docs", "alerts" and
// "services"; ProjectID names the project concerned, when one is.
type ChangedPayload struct {
	Kinds     []string `json:"kinds"`
	ProjectID string   `json:"project_id,omitempty"`
}

// Changed is the event telling a workspace's open tabs to refetch what
// kinds name. The one place its shape is built.
func Changed(projectID string, kinds ...string) Event {
	return Event{Type: "changed", Payload: ChangedPayload{Kinds: kinds, ProjectID: projectID}}
}

// Broker is an org-scoped in-memory pub/sub bus.
// Each subscriber gets its own buffered channel; slow consumers are skipped
// so a stalled browser tab never blocks the deployment pipeline.
type Broker struct {
	mu   sync.RWMutex
	subs map[string]map[int64]chan Event // orgID → subID → ch
	next atomic.Int64
}

func New() *Broker {
	return &Broker{subs: make(map[string]map[int64]chan Event)}
}

// Subscribe returns a receive-only event channel and a cancel func.
// The caller MUST call cancel (via defer) when done consuming.
func (b *Broker) Subscribe(orgID string) (<-chan Event, func()) {
	id := b.next.Add(1)
	ch := make(chan Event, 32)

	b.mu.Lock()
	if b.subs[orgID] == nil {
		b.subs[orgID] = make(map[int64]chan Event)
	}
	b.subs[orgID][id] = ch
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		delete(b.subs[orgID], id)
		if len(b.subs[orgID]) == 0 {
			delete(b.subs, orgID)
		}
		b.mu.Unlock()
		// drain so any blocked send can complete
		for len(ch) > 0 {
			<-ch
		}
	}
}

// Publish sends an event to all subscribers of orgID.
// Non-blocking: slow consumers are skipped. The read lock is held while
// ranging: Subscribe and its cancel write the same inner map, and Go aborts
// the process on a concurrent map iteration and write. The sends never
// block, so a publisher holds the lock for microseconds.
func (b *Broker) Publish(orgID string, e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs[orgID] {
		select {
		case ch <- e:
		default: // slow consumer — drop; never block the publisher
		}
	}
}
