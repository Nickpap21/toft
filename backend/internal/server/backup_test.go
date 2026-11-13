package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── fakeRepo: backup destinations ───────────────────────────────────────────

func (r *fakeRepo) GetBackupDestination(_ context.Context, serverID string) (*BackupDestination, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.backups[serverID]; ok {
		c := *d
		return &c, nil
	}
	return nil, ErrNoBackupDestination
}

func (r *fakeRepo) ListBackupDestinations(context.Context) ([]BackupDestination, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BackupDestination, 0, len(r.backups))
	for _, d := range r.backups {
		out = append(out, *d)
	}
	return out, nil
}

func (r *fakeRepo) CreateBackupDestination(_ context.Context, serverID string, in BackupInput, password string) (*BackupDestination, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.backups == nil {
		r.backups = map[string]*BackupDestination{}
	}
	d := &BackupDestination{ID: "bd-" + serverID, ServerID: serverID, Endpoint: in.Endpoint, Bucket: in.Bucket,
		AccessKey: in.AccessKey, SecretKey: in.SecretKey, RepoPassword: password, Schedule: in.Schedule, CreatedAt: time.Now()}
	r.backups[serverID] = d
	c := *d
	return &c, nil
}

func (r *fakeRepo) UpdateBackupDestination(_ context.Context, id string, in BackupInput) (*BackupDestination, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.backups {
		if d.ID == id {
			d.Endpoint, d.Bucket, d.AccessKey, d.Schedule = in.Endpoint, in.Bucket, in.AccessKey, in.Schedule
			if in.SecretKey != "" {
				d.SecretKey = in.SecretKey
			}
			c := *d
			return &c, nil
		}
	}
	return nil, ErrNoBackupDestination
}

func (r *fakeRepo) DeleteBackupDestination(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, d := range r.backups {
		if d.ID == id {
			delete(r.backups, k)
		}
	}
	return nil
}

func (r *fakeRepo) RecordBackupRun(_ context.Context, id string, run BackupRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.backups {
		if d.ID == id {
			at, ok, b := run.FinishedAt, run.OK, run.BytesTotal
			d.LastRunAt, d.LastOK, d.LastBytes, d.LastError = &at, &ok, &b, run.Error
			if ok {
				d.LastOKAt = &at
			}
		}
	}
	return nil
}

// ─── Validation, the env file, the rule ──────────────────────────────────────

func validBackup() BackupInput {
	return BackupInput{Endpoint: "https://fsn1.your-objectstorage.com/", Bucket: "northwind-backups",
		AccessKey: "AKIAEXAMPLE", SecretKey: "s3cr3t/Key+value=", Schedule: ""}
}

func TestBackupInputNormalize(t *testing.T) {
	in := validBackup()
	require.NoError(t, in.normalize())
	assert.Equal(t, "https://fsn1.your-objectstorage.com", in.Endpoint, "the trailing slash goes")
	assert.Equal(t, "03:00", in.Schedule, "the default time")

	for name, mutate := range map[string]func(*BackupInput){
		"plain http":                  func(in *BackupInput) { in.Endpoint = "http://fsn1.your-objectstorage.com" },
		"a path in the endpoint":      func(in *BackupInput) { in.Endpoint = "https://host/bucket" },
		"credentials in the endpoint": func(in *BackupInput) { in.Endpoint = "https://user:pw@host" },
		"an uppercase bucket":         func(in *BackupInput) { in.Bucket = "Northwind" },
		"a newline in a key":          func(in *BackupInput) { in.AccessKey = "AKIA\nRESTIC_PASSWORD=x" },
		"a quote in the secret":       func(in *BackupInput) { in.SecretKey = "abc'def" },
		"a space in the secret":       func(in *BackupInput) { in.SecretKey = "abc def" },
		"a $ in the secret":           func(in *BackupInput) { in.SecretKey = "abc$(reboot)" },
		"no access key":               func(in *BackupInput) { in.AccessKey = "" },
		"24:00":                       func(in *BackupInput) { in.Schedule = "24:00" },
		"3:00":                        func(in *BackupInput) { in.Schedule = "3:00" },
	} {
		in := validBackup()
		mutate(&in)
		assert.ErrorIs(t, in.normalize(), ErrInvalidBackupDestination, name)
	}
}

