package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// toft rotate-encryption-key (D-126): replaces ENCRYPTION_KEY. Every value
// names the key it is sealed with, and the backend opens values under
// ENCRYPTION_KEY_PREVIOUS while it is set, so a crash at any step leaves
// every secret readable, and running the command again resumes.

const rotateUsage = `usage: toft rotate-encryption-key

Writes a new ENCRYPTION_KEY to /etc/toft/secrets.env and keeps the old one
as ENCRYPTION_KEY_PREVIOUS; restarts the backend, whose start seals every
secret under the new key; checks that none is left under the old one; then
removes the old key and starts a backup. Running it again resumes an
unfinished rotation. Run as root on the panel host.`

func rotateCmd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("rotate-encryption-key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError(rotateUsage)
	}
	if os.Geteuid() != 0 {
		return errors.New("run toft rotate-encryption-key as root on the panel host")
	}
	u := &upgrader{h: osHost{}, p: hostPaths, out: stdout, wait: 2 * time.Minute, poll: 3 * time.Second, now: time.Now, sleep: time.Sleep}
	return u.rotateKey(ctx)
}

func (u *upgrader) rotateKey(ctx context.Context) error {
	if err := os.MkdirAll(u.p.work, 0o700); err != nil {
		return err
	}
	unlock, err := lock(filepath.Join(u.p.work, "lock"))
	if err != nil {
		return err
	}
	defer unlock()
	if err := u.newKey(); err != nil {
		return err
	}
	return u.finishRotation(ctx)
}

// newKey writes a new ENCRYPTION_KEY and the old one as
// ENCRYPTION_KEY_PREVIOUS, in one write. While a rotation is unfinished
// (ENCRYPTION_KEY_PREVIOUS set) it writes nothing, so there are never
// three keys.
func (u *upgrader) newKey() error {
	env, err := readEnv(u.p.secrets)
	if err != nil {
		return fmt.Errorf("read %s: %w", u.p.secrets, err)
	}
	if env["ENCRYPTION_KEY_PREVIOUS"] != "" {
		u.say("Resuming the rotation: ENCRYPTION_KEY_PREVIOUS is set")
		return nil
	}
	if env["ENCRYPTION_KEY"] == "" {
		return fmt.Errorf("%s has no ENCRYPTION_KEY", u.p.secrets)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("generate a key: %w", err)
	}
	if err := setEnv(u.p.secrets, "ENCRYPTION_KEY", hex.EncodeToString(b), "ENCRYPTION_KEY_PREVIOUS", env["ENCRYPTION_KEY"]); err != nil {
		return fmt.Errorf("write the new key: %w", err)
	}
	u.ok("New ENCRYPTION_KEY written; the old one kept as ENCRYPTION_KEY_PREVIOUS")
	return nil
}

// finishRotation restarts the backend, whose start seals every secret under
// the new key, checks that none is left under another, then removes the old
// key and starts a backup under the new one.
func (u *upgrader) finishRotation(ctx context.Context) error {
	if err := u.restartBackend(ctx); err != nil {
		return fmt.Errorf("%w; ENCRYPTION_KEY_PREVIOUS stays, and running toft rotate-encryption-key again resumes", err)
	}
	u.ok("Backend restarted; its start sealed the secrets under the new key")
	var out bytes.Buffer
	if err := u.h.run(ctx, &out, nil, nil, "docker", "exec", "paas-backend", "./server", "reseal", "--check"); err != nil {
		u.fail("Some values are not under the new key:")
		_, _ = io.Copy(u.out, &out)
		return fmt.Errorf("values left under another key; ENCRYPTION_KEY_PREVIOUS stays: %w", err)
	}
	u.ok("Every value is sealed under the new key")
	if err := setEnv(u.p.secrets, "ENCRYPTION_KEY_PREVIOUS", ""); err != nil {
		return fmt.Errorf("remove ENCRYPTION_KEY_PREVIOUS: %w", err)
	}
	if err := u.restartBackend(ctx); err != nil {
		return fmt.Errorf("ENCRYPTION_KEY_PREVIOUS is removed, but %w", err)
	}
	u.ok("Old key removed")
	// backup.sh runs once a day from its timer; "now" runs it at once. Its
	// settings are in <TOFT_DATA_DIR>/backup (installHost).
	env, err := readEnv(u.p.secrets)
	if err != nil {
		return err
	}
	dataDir := env["TOFT_DATA_DIR"]
	if dataDir == "" {
		dataDir = "/var/paas"
	}
	if _, err := os.Stat(filepath.Join(u.p.units, dataDir, "backup/backup.env")); err == nil {
		if err := u.h.run(ctx, io.Discard, nil, nil, "systemd-run", "--unit", "toft-backup-rotation", "--collect", "/usr/local/lib/toft/backup.sh", "now"); err != nil {
			u.fail("Backup not started: %v", err)
		} else {
			u.ok("Backup started, under the new key")
		}
	}
	u.say("\nDownload the recovery kit again: it holds the new key. Backups from before now restore with the old kit.")
	return nil
}

// restartBackend recreates the backend, so it reads secrets.env again, and
// waits for it to be healthy.
func (u *upgrader) restartBackend(ctx context.Context) error {
	out, err := u.dockerOut(ctx, "inspect", "--format", "{{.Image}}", "paas-backend", "paas-frontend")
	if err != nil {
		return fmt.Errorf("inspect the stack: %w", err)
	}
	ids := strings.Fields(out)
	if len(ids) != 2 {
		return errors.New("the backend and the frontend aren't both running")
	}
	if err := u.compose(ctx, u.p.current, "up", "-d", "--no-deps", "--force-recreate", "backend"); err != nil {
		return fmt.Errorf("restart the backend: %w", err)
	}
	if err := u.healthy(ctx, ids); err != nil {
		return fmt.Errorf("the backend is not healthy (docker logs paas-backend): %w", err)
	}
	return nil
}
