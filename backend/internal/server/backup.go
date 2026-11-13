package server

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// Backups (D-43). Each server backs itself up: toft-backup.timer runs
// toft-backup.sh, which reads the destination from an env file the panel
// writes and reports to last.json, which the alert monitor reads. The panel
// host gets the same files from toft upgrade; its env file and result live in
// the data directory the backend shares with the host (panelBackupDir).

var (
	//go:embed toft-backup.sh
	backupScript string
	//go:embed toft-backup.service
	backupServiceUnit string
	//go:embed toft-backup.timer
	backupTimerUnit string
)

const (
	backupScriptPath  = "/usr/local/lib/toft/backup.sh"
	backupServicePath = "/etc/systemd/system/toft-backup.service"
	backupTimerPath   = "/etc/systemd/system/toft-backup.timer"
	backupDir         = "/var/lib/toft/backup"
	backupEnvPath     = backupDir + "/backup.env"
	backupResultPath  = backupDir + "/last.json"

	// panelDBContainer is the panel's Postgres container (docker-compose.yml),
	// which the panel host's backup dumps.
	panelDBContainer = "paas-postgres"
	// backupOverdue: no successful backup for this long fires backup_failed.
	backupOverdue = 26 * time.Hour
	// backupReadConcurrency and backupReadTimeout bound reading the results.
	backupReadConcurrency = 8
	backupReadTimeout     = 20 * time.Second
)

var (
	// ErrNoBackupDestination is a server without a backup destination.
	ErrNoBackupDestination = errors.New("this server has no backup destination")
	// ErrInvalidBackupDestination is a destination the platform won't write.
	ErrInvalidBackupDestination = errors.New("the endpoint must be https://host (no path), the bucket 3 to 63 lowercase letters, digits, dots or hyphens, " +
		"the access and secret keys letters, digits and + / = _ . ~ - (up to 255), and the time HH:MM")
	// ErrSameMachineBackup: another server entry is the same machine and
	// already has a destination. A machine has one backup: its timer backs
	// up every volume on it.
	ErrSameMachineBackup = errors.New("this machine is also another server entry (in another workspace) that already has a backup destination; " +
		"a machine is backed up once, with everything on it, so remove that one first")
	// ErrPanelBackupOperatorOnly: only instance operators set the panel
	// host's backup.
	ErrPanelBackupOperatorOnly = errors.New("only instance operators can set the panel host's backup")
	// ErrBackupRemovedNotApplied: the destination was removed, but the
	// server could not be reached to stop its backups.
	ErrBackupRemovedNotApplied = errors.New("removed, but the server keeps its backup settings until it is reached: provision it to stop its backups")
)

var (
	backupEndpointPattern = regexp.MustCompile(`^https://[a-z0-9.-]+(:[0-9]{1,5})?$`)
	backupBucketPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	// backupKeyPattern: the keys are written unquoted to the env file, which
	// both sh and docker --env-file read, so nothing either reads differently.
	backupKeyPattern      = regexp.MustCompile(`^[A-Za-z0-9+/=_.~-]{1,255}$`)
	backupSchedulePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// BackupDestination is where a server backs itself up. ServerID "" is the
// panel host. SecretKey and RepoPassword are decrypted.
type BackupDestination struct {
	ID           string
	ServerID     string
	Endpoint     string
	Bucket       string
	AccessKey    string
	SecretKey    string
	RepoPassword string
	Schedule     string
	LastRunAt    *time.Time
	LastOK       *bool
	LastOKAt     *time.Time
	LastBytes    *int64
	LastError    string
	CreatedAt    time.Time
}

// BackupInput is what an owner (or, for the panel host, an operator)
// sends. An empty SecretKey keeps the stored one; a new destination needs
// one. An empty Schedule is 03:00.
type BackupInput struct {
	Endpoint  string `json:"endpoint" binding:"max=2048"`
	Bucket    string `json:"bucket" binding:"max=255"`
	AccessKey string `json:"access_key" binding:"max=256"`
	SecretKey string `json:"secret_key" binding:"max=256"`
	Schedule  string `json:"schedule" binding:"max=16"`
}

// BackupRun is a server's report of its last backup (last.json).
type BackupRun struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	OK         bool      `json:"ok"`
	BytesAdded int64     `json:"bytes_added"`
	BytesTotal int64     `json:"bytes_total"`
	Snapshot   string    `json:"snapshot"`
	Error      string    `json:"error"`
}

