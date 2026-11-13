package deployment_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type settingsOf struct{ svc service.Service }

func (r settingsOf) Get(context.Context, string) (*service.Service, error) {
	s := r.svc
	return &s, nil
}

// configFiles are a service's config files; each read after the first
// gives later's, when set (a save while a deploy runs).
type configFiles struct {
	mu           sync.Mutex
	now, later   []string
	reads, after int
}

func (c *configFiles) Contents(context.Context, string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	if c.later != nil && c.reads > c.after {
		return c.later, nil
	}
	return c.now, nil
}

// WriteToHost writes the files it reads, as the deploy's own write does.
func (c *configFiles) WriteToHost(ctx context.Context, id string, _ *server.Server) ([]deployment.ConfigFileMount, []string, error) {
	files, err := c.Contents(ctx, id)
	return nil, files, err
}

// panelLimits are a stack's limits in the panel (resources.ComposeLimits).
type panelLimits struct {
	deployment.ResourceLimits
	limits map[string]resources.ComposeLimit
}

func (p panelLimits) ComposeLimits(context.Context, string) (map[string]resources.ComposeLimit, error) {
	return p.limits, nil
}

// A deploy's settings fingerprint (D-159) changes with what it ships: a
// field, a variable, a config file, a stack's limits; not with the
// variables' order or the service's state.
func TestSettingsFingerprint(t *testing.T) {
	ctx := context.Background()
	fp := func(svc service.Service, key []byte, files []string, limits map[string]resources.ComposeLimit, vars ...string) string {
		s := deployment.NewService(nil, nil, nil, zap.NewNop())
		s.WithServiceReader(settingsOf{svc})
		s.WithConfigFileWriter(&configFiles{now: files})
		s.WithResources(panelLimits{limits: limits})
		if key != nil {
			s.WithHashKey(key)
		}
		got, err := s.SettingsFingerprint(ctx, "s1", "", vars)
		require.NoError(t, err)
		return got
	}
	key := []byte("k")
	web := service.Service{ID: "s1", Name: "web", Port: 8080, RuntimeStatus: "running"}
	conf := []string{"/etc/app.conf\x00debug=false"}
	base := fp(web, key, conf, nil, "A=1", "B=2")
	assert.Regexp(t, `^[0-9a-f]{64}$`, base)
	assert.Equal(t, base, fp(web, key, conf, nil, "B=2", "A=1"), "the variables' order isn't a change")
	assert.NotEqual(t, base, fp(web, key, conf, nil, "A=1", "B=3"), "a variable is")
	assert.NotEqual(t, base, fp(web, key, []string{"/etc/app.conf\x00debug=true"}, nil, "A=1", "B=2"), "a config file is")
	stopped := web
	stopped.RuntimeStatus, stopped.ActiveDeploymentID = "stopped", "d2"
	assert.Equal(t, base, fp(stopped, key, conf, nil, "A=1", "B=2"), "its state isn't")
	assert.NotEqual(t, base, fp(web, []byte("other"), conf, nil, "A=1", "B=2"), "keyed by the instance")
	assert.Empty(t, fp(web, nil, conf, nil, "A=1"), "no key, no fingerprint")

	stack := web
	stack.SourceType = service.SourceTypeDockerCompose
	was := fp(stack, key, nil, map[string]resources.ComposeLimit{"db": {MemoryMB: 1024}})
	assert.NotEqual(t, was, fp(stack, key, nil, map[string]resources.ComposeLimit{"db": {MemoryMB: 256}}), "a stack's limit is")
}

