// Package testenv answers for what a test needs from the machine it runs on.
package testenv

import (
	"os"
	"testing"
)

// Skip skips t for a tool the machine lacks (git, its http-backend, ssh),
// except in CI (CI is set, as GitHub Actions sets it): there the tool must
// be installed, since a skip passes the gate without running the test.
func Skip(t testing.TB, format string, args ...any) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf("CI must have this; "+format, args...)
	}
	t.Skipf(format, args...)
}
