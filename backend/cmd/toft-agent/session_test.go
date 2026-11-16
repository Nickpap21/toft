package main

import (
	"slices"
	"strings"
	"testing"
)

// The scope's description is fixed and the command comes after "--":
// systemd's default description is the command line, and the journal
// keeps it.
func TestScopedKeepsTheCommandLineOutOfTheDescription(t *testing.T) {
	cmd := []string{"/bin/sh", "-c", "docker build --build-arg KEY=value ."}
	got := scoped("/usr/bin/systemd-run", cmd)
	sep := slices.Index(got, "--")
	if sep < 0 || !slices.Equal(got[sep+1:], cmd) {
		t.Fatalf("the command is not what follows --: %q", got)
	}
	if !slices.ContainsFunc(got[:sep], func(a string) bool { return strings.HasPrefix(a, "--description=") }) {
		t.Fatalf("no --description, so systemd describes the scope by its command line: %q", got)
	}
}
