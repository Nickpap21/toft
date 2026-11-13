package deployment

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/internal/service"
)

type panelOnly struct {
	limits map[string]resources.ComposeLimit
}

func (panelOnly) MemoryCheckFor(context.Context, resources.ServiceRef) (*resources.MemoryCheck, error) {
	return nil, nil
}
func (panelOnly) AddComposeDefaults(context.Context, string, composespec.Source) (map[string]resources.DefaultAdded, error) {
	return nil, nil
}
func (p panelOnly) ComposeLimits(context.Context, string) (map[string]resources.ComposeLimit, error) {
	return p.limits, nil
}

func TestRequestedLimits(t *testing.T) {
	half := 0.5
	s := &Service{resources: panelOnly{limits: map[string]resources.ComposeLimit{
		"web": {MemoryMB: 256, CPUCores: &half},
		"db":  {MemoryMB: 1024},
	}}}
	ctx := context.Background()

	// A compose service requests the sum of its panel limits; its own
	// single-container fields are ignored.
	cpu, mem := s.requestedLimits(ctx, &service.Service{ID: "c", SourceType: service.SourceTypeDockerCompose, MemoryLimit: "9g"})
	assert.InDelta(t, 0.5, cpu, 1e-9)
	assert.InDelta(t, 1280, mem, 1e-9)

	// Single-container behaviour is unchanged.
	cpu, mem = s.requestedLimits(ctx, &service.Service{ID: "i", SourceType: service.SourceTypeDockerImage, CPULimit: "1.5", MemoryLimit: "512m"})
	assert.InDelta(t, 1.5, cpu, 1e-9)
	assert.InDelta(t, 512, mem, 1e-9)

	// No resources wired: a compose service requests nothing.
	cpu, mem = (&Service{}).requestedLimits(ctx, &service.Service{ID: "c", SourceType: service.SourceTypeDockerCompose})
	assert.Zero(t, cpu)
	assert.Zero(t, mem)
}
