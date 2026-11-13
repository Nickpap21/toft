package deploypolicy_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deploypolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An exception pins what it approved (K3), so every violation of a rule
// granted by value must carry the value it is pinned to.
func TestCheckDeploy_EveryGrantableViolationHasAValue(t *testing.T) {
	svc := deploypolicy.NewService(newFakeRepo(), deploypolicy.ModeEnforce, nil)
	d := svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{
		ServiceID:  "s1",
		SourceType: deploypolicy.SourceCompose,
		ComposeContent: `services:
  a:
    image: tool:1
    privileged: true
    cap_add: [SYS_ADMIN]
    group_add: ["0"]
    devices: ["/dev/fuse:/dev/fuse:rwm"]
    pid: host
    network_mode: host
    runtime: nvidia
    security_opt: ["seccomp:unconfined"]
    sysctls: {vm.swappiness: "10"}
    cgroup_parent: /x
    volumes: ["/var/run/docker.sock:/var/run/docker.sock", "/srv/data:/data", "ext:/e"]
  b:
    image: tool:1
    ipc: "container:other"
volumes:
  ext:
    external: true
`,
		PreDeployCommands: []string{"echo hi"},
	})
	seen := map[composespec.Rule]bool{}
	for _, v := range d.Violations {
		if !composespec.Grantable(v.Rule) {
			continue
		}
		seen[v.Rule] = true
		assert.NotEmpty(t, v.Value, "%s %s", v.Rule, v.Field)
		assert.NotEmpty(t, v.Fingerprint, "%s %s", v.Rule, v.Field)
	}
	for _, r := range []composespec.Rule{
		composespec.RulePrivileged, composespec.RuleCapAdd, composespec.RuleGroupAdd, composespec.RuleDevices,
		composespec.RuleHostNamespace, composespec.RuleNetworkModeHost, composespec.RuleContainerNamespace,
		composespec.RuleRuntime, composespec.RuleSecurityOpt, composespec.RuleSysctls, composespec.RuleCgroupParent,
		composespec.RuleDockerSocket, composespec.RuleHostBindMount, composespec.RuleExternalVolume, composespec.RuleHostExec,
	} {
		assert.True(t, seen[r], "no %s violation to check", r)
	}
}

func grantedService(t *testing.T) (*fakeRepo, *deploypolicy.Service) {
	t.Helper()
	repo := newFakeRepo()
	repo.services["s1"] = &deploypolicy.StoredService{
		ID: "s1", SourceType: deploypolicy.SourceImage, DockerImage: "agent:1",
		Volumes:           []deploypolicy.VolumeSpec{{Name: "data", HostPath: "/srv/a", Target: "/data"}},
		PreDeployCommands: "echo hi",
	}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).WithOperatorChecker(fakeOperators{"op": true})
	e, err := svc.GrantException(context.Background(), "op", "s1", []string{"host_bind_mount", "host_exec"}, "agent", digestOf(t, svc, "s1"), "v")
	require.NoError(t, err)
	require.Len(t, e.AllowedValues, 2)
	return repo, svc
}

func deployImage(svc *deploypolicy.Service, image, hostPath, cmd string) deploypolicy.Decision {
	return svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{
		ServiceID: "s1", SourceType: deploypolicy.SourceImage, Image: image,
		Volumes:           []deploypolicy.VolumeSpec{{Name: "data", HostPath: hostPath, Target: "/data"}},
		PreDeployCommands: []string{cmd},
	})
}

func TestException_PinsValues(t *testing.T) {
	_, svc := grantedService(t)

	d := deployImage(svc, "agent:1", "/srv/a", "echo hi")
	assert.False(t, d.Blocked)
	assert.Equal(t, []string{"host_bind_mount", "host_exec"}, rules(d.Allowed))
	for _, v := range d.Allowed {
		assert.Equal(t, deploypolicy.StatePinned, v.State)
	}

	for name, d := range map[string]deploypolicy.Decision{
		"image":   deployImage(svc, "agent:2", "/srv/a", "echo hi"),
		"path":    deployImage(svc, "agent:1", "/srv/b", "echo hi"),
		"command": deployImage(svc, "agent:1", "/srv/a", "echo bye"),
	} {
		assert.True(t, d.Blocked, name)
		require.NotEmpty(t, d.Violations, name)
		assert.Equal(t, deploypolicy.StatePending, d.Violations[0].State, name)
		assert.Contains(t, d.Violations[0].Message, "differs from what the exception approved", name)
	}
}

// An exception from before v0.25.0 has no values and still covers its
// rules at any value until it is granted again.
func TestException_LegacyCoversAnyValue(t *testing.T) {
	repo, svc := grantedService(t)
	repo.exceptions["s1"].AnyValue = true
	repo.exceptions["s1"].AllowedValues = nil

	d := deployImage(svc, "agent:2", "/srv/b", "echo bye")
	assert.False(t, d.Blocked)
	for _, v := range d.Allowed {
		assert.Equal(t, deploypolicy.StateGranted, v.State)
	}
}

