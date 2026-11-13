package composespec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every rule says why it exists and what to do instead, and a violation's
// JSON carries that text, so every client explains it the same way.
func TestEveryRuleIsExplained(t *testing.T) {
	for _, info := range Rules() {
		assert.NotEmpty(t, info.Description, info.Rule)
		assert.NotEmpty(t, info.Why, info.Rule)
		assert.NotEmpty(t, info.Fix, info.Rule)
	}

	b, err := json.Marshal(Violation{Rule: RuleHostBindMount, Field: "volumes[0].host_path", Message: "volume 1 mounts the server folder /srv"})
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	assert.Equal(t, "host_bind_mount", got["rule"])
	assert.Equal(t, "volumes[0].host_path", got["field"])
	assert.Equal(t, "Bind mounts of host paths", got["title"])
	assert.Contains(t, got["why"], "folder of the server")
	assert.Contains(t, got["fix"], "named volume")
	assert.Equal(t, true, got["grantable"])

	b, _ = json.Marshal([]Violation{{Rule: RuleReservedLabel}})
	assert.Contains(t, string(b), `"grantable":false`)
}