func TestBackupEnvFile(t *testing.T) {
	d := &BackupDestination{ServerID: "11111111-2222-3333-4444-555555555555", Endpoint: "https://fsn1.your-objectstorage.com",
		Bucket: "b-1", AccessKey: "AK", SecretKey: "SK", RepoPassword: "pw", Schedule: "02:30"}
	assert.Equal(t, "RESTIC_REPOSITORY=s3:https://fsn1.your-objectstorage.com/b-1/toft/11111111-2222-3333-4444-555555555555\n"+
		"RESTIC_PASSWORD=pw\nAWS_ACCESS_KEY_ID=AK\nAWS_SECRET_ACCESS_KEY=SK\nTOFT_BACKUP_AT=02:30\n", d.envFile())

	d.ServerID = ""
	env := d.envFile()
	assert.Contains(t, env, "/b-1/toft/control-plane\n")
	assert.Contains(t, env, "TOFT_PANEL_DB=paas-postgres\n", "the panel host also dumps the panel's database")
}

func TestBackupHealth(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	yes, no := true, false
	cases := []struct {
		name    string
		d       BackupDestination
		failing bool
		detail  string
	}{
		{"new, not run yet", BackupDestination{CreatedAt: now.Add(-2 * time.Hour)}, false, ""},
		{"new, never succeeded in 26 hours", BackupDestination{CreatedAt: now.Add(-27 * time.Hour)}, true, "no backup has succeeded"},
		{"ran fine last night", BackupDestination{CreatedAt: now.Add(-72 * time.Hour), LastRunAt: ago(9 * time.Hour), LastOK: &yes, LastOKAt: ago(9 * time.Hour)}, false, ""},
		{"the last run failed", BackupDestination{CreatedAt: now.Add(-72 * time.Hour), LastRunAt: ago(time.Hour), LastOK: &no, LastOKAt: ago(25 * time.Hour), LastError: "the dump of db failed"}, true, "the last backup failed: the dump of db failed"},
		{"stopped running", BackupDestination{CreatedAt: now.Add(-72 * time.Hour), LastRunAt: ago(30 * time.Hour), LastOK: &yes, LastOKAt: ago(30 * time.Hour)}, true, "no successful backup in 26 hours"},
	}
	for _, tc := range cases {
		h := tc.d.health(now)
		assert.Equal(t, tc.failing, h.Failing, tc.name)
		assert.Contains(t, h.Detail, tc.detail, tc.name)
	}
}

// ─── ensureBackup (fake shell) ───────────────────────────────────────────────

// backupHost is a host for the backup step: the files it has, and whether
// the timer is enabled.
type backupHost struct {
	files   map[string]string
	enabled bool
}

func (h *backupHost) shell() *fakeShell {
	return &fakeShell{answer: func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "sha256sum "):
			path := strings.Fields(cmd)[1]
			if c, ok := h.files[path]; ok {
				return sha256Hex(c)
			}
		case strings.HasPrefix(cmd, "systemctl is-enabled toft-backup.timer"):
			if h.enabled {
				return "enabled"
			}
			return "disabled"
		case cmd == "systemctl enable --now toft-backup.timer":
			h.enabled = true
		}
		return ""
	}}
}

func TestEnsureBackupOnAFreshHost(t *testing.T) {
	h := &backupHost{}
	sh := h.shell()
	env := "RESTIC_PASSWORD=pw\nAWS_SECRET_ACCESS_KEY=SK\n"
	require.NoError(t, ensureBackup(sh, env, func(string) {}))

	order := []string{"cat > " + backupScriptPath, "chmod 0755 " + backupScriptPath, "cat > " + backupServicePath, "cat > " + backupTimerPath,
		"systemctl daemon-reload", "systemctl enable --now toft-backup.timer", "umask 077 && cat > " + backupEnvPath}
	at := -1
	for _, want := range order {
		i := indexOf(sh.cmds, want)
		require.GreaterOrEqual(t, i, 0, "%q is run", want)
		assert.Greater(t, i, at, "%q runs after the step before it", want)
		at = i
	}
	assert.Equal(t, []string{backupScript, backupServiceUnit, backupTimerUnit, env}, sh.inputs, "every file goes over stdin")
	for _, c := range sh.cmds {
		assert.NotContains(t, c, "SK", "a secret never appears on a command line")
		assert.NotContains(t, c, "pw")
	}
}

