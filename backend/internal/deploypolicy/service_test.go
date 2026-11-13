package deploypolicy_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Fakes ────────────────────────────────────────────────────────────────────

type fakeRepo struct {
	exceptions map[string]*deploypolicy.Exception
	services   map[string]*deploypolicy.StoredService
	envKeys    map[string][]string
	paths      map[string][]string
	checks     map[string]*deploypolicy.LastCheck
	getErr     error

	grants  []deploypolicy.Grant
	revokes []string
	repins  [][2][]string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		exceptions: map[string]*deploypolicy.Exception{},
		services:   map[string]*deploypolicy.StoredService{},
		envKeys:    map[string][]string{},
		paths:      map[string][]string{},
		checks:     map[string]*deploypolicy.LastCheck{},
	}
}

func (f *fakeRepo) GetException(_ context.Context, id string) (*deploypolicy.Exception, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if e, ok := f.exceptions[id]; ok {
		return e, nil
	}
	return nil, deploypolicy.ErrNotFound
}

func (f *fakeRepo) ListExceptions(context.Context) (map[string]*deploypolicy.Exception, error) {
	return f.exceptions, nil
}

func (f *fakeRepo) GrantException(_ context.Context, g deploypolicy.Grant) (*deploypolicy.Exception, error) {
	if _, ok := f.services[g.ServiceID]; !ok {
		return nil, deploypolicy.ErrServiceNotFound
	}
	f.grants = append(f.grants, g)
	e := &deploypolicy.Exception{ServiceID: g.ServiceID, AllowedRules: g.AllowedRules, AllowedValues: g.AllowedValues, Reason: g.Reason, GrantedBy: g.ActorID}
	f.exceptions[g.ServiceID] = e
	return e, nil
}

func (f *fakeRepo) RevokeException(_ context.Context, id, _, _ string) (*deploypolicy.Exception, error) {
	e, ok := f.exceptions[id]
	if !ok {
		return nil, deploypolicy.ErrNotFound
	}
	delete(f.exceptions, id)
	f.revokes = append(f.revokes, id)
	return e, nil
}

func (f *fakeRepo) RepinValues(_ context.Context, id string, old, keyed []string) error {
	f.repins = append(f.repins, [2][]string{old, keyed})
	for i, p := range f.exceptions[id].AllowedValues {
		if j := slices.Index(old, p.Fingerprint); j >= 0 {
			f.exceptions[id].AllowedValues[i].Fingerprint = keyed[j]
		}
	}
	return nil
}

func (f *fakeRepo) GetStoredService(_ context.Context, id string) (*deploypolicy.StoredService, error) {
	if s, ok := f.services[id]; ok {
		return s, nil
	}
	return nil, deploypolicy.ErrServiceNotFound
}

func (f *fakeRepo) ListStoredServices(context.Context) ([]deploypolicy.StoredService, error) {
	ids := make([]string, 0, len(f.services))
	for id := range f.services {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]deploypolicy.StoredService, 0, len(ids))
	for _, id := range ids {
		out = append(out, *f.services[id])
	}
	return out, nil
}

func (f *fakeRepo) ListEnvKeys(_ context.Context, id string) (map[string][]string, error) {
	if id == "" {
		return f.envKeys, nil
	}
	return map[string][]string{id: f.envKeys[id]}, nil
}

func (f *fakeRepo) ListConfigFilePaths(_ context.Context, id string) (map[string][]string, error) {
	if id == "" {
		return f.paths, nil
	}
	return map[string][]string{id: f.paths[id]}, nil
}

func (f *fakeRepo) GetLastCheck(_ context.Context, id string) (*deploypolicy.LastCheck, error) {
	if c, ok := f.checks[id]; ok {
		return c, nil
	}
	return nil, deploypolicy.ErrNotFound
}

func (f *fakeRepo) ListLastChecks(context.Context) (map[string]*deploypolicy.LastCheck, error) {
	return f.checks, nil
}

func (f *fakeRepo) SaveLastCheck(_ context.Context, id, sha string, values []deploypolicy.PinnedValue) error {
	f.checks[id] = &deploypolicy.LastCheck{CommitSHA: sha, Values: values}
	return nil
}

type fakeOperators map[string]bool

func (f fakeOperators) IsOperator(_ context.Context, id string) (bool, error) { return f[id], nil }

type fakeOwnership map[string]string // serviceID -> orgID

func (f fakeOwnership) CheckOwnership(_ context.Context, id, orgID string) (bool, error) {
	return f[id] == orgID, nil
}

// fakeEnv maps a service ID to KEY=VALUE pairs; keys starting with "SECRET"
// are secrets.
type fakeEnv map[string][]string

func (f fakeEnv) ListAllDecrypted(_ context.Context, id string) ([]envvar.EnvVar, error) {
	out := make([]envvar.EnvVar, 0, len(f[id]))
	for _, kv := range f[id] {
		k, v, _ := strings.Cut(kv, "=")
		out = append(out, envvar.EnvVar{Key: k, Value: v, IsSecret: strings.HasPrefix(k, "SECRET")})
	}
	return out, nil
}

type failingEnv struct{}

func (failingEnv) ListAllDecrypted(context.Context, string) ([]envvar.EnvVar, error) {
	return nil, errors.New("db down")
}

func rules(vs []composespec.Violation) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, string(v.Rule))
	}
	sort.Strings(out)
	return out
}

// ─── ServiceSpec ──────────────────────────────────────────────────────────────

func TestCheckServiceSpec(t *testing.T) {
	vs := deploypolicy.CheckServiceSpec(deploypolicy.ServiceSpec{
		Volumes: []deploypolicy.VolumeSpec{
			{Name: "data", Target: "/data"},
			{Name: "sock", Target: "/var/run/docker.sock", HostPath: "/var/run/docker.sock"},
			{Name: "root", Target: "/host", HostPath: "/"},
		},
		PreDeployCommands: []string{"echo hi", "  "},
		ConfigFilePaths:   []string{"/etc/nginx/nginx.conf", "/../../etc/cron.d/x"},
	})
	assert.Equal(t, []string{"config_path", "docker_socket", "host_bind_mount", "host_exec"}, rules(vs))
}

