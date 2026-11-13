package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
)

// hashKeySetting holds the instance's HMAC key, sealed, in
// instance_settings: it keys the deploy policy's fingerprints and the agent
// approvals' request hash, so neither can be checked against a guessed
// secret. Random rather than derived from ENCRYPTION_KEY, so a rotation
// changes no fingerprint (D-126).
const hashKeySetting = "hmac_key"

var hashKeyAD = crypto.AD("instance_settings.value", hashKeySetting)

// instanceHashKey reads the instance's HMAC key, or makes one on the first
// start.
func instanceHashKey(ctx context.Context, settings settingStore, keys *crypto.Keys) ([]byte, error) {
	var stored struct {
		Key string `json:"key"`
	}
	raw, err := settings.GetSetting(ctx, hashKeySetting)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &stored); err != nil {
			return nil, fmt.Errorf("decode the HMAC key: %w", err)
		}
		// Either format: it runs before the reseal pass converts it.
		plain, err := keys.OpenLegacy(hashKeyAD, stored.Key)
		if err != nil {
			// Every other secret is unreadable too: refuse to start, and say why.
			return nil, fmt.Errorf("decrypt the HMAC key: ENCRYPTION_KEY isn't the key this database was encrypted with (the recovery kit has it): %w", err)
		}
		return hex.DecodeString(plain)
	case !errors.Is(err, instance.ErrNotFound):
		return nil, fmt.Errorf("read the HMAC key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate the HMAC key: %w", err)
	}
	if stored.Key, err = keys.Seal(hashKeyAD, hex.EncodeToString(key)); err != nil {
		return nil, fmt.Errorf("encrypt the HMAC key: %w", err)
	}
	value, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	if err := settings.MergeSetting(ctx, hashKeySetting, value, ""); err != nil {
		return nil, fmt.Errorf("store the HMAC key: %w", err)
	}
	return key, nil
}