func TestEnsureBackupOnAHostThatHasIt(t *testing.T) {
	h := &backupHost{enabled: true, files: map[string]string{
		backupScriptPath: backupScript, backupServicePath: backupServiceUnit, backupTimerPath: backupTimerUnit,
	}}
	sh := h.shell()
	require.NoError(t, ensureBackup(sh, "RESTIC_PASSWORD=pw\n", func(string) {}))
	for _, m := range []string{"cat > " + backupScriptPath, "chmod", "daemon-reload", "systemctl enable"} {
		assert.False(t, issued(sh.cmds, m), "must not run %q", m)
	}
	assert.True(t, issued(sh.cmds, "cat > "+backupEnvPath), "the settings are written every time")
}

func TestEnsureBackupWithoutADestinationRemovesTheSettings(t *testing.T) {
	h := &backupHost{enabled: true, files: map[string]string{
		backupScriptPath: backupScript, backupServicePath: backupServiceUnit, backupTimerPath: backupTimerUnit,
	}}
	sh := h.shell()
	require.NoError(t, ensureBackup(sh, "", func(string) {}))
	assert.True(t, issued(sh.cmds, "rm -f "+backupEnvPath))
	assert.False(t, issued(sh.cmds, "cat > "+backupEnvPath))
	assert.Empty(t, sh.inputs)
}

func TestEnsureBackupStopsAtAFailedWrite(t *testing.T) {
	sh := &refusingShell{refuse: "cat > " + backupScriptPath}
	assert.Error(t, ensureBackup(sh, "RESTIC_PASSWORD=pw\n", func(string) {}))
	assert.False(t, issued(sh.cmds, "systemctl enable"))
	assert.False(t, issued(sh.cmds, "cat > "+backupEnvPath))
}

func TestTheBackupFilesAreInstalledWhereTheUnitsLook(t *testing.T) {
	assert.Contains(t, backupServiceUnit, "ExecStart="+backupScriptPath)
	assert.Contains(t, backupScript, "DIR="+backupDir+"\n")
	assert.Contains(t, backupScript, "restic/restic:0.19.1@sha256:", "the restic image is pinned by digest")
}

// ─── The service ──────────────────────────────────────────────────────────────

func TestSetBackupCreatesOnceThenUpdates(t *testing.T) {
	repo := newFakeRepo(&Server{ID: testServerID, OrgID: "org-a", IPAddress: "203.0.113.7", Port: 22})
	s := NewService(repo, "")

	dto, err := s.SetBackup(context.Background(), testServerID, validBackup())
	require.NoError(t, err)
	assert.Len(t, dto.RepositoryPassword, 43, "a new destination shows its password once")
	assert.Equal(t, "s3:https://fsn1.your-objectstorage.com/northwind-backups/toft/"+testServerID, dto.Repository)
	assert.NotEmpty(t, dto.ApplyError, "no SSH here: saved, not written to the server, and it says so")
	password := repo.backups[testServerID].RepoPassword

	update := validBackup()
	update.SecretKey = ""
	update.Schedule = "04:15"
	dto, err = s.SetBackup(context.Background(), testServerID, update)
	require.NoError(t, err)
	assert.Empty(t, dto.RepositoryPassword, "never again")
	assert.Equal(t, "04:15", dto.Schedule)
	assert.Equal(t, "s3cr3t/Key+value=", repo.backups[testServerID].SecretKey, "an empty secret key keeps the stored one")
	assert.Equal(t, password, repo.backups[testServerID].RepoPassword, "the password never changes: it decrypts the old backups")

	got, err := s.GetBackup(context.Background(), testServerID)
	require.NoError(t, err)
	raw, _ := json.Marshal(got)
	assert.NotContains(t, string(raw), "s3cr3t")
	assert.NotContains(t, string(raw), password)
}