func TestCheckServiceSpec_Clean(t *testing.T) {
	assert.Empty(t, deploypolicy.CheckServiceSpec(deploypolicy.ServiceSpec{
		Volumes:         []deploypolicy.VolumeSpec{{Name: "data", Target: "/data"}},
		ConfigFilePaths: []string{"/etc/app.yml"},
	}))
}

// ─── CheckDeploy ──────────────────────────────────────────────────────────────

func TestCheckDeploy_EnforceBlocks(t *testing.T) {
	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	d := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{
		ServiceID:      "s1",
		ServiceName:    "web",
		SourceType:     deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: nginx\n    privileged: ${PRIV:-false}\n",
		EnvVars:        []string{"PRIV=true"},
	})
	assert.True(t, d.Blocked)
	assert.Equal(t, []string{"privileged"}, rules(d.Violations))
	var verr *composespec.ViolationsError
	require.True(t, errors.As(d.Err(), &verr))
	assert.Len(t, verr.Violations, 1)
}

func TestCheckDeploy_WarnModeDoesNotBlock(t *testing.T) {
	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeWarn, nil)
	d := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{
		ServiceID:         "s1",
		SourceType:        deploypolicy.SourceImage,
		PreDeployCommands: []string{"rm -rf /tmp/cache"},
	})
	assert.False(t, d.Blocked)
	assert.NoError(t, d.Err())
	assert.Equal(t, []string{"host_exec"}, rules(d.Violations))
	assert.Contains(t, d.LogLines()[0], "warn mode")
}

// A compose service's own traefik.* labels are refused at deploy even in warn
// mode: otherwise the stack would start and then be taken down by the check
// after up. Other violations stay warnings, and saving is not refused.
func TestCheckDeploy_WarnModeStillRefusesSelfRouting(t *testing.T) {
	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeWarn, nil)
	compose := "services:\n  web:\n    image: nginx\n    privileged: true\n    labels:\n      traefik.http.routers.web.rule: Host(`app.example.com`)\n" +
		"networks:\n  edge:\n    labels:\n      traefik.enable: \"true\"\n"
	d := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{
		ServiceID: "s1", ServiceName: "web", SourceType: deploypolicy.SourceCompose, ComposeContent: compose,
	})
	require.True(t, d.Blocked)
	var verr *composespec.ViolationsError
	require.True(t, errors.As(d.Err(), &verr))
	require.Len(t, verr.Violations, 1, "only the service's traefik label refuses the deploy: %v", verr.Violations)
	assert.Equal(t, "services.web.labels.traefik.http.routers.web.rule", verr.Violations[0].Field)

	lines := strings.Join(d.LogLines(), "\n")
	assert.Contains(t, lines, "✗ deploy policy (refused in warn mode too")
	assert.Contains(t, lines, "⚠ deploy policy (warn mode, not enforced): ", "privileged and the network label stay warnings")

	plain := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{
		ServiceID: "s1", ServiceName: "web", SourceType: deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: nginx\n    privileged: true\n",
	})
	assert.False(t, plain.Blocked, "without traefik labels warn mode still lets it through")

	save := svc.CheckSave(context.Background(), deploypolicy.SaveCheck{ServiceID: "s1", ServiceName: "web", SourceType: deploypolicy.SourceCompose, ComposeContent: compose})
	assert.False(t, save.Blocked, "saving is not refused in warn mode, so the labels can be removed in steps")
}

func TestCheckDeploy_ExceptionSuppressesOnlyGrantedRules(t *testing.T) {
	repo := newFakeRepo()
	repo.exceptions["s1"] = &deploypolicy.Exception{AnyValue: true,
		ServiceID: "s1",
		// reserved_label and external_ref can never be granted, even if a
		// row somehow lists them.
		AllowedRules: []composespec.Rule{composespec.RuleDockerSocket, composespec.RuleReservedLabel, composespec.RuleExternalRef},
	}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil)
	d := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{
		ServiceID:  "s1",
		SourceType: deploypolicy.SourceCompose,
		ComposeContent: `services:
  agent:
    image: monitor
    privileged: true
    volumes: ["/var/run/docker.sock:/var/run/docker.sock:ro"]
    env_file: /app/.env
    labels: {traefik.enable: "true"}
`,
	})
	assert.True(t, d.Blocked)
	assert.Equal(t, []string{"external_ref", "privileged", "reserved_label"}, rules(d.Violations))
	assert.Equal(t, []string{"docker_socket"}, rules(d.Allowed))
}

func TestCheckDeploy_ExceptionReadErrorFailsClosed(t *testing.T) {
	repo := newFakeRepo()
	repo.exceptions["s1"] = &deploypolicy.Exception{AllowedRules: []composespec.Rule{composespec.RuleHostExec}}
	repo.getErr = errors.New("db down")
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil)
	d := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{ServiceID: "s1", SourceType: "git", PreDeployCommands: []string{"x"}})
	assert.True(t, d.Blocked)
}

func TestCheckDeploy_SingleContainerHostPath(t *testing.T) {
	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	d := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{
		ServiceID:  "s1",
		SourceType: deploypolicy.SourceImage,
		Volumes:    []deploypolicy.VolumeSpec{{Name: "x", Target: "/x", HostPath: "/etc"}},
		EnvVars:    []string{"GOOD=1", "BAD-KEY=2"},
	})
	assert.Equal(t, []string{"host_bind_mount"}, rules(d.Violations))
	require.Len(t, d.Warnings, 1, "invalid env keys on single-container services only warn")
	assert.Contains(t, d.Warnings[0], "BAD-KEY")
}