// BackupDestinationDTO is the API's view. It never holds the secret key,
// and holds the repository password only in the answer that created the
// destination.
type BackupDestinationDTO struct {
	Endpoint   string     `json:"endpoint"`
	Bucket     string     `json:"bucket"`
	AccessKey  string     `json:"access_key"`
	Schedule   string     `json:"schedule"`
	Repository string     `json:"repository"`
	LastRunAt  *time.Time `json:"last_run_at"`
	LastOK     *bool      `json:"last_ok"`
	LastOKAt   *time.Time `json:"last_ok_at"`
	LastBytes  *int64     `json:"last_bytes"`
	LastError  string     `json:"last_error"`
	CreatedAt  time.Time  `json:"created_at"`
	// RepositoryPassword is shown once: it decrypts the backups, and is
	// the only way to read them if the panel and the server are both lost.
	RepositoryPassword string `json:"repository_password,omitempty"`
	// ApplyError: saved, but not written to the server yet; the server's
	// next Provision (or the panel's next deploy, for the panel host) does.
	ApplyError string `json:"apply_error,omitempty"`
	// Paused (the panel host only): a destination is stored, but the host
	// has no backup settings, so nothing runs. A restore leaves it so
	// (D-163); saving the destination again writes them.
	Paused bool `json:"paused,omitempty"`
	// RestoredAt is when the panel was restored, while no backup ran since:
	// with Paused the restore is why, and after a resume the last backup
	// is still the one from before it.
	RestoredAt *time.Time `json:"restored_at,omitempty"`
}

// BackupHealth is what the alert monitor judges: whether a destination's
// backups are failing, and why.
type BackupHealth struct {
	ServerID string // "" is the panel host
	Failing  bool
	Detail   string
}

func (in *BackupInput) normalize() error {
	in.Endpoint = strings.TrimSuffix(strings.TrimSpace(in.Endpoint), "/")
	in.Bucket = strings.TrimSpace(in.Bucket)
	in.AccessKey = strings.TrimSpace(in.AccessKey)
	in.SecretKey = strings.TrimSpace(in.SecretKey)
	if in.Schedule = strings.TrimSpace(in.Schedule); in.Schedule == "" {
		in.Schedule = "03:00"
	}
	if !backupEndpointPattern.MatchString(in.Endpoint) || !backupBucketPattern.MatchString(in.Bucket) ||
		!backupKeyPattern.MatchString(in.AccessKey) || (in.SecretKey != "" && !backupKeyPattern.MatchString(in.SecretKey)) ||
		!backupSchedulePattern.MatchString(in.Schedule) {
		return ErrInvalidBackupDestination
	}
	return nil
}

// repository is the restic repository: one per server under toft/ in the
// bucket, so servers can share a bucket.
func (d *BackupDestination) repository() string {
	name := d.ServerID
	if name == "" {
		name = "control-plane"
	}
	return "s3:" + d.Endpoint + "/" + d.Bucket + "/toft/" + name
}

// envFile is the env file toft-backup.sh reads. Every value matches a
// pattern above or is generated, so it needs no quoting.
func (d *BackupDestination) envFile() string {
	var b strings.Builder
	fmt.Fprintf(&b, "RESTIC_REPOSITORY=%s\nRESTIC_PASSWORD=%s\nAWS_ACCESS_KEY_ID=%s\nAWS_SECRET_ACCESS_KEY=%s\nTOFT_BACKUP_AT=%s\n",
		d.repository(), d.RepoPassword, d.AccessKey, d.SecretKey, d.Schedule)
	if d.ServerID == "" {
		fmt.Fprintf(&b, "TOFT_PANEL_DB=%s\n", panelDBContainer)
	}
	return b.String()
}

func (d *BackupDestination) dto() *BackupDestinationDTO {
	return &BackupDestinationDTO{
		Endpoint: d.Endpoint, Bucket: d.Bucket, AccessKey: d.AccessKey, Schedule: d.Schedule, Repository: d.repository(),
		LastRunAt: d.LastRunAt, LastOK: d.LastOK, LastOKAt: d.LastOKAt, LastBytes: d.LastBytes, LastError: d.LastError,
		CreatedAt: d.CreatedAt,
	}
}

