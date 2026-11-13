package webhook

import (
	"context"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memRepo keeps encrypted secrets, as the table does.
type memRepo struct{ rows map[string]string }

func (m *memRepo) Secret(_ context.Context, projectID string) (string, error) {
	if s, ok := m.rows[projectID]; ok {
		return s, nil
	}
	return "", ErrUnknownWebhook
}

func (m *memRepo) Ensure(_ context.Context, projectID, encrypted string) (string, error) {
	if _, ok := m.rows[projectID]; !ok {
		m.rows[projectID] = encrypted
	}
	return m.rows[projectID], nil
}

func (m *memRepo) Replace(_ context.Context, projectID, encrypted string) error {
	m.rows[projectID] = encrypted
	return nil
}

func (m *memRepo) LastPush(context.Context, string) (LastPush, error) { return LastPush{}, nil }
func (m *memRepo) MarkPushed(context.Context, string, string) error   { return nil }

func TestSecrets(t *testing.T) {
	repo := &memRepo{rows: map[string]string{}}
	s := NewSecrets(repo, crypto.NewKeys(strings.Repeat("k", 32), ""))
	ctx := context.Background()

	_, err := s.Get(ctx, projectA)
	assert.ErrorIs(t, err, ErrUnknownWebhook, "no secret until someone opens the webhook")

	first, err := s.Ensure(ctx, projectA)
	require.NoError(t, err)
	assert.Len(t, first, 64)
	assert.NotContains(t, repo.rows[projectA], first, "stored encrypted")

	again, err := s.Ensure(ctx, projectA)
	require.NoError(t, err)
	assert.Equal(t, first, again, "opening it again shows the same secret")
	got, err := s.Get(ctx, projectA)
	require.NoError(t, err)
	assert.Equal(t, first, got)

	rotated, err := s.Rotate(ctx, projectA)
	require.NoError(t, err)
	assert.NotEqual(t, first, rotated)
	got, _ = s.Get(ctx, projectA)
	assert.Equal(t, rotated, got)

	other, _ := s.Ensure(ctx, projectB)
	assert.NotEqual(t, rotated, other, "each project has its own")
}