func TestCheckDeploy_ComposeReservedAndInvalidEnv(t *testing.T) {
	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	d := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{
		ServiceID:       "s1",
		SourceType:      deploypolicy.SourceCompose,
		ComposeFromRepo: true,
		EnvVars:         []string{"DOCKER_CONFIG=/tmp/x", "BAD-KEY=1", "FINE=2"},
	})
	assert.True(t, d.Blocked)
	assert.Equal(t, []string{"reserved_env", "reserved_env"}, rules(d.Violations))
}

func TestCheckComposeRepo(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "deploy"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "deploy", "docker-compose.yml"),
		[]byte("services:\n  web:\n    image: nginx\n    volumes: [\"../conf:/etc/conf\", \"/var/run/docker.sock:/s\"]\n"), 0o644))

	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	d := svc.CheckComposeRepo(context.Background(), deploypolicy.RepoCheck{
		ServiceID: "s1", ServiceName: "web", RepoDir: root, ComposeFilePath: "deploy/docker-compose.yml",
	})
	assert.Equal(t, []string{"docker_socket"}, rules(d.Violations), "../conf stays inside the repo for a remote deploy")

	require.NoError(t, os.Symlink("/etc", filepath.Join(root, "conf")))
	d = svc.CheckComposeRepo(context.Background(), deploypolicy.RepoCheck{
		ServiceID: "s1", ServiceName: "web", RepoDir: root, ComposeFilePath: "deploy/docker-compose.yml",
	})
	assert.Equal(t, []string{"external_ref"}, rules(d.Violations))
}

// A remote deploy replaces the compose directory's .env, and compose reads it
// for a file included from that directory. With the deploy's .env staged in
// the clone (as the pipeline does), the repository's .env can no longer
// neutralise a dangerous default at check time.
func TestCheckComposeRepo_RemoteIncludeIsCheckedWithTheDeployedDotEnv(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"docker-compose.yml": "include:\n  - sub.yml\n",
		"sub.yml":            "services:\n  app:\n    image: alpine\n    privileged: ${LOCK:-true}\n    volumes: [\"${ROOT:-/}:/host\"]\n",
		".env":               "LOCK=false\nROOT=.\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(content), 0o644))
	}
	env := []string{"A=1"}
	values := map[string]string{"A": "1"}
	require.NoError(t, composespec.StageDotEnv(root, "docker-compose.yml", values))

	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	d := svc.CheckComposeRepo(context.Background(), deploypolicy.RepoCheck{
		ServiceID: "s1", ServiceName: "web", RepoDir: root, ComposeFilePath: "docker-compose.yml", EnvVars: env,
	})
	assert.True(t, d.Blocked)
	assert.Equal(t, []string{"host_bind_mount", "privileged"}, rules(d.Violations))
}

// ─── CheckSave ────────────────────────────────────────────────────────────────

func TestCheckSave_IgnoresUnloadableComposeButReportsViolations(t *testing.T) {
	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	d := svc.CheckSave(context.Background(), deploypolicy.SaveCheck{
		SourceType:     deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: nginx\n    ports: [\"${PORT}:80\"]\n    privileged: notabool-${X}\n",
	})
	assert.False(t, d.Blocked, "a file that cannot load yet is left to the deploy check")

	d = svc.CheckSave(context.Background(), deploypolicy.SaveCheck{
		SourceType:     deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: nginx\n    cap_add: [NET_ADMIN]\n",
	})
	assert.True(t, d.Blocked)
	assert.Equal(t, []string{"cap_add"}, rules(d.Violations))
}

func TestCheckSave_UsesStoredEnvAndException(t *testing.T) {
	repo := newFakeRepo()
	repo.exceptions["s1"] = &deploypolicy.Exception{AllowedRules: []composespec.Rule{composespec.RuleNetworkModeHost}}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).
		WithEnvProvider(fakeEnv{"s1": {"NET=host"}})
	d := svc.CheckSave(context.Background(), deploypolicy.SaveCheck{
		ServiceID:      "s1",
		SourceType:     deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: nginx\n    network_mode: ${NET:-bridge}\n",
	})
	assert.False(t, d.Blocked)
	assert.Equal(t, []string{"network_mode_host"}, rules(d.Allowed))
}

// ─── Exceptions ───────────────────────────────────────────────────────────────

// digestOf is what an operator's open exception dialog sends with a grant.
func digestOf(t *testing.T, svc *deploypolicy.Service, serviceID string) string {
	t.Helper()
	p, err := svc.ServicePolicy(context.Background(), "op", "", serviceID)
	require.NoError(t, err)
	return p.ValuesDigest
}

func TestGrantException_OperatorOnly(t *testing.T) {
	repo := newFakeRepo()
	repo.services["s1"] = &deploypolicy.StoredService{ID: "s1"}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).WithOperatorChecker(fakeOperators{"op": true})

	_, err := svc.GrantException(context.Background(), "owner", "s1", []string{"docker_socket"}, "monitoring agent", "", "v")
	assert.ErrorIs(t, err, deploypolicy.ErrNotOperator)

	// No checker configured: fail closed.
	bare := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil)
	_, err = bare.GrantException(context.Background(), "op", "s1", []string{"docker_socket"}, "x", "", "v")
	assert.ErrorIs(t, err, deploypolicy.ErrNotOperator)

	e, err := svc.GrantException(context.Background(), "op", "s1", []string{"docker_socket", "host_bind_mount", "docker_socket"}, "  monitoring agent  ", digestOf(t, svc, "s1"), "v")
	require.NoError(t, err)
	assert.Equal(t, []composespec.Rule{composespec.RuleDockerSocket, composespec.RuleHostBindMount}, e.AllowedRules)
	require.Len(t, repo.grants, 1)
	assert.Equal(t, "monitoring agent", repo.grants[0].Reason)
	assert.Equal(t, "op", repo.grants[0].ActorID)
}

