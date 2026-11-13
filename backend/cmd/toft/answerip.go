package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// toft answer-on-ip: the way back for a restored panel whose answer on the
// IP address was stopped in the panel while its domain pointed elsewhere
// (D-164's "Stop answering on the IP…"), which leaves nobody able to open it.

const answerOnIPUsage = `usage: toft answer-on-ip

A panel restored from a backup answers on its server's IP address until its
domain reaches it. Stopped sooner in the panel while the domain points
elsewhere, nobody can open it: this brings the answer on the IP address
back and restarts the backend. The panel ends it by itself within a few
minutes of the domain reaching it with its certificate. Run as root on the
panel host.`

func answerOnIPCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("answer-on-ip", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError(answerOnIPUsage)
	}
	if os.Geteuid() != 0 {
		return errors.New("run toft answer-on-ip as root on the panel host")
	}
	u := &upgrader{h: osHost{}, p: hostPaths, out: stdout, wait: 2 * time.Minute, poll: 3 * time.Second, now: time.Now, sleep: time.Sleep}
	return u.answerOnIP(ctx)
}

func (u *upgrader) answerOnIP(ctx context.Context) error {
	if err := os.MkdirAll(u.p.work, 0o700); err != nil {
		return err
	}
	// Not while an upgrade or a key rotation restarts the backend.
	unlock, err := lock(filepath.Join(u.p.work, "lock"))
	if err != nil {
		return err
	}
	defer unlock()
	// The backend's own command writes it: the mark is in its database.
	var out bytes.Buffer
	if err := u.h.run(ctx, &out, nil, nil, "docker", "exec", "paas-backend", "./server", "answer-on-ip"); err != nil {
		return fmt.Errorf("nothing was changed: %w: %s", err, strings.TrimSpace(out.String()))
	}
	lines := strings.Fields(out.String())
	answer := ""
	if len(lines) > 0 {
		answer = lines[len(lines)-1] // after anything the backend logs as it starts
	}
	switch answer {
	case "never-restored":
		return errors.New("this panel was not restored from a backup, so it has no answer on its IP address to bring back; nothing was changed")
	case "already":
		u.say("The panel already answers on this server's IP address, until its domain reaches it. Nothing was changed.")
		return nil
	case "ok":
	default:
		return fmt.Errorf("nothing was restarted: the backend answered %q; is it older than this toft?", answer)
	}
	if err := u.restartBackend(ctx); err != nil {
		return fmt.Errorf("it is recorded, and the panel answers on the IP address once the backend starts, but %w", err)
	}
	u.ok("The panel answers on this server's IP address again")
	u.say("Your browser warns about the certificate there. Within a few minutes of the panel's domain reaching this\nserver with its certificate, the panel answers only at its domain again.")
	return nil
}
