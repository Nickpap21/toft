package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/reseal"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
)

type memSettings map[string][]byte

func (m memSettings) GetSetting(_ context.Context, key string) ([]byte, error) {
	if v, ok := m[key]; ok {
		return v, nil
	}
	return nil, instance.ErrNotFound
}

func (m memSettings) MergeSetting(_ context.Context, key string, value []byte, _ string) error {
	m[key] = value
	return nil
}

// The instance keys, sealed in v1 (D-126), are made on the first start and
// read back on the next.
func TestInstanceKeysRoundTripInV1(t *testing.T) {
	ctx := context.Background()
	keys := crypto.NewKeys("test-encryption-key", "")
	settings := memSettings{}

	made, err := instanceHashKey(ctx, settings, keys)
	if err != nil || len(made) != 32 {
		t.Fatalf("instanceHashKey = %x, %v", made, err)
	}
	read, err := instanceHashKey(ctx, settings, keys)
	if err != nil || !bytes.Equal(made, read) {
		t.Fatalf("read back %x, %v; made %x", read, err, made)
	}

	signer, err := gitHostKey(ctx, settings, keys)
	if err != nil {
		t.Fatal(err)
	}
	again, err := gitHostKey(ctx, settings, keys)
	if err != nil || !bytes.Equal(signer.PublicKey().Marshal(), again.PublicKey().Marshal()) {
		t.Fatalf("git host key changed: %v", err)
	}
	if !bytes.Contains(settings[hashKeySetting], []byte(`"v1.`)) || !bytes.Contains(settings[gitHostKeySetting], []byte(`"v1.`)) {
		t.Fatalf("not sealed in v1: %s", settings)
	}
}

// A rotation's reseal pass carries the instance keys over to the new key:
// it seals them for the same place they are read at.
func TestInstanceKeysSurviveARotation(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	settings := instance.NewPostgresRepository(db)
	const keyA, keyB = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	made, err := instanceHashKey(ctx, settings, crypto.NewKeys(keyA, ""))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gitHostKey(ctx, settings, crypto.NewKeys(keyA, ""))
	if err != nil {
		t.Fatal(err)
	}

	if res, err := reseal.Run(ctx, sqlcdb.New(db), crypto.NewKeys(keyB, keyA), false); err != nil || res.Resealed != 2 || len(res.Lost) != 0 {
		t.Fatalf("reseal = %+v, %v", res, err)
	}
	keys := crypto.NewKeys(keyB, "")
	if read, err := instanceHashKey(ctx, settings, keys); err != nil || !bytes.Equal(made, read) {
		t.Fatalf("HMAC key after the rotation = %x, %v; made %x", read, err, made)
	}
	if again, err := gitHostKey(ctx, settings, keys); err != nil || !bytes.Equal(signer.PublicKey().Marshal(), again.PublicKey().Marshal()) {
		t.Fatalf("git host key after the rotation: %v", err)
	}
}

// The HMAC key is the check that ENCRYPTION_KEY is this database's, and runs
// before the reseal pass: it opens in the format v0.27.0 wrote, and refuses
// another key.
func TestInstanceHashKeyOpensTheOldFormatAndRefusesAnotherKey(t *testing.T) {
	ctx := context.Background()
	old, err := crypto.Encrypt("test-encryption-key", "00112233")
	if err != nil {
		t.Fatal(err)
	}
	settings := memSettings{hashKeySetting: []byte(`{"key":"` + old + `"}`)}
	if read, err := instanceHashKey(ctx, settings, crypto.NewKeys("test-encryption-key", "")); err != nil || !bytes.Equal(read, []byte{0x00, 0x11, 0x22, 0x33}) {
		t.Fatalf("old format = %x, %v", read, err)
	}
	if _, err := instanceHashKey(ctx, settings, crypto.NewKeys("another-encryption-key", "")); err == nil {
		t.Fatal("another key opened the HMAC key")
	}
}