func TestGrantException_Validation(t *testing.T) {
	repo := newFakeRepo()
	repo.services["s1"] = &deploypolicy.StoredService{ID: "s1"}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).WithOperatorChecker(fakeOperators{"op": true})
	ctx := context.Background()

	_, err := svc.GrantException(ctx, "op", "s1", []string{"reserved_label"}, "x", "", "v")
	assert.ErrorIs(t, err, deploypolicy.ErrInvalidRules)
	_, err = svc.GrantException(ctx, "op", "s1", []string{"external_ref"}, "x", "", "v")
	assert.ErrorIs(t, err, deploypolicy.ErrInvalidRules)
	_, err = svc.GrantException(ctx, "op", "s1", []string{"made_up"}, "x", "", "v")
	assert.ErrorIs(t, err, deploypolicy.ErrInvalidRules)
	_, err = svc.GrantException(ctx, "op", "s1", []string{}, "x", "", "v")
	assert.ErrorIs(t, err, deploypolicy.ErrInvalidRules)
	_, err = svc.GrantException(ctx, "op", "s1", []string{"cap_add"}, "   ", "", "v")
	assert.ErrorIs(t, err, deploypolicy.ErrReasonRequired)
	_, err = svc.GrantException(ctx, "op", "missing", []string{"cap_add"}, "x", "", "v")
	assert.ErrorIs(t, err, deploypolicy.ErrServiceNotFound)
	assert.Empty(t, repo.grants)
}

func TestRevokeException(t *testing.T) {
	repo := newFakeRepo()
	repo.exceptions["s1"] = &deploypolicy.Exception{ServiceID: "s1", AllowedRules: []composespec.Rule{composespec.RuleCapAdd}}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).WithOperatorChecker(fakeOperators{"op": true})

	assert.ErrorIs(t, svc.RevokeException(context.Background(), "dev", "s1", ""), deploypolicy.ErrNotOperator)
	require.NoError(t, svc.RevokeException(context.Background(), "op", "s1", "no longer needed"))
	assert.Equal(t, []string{"s1"}, repo.revokes)
	assert.ErrorIs(t, svc.RevokeException(context.Background(), "op", "s1", ""), deploypolicy.ErrNotFound)
}

// ─── Views ────────────────────────────────────────────────────────────────────

func TestServicePolicy_Access(t *testing.T) {
	repo := newFakeRepo()
	repo.services["s1"] = &deploypolicy.StoredService{ID: "s1", Name: "web", SourceType: "docker-image",
		Volumes: []deploypolicy.VolumeSpec{{Name: "h", HostPath: "/srv"}}}
	repo.envKeys["s1"] = []string{"OK", "NOT-OK"}
	repo.paths["s1"] = []string{"/etc/../x"}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).
		WithOperatorChecker(fakeOperators{"op": true}).
		WithOwnershipChecker(fakeOwnership{"s1": "org-a"})
	ctx := context.Background()

	_, err := svc.ServicePolicy(ctx, "stranger", "org-b", "s1")
	assert.ErrorIs(t, err, deploypolicy.ErrServiceNotFound)

	p, err := svc.ServicePolicy(ctx, "member", "org-a", "s1")
	require.NoError(t, err)
	assert.False(t, p.CanManageExceptions)
	assert.Equal(t, []string{"config_path", "host_bind_mount"}, rules(p.Violations))
	assert.Equal(t, []string{"NOT-OK"}, p.InvalidEnvKeys)
	assert.NotEmpty(t, p.Rules)

	p, err = svc.ServicePolicy(ctx, "op", "org-b", "s1")
	require.NoError(t, err)
	assert.True(t, p.CanManageExceptions, "operators can read any service")
}

// A failed read of the exception is a failed read, not "no exception": the
// page would show an empty state with a version it could save on.
func TestServicePolicy_FailedExceptionReadIsAnError(t *testing.T) {
	repo := newFakeRepo()
	repo.services["s1"] = &deploypolicy.StoredService{ID: "s1", Name: "web", SourceType: "docker-image"}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).
		WithOperatorChecker(fakeOperators{"op": true})
	ctx := context.Background()

	p, err := svc.ServicePolicy(ctx, "op", "", "s1")
	require.NoError(t, err, "no exception is not an error")
	assert.Nil(t, p.Exception)

	repo.getErr = errors.New("db down")
	_, err = svc.ServicePolicy(ctx, "op", "", "s1")
	assert.ErrorIs(t, err, repo.getErr)
}

// The service's Deploy policy card sees the TOFT_URL its deploy gets
// (D-147): a catalog stack with a domain shows clean, without one it shows
// the template's own message.
func TestServicePolicy_SeesTheServiceURL(t *testing.T) {
	repo := newFakeRepo()
	file := "services:\n  web:\n    image: nginx\n    mem_limit: 64m\n    environment:\n      URL: \"${TOFT_URL:?Ghost needs the service’s address, so add a domain}\"\n"
	repo.services["s1"] = &deploypolicy.StoredService{ID: "s1", Name: "ghost", SourceType: "docker-compose", ComposeContent: file,
		Hostnames: []string{"*.example.com", "blog.example.com"}}
	repo.services["s2"] = &deploypolicy.StoredService{ID: "s2", Name: "ghost", SourceType: "docker-compose", ComposeContent: file}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).
		WithEnvProvider(fakeEnv{}).
		WithOwnershipChecker(fakeOwnership{"s1": "org-a", "s2": "org-a"})
	ctx := context.Background()

	p, err := svc.ServicePolicy(ctx, "member", "org-a", "s1")
	require.NoError(t, err)
	assert.Empty(t, p.Violations)

	p, err = svc.ServicePolicy(ctx, "member", "org-a", "s2")
	require.NoError(t, err)
	require.Len(t, p.Violations, 1)
	assert.Equal(t, "Ghost needs the service’s address, so add a domain", p.Violations[0].Message)
}

