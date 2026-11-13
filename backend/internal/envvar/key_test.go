package envvar

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestValidKey(t *testing.T) {
	for _, k := range []string{"A", "_A", "DATABASE_URL", "a1_b2", "NEXT_PUBLIC_X"} {
		assert.True(t, ValidKey(k), k)
	}
	for _, k := range []string{"", "1A", "MY-VAR", "A B", "A=B", "A\nB", "A'B", "ÄB"} {
		assert.False(t, ValidKey(k), k)
	}
}

type fakeRepo struct {
	Repository
	bulk, upserts, secrets int
}

func (f *fakeRepo) Upsert(_ context.Context, _, _ string, in UpsertInput) (*EnvVar, error) {
	f.upserts++
	return &EnvVar{Key: in.Key}, nil
}

func (f *fakeRepo) BulkUpsert(_ context.Context, _, _, _ string, vars []UpsertInput) ([]EnvVar, error) {
	f.bulk++
	return nil, nil
}

func (f *fakeRepo) CreateSecret(_ context.Context, _, _ string, in CreateSecretInput) (*SecretDTO, error) {
	f.secrets++
	return &SecretDTO{Key: in.Key}, nil
}

func TestService_RejectsInvalidKeys(t *testing.T) {
	repo := &fakeRepo{}
	s := NewService(repo, zap.NewNop())
	ctx := context.Background()

	_, err := s.Upsert(ctx, "s", "u", UpsertInput{Key: "BAD KEY", Value: "v"})
	assert.ErrorIs(t, err, ErrInvalidKey)
	_, _, err = s.BulkUpsert(ctx, "s", "u", "v", []UpsertInput{{Key: "OK", Value: "1"}, {Key: "A\nCOMPOSE_PROFILES", Value: "x"}})
	assert.ErrorIs(t, err, ErrInvalidKey)
	_, err = s.CreateSecret(ctx, "s", "u", CreateSecretInput{Key: "1PASSWORD", Value: "x"})
	assert.ErrorIs(t, err, ErrInvalidKey)
	assert.Zero(t, repo.upserts+repo.bulk+repo.secrets)

	_, _, err = s.BulkUpsert(ctx, "s", "u", "v", []UpsertInput{{Key: "DATABASE_URL", Value: "x"}})
	assert.NoError(t, err)
	assert.Equal(t, 1, repo.bulk)
}
