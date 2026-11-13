package instance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// RecoveryKit is what restores the panel on a new server (D-62, D-64): the
// key that decrypts the secrets in its database, and where its backups are.
// Service secrets stay inside the database; the JWT secret is left out, so
// a restore signs everyone out.
type RecoveryKit struct {
	Filename       string     `json:"filename"`
	Text           string     `json:"text"`
	Domain         string     `json:"domain"`
	Version        string     `json:"version"`
	EncryptionKey  string     `json:"encryption_key"`
	Backup         *KitBackup `json:"backup"`
	RestoreCommand string     `json:"restore_command"`
	// Fingerprint names what this kit restores; PATCH /instance/setup sends
	// it back when the kit is acknowledged (D-163).
	Fingerprint string `json:"fingerprint"`
}

// KitBackup is the panel host's backup destination as the kit names it. The
// S3 secret key stays out: the restore asks for it.
type KitBackup struct {
	Repository string `json:"repository"`
	Password   string `json:"password"`
	AccessKey  string `json:"access_key"`
}

// KitBackupReader reads the panel host's backup destination with its
// repository password decrypted; nil when none is set.
type KitBackupReader interface {
	PanelKitBackup(ctx context.Context) (*KitBackup, error)
}

// WithRecoveryKit sets what the kit holds besides the panel's domain.
func (s *Service) WithRecoveryKit(encryptionKey, version string, backups KitBackupReader) *Service {
	s.kitKey = encryptionKey
	s.kitVersion = version
	s.kitBackups = backups
	return s
}

// RecoveryKit returns the kit. Operators only; the handler asks for a
// recent password confirmation first (D-23), and every read is audited.
func (s *Service) RecoveryKit(ctx context.Context, actorID string) (*RecoveryKit, error) {
	if err := s.requireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	if s.kitKey == "" {
		return nil, fmt.Errorf("recovery kit not configured")
	}
	backup, err := s.kitBackup(ctx)
	if err != nil {
		return nil, err
	}
	kit := &RecoveryKit{
		Filename:      "toft-recovery.txt",
		Domain:        s.PanelDomain(),
		Version:       s.kitVersion,
		EncryptionKey: s.kitKey,
		Backup:        backup,
		Fingerprint:   kitFingerprint(s.kitKey, backup),
	}
	if kit.Domain != "" {
		kit.Filename = "toft-recovery-" + kit.Domain + ".txt"
	}
	kit.RestoreCommand = "sh install.sh --restore --kit " + kit.Filename
	kit.Text = kit.text(time.Now().UTC())
	s.audit(ctx, actorID, "recovery_kit.viewed", "", nil)
	return kit, nil
}

// kitBackup reads the panel host's backup destination as the kit names it;
// nil without one.
func (s *Service) kitBackup(ctx context.Context) (*KitBackup, error) {
	if s.kitBackups == nil {
		return nil, nil
	}
	backup, err := s.kitBackups.PanelKitBackup(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the panel host's backup: %w", err)
	}
	return backup, nil
}

// kitFingerprint names what a kit restores (D-163): the four lines
// install.sh --restore reads, which are the encryption key and the backup's
// repository, password and access key. It is an HMAC keyed with the
// encryption key, so the panel can keep it beside the kit's date without
// keeping any of the four: it says only whether they are still the same.
func kitFingerprint(key string, backup *KitBackup) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("toft recovery kit"))
	if backup != nil {
		for _, v := range []string{backup.Repository, backup.Password, backup.AccessKey} {
			// The length first: no two sets of values write the same bytes.
			fmt.Fprintf(mac, "\x00%d:%s", len(v), v)
		}
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// kitOutOfDate reports whether the stored kit no longer restores this
// panel. A kit stored before fingerprints were kept is out of date once the
// panel host has a backup destination: nothing says that kit names it.
// Stopping backups doesn't make it so: the stored kit still opens the copies
// in the bucket, and a new one names none.
func (s *Service) kitOutOfDate(ctx context.Context, st setupState) bool {
	if st.KitAcknowledgedAt == nil || s.kitKey == "" {
		return false
	}
	backup, err := s.kitBackup(ctx)
	if err != nil {
		// Setup's other answers don't wait on the backup's settings.
		s.log.Warn("panel host's backup not read; the stored recovery kit is not checked", zap.Error(err))
		return false
	}
	if st.KitFingerprint == "" {
		return backup != nil
	}
	current := kitFingerprint(s.kitKey, backup)
	return st.KitFingerprint != current && (backup != nil || st.KitKeyFingerprint != current)
}

// text is the kit as the file the operator stores. install.sh --restore
// reads its "Key: value" lines.
func (k *RecoveryKit) text(now time.Time) string {
	var b strings.Builder
	line := func(key, value string) { fmt.Fprintf(&b, "%-20s %s\n", key+":", value) }
	b.WriteString("Toft recovery kit\n\n")
	b.WriteString("Keep this file off the server it describes: in a password manager, or\nprinted and locked away. Anyone who has it and a copy of the backups can\nread every secret the panel stores.\n\n")
	domain := k.Domain
	if domain == "" {
		domain = "(none: the panel answers on its IP address)"
	}
	line("Panel", domain)
	line("Version", k.Version)
	line("Created", now.Format("2006-01-02 15:04 UTC"))
	b.WriteString("\n")
	line("Encryption key", k.EncryptionKey)
	b.WriteString("\n")
	if k.Backup == nil {
		b.WriteString("Backups are not set up. Set them up under Settings → Backups and download\nthis kit again: it then names the repository and its password.\n")
	} else {
		line("Backup repository", k.Backup.Repository)
		line("Repository password", k.Backup.Password)
		line("Access key", k.Backup.AccessKey)
		b.WriteString("The secret key is with your storage provider; the restore asks for it.\n")
	}
	b.WriteString("\nTo restore on a new server, as root, with this file next to the installer:\n\n    " + k.RestoreCommand + "\n")
	return b.String()
}