func TestReport(t *testing.T) {
	repo := newFakeRepo()
	repo.services["a-clean"] = &deploypolicy.StoredService{ID: "a-clean", Name: "clean", SourceType: "git"}
	repo.services["b-bad"] = &deploypolicy.StoredService{ID: "b-bad", Name: "bad", SourceType: "docker-compose",
		ComposeContent: "services:\n  x:\n    image: a\n    pid: host\n", OrgName: "Acme"}
	repo.services["c-repo"] = &deploypolicy.StoredService{ID: "c-repo", Name: "infra", SourceType: "docker-compose",
		ComposeRepoURL: "https://example.com/infra.git"}
	repo.services["d-excepted"] = &deploypolicy.StoredService{ID: "d-excepted", Name: "agent", SourceType: "git", PreDeployCommands: "make migrate",
		ServerID: "srv-1", ServerName: "fsn1"}
	granted := time.Date(2026, 10, 11, 9, 12, 0, 0, time.UTC)
	repo.exceptions["d-excepted"] = &deploypolicy.Exception{ServiceID: "d-excepted", AllowedRules: []composespec.Rule{composespec.RuleHostExec}, AnyValue: true,
		GrantedByEmail: "admin@example.test", UpdatedAt: granted}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeWarn, nil).WithOperatorChecker(fakeOperators{"op": true})

	_, err := svc.Report(context.Background(), "dev")
	assert.ErrorIs(t, err, deploypolicy.ErrNotOperator)

	r, err := svc.Report(context.Background(), "op")
	require.NoError(t, err)
	assert.Equal(t, deploypolicy.ModeWarn, r.Mode)
	require.Len(t, r.Services, 3)
	assert.Equal(t, "b-bad", r.Services[0].ServiceID)
	assert.Equal(t, []string{"host_namespace"}, rules(r.Services[0].Violations))
	assert.Equal(t, "c-repo", r.Services[1].ServiceID)
	assert.True(t, r.Services[1].ComposeCheckedAtDeploy)
	assert.Equal(t, "d-excepted", r.Services[2].ServiceID)
	assert.Empty(t, r.Services[2].Violations)
	assert.Equal(t, []string{"host_exec"}, rules(r.Services[2].Allowed))
	assert.Equal(t, "fsn1", r.Services[2].ServerName)
	assert.Equal(t, "admin@example.test", r.Services[2].ExceptionGrantedBy, "the email when the granter has no name")
	require.NotNil(t, r.Services[2].ExceptionGrantedAt)
	assert.Equal(t, granted, *r.Services[2].ExceptionGrantedAt)
	assert.Equal(t, 4, r.ServicesChecked)
	assert.Equal(t, 2, r.ServersChecked, "srv-1 and the control-plane server")
	assert.NotEmpty(t, r.Rules)
}

func TestParseMode(t *testing.T) {
	m, ok := deploypolicy.ParseMode("")
	assert.Equal(t, deploypolicy.ModeEnforce, m)
	assert.True(t, ok)
	m, ok = deploypolicy.ParseMode(" WARN ")
	assert.Equal(t, deploypolicy.ModeWarn, m)
	assert.True(t, ok)
	m, ok = deploypolicy.ParseMode("off")
	assert.Equal(t, deploypolicy.ModeEnforce, m, "unknown values fail closed")
	assert.False(t, ok)
}

func TestComposeProjectName(t *testing.T) {
	assert.Equal(t, "paas-my-app-12345678", deploypolicy.ComposeProjectName("12345678-aaaa", "My App!"))
	assert.Equal(t, "paas-12345678", deploypolicy.ComposeProjectName("12345678-aaaa", ""))
}

// ─── Secrets never leave through violations ──────────────────────────────────

func violationText(vs []composespec.Violation) string {
	var b strings.Builder
	for _, v := range vs {
		b.WriteString(v.Field + " " + v.Message + "\n")
	}
	return b.String()
}

func TestSecretsAreRedactedFromViolations(t *testing.T) {
	repo := newFakeRepo()
	compose := "services:\n  web:\n    image: nginx\n    cap_add: [\"${SECRET_DB_PASSWORD}\"]\n    volumes: [\"/${SECRET_JWT}/x:/x\"]\n    labels: [\"traefik.${SECRET_DB_PASSWORD}=1\"]\n    privileged: ${SECRET_JWT}\n"
	repo.services["s1"] = &deploypolicy.StoredService{ID: "s1", Name: "web", SourceType: deploypolicy.SourceCompose, ComposeContent: compose}
	env := fakeEnv{"s1": {"SECRET_DB_PASSWORD=hunter2-secret", "SECRET_JWT=supersecretvalue", "PLAIN=visible"}}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).
		WithEnvProvider(env).
		WithOwnershipChecker(fakeOwnership{"s1": "org-a"}).
		WithOperatorChecker(fakeOperators{"op": true})
	ctx := context.Background()

	assertClean := func(t *testing.T, vs []composespec.Violation) {
		t.Helper()
		require.NotEmpty(t, vs)
		text := violationText(vs)
		assert.NotContains(t, text, "hunter2-secret")
		assert.NotContains(t, text, "supersecretvalue")
		assert.Contains(t, text, "[secret]")
	}

	// Save time (a 422 body), with a value that does not load as a bool.
	d := svc.CheckSave(ctx, deploypolicy.SaveCheck{ServiceID: "s1", ServiceName: "web", SourceType: deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: nginx\n    cap_add: [\"${SECRET_DB_PASSWORD}\"]\n    volumes: [\"/${SECRET_JWT}:/x\"]\n"})
	assertClean(t, d.Violations)
	assert.NotContains(t, d.Err().Error(), "hunter2-secret")

	// Deploy time: the deploy log and the 422 body.
	d = svc.CheckDeploy(ctx, deploypolicy.DeployCheck{ServiceID: "s1", ServiceName: "web", SourceType: deploypolicy.SourceCompose,
		ComposeContent: compose, EnvVars: env["s1"]})
	assertClean(t, d.Violations)
	assert.NotContains(t, strings.Join(d.LogLines(), "\n"), "supersecretvalue")

	// The service view (any member) and the operator report.
	p, err := svc.ServicePolicy(ctx, "viewer", "org-a", "s1")
	require.NoError(t, err)
	assertClean(t, p.Violations)
	r, err := svc.Report(ctx, "op")
	require.NoError(t, err)
	require.Len(t, r.Services, 1)
	assertClean(t, r.Services[0].Violations)
}