func TestSetBackupNeedsASecretKeyToCreate(t *testing.T) {
	repo := newFakeRepo(&Server{ID: testServerID, OrgID: "org-a"})
	in := validBackup()
	in.SecretKey = ""
	_, err := NewService(repo, "").SetBackup(context.Background(), testServerID, in)
	assert.ErrorIs(t, err, ErrInvalidBackupDestination)
	assert.Empty(t, repo.backups)
}

func TestSetBackupRefusesASecondEntryForTheSameMachine(t *testing.T) {
	repo := newFakeRepo(
		&Server{ID: testServerID, OrgID: "org-a", IPAddress: "203.0.113.7", Port: 22},
		&Server{ID: "other-entry", OrgID: "org-b", IPAddress: "203.0.113.7", Port: 22},
	)
	s := NewService(repo, "")
	_, err := s.SetBackup(context.Background(), "other-entry", validBackup())
	require.NoError(t, err)
	_, err = s.SetBackup(context.Background(), testServerID, validBackup())
	assert.ErrorIs(t, err, ErrSameMachineBackup)
}

func TestPanelBackupWritesTheSettingsForTheHost(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backup")
	repo := newFakeRepo()
	s := NewService(repo, "")
	s.WithPanelBackupDir(dir)
	s.WithOperatorChecker(fakeOperators{"op": true})

	_, err := s.SetPanelBackup(context.Background(), "someone", validBackup())
	assert.ErrorIs(t, err, ErrPanelBackupOperatorOnly)

	dto, err := s.SetPanelBackup(context.Background(), "op", validBackup())
	require.NoError(t, err)
	assert.Empty(t, dto.ApplyError)
	env := filepath.Join(dir, "backup.env")
	st, err := os.Stat(env)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm(), "root-only")
	b, _ := os.ReadFile(env)
	assert.Contains(t, string(b), "TOFT_PANEL_DB=paas-postgres")
	assert.Contains(t, string(b), "RESTIC_PASSWORD="+dto.RepositoryPassword)

	require.NoError(t, s.DeletePanelBackup(context.Background(), "op"))
	_, err = os.Stat(env)
	assert.ErrorIs(t, err, os.ErrNotExist, "no settings: the script does nothing")
}

type fakeRestore struct{ at *time.Time }

func (f fakeRestore) RestoredAt(context.Context) (*time.Time, error) { return f.at, nil }