// A deploy that goes live records what it shipped: the service its caller
// built it from, before the deploy adds TOFT_URL, which is routing.
func TestTrigger_RecordsSettingsFingerprint(t *testing.T) {
	repo := &mockRepo{createdID: "dep-1"}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	now := service.Service{ID: "svc-1", Name: "web", Port: 8080}
	svc.WithServiceReader(settingsOf{now})
	svc.WithHashKey([]byte("k"))
	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{ServiceID: "svc-1", SourceType: "docker-image",
		DockerImage: "nginx", EnvVars: []string{"A=1"}, Settings: &now})
	require.NoError(t, err)
	want, err := svc.SettingsFingerprint(context.Background(), "svc-1", "", []string{"A=1"})
	require.NoError(t, err)
	assert.Equal(t, want, waitForFingerprint(t, repo))

	// Built from settings older than the service's now: it records those.
	repo = &mockRepo{createdID: "dep-2"}
	svc = newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	svc.WithServiceReader(settingsOf{now})
	svc.WithHashKey([]byte("k"))
	older := now
	older.Port = 3000
	_, err = svc.Trigger(context.Background(), deployment.TriggerInput{ServiceID: "svc-1", SourceType: "docker-image",
		DockerImage: "nginx", EnvVars: []string{"A=1"}, Settings: &older})
	require.NoError(t, err)
	assert.NotEqual(t, want, waitForFingerprint(t, repo))
}

// waitForFingerprint is the fingerprint the deploy records, which happens
// just after its status turns running: waiting for the status alone races.
func waitForFingerprint(t *testing.T, repo *mockRepo) string {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		repo.mu.Lock()
		f := repo.fingerprint
		repo.mu.Unlock()
		if f != "" {
			return f
		}
	}
	t.Fatal("timed out waiting for the deploy's fingerprint")
	return ""
}

// A config file saved while the deploy runs: what it wrote isn't what
// Trigger read, so it records no fingerprint, and Toft's redeploy waits.
func TestTrigger_SettingsChangedMidDeployRecordsNone(t *testing.T) {
	repo := &mockRepo{createdID: "dep-1"}
	svc := newTestService(repo, &mockFactory{orch: &mockOrchestrator{}}, &mockStatusUpdater{})
	now := service.Service{ID: "svc-1", Name: "web"}
	svc.WithServiceReader(settingsOf{now})
	svc.WithConfigFileWriter(&configFiles{now: []string{"/a\x00one"}, later: []string{"/a\x00two"}, after: 1})
	svc.WithHashKey([]byte("k"))
	_, err := svc.Trigger(context.Background(), deployment.TriggerInput{ServiceID: "svc-1", SourceType: "docker-image",
		DockerImage: "nginx", Settings: &now})
	require.NoError(t, err)
	waitForStatus(t, repo, deployment.StatusRunning, 2*time.Second)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	assert.Empty(t, repo.fingerprint)
}

// What of a service counts in its settings fingerprint (D-159): every
// field of service.Service is in exactly one list, and every column of
// services is a field or in columnsNotOnService. A new one fails
// TestShippedFieldsClassified until it is put in one; one that counts goes
// in deployment.shippedFields too, or TestSettingsFingerprintFields fails.
var (
	// What runs: a change waits for a deploy.
	fingerprintCounts = []string{
		"ID", "Scope", "ProjectID", "EnvironmentID", "ScopedOrgID", "ServerID", "Name", "Type", "SourceType",
		"GitRepoURL", "GitBranch", "RepositoryID", "BaseDirectory", "DockerImage", "InstallCommand",
		"BuildCommand", "StartCommand", "BuildPack", "Port", "Replicas", "CPULimit", "MemoryLimit", "PidsLimit",
		"Isolation", "EffectiveIsolation", "ComposeContent", "ComposeRepoURL", "ComposeFilePath",
		"ComposeRouteService", "DeployStrategy", "RegistryServer", "RegistryUsername", "RegistryPassword",
		"HasRegistryPassword", "AddonSlug", "NetworkAlias", "Volumes", "GitUsername", "GitToken", "HasGitToken",
		"PreDeployCommands", "PostDeployCommands", "HealthCheckPath", "HealthCheckTimeout", "HealthCheckRetries",
		"RequiredEnvVars", "MigrateCommand", "Dependencies", "BasicAuthUser", "BasicAuthPassword", "IPAllowlist",
		"ContainerHealth", "ContainerHealthCommand",
	}
	// Its state, written by Toft (a deploy's success among it); how it is
	// shown; the settings of deploys rather than of what runs.
	fingerprintIgnores = []string{
		"RuntimeStatus", "ActiveDeploymentID", "LiveSince", "CreatedAt", "UpdatedAt", "Deploying", "Down", "Unhealthy",
		"LastDeployFailed", "LastHealthCheck", "ComposeNetworkService",
		"IsolationAdvice", "Kind", "EffectiveDeployStrategy", "ConnectTo",
		"DeployOnPush", "RollbackImagesKept", "DeployTimeoutMinutes",
	}
	// Columns of services the service isn't read with: Toft's state.
	columnsNotOnService = []string{"MemoryLimitGraceUntil", "PreviewOf", "LoopbackPort"}
)

