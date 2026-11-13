package docker

import (
	"context"
	"fmt"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestComposeLimitsOverride(t *testing.T) {
	out, err := composeLimitsOverride(map[string]deployment.ComposeLimit{
		"db":  {MemoryMB: 1024},
		"app": {MemoryMB: 768, CPUCores: 1.5},
	})
	require.NoError(t, err)
	var got struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(out, &got))
	assert.EqualValues(t, 1073741824, got.Services["db"]["mem_limit"])
	assert.EqualValues(t, 1073741824, got.Services["db"]["memswap_limit"])
	assert.NotContains(t, got.Services["db"], "cpus")
	assert.EqualValues(t, 1.5, got.Services["app"]["cpus"])

	out, err = composeLimitsOverride(map[string]deployment.ComposeLimit{"cpu": {CPUCores: 2}, "empty": {}})
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(out, &got))
	assert.EqualValues(t, 2, got.Services["cpu"]["cpus"])
	assert.NotContains(t, got.Services["cpu"], "mem_limit")
	assert.NotContains(t, got.Services["cpu"], "memswap_limit")
	assert.NotContains(t, got.Services, "empty")

	out, err = composeLimitsOverride(map[string]deployment.ComposeLimit{"empty": {}})
	require.NoError(t, err)
	assert.Nil(t, out)

	out, err = composeLimitsOverride(nil)
	require.NoError(t, err)
	assert.Nil(t, out)

	_, err = composeLimitsOverride(map[string]deployment.ComposeLimit{"bad name": {MemoryMB: 1}})
	assert.Error(t, err)
}

func TestComposeOverridesFiltersLimitsToFileServices(t *testing.T) {
	limits := map[string]deployment.ComposeLimit{"db": {MemoryMB: 64}, "ghost": {MemoryMB: 64}}
	out, err := composeOverrides(testServiceID, rollout.NetConfig{}, nil, deployment.ComposeNetworks{Home: "paas-network", Services: []string{"db"}}, limits)
	require.NoError(t, err)
	var limitsFile *overrideFile
	for i := range out {
		if out[i].name == "docker-compose.limits.yml" {
			limitsFile = &out[i]
		}
	}
	require.NotNil(t, limitsFile)
	assert.Contains(t, string(limitsFile.content), "db:")
	assert.NotContains(t, string(limitsFile.content), "ghost")

	out, err = composeOverrides(testServiceID, rollout.NetConfig{}, nil, deployment.ComposeNetworks{Home: "paas-network"}, limits)
	require.NoError(t, err)
	for _, o := range out {
		assert.NotEqual(t, "docker-compose.limits.yml", o.name)
	}
}

// effectiveLimits are the memory (bytes) and CPU limits docker compose runs
// a service with: deploy.resources.limits wins over the top-level keys.
func effectiveLimits(s composetypes.ServiceConfig) (int64, float32) {
	mem, cpus := int64(s.MemLimit), s.CPUS
	if s.Deploy != nil && s.Deploy.Resources.Limits != nil {
		if b := int64(s.Deploy.Resources.Limits.MemoryBytes); b != 0 {
			mem = b
		}
		if c := s.Deploy.Resources.Limits.NanoCPUs.Value(); c != 0 {
			cpus = c
		}
	}
	return mem, cpus
}

// compose refuses distinct values for a top-level limit and its
// deploy.resources.limits counterpart (cpus, mem_limit, pids_limit) as soon
// as the service has a deploy.resources.limits block. The override mirrors
// the file: both forms where the file declares the block, the top-level
// keys only where it does not. Each file is loaded with the override the
// deploy would write, the form taken from the same InspectLimits load.
func TestComposeLimitsOverrideMirrorsTheFilesForm(t *testing.T) {
	const panelMB = 768
	files := map[string]string{
		"a: deploy memory":       "services:\n  db:\n    image: postgres\n    deploy:\n      resources:\n        limits:\n          memory: 512M\n",
		"b: mem_limit":           "services:\n  db:\n    image: postgres\n    mem_limit: 512m\n",
		"c: no limits":           "services:\n  db:\n    image: postgres\n",
		"d: deploy cpus":         "services:\n  db:\n    image: postgres\n    deploy:\n      resources:\n        limits:\n          cpus: \"0.5\"\n",
		"e: pids_limit":          "services:\n  db:\n    image: postgres\n    pids_limit: 100\n",
		"f: top-level cpus":      "services:\n  db:\n    image: postgres\n    cpus: 0.5\n",
		"g: deploy memory, pids": "services:\n  db:\n    image: postgres\n    pids_limit: 100\n    deploy:\n      resources:\n        limits:\n          memory: 512M\n          pids: 100\n",
	}
	for name, file := range files {
		for _, cpu := range []float64{0, 1.5} {
			t.Run(fmt.Sprintf("%s, panel cpu %g", name, cpu), func(t *testing.T) {
				src := composespec.Source{Root: "/nonexistent/toft-test", Content: file, ProjectName: "paas-test-0a1b2c3d"}
				vs, _, _, deployLimits := composespec.InspectLimits(context.Background(), src)
				require.Empty(t, vs)
				alone, _, err := composespec.Load(context.Background(), src)
				require.NoError(t, err)
				_, fileCPU := effectiveLimits(alone.Services["db"])

				override, err := composeLimitsOverride(map[string]deployment.ComposeLimit{
					"db": {MemoryMB: panelMB, CPUCores: cpu, DeployLimits: deployLimits["db"]},
				})
				require.NoError(t, err)
				src.Overrides = []composespec.Override{{Name: "docker-compose.limits.yml", Content: override}}
				p, lvs, err := composespec.Load(context.Background(), src)
				require.NoError(t, err, "compose must load the file with the override:\n%s", override)
				require.Empty(t, lvs)

				db := p.Services["db"]
				mem, cpus := effectiveLimits(db)
				assert.EqualValues(t, panelMB<<20, mem, "the panel's memory limit")
				assert.EqualValues(t, panelMB<<20, db.MemSwapLimit, "no swap beyond memory")
				if cpu > 0 {
					assert.EqualValues(t, cpu, cpus, "the panel's CPU limit")
				} else {
					assert.Equal(t, fileCPU, cpus, "no panel CPU: the file's stays")
				}
				if !deployLimits["db"] {
					assert.Nil(t, db.Deploy, "a file without deploy.resources.limits gets none")
				}
			})
		}
	}
}
