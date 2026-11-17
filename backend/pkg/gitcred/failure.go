package gitcred

import (
	"fmt"
	"regexp"
	"strings"
)

// userinfo is the "user:token@" of a URL anywhere in a line of git output.
var userinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s]*@`)

const maxReason = 300

// Failure wraps err (a failed git command) with op and git's reason, so a
// deployment's error message says why, not only "exit status 128". The
// reason is the last "fatal:" or "error:" line of output (none, as when a
// clone is killed mid-transfer, leaves err alone rather than name a
// progress line), with any user:token@ in a URL removed and the length
// capped: the message is shown to every member of the workspace. err stays
// in the chain for errors.Is and errors.As.
func Failure(op string, output []byte, err error) error {
	reason := Reason(output)
	if reason == "" {
		return fmt.Errorf("%s: %w", op, err)
	}
	return fmt.Errorf("%s: %s (%w)", op, reason, err)
}

// Reason is the line of git's output that says why it failed, "" for none.
func Reason(output []byte) string {
	var picked string
	// git ends progress lines with \r, not \n.
	for _, line := range strings.FieldsFunc(string(output), func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "fatal:"); ok {
			picked = rest
		} else if rest, ok := strings.CutPrefix(line, "error:"); ok {
			picked = rest
		}
	}
	picked = strings.TrimSpace(userinfo.ReplaceAllString(picked, "$1"))
	if r := []rune(picked); len(r) > maxReason {
		picked = string(r[:maxReason]) + "…"
	}
	return picked
}