func TestShippedFieldsClassified(t *testing.T) {
	classified := map[string]bool{}
	for _, list := range [][]string{fingerprintCounts, fingerprintIgnores} {
		for _, f := range list {
			if classified[strings.ToLower(f)] {
				t.Errorf("%s is classified twice", f)
			}
			classified[strings.ToLower(f)] = true
		}
	}
	fields := map[string]bool{}
	for f := range reflect.TypeFor[service.Service]().Fields() {
		fields[strings.ToLower(f.Name)] = true
		if !classified[strings.ToLower(f.Name)] {
			t.Errorf("service.Service.%s is unclassified: add it to fingerprintCounts (and deployment.shippedFields) or fingerprintIgnores", f.Name)
		}
	}
	for f := range classified {
		if !fields[f] {
			t.Errorf("%s is classified but no field of service.Service", f)
		}
	}
	for _, c := range columnsNotOnService {
		fields[strings.ToLower(c)] = true
	}
	for f := range reflect.TypeFor[sqlcdb.Service]().Fields() {
		if !fields[strings.ToLower(f.Name)] {
			t.Errorf("column services.%s is unclassified: read it into a service.Service field and classify that, or add %s to columnsNotOnService", f.Tag.Get("json"), f.Name)
		}
	}
}

// Each field changes the fingerprint when it counts, and only then.
func TestSettingsFingerprintFields(t *testing.T) {
	fp := func(svc service.Service) string {
		s := deployment.NewService(nil, nil, nil, zap.NewNop())
		s.WithServiceReader(settingsOf{svc})
		s.WithHashKey([]byte("k"))
		got, err := s.SettingsFingerprint(context.Background(), "s1", "", nil)
		require.NoError(t, err)
		return got
	}
	base := fp(service.Service{})
	for _, tc := range []struct {
		fields []string
		counts bool
	}{{fingerprintCounts, true}, {fingerprintIgnores, false}} {
		for _, name := range tc.fields {
			t.Run(name, func(t *testing.T) {
				var svc service.Service
				f := reflect.ValueOf(&svc).Elem().FieldByName(name)
				f.Set(nonZero(t, f.Type()))
				assert.Equal(t, tc.counts, fp(svc) != base, "does %s count", name)
			})
		}
	}
}

func nonZero(t *testing.T, typ reflect.Type) reflect.Value {
	v := reflect.New(typ).Elem()
	switch typ.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Slice:
		v = reflect.Append(v, nonZero(t, typ.Elem()))
	case reflect.Pointer:
		v = reflect.New(typ.Elem())
		v.Elem().Set(nonZero(t, typ.Elem()))
	case reflect.Struct:
		if typ == reflect.TypeFor[time.Time]() {
			return reflect.ValueOf(time.Unix(1, 0))
		}
		v.Field(0).Set(nonZero(t, typ.Field(0).Type))
	default:
		t.Fatalf("no non-zero %s: add its kind to nonZero", typ)
	}
	return v
}