// health is the backup_failed rule: the last run failed, or nothing
// succeeded for backupOverdue (counted from the destination's creation
// until a first success).
func (d *BackupDestination) health(now time.Time) BackupHealth {
	h := BackupHealth{ServerID: d.ServerID}
	if d.LastOK != nil && !*d.LastOK {
		h.Failing = true
		h.Detail = "the last backup failed: " + d.LastError
		return h
	}
	since := d.CreatedAt
	if d.LastOKAt != nil {
		since = *d.LastOKAt
	}
	if now.Sub(since) > backupOverdue {
		h.Failing = true
		if d.LastOKAt == nil {
			h.Detail = "no backup has succeeded since the destination was set up"
		} else {
			h.Detail = "no successful backup in 26 hours; the last was at " + d.LastOKAt.UTC().Format("2006-01-02 15:04 UTC")
		}
	}
	return h
}

// newRepoPassword is a restic repository password: 256 random bits.
func newRepoPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate the repository password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// WithPanelBackupDir is where the panel host's backup env file and result
// live: the backup directory under the data directory the backend shares
// with the host, which toft upgrade links to /var/lib/toft/backup.
func (s *Service) WithPanelBackupDir(dir string) { s.panelBackupDir = dir }

// PanelRestore reads when the panel was restored from a backup (D-163);
// nil when it never was.
type PanelRestore interface {
	RestoredAt(ctx context.Context) (*time.Time, error)
}

// WithPanelRestore lets the panel host's destination say that its backups
// are paused since the restore.
func (s *Service) WithPanelRestore(p PanelRestore) { s.panelRestore = p }

// markPaused sets Paused on the panel host's destination when the host has
// no backup settings, and RestoredAt when no backup ran since a restore.
// Settings that can't be read are an error, never "not paused".
func (s *Service) markPaused(ctx context.Context, d *BackupDestination, dto *BackupDestinationDTO) error {
	if d.ServerID != "" || s.panelBackupDir == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(s.panelBackupDir, "backup.env")); errors.Is(err, os.ErrNotExist) {
		dto.Paused = true
	} else if err != nil {
		return fmt.Errorf("the panel host's backup settings: %w", err)
	}
	if s.panelRestore == nil {
		return nil
	}
	at, err := s.panelRestore.RestoredAt(ctx)
	if err != nil {
		s.log.Warn("backups: could not read when the panel was restored", zap.Error(err))
		return nil
	}
	// A destination set after the restore lost its settings some other way.
	if at != nil && d.CreatedAt.Before(*at) && (d.LastRunAt == nil || d.LastRunAt.Before(*at)) {
		dto.RestoredAt = at
	}
	return nil
}

// GetBackup returns a server's destination without its secrets
// (ErrNoBackupDestination). serverID "" is the panel host.
func (s *Service) GetBackup(ctx context.Context, serverID string) (*BackupDestinationDTO, error) {
	d, err := s.repo.GetBackupDestination(ctx, serverID)
	if err != nil {
		return nil, err
	}
	dto := d.dto()
	if err := s.markPaused(ctx, d, dto); err != nil {
		return nil, err
	}
	return dto, nil
}

// PanelBackupKit is the panel host's repository, its password and the
// access key, for the recovery kit (D-62); ok is false without a
// destination. The caller checks that the reader is an operator.
func (s *Service) PanelBackupKit(ctx context.Context) (repository, password, accessKey string, ok bool, err error) {
	d, err := s.repo.GetBackupDestination(ctx, "")
	if errors.Is(err, ErrNoBackupDestination) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, err
	}
	return d.repository(), d.RepoPassword, d.AccessKey, true, nil
}

