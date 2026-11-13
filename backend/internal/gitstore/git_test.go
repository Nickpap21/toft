package gitstore

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestRepoPath(t *testing.T) {
	p, err := RepoPath("/r", "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11")
	if err != nil || p != "/r/@hashed/0b/6f/0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11.git" {
		t.Fatalf("%q %v", p, err)
	}
	for _, id := range []string{"", "../../etc", "0B6F9C4E-2A51-4F0E-9D6E-6F0C2A7B9D11", "{0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11}"} {
		if _, err := RepoPath("/r", id); err == nil {
			t.Errorf("%q made a path", id)
		}
	}
}

// git runs with nothing of the process's environment: no secret the
// executor might hold reaches it, and GIT_PROTOCOL only when well formed.
func TestCommandEnvironment(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "secret")
	t.Setenv("GIT_DIR", "/elsewhere")
	allowed := map[string]bool{"PATH": true, "HOME": true, "GIT_CONFIG_NOSYSTEM": true, "GIT_CONFIG_GLOBAL": true,
		"GIT_TERMINAL_PROMPT": true, "GIT_NO_REPLACE_OBJECTS": true, "GIT_LITERAL_PATHSPECS": true, "GIT_PROTOCOL": true}
	cmd := command(context.Background(), "version=2", "version")
	for _, kv := range cmd.Env {
		if k, _, _ := strings.Cut(kv, "="); !allowed[k] {
			t.Errorf("git gets %s", kv)
		}
	}
	if !strings.Contains(strings.Join(cmd.Env, "\n"), "GIT_PROTOCOL=version=2") {
		t.Error("GIT_PROTOCOL not passed")
	}
	if bad := command(context.Background(), "version=2\nGIT_DIR=/x", "version"); strings.Contains(strings.Join(bad.Env, "\n"), "GIT_PROTOCOL") {
		t.Error("a malformed GIT_PROTOCOL was passed")
	}
	if !IsV2("version=2") || !IsV2("object-format=sha1:version=2") || IsV2("version=1") || IsV2("") {
		t.Error("IsV2")
	}
	if len(os.Getenv("ENCRYPTION_KEY")) == 0 {
		t.Fatal("setenv")
	}
}