func TestDeployRedactsEveryValueWhenSecretsAreUnknown(t *testing.T) {
	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil).WithEnvProvider(failingEnv{})
	d := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{ServiceID: "s1", SourceType: deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: nginx\n    cap_add: [\"${X}\"]\n", EnvVars: []string{"X=maybe-a-secret"}})
	require.Equal(t, []string{"cap_add"}, rules(d.Violations))
	assert.NotContains(t, d.Violations[0].Message, "maybe-a-secret")
}

func TestPlainEnvValuesAreShown(t *testing.T) {
	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil).WithEnvProvider(fakeEnv{"s1": {"CAP=NET_ADMIN"}})
	d := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{ServiceID: "s1", SourceType: deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: nginx\n    cap_add: [\"${CAP}\"]\n", EnvVars: []string{"CAP=NET_ADMIN"}})
	require.Equal(t, []string{"cap_add"}, rules(d.Violations))
	assert.Contains(t, d.Violations[0].Message, "NET_ADMIN")
}

func TestCheckDeploy_ReturnsComposeServices(t *testing.T) {
	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	d := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{ServiceID: "s1", SourceType: deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: nginx\n    mem_limit: 1g\n  db:\n    image: postgres\n    profiles: [later]\n    deploy: {resources: {limits: {cpus: \"0.5\"}}}\n"})
	assert.Equal(t, []string{"db", "web"}, d.ComposeServices)
	assert.Equal(t, map[string]int64{"db": 0, "web": 1024}, d.ComposeMemoryMB)
	assert.Equal(t, map[string]bool{"db": true}, d.ComposeDeployLimits)
}

// ─── Exception reasons are counted in characters ─────────────────────────────

func TestGrantException_ReasonLengthInCharacters(t *testing.T) {
	repo := newFakeRepo()
	repo.services["s1"] = &deploypolicy.StoredService{ID: "s1"}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).WithOperatorChecker(fakeOperators{"op": true})
	greek := strings.Repeat("λ", deploypolicy.MaxReasonLength) // 2 bytes each
	_, err := svc.GrantException(context.Background(), "op", "s1", []string{"cap_add"}, greek, digestOf(t, svc, "s1"), "v")
	require.NoError(t, err)
	_, err = svc.GrantException(context.Background(), "op", "s1", []string{"cap_add"}, greek+"λ", "", "v")
	assert.ErrorIs(t, err, deploypolicy.ErrReasonRequired)
}

type reasonRepo struct {
	*fakeRepo
	reason string
}

func (r *reasonRepo) RevokeException(ctx context.Context, id, actor, reason string) (*deploypolicy.Exception, error) {
	r.reason = reason
	return r.fakeRepo.RevokeException(ctx, id, actor, reason)
}

func TestRevokeException_TruncatesReasonOnRuneBoundary(t *testing.T) {
	repo := &reasonRepo{fakeRepo: newFakeRepo()}
	repo.exceptions["s1"] = &deploypolicy.Exception{ServiceID: "s1", AllowedRules: []composespec.Rule{composespec.RuleCapAdd}}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).WithOperatorChecker(fakeOperators{"op": true})
	require.NoError(t, svc.RevokeException(context.Background(), "op", "s1", "a"+strings.Repeat("λ", deploypolicy.MaxReasonLength)))
	assert.True(t, utf8.ValidString(repo.reason))
	assert.Equal(t, deploypolicy.MaxReasonLength, utf8.RuneCountInString(repo.reason))
}

// A value pinned by its plain fingerprint (before v0.28.0) still deploys,
// and the deploy rewrites the pin to the keyed one (D-126).
func TestCheckDeploy_RepinsAPlainFingerprint(t *testing.T) {
	repo := newFakeRepo()
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).WithHashKey([]byte("instance-key"))
	ctx := context.Background()
	in := deploypolicy.DeployCheck{ServiceID: "s1", ServiceName: "stack", SourceType: deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: nginx\n    cap_add: [NET_ADMIN]\n"}
	d := svc.CheckDeploy(ctx, in)
	require.True(t, d.Blocked)
	require.Len(t, d.Violations, 1)
	v := d.Violations[0]
	require.NotEmpty(t, v.AltFingerprint)
	require.NotEqual(t, v.Fingerprint, v.AltFingerprint)

	repo.exceptions["s1"] = &deploypolicy.Exception{ServiceID: "s1", AllowedRules: []deploypolicy.Rule{v.Rule},
		AllowedValues: []deploypolicy.PinnedValue{{Fingerprint: v.AltFingerprint, Rule: v.Rule}}}
	assert.False(t, svc.CheckDeploy(ctx, in).Blocked)
	assert.Equal(t, [][2][]string{{{v.AltFingerprint}, {v.Fingerprint}}}, repo.repins)
	assert.Equal(t, v.Fingerprint, repo.exceptions["s1"].AllowedValues[0].Fingerprint)

	assert.False(t, svc.CheckDeploy(ctx, in).Blocked, "the keyed pin matches")
	assert.Len(t, repo.repins, 1, "and needs no rewrite")
}

// ─── Control-plane stopgap ───────────────────────────────────────────────────

