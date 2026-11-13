package rollout

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
)

func TestApplyResourceLimitsSwapEqualsMemory(t *testing.T) {
	for _, mem := range []int64{512 << 20, 0} {
		hc := &container.HostConfig{}
		applyResourceLimits(hc, 0, mem)
		assert.Equal(t, mem, hc.Memory)
		assert.Equal(t, mem, hc.MemorySwap)
	}
	hc := &container.HostConfig{}
	applyResourceLimits(hc, 1.5, 0)
	assert.Equal(t, int64(150000), hc.CPUQuota)
	assert.Equal(t, int64(100000), hc.CPUPeriod)
}
