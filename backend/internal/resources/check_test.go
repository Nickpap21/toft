package resources

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Nickpap21/toft/backend/internal/composespec"
)

var t0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func TestCheckMemory_SingleWithLimit(t *testing.T) {
	vs, ws := CheckMemory(MemoryCheck{SingleLimit: "512m"}, t0)
	assert.Empty(t, vs)
	assert.Empty(t, ws)
}

func TestCheckMemory_SingleMissing(t *testing.T) {
	vs, ws := CheckMemory(MemoryCheck{Suggested: map[string]SuggestedLimit{"": {Value: "768m", FromUsage: true}}}, t0)
	assert.Empty(t, ws)
	require.Len(t, vs, 1)
	assert.Equal(t, composespec.RuleMemoryLimit, vs[0].Rule)
	assert.Equal(t, "memory_limit", vs[0].Field)
	assert.Contains(t, vs[0].Message, "set it to 768m, the suggestion from its usage")

	vs, _ = CheckMemory(MemoryCheck{Suggested: map[string]SuggestedLimit{"": {Value: "1g"}}}, t0)
	require.Len(t, vs, 1)
	assert.Contains(t, vs[0].Message, "set it to 1g, the default for its kind")
	assert.NotContains(t, vs[0].Message, "usage", "no usage was measured")

	vs, _ = CheckMemory(MemoryCheck{}, t0)
	require.Len(t, vs, 1)
	assert.NotContains(t, vs[0].Message, "suggestion")
}

func TestCheckMemory_Grace(t *testing.T) {
	grace := t0.Add(24 * time.Hour)
	vs, ws := CheckMemory(MemoryCheck{GraceUntil: &grace}, t0)
	assert.Empty(t, vs)
	require.Len(t, ws, 1)
	assert.Contains(t, ws[0], "13 Oct 2026")

	over := t0.Add(-time.Second)
	vs, ws = CheckMemory(MemoryCheck{GraceUntil: &over}, t0)
	assert.Len(t, vs, 1)
	assert.Empty(t, ws)

	now := t0
	vs, ws = CheckMemory(MemoryCheck{GraceUntil: &now}, t0)
	assert.Len(t, vs, 1, "grace == now is the first day it is required")
	assert.Empty(t, ws)
}

// In grace the warning offers only a value from usage: the service is being
// measured, and its kind's default is often far above what it uses. After
// grace the refusal offers the default too, so it has one click.
func TestCheckMemory_GraceHintOnlyFromUsage(t *testing.T) {
	grace := t0.Add(24 * time.Hour)
	def := map[string]SuggestedLimit{"": {Value: "512m"}}
	_, ws := CheckMemory(MemoryCheck{GraceUntil: &grace, Suggested: def}, t0)
	require.Len(t, ws, 1)
	assert.NotContains(t, ws[0], "512m", "no default offered in grace")

	used := map[string]SuggestedLimit{"": {Value: "128m", FromUsage: true}}
	_, ws = CheckMemory(MemoryCheck{GraceUntil: &grace, Suggested: used}, t0)
	require.Len(t, ws, 1)
	assert.Contains(t, ws[0], "set it to 128m, the suggestion from its usage")

	over := t0.Add(-time.Second)
	vs, _ := CheckMemory(MemoryCheck{GraceUntil: &over, Suggested: def}, t0)
	require.Len(t, vs, 1)
	assert.Contains(t, vs[0].Message, "set it to 512m, the default for its kind")
}

func TestCheckMemory_Compose(t *testing.T) {
	vs, _ := CheckMemory(MemoryCheck{Compose: true, ComposeServices: map[string]ComposeLimitSource{
		"db": {FileMB: 512}, "app": {PanelMB: 256}, "worker": {},
	}}, t0)
	require.Len(t, vs, 1)
	assert.Equal(t, "worker", vs[0].Service)
	assert.Equal(t, "services.worker.mem_limit", vs[0].Field)

	vs, ws := CheckMemory(MemoryCheck{Compose: true}, t0)
	assert.Empty(t, vs, "a repository file at save time: nothing to check yet")
	assert.Empty(t, ws)
}
