package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A join as another server drops the previous server's desired state,
// rollouts and metrics, and keeps the agent's key and the panel's.
func TestJoinAsAnotherServerForgetsThePreviousOne(t *testing.T) {
	stateDir = t.TempDir()
	for _, p := range []string{desiredFile(), metricsFile(), filepath.Join(rolloutDir(), "dep-1.json"), keyFile(), panelKeyFile()} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	forgetServer()
	for _, p := range []string{desiredFile(), metricsFile(), rolloutDir()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s is kept", p)
		}
	}
	for _, p := range []string{keyFile(), panelKeyFile()} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
}

// The install script hands the join token over in a file (D-123): join
// reads it and deletes it.
func TestJoinReadsTheTokenFileAndDeletesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "join-token")
	if err := os.WriteFile(path, []byte("toft_join_abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := readTokenFile(path)
	if err != nil || tok != "toft_join_abc" {
		t.Fatalf("token %q, %v", tok, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the token file is kept")
	}
}

// join takes the token from --token-file as from --token. As root (CI's
// Linux container) it gets as far as agent.env; elsewhere, the root check.
func TestJoinTakesTheTokenFromTheFile(t *testing.T) {
	dir := t.TempDir()
	stateDir, envFile = filepath.Join(dir, "state"), filepath.Join(dir, "agent.env")
	path := filepath.Join(dir, "join-token")
	if err := os.WriteFile(path, []byte("toft_join_abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := join([]string{"--panel", "https://panel.example.com", "--server", "s1", "--token-file", path, "--panel-key", "SHA256:x", "--no-start"})
	if os.Geteuid() != 0 {
		if err == nil || err.Error() != "run join as root" {
			t.Fatalf("got %v, want the root check: the token was taken", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(envFile)
	if err != nil || !strings.Contains(string(env), "TOFT_JOIN_TOKEN=toft_join_abc\n") {
		t.Fatalf("agent.env %q, %v", env, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the token file is kept")
	}
}
