package gitstore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/testenv"
)

// TestAcquireCaps: one account, or one workspace, at its cap is refused
// with a reason it can act on while another still gets a slot; the backend's
// own calls (no owner) count against the instance and repository only.
func TestAcquireCaps(t *testing.T) {
	e := NewExecutor(t.TempDir())
	e.wait = 10 * time.Millisecond
	ctx := context.Background()
	var held []func()
	take := func(repo, user, ws string) error {
		t.Helper()
		release, err := e.acquire(ctx, repo, user, ws)
		if err == nil {
			held = append(held, release)
		}
		return err
	}
	releaseAll := func() {
		for _, r := range held {
			r()
		}
		held = nil
	}

	// One account across repositories in workspaces of its own.
	for i := range maxUserOps {
		if err := take(fmt.Sprint("r", i), "u1", fmt.Sprint("w", i)); err != nil {
			t.Fatalf("u1's slot %d: %v", i, err)
		}
	}
	if err := take("r-next", "u1", "w-next"); !errors.Is(err, errUserBusy) {
		t.Errorf("u1 past its cap: %v", err)
	}
	if err := take("r-next", "u2", "w-next"); err != nil {
		t.Fatalf("u2 was refused: %v", err)
	}
	releaseAll()

	// One workspace, each operation another account's.
	for i := range maxWorkspaceOps {
		if err := take(fmt.Sprint("r", i), fmt.Sprint("u", i), "w1"); err != nil {
			t.Fatalf("w1's slot %d: %v", i, err)
		}
	}
	if err := take("r-next", "u-next", "w1"); !errors.Is(err, errWorkspaceBusy) {
		t.Errorf("w1 past its cap: %v", err)
	}
	if err := take("r-next", "u-next", "w2"); err != nil {
		t.Fatalf("w2 was refused: %v", err)
	}
	// The backend's own call still runs.
	if err := take("r-other", "", ""); err != nil {
		t.Fatalf("an ownerless call was refused: %v", err)
	}
	releaseAll()
	if len(e.held) != 0 || len(e.ops) != 0 {
		t.Fatalf("slots left after every release: %v, %d", e.held, len(e.ops))
	}
}

// TestAcquireConcurrent: the accounting stays exact with callers racing
// (run with -race).
func TestAcquireConcurrent(t *testing.T) {
	e := NewExecutor(t.TempDir())
	e.wait = time.Millisecond
	var wg sync.WaitGroup
	for i := range 200 {
		wg.Go(func() {
			if release, err := e.acquire(context.Background(), fmt.Sprint("r", i%5), fmt.Sprint("u", i%7), fmt.Sprint("w", i%3)); err == nil {
				release()
			}
		})
	}
	wg.Wait()
	if len(e.held) != 0 || len(e.ops) != 0 {
		t.Fatalf("slots left after every release: %v, %d", e.held, len(e.ops))
	}
}

// TestTwoAccountsLeaveClientSlots: two accounts, each at its cap, leave a
// third a slot.
func TestTwoAccountsLeaveClientSlots(t *testing.T) {
	e := NewExecutor(t.TempDir())
	e.wait = 10 * time.Millisecond
	ctx := context.Background()
	var held []func()
	defer func() {
		for _, r := range held {
			r()
		}
	}()
	for _, u := range []string{"u1", "u2"} {
		for i := 0; ; i++ {
			release, err := e.acquire(ctx, fmt.Sprint(u, "r", i), u, fmt.Sprint(u, "w", i))
			if err != nil {
				break
			}
			held = append(held, release)
		}
	}
	release, err := e.acquire(ctx, "r-next", "u3", "w-next")
	if err != nil {
		t.Fatalf("two accounts (%d slots) left a third none: %v", len(held), err)
	}
	held = append(held, release)
}

