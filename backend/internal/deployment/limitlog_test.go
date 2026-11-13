package deployment

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Nickpap21/toft/backend/internal/resources"
)

func TestLimitLogLines(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  TriggerInput
		fileMB map[string]int64
		want   []string
	}{
		{"single set", TriggerInput{MemoryLimit: "768m"}, nil, []string{"→ memory limit 768m (panel)"}},
		{"single with cpu", TriggerInput{MemoryLimit: "768m", CPULimit: "1.5"}, nil, []string{"→ memory limit 768m (panel), cpu 1.5"}},
		{"single none", TriggerInput{}, nil, []string{"→ memory limit none"}},
		{"compose", TriggerInput{SourceType: "docker-compose", ComposeLimits: map[string]ComposeLimit{"web": {MemoryMB: 768}, "db": {MemoryMB: 1024}}},
			map[string]int64{"web": 256, "db": 512, "cache": 128, "worker": 0},
			[]string{"→ cache: memory limit 128m (compose file)", "→ db: memory limit 1g (panel)", "→ web: memory limit 768m (panel)", "→ worker: memory limit none"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, limitLogLines(tc.input, tc.fileMB, nil))
		})
	}
}

// A default the deploy itself added says so, once per compose service.
func TestLimitLogLines_DefaultAddedByTheDeploy(t *testing.T) {
	input := TriggerInput{SourceType: "docker-compose", ComposeLimits: map[string]ComposeLimit{"web": {MemoryMB: 512}, "db": {MemoryMB: 1024}}}
	added := map[string]resources.DefaultAdded{"web": {MemoryMB: 512, Kind: resources.KindApp}}
	assert.Equal(t, []string{
		"→ db: memory limit 1g (panel)",
		"→ web: memory limit 512m (default for app); change it on the service page",
	}, limitLogLines(input, map[string]int64{"web": 0, "db": 0}, added))
}