// A restore brings the destination back in the database and leaves the
// host without its settings (D-163): paused until an operator resumes.
func TestPanelBackupIsPausedUntilItsSettingsAreWrittenAgain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backup")
	repo := newFakeRepo(&Server{ID: testServerID, OrgID: "org-a", IPAddress: "203.0.113.7", Port: 22})
	s := NewService(repo, "")
	s.WithPanelBackupDir(dir)
	ctx := context.Background()

	created, err := s.SetBackup(ctx, "", validBackup())
	require.NoError(t, err)
	assert.False(t, created.Paused, "just written")
	env := filepath.Join(dir, "backup.env")
	before, err := os.ReadFile(env)
	require.NoError(t, err)

	require.NoError(t, os.Remove(env))
	got, err := s.GetBackup(ctx, "")
	require.NoError(t, err)
	assert.True(t, got.Paused)
	assert.Nil(t, got.RestoredAt, "no restore on record")

	restored := time.Now()
	s.WithPanelRestore(fakeRestore{&restored})
	got, err = s.GetBackup(ctx, "")
	require.NoError(t, err)
	assert.True(t, got.Paused)
	assert.Equal(t, &restored, got.RestoredAt)

	ran := restored.Add(time.Hour)
	repo.backups[""].LastRunAt = &ran
	got, err = s.GetBackup(ctx, "")
	require.NoError(t, err)
	assert.True(t, got.Paused)
	assert.Nil(t, got.RestoredAt, "a backup ran since the restore: the settings went missing later")

	repo.backups[""].LastRunAt = nil
	createdAt := repo.backups[""].CreatedAt
	repo.backups[""].CreatedAt = restored.Add(time.Minute)
	got, err = s.GetBackup(ctx, "")
	require.NoError(t, err)
	assert.True(t, got.Paused)
	assert.Nil(t, got.RestoredAt, "set after the restore")
	repo.backups[""].CreatedAt = createdAt

	// A server's settings are on the server: never paused here.
	_, err = s.SetBackup(ctx, testServerID, validBackup())
	require.NoError(t, err)
	remote, err := s.GetBackup(ctx, testServerID)
	require.NoError(t, err)
	assert.False(t, remote.Paused)

	// Resume: the stored destination again, with no key typed.
	in := validBackup()
	in.SecretKey = ""
	// The settings can't be written: it says why, and stays paused.
	require.NoError(t, os.Mkdir(env+".tmp", 0o700))
	failed, err := s.SetBackup(ctx, "", in)
	require.NoError(t, err)
	assert.NotEmpty(t, failed.ApplyError)
	assert.True(t, failed.Paused)
	require.NoError(t, os.Remove(env+".tmp"))
	resumed, err := s.SetBackup(ctx, "", in)
	require.NoError(t, err)
	assert.False(t, resumed.Paused)
	assert.Empty(t, resumed.RepositoryPassword, "the same repository")
	after, err := os.ReadFile(env)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the stored secret key and repository password")
	// Until this host's first run, the last backup is still the one from
	// before the restore (D-164).
	assert.Equal(t, &restored, resumed.RestoredAt)
	repo.backups[""].LastRunAt = &ran
	got, err = s.GetBackup(ctx, "")
	require.NoError(t, err)
	assert.Nil(t, got.RestoredAt, "this host ran one")
}

// Settings that can't be read are a failed read, never "not paused" (D-164).
func TestPanelBackupSettingsThatCannotBeReadFailTheRead(t *testing.T) {
	s := NewService(newFakeRepo(), "")
	s.WithPanelBackupDir(filepath.Join(t.TempDir(), "backup"))
	ctx := context.Background()
	_, err := s.SetBackup(ctx, "", validBackup())
	require.NoError(t, err)

	// A file where the folder was: the stat fails, and not with "not there".
	notADir := filepath.Join(t.TempDir(), "backup")
	require.NoError(t, os.WriteFile(notADir, nil, 0o600))
	s.WithPanelBackupDir(notADir)
	_, err = s.GetBackup(ctx, "")
	require.Error(t, err)
}

func TestCheckBackupsReadsAndJudgesTheLastRun(t *testing.T) {
	dir := t.TempDir()
	repo := newFakeRepo()
	s := NewService(repo, "")
	s.WithPanelBackupDir(dir)
	s.WithOperatorChecker(fakeOperators{"op": true})
	_, err := s.SetPanelBackup(context.Background(), "op", validBackup())
	require.NoError(t, err)

	h, err := s.CheckBackups(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []BackupHealth{{ServerID: ""}}, h, "not run yet, not overdue")

	finished := time.Now().UTC().Add(time.Second).Truncate(time.Second)
	write := func(ok bool, msg string) {
		run := BackupRun{StartedAt: finished.Add(-time.Minute), FinishedAt: finished, OK: ok, BytesTotal: 2_400_000_000, Error: msg}
		b, _ := json.Marshal(run)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "last.json"), b, 0o600))
	}
	write(false, "the dump of northwind-db failed: Access denied")
	h, err = s.CheckBackups(context.Background())
	require.NoError(t, err)
	require.Len(t, h, 1)
	assert.True(t, h[0].Failing)
	assert.Equal(t, "the last backup failed: the dump of northwind-db failed: Access denied", h[0].Detail)
	assert.Equal(t, int64(2_400_000_000), *repo.backups[""].LastBytes)

	finished = finished.Add(time.Hour)
	write(true, "")
	h, _ = s.CheckBackups(context.Background())
	assert.False(t, h[0].Failing, "a success resolves it")
	assert.Equal(t, finished, *repo.backups[""].LastOKAt)
}

