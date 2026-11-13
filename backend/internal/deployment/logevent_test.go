package deployment

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogEventIsJSONWhateverTheLineHolds(t *testing.T) {
	entry := Log{
		ID:           7,
		DeploymentID: "dep-1",
		Level:        "info",
		Message:      "\x1b[32mStep 1/4\x1b[0m \"quoted\" \\ back\nnext line \t é \x00",
		CreatedAt:    time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC),
	}
	event, err := logEvent(entry)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(event, "id: 7\ndata: "), event)
	require.True(t, strings.HasSuffix(event, "\n\n"), event)
	data := strings.TrimSuffix(strings.TrimPrefix(event, "id: 7\ndata: "), "\n\n")
	assert.NotContains(t, data, "\n", "one data line, or the client splits the event")

	var got Log
	require.NoError(t, json.Unmarshal([]byte(data), &got), data)
	assert.Equal(t, entry.Message, got.Message)
	assert.Equal(t, entry.ID, got.ID)
	assert.Equal(t, entry.DeploymentID, got.DeploymentID)
	assert.True(t, entry.CreatedAt.Equal(got.CreatedAt))
}
