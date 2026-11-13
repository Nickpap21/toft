package docker

import (
	"strings"
	"testing"
)

// A helper's output reaches logFn a line at a time, however Docker splits
// it, and only its last MiB is returned.
func TestHostOutputLines(t *testing.T) {
	var lines []string
	w := &helperOutput{logFn: func(l string) { lines = append(lines, l) }}
	for _, chunk := range []string{"[1/5] Check", "ing\r\n  ✓ ok\n[2/5", "] Repo"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	w.flush()
	if got, want := strings.Join(lines, "|"), "[1/5] Checking|  ✓ ok|[2/5] Repo"; got != want {
		t.Fatalf("lines = %q, want %q", got, want)
	}

	quiet := &helperOutput{}
	_, _ = quiet.Write([]byte(strings.Repeat("x", helperOutputKeep)))
	_, _ = quiet.Write([]byte("\nthe error"))
	if len(quiet.buf) != helperOutputKeep || !strings.HasSuffix(string(quiet.buf), "\nthe error") {
		t.Fatalf("kept %d bytes ending %q", len(quiet.buf), quiet.buf[len(quiet.buf)-10:])
	}
}