func TestControlPlaneException(t *testing.T) {
	repo := newFakeRepo()
	repo.services["s1"] = &deploypolicy.StoredService{ID: "s1", Name: "web", SourceType: "git"}
	repo.services["s2"] = &deploypolicy.StoredService{ID: "s2", Name: "api", SourceType: "git", ServerID: "srv-1"}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).
		WithOperatorChecker(fakeOperators{"op": true}).
		WithControlPlaneStopgap(true)
	ctx := context.Background()

	assert.False(t, svc.AllowsControlPlane(ctx, "s1"))
	p, err := svc.ServicePolicy(ctx, "op", "", "s1")
	require.NoError(t, err)
	assert.True(t, p.ControlPlaneRestricted)
	p, err = svc.ServicePolicy(ctx, "op", "", "s2")
	require.NoError(t, err)
	assert.False(t, p.ControlPlaneRestricted, "a service on another server is not affected")

	_, err = svc.GrantException(ctx, "op", "s1", []string{"control_plane"}, "trusted team, single-server install", digestOf(t, svc, "s1"), "v")
	require.NoError(t, err)
	assert.True(t, svc.AllowsControlPlane(ctx, "s1"))
	p, err = svc.ServicePolicy(ctx, "op", "", "s1")
	require.NoError(t, err)
	assert.False(t, p.ControlPlaneRestricted)

	// Every exception is in the operator report, even with no violation.
	r, err := svc.Report(ctx, "op")
	require.NoError(t, err)
	require.Len(t, r.Services, 1)
	assert.Equal(t, []composespec.Rule{composespec.RuleControlPlane}, r.Services[0].AllowedRules)
	assert.Equal(t, "trusted team, single-server install", r.Services[0].ExceptionReason)

	repo.getErr = errors.New("db down")
	assert.False(t, svc.AllowsControlPlane(ctx, "s1"), "a read error fails closed")
}

// ─── Domain Routing choices in the service view ──────────────────────────────

