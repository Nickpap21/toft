package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLoki records the lines pushed to it, failing the first fail pushes
// and refusing the next tooOld as Loki refuses lines older than it takes.
type fakeLoki struct {
	*httptest.Server
	mu     sync.Mutex
	fail   int
	tooOld int
	pushes int
	lines  []string
	labels []map[string]string
	tenant []string
}

func newFakeLoki(t *testing.T, fail int) *fakeLoki {
	l := &fakeLoki{fail: fail}
	l.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if r.URL.Path != "/loki/api/v1/push" {
			http.NotFound(w, r)
			return
		}
		l.pushes++
		if l.fail > 0 {
			l.fail--
			http.Error(w, "ingester unavailable", http.StatusServiceUnavailable)
			return
		}
		if l.tooOld > 0 {
			l.tooOld--
			http.Error(w, "entry for stream '{service_id=\"x\"}' has timestamp too old: 2026-10-12T10:00:00Z, "+
				"oldest acceptable timestamp is: 2026-10-28T10:00:00Z", http.StatusBadRequest)
			return
		}
		var body lokiPush
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		l.tenant = append(l.tenant, r.Header.Get("X-Scope-OrgID"))
		for _, s := range body.Streams {
			l.labels = append(l.labels, s.Stream)
			for _, v := range s.Values {
				l.lines = append(l.lines, v[1])
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(l.Close)
	return l
}

func (l *fakeLoki) got() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// base is an hour ago: within the lines Loki takes.
var base = time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

func at(sec int) string {
	return base.Add(time.Duration(sec) * time.Second).Format(time.RFC3339Nano)
}

// runShipper runs a shipper of testSvc's logs to loki until the returned
// stop is called, which waits for it; retries gets each backoff's attempt.
func runShipper(t *testing.T, f *fakedocker.Docker, loki string, retries *[]int) (stop func()) {
	s := newShipper(f.Client())
	s.services = func() []agentproto.DesiredService {
		return []agentproto.DesiredService{{ID: testSvc, Running: true, Loki: loki + "/"}}
	}
	s.poll, s.wait = 20*time.Millisecond, 20*time.Millisecond
	s.retry = func(n int) time.Duration {
		*retries = append(*retries, n) // only the one follower writes it
		return 10 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.watch(ctx)
		close(done)
	}()
	return func() {
		cancel()
		<-done
	}
}

// The agent ships each line once: through a Loki outage (backing off), an
// agent restart (from its cursor, lines of one time included), and lines
// that rotated away while it was down (counted); a container that goes
// takes its follower and its cursor with it.
func TestShipperShipsEachLineOnce(t *testing.T) {
	stateDir = t.TempDir()
	f := fakedocker.New(t)
	name := rollout.BlueContainerName(testSvc, "web", "")
	c := f.Add(name, container.StateRunning, map[string]string{rollout.LabelServiceID: testSvc})
	f.Add("tenant-thing", container.StateRunning, map[string]string{rollout.LabelServiceID: testSvc}) // not a slot's name
	f.Log(name, at(1)+" one", at(2)+" two", at(3)+" three")
	f.Log("tenant-thing", at(1)+" not ours")
	loki := newFakeLoki(t, 2)
	var retries []int

	stop := runShipper(t, f, loki.URL, &retries)
	require.Eventually(t, func() bool { return len(loki.got()) >= 3 }, 5*time.Second, 10*time.Millisecond)
	f.Log(name, at(4)+" four", at(4)+" four again")
	// The cursor is written once Loki has answered, after it counted the lines.
	want := cursor{Time: base.Add(4 * time.Second), Seen: 2}
	require.Eventually(t, func() bool { return len(loki.got()) >= 5 && readCursor(c.ID) == want }, 5*time.Second, 10*time.Millisecond)
	stop()
	assert.Equal(t, []string{"one", "two", "three", "four", "four again"}, loki.got())
	assert.Equal(t, []int{1, 2}, retries, "Loki down twice: two backoffs")
	assert.Equal(t, map[string]string{"container_name": name, "service_id": testSvc}, loki.labels[0])
	assert.Empty(t, loki.tenant[0], "no tenant header")

	// Restarted: from the cursor, the two lines at its time not again.
	f.Log(name, at(5)+" five")
	stop = runShipper(t, f, loki.URL, &retries)
	require.Eventually(t, func() bool { return len(loki.got()) >= 6 }, 5*time.Second, 10*time.Millisecond)
	stop()
	assert.Equal(t, []string{"one", "two", "three", "four", "four again", "five"}, loki.got())

	// Down while Docker rotated everything up to six away.
	f.Mu.Lock()
	c.Logs = []string{at(7) + " seven"}
	f.Mu.Unlock()
	stop = runShipper(t, f, loki.URL, &retries)
	require.Eventually(t, func() bool { return len(loki.got()) >= 7 && readCursor(c.ID).Gaps == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "seven", loki.got()[6])

	// The container goes: its follower ends and its cursor goes.
	require.NoError(t, f.Client().ContainerRemove(context.Background(), c.ID, container.RemoveOptions{Force: true}))
	require.Eventually(t, func() bool {
		_, err := os.Stat(cursorFile(c.ID))
		return os.IsNotExist(err)
	}, 5*time.Second, 10*time.Millisecond)
	stop()
	assert.Len(t, loki.got(), 7)
}

// With no cursor yet, the agent ships no line older than Loki takes, however
// far back Docker's files go.
func TestShipperStartsWithinLokisWindow(t *testing.T) {
	stateDir = t.TempDir()
	f := fakedocker.New(t)
	name := rollout.BlueContainerName(testSvc, "web", "")
	f.Add(name, container.StateRunning, map[string]string{rollout.LabelServiceID: testSvc})
	old := func(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339Nano) }
	f.Log(name, old(23*24*time.Hour)+" october", old(lokiMaxAge+time.Minute)+" just too old", at(1)+" recent")
	loki := newFakeLoki(t, 0)
	var retries []int

	stop := runShipper(t, f, loki.URL, &retries)
	require.Eventually(t, func() bool { return len(loki.got()) >= 1 }, 5*time.Second, 10*time.Millisecond)
	stop()
	assert.Equal(t, []string{"recent"}, loki.got())
}

// A batch Loki refuses as too old counts as shipped: the cursor moves past
// it and it is never sent again. (Other failures are retried:
// TestShipperShipsEachLineOnce.)
func TestShipperSkipsLinesLokiFindsTooOld(t *testing.T) {
	stateDir = t.TempDir()
	f := fakedocker.New(t)
	name := rollout.BlueContainerName(testSvc, "web", "")
	c := f.Add(name, container.StateRunning, map[string]string{rollout.LabelServiceID: testSvc})
	f.Log(name, at(1)+" one", at(2)+" two")
	loki := newFakeLoki(t, 0)
	loki.tooOld = 1
	var retries []int

	stop := runShipper(t, f, loki.URL, &retries)
	want := cursor{Time: base.Add(2 * time.Second), Seen: 1}
	require.Eventually(t, func() bool { return readCursor(c.ID) == want }, 5*time.Second, 10*time.Millisecond)
	f.Log(name, at(3)+" three")
	require.Eventually(t, func() bool { return len(loki.got()) >= 1 }, 5*time.Second, 10*time.Millisecond)
	stop()
	assert.Equal(t, []string{"three"}, loki.got(), "the refused lines are not sent again")
	loki.mu.Lock()
	assert.Equal(t, 2, loki.pushes)
	loki.mu.Unlock()
	assert.Empty(t, retries, "a too-old refusal is no failure")
}