// TestPktWatch: the negotiation ends at upload-pack's first side-band
// packet (not at a ref named PACK) or a raw pack, and at the flush after
// receive-pack's commands, whatever the writes' boundaries.
func TestPktWatch(t *testing.T) {
	sideband := func(h []byte) bool { return len(h) == 5 && h[4] >= 1 && h[4] <= 3 }
	flush := func(h []byte) bool { return string(h) == "0000" }
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	oid := strings.Repeat("a", 40)
	advert := pkt(oid+" refs/heads/PACKAGE\x00side-band-64k\n") + "0000" + pkt("NAK\n")
	for _, tc := range []struct {
		name, before, at string
		begun            func([]byte) bool
	}{
		{"upload-pack side-band", advert, pkt("\x01"), sideband},
		{"upload-pack progress", advert, pkt("\x02Enumerating objects"), sideband},
		{"upload-pack raw pack", advert, "PACK", sideband},
		{"receive-pack commands", pkt(oid + " " + oid + " refs/heads/main\x00 report-status\n"), "0000", flush},
	} {
		for _, step := range []int{1, 3, 1 << 10} {
			p := &pktWatch{deadline: time.AfterFunc(time.Hour, func() {}), begun: tc.begun}
			feed := func(s string) {
				for len(s) > 0 {
					k := min(step, len(s))
					p.Write([]byte(s[:k]))
					s = s[k:]
				}
			}
			feed(tc.before)
			if p.done {
				t.Fatalf("%s (writes of %d): ended before %q", tc.name, step, tc.at)
			}
			feed(tc.at)
			if !p.done || p.deadline.Stop() {
				t.Fatalf("%s (writes of %d): still negotiating after %q", tc.name, step, tc.at)
			}
		}
	}
}

// TestSlowNegotiationLosesItsSlot: a client that trickles its negotiation,
// never quiet long enough for the idle limit, is cut off at the
// negotiation cap, and its slot is free again.
func TestSlowNegotiationLosesItsSlot(t *testing.T) {
	if _, err := CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git %d.%d or newer: %v", MinVersion[0], MinVersion[1], err)
	}
	e := NewExecutor(t.TempDir())
	e.negotiate = time.Second
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	pr, pw := io.Pipe()
	defer pr.Close() // ends the writer below
	go func() {
		// A packet's length, then its payload a byte at a time (25 s in
		// all): upload-pack waits for the rest of it.
		if _, err := io.WriteString(pw, "0100"); err != nil {
			return
		}
		for range 0x100 - 4 {
			time.Sleep(100 * time.Millisecond)
			if _, err := pw.Write([]byte("a")); err != nil {
				return
			}
		}
	}()
	start := time.Now()
	resp, err := http.Post(srv.URL+"/v1/repos/0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11/upload-pack?mode=stream", "application/octet-stream", pr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("a slow negotiation ran %s", took)
	}
	stderr, _ := base64.StdEncoding.DecodeString(resp.Trailer.Get("Toft-Git-Stderr"))
	if resp.Trailer.Get("Toft-Git-Exit") == "0" || !strings.Contains(string(stderr), "ran too long") {
		t.Errorf("not cut off: exit %q, %s", resp.Trailer.Get("Toft-Git-Exit"), stderr)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.held) != 0 || len(e.ops) != 0 {
		t.Fatalf("slots left after the call: %v, %d", e.held, len(e.ops))
	}
}

// TestAcquireKeepsSlotsForTheBackend:clients together, whatever their
// accounts and workspaces, leave the backend's own calls (deploys,
// mirrors) slots of their own.
func TestAcquireKeepsSlotsForTheBackend(t *testing.T) {
	e := NewExecutor(t.TempDir())
	e.wait = 10 * time.Millisecond
	ctx := context.Background()
	var held []func()
	defer func() {
		for _, r := range held {
			r()
		}
	}()
	for i := range maxClientOps {
		release, err := e.acquire(ctx, fmt.Sprint("r", i), fmt.Sprint("u", i), fmt.Sprint("w", i))
		if err != nil {
			t.Fatalf("client slot %d: %v", i, err)
		}
		held = append(held, release)
	}
	if _, err := e.acquire(ctx, "r-next", "u-next", "w-next"); !errors.Is(err, ErrBusy) {
		t.Errorf("a client past the clients' share: %v", err)
	}
	for i := range maxOps - maxClientOps {
		release, err := e.acquire(ctx, fmt.Sprint("backend", i), "", "")
		if err != nil {
			t.Fatalf("the backend's slot %d: %v", i, err)
		}
		held = append(held, release)
	}
}