// A secret's value counts though the screens never show it: a changed
// secret that changes a host path needs approving again.
func TestException_ChangedSecretIsRefused(t *testing.T) {
	repo := newFakeRepo()
	repo.services["s1"] = &deploypolicy.StoredService{ID: "s1", SourceType: deploypolicy.SourceCompose,
		ComposeContent: "services:\n  a:\n    image: x\n    volumes: [\"${SECRET_DIR}:/data\"]\n"}
	env := fakeEnv{"s1": {"SECRET_DIR=/srv/hidden-a"}}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).
		WithEnvProvider(env).WithOperatorChecker(fakeOperators{"op": true})
	e, err := svc.GrantException(context.Background(), "op", "s1", []string{"host_bind_mount"}, "data", digestOf(t, svc, "s1"), "v")
	require.NoError(t, err)
	require.Len(t, e.AllowedValues, 1)
	assert.NotContains(t, e.AllowedValues[0].Value, "hidden-a")

	deploy := func() deploypolicy.Decision {
		return svc.CheckDeploy(context.Background(), deploypolicy.DeployCheck{
			ServiceID: "s1", SourceType: deploypolicy.SourceCompose,
			ComposeContent: repo.services["s1"].ComposeContent, EnvVars: env["s1"],
		})
	}
	assert.False(t, deploy().Blocked)
	env["s1"] = []string{"SECRET_DIR=/srv/hidden-b"}
	d := deploy()
	assert.True(t, d.Blocked)
	require.Len(t, d.Violations, 1)
	assert.NotContains(t, d.Violations[0].Value, "hidden-b")
}

// A compose file from a repository is read only at deploy: the deploy
// records what it found, and a grant approves that.
func TestException_RepoComposePinsTheLastCheck(t *testing.T) {
	root := t.TempDir()
	write := func(volume string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, "docker-compose.yml"),
			[]byte("services:\n  web:\n    image: nginx\n    volumes: [\""+volume+"\"]\n"), 0o644))
	}
	write("/srv/a:/data")
	repo := newFakeRepo()
	repo.services["s1"] = &deploypolicy.StoredService{ID: "s1", SourceType: deploypolicy.SourceCompose, ComposeRepoURL: "https://git.example/infra"}
	svc := deploypolicy.NewService(repo, deploypolicy.ModeEnforce, nil).WithOperatorChecker(fakeOperators{"op": true})
	check := func() deploypolicy.Decision {
		return svc.CheckComposeRepo(context.Background(), deploypolicy.RepoCheck{
			ServiceID: "s1", ServiceName: "web", RepoDir: root, ComposeFilePath: "docker-compose.yml", CommitSHA: "abc123",
		})
	}

	assert.True(t, check().Blocked)
	require.Contains(t, repo.checks, "s1")
	assert.Equal(t, "abc123", repo.checks["s1"].CommitSHA)
	require.Len(t, repo.checks["s1"].Values, 1)

	e, err := svc.GrantException(context.Background(), "op", "s1", []string{"host_bind_mount"}, "data", digestOf(t, svc, "s1"), "v")
	require.NoError(t, err)
	require.Len(t, e.AllowedValues, 1)
	assert.False(t, check().Blocked)

	write("/srv/b:/data")
	assert.True(t, check().Blocked)
	p, err := svc.ServicePolicy(context.Background(), "op", "", "s1")
	require.NoError(t, err)
	require.NotNil(t, p.LastDeployCheck)
	require.Len(t, p.LastDeployCheck.Violations, 1)
	assert.Equal(t, deploypolicy.StatePending, p.LastDeployCheck.Violations[0].State)
}

// A grant approves what the operator saw: values that changed since the
// dialog opened are refused, not approved unseen.
func TestException_StaleDigestIsRefused(t *testing.T) {
	repo, svc := grantedService(t)
	digest := digestOf(t, svc, "s1")
	repo.services["s1"].Volumes[0].HostPath = "/srv/b"
	_, err := svc.GrantException(context.Background(), "op", "s1", []string{"host_bind_mount"}, "data", digest, "v")
	assert.ErrorIs(t, err, deploypolicy.ErrValuesChanged)
	assert.Len(t, repo.grants, 1, "only grantedService's grant")
}

// A grant keeps the values approved before for the rules it still grants,
// so a rollback to one passes; a rule it drops loses its values.
func TestException_RegrantKeepsEarlierValues(t *testing.T) {
	repo, svc := grantedService(t)
	repo.services["s1"].DockerImage = "agent:2"
	e, err := svc.GrantException(context.Background(), "op", "s1", []string{"host_bind_mount", "host_exec"}, "agent v2", digestOf(t, svc, "s1"), "v")
	require.NoError(t, err)
	assert.Len(t, e.AllowedValues, 4)
	assert.False(t, deployImage(svc, "agent:2", "/srv/a", "echo hi").Blocked)
	assert.False(t, deployImage(svc, "agent:1", "/srv/a", "echo hi").Blocked, "rollback to the approved v1")
	assert.True(t, deployImage(svc, "agent:3", "/srv/a", "echo hi").Blocked)

	e, err = svc.GrantException(context.Background(), "op", "s1", []string{"host_exec"}, "agent v2", digestOf(t, svc, "s1"), "v")
	require.NoError(t, err)
	for _, v := range e.AllowedValues {
		assert.Equal(t, composespec.RuleHostExec, v.Rule)
	}
}

// A token in a git URL is neither pinned nor shown.
func TestServiceImage_StripsGitCredentials(t *testing.T) {
	assert.Equal(t, "https://git.example/app.git",
		deploypolicy.ServiceImage(deploypolicy.SourceGit, "", "https://x-token:s3cret@git.example/app.git", ""))
	assert.Equal(t, "https://git.example/app.git",
		deploypolicy.ServiceImage(deploypolicy.SourceGit, "", "https://git.example/app.git", ""))
}