// SetBackup creates or replaces a server's destination and writes it to
// the server. A new destination gets a repository password, returned once.
// serverID "" is the panel host.
func (s *Service) SetBackup(ctx context.Context, serverID string, in BackupInput) (*BackupDestinationDTO, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	if serverID != "" {
		if err := s.checkSameMachineBackup(ctx, serverID); err != nil {
			return nil, err
		}
	}
	d, err := s.repo.GetBackupDestination(ctx, serverID)
	created := false
	switch {
	case errors.Is(err, ErrNoBackupDestination):
		if in.SecretKey == "" {
			return nil, ErrInvalidBackupDestination
		}
		password, perr := newRepoPassword()
		if perr != nil {
			return nil, perr
		}
		if d, err = s.repo.CreateBackupDestination(ctx, serverID, in, password); err != nil {
			return nil, err
		}
		created = true
	case err != nil:
		return nil, err
	default:
		if d, err = s.repo.UpdateBackupDestination(ctx, d.ID, in); err != nil {
			return nil, err
		}
	}
	dto := d.dto()
	if created {
		dto.RepositoryPassword = d.RepoPassword
	}
	if err := s.applyBackup(ctx, serverID, d); err != nil {
		s.log.Warn("backup destination saved but not written to the server", zap.String("server_id", serverID), zap.Error(err))
		dto.ApplyError = err.Error()
	}
	// Saved already: what can't be read back is said the same way.
	if err := s.markPaused(ctx, d, dto); err != nil && dto.ApplyError == "" {
		dto.ApplyError = err.Error()
	}
	return dto, nil
}

// DeleteBackup removes a server's destination and its env file from the
// server, which stops its backups. The backups in the bucket stay.
func (s *Service) DeleteBackup(ctx context.Context, serverID string) error {
	d, err := s.repo.GetBackupDestination(ctx, serverID)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteBackupDestination(ctx, d.ID); err != nil {
		return err
	}
	if err := s.applyBackup(ctx, serverID, nil); err != nil {
		return fmt.Errorf("%w (%v)", ErrBackupRemovedNotApplied, err)
	}
	return nil
}

// The panel host's destination: instance operators only.

func (s *Service) GetPanelBackup(ctx context.Context, actorID string) (*BackupDestinationDTO, error) {
	if err := s.requireOperator(ctx, actorID, ErrPanelBackupOperatorOnly); err != nil {
		return nil, err
	}
	return s.GetBackup(ctx, "")
}

func (s *Service) SetPanelBackup(ctx context.Context, actorID string, in BackupInput) (*BackupDestinationDTO, error) {
	if err := s.requireOperator(ctx, actorID, ErrPanelBackupOperatorOnly); err != nil {
		return nil, err
	}
	return s.SetBackup(ctx, "", in)
}

func (s *Service) DeletePanelBackup(ctx context.Context, actorID string) error {
	if err := s.requireOperator(ctx, actorID, ErrPanelBackupOperatorOnly); err != nil {
		return err
	}
	return s.DeleteBackup(ctx, "")
}

// checkSameMachineBackup refuses a destination for serverID when another
// entry for the same machine has one.
func (s *Service) checkSameMachineBackup(ctx context.Context, serverID string) error {
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	all, err := s.repo.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("list servers: %w", err)
	}
	for _, other := range all {
		if other.ID == srv.ID || !sameMachine(*srv, other) {
			continue
		}
		if _, err := s.repo.GetBackupDestination(ctx, other.ID); err == nil {
			return ErrSameMachineBackup
		} else if !errors.Is(err, ErrNoBackupDestination) {
			return err
		}
	}
	return nil
}

// applyBackup writes d's env file to its server, or removes it when d is
// nil: over SSH for a remote server, in panelBackupDir for the panel host.
func (s *Service) applyBackup(ctx context.Context, serverID string, d *BackupDestination) error {
	env := ""
	if d != nil {
		env = d.envFile()
	}
	if serverID == "" {
		return s.writePanelBackupEnv(env)
	}
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	return ensureBackup(shellFor(ctx, s.SSH(srv)), env, func(string) {})
}

