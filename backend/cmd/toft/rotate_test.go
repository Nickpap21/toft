package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const oldKey = "0123456789abcdef0123456789abcdef" // gitleaks:allow

// rotating is a panel host running v0.28.0 with ENCRYPTION_KEY=key.
func rotating(t *testing.T, key string) (*fakeHost, *upgrader) {
	h, u, _ := installed(t)
	if err := setEnv(u.p.secrets, "ENCRYPTION_KEY", key); err != nil {
		t.Fatal(err)
	}
	h.calls = nil
	return h, u
}

func keysOf(t *testing.T, u *upgrader) (current, previous string) {
	t.Helper()
	env, err := readEnv(u.p.secrets)
	if err != nil {
		t.Fatal(err)
	}
	return env["ENCRYPTION_KEY"], env["ENCRYPTION_KEY_PREVIOUS"]
}

func TestRotate_Fresh(t *testing.T) {
	h, u := rotating(t, oldKey)
	write(t, filepath.Join(u.p.units, "var/paas/backup/backup.env"), "x")
	if err := u.rotateKey(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, u.out)
	}
	current, previous := keysOf(t, u)
	if current == oldKey || len(current) != 64 || previous != "" {
		t.Fatalf("keys = %q, previous %q", current, previous)
	}
	if n := countSub(h.calls, "up -d --no-deps --force-recreate backend"); n != 2 {
		t.Errorf("backend restarted %d times, want 2 (both keys, then the new one alone)", n)
	}
	if h.did("docker exec paas-backend ./server reseal --check") != 1 || h.did("systemd-run --unit toft-backup-rotation --collect /usr/local/lib/toft/backup.sh now") != 1 {
		t.Errorf("calls = %v", h.calls)
	}
}

// A rotation interrupted after writing the keys resumes with them: no third key.
func TestRotate_Resumes(t *testing.T) {
	h, u := rotating(t, "fedcba9876543210fedcba9876543210")
	if err := setEnv(u.p.secrets, "ENCRYPTION_KEY_PREVIOUS", oldKey); err != nil {
		t.Fatal(err)
	}
	if err := u.rotateKey(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, u.out)
	}
	if current, previous := keysOf(t, u); current != "fedcba9876543210fedcba9876543210" || previous != "" {
		t.Fatalf("keys = %q, previous %q", current, previous)
	}
	if h.did("systemd-run") != 0 {
		t.Error("a backup started without backups set up")
	}
}

// Values left under the old key: it stays, and the command says so.
func TestRotate_CheckFailsKeepsThePreviousKey(t *testing.T) {
	h, u := rotating(t, oldKey)
	h.checkFails = true
	err := u.rotateKey(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ENCRYPTION_KEY_PREVIOUS stays") {
		t.Fatalf("err = %v", err)
	}
	if current, previous := keysOf(t, u); current == oldKey || previous != oldKey {
		t.Fatalf("keys = %q, previous %q", current, previous)
	}
	if n := countSub(h.calls, "--force-recreate backend"); n != 1 {
		t.Errorf("backend restarted %d times, want 1", n)
	}
}

// From v0.28.0 an upgrade starts from v0.27.0 or later, and replaces the
// example's ENCRYPTION_KEY (D-126).
func TestUpgrade_SealingRelease(t *testing.T) {
	h := newFakeHost(t)
	u, out := newUpgrader(t, h)
	if err := u.upgrade(context.Background(), "v0.26.0", false); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	h.calls = nil
	err := u.upgrade(context.Background(), "v0.28.0", false)
	if err == nil || !strings.Contains(err.Error(), "upgrade to v0.27.0 first") {
		t.Fatalf("err = %v", err)
	}
	if h.did("docker pull") != 0 || tag(t, u) != "v0.26.0" {
		t.Fatalf("a refused upgrade went on: %v", h.calls)
	}
	// An adopted panel's images are older than v0.27.0 too.
	st := u.readState()
	st.Version = "adopted"
	u.writeState(st)
	if err := u.upgrade(context.Background(), "v0.28.0", false); err == nil || !strings.Contains(err.Error(), "upgrade to v0.27.0 first") {
		t.Fatalf("from adopted: err = %v", err)
	}

	h = newFakeHost(t)
	u, out = newUpgrader(t, h)
	if err := setEnv(u.p.secrets, "ENCRYPTION_KEY", "change-me-to-a-random-32-char-string"); err != nil {
		t.Fatal(err)
	}
	if err := u.upgrade(context.Background(), "v0.27.0", false); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if current, _ := keysOf(t, u); current != "change-me-to-a-random-32-char-string" {
		t.Fatal("v0.27.0 doesn't replace the key")
	}
	if err := u.upgrade(context.Background(), "v0.28.0", false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if current, previous := keysOf(t, u); len(current) != 64 || previous != "" {
		t.Fatalf("keys = %q, previous %q", current, previous)
	}
	if !strings.Contains(out.String(), "Download the recovery kit again") {
		t.Errorf("output:\n%s", out)
	}
}

func countSub(calls []string, sub string) int {
	n := 0
	for _, c := range calls {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

// An upgrade to a sealing release finishes a rotation an earlier one left
// unfinished (it rolled back after replacing the placeholder): the key
// stays, and the previous one goes.
func TestUpgrade_FinishesAnUnfinishedRotation(t *testing.T) {
	h := newFakeHost(t)
	u, out := newUpgrader(t, h)
	if err := u.upgrade(context.Background(), "v0.27.0", false); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	key := strings.Repeat("ab", 32)
	if err := setEnv(u.p.secrets, "ENCRYPTION_KEY", key, "ENCRYPTION_KEY_PREVIOUS", "change-me-to-a-random-32-char-string"); err != nil {
		t.Fatal(err)
	}
	if err := u.upgrade(context.Background(), "v0.28.0", false); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if current, previous := keysOf(t, u); current != key || previous != "" {
		t.Fatalf("keys = %q, previous %q", current, previous)
	}
	if strings.Contains(out.String(), "the example's placeholder") {
		t.Errorf("said the current key is the placeholder:\n%s", out)
	}
}

// toft answer-on-ip restarts the backend only once the backend recorded it.
func TestAnswerOnIP(t *testing.T) {
	for answer, want := range map[string]struct {
		restarts int
		err, out string
	}{
		"ok":             {1, "", "answers on this server's IP address again"},
		"already":        {0, "", "already answers"},
		"never-restored": {0, "was not restored from a backup", ""},
		"":               {0, "older than this toft", ""},
	} {
		h, u := rotating(t, oldKey)
		h.answerIP = answer
		err := u.answerOnIP(context.Background())
		if (err == nil) != (want.err == "") || err != nil && !strings.Contains(err.Error(), want.err) {
			t.Errorf("%q: err = %v, want %q", answer, err, want.err)
		}
		if n := countSub(h.calls, "--force-recreate backend"); n != want.restarts {
			t.Errorf("%q: backend restarted %d times, want %d", answer, n, want.restarts)
		}
		if !strings.Contains(fmt.Sprint(u.out), want.out) {
			t.Errorf("%q: output:\n%s", answer, u.out)
		}
	}
}
