package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffectiveDeployStrategy(t *testing.T) {
	cases := []struct {
		strategy, addon, image, want string
	}{
		{"auto", "", "postgres:16", DeployStrategyRecreate},
		{"auto", "mysql", "", DeployStrategyRecreate},
		{"auto", "", "ghcr.io/acme/mariadb:11@sha256:abc", DeployStrategyRecreate},
		{"auto", "", "redis:7-alpine", DeployStrategyRecreate},
		{"auto", "", "valkey/valkey:8", DeployStrategyRecreate},
		{"", "", "mongo:7", DeployStrategyRecreate},
		{"auto", "", "nginx:alpine", DeployStrategyRolling},
		{"auto", "phpmyadmin", "phpmyadmin:5", DeployStrategyRolling},
		{"auto", "", "", DeployStrategyRolling}, // a git service
		{"rolling", "", "postgres:16", DeployStrategyRolling},
		{"recreate", "", "nginx:alpine", DeployStrategyRecreate},
		{"rolling", "loki", "grafana/loki:3", DeployStrategyRecreate}, // one 127.0.0.1 port (D-148)
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, EffectiveDeployStrategy(tc.strategy, tc.addon, tc.image), "%+v", tc)
	}
}

func TestValidateDeployStrategy(t *testing.T) {
	for _, ok := range []string{"", "auto", "rolling", "recreate"} {
		assert.NoError(t, validateDeployStrategy(ok), ok)
	}
	for _, bad := range []string{"blue-green", "Recreate", "auto "} {
		assert.ErrorIs(t, validateDeployStrategy(bad), ErrInvalidConfig, bad)
	}
}

func TestServiceJSONShowsTheEffectiveDeployStrategy(t *testing.T) {
	b, err := json.Marshal(Service{DockerImage: "mysql:8", DeployStrategy: "auto"})
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, "auto", got["deploy_strategy"])
	assert.Equal(t, "recreate", got["effective_deploy_strategy"])
}