func (s *Service) writePanelBackupEnv(env string) error {
	if s.panelBackupDir == "" {
		return errors.New("the panel host's backup directory is not set")
	}
	path := filepath.Join(s.panelBackupDir, "backup.env")
	if env == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(s.panelBackupDir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(env), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ensureBackup installs the backup script and units if they changed,
// enables the timer, and writes env as the server's backup env file, or
// removes the file when env is "" (no destination: the script then does
// nothing). Idempotent. The env file holds secrets, so it goes through
// stdin into a root-only file.
func ensureBackup(sc shell, env string, logFn func(string)) error {
	scriptChanged, err := writeIfChanged(sc, backupScriptPath, backupScript)
	if err != nil {
		return err
	}
	if scriptChanged {
		if _, err := sc.Exec("chmod 0755 " + backupScriptPath); err != nil {
			return fmt.Errorf("chmod the backup script: %w", err)
		}
	}
	serviceChanged, err := writeIfChanged(sc, backupServicePath, backupServiceUnit)
	if err != nil {
		return err
	}
	timerChanged, err := writeIfChanged(sc, backupTimerPath, backupTimerUnit)
	if err != nil {
		return err
	}
	if serviceChanged || timerChanged {
		if _, err := sc.Exec("systemctl daemon-reload"); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %w", err)
		}
	}
	if enabled, _ := sc.Exec("systemctl is-enabled toft-backup.timer 2>/dev/null"); strings.TrimSpace(enabled) != "enabled" {
		if _, err := sc.Exec("systemctl enable --now toft-backup.timer"); err != nil {
			return fmt.Errorf("systemctl enable toft-backup.timer: %w", err)
		}
	}
	if env == "" {
		if _, err := sc.Exec("rm -f " + backupEnvPath); err != nil {
			return fmt.Errorf("remove the backup settings: %w", err)
		}
		logFn("Backups: no destination set")
		return nil
	}
	if _, err := sc.ExecInput("mkdir -p -m 0700 "+backupDir+" && umask 077 && cat > "+backupEnvPath, strings.NewReader(env)); err != nil {
		return fmt.Errorf("write the backup settings: %w", err)
	}
	logFn("Backups: daily, to the server's backup destination")
	return nil
}

// CheckBackups reads every destination's last result from its server,
// records a new one, and judges each (BackupHealth). A server that cannot
// be read is judged on what was recorded before. For the alert monitor.
func (s *Service) CheckBackups(ctx context.Context) ([]BackupHealth, error) {
	list, err := s.repo.ListBackupDestinations(ctx)
	if err != nil {
		return nil, err
	}
	var g errgroup.Group
	g.SetLimit(backupReadConcurrency)
	for i := range list {
		d := &list[i]
		g.Go(func() error {
			rctx, cancel := context.WithTimeout(ctx, backupReadTimeout)
			defer cancel()
			run, err := s.readBackupRun(rctx, d.ServerID)
			// A run from before the destination was set up is a previous
			// destination's, still in last.json.
			if err != nil || run == nil || run.FinishedAt.Before(d.CreatedAt) ||
				(d.LastRunAt != nil && !run.FinishedAt.After(*d.LastRunAt)) {
				return nil
			}
			if err := s.repo.RecordBackupRun(ctx, d.ID, *run); err != nil {
				s.log.Warn("backups: could not record a result", zap.String("server_id", d.ServerID), zap.Error(err))
				return nil
			}
			at, ok, bytes := run.FinishedAt, run.OK, run.BytesTotal
			d.LastRunAt, d.LastOK, d.LastBytes, d.LastError = &at, &ok, &bytes, run.Error
			if run.OK {
				d.LastOKAt = &at
			}
			return nil
		})
	}
	_ = g.Wait()
	now := time.Now()
	out := make([]BackupHealth, 0, len(list))
	for i := range list {
		out = append(out, list[i].health(now))
	}
	return out, nil
}

// readBackupRun reads a server's last.json; nil when there is none yet.
func (s *Service) readBackupRun(ctx context.Context, serverID string) (*BackupRun, error) {
	var raw []byte
	if serverID == "" {
		if s.panelBackupDir == "" {
			return nil, errors.New("the panel host's backup directory is not set")
		}
		b, err := os.ReadFile(filepath.Join(s.panelBackupDir, "last.json"))
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		raw = b
	} else {
		srv, err := s.repo.GetByID(ctx, serverID)
		if err != nil {
			return nil, err
		}
		out, err := s.SSH(srv).Exec(ctx, "cat "+backupResultPath+" 2>/dev/null || true")
		if err != nil {
			return nil, err
		}
		raw = []byte(out)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var run BackupRun
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("read %s: %w", backupResultPath, err)
	}
	if run.FinishedAt.IsZero() {
		return nil, nil
	}
	if run.BytesTotal < 0 {
		run.BytesTotal = 0
	}
	return &run, nil
}