func TestCheckBackupsIgnoresARunFromBeforeTheDestination(t *testing.T) {
	dir := t.TempDir()
	old := BackupRun{FinishedAt: time.Now().Add(-time.Hour), OK: false, Error: "a previous destination's failure"}
	b, _ := json.Marshal(old)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "last.json"), b, 0o600))

	repo := newFakeRepo()
	s := NewService(repo, "")
	s.WithPanelBackupDir(dir)
	s.WithOperatorChecker(fakeOperators{"op": true})
	_, err := s.SetPanelBackup(context.Background(), "op", validBackup())
	require.NoError(t, err)

	h, err := s.CheckBackups(context.Background())
	require.NoError(t, err)
	assert.False(t, h[0].Failing)
	assert.Nil(t, repo.backups[""].LastRunAt, "not recorded")
}

func TestBackupRoutes(t *testing.T) {
	repo := newFakeRepo(&Server{ID: testServerID, OrgID: "org-a", IPAddress: "203.0.113.7", Port: 22})
	s := NewService(repo, "")
	call := func(role, orgID, method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/v1/servers/"+testServerID+"/backup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		roleRouterAs(s, &fakeAudit{}, role, orgID, "u1").ServeHTTP(w, req)
		return w
	}
	body, _ := json.Marshal(validBackup())

	none := call("viewer", "org-a", http.MethodGet, "")
	assert.Equal(t, http.StatusOK, none.Code)
	assert.JSONEq(t, `{"data":null}`, none.Body.String(), "no destination is a state, not an error")
	for _, role := range []string{"viewer", "developer"} {
		assert.Equal(t, http.StatusForbidden, call(role, "org-a", http.MethodPut, string(body)).Code, role)
		assert.Equal(t, http.StatusForbidden, call(role, "org-a", http.MethodDelete, "").Code, role)
	}
	assert.Equal(t, http.StatusNotFound, call("owner", "org-b", http.MethodPut, string(body)).Code, "another workspace's owner")
	assert.Empty(t, repo.backups)

	w := call("owner", "org-a", http.MethodPut, string(body))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "s3cr3t", "the secret key is never returned")
	assert.Contains(t, w.Body.String(), `"repository_password"`)

	w = call("viewer", "org-a", http.MethodGet, "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "s3cr3t")
	assert.NotContains(t, w.Body.String(), "repository_password")
	assert.NotContains(t, w.Body.String(), repo.backups[testServerID].RepoPassword)

	assert.Equal(t, http.StatusNotFound, call("owner", "org-b", http.MethodGet, "").Code, "another workspace sees nothing")
	assert.Equal(t, http.StatusBadRequest, call("owner", "org-a", http.MethodPut, `{"endpoint":"http://x"}`).Code)
}

func TestPanelBackupRoutesAreForOperators(t *testing.T) {
	s := NewService(newFakeRepo(), "")
	s.WithPanelBackupDir(t.TempDir())
	s.WithOperatorChecker(fakeOperators{"op": true})
	body, _ := json.Marshal(validBackup())
	for _, user := range []string{"u1", "op"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/instance/backup", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		roleRouterAs(s, &fakeAudit{}, "owner", "org-a", user).ServeHTTP(w, req)
		if user == "op" {
			assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
		} else {
			assert.Equal(t, http.StatusForbidden, w.Code, "a workspace owner who is not an operator")
		}
	}
}

func TestProvisionWritesTheBackupDestination(t *testing.T) {
	host := &provisionedHost{}
	sh := &fakeShell{answer: host.answer}
	env := "RESTIC_PASSWORD=pw-s3cret\n"
	_, err := provision(sh, provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22, BackupEnv: env}, func(string) {})
	require.NoError(t, err)
	assert.Equal(t, env, sh.inputs[len(sh.inputs)-1], "the settings go over stdin")
	assert.NotContains(t, sh.inputs, backupScript, "on a provisioned host the script is not written again")
	for _, c := range sh.cmds {
		assert.NotContains(t, c, "s3cret")
	}
}
