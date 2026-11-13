// Package listversion is the version of a list a client replaces whole: the
// read hands it out, the replace must send it back, and a replace that
// doesn't match what is stored writes nothing. So a stale or failed read can
// never delete what its client didn't see.
package listversion

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
)

var (
	// ErrRequired is a replace that sent no version: it can't show it read
	// what it would delete.
	ErrRequired = errors.New("send the version from the list you read")

	// ErrChanged is a replace whose version isn't what is stored now:
	// nothing is written.
	ErrChanged = errors.New("this list changed since it was read; nothing was saved")
)

// Of digests a list's rows, one line each, in any order. The lines must not
// contain a newline (%q a free-form field). An empty list has its own
// version, so a first save works; a client whose read failed has none.
func Of(lines []string) string {
	sorted := slices.Sorted(slices.Values(lines))
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}
