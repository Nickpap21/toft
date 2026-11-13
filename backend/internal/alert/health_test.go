package alert

import (
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/stretchr/testify/assert"
)

func TestMonitorKeepsTheLastHealthAnswer(t *testing.T) {
	m := &Monitor{}
	_, ok := m.LastHealthCheck("s1")
	assert.False(t, ok, "nothing before the first probe")

	t0 := time.Date(2026, 10, 14, 11, 2, 0, 0, time.UTC)
	blue := []service.ContainerState{{Name: "paas-web-s1-blue", HealthAnswer: &service.HealthAnswer{Status: 200, Millis: 1, Healthy: true}}}
	m.recordHealth("s1", blue, t0)
	m.recordHealth("s1", blue, t0.Add(3*time.Hour))
	a, ok := m.LastHealthCheck("s1")
	assert.True(t, ok)
	assert.Equal(t, 200, a.Status)
	assert.Equal(t, t0.Add(3*time.Hour), a.At)
	assert.Equal(t, t0, a.Since, "the same container: checked every minute since its first answer")

	green := []service.ContainerState{{Name: "paas-web-s1-green", HealthAnswer: &service.HealthAnswer{Status: 503, Millis: 40}}}
	m.recordHealth("s1", green, t0.Add(4*time.Hour))
	a, _ = m.LastHealthCheck("s1")
	assert.Equal(t, 503, a.Status)
	assert.Equal(t, t0.Add(4*time.Hour), a.Since, "a new container starts over")

	m.recordHealth("s2", []service.ContainerState{{Name: "x"}}, t0)
	_, ok = m.LastHealthCheck("s2")
	assert.False(t, ok, "a container the probe did not run in has no answer")
}