func TestServicePolicy_ComposeServices(t *testing.T) {
	repo := newFakeRepo()
	repo.services["inline"] = &deploypolicy.StoredService{ID: "inline", Name: "stack", SourceType: deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: \"nginx:${TAG}\"\n  db:\n    image: postgres\n    profiles: [later]\n  cache:\n    image: redis\n    privileged: true\n"}
	repo.services["repo"] = &deploypolicy.StoredService{ID: "repo", Name: "infra", SourceType: deploypolicy.SourceCompose, ComposeRepoURL: "https://example.com/infra.git"}
	repo.services["broken"] = &deploypolicy.StoredService{ID: "broken", Name: "x", SourceType: deploypolicy.SourceCompose, ComposeContent: "services: ["}
	repo.services["git"] = &deploypolicy.StoredService{ID: "git", Name: "api", SourceType: "git"}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).
		WithOperatorChecker(fakeOperators{"op": true}).
		WithEnvProvider(fakeEnv{"inline": {"TAG=1.27"}})
	ctx := context.Background()

	p, err := svc.ServicePolicy(ctx, "op", "", "inline")
	require.NoError(t, err)
	assert.Equal(t, []string{"cache", "db", "web"}, p.ComposeServices, "sorted, profiles included, even with violations")
	assert.NotEmpty(t, p.Violations)

	b, err := json.Marshal(p)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"compose_services":["cache","db","web"]`)

	for _, id := range []string{"repo", "broken", "git"} {
		p, err := svc.ServicePolicy(ctx, "op", "", id)
		require.NoError(t, err)
		assert.Nil(t, p.ComposeServices, id)
		b, err := json.Marshal(p)
		require.NoError(t, err)
		assert.Contains(t, string(b), `"compose_services":null`, id)
	}
}

// Secrets with characters that Go and JSON escape (quotes, backslashes,
// control characters, non-ASCII) are redacted in their escaped forms too:
// compose-go load errors and some checker messages quote the value.
func TestSecretsWithEscapedCharactersAreRedacted(t *testing.T) {
	secret := "p@ss\"w\\rd\t✓\x01<&>"
	cases := map[string]string{
		"load error (ParseFloat)": "services:\n  web:\n    image: nginx\n    cpus: ${SECRET_DB_PASS}\n",
		"security_opt":            "services:\n  web:\n    image: nginx\n    security_opt: [\"${SECRET_DB_PASS}\"]\n",
		"volume name":             "services:\n  web:\n    image: nginx\nvolumes:\n  data:\n    name: \"${SECRET_DB_PASS}\"\n",
		"build entitlement":       "services:\n  web:\n    build:\n      context: .\n      entitlements: [\"${SECRET_DB_PASS}\"]\n",
		"reserved label":          "services:\n  web:\n    image: nginx\n    labels: [\"traefik.${SECRET_DB_PASS}=1\"]\n",
		// Redacted, then cut to 500 characters.
		"long load error": "services:\n  web:\n    image: nginx\n    cpus: \"${SECRET_DB_PASS}" + strings.Repeat("x", 600) + "\"\n",
	}
	env := fakeEnv{"s1": {"SECRET_DB_PASS=" + secret}}
	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil).WithEnvProvider(env)
	ctx := context.Background()
	leaks := func(t *testing.T, text string) {
		t.Helper()
		for _, part := range []string{"p@ss", "✓", `\u2713`, "x01", "u0001", "u003c"} {
			assert.NotContains(t, text, part)
		}
	}
	for name, compose := range cases {
		t.Run(name, func(t *testing.T) {
			d := svc.CheckDeploy(ctx, deploypolicy.DeployCheck{ServiceID: "s1", ServiceName: "web", SourceType: deploypolicy.SourceCompose,
				ComposeContent: compose, EnvVars: env["s1"]})
			require.NotEmpty(t, d.Violations)
			text := violationText(d.Violations) + strings.Join(d.LogLines(), "\n") + d.Err().Error()
			leaks(t, text)
			assert.Contains(t, text, "[secret]")
			if name == "long load error" {
				assert.True(t, strings.HasSuffix(d.Violations[0].Message, "…"), "long messages are still cut")
				assert.Equal(t, 501, utf8.RuneCountInString(d.Violations[0].Message))
			}

			// Save time (the 422 body); a file that cannot load is left to
			// the deploy check there.
			saved := svc.CheckSave(ctx, deploypolicy.SaveCheck{ServiceID: "s1", ServiceName: "web", SourceType: deploypolicy.SourceCompose, ComposeContent: compose})
			leaks(t, violationText(saved.Violations))
		})
	}
}

func TestCheckDeploy_AppNetworkRuleOnlyUnderIsolation(t *testing.T) {
	in := deploypolicy.DeployCheck{
		ServiceID:      "s1",
		ServiceName:    "web",
		SourceType:     deploypolicy.SourceCompose,
		ComposeContent: "services:\n  web:\n    image: nginx\n    networks: [x]\nnetworks:\n  x:\n    external: true\n    name: paas-env-someone-else\n",
	}
	legacy := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	assert.False(t, legacy.CheckDeploy(context.Background(), in).Blocked)

	isolated := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil).
		WithAppNetworks(composespec.AppNetworkPolicy{Isolated: true, Legacy: []string{"paas-network"}})
	d := isolated.CheckDeploy(context.Background(), in)
	assert.True(t, d.Blocked)
	assert.Equal(t, []string{"app_network"}, rules(d.Violations))

	in.ComposeContent = "services:\n  web:\n    image: nginx\n    networks: [paas-network]\nnetworks:\n  paas-network:\n    external: true\n"
	assert.False(t, isolated.CheckDeploy(context.Background(), in).Blocked, "the control plane's network name is rewritten, not refused")
}

// ─── memory_limit (D-41) ─────────────────────────────────────────────────────

func memCheck(grace *time.Time) deploypolicy.DeployCheck {
	return deploypolicy.DeployCheck{
		ServiceID: "s1", SourceType: deploypolicy.SourceImage,
		Memory: &resources.MemoryCheck{GraceUntil: grace},
	}
}

func TestCheckDeploy_MemoryLimit(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	newSvc := func(repo *fakeRepo, mode deploypolicy.Mode) *deploypolicy.Service {
		s := deploypolicy.NewService(repo, mode, nil)
		s.SetNow(func() time.Time { return now })
		return s
	}
	ctx := context.Background()

	d := newSvc(newFakeRepo(), deploypolicy.ModeEnforce).CheckDeploy(ctx, memCheck(nil))
	assert.True(t, d.Blocked)
	assert.Equal(t, []string{"memory_limit"}, rules(d.Violations))

	repo := newFakeRepo()
	// Granted since v0.25.0, with no value pinned: a missing limit is
	// always "no limit", so the rule covers it.
	repo.exceptions["s1"] = &deploypolicy.Exception{ServiceID: "s1", AllowedRules: []deploypolicy.Rule{composespec.RuleMemoryLimit}, AllowedValues: []deploypolicy.PinnedValue{}}
	d = newSvc(repo, deploypolicy.ModeEnforce).CheckDeploy(ctx, memCheck(nil))
	assert.False(t, d.Blocked)
	assert.Equal(t, []string{"memory_limit"}, rules(d.Allowed))

	d = newSvc(newFakeRepo(), deploypolicy.ModeWarn).CheckDeploy(ctx, memCheck(nil))
	assert.False(t, d.Blocked)
	assert.Contains(t, d.LogLines()[0], "warn mode, not enforced")

	grace := now.Add(24 * time.Hour)
	d = newSvc(newFakeRepo(), deploypolicy.ModeEnforce).CheckDeploy(ctx, memCheck(&grace))
	assert.False(t, d.Blocked)
	assert.Empty(t, d.Violations)
	assert.Len(t, d.Warnings, 1)

	// Nil Memory: not checked.
	d = newSvc(newFakeRepo(), deploypolicy.ModeEnforce).CheckDeploy(ctx, deploypolicy.DeployCheck{ServiceID: "s1", SourceType: deploypolicy.SourceImage})
	assert.False(t, d.Blocked)
}

func TestCheckDeploy_MemoryGraceBoundary(t *testing.T) {
	grace := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	for now, blocked := range map[time.Time]bool{
		time.Date(2026, 10, 18, 23, 59, 0, 0, time.UTC): false,
		grace: true,
	} {
		s := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
		s.SetNow(func() time.Time { return now })
		d := s.CheckDeploy(context.Background(), memCheck(&grace))
		assert.Equal(t, blocked, d.Blocked, now.String())
	}
}

func TestCheckSave_AndRepo_Memory(t *testing.T) {
	s := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	d := s.CheckSave(context.Background(), deploypolicy.SaveCheck{SourceType: deploypolicy.SourceImage, Memory: &resources.MemoryCheck{}})
	assert.True(t, d.Blocked)
}

func TestCheckComposeRepo_MemoryFromTheRepositoryFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "docker-compose.yml"),
		[]byte("services:\n  db:\n    image: postgres\n    mem_limit: 512m\n  web:\n    image: nginx\n  cache:\n    image: redis\n    deploy: {resources: {limits: {memory: 256m}}}\n"), 0o644))
	grace := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	s.SetNow(func() time.Time { return grace.Add(24 * time.Hour) })
	d := s.CheckComposeRepo(context.Background(), deploypolicy.RepoCheck{
		ServiceID: "s1", ServiceName: "web", RepoDir: root, ComposeFilePath: "docker-compose.yml",
		Memory: &resources.MemoryCheck{Compose: true, GraceUntil: &grace},
	})
	require.Len(t, d.Violations, 1)
	assert.Equal(t, "memory_limit", string(d.Violations[0].Rule))
	assert.Equal(t, "web", d.Violations[0].Service)
	assert.True(t, d.Blocked)
	assert.Equal(t, map[string]int64{"db": 512, "web": 0, "cache": 256}, d.ComposeMemoryMB)
	assert.Equal(t, map[string]bool{"cache": true}, d.ComposeDeployLimits)
}

func TestCheckDeploy_ComposeMemory_FileServicesWithPanelLimit(t *testing.T) {
	s := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	d := s.CheckDeploy(context.Background(), deploypolicy.DeployCheck{
		ServiceID: "s1", SourceType: deploypolicy.SourceCompose,
		ComposeContent: "services:\n  a:\n    image: x\n  b:\n    image: y\n    mem_limit: 64m\n",
		Memory: &resources.MemoryCheck{Compose: true, ComposeServices: map[string]resources.ComposeLimitSource{
			"a": {PanelMB: 128}, "gone": {},
		}},
	})
	assert.Empty(t, d.Violations, "a has the panel limit, b the file's, gone is ignored")
}
